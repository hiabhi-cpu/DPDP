package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrUserNotFound means no admin user matched the email (or the account is unusable).
var ErrUserNotFound = errors.New("admin user not found")

// AdminUser is a dashboard login account (auth.admin_users).
type AdminUser struct {
	ID           string
	HospitalID   string
	Email        string
	PasswordHash string
	Role         string
	Disabled     bool
}

// UserRepository looks up admin users for authentication.
type UserRepository interface {
	GetByEmail(ctx context.Context, email string) (*AdminUser, error)
}

type pgxUserRepository struct {
	pool *pgxpool.Pool
}

// NewUserRepository returns a Postgres-backed UserRepository.
func NewUserRepository(pool *pgxpool.Pool) UserRepository {
	return &pgxUserRepository{pool: pool}
}

const queryGetAdminByEmail = `
	SELECT id, hospital_id, email, password_hash, role, disabled
	FROM auth.admin_users
	WHERE email = $1
`

func (r *pgxUserRepository) GetByEmail(ctx context.Context, email string) (*AdminUser, error) {
	var u AdminUser
	err := r.pool.QueryRow(ctx, queryGetAdminByEmail, email).Scan(
		&u.ID, &u.HospitalID, &u.Email, &u.PasswordHash, &u.Role, &u.Disabled,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("auth.GetByEmail: %w", err)
	}
	return &u, nil
}
