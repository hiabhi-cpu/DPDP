package repository

import (
	"context"

	"github.com/hiabhi-cpu/audit-service/pkg/audit/model"
)

// AuditRepository defines the data access contract for the audit log.
type AuditRepository interface {
	// Insert adds a new immutable event to the audit log.
	Insert(ctx context.Context, event *model.AuditEvent) error

	// Find paginates through the audit log based on the provided filter.
	Find(ctx context.Context, filter model.AuditLogFilter) ([]model.AuditEvent, int, error)
}
