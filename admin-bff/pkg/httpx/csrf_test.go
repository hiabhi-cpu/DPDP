package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func newCSRFRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CSRF(CookieConfig{Secure: false}))
	r.POST("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	return r
}

func TestCSRFAllowsSafeMethods(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	newCSRFRouter().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET code = %d, want 200", w.Code)
	}
}

func TestCSRFRejectsMissingToken(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	newCSRFRouter().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("POST without token code = %d, want 403", w.Code)
	}
}

func TestCSRFAcceptsMatchingToken(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: "tok-abc"})
	req.Header.Set("X-CSRF-Token", "tok-abc")
	newCSRFRouter().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST with matching token code = %d, want 200", w.Code)
	}
}
