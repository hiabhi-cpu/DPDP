// Package middleware provides BFF-local gin middleware.
package middleware

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/hiabhi-cpu/admin-bff/pkg/httpx"
	"github.com/hiabhi-cpu/admin-bff/pkg/session"
)

// CtxUser is the gin context key holding the authenticated session.Session.
const CtxUser = "admin_user"

// RequireSession loads the session from the cookie and aborts 401 if absent/expired.
func RequireSession(store session.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := c.Cookie(httpx.SessionCookieName)
		if err != nil || id == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
			return
		}
		sess, err := store.Get(c.Request.Context(), id)
		if err != nil {
			if errors.Is(err, session.ErrNotFound) {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "session expired"})
				return
			}
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "session lookup failed"})
			return
		}
		c.Set(CtxUser, sess)
		c.Next()
	}
}
