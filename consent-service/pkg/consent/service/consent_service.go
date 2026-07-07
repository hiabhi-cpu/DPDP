package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hiabhi-cpu/consent-service/pkg/consent/model"
	"github.com/hiabhi-cpu/consent-service/pkg/consent/repository"
	sharedcrypto "github.com/hiabhi-cpu/shared/crypto"
	"github.com/hiabhi-cpu/shared/secrets"
)

// Sentinel errors — returned by the service, mapped to HTTP status in the
// controller via errors.Is. Never match on err.Error() strings.
var (
	ErrActiveConsentExists = errors.New("active consent already exists")
	ErrNoActiveConsent     = errors.New("no active consent found")
	// ErrNoConsentToRenew — grant requires an existing consent chain to extend;
	// a first-time consent must go through Capture.
	ErrNoConsentToRenew = errors.New("no existing consent to grant onto")
)

// Check reasons (plan §11): distinguish never-granted from withdrawn.
const (
	reasonNoConsent        = "no_consent"
	reasonConsentWithdrawn = "consent_withdrawn"
)

// ConsentService defines the business logic contract for consent operations.
type ConsentService interface {
	// Capture returns the consent and created=true for a new record, or the
	// existing record with created=false for an idempotent replay (same session_id).
	Capture(ctx context.Context, hospitalID, ip string, req *model.CaptureConsentRequest) (consent *model.Consent, created bool, err error)
	// Check answers whether a specific purpose is currently allowed for a patient.
	Check(ctx context.Context, hospitalID, ip string, req *model.CheckConsentRequest) (*model.CheckConsentResponse, error)
	Withdraw(ctx context.Context, hospitalID, ip string, req *model.WithdrawConsentRequest) error
	// Grant extends an existing chain to (re-)grant purposes. changed=false means
	// every requested purpose was already active (no new row written).
	Grant(ctx context.Context, hospitalID, ip string, req *model.GrantConsentRequest) (consent *model.Consent, changed bool, err error)
}

type consentService struct {
	repo            repository.ConsentRepository
	secretsProvider secrets.Provider
}

// NewConsentService creates a ConsentService. Audit events are written to the
// transactional outbox (via repo) and shipped asynchronously by the relay — the
// service no longer calls audit-service directly.
func NewConsentService(repo repository.ConsentRepository, sp secrets.Provider) ConsentService {
	return &consentService{
		repo:            repo,
		secretsProvider: sp,
	}
}

// patientKeyFor computes the hospital-scoped patient key, surfacing secret-fetch
// errors instead of silently deriving a wrong key.
func (s *consentService) patientKeyFor(ctx context.Context, hospitalID, mobile string) (string, error) {
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

// buildOutbox serializes an audit event into an outbox record. The record ID is
// the event's EventID, which becomes audit_log.event_id for idempotency.
func buildOutbox(event AuditEvent) (*model.OutboxRecord, error) {
	payload, err := json.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal audit event: %w", err)
	}
	return &model.OutboxRecord{ID: event.EventID, Payload: payload}, nil
}

