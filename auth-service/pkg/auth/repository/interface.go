package repository

import (
	"context"

	"github.com/hiabhi-cpu/auth-service/pkg/auth/model"
)

// HospitalRepository defines the data access contract for hospital lookups.
// The service layer depends on this interface, not on any concrete implementation.
type HospitalRepository interface {
	// ListActive returns all active hospitals. Used for legacy/other checks.
	ListActive(ctx context.Context) ([]model.Hospital, error)

	// GetByAPIKeyHash retrieves a single hospital by the SHA-256 hash of its API key.
	GetByAPIKeyHash(ctx context.Context, hash string) (*model.Hospital, error)
}
