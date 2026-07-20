package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hiabhi-cpu/consent-service/pkg/consent/model"
	"github.com/hiabhi-cpu/consent-service/pkg/consent/service"
	"github.com/hiabhi-cpu/shared/middleware"
)

// stubCheckService records whether Check was reached. Embedding the interface
// means any other method would panic — this test must not exercise them.
type stubCheckService struct {
	service.ConsentService
	called bool
}

func (s *stubCheckService) Check(_ context.Context, _, _ string, _ *model.CheckConsentRequest) (*model.CheckConsentResponse, error) {
	s.called = true
	return &model.CheckConsentResponse{}, nil
}

// A mobile names a household, not a person — families share one number. A check
// that supplies only a mobile cannot say which patient it is asking about, so it
// must be rejected outright rather than answered for whichever family member
// consented most recently.
func TestCheckRejectsMobileOnlyRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &stubCheckService{}
	h := NewConsentHandler(stub)

	r := gin.New()
	r.POST("/check", func(c *gin.Context) {
		c.Set(middleware.CtxHospitalID, "hosp-1")
	}, h.Check)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/check",
		strings.NewReader(`{"mobile":"9876543210","purpose":"treatment"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 — a mobile-only check does not name a patient", w.Code)
	}
	if stub.called {
		t.Fatal("service was reached; a request that names no patient must be rejected at the boundary")
	}
}

// The doctor/HMS path is the supported one and must still work.
func TestCheckAcceptsHMSPatientID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &stubCheckService{}
	h := NewConsentHandler(stub)

	r := gin.New()
	r.POST("/check", func(c *gin.Context) {
		c.Set(middleware.CtxHospitalID, "hosp-1")
	}, h.Check)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/check",
		strings.NewReader(`{"hms_patient_id":"PA-son","purpose":"treatment"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !stub.called {
		t.Fatal("service was not reached for a well-formed check")
	}
}
