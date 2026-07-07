package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/hiabhi-cpu/emergency-service/pkg/emergency/model"
)

// EmergencyRepository is the data-access contract for emergency access records
// and the DPO review queue. Domain writes and their audit event are persisted
// atomically (same transaction) so an emergency access can never exist without
// an audit row.
type EmergencyRepository interface {
	// InsertAccess writes the immutable consent_vault EMERGENCY_OVERRIDE row, the
	// mutable emergency.reviews queue row, and the audit outbox row — all in one
	// transaction. Sets rec.CreatedAt-equivalent server-side.
	InsertAccess(ctx context.Context, rec *model.AccessRecord, outbox *model.OutboxRecord) error

	// ListPending returns up to limit pending review items for a hospital.
	ListPending(ctx context.Context, hospitalID string, limit int) ([]model.ReviewItem, error)

	// RecordReview marks a pending access reviewed (VERIFIED/FLAGGED) and writes
	// the audit outbox row in one transaction. Returns false if the access was not
	// found or was already reviewed (no PENDING row matched).
	RecordReview(ctx context.Context, hospitalID string, accessID uuid.UUID, status, reviewerID string, outbox *model.OutboxRecord) (bool, error)

	// Relay operations over the outbox.
	FetchUnshippedOutbox(ctx context.Context, limit int) ([]model.OutboxRecord, error)
	MarkOutboxShipped(ctx context.Context, id uuid.UUID) error
	MarkOutboxFailed(ctx context.Context, id uuid.UUID, errMsg string) error
}
