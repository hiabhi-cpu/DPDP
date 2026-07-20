package consent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

func TestActiveHMSPatientIDs_HappyPath(t *testing.T) {
	var gotPath, gotAuth, gotContentType string
	var gotBody struct {
		HMSPatientIDs []string `json:"hms_patient_ids"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("server: decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"active":["PA-mother"]}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	active, err := c.ActiveHMSPatientIDs(context.Background(), "Bearer test-token", []string{"PA-mother", "PA-son"})
	if err != nil {
		t.Fatalf("ActiveHMSPatientIDs returned error: %v", err)
	}
	if !active["PA-mother"] {
		t.Fatalf("active[PA-mother] = %v, want true", active["PA-mother"])
	}
	if active["PA-son"] {
		t.Fatalf("active[PA-son] = %v, want false/absent", active["PA-son"])
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
	if len(gotBody.HMSPatientIDs) != 2 || gotBody.HMSPatientIDs[0] != "PA-mother" || gotBody.HMSPatientIDs[1] != "PA-son" {
		t.Fatalf("request body hms_patient_ids = %v, want [PA-mother PA-son]", gotBody.HMSPatientIDs)
	}
}

func TestActiveHMSPatientIDs_NonOKStatusReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A valid, decodable body on the 500 isolates the branch under test: only
		// the status check can fail this test. (An empty body would ALSO fail
		// json.Decode with io.EOF, so the test would pass even if the status check
		// were deleted — that's not what we're testing here.)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"active":[]}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	_, err := c.ActiveHMSPatientIDs(context.Background(), "Bearer test-token", []string{"PA-mother"})
	if err == nil {
		t.Fatalf("ActiveHMSPatientIDs returned nil error for a 500 response, want non-nil")
	}
}

func TestActiveHMSPatientIDs_ChunksAt200AndMergesResults(t *testing.T) {
	// 250 unique HMS patient IDs: split into a 200 chunk and a 50 chunk. The active
	// mobile in each chunk must survive the merge.
	ids := make([]string, 250)
	for i := range ids {
		ids[i] = fmt.Sprintf("PA-%05d", i)
	}
	activeInFirstChunk := ids[0]    // index 0, in the first chunk
	activeInSecondChunk := ids[210] // index 210, in the second chunk

	var mu sync.Mutex
	var requests [][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			HMSPatientIDs []string `json:"hms_patient_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("server: decode request body: %v", err)
		}

		mu.Lock()
		requests = append(requests, body.HMSPatientIDs)
		mu.Unlock()

		var active []string
		for _, m := range body.HMSPatientIDs {
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
	got, err := c.ActiveHMSPatientIDs(context.Background(), "Bearer test-token", ids)
	if err != nil {
		t.Fatalf("ActiveHMSPatientIDs returned error: %v", err)
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

func TestActiveHMSPatientIDs_DedupesBeforeSending(t *testing.T) {
	var gotIDs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			HMSPatientIDs []string `json:"hms_patient_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("server: decode request body: %v", err)
		}
		gotIDs = body.HMSPatientIDs
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"active":["PA-mother"]}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	// The same mobile twice — e.g. two staged records for family members sharing
	// a phone — must be sent once.
	got, err := c.ActiveHMSPatientIDs(context.Background(), "Bearer test-token", []string{"PA-mother", "PA-mother"})
	if err != nil {
		t.Fatalf("ActiveHMSPatientIDs returned error: %v", err)
	}
	if len(gotIDs) != 1 {
		t.Fatalf("request carried %d ids, want 1 (deduped): %v", len(gotIDs), gotIDs)
	}
	if !got["PA-mother"] {
		t.Fatalf("active[PA-mother] = false, want true")
	}
}

func TestActiveHMSPatientIDs_CallerCtxCancelledReturnsError(t *testing.T) {
	// Proves ctx is actually threaded into the chunk requests (not silently
	// dropped in favour of only the client's own timeout): with the caller's
	// ctx already expired, the very first request must fail fast rather than
	// hang or succeed.
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"active":[]}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	<-ctx.Done() // guarantee it's expired before ActiveHMSPatientIDs ever sees it

	c := NewClient(srv.URL)
	_, err := c.ActiveHMSPatientIDs(ctx, "Bearer test-token", []string{"PA-mother"})
	if err == nil {
		t.Fatalf("ActiveHMSPatientIDs returned nil error for an already-expired ctx, want non-nil")
	}
	if got := atomic.LoadInt32(&requests); got > 1 {
		t.Fatalf("server received %d requests, want at most 1", got)
	}
}

func TestActiveHMSPatientIDs_MalformedBodyReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not valid json`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	_, err := c.ActiveHMSPatientIDs(context.Background(), "Bearer test-token", []string{"PA-mother"})
	if err == nil {
		t.Fatalf("ActiveHMSPatientIDs returned nil error for a malformed body, want non-nil")
	}
}
