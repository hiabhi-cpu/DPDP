// Package handlers holds the BFF HTTP handlers: auth (login/logout/me) and proxy.
package handlers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hiabhi-cpu/admin-bff/pkg/auth"
	"github.com/hiabhi-cpu/admin-bff/pkg/httpx"
	bffmw "github.com/hiabhi-cpu/admin-bff/pkg/middleware"
	"github.com/hiabhi-cpu/admin-bff/pkg/session"
)

// AuthHandler serves login, logout, and current-user endpoints.
type AuthHandler struct {
	users  auth.UserRepository
	store  session.Store
	ttl    time.Duration
	cookie httpx.CookieConfig
}

// NewAuthHandler builds an AuthHandler.
func NewAuthHandler(users auth.UserRepository, store session.Store, ttl time.Duration, cfg httpx.CookieConfig) *AuthHandler {
	return &AuthHandler{users: users, store: store, ttl: ttl, cookie: cfg}
}

type loginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

// Login handles POST /api/session. On success it creates a server-side session,
// sets the session + CSRF cookies, and returns the user's display identity.
func (h *AuthHandler) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "email and password are required"})
		return
	}

	user, err := h.users.GetByEmail(c.Request.Context(), req.Email)
	// Uniform 401 whether the user is missing, disabled, or the password is wrong —
	// never reveal which, to avoid account enumeration.
	if err != nil || user.Disabled || !auth.VerifyPassword(user.PasswordHash, req.Password) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid email or password"})
		return
	}

	id, err := h.store.Create(c.Request.Context(), session.Session{
		UserID: user.ID, Email: user.Email, Role: user.Role, HospitalID: user.HospitalID,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not start session"})
		return
	}
	httpx.SetSessionCookie(c, id, h.ttl, h.cookie)
	httpx.IssueCSRF(c, h.cookie)
	c.JSON(http.StatusOK, gin.H{"email": user.Email, "role": user.Role})
}

// Logout handles DELETE /api/session.
func (h *AuthHandler) Logout(c *gin.Context) {
	if id, err := c.Cookie(httpx.SessionCookieName); err == nil && id != "" {
		_ = h.store.Delete(c.Request.Context(), id)
	}
	httpx.ClearSessionCookie(c, h.cookie)
	c.JSON(http.StatusOK, gin.H{"status": "logged out"})
}

// CSRFToken handles GET /api/csrf. It seeds the double-submit CSRF cookie so the
// SPA has a token to echo on its first mutating request (login). GET is a safe
// method, so it passes the CSRF gate itself.
func (h *AuthHandler) CSRFToken(c *gin.Context) {
	httpx.IssueCSRF(c, h.cookie)
	c.Status(http.StatusNoContent)
}

// Me handles GET /api/me — returns the current user, or 401 (via RequireSession).
func (h *AuthHandler) Me(c *gin.Context) {
	v, ok := c.Get(bffmw.CtxUser)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return
	}
	sess := v.(session.Session)
	c.JSON(http.StatusOK, gin.H{"email": sess.Email, "role": sess.Role})
}
