package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hiabhi-cpu/integration-service/pkg/pending/model"
	"github.com/hiabhi-cpu/shared/middleware"
)

// mapStore is an in-memory PendingStore for read tests.
type mapStore struct{ recs []model.PendingRegistration }

func (m *mapStore) Upsert(context.Context, model.PendingRegistration) error { return nil }
func (m *mapStore) Get(_ context.Context, hospitalID, hms string) (*model.PendingRegistration, error) {
	for i := range m.recs {
		if m.recs[i].HospitalID == hospitalID && m.recs[i].HMSPatientID == hms {
			return &m.recs[i], nil
		}
	}
	return nil, nil
}
func (m *mapStore) List(_ context.Context, hospitalID string) ([]model.PendingRegistration, error) {
	var out []model.PendingRegistration
	for _, r := range m.recs {
		if r.HospitalID == hospitalID {
			out = append(out, r)
		}
	}
	return out, nil
}

// readRouter injects a fixed hospital id (simulating middleware.JWTAuth).
func readRouter(store PendingStore, hospitalID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewReadHandler(store)
	grp := r.Group("/internal/v1")
	grp.Use(func(c *gin.Context) { c.Set(middleware.CtxHospitalID, hospitalID); c.Next() })
	grp.GET("/registrations", h.List)
	grp.GET("/registrations/:hms_patient_id", h.Get)
	return r
}

func TestList_MasksMobileAndScopesByHospital(t *testing.T) {
	store := &mapStore{recs: []model.PendingRegistration{
		{HospitalID: "hosp-1", HMSPatientID: "PA-1", Name: "Asha", Mobile: "9876543210"},
		{HospitalID: "hosp-2", HMSPatientID: "PA-9", Name: "Other", Mobile: "9000000000"},
	}}
	r := readRouter(store, "hosp-1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/internal/v1/registrations", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got []map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (hosp-2 leaked?)", len(got))
	}
	if got[0]["mobile"] != "98****3210" {
		t.Errorf("mobile = %v, want masked 98****3210", got[0]["mobile"])
	}
}

func TestGet_ReturnsRawMobile(t *testing.T) {
	store := &mapStore{recs: []model.PendingRegistration{
		{HospitalID: "hosp-1", HMSPatientID: "PA-1", Name: "Asha", Mobile: "9876543210"},
	}}
	r := readRouter(store, "hosp-1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/internal/v1/registrations/PA-1", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "9876543210") {
		t.Errorf("body missing raw mobile: %s", w.Body.String())
	}
}

func TestGet_UnknownReturns404(t *testing.T) {
	r := readRouter(&mapStore{}, "hosp-1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/internal/v1/registrations/nope", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}
