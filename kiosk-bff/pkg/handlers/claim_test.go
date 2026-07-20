package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestResolveCombinesSessionAndName(t *testing.T) {
	gin.SetMode(gin.TestMode)
	notification := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/otp/claim/resolve" {
			t.Errorf("notification path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"session_id":"sess-1","mobile":"9876543210","ref":"PA-1"}`))
	}))
	defer notification.Close()
	integration := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/registrations/PA-1" {
			t.Errorf("integration path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"hms_patient_id":"PA-1","name":"Asha Rao","mobile":"9876543210","status":"CODE_SENT"}`))
	}))
	defer integration.Close()

	h := NewClaimHandler(notification.URL, integration.URL, "http://unused", StubProvider("jwt"))
	r := gin.New()
	r.POST("/kiosk/api/claim/resolve", h.Resolve)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/kiosk/api/claim/resolve", strings.NewReader(`{"otp":"123456"}`)))

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", w.Code, w.Body.String())
	}
	var got map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["session_id"] != "sess-1" || got["mobile"] != "9876543210" || got["name"] != "Asha Rao" || got["hms_patient_id"] != "PA-1" {
		t.Fatalf("resolve body = %s", w.Body.String())
	}
}

func TestResolveFailurePipesError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	notification := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"code not recognized"}`))
	}))
	defer notification.Close()

	h := NewClaimHandler(notification.URL, "http://unused", "http://unused", StubProvider("jwt"))
	r := gin.New()
	r.POST("/kiosk/api/claim/resolve", h.Resolve)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/kiosk/api/claim/resolve", strings.NewReader(`{"otp":"000000"}`)))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", w.Code)
	}
}

func TestResolveNameLookupFailureIsNonFatal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	notification := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"session_id":"sess-1","mobile":"9876543210","ref":"PA-1"}`))
	}))
	defer notification.Close()
	integration := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer integration.Close()

	h := NewClaimHandler(notification.URL, integration.URL, "http://unused", StubProvider("jwt"))
	r := gin.New()
	r.POST("/kiosk/api/claim/resolve", h.Resolve)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/kiosk/api/claim/resolve", strings.NewReader(`{"otp":"123456"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (name lookup failure must not fail resolve)", w.Code)
	}
	var got map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["session_id"] != "sess-1" || got["name"] != "" {
		t.Fatalf("expected empty name fallback, got %s", w.Body.String())
	}
}

func TestCaptureFiresDoneOn201(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// markDone runs in a detached goroutine (best-effort, must not block the
	// patient's response), so wait for it on a channel rather than reading a bool.
	done := make(chan struct{}, 1)
	consent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"c-1","hms_patient_id":"PA-1"}`))
	}))
	defer consent.Close()
	integration := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/internal/v1/registrations/PA-1/status" {
			b, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(b), "DONE") {
				t.Errorf("status body = %s", b)
			}
			done <- struct{}{}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer integration.Close()

	h := NewClaimHandler("http://unused", integration.URL, consent.URL, StubProvider("jwt"))
	r := gin.New()
	r.POST("/kiosk/api/consent/capture", h.Capture)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/kiosk/api/consent/capture",
		strings.NewReader(`{"mobile":"9876543210","session_id":"sess-1","hms_patient_id":"PA-1","purposes":["treatment"]}`)))

	if w.Code != http.StatusCreated {
		t.Fatalf("code = %d, want 201", w.Code)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected integration DONE status call after 201 capture")
	}
}

// A capture that the kiosk retried after losing the first response comes back
// 200, not 201: consent-service replays the idempotency key and returns the
// original row. The consent IS saved, so the reception queue must still be
// cleared — otherwise staff keep chasing a patient who already consented.
func TestCaptureFiresDoneOn200Replay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	done := make(chan struct{}, 1)
	consent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"c-1","hms_patient_id":"PA-1"}`))
	}))
	defer consent.Close()
	integration := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/internal/v1/registrations/PA-1/status" {
			done <- struct{}{}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer integration.Close()

	h := NewClaimHandler("http://unused", integration.URL, consent.URL, StubProvider("jwt"))
	r := gin.New()
	r.POST("/kiosk/api/consent/capture", h.Capture)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/kiosk/api/consent/capture",
		strings.NewReader(`{"mobile":"9876543210","session_id":"sess-1","hms_patient_id":"PA-1","purposes":["treatment"]}`)))

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", w.Code)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected integration DONE status call after a 200 replay capture")
	}
}

// A capture that genuinely failed must NOT clear the queue — the patient still
// needs the front desk.
func TestCaptureDoesNotFireDoneOnFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	called := make(chan struct{}, 1)
	consent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"otp session invalid or expired"}`))
	}))
	defer consent.Close()
	integration := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/status") {
			called <- struct{}{}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer integration.Close()

	h := NewClaimHandler("http://unused", integration.URL, consent.URL, StubProvider("jwt"))
	r := gin.New()
	r.POST("/kiosk/api/consent/capture", h.Capture)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/kiosk/api/consent/capture",
		strings.NewReader(`{"mobile":"9876543210","session_id":"sess-1","hms_patient_id":"PA-1","purposes":["treatment"]}`)))

	if w.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", w.Code)
	}
	select {
	case <-called:
		t.Fatal("DONE status must not be marked when the capture failed")
	case <-time.After(200 * time.Millisecond):
	}
}
