package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/hiabhi-cpu/DPDP/audit-service/model"
)

// AuditService writes audit events to the append-only audit.audit_log table.
//
// Design rules (enforced here AND at DB level):
//   - Uses sqlx.NamedExec for raw INSERT — NEVER uses an ORM for this table
//   - Sets SET LOCAL app.hospital_id before every insert to activate RLS
//   - Never calls UPDATE or DELETE — the DB trigger would reject it anyway
type AuditService struct {
	db *sqlx.DB
}

// NewAuditService creates an AuditService backed by a sqlx DB connection.
func NewAuditService(db *sqlx.DB) *AuditService {
	return &AuditService{db: db}
}

// LogEvent writes a single audit event. Returns an error only if the DB write fails.
// Callers should log the error but NOT fail the main request — audit failure
// must never block a clinical workflow (emergency access, consent check, etc.)
func (s *AuditService) LogEvent(ctx context.Context, event model.AuditEvent) error {
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}

	detailsJSON, err := json.Marshal(event.Details)
	if err != nil {
		detailsJSON = []byte("{}")
	}

	// Use a transaction so SET LOCAL app.hospital_id scopes to exactly this insert
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("audit: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Activate RLS for this hospital — MUST be SET LOCAL (not SET) so it
	// doesn't leak to other goroutines using the same connection.
	if _, err := tx.ExecContext(ctx,
		"SET LOCAL app.hospital_id = $1", event.HospitalID,
	); err != nil {
		return fmt.Errorf("audit: set rls context: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO audit.audit_log (
			hospital_id,
			event_type,
			actor_id,
			actor_type,
			patient_key,
			consent_id,
			request_id,
			ip_address,
			details,
			created_at
		) VALUES (
			$1, $2, $3, $4,
			NULLIF($5, ''),
			NULLIF($6, '')::uuid,
			NULLIF($7, '')::uuid,
			NULLIF($8, '')::inet,
			$9::jsonb,
			$10
		)`,
		event.HospitalID,
		event.EventType,
		event.ActorID,
		event.ActorType,
		event.PatientKey,
		event.ConsentID,
		event.RequestID,
		event.IPAddress,
		string(detailsJSON),
		event.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("audit: insert event: %w", err)
	}

	return tx.Commit()
}
