package model

import (
	"time"

	"github.com/google/uuid"
)

// AuditEvent represents a single immutable event in the audit log.
type AuditEvent struct {
	ID         int64          `json:"id"`
	EventID    uuid.UUID      `json:"event_id"` // idempotency key (outbox row id)
	HospitalID string         `json:"hospital_id"`
	EventType  string         `json:"event_type"`
	ActorID    string         `json:"actor_id"`
	ActorType  string         `json:"actor_type"`
	PatientKey string         `json:"patient_key"`
	ConsentID  *uuid.UUID     `json:"consent_id,omitempty"`
	RequestID  uuid.UUID      `json:"request_id"`
	IPAddress  string         `json:"ip_address"`
	Details    map[string]any `json:"details"`
	CreatedAt  time.Time      `json:"created_at"`
}

// AuditLogFilter holds query parameters for fetching audit logs.
type AuditLogFilter struct {
	HospitalID string
	EventType  string
	Page       int
	Limit      int
}

// AuditLogPage represents a paginated response of audit events.
type AuditLogPage struct {
	Events []AuditEvent `json:"events"`
	Total  int          `json:"total"`
	Page   int          `json:"page"`
	Limit  int          `json:"limit"`
}
