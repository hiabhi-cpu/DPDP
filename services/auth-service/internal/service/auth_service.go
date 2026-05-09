package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/hiabhi-cpu/DPDP/auth-service/config"
	db "github.com/hiabhi-cpu/DPDP/auth-service/db/sqlc"
	"github.com/hiabhi-cpu/DPDP/auth-service/internal/repository"
	"github.com/hiabhi-cpu/DPDP/shared"
)

// Common auth errors — returned by service, mapped to HTTP status codes in handler.
var (
	ErrUserNotFound       = errors.New("user not found")
	ErrInvalidPassword    = errors.New("invalid password")
	ErrEmailAlreadyExists = errors.New("email already registered")
	ErrPhoneAlreadyExists = errors.New("phone already registered")
	ErrInvalidToken       = errors.New("invalid or expired token")
)

// --- DTOs (Data Transfer Objects) ---
// These are the inputs/outputs of the service layer.
// Handlers build these from HTTP requests; service returns these to handlers.

type RegisterInput struct {
	Role     string // "data_principal" | "data_fiduciary" | "dpo"
	FullName string
	Email    *string
	Phone    *string
	Password string
	OrgID    *string // only for data_fiduciary
}

type LoginInput struct {
	Email    *string
	Phone    *string
	Password string
}

type TokenPair struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

type AuthUser struct {
	ID         uuid.UUID
	Role       string
	FullName   string
	Email      *string
	Phone      *string
	IsVerified bool
}

// --- Service Interface ---

// AuthService defines the business logic contract for authentication.
// Handler layer depends on this interface — never on the concrete struct.
type AuthService interface {
	Register(ctx context.Context, input RegisterInput) (*AuthUser, error)
	Login(ctx context.Context, input LoginInput) (*TokenPair, error)
	RefreshToken(ctx context.Context, refreshToken string) (*TokenPair, error)
	Logout(ctx context.Context, refreshToken string) error
	LogoutAll(ctx context.Context, userID uuid.UUID) error
	GetUserByID(ctx context.Context, id uuid.UUID) (*AuthUser, error)
}

// --- Concrete Implementation ---

type authService struct {
	userRepo  repository.UserRepository
	tokenRepo repository.TokenRepository
	cfg       *config.Config
}

// NewAuthService constructs an AuthService with all its dependencies injected.
func NewAuthService(
	userRepo repository.UserRepository,
	tokenRepo repository.TokenRepository,
	cfg *config.Config,
) AuthService {
	return &authService{
		userRepo:  userRepo,
		tokenRepo: tokenRepo,
		cfg:       cfg,
	}
}

// Register creates a new user account after validating uniqueness and hashing the password.
func (s *authService) Register(ctx context.Context, input RegisterInput) (*AuthUser, error) {
	// Hash password with bcrypt (cost 12 is a good balance of security vs. speed)
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), 12)
	if err != nil {
		return nil, fmt.Errorf("hashing password: %w", err)
	}

	user, err := s.userRepo.CreateUser(ctx, db.CreateUserParams{
		Role:         input.Role,
		Email:        shared.NewText(*input.Email),
		Phone:        shared.NewText(*input.Phone),
		PasswordHash: shared.NewText(string(hash)),
		FullName:     input.FullName,
		OrgID:        shared.NewText(*input.OrgID),
	})
	if err != nil {
		// TODO: detect unique constraint violations and return ErrEmailAlreadyExists / ErrPhoneAlreadyExists
		return nil, fmt.Errorf("creating user: %w", err)
	}

	return toAuthUser(user), nil
}

// Login verifies credentials and issues a JWT access + refresh token pair.
func (s *authService) Login(ctx context.Context, input LoginInput) (*TokenPair, error) {
	var user db.AuthUser
	var err error

	if input.Email != nil {
		user, err = s.userRepo.GetUserByEmail(ctx, *input.Email)
	} else if input.Phone != nil {
		user, err = s.userRepo.GetUserByPhone(ctx, *input.Phone)
	} else {
		return nil, errors.New("email or phone is required")
	}

	if err != nil {
		return nil, ErrUserNotFound
	}

	// Compare bcrypt hash
	if strings.TrimSpace(user.PasswordHash.String) == "" {
		return nil, ErrInvalidPassword
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash.String), []byte(input.Password)); err != nil {
		return nil, ErrInvalidPassword
	}

	return s.issueTokenPair(ctx, user)
}

// RefreshToken validates a refresh token and issues a new token pair (rotation).
func (s *authService) RefreshToken(ctx context.Context, refreshToken string) (*TokenPair, error) {
	// TODO: implement token refresh with hash lookup + rotation
	return nil, errors.New("not implemented yet")
}

// Logout revokes a single refresh token (logout from current device).
func (s *authService) Logout(ctx context.Context, refreshToken string) error {
	// TODO: hash the incoming token and revoke it
	return errors.New("not implemented yet")
}

// LogoutAll revokes all refresh tokens for a user (logout from all devices).
func (s *authService) LogoutAll(ctx context.Context, userID uuid.UUID) error {
	return s.tokenRepo.RevokeAllUserTokens(ctx, userID)
}

// GetUserByID fetches a user's profile by their ID.
func (s *authService) GetUserByID(ctx context.Context, id uuid.UUID) (*AuthUser, error) {
	user, err := s.userRepo.GetUserByID(ctx, id)
	if err != nil {
		return nil, ErrUserNotFound
	}
	return toAuthUser(user), nil
}

// --- Helpers ---

// issueTokenPair generates a JWT access token + opaque refresh token for a user.
func (s *authService) issueTokenPair(ctx context.Context, user db.AuthUser) (*TokenPair, error) {
	expiresAt := time.Now().Add(time.Duration(s.cfg.JWTExpiryHours) * time.Hour)

	// Build JWT claims
	claims := jwt.MapClaims{
		"sub":  user.ID.String(),
		"role": user.Role,
		"exp":  expiresAt.Unix(),
		"iat":  time.Now().Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	accessToken, err := token.SignedString([]byte(s.cfg.JWTSecret))
	if err != nil {
		return nil, fmt.Errorf("signing JWT: %w", err)
	}

	// TODO: generate opaque refresh token, hash it, store in DB
	refreshToken := uuid.New().String() // placeholder — replace with secure random

	return &TokenPair{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt,
	}, nil
}

// toAuthUser maps a SQLC-generated db.AuthUser to our service-layer DTO.
// This keeps the handler/service layers from depending on SQLC types directly.
func toAuthUser(u db.AuthUser) *AuthUser {
	return &AuthUser{
		ID:         u.ID,
		Role:       u.Role,
		FullName:   u.FullName,
		Email:      &u.Email.String,
		Phone:      &u.Phone.String,
		IsVerified: u.IsVerified,
	}
}

func stringPtr(s string) *string { return &s }
