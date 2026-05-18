package model

import "time"

// EventType represents a DPDP audit event category.
// These values map 1:1 to the CHECK constraint in 04_audit_log.sql.
type EventType string

const (
	EventConsentGranted          EventType = "CONSENT_GRANTED"
	EventConsentWithdrawn        EventType = "CONSENT_WITHDRAWN"
	EventDataAccessed            EventType = "DATA_ACCESSED"
	EventConsentMissingAttempt   EventType = "CONSENT_MISSING_ACCESS_ATTEMPT"
	EventEmergencyAccess         EventType = "EMERGENCY_ACCESS"
	EventOTPSent                 EventType = "OTP_SENT"
	EventOTPVerified             EventType = "OTP_VERIFIED"
	EventOTPFailed               EventType = "OTP_FAILED"
	EventBypassDetected          EventType = "BYPASS_DETECTED"
	EventBreachReported          EventType = "BREACH_REPORTED"
	EventDPBINotificationSent    EventType = "DPBI_NOTIFICATION_SUBMITTED"
	EventPatientNotified         EventType = "PATIENT_NOTIFIED"
	EventDPOReviewCompleted      EventType = "DPO_REVIEW_COMPLETED"
	EventSystem                  EventType = "SYSTEM_EVENT"
)

// ActorType identifies who triggered an audit event.
type ActorType string

const (
	ActorDoctor  ActorType = "DOCTOR"
	ActorAdmin   ActorType = "ADMIN"
	ActorDPO     ActorType = "DPO"
	ActorPatient ActorType = "PATIENT"
	ActorSystem  ActorType = "SYSTEM"
	ActorKiosk   ActorType = "KIOSK"
)

// AuditEvent is a single immutable event record to be written to audit.audit_log.
// All fields map directly to the audit_log table columns.
type AuditEvent struct {
	HospitalID  string            `json:"hospital_id" db:"hospital_id"`
	EventType   EventType         `json:"event_type"  db:"event_type"`
	ActorID     string            `json:"actor_id"    db:"actor_id"`
	ActorType   ActorType         `json:"actor_type"  db:"actor_type"`
	PatientKey  string            `json:"patient_key" db:"patient_key"`   // hashed; empty string if unknown
	ConsentID   string            `json:"consent_id"  db:"consent_id"`    // empty string if not applicable
	RequestID   string            `json:"request_id"  db:"request_id"`
	IPAddress   string            `json:"ip_address"  db:"ip_address"`    // empty string if not available
	Details     map[string]any    `json:"details"     db:"details"`
	CreatedAt   time.Time         `json:"created_at"  db:"created_at"`
}
