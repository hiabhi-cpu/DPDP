package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hiabhi-cpu/admin-bff/pkg/session"
)

func routerWithRole(role string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(CtxUser, session.Session{Role: role}); c.Next() })
	r.GET("/reception", RequireRole("reception"), func(c *gin.Context) { c.Status(http.StatusOK) })
	return r
}

func TestRequireRoleAllowsMatch(t *testing.T) {
	w := httptest.NewRecorder()
	routerWithRole("reception").ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/reception", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", w.Code)
	}
}

func TestRequireRoleBlocksMismatch(t *testing.T) {
	w := httptest.NewRecorder()
	routerWithRole("admin").ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/reception", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", w.Code)
	}
}
