package repository

import (
	"context"
	"fmt"

	"github.com/hiabhi-cpu/auth-service/pkg/auth/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pgxHospitalRepository is the PostgreSQL implementation of HospitalRepository.
type pgxHospitalRepository struct {
	pool *pgxpool.Pool
}

// New returns a HospitalRepository backed by a pgx connection pool.
func New(pool *pgxpool.Pool) HospitalRepository {
	return &pgxHospitalRepository{pool: pool}
}

func (r *pgxHospitalRepository) GetByAPIKeyHash(ctx context.Context, hash string) (*model.Hospital, error) {
	var h model.Hospital
	err := r.pool.QueryRow(ctx, queryGetByAPIKeyHash, hash).Scan(
		&h.ID,
		&h.Name,
		&h.Slug,
		&h.APIKeyHash,
		&h.Active,
		&h.CreatedAt,
	)
	if err != nil {
		// Wrapped with %w so the service can errors.Is(err, pgx.ErrNoRows) and
		// treat "no such key" differently from a real DB failure.
		return nil, fmt.Errorf("repository.GetByAPIKeyHash: %w", err)
	}
	return &h, nil
}
