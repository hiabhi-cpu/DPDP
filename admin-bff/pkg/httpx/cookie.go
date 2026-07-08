// Package httpx holds HTTP concerns shared across BFF handlers: cookies and CSRF.
package httpx

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	// SessionCookieName is the opaque, HttpOnly session id cookie.
	SessionCookieName = "admin_session"
	// CSRFCookieName is the JS-readable double-submit CSRF token cookie.
	CSRFCookieName = "csrf_token"
)

// CookieConfig carries deployment-specific cookie flags.
type CookieConfig struct {
	Secure bool // true in production (HTTPS); false for local http dev
}

// SetSessionCookie writes the HttpOnly, SameSite=Strict session cookie.
func SetSessionCookie(c *gin.Context, id string, ttl time.Duration, cfg CookieConfig) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     SessionCookieName,
		Value:    id,
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		Secure:   cfg.Secure,
		SameSite: http.SameSiteStrictMode,
	})
}

// ClearSessionCookie expires the session cookie.
func ClearSessionCookie(c *gin.Context, cfg CookieConfig) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   cfg.Secure,
		SameSite: http.SameSiteStrictMode,
	})
}
