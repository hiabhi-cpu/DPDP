package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hiabhi-cpu/integration-service/pkg/pending/model"
	"github.com/hiabhi-cpu/integration-service/pkg/pending/repository"
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
func (m *mapStore) SetStatus(_ context.Context, hospitalID, hms, status string) error {
	for i := range m.recs {
		if m.recs[i].HospitalID == hospitalID && m.recs[i].HMSPatientID == hms {
			m.recs[i].Status = status
			return nil
		}
	}
	return repository.ErrNotFound
}

// fakeChecker is a ConsentChecker for read tests.
type fakeChecker struct {
	active     map[string]bool
	err        error
	gotAuth    string
	gotMobiles []string
	calls      int
}

func (f *fakeChecker) ActiveMobiles(_ context.Context, authHeader string, mobiles []string) (map[string]bool, error) {
	f.calls++
	f.gotAuth = authHeader
	f.gotMobiles = mobiles
	return f.active, f.err
}

// readRouter injects a fixed hospital id (simulating middleware.JWTAuth).
func readRouter(store PendingStore, checker ConsentChecker, hospitalID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewReadHandler(store, checker)
	grp := r.Group("/internal/v1")
	grp.Use(func(c *gin.Context) { c.Set(middleware.CtxHospitalID, hospitalID); c.Next() })
	grp.GET("/registrations", h.List)
	grp.GET("/registrations/:hms_patient_id", h.Get)
	grp.POST("/registrations/:hms_patient_id/status", h.SetStatus)
	return r
}

func TestList_MasksMobileAndScopesByHospital(t *testing.T) {
	store := &mapStore{recs: []model.PendingRegistration{
		{HospitalID: "hosp-1", HMSPatientID: "PA-1", Name: "Asha", Mobile: "9876543210"},
		{HospitalID: "hosp-2", HMSPatientID: "PA-9", Name: "Other", Mobile: "9000000000"},
	}}
	r := readRouter(store, &fakeChecker{}, "hosp-1")
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
	r := readRouter(store, &fakeChecker{}, "hosp-1")
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
	r := readRouter(&mapStore{}, &fakeChecker{}, "hosp-1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/internal/v1/registrations/nope", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestSetStatus_UpdatesAndRejectsBadValue(t *testing.T) {
	store := &mapStore{recs: []model.PendingRegistration{
		{HospitalID: "hosp-1", HMSPatientID: "PA-1", Name: "Asha", Mobile: "9876543210", Status: "PENDING"},
	}}
	r := readRouter(store, &fakeChecker{}, "hosp-1")

	// bad status → 400
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/internal/v1/registrations/PA-1/status",
		strings.NewReader(`{"status":"NOPE"}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad status code = %d, want 400", w.Code)
	}
	// valid → 200 and mutation
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/internal/v1/registrations/PA-1/status",
		strings.NewReader(`{"status":"CODE_SENT"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("valid status code = %d, want 200", w.Code)
	}
	if store.recs[0].Status != "CODE_SENT" {
		t.Fatalf("status = %q, want CODE_SENT", store.recs[0].Status)
	}
}

// TestList_FlagsConsentedRows verifies the queue is told which patients already
// consented, and that the lookup is keyed by MOBILE — not hms_patient_id, which
// is not what capture blocks on.
func TestList_FlagsConsentedRows(t *testing.T) {
	store := &mapStore{recs: []model.PendingRegistration{
		{HospitalID: "hosp-1", HMSPatientID: "PA-1", Name: "Asha", Mobile: "9876543210"},
		{HospitalID: "hosp-1", HMSPatientID: "PA-2", Name: "Ravi", Mobile: "9000000000"},
	}}
	checker := &fakeChecker{active: map[string]bool{"9876543210": true}}
	r := readRouter(store, checker, "hosp-1")

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/registrations", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var items []listItem
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	for _, it := range items {
		want := it.HMSPatientID == "PA-1"
		if it.Consented != want {
			t.Fatalf("%s consented = %v, want %v", it.HMSPatientID, it.Consented, want)
		}
	}
	// Raw mobiles are the lookup key; masked ones would never match.
	if len(checker.gotMobiles) != 2 {
		t.Fatalf("checker got %v, want both raw mobiles", checker.gotMobiles)
	}
	for _, m := range checker.gotMobiles {
		if strings.Contains(m, "*") {
			t.Fatalf("masked mobile %q sent to consent lookup — cannot match", m)
		}
	}
	// The caller's hospital JWT is forwarded as-is.
	if checker.gotAuth != "Bearer test-token" {
		t.Fatalf("forwarded auth = %q, want %q", checker.gotAuth, "Bearer test-token")
	}
}

// TestList_ConsentLookupFailureFailsOpen is the load-bearing degradation test: a
// consent-service outage must leave the queue usable and unbadged, never empty.
func TestList_ConsentLookupFailureFailsOpen(t *testing.T) {
	store := &mapStore{recs: []model.PendingRegistration{
		{HospitalID: "hosp-1", HMSPatientID: "PA-1", Name: "Asha", Mobile: "9876543210"},
	}}
	checker := &fakeChecker{err: errors.New("consent-service down")}
	r := readRouter(store, checker, "hosp-1")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/internal/v1/registrations", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a consent blip must not fail the queue", w.Code)
	}
	var items []listItem
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1 — the board must never empty on a consent outage", len(items))
	}
	if items[0].Consented {
		t.Fatalf("consented = true on lookup failure, want false (fail open)")
	}
}

// TestList_NoRecordsSkipsConsentLookup guards the short-circuit — an empty queue
// must not fire a pointless request every poll.
func TestList_NoRecordsSkipsConsentLookup(t *testing.T) {
	checker := &fakeChecker{}
	r := readRouter(&mapStore{}, checker, "hosp-1")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/internal/v1/registrations", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if checker.calls != 0 {
		t.Fatalf("consent lookup called %d times for an empty queue, want 0", checker.calls)
	}
}
