package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hiabhi-cpu/DPDP/consent-service/model"
	sharedcrypto "github.com/hiabhi-cpu/DPDP/shared/crypto"
	sharedsecrets "github.com/hiabhi-cpu/DPDP/shared/secrets"
)

// ConsentService is the core business logic for consent capture, check, and withdrawal.
type ConsentService struct {
	db              *pgxpool.Pool
	secretsProvider sharedsecrets.Provider
	auditServiceURL string
	httpClient      *http.Client
}

// NewConsentService creates a ConsentService.
func NewConsentService(db *pgxpool.Pool, secretsProvider sharedsecrets.Provider, auditServiceURL string) *ConsentService {
	return &ConsentService{
		db:              db,
		secretsProvider: secretsProvider,
		auditServiceURL: auditServiceURL,
		httpClient:      &http.Client{Timeout: 5 * time.Second},
	}
}

// CaptureRequest contains all data needed to create a consent artifact.
type CaptureRequest struct {
	HospitalID    string
	Mobile        string   // discarded immediately after hashing
	HMSPatientID  string
	Purposes      []string
	Language      string
	NoticeVersion string
	OTPVerified   bool
	KioskID       string
	RequestID     string
	ActorIP       string
}

// Capture creates a new consent artifact in the vault.
// Mobile number is hashed immediately — discarded by end of function.
func (s *ConsentService) Capture(ctx context.Context, req CaptureRequest) (*model.ConsentArtifact, error) {
	// Step 1: Fetch secrets, compute patient key, discard mobile
	salt, err := s.secretsProvider.GetSystemSalt(ctx)
	if err != nil {
		return nil, fmt.Errorf("consent: get salt: %w", err)
	}
	hospitalKey, err := s.secretsProvider.GetHospitalKey(ctx, req.HospitalID)
	if err != nil {
		return nil, fmt.Errorf("consent: get hospital key: %w", err)
	}
	patientKey := sharedcrypto.ComputePatientKey(req.Mobile, salt, hospitalKey)
	req.Mobile = "" // ← raw mobile explicitly discarded

	// Step 2: Check idempotency — existing active consent for this patient+hospital?
	existing, err := s.findActiveConsent(ctx, req.HospitalID, patientKey)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		// Return existing — idempotent, don't create duplicate
		return existing, nil
	}

	// Step 3: Build consent artifact
	artifactID := uuid.New().String()
	now := time.Now().UTC()
	purposesJSON, _ := json.Marshal(req.Purposes)

	// Compute tamper-evident hash of all fields
	artifactHash := computeArtifactHash(artifactID, req.HospitalID, patientKey,
		req.HMSPatientID, req.Purposes, req.Language, req.NoticeVersion, now)

	// Step 4: Write to DB inside RLS transaction
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("consent: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Activate RLS for this hospital
	if _, err := tx.Exec(ctx, "SET LOCAL app.hospital_id = $1", req.HospitalID); err != nil {
		return nil, fmt.Errorf("consent: set rls: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO consent.consent_vault (
			id, hospital_id, patient_key, hms_patient_id,
			type, status, legal_basis, purposes,
			notice_version, language, otp_verified, kiosk_id,
			version, artifact_hash, created_at
		) VALUES (
			$1,$2,$3,$4, 'CONSENT_GIVEN','ACTIVE','EXPLICIT_CONSENT',$5::jsonb,
			$6,$7,$8,$9, 1,$10,$11
		)`,
		artifactID, req.HospitalID, patientKey, req.HMSPatientID,
		string(purposesJSON),
		req.NoticeVersion, req.Language, req.OTPVerified, req.KioskID,
		artifactHash, now,
	)
	if err != nil {
		return nil, fmt.Errorf("consent: insert artifact: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("consent: commit: %w", err)
	}

	artifact := &model.ConsentArtifact{
		ID: artifactID, HospitalID: req.HospitalID, PatientKey: patientKey,
		HMSPatientID: req.HMSPatientID, Type: "CONSENT_GIVEN", Status: "ACTIVE",
		LegalBasis: "EXPLICIT_CONSENT", Purposes: req.Purposes,
		NoticeVersion: req.NoticeVersion, Language: req.Language,
		OTPVerified: req.OTPVerified, KioskID: req.KioskID,
		Version: 1, ArtifactHash: artifactHash, CreatedAt: now,
	}

	// Step 5: Async audit log — never blocks the response
	go s.logAuditEvent(context.Background(), map[string]any{
		"hospital_id": req.HospitalID,
		"event_type":  "CONSENT_GRANTED",
		"actor_type":  "KIOSK",
		"patient_key": patientKey,
		"consent_id":  artifactID,
		"request_id":  req.RequestID,
		"ip_address":  req.ActorIP,
		"details":     map[string]any{"purposes": req.Purposes, "language": req.Language},
	})

	return artifact, nil
}

// CheckRequest is the input for a consent gate check.
type CheckRequest struct {
	HospitalID   string
	HMSPatientID string
	Purpose      string
	DoctorID     string
	RequestID    string
	ActorIP      string
}

// Check returns whether a patient has active consent for the requested purpose.
// Always logs DATA_ACCESSED (or CONSENT_MISSING_ACCESS_ATTEMPT) to audit.
func (s *ConsentService) Check(ctx context.Context, req CheckRequest) (*model.ConsentStatus, error) {
	// Look up by hms_patient_id first, then filter by purpose
	row := s.db.QueryRow(ctx, `
		SELECT id, status, purposes, created_at
		FROM consent.consent_vault
		WHERE hospital_id = $1
		  AND hms_patient_id = $2
		  AND status = 'ACTIVE'
		  AND type = 'CONSENT_GIVEN'
		ORDER BY created_at DESC
		LIMIT 1
	`, req.HospitalID, req.HMSPatientID)

	var consentID, status string
	var purposesJSON []byte
	var capturedAt time.Time

	if err := row.Scan(&consentID, &status, &purposesJSON, &capturedAt); err != nil {
		// No active consent found
		go s.logAuditEvent(context.Background(), map[string]any{
			"hospital_id": req.HospitalID, "event_type": "CONSENT_MISSING_ACCESS_ATTEMPT",
			"actor_id": req.DoctorID, "actor_type": "DOCTOR",
			"request_id": req.RequestID, "ip_address": req.ActorIP,
		})
		return &model.ConsentStatus{Allowed: false, Reason: "no_active_consent"}, nil
	}

	var purposes []string
	_ = json.Unmarshal(purposesJSON, &purposes)

	// Check if requested purpose is covered
	purposeCovered := req.Purpose == "" // empty purpose = check any consent
	for _, p := range purposes {
		if strings.EqualFold(p, req.Purpose) {
			purposeCovered = true
			break
		}
	}

	eventType := "DATA_ACCESSED"
	allowed := purposeCovered
	reason := ""
	if !purposeCovered {
		eventType = "CONSENT_MISSING_ACCESS_ATTEMPT"
		reason = "purpose_not_covered"
	}

	go s.logAuditEvent(context.Background(), map[string]any{
		"hospital_id": req.HospitalID, "event_type": eventType,
		"actor_id": req.DoctorID, "actor_type": "DOCTOR",
		"consent_id": consentID, "request_id": req.RequestID, "ip_address": req.ActorIP,
		"details": map[string]any{"requested_purpose": req.Purpose},
	})

	return &model.ConsentStatus{
		Allowed:    allowed,
		Reason:     reason,
		ConsentID:  consentID,
		Purposes:   purposes,
		CapturedAt: &capturedAt,
	}, nil
}

// WithdrawRequest is the input for consent withdrawal.
type WithdrawRequest struct {
	HospitalID   string
	Mobile       string
	HMSPatientID string
	Purposes     []string // specific purposes to withdraw; empty = withdraw all
	RequestID    string
	ActorIP      string
}

// Withdraw creates a WITHDRAWAL record (never modifies existing consent).
func (s *ConsentService) Withdraw(ctx context.Context, req WithdrawRequest) error {
	salt, _ := s.secretsProvider.GetSystemSalt(ctx)
	hospitalKey, _ := s.secretsProvider.GetHospitalKey(ctx, req.HospitalID)
	patientKey := sharedcrypto.ComputePatientKey(req.Mobile, salt, hospitalKey)
	req.Mobile = "" // discard

	// Find most recent active consent to link as previous_id
	var previousID string
	_ = s.db.QueryRow(ctx, `
		SELECT id FROM consent.consent_vault
		WHERE hospital_id = $1 AND patient_key = $2 AND status = 'ACTIVE'
		ORDER BY created_at DESC LIMIT 1
	`, req.HospitalID, patientKey).Scan(&previousID)

	withdrawalID := uuid.New().String()
	now := time.Now().UTC()
	purposesJSON, _ := json.Marshal(req.Purposes)
	artifactHash := computeArtifactHash(withdrawalID, req.HospitalID, patientKey,
		req.HMSPatientID, req.Purposes, "", "", now)

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, "SET LOCAL app.hospital_id = $1", req.HospitalID); err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO consent.consent_vault (
			id, hospital_id, patient_key, hms_patient_id,
			type, status, legal_basis, purposes,
			notice_version, language, otp_verified,
			previous_id, version, artifact_hash, created_at
		) VALUES (
			$1,$2,$3,$4, 'WITHDRAWAL','WITHDRAWN','EXPLICIT_CONSENT',$5::jsonb,
			'','',false,
			NULLIF($6,'')::uuid, 2, $7, $8
		)`,
		withdrawalID, req.HospitalID, patientKey, req.HMSPatientID,
		string(purposesJSON), previousID, artifactHash, now,
	)
	if err != nil {
		return fmt.Errorf("consent: insert withdrawal: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	go s.logAuditEvent(context.Background(), map[string]any{
		"hospital_id": req.HospitalID, "event_type": "CONSENT_WITHDRAWN",
		"actor_type": "PATIENT", "patient_key": patientKey,
		"consent_id": withdrawalID, "request_id": req.RequestID,
		"details": map[string]any{"purposes_withdrawn": req.Purposes},
	})

	return nil
}

