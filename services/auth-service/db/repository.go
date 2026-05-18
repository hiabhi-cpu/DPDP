package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/hiabhi-cpu/DPDP/auth-service/handlers"
	sharedcrypto "github.com/hiabhi-cpu/DPDP/shared/crypto"
)

// HospitalRepository implements handlers.HospitalRepo using PostgreSQL (pgx).
type HospitalRepository struct {
	pool *pgxpool.Pool
}

// NewHospitalRepository creates a repository backed by a pgx connection pool.
func NewHospitalRepository(pool *pgxpool.Pool) *HospitalRepository {
	return &HospitalRepository{pool: pool}
}

// GetByAPIKey finds a hospital by verifying the raw API key against all stored bcrypt hashes.
//
// Phase 1 approach: scan all active hospitals and bcrypt.Compare each.
// This is fine for < 100 hospitals. Phase 2 optimization: store a deterministic
// prefix of the API key to enable indexed lookup before bcrypt comparison.
func (r *HospitalRepository) GetByAPIKey(ctx context.Context, rawAPIKey string) (*handlers.Hospital, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, slug, api_key_hash, active
		FROM auth.hospitals
		WHERE active = true
	`)
	if err != nil {
		return nil, fmt.Errorf("db: query hospitals: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var h handlers.Hospital
		if err := rows.Scan(&h.ID, &h.Slug, &h.APIKeyHash, &h.Active); err != nil {
			return nil, fmt.Errorf("db: scan hospital row: %w", err)
		}
		if sharedcrypto.VerifyAPIKey(rawAPIKey, h.APIKeyHash) {
			return &h, nil
		}
	}

	return nil, nil // not found
}

// GetByAPIKeyHash looks up a hospital by its stored bcrypt hash (for internal use).
func (r *HospitalRepository) GetByAPIKeyHash(ctx context.Context, apiKeyHash string) (*handlers.Hospital, error) {
	var h handlers.Hospital
	err := r.pool.QueryRow(ctx, `
		SELECT id, slug, api_key_hash, active
		FROM auth.hospitals
		WHERE api_key_hash = $1
	`, apiKeyHash).Scan(&h.ID, &h.Slug, &h.APIKeyHash, &h.Active)
	if err != nil {
		return nil, nil // treat as not found
	}
	return &h, nil
}
