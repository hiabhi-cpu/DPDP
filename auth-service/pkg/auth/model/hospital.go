package model

import (
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Hospital represents a registered hospital in the auth schema.
type Hospital struct {
	ID         uuid.UUID `db:"id"`
	Name       string    `db:"name"`
	Slug       string    `db:"slug"`
	APIKeyHash string    `db:"api_key_hash"`
	Active     bool      `db:"active"`
	CreatedAt  time.Time `db:"created_at"`
}

// HospitalClaims is the JWT payload signed by auth-service (RS256).
// All other services parse this to extract hospital identity.
type HospitalClaims struct {
	HospitalID   string `json:"hospital_id"`
	HospitalSlug string `json:"hospital_slug"`
	Role         string `json:"role"`
	jwt.RegisteredClaims
}

// IssueTokenRequest is the request body for POST /v1/auth/token.
type IssueTokenRequest struct {
	APIKey string `json:"api_key" binding:"required"`
}

// IssueTokenResponse is the response body for a successful token issuance.
type IssueTokenResponse struct {
	Token      string    `json:"token"`
	ExpiresAt  time.Time `json:"expires_at"`
	HospitalID string    `json:"hospital_id"`
}

// ServiceTokenRequest is the request body for POST /v1/auth/service-token.
// Internal services present a shared bootstrap secret to obtain a short-lived
// RS256 service token used for service-to-service calls.
type ServiceTokenRequest struct {
	ServiceName  string `json:"service_name" binding:"required"`
	ClientSecret string `json:"client_secret" binding:"required"`
}

// ServiceTokenResponse is returned on successful service-token issuance.
type ServiceTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}