// findActiveConsent returns the most recent ACTIVE consent for a patient at a hospital.
func (s *ConsentService) findActiveConsent(ctx context.Context, hospitalID, patientKey string) (*model.ConsentArtifact, error) {
	row := s.db.QueryRow(ctx, `
		SELECT id, type, status, purposes, notice_version, language, otp_verified,
		       kiosk_id, version, artifact_hash, created_at, hms_patient_id
		FROM consent.consent_vault
		WHERE hospital_id = $1 AND patient_key = $2 AND status = 'ACTIVE'
		ORDER BY created_at DESC LIMIT 1
	`, hospitalID, patientKey)

	var a model.ConsentArtifact
	var purposesJSON []byte
	err := row.Scan(&a.ID, &a.Type, &a.Status, &purposesJSON, &a.NoticeVersion,
		&a.Language, &a.OTPVerified, &a.KioskID, &a.Version, &a.ArtifactHash,
		&a.CreatedAt, &a.HMSPatientID)
	if err != nil {
		return nil, nil // not found
	}
	_ = json.Unmarshal(purposesJSON, &a.Purposes)
	a.HospitalID = hospitalID
	a.PatientKey = patientKey
	return &a, nil
}

// computeArtifactHash computes SHA256 over the key consent fields for tamper evidence.
func computeArtifactHash(id, hospitalID, patientKey, hmsID string, purposes []string, language, noticeVersion string, ts time.Time) string {
	purposesStr := strings.Join(purposes, ",")
	raw := strings.Join([]string{id, hospitalID, patientKey, hmsID, purposesStr, language, noticeVersion, ts.UTC().Format(time.RFC3339Nano)}, "|")
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// logAuditEvent calls audit-service asynchronously. Never panics.
func (s *ConsentService) logAuditEvent(ctx context.Context, payload map[string]any) {
	if s.auditServiceURL == "" {
		return
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		s.auditServiceURL+"/internal/audit/log", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}
