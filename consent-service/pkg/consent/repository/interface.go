package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/hiabhi-cpu/consent-service/pkg/consent/model"
)

// ConsentRepository defines the data access contract for consents and the
// transactional audit outbox. Consent writes and their audit event are persisted
// atomically (same transaction) so a consent can never exist without an audit row.
type ConsentRepository interface {
	// Insert writes a new consent row and its audit outbox row in one transaction.
	Insert(ctx context.Context, consent *model.Consent, outbox *model.OutboxRecord) error
	// InsertWithdrawn writes a withdrawal row and its audit outbox row in one transaction.
	InsertWithdrawn(ctx context.Context, consent *model.Consent, outbox *model.OutboxRecord) error
	// InsertRenewal writes a renewal row (re-grant / add purpose) and its audit
	// outbox row in one transaction.
	InsertRenewal(ctx context.Context, consent *model.Consent, outbox *model.OutboxRecord) error
	// GetLatestByPatientKey returns the most recent consent row (any status), or
	// nil. Callers inspect its per-purpose Purposes map for current state.
	GetLatestByPatientKey(ctx context.Context, hospitalID string, patientKey string) (*model.Consent, error)
	// GetLatestByHMSPatientID returns the most recent consent row for a hospital's
	// opaque HMS patient ID (doctor/HMS access path), or nil.
	GetLatestByHMSPatientID(ctx context.Context, hospitalID, hmsPatientID string) (*model.Consent, error)
	// GetByIdempotencyKey returns the CONSENT_GIVEN row for a capture idempotency
	// key (session_id), or nil if none exists.
	GetByIdempotencyKey(ctx context.Context, hospitalID, key string) (*model.Consent, error)

	// EnqueueAudit writes a standalone audit outbox row (for reads/checks that have
	// no domain row to piggyback on).
	EnqueueAudit(ctx context.Context, outbox *model.OutboxRecord) error

	// Relay operations over the outbox.
	FetchUnshippedOutbox(ctx context.Context, limit int) ([]model.OutboxRecord, error)
	MarkOutboxShipped(ctx context.Context, id uuid.UUID) error
	MarkOutboxFailed(ctx context.Context, id uuid.UUID, errMsg string) error
}
