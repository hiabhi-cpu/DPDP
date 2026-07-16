package consent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
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

func TestActiveMobiles_ChunksAt200AndMergesResults(t *testing.T) {
	// 250 unique mobiles: split into a 200 chunk and a 50 chunk. The active
	// mobile in each chunk must survive the merge.
	mobiles := make([]string, 250)
	for i := range mobiles {
		mobiles[i] = fmt.Sprintf("9%09d", i)
	}
	activeInFirstChunk := mobiles[0]    // index 0, in the first chunk
	activeInSecondChunk := mobiles[210] // index 210, in the second chunk

	var mu sync.Mutex
	var requests [][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Mobiles []string `json:"mobiles"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("server: decode request body: %v", err)
		}

		mu.Lock()
		requests = append(requests, body.Mobiles)
		mu.Unlock()

		var active []string
		for _, m := range body.Mobiles {
			if m == activeInFirstChunk || m == activeInSecondChunk {
				active = append(active, m)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		resp, _ := json.Marshal(struct {
			Active []string `json:"active"`
		}{Active: active})
		_, _ = w.Write(resp)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	got, err := c.ActiveMobiles(context.Background(), "Bearer test-token", mobiles)
	if err != nil {
		t.Fatalf("ActiveMobiles returned error: %v", err)
	}

	if len(requests) != 2 {
		t.Fatalf("server received %d requests, want 2 (one per chunk)", len(requests))
	}
	if len(requests[0]) != 200 {
		t.Fatalf("first request carried %d mobiles, want 200", len(requests[0]))
	}
	if len(requests[1]) != 50 {
		t.Fatalf("second request carried %d mobiles, want 50", len(requests[1]))
	}

	if !got[activeInFirstChunk] {
		t.Fatalf("active[%s] (from chunk 1) = false, want true", activeInFirstChunk)
	}
	if !got[activeInSecondChunk] {
		t.Fatalf("active[%s] (from chunk 2) = false, want true", activeInSecondChunk)
	}
	if len(got) != 2 {
		t.Fatalf("merged active map has %d entries, want 2", len(got))
	}
}

func TestActiveMobiles_DedupesBeforeSending(t *testing.T) {
	var gotMobiles []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Mobiles []string `json:"mobiles"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("server: decode request body: %v", err)
		}
		gotMobiles = body.Mobiles
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"active":["9876543210"]}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	// The same mobile twice — e.g. two staged records for family members sharing
	// a phone — must be sent once.
	got, err := c.ActiveMobiles(context.Background(), "Bearer test-token", []string{"9876543210", "9876543210"})
	if err != nil {
		t.Fatalf("ActiveMobiles returned error: %v", err)
	}
	if len(gotMobiles) != 1 {
		t.Fatalf("request carried %d mobiles, want 1 (deduped): %v", len(gotMobiles), gotMobiles)
	}
	if !got["9876543210"] {
		t.Fatalf("active[9876543210] = false, want true")
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
