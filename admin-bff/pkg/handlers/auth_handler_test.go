package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hiabhi-cpu/admin-bff/pkg/auth"
	"github.com/hiabhi-cpu/admin-bff/pkg/httpx"
	"github.com/hiabhi-cpu/admin-bff/pkg/session"
)

type fakeUserRepo struct {
	user *auth.AdminUser
}

func (f *fakeUserRepo) GetByEmail(_ context.Context, email string) (*auth.AdminUser, error) {
	if f.user != nil && strings.EqualFold(f.user.Email, email) {
		return f.user, nil
	}
	return nil, auth.ErrUserNotFound
}

func newAuthRouter(t *testing.T, repo auth.UserRepository) (*gin.Engine, session.Store) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	store := session.NewMemStore()
	h := NewAuthHandler(repo, store, time.Hour, httpx.CookieConfig{Secure: false})
	r := gin.New()
	r.POST("/api/session", h.Login)
	r.DELETE("/api/session", h.Logout)
	r.GET("/api/me", h.Me)
	return r, store
}

func seededRepo(t *testing.T) *fakeUserRepo {
	t.Helper()
	hash, err := auth.HashPassword("good-pw")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	return &fakeUserRepo{user: &auth.AdminUser{
		ID: "u1", HospitalID: "h1", Email: "admin@x.local",
		PasswordHash: hash, Role: "admin",
	}}
}

func TestLoginSuccessSetsCookie(t *testing.T) {
	r, _ := newAuthRouter(t, seededRepo(t))
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/session",
		strings.NewReader(`{"email":"admin@x.local","password":"good-pw"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var gotSession bool
	for _, ck := range w.Result().Cookies() {
		if ck.Name == httpx.SessionCookieName && ck.Value != "" {
			gotSession = true
		}
	}
	if !gotSession {
		t.Fatal("no session cookie set on success")
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["email"] != "admin@x.local" || body["role"] != "admin" {
		t.Fatalf("body = %v, want email+role", body)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	r, _ := newAuthRouter(t, seededRepo(t))
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/session",
		strings.NewReader(`{"email":"admin@x.local","password":"nope"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", w.Code)
	}
}

func TestLoginDisabledUser(t *testing.T) {
	repo := seededRepo(t)
	repo.user.Disabled = true
	r, _ := newAuthRouter(t, repo)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/session",
		strings.NewReader(`{"email":"admin@x.local","password":"good-pw"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("disabled login code = %d, want 401", w.Code)
	}
}
