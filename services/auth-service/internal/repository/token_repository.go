package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	db "github.com/hiabhi-cpu/DPDP/auth-service/db/sqlc"
)

// TokenRepository defines the contract for refresh token DB operations.
type TokenRepository interface {
	CreateRefreshToken(ctx context.Context, params db.CreateRefreshTokenParams) (db.AuthRefreshToken, error)
	GetRefreshToken(ctx context.Context, tokenHash string) (db.AuthRefreshToken, error)
	RevokeRefreshToken(ctx context.Context, tokenHash string) error
	RevokeAllUserTokens(ctx context.Context, userID uuid.UUID) error
	DeleteExpiredTokens(ctx context.Context) error
}

// tokenRepository is the concrete implementation backed by SQLC-generated queries.
type tokenRepository struct {
	queries *db.Queries
}

// NewTokenRepository constructs a TokenRepository backed by the given SQLC Queries.
func NewTokenRepository(queries *db.Queries) TokenRepository {
	return &tokenRepository{queries: queries}
}

func (r *tokenRepository) CreateRefreshToken(ctx context.Context, params db.CreateRefreshTokenParams) (db.AuthRefreshToken, error) {
	return r.queries.CreateRefreshToken(ctx, params)
}

func (r *tokenRepository) GetRefreshToken(ctx context.Context, tokenHash string) (db.AuthRefreshToken, error) {
	return r.queries.GetRefreshToken(ctx, tokenHash)
}

func (r *tokenRepository) RevokeRefreshToken(ctx context.Context, tokenHash string) error {
	return r.queries.RevokeRefreshToken(ctx, tokenHash)
}

func (r *tokenRepository) RevokeAllUserTokens(ctx context.Context, userID uuid.UUID) error {
	return r.queries.RevokeAllUserTokens(ctx, userID)
}

func (r *tokenRepository) DeleteExpiredTokens(ctx context.Context) error {
	return r.queries.DeleteExpiredTokens(ctx)
}

// RefreshTokenParams is a convenience struct used by the service layer
// to avoid importing the SQLC package directly.
type RefreshTokenParams struct {
	UserID    uuid.UUID
	TokenHash string
	ExpiresAt time.Time
	IPAddress *string
	UserAgent *string
}
