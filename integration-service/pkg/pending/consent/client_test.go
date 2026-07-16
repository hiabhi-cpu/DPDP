package consent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestActiveMobiles_HappyPath(t *testing.T) {
	var gotPath, gotAuth, gotContentType string
	var gotBody struct {
		Mobiles []string `json:"mobiles"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("server: decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"active":["9876543210"]}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	active, err := c.ActiveMobiles(context.Background(), "Bearer test-token", []string{"9876543210", "9000000000"})
	if err != nil {
		t.Fatalf("ActiveMobiles returned error: %v", err)
	}
	if !active["9876543210"] {
		t.Fatalf("active[9876543210] = %v, want true", active["9876543210"])
	}
	if active["9000000000"] {
		t.Fatalf("active[9000000000] = %v, want false/absent", active["9000000000"])
	}

	if gotPath != "/api/v1/consent/active" {
		t.Fatalf("request path = %q, want /api/v1/consent/active", gotPath)
	}
	if gotAuth != "Bearer test-token" {
		t.Fatalf("Authorization header = %q, want %q", gotAuth, "Bearer test-token")
	}
	if gotContentType != "application/json" {
		t.Fatalf("Content-Type header = %q, want application/json", gotContentType)
	}
	if len(gotBody.Mobiles) != 2 || gotBody.Mobiles[0] != "9876543210" || gotBody.Mobiles[1] != "9000000000" {
		t.Fatalf("request body mobiles = %v, want [9876543210 9000000000]", gotBody.Mobiles)
	}
}

func TestActiveMobiles_NonOKStatusReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	_, err := c.ActiveMobiles(context.Background(), "Bearer test-token", []string{"9876543210"})
	if err == nil {
		t.Fatalf("ActiveMobiles returned nil error for a 500 response, want non-nil")
	}
}

func TestActiveMobiles_MalformedBodyReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not valid json`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	_, err := c.ActiveMobiles(context.Background(), "Bearer test-token", []string{"9876543210"})
	if err == nil {
		t.Fatalf("ActiveMobiles returned nil error for a malformed body, want non-nil")
	}
}
