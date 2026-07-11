package service

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	log "github.com/sirupsen/logrus"

	sharedcrypto "github.com/hiabhi-cpu/shared/crypto"
	"github.com/hiabhi-cpu/shared/middleware"

	"github.com/hiabhi-cpu/auth-service/pkg/auth/model"
	"github.com/hiabhi-cpu/auth-service/pkg/auth/repository"
)

// AuthService defines the business logic contract for authentication.
type AuthService interface {
	// IssueToken looks up the hospital by raw API key (SHA-256 hash lookup),
	// then issues a signed RS256 JWT if the key is valid.
	IssueToken(ctx context.Context, rawAPIKey string) (*model.IssueTokenResponse, error)

	// IssueServiceToken validates the shared bootstrap secret and issues a
	// short-lived RS256 service token (role=service, no hospital_id) for
	// service-to-service calls.
	IssueServiceToken(ctx context.Context, serviceName, clientSecret string) (*model.ServiceTokenResponse, error)
}

type authService struct {
	repo          repository.HospitalRepository
	privateKey    interface{} // *rsa.PrivateKey — kept as interface{} for jwt library
	expiry        time.Duration
	serviceSecret string
	serviceExpiry time.Duration
}

// New creates an AuthService.
// privateKey must be an *rsa.PrivateKey loaded from the PEM file.
// serviceSecret is the shared bootstrap secret internal callers present to
// obtain a service token; serviceExpiry is the lifetime of issued service tokens.
func New(repo repository.HospitalRepository, privateKey interface{}, expiry time.Duration, serviceSecret string, serviceExpiry time.Duration) AuthService {
	return &authService{
		repo:          repo,
		privateKey:    privateKey,
		expiry:        expiry,
		serviceSecret: serviceSecret,
		serviceExpiry: serviceExpiry,
	}
}

func (s *authService) IssueToken(ctx context.Context, rawAPIKey string) (*model.IssueTokenResponse, error) {
	// 1. Hash the raw key using deterministic SHA-256
	hash := sharedcrypto.HashAPIKey(rawAPIKey)

	// 2. O(1) lookup in the database
	matched, err := s.repo.GetByAPIKeyHash(ctx, hash)
	if err != nil {
		// Only an unknown key is 401. A DB failure must surface as a 500 and be
		// logged — otherwise an outage looks like thousands of bad API keys.
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrInvalidAPIKey
		}
		log.Printf("auth-service: IssueToken lookup failed: %v", err)
		return nil, fmt.Errorf("service.IssueToken: %w", err)
	}

	if matched == nil {
		return nil, ErrInvalidAPIKey
	}

	if !matched.Active {
		return nil, ErrHospitalInactive
	}

	// Build and sign JWT.
	now := time.Now()
	expiresAt := now.Add(s.expiry)

	claims := model.HospitalClaims{
		HospitalID:   matched.ID.String(),
		HospitalSlug: matched.Slug,
		Role:         "hospital",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   matched.ID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			ID:        uuid.New().String(), // jti — prevents token replay
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := token.SignedString(s.privateKey)
	if err != nil {
		return nil, fmt.Errorf("service.IssueToken: jwt signing failed: %w", err)
	}

	return &model.IssueTokenResponse{
		Token:      signed,
		ExpiresAt:  expiresAt,
		HospitalID: matched.ID.String(),
	}, nil
}

func (s *authService) IssueServiceToken(_ context.Context, serviceName, clientSecret string) (*model.ServiceTokenResponse, error) {
	// Constant-time comparison — never leak secret validity via timing.
	if subtle.ConstantTimeCompare([]byte(clientSecret), []byte(s.serviceSecret)) != 1 {
		return nil, ErrInvalidServiceCredential
	}

	now := time.Now()
	expiresAt := now.Add(s.serviceExpiry)

	claims := middleware.ServiceClaims{
		Role: middleware.RoleService,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   serviceName,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			ID:        uuid.New().String(),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := token.SignedString(s.privateKey)
	if err != nil {
		return nil, fmt.Errorf("service.IssueServiceToken: jwt signing failed: %w", err)
	}

	return &model.ServiceTokenResponse{
		Token:     signed,
		ExpiresAt: expiresAt,
	}, nil
}

// Sentinel errors — returned by service, mapped to HTTP status in controller.
var (
	ErrInvalidAPIKey            = fmt.Errorf("invalid API key")
	ErrHospitalInactive         = fmt.Errorf("hospital account is inactive")
	ErrInvalidServiceCredential = fmt.Errorf("invalid service credential")
)
