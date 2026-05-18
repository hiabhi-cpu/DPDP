package service

import (
	"context"
	"crypto/rsa"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	sharedcrypto "github.com/hiabhi-cpu/DPDP/shared/crypto"
)

// HospitalClaims are the JWT claims embedded in every token issued by auth-service.
// hospital_id is the critical field — all other services extract it from here
// and NEVER trust hospital_id from request bodies.
type HospitalClaims struct {
	HospitalID   string `json:"hospital_id"`
	HospitalSlug string `json:"hospital_slug"`
	Role         string `json:"role"` // "hospital" for API keys; future: "doctor", "dpo", "patient"
	jwt.RegisteredClaims
}

// AuthService handles JWT issuance and API key verification for hospital tenants.
type AuthService struct {
	privateKey  *rsa.PrivateKey
	publicKey   *rsa.PublicKey
	tokenExpiry time.Duration
}

// NewAuthService creates an AuthService with the provided RSA key pair and expiry.
func NewAuthService(privateKey *rsa.PrivateKey, publicKey *rsa.PublicKey, expiry time.Duration) *AuthService {
	return &AuthService{
		privateKey:  privateKey,
		publicKey:   publicKey,
		tokenExpiry: expiry,
	}
}

// IssueToken signs a new RS256 JWT for a hospital.
// Returns the signed token string or an error.
func (s *AuthService) IssueToken(_ context.Context, hospitalID, slug string) (string, time.Time, error) {
	now := time.Now()
	expiresAt := now.Add(s.tokenExpiry)

	claims := HospitalClaims{
		HospitalID:   hospitalID,
		HospitalSlug: slug,
		Role:         "hospital",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   hospitalID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			// jti prevents token replay — each token is uniquely identifiable
			ID: uuid.New().String(),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := token.SignedString(s.privateKey)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("auth: failed to sign token: %w", err)
	}

	return signed, expiresAt, nil
}

// VerifyAPIKey checks a raw hospital API key against a stored bcrypt hash.
// Delegates to shared/crypto — timing-safe.
func (s *AuthService) VerifyAPIKey(raw, hash string) bool {
	return sharedcrypto.VerifyAPIKey(raw, hash)
}
