package controller

import (
	"context"

	"github.com/hiabhi-cpu/integration-service/pkg/pending/model"
)

// PendingStore is the slice of the repository the controllers need. Defined here
// (consumer side) so handlers depend on behavior, not the concrete Redis store.
type PendingStore interface {
	Upsert(ctx context.Context, reg model.PendingRegistration) error
	Get(ctx context.Context, hospitalID, hmsPatientID string) (*model.PendingRegistration, error)
	List(ctx context.Context, hospitalID string) ([]model.PendingRegistration, error)
	SetStatus(ctx context.Context, hospitalID, hmsPatientID, status string) error
}

// ConsentChecker is the slice of consent-service the read API needs: given the
// caller's Authorization header and a batch of raw mobiles, which of them already
// have an active consent? Defined here (consumer side) alongside PendingStore.
type ConsentChecker interface {
	ActiveMobiles(ctx context.Context, authHeader string, mobiles []string) (map[string]bool, error)
}
