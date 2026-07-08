package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHospitalTokenClientFetchesAndCaches(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/auth/token" {
			t.Errorf("path = %s, want /v1/auth/token", r.URL.Path)
		}
		var body struct {
			APIKey string `json:"api_key"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.APIKey != "raw-key" {
			t.Errorf("api_key = %q, want raw-key", body.APIKey)
		}
		calls++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "jwt-123",
			"expires_at": time.Now().Add(time.Hour),
		})
	}))
	defer srv.Close()

	c := NewHospitalTokenClient(srv.URL, "raw-key")

	tok, err := c.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "jwt-123" {
		t.Fatalf("token = %q, want jwt-123", tok)
	}
	// Second call within the validity window must be served from cache.
	if _, err := c.Token(context.Background()); err != nil {
		t.Fatalf("Token(2): %v", err)
	}
	if calls != 1 {
		t.Fatalf("auth-service called %d times, want 1 (cache miss)", calls)
	}
}
