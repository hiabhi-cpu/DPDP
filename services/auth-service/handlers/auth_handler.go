package handlers

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	authservice "github.com/hiabhi-cpu/DPDP/auth-service/service"
)

// HospitalRepo is the interface the handler uses to look up hospitals.
// Implemented by db/repository — mocked in tests.
type HospitalRepo interface {
	GetByAPIKeyHash(ctx context.Context, apiKeyHash string) (*Hospital, error)
	GetByAPIKey(ctx context.Context, rawAPIKey string) (*Hospital, error)
}

// Hospital is a minimal struct with only what the handler needs from the DB.
type Hospital struct {
	ID         string
	Slug       string
	APIKeyHash string
	Active     bool
}

// AuthHandler handles hospital authentication.
type AuthHandler struct {
	svc  *authservice.AuthService
	repo HospitalRepo
}

// NewAuthHandler creates an AuthHandler.
func NewAuthHandler(svc *authservice.AuthService, repo HospitalRepo) *AuthHandler {
	return &AuthHandler{svc: svc, repo: repo}
}

// TokenRequest is the body for POST /v1/auth/token.
type TokenRequest struct {
	APIKey string `json:"api_key" binding:"required"`
}

// TokenResponse is returned on successful authentication.
type TokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
	HospitalID string `json:"hospital_id"`
}

// IssueToken handles POST /v1/auth/token.
// Verifies the raw API key against the stored bcrypt hash,
// then issues a signed RS256 JWT with hospital_id in claims.
func (h *AuthHandler) IssueToken(c *gin.Context) {
	var req TokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "api_key is required"})
		return
	}

	// Look up hospital by scanning stored hashes
	// NOTE: This performs a bcrypt comparison per hospital row.
	// In Phase 1 with few hospitals this is fine. Phase 2: add API key prefix for indexed lookup.
	hospital, err := h.repo.GetByAPIKey(c.Request.Context(), req.APIKey)
	if err != nil || hospital == nil {
		// Return identical error for both "not found" and "wrong key" — prevents enumeration
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid API key"})
		return
	}

	if !hospital.Active {
		c.JSON(http.StatusForbidden, gin.H{"error": "hospital account is inactive"})
		return
	}

	token, expiresAt, err := h.svc.IssueToken(c.Request.Context(), hospital.ID, hospital.Slug)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "token issuance failed"})
		return
	}

	c.JSON(http.StatusOK, TokenResponse{
		Token:      token,
		ExpiresAt:  expiresAt.UTC().Format("2006-01-02T15:04:05Z"),
		HospitalID: hospital.ID,
	})
}

// HealthHandler handles GET /health.
func HealthHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"service": "auth-service",
		"status":  "ok",
	})
}
