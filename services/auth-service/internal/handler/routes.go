package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/hiabhi-cpu/DPDP/auth-service/internal/service"
)

// SetupRoutes registers all auth-service routes on the given gin engine.
// Called once from main.go during server startup.
func SetupRoutes(r *gin.Engine, h *AuthHandler) {
	// Health check — no auth required
	r.GET("/health", healthCheck)

	// Auth routes — grouped under /auth
	auth := r.Group("/auth")
	{
		auth.POST("/register", h.Register)     // create account
		auth.POST("/login", h.Login)           // get token pair
		auth.POST("/refresh", h.RefreshToken)  // rotate tokens
		auth.POST("/logout", h.Logout)         // revoke current device

		// Protected routes — require valid JWT
		// TODO: add JWTMiddleware() once middleware package is built
		protected := auth.Group("")
		// protected.Use(middleware.JWTMiddleware(cfg))
		{
			protected.GET("/me", h.Me) // get current user profile
		}
	}
}

// healthCheck is a simple liveness probe endpoint.
func healthCheck(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"service": "auth-service",
		"status":  "ok",
		"port":    9006,
	})
}

// ─── Shared response helpers ──────────────────────────────────────────────────

// errorResponse wraps a message string in a standard error envelope.
func errorResponse(msg string) gin.H {
	return gin.H{"error": msg}
}

// handleServiceError maps known service-layer errors to appropriate HTTP status codes.
// Keeps the handler free of business logic — it just translates errors.
func handleServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrUserNotFound):
		c.JSON(http.StatusNotFound, errorResponse("user not found"))
	case errors.Is(err, service.ErrInvalidPassword):
		c.JSON(http.StatusUnauthorized, errorResponse("invalid credentials"))
	case errors.Is(err, service.ErrEmailAlreadyExists):
		c.JSON(http.StatusConflict, errorResponse("email already registered"))
	case errors.Is(err, service.ErrPhoneAlreadyExists):
		c.JSON(http.StatusConflict, errorResponse("phone already registered"))
	case errors.Is(err, service.ErrInvalidToken):
		c.JSON(http.StatusUnauthorized, errorResponse("invalid or expired token"))
	default:
		c.JSON(http.StatusInternalServerError, errorResponse("internal server error"))
	}
}
