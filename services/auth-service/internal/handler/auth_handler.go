package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/hiabhi-cpu/DPDP/auth-service/internal/service"
)

// AuthHandler holds the service dependency and exposes HTTP handler methods.
// Each method follows the pattern: parse → call service → respond.
type AuthHandler struct {
	authSvc service.AuthService
}

// NewAuthHandler constructs an AuthHandler with the given service injected.
func NewAuthHandler(authSvc service.AuthService) *AuthHandler {
	return &AuthHandler{authSvc: authSvc}
}

// ─── Register ────────────────────────────────────────────────────────────────

type registerRequest struct {
	Role     string  `json:"role"      binding:"required,oneof=data_principal data_fiduciary dpo"`
	FullName string  `json:"full_name" binding:"required"`
	Email    *string `json:"email"`
	Phone    *string `json:"phone"`
	Password string  `json:"password"  binding:"required,min=8"`
	OrgID    *string `json:"org_id"`
}

// Register godoc
// POST /auth/register
func (h *AuthHandler) Register(c *gin.Context) {
	var req registerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(err.Error()))
		return
	}

	if req.Email == nil && req.Phone == nil {
		c.JSON(http.StatusBadRequest, errorResponse("email or phone is required"))
		return
	}

	user, err := h.authSvc.Register(c.Request.Context(), service.RegisterInput{
		Role:     req.Role,
		FullName: req.FullName,
		Email:    req.Email,
		Phone:    req.Phone,
		Password: req.Password,
		OrgID:    req.OrgID,
	})
	if err != nil {
		handleServiceError(c, err)
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"user": user,
	})
}

// ─── Login ───────────────────────────────────────────────────────────────────

type loginRequest struct {
	Email    *string `json:"email"`
	Phone    *string `json:"phone"`
	Password string  `json:"password" binding:"required"`
}

// Login godoc
// POST /auth/login
func (h *AuthHandler) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(err.Error()))
		return
	}

	tokens, err := h.authSvc.Login(c.Request.Context(), service.LoginInput{
		Email:    req.Email,
		Phone:    req.Phone,
		Password: req.Password,
	})
	if err != nil {
		handleServiceError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"access_token":  tokens.AccessToken,
		"refresh_token": tokens.RefreshToken,
		"expires_at":    tokens.ExpiresAt,
	})
}

// ─── Refresh Token ────────────────────────────────────────────────────────────

type refreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

// RefreshToken godoc
// POST /auth/refresh
func (h *AuthHandler) RefreshToken(c *gin.Context) {
	var req refreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(err.Error()))
		return
	}

	tokens, err := h.authSvc.RefreshToken(c.Request.Context(), req.RefreshToken)
	if err != nil {
		handleServiceError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"access_token":  tokens.AccessToken,
		"refresh_token": tokens.RefreshToken,
		"expires_at":    tokens.ExpiresAt,
	})
}

// ─── Logout ───────────────────────────────────────────────────────────────────

type logoutRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

// Logout godoc
// POST /auth/logout
func (h *AuthHandler) Logout(c *gin.Context) {
	var req logoutRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(err.Error()))
		return
	}

	if err := h.authSvc.Logout(c.Request.Context(), req.RefreshToken); err != nil {
		handleServiceError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "logged out successfully"})
}

// ─── Me (get current user profile) ───────────────────────────────────────────

// Me godoc
// GET /auth/me  (requires JWT middleware — userID injected by middleware)
func (h *AuthHandler) Me(c *gin.Context) {
	userIDStr, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, errorResponse("unauthorized"))
		return
	}

	userID, err := uuid.Parse(userIDStr.(string))
	if err != nil {
		c.JSON(http.StatusUnauthorized, errorResponse("invalid user id in token"))
		return
	}

	user, err := h.authSvc.GetUserByID(c.Request.Context(), userID)
	if err != nil {
		handleServiceError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"user": user})
}

//need to see what is last service for
