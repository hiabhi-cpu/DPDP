package controller

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/hiabhi-cpu/auth-service/pkg/auth/model"
	"github.com/hiabhi-cpu/auth-service/pkg/auth/service"
)

// AuthHandler holds HTTP handlers for the auth domain.
type AuthHandler struct {
	svc service.AuthService
}

// New creates an AuthHandler.
func New(svc service.AuthService) *AuthHandler {
	return &AuthHandler{svc: svc}
}

// IssueToken handles POST /v1/auth/token.
// It validates the API key and returns a signed JWT on success.
func (h *AuthHandler) IssueToken(c *gin.Context) {
	var req model.IssueTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "api_key is required"})
		return
	}

	resp, err := h.svc.IssueToken(c.Request.Context(), req.APIKey)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidAPIKey):
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid API key"})
		case errors.Is(err, service.ErrHospitalInactive):
			c.JSON(http.StatusForbidden, gin.H{"error": "hospital account is inactive"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		}
		return
	}

	c.JSON(http.StatusOK, resp)
}

// IssueServiceToken handles POST /v1/auth/service-token.
// It validates the shared bootstrap secret and returns a short-lived service JWT.
func (h *AuthHandler) IssueServiceToken(c *gin.Context) {
	var req model.ServiceTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "service_name and client_secret are required"})
		return
	}

	resp, err := h.svc.IssueServiceToken(c.Request.Context(), req.ServiceName, req.ClientSecret)
	if err != nil {
		if errors.Is(err, service.ErrInvalidServiceCredential) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid service credential"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}

	c.JSON(http.StatusOK, resp)
}
