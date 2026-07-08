package httpx

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// IssueCSRF sets a fresh double-submit CSRF token cookie. It is deliberately NOT
// HttpOnly: the SPA reads it and echoes it in the X-CSRF-Token header.
func IssueCSRF(c *gin.Context, cfg CookieConfig) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     CSRFCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int((8 * time.Hour).Seconds()),
		HttpOnly: false,
		Secure:   cfg.Secure,
		SameSite: http.SameSiteStrictMode,
	})
}

// CSRF enforces the double-submit pattern on unsafe methods: the X-CSRF-Token
// header must equal the csrf_token cookie. Safe methods pass through.
func CSRF(_ CookieConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			c.Next()
			return
		}
		cookie, err := c.Cookie(CSRFCookieName)
		header := c.GetHeader("X-CSRF-Token")
		if err != nil || cookie == "" || header == "" ||
			subtle.ConstantTimeCompare([]byte(cookie), []byte(header)) != 1 {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "invalid CSRF token"})
			return
		}
		c.Next()
	}
}
