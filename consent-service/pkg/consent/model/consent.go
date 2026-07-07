package model

import (
	"time"

	"github.com/google/uuid"
)

type ConsentType string

const (
	TypeConsentGiven ConsentType = "CONSENT_GIVEN"
	TypeWithdrawal   ConsentType = "WITHDRAWAL"
	TypeRenewal      ConsentType = "CONSENT_RENEWAL"
)

type ConsentStatus string

const (
	StatusActive    ConsentStatus = "ACTIVE"
	StatusWithdrawn ConsentStatus = "WITHDRAWN"
)

// PurposeState is the per-purpose consent status. Consent is granular: a patient
// can withdraw one purpose (e.g. "insurance") while another (e.g. "treatment")
// stays active. The withdrawn/absent distinction is legally meaningful, so we
// keep WITHDRAWN explicit rather than just dropping the purpose.
type PurposeState string

const (
	PurposeActive    PurposeState = "ACTIVE"
	PurposeWithdrawn PurposeState = "WITHDRAWN"
)

// Consent represents an immutable row in the consent vault. The latest row for a
// patient is the current state; Purposes is a per-purpose status map (e.g.
// {"treatment":"ACTIVE","insurance":"WITHDRAWN"}). Status is the derived
// aggregate: ACTIVE if any purpose is active, else WITHDRAWN.
type Consent struct {
	ID             uuid.UUID               `json:"id"`
	HospitalID     string                  `json:"hospital_id"`
	PatientKey     string                  `json:"patient_key"`
	HMSPatientID   string                  `json:"hms_patient_id,omitempty"` // opaque HMS ID e.g. "PA-00234"
	Type           ConsentType             `json:"type"`
	Status         ConsentStatus           `json:"status"`
	Purposes       map[string]PurposeState `json:"purposes"`
	CreatedAt      time.Time               `json:"created_at"`
	PreviousID     *uuid.UUID              `json:"previous_id,omitempty"`
	Version        int                     `json:"version"`
	ArtifactHash   string                  `json:"artifact_hash"`
	IdempotencyKey string                  `json:"-"` // capture session_id; dedupes replays
}

// AnyActive reports whether at least one purpose is currently active.
func (c *Consent) AnyActive() bool {
	for _, s := range c.Purposes {
		if s == PurposeActive {
			return true
		}
	}
	return false
}

// AggregateStatus derives the row-level status from the per-purpose map.
func (c *Consent) AggregateStatus() ConsentStatus {
	if c.AnyActive() {
		return StatusActive
	}
	return StatusWithdrawn
}

// OutboxRecord is a pending audit event queued in consent.audit_outbox. It is
// written in the same DB transaction as a consent row (or on its own for reads),
// then shipped to audit-service by the relay. ID is the canonical event id and
// doubles as audit_log.event_id for idempotent at-least-once delivery.
type OutboxRecord struct {
	ID      uuid.UUID
	Payload []byte // marshaled audit event
}

// CaptureConsentRequest is the body for POST /api/v1/consent/capture.
// HMSPatientID is the hospital's own opaque patient ID (from the HMS webhook);
// optional — a kiosk without HMS integration may omit it. Persisting it enables
// non-PII consent checks by HMS ID (plan §11).
type CaptureConsentRequest struct {
	Mobile       string   `json:"mobile" binding:"required,len=10"`
	SessionID    string   `json:"session_id" binding:"required"`
	Purposes     []string `json:"purposes" binding:"required,min=1"`
	HMSPatientID string   `json:"hms_patient_id"`
}

// CheckConsentRequest is the body for POST /api/v1/consent/check. Purpose is
// required — checks are purpose-scoped (plan §11). The patient is identified by
// EITHER hms_patient_id (the doctor/HMS access path — opaque, non-PII) OR mobile
// (kiosk/portal path). Exactly one must be provided; validated in the handler.
// Mobile is only ever in the body, never a URL, so raw mobiles never hit logs.
type CheckConsentRequest struct {
	Mobile       string `json:"mobile"`
	HMSPatientID string `json:"hms_patient_id"`
	Purpose      string `json:"purpose" binding:"required"`
}

// CheckConsentResponse is the response for POST /api/consent/v1/check.
// Reason is "no_consent" (purpose never granted) or "consent_withdrawn"
// (purpose was granted then withdrawn).
type CheckConsentResponse struct {
	Allowed   bool       `json:"allowed"`
	ConsentID *uuid.UUID `json:"consent_id,omitempty"`
	Reason    string     `json:"reason,omitempty"`
}

// WithdrawConsentRequest is the body for POST /api/consent/v1/withdraw.
// Purposes lists which purposes to withdraw; empty means withdraw all currently
// active purposes.
type WithdrawConsentRequest struct {
	Mobile    string   `json:"mobile" binding:"required,len=10"`
	SessionID string   `json:"session_id" binding:"required"`
	Purposes  []string `json:"purposes"`
}

// GrantConsentRequest is the body for POST /api/consent/v1/grant. It extends an
// existing consent chain — re-granting a withdrawn purpose or adding a new one.
// Requires an existing chain; first-time consent uses /capture.
type GrantConsentRequest struct {
	Mobile    string   `json:"mobile" binding:"required,len=10"`
	SessionID string   `json:"session_id" binding:"required"`
	Purposes  []string `json:"purposes" binding:"required,min=1"`
}
