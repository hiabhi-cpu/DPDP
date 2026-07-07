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

func (r *pgxHospitalRepository) ListActive(ctx context.Context) ([]model.Hospital, error) {
	rows, err := r.pool.Query(ctx, queryListActive)
	if err != nil {
		return nil, fmt.Errorf("repository.ListActive: query failed: %w", err)
	}
	defer rows.Close()

	var hospitals []model.Hospital
	for rows.Next() {
		var h model.Hospital
		if err := rows.Scan(
			&h.ID,
			&h.Name,
			&h.Slug,
			&h.APIKeyHash,
			&h.Active,
			&h.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("repository.ListActive: scan failed: %w", err)
		}
		hospitals = append(hospitals, h)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("repository.ListActive: rows error: %w", err)
	}

	return hospitals, nil
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
		// return nil, err so the service layer can handle pgx.ErrNoRows if it wants,
		// or we can wrap it. Let's just return the err for now.
		return nil, fmt.Errorf("repository.GetByAPIKeyHash: %w", err)
	}
	return &h, nil
}