func (s *consentService) Capture(ctx context.Context, hospitalID, ip string, req *model.CaptureConsentRequest) (*model.Consent, bool, error) {
	patientKey, err := s.patientKeyFor(ctx, hospitalID, req.Mobile)
	if err != nil {
		return nil, false, fmt.Errorf("ConsentService.Capture: %w", err)
	}

	// Idempotent replay: a capture with a session_id we've already recorded
	// returns the original record rather than erroring or duplicating.
	if req.SessionID != "" {
		if prev, err := s.repo.GetByIdempotencyKey(ctx, hospitalID, req.SessionID); err != nil {
			return nil, false, fmt.Errorf("ConsentService.Capture: %w", err)
		} else if prev != nil {
			return prev, false, nil
		}
	}

	// Re-grant of a withdrawn purpose is out of scope for now: capture is blocked
	// only while some purpose is still active. After a full withdrawal (no active
	// purposes) a fresh capture is allowed.
	existing, err := s.repo.GetLatestByPatientKey(ctx, hospitalID, patientKey)
	if err != nil {
		return nil, false, fmt.Errorf("ConsentService.Capture: failed to check existing: %w", err)
	}
	if existing != nil && existing.AnyActive() {
		return nil, false, ErrActiveConsentExists
	}

	// Build the per-purpose status map — every requested purpose starts ACTIVE.
	purposes := make(map[string]model.PurposeState, len(req.Purposes))
	for _, p := range req.Purposes {
		purposes[p] = model.PurposeActive
	}

	consentID := uuid.New()
	purposesStr := fmt.Sprintf("%v", req.Purposes)
	artifactHash := sharedcrypto.ComputeArtifactHash(
		consentID.String(),
		hospitalID,
		patientKey,
		purposesStr,
		time.Now().Format(time.RFC3339),
	)

	consent := &model.Consent{
		ID:             consentID,
		HospitalID:     hospitalID,
		PatientKey:     patientKey,
		HMSPatientID:   req.HMSPatientID,
		Type:           model.TypeConsentGiven,
		Status:         model.StatusActive,
		Purposes:       purposes,
		ArtifactHash:   artifactHash,
		IdempotencyKey: req.SessionID,
	}

	// actor_id is the hashed patient key — never the raw mobile.
	outbox, err := buildOutbox(AuditEvent{
		EventID:    uuid.New(),
		HospitalID: hospitalID,
		EventType:  "CONSENT_GRANTED",
		ActorID:    patientKey,
		ActorType:  "PATIENT",
		PatientKey: patientKey,
		ConsentID:  &consentID,
		RequestID:  uuid.New(),
		IPAddress:  ip,
		Details:    map[string]any{"purposes": req.Purposes, "session_id": req.SessionID},
	})
	if err != nil {
		return nil, false, fmt.Errorf("ConsentService.Capture: %w", err)
	}

	if err := s.repo.Insert(ctx, consent, outbox); err != nil {
		// Lost a race with a concurrent identical capture — return the winner.
		if errors.Is(err, repository.ErrDuplicateIdempotency) && req.SessionID != "" {
			if prev, ferr := s.repo.GetByIdempotencyKey(ctx, hospitalID, req.SessionID); ferr == nil && prev != nil {
				return prev, false, nil
			}
		}
		return nil, false, fmt.Errorf("ConsentService.Capture: %w", err)
	}

	return consent, true, nil
}

func (s *consentService) Check(ctx context.Context, hospitalID, ip string, req *model.CheckConsentRequest) (*model.CheckConsentResponse, error) {
	// Resolve the patient's latest row by HMS ID (doctor/HMS path — no raw mobile
	// needed) or by mobile (kiosk/portal path). The controller guarantees exactly
	// one is set. patientKey is used only for the audit record.
	var (
		latest     *model.Consent
		patientKey string
		err        error
	)
	if req.HMSPatientID != "" {
		latest, err = s.repo.GetLatestByHMSPatientID(ctx, hospitalID, req.HMSPatientID)
		if err != nil {
			return nil, fmt.Errorf("ConsentService.Check: %w", err)
		}
		if latest != nil {
			patientKey = latest.PatientKey
		}
	} else {
		patientKey, err = s.patientKeyFor(ctx, hospitalID, req.Mobile)
		if err != nil {
			return nil, fmt.Errorf("ConsentService.Check: %w", err)
		}
		latest, err = s.repo.GetLatestByPatientKey(ctx, hospitalID, patientKey)
		if err != nil {
			return nil, fmt.Errorf("ConsentService.Check: %w", err)
		}
	}

	// Resolve the state of the requested purpose from the latest row's map.
	var resp model.CheckConsentResponse
	switch {
	case latest != nil && latest.Purposes[req.Purpose] == model.PurposeActive:
		resp.Allowed = true
		resp.ConsentID = &latest.ID
	case latest != nil && latest.Purposes[req.Purpose] == model.PurposeWithdrawn:
		resp.Reason = reasonConsentWithdrawn
	default:
		resp.Reason = reasonNoConsent // no history, or purpose never granted
	}

	// DATA_ACCESSED when allowed; otherwise a missing/withdrawn access attempt.
	eventType := "DATA_ACCESSED"
	if !resp.Allowed {
		eventType = "CONSENT_MISSING_ACCESS_ATTEMPT"
	}

	details := map[string]any{"purpose": req.Purpose, "allowed": resp.Allowed, "reason": resp.Reason}
	if req.HMSPatientID != "" {
		details["hms_patient_id"] = req.HMSPatientID
	}

	outbox, err := buildOutbox(AuditEvent{
		EventID:    uuid.New(),
		HospitalID: hospitalID,
		EventType:  eventType,
		ActorID:    "system",
		ActorType:  "SYSTEM",
		PatientKey: patientKey,
		ConsentID:  resp.ConsentID,
		RequestID:  uuid.New(),
		IPAddress:  ip,
		Details:    details,
	})
	if err != nil {
		return nil, fmt.Errorf("ConsentService.Check: %w", err)
	}
	if err := s.repo.EnqueueAudit(ctx, outbox); err != nil {
		return nil, fmt.Errorf("ConsentService.Check: %w", err)
	}

	return &resp, nil
}

