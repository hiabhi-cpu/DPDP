package model

import (
	"time"

	"github.com/google/uuid"
)

// Review status values (emergency.reviews.review_status).
const (
	ReviewPending  = "PENDING"
	ReviewVerified = "VERIFIED" // legitimate emergency — record closed
	ReviewFlagged  = "FLAGGED"  // possible misuse — escalate/investigate
)

// Valid emergency reasons (product plan §12 step 1b). The clinical note is free
// text; the reason is constrained so the DPO queue is filterable.
var ValidReasons = map[string]bool{
	"unconscious":            true, // patient unconscious / unresponsive
	"life_threatening":       true, // life-threatening, immediate access
	"minor_guardian_absent":  true, // minor, guardian unavailable
	"mentally_incapacitated": true, // patient mentally incapacitated
}

// LegalBasisEmergency is the DPDP Act basis for emergency (deemed) consent.
const LegalBasisEmergency = "DPDP_SECTION_7B"

// DPO review deadline window (product plan §12 step 4).
const ReviewWindow = 72 * time.Hour

// EmergencyOverrideRequest is the body of POST /consent/emergency-override.
// Identity fields are optional: an unconscious, unidentified patient still gets
// access, recorded without a patient_key.
type EmergencyOverrideRequest struct {
	HMSPatientID    string `json:"hms_patient_id"`
	Mobile          string `json:"mobile"`
	DoctorID        string `json:"doctor_id" binding:"required"`
	EmergencyReason string `json:"emergency_reason" binding:"required"`
	ClinicalNote    string `json:"clinical_note" binding:"required"`
}

// EmergencyOverrideResponse is returned by the override endpoint. Access is
// ALWAYS allowed — this is never a gate (product plan §7, §12 step 2).
type EmergencyOverrideResponse struct {
	Allowed     bool   `json:"allowed"` // always true
	EmergencyID string `json:"emergency_id"`
	AccessID    string `json:"access_id"`
}

// ReviewItem is one row in the DPO pending-review queue.
type ReviewItem struct {
	AccessID        uuid.UUID  `json:"access_id"`
	EmergencyRef    string     `json:"emergency_id"`
	DoctorID        string     `json:"doctor_id"`
	EmergencyReason string     `json:"emergency_reason"`
	ClinicalNote    string     `json:"clinical_note"`
	HMSPatientID    string     `json:"hms_patient_id,omitempty"`
	ReviewStatus    string     `json:"review_status"`
	DPODeadline     time.Time  `json:"dpo_deadline"`
	Overdue         bool       `json:"overdue"` // derived: PENDING and past deadline
	ReviewedBy      string     `json:"reviewed_by,omitempty"`
	ReviewedAt      *time.Time `json:"reviewed_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

// ReviewDecisionRequest is the body of POST /emergency/:id/review.
type ReviewDecisionRequest struct {
	Decision   string `json:"decision" binding:"required"` // VERIFIED | FLAGGED
	ReviewerID string `json:"reviewer_id" binding:"required"`
}

// AccessRecord is the immutable consent_vault EMERGENCY_OVERRIDE row plus the
// review-queue fields, assembled by the service for one atomic write.
type AccessRecord struct {
	AccessID        uuid.UUID
	HospitalID      string
	EmergencyRef    string
	PatientKey      string // empty when identity unknown → stored NULL
	HMSPatientID    string
	DoctorID        string
	EmergencyReason string
	ClinicalNote    string
	ArtifactHash    string
	DPODeadline     time.Time
}

// OutboxRecord is a pending audit event queued in emergency.audit_outbox.
type OutboxRecord struct {
	ID      uuid.UUID
	Payload []byte
}
