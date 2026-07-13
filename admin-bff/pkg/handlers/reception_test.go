package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	bffmw "github.com/hiabhi-cpu/admin-bff/pkg/middleware"
	"github.com/hiabhi-cpu/admin-bff/pkg/session"
)

type stubToken string

func (s stubToken) Token(context.Context) (string, error) { return string(s), nil }

func TestSendCodeOrchestration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var gotClaimBody, gotStatusBody string

	integration := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/internal/v1/registrations/PA-1":
			_, _ = w.Write([]byte(`{"hms_patient_id":"PA-1","name":"Asha","mobile":"9876543210","status":"PENDING"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/internal/v1/registrations/PA-1/status":
			b, _ := io.ReadAll(r.Body)
			gotStatusBody = string(b)
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected integration call %s %s", r.Method, r.URL.Path)
		}
	}))
	defer integration.Close()

	notification := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/otp/claim/send" {
			t.Errorf("unexpected notification call %s", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		gotClaimBody = string(b)
		_, _ = w.Write([]byte(`{"reference_id":"ref-1"}`))
	}))
	defer notification.Close()

	h := NewReceptionHandler(integration.URL, notification.URL, stubToken("jwt"))

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(bffmw.CtxUser, session.Session{Role: "reception", HospitalID: "hosp-1"})
		c.Next()
	})
	r.POST("/api/v1/reception/registrations/:hms/send-code", h.SendCode)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/reception/registrations/PA-1/send-code", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", w.Code, w.Body.String())
	}
	var claim map[string]string
	_ = json.Unmarshal([]byte(gotClaimBody), &claim)
	if claim["mobile"] != "9876543210" || claim["ref"] != "PA-1" {
		t.Fatalf("claim body = %s", gotClaimBody)
	}
	if gotStatusBody == "" || !json.Valid([]byte(gotStatusBody)) {
		t.Fatalf("status not set: %q", gotStatusBody)
	}
}
