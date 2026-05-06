package repository

import (
	"context"

	"github.com/google/uuid"
	db "github.com/hiabhi-cpu/DPDP/auth-service/db/sqlc"
	"github.com/jackc/pgx/v5/pgtype"
)

// UserRepository defines the contract for all user-related DB operations.
// The service layer depends on this interface, not the concrete implementation —
// this makes the service layer testable without a real database.
type UserRepository interface {
	CreateUser(ctx context.Context, params db.CreateUserParams) (db.AuthUser, error)
	GetUserByID(ctx context.Context, id uuid.UUID) (db.AuthUser, error)
	GetUserByEmail(ctx context.Context, email string) (db.AuthUser, error)
	GetUserByPhone(ctx context.Context, phone string) (db.AuthUser, error)
	UpdateUserVerified(ctx context.Context, id uuid.UUID) (db.AuthUser, error)
	DeactivateUser(ctx context.Context, id uuid.UUID) error
}

// userRepository is the concrete implementation backed by SQLC-generated queries.
type userRepository struct {
	queries *db.Queries
}

// NewUserRepository constructs a UserRepository backed by the given SQLC Queries.
func NewUserRepository(queries *db.Queries) UserRepository {
	return &userRepository{queries: queries}
}

func (r *userRepository) CreateUser(ctx context.Context, params db.CreateUserParams) (db.AuthUser, error) {
	return r.queries.CreateUser(ctx, params)
}

func (r *userRepository) GetUserByID(ctx context.Context, id uuid.UUID) (db.AuthUser, error) {
	return r.queries.GetUserByID(ctx, id)
}

func (r *userRepository) GetUserByEmail(ctx context.Context, email string) (db.AuthUser, error) {
	return r.queries.GetUserByEmail(ctx, newText(email))
}

func (r *userRepository) GetUserByPhone(ctx context.Context, phone string) (db.AuthUser, error) {
	return r.queries.GetUserByPhone(ctx, newText(phone))
}

func (r *userRepository) UpdateUserVerified(ctx context.Context, id uuid.UUID) (db.AuthUser, error) {
	return r.queries.UpdateUserVerified(ctx, id)
}

func (r *userRepository) DeactivateUser(ctx context.Context, id uuid.UUID) error {
	return r.queries.DeactivateUser(ctx, id)
}

func newText(s string) pgtype.Text {
	return pgtype.Text{
		String: s,
		Valid:  s != "", // Or just true if you want empty strings to be valid
	}
}
