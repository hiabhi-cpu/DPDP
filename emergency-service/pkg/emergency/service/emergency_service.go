package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hiabhi-cpu/emergency-service/pkg/emergency/model"
	"github.com/hiabhi-cpu/emergency-service/pkg/emergency/repository"
	sharedcrypto "github.com/hiabhi-cpu/shared/crypto"
	"github.com/hiabhi-cpu/shared/secrets"
)

// Sentinel errors — mapped to HTTP status in the controller via errors.Is.
var (
	ErrInvalidReason   = errors.New("invalid emergency_reason")
	ErrInvalidDecision = errors.New("decision must be VERIFIED or FLAGGED")
	ErrReviewNotFound  = errors.New("no pending emergency access found for review")
)

const pendingQueueLimit = 200

// EmergencyService is the business-logic contract for emergency access + DPO review.
type EmergencyService interface {
	// Override records an emergency access. It ALWAYS succeeds for a well-formed
	// request — access is deemed consent under §7(b) and is never blocked.
	Override(ctx context.Context, hospitalID, ip string, req *model.EmergencyOverrideRequest) (*model.EmergencyOverrideResponse, error)
	// Pending returns the DPO review queue for a hospital.
	Pending(ctx context.Context, hospitalID string) ([]model.ReviewItem, error)
	// Review records a DPO decision (VERIFIED/FLAGGED) for an emergency access.
	Review(ctx context.Context, hospitalID, ip string, accessID uuid.UUID, req *model.ReviewDecisionRequest) error
}

type emergencyService struct {
	repo            repository.EmergencyRepository
	secretsProvider secrets.Provider
}

// NewEmergencyService builds the service. Audit events go through the
// transactional outbox (via repo) and are shipped by the relay — never on the
// request hot path (the override must stay under 300ms).
func NewEmergencyService(repo repository.EmergencyRepository, sp secrets.Provider) EmergencyService {
	return &emergencyService{repo: repo, secretsProvider: sp}
}

// patientKeyFor computes the hospital-scoped patient key. Returns "" when no
// mobile is supplied (unknown identity) — a valid emergency case.
func (s *emergencyService) patientKeyFor(ctx context.Context, hospitalID, mobile string) (string, error) {
	if mobile == "" {
		return "", nil
	}
	sysSalt, err := s.secretsProvider.GetSystemSalt(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get system salt: %w", err)
	}
	hospKey, err := s.secretsProvider.GetHospitalKey(ctx, hospitalID)
	if err != nil {
		return "", fmt.Errorf("failed to get hospital key: %w", err)
	}
	return sharedcrypto.ComputePatientKey(mobile, sysSalt, hospKey), nil
}

func buildOutbox(event AuditEvent) (*model.OutboxRecord, error) {
	payload, err := json.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal audit event: %w", err)
	}
	return &model.OutboxRecord{ID: event.EventID, Payload: payload}, nil
}

func (s *emergencyService) Override(ctx context.Context, hospitalID, ip string, req *model.EmergencyOverrideRequest) (*model.EmergencyOverrideResponse, error) {
	if !model.ValidReasons[req.EmergencyReason] {
		return nil, ErrInvalidReason
	}

	patientKey, err := s.patientKeyFor(ctx, hospitalID, req.Mobile)
	if err != nil {
		return nil, fmt.Errorf("EmergencyService.Override: %w", err)
	}

	accessID := uuid.New()
	now := time.Now()
	ref := fmt.Sprintf("EMRG-%d-%s", now.Year(), strings.ToUpper(accessID.String()[:8]))
	deadline := now.Add(model.ReviewWindow)

	artifactHash := sharedcrypto.ComputeArtifactHash(
		accessID.String(),
		hospitalID,
		patientKey,
		req.DoctorID,
		req.EmergencyReason,
		now.Format(time.RFC3339),
	)

	rec := &model.AccessRecord{
		AccessID:        accessID,
		HospitalID:      hospitalID,
		EmergencyRef:    ref,
		PatientKey:      patientKey,
		HMSPatientID:    req.HMSPatientID,
		DoctorID:        req.DoctorID,
		EmergencyReason: req.EmergencyReason,
		ClinicalNote:    req.ClinicalNote,
		ArtifactHash:    artifactHash,
		DPODeadline:     deadline,
	}

	outbox, err := buildOutbox(AuditEvent{
		EventID:    uuid.New(),
		HospitalID: hospitalID,
		EventType:  "EMERGENCY_ACCESS",
		ActorID:    req.DoctorID,
		ActorType:  "DOCTOR",
		PatientKey: patientKey, // may be empty (unknown identity)
		ConsentID:  &accessID,
		RequestID:  uuid.New(),
		IPAddress:  ip,
		Details: map[string]any{
			"emergency_ref":    ref,
			"emergency_reason": req.EmergencyReason,
			"hms_patient_id":   req.HMSPatientID,
			"legal_basis":      model.LegalBasisEmergency,
			"dpo_deadline":     deadline.Format(time.RFC3339),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("EmergencyService.Override: %w", err)
	}

	if err := s.repo.InsertAccess(ctx, rec, outbox); err != nil {
		return nil, fmt.Errorf("EmergencyService.Override: %w", err)
	}

	return &model.EmergencyOverrideResponse{
		Allowed:     true,
		EmergencyID: ref,
		AccessID:    accessID.String(),
	}, nil
}

func (s *emergencyService) Pending(ctx context.Context, hospitalID string) ([]model.ReviewItem, error) {
	items, err := s.repo.ListPending(ctx, hospitalID, pendingQueueLimit)
	if err != nil {
		return nil, fmt.Errorf("EmergencyService.Pending: %w", err)
	}
	return items, nil
}

func (s *emergencyService) Review(ctx context.Context, hospitalID, ip string, accessID uuid.UUID, req *model.ReviewDecisionRequest) error {
	decision := strings.ToUpper(strings.TrimSpace(req.Decision))
	if decision != model.ReviewVerified && decision != model.ReviewFlagged {
		return ErrInvalidDecision
	}

	outbox, err := buildOutbox(AuditEvent{
		EventID:    uuid.New(),
		HospitalID: hospitalID,
		EventType:  "DPO_REVIEW_COMPLETED",
		ActorID:    req.ReviewerID,
		ActorType:  "DPO",
		ConsentID:  &accessID,
		RequestID:  uuid.New(),
		IPAddress:  ip,
		Details: map[string]any{
			"access_id": accessID.String(),
			"decision":  decision,
		},
	})
	if err != nil {
		return fmt.Errorf("EmergencyService.Review: %w", err)
	}

	ok, err := s.repo.RecordReview(ctx, hospitalID, accessID, decision, req.ReviewerID, outbox)
	if err != nil {
		return fmt.Errorf("EmergencyService.Review: %w", err)
	}
	if !ok {
		return ErrReviewNotFound
	}
	return nil
}