func (s *consentService) Withdraw(ctx context.Context, hospitalID, ip string, req *model.WithdrawConsentRequest) error {
	patientKey, err := s.patientKeyFor(ctx, hospitalID, req.Mobile)
	if err != nil {
		return fmt.Errorf("ConsentService.Withdraw: %w", err)
	}

	existing, err := s.repo.GetLatestByPatientKey(ctx, hospitalID, patientKey)
	if err != nil {
		return fmt.Errorf("ConsentService.Withdraw: %w", err)
	}
	if existing == nil || !existing.AnyActive() {
		return ErrNoActiveConsent
	}

	// Determine the target set: the requested purposes, or all currently-active
	// purposes when none are specified (withdraw-all).
	targets := req.Purposes
	if len(targets) == 0 {
		for p, st := range existing.Purposes {
			if st == model.PurposeActive {
				targets = append(targets, p)
			}
		}
	}

	// Carry the current map forward and flip the targeted, currently-active
	// purposes to WITHDRAWN. Only actually-active purposes count as "withdrawn".
	newMap := make(map[string]model.PurposeState, len(existing.Purposes))
	for p, st := range existing.Purposes {
		newMap[p] = st
	}
	var withdrawn []string
	for _, p := range targets {
		if newMap[p] == model.PurposeActive {
			newMap[p] = model.PurposeWithdrawn
			withdrawn = append(withdrawn, p)
		}
	}
	if len(withdrawn) == 0 {
		// Nothing among the requested purposes was active to withdraw.
		return ErrNoActiveConsent
	}

	newID := uuid.New()
	purposesStr := fmt.Sprintf("%v", newMap)
	artifactHash := sharedcrypto.ComputeArtifactHash(
		newID.String(),
		hospitalID,
		patientKey,
		purposesStr,
		time.Now().Format(time.RFC3339),
	)

	withdrawnConsent := &model.Consent{
		ID:           newID,
		HospitalID:   hospitalID,
		PatientKey:   patientKey,
		HMSPatientID: existing.HMSPatientID, // carry forward so latest row stays resolvable by HMS ID
		Type:         model.TypeWithdrawal,
		Purposes:     newMap,
		PreviousID:   &existing.ID,
		Version:      existing.Version + 1,
		ArtifactHash: artifactHash,
	}
	// Aggregate status: still ACTIVE if any purpose remains active (partial
	// withdrawal), else WITHDRAWN.
	withdrawnConsent.Status = withdrawnConsent.AggregateStatus()

	outbox, err := buildOutbox(AuditEvent{
		EventID:    uuid.New(),
		HospitalID: hospitalID,
		EventType:  "CONSENT_WITHDRAWN",
		ActorID:    patientKey,
		ActorType:  "PATIENT",
		PatientKey: patientKey,
		ConsentID:  &newID,
		RequestID:  uuid.New(),
		IPAddress:  ip,
		Details: map[string]any{
			"session_id":         req.SessionID,
			"previous_id":        existing.ID,
			"withdrawn_purposes": withdrawn,
			"remaining_status":   withdrawnConsent.Status,
		},
	})
	if err != nil {
		return fmt.Errorf("ConsentService.Withdraw: %w", err)
	}

	if err := s.repo.InsertWithdrawn(ctx, withdrawnConsent, outbox); err != nil {
		return fmt.Errorf("ConsentService.Withdraw: %w", err)
	}

	return nil
}

