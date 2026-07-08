package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	bffmw "github.com/hiabhi-cpu/admin-bff/pkg/middleware"
	"github.com/hiabhi-cpu/admin-bff/pkg/session"
)

type staticToken struct{ tok string }

func (s staticToken) Token(_ context.Context) (string, error) { return s.tok, nil }

func TestForwardGetInjectsBearerAndQuery(t *testing.T) {
	var gotAuth, gotQuery string
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer down.Close()

	gin.SetMode(gin.TestMode)
	p := NewProxy(down.URL, staticToken{tok: "jwt-xyz"})
	r := gin.New()
	r.GET("/api/consent/stats", func(c *gin.Context) { p.ForwardGet(c, "/api/v1/consent/stats") })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/consent/stats?window_days=7", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", w.Code)
	}
	if gotAuth != "Bearer jwt-xyz" {
		t.Fatalf("downstream Authorization = %q, want Bearer jwt-xyz", gotAuth)
	}
	if gotQuery != "window_days=7" {
		t.Fatalf("downstream query = %q, want window_days=7", gotQuery)
	}
}

func TestForwardReviewInjectsReviewerID(t *testing.T) {
	var gotBody map[string]any
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"reviewed"}`))
	}))
	defer down.Close()

	gin.SetMode(gin.TestMode)
	p := NewProxy(down.URL, staticToken{tok: "jwt-xyz"})
	r := gin.New()
	// Simulate RequireSession having set the user.
	r.POST("/api/emergency/:id/review", func(c *gin.Context) {
		c.Set(bffmw.CtxUser, session.Session{UserID: "u1", Email: "dpo@x.local", Role: "dpo"})
		p.ForwardReview(c)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/emergency/abc-123/review",
		strings.NewReader(`{"decision":"VERIFIED"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if gotBody["reviewer_id"] != "dpo@x.local" {
		t.Fatalf("reviewer_id = %v, want dpo@x.local (server-injected)", gotBody["reviewer_id"])
	}
	if gotBody["decision"] != "VERIFIED" {
		t.Fatalf("decision = %v, want VERIFIED (preserved)", gotBody["decision"])
	}
}

func TestForwardReviewNullBodyInjectsReviewer(t *testing.T) {
	var gotBody map[string]any
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"reviewed"}`))
	}))
	defer down.Close()

	gin.SetMode(gin.TestMode)
	p := NewProxy(down.URL, staticToken{tok: "jwt-xyz"})
	r := gin.New()
	// Simulate RequireSession having set the user.
	r.POST("/api/emergency/:id/review", func(c *gin.Context) {
		c.Set(bffmw.CtxUser, session.Session{UserID: "u1", Email: "dpo@x.local", Role: "dpo"})
		p.ForwardReview(c)
	})

	w := httptest.NewRecorder()
	// A literal JSON "null" body unmarshals into a nil map without error;
	// ForwardReview must not panic when injecting reviewer_id into it.
	req := httptest.NewRequest(http.MethodPost, "/api/emergency/abc-123/review",
		strings.NewReader(`null`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if gotBody["reviewer_id"] != "dpo@x.local" {
		t.Fatalf("reviewer_id = %v, want dpo@x.local (server-injected)", gotBody["reviewer_id"])
	}
}