// Grant extends an existing consent chain: it re-grants withdrawn purposes and/or
// adds new ones, appending a CONSENT_RENEWAL row. It is the mirror of Withdraw.
// If every requested purpose is already active it is a no-op (changed=false) and
// no row is written — which also makes repeated grants safe (option A idempotency).
func (s *consentService) Grant(ctx context.Context, hospitalID, ip string, req *model.GrantConsentRequest) (*model.Consent, bool, error) {
	patientKey, err := s.patientKeyFor(ctx, hospitalID, req.Mobile)
	if err != nil {
		return nil, false, fmt.Errorf("ConsentService.Grant: %w", err)
	}

	existing, err := s.repo.GetLatestByPatientKey(ctx, hospitalID, patientKey)
	if err != nil {
		return nil, false, fmt.Errorf("ConsentService.Grant: %w", err)
	}
	if existing == nil {
		// No chain to extend — first-time consent must go through Capture.
		return nil, false, ErrNoConsentToRenew
	}

	// Carry the current map forward and flip the requested, not-already-active
	// purposes to ACTIVE.
	newMap := make(map[string]model.PurposeState, len(existing.Purposes)+len(req.Purposes))
	for p, st := range existing.Purposes {
		newMap[p] = st
	}
	var granted []string
	for _, p := range req.Purposes {
		if newMap[p] != model.PurposeActive {
			newMap[p] = model.PurposeActive
			granted = append(granted, p)
		}
	}
	if len(granted) == 0 {
		// Everything requested is already active — no-op, return current state.
		return existing, false, nil
	}

	newID := uuid.New()
	purposesStr := fmt.Sprintf("%v", newMap)
	artifactHash := sharedcrypto.ComputeArtifactHash(
		newID.String(),
		hospitalID,
		patientKey,
		purposesStr,
		time.Now().Format(time.RFC3339),
	)

	renewed := &model.Consent{
		ID:           newID,
		HospitalID:   hospitalID,
		PatientKey:   patientKey,
		HMSPatientID: existing.HMSPatientID, // carry forward
		Type:         model.TypeRenewal,
		Status:       model.StatusActive, // any granted purpose ⇒ ACTIVE
		Purposes:     newMap,
		PreviousID:   &existing.ID,
		Version:      existing.Version + 1,
		ArtifactHash: artifactHash,
	}

	outbox, err := buildOutbox(AuditEvent{
		EventID:    uuid.New(),
		HospitalID: hospitalID,
		EventType:  "CONSENT_GRANTED",
		ActorID:    patientKey,
		ActorType:  "PATIENT",
		PatientKey: patientKey,
		ConsentID:  &newID,
		RequestID:  uuid.New(),
		IPAddress:  ip,
		Details: map[string]any{
			"session_id":       req.SessionID,
			"previous_id":      existing.ID,
			"granted_purposes": granted,
			"renewal":          true,
		},
	})
	if err != nil {
		return nil, false, fmt.Errorf("ConsentService.Grant: %w", err)
	}

	if err := s.repo.InsertRenewal(ctx, renewed, outbox); err != nil {
		return nil, false, fmt.Errorf("ConsentService.Grant: %w", err)
	}

	return renewed, true, nil
}
