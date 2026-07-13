package controller

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hiabhi-cpu/integration-service/pkg/pending/model"
)

// fakeStore records the last Upsert for assertions.
type fakeStore struct{ last *model.PendingRegistration }

func (f *fakeStore) Upsert(_ context.Context, r model.PendingRegistration) error {
	f.last = &r
	return nil
}
func (f *fakeStore) Get(context.Context, string, string) (*model.PendingRegistration, error) {
	return nil, nil
}
func (f *fakeStore) List(context.Context, string) ([]model.PendingRegistration, error) {
	return nil, nil
}
func (f *fakeStore) SetStatus(context.Context, string, string, string) error {
	return nil
}

// mtlsServer starts a gin router behind RequireAndVerifyClientCert.
func mtlsServer(t *testing.T, pki *testPKI, store PendingStore) *httptest.Server {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	h := NewWebhookHandler(store)
	r.POST("/webhook/patient-registered", h.PatientRegistered)

	srv := httptest.NewUnstartedServer(r)
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{pki.serverTL},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pki.caPool,
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func clientWithCert(pki *testPKI, cert tls.Certificate) *http.Client {
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs:      pki.caPool,
		Certificates: []tls.Certificate{cert},
	}}}
}

func TestWebhook_StoresWithHospitalFromCert(t *testing.T) {
	pki := newTestPKI(t)
	store := &fakeStore{}
	srv := mtlsServer(t, pki, store)
	client := clientWithCert(pki, pki.clientCertFor(t, "hosp-42"))

	body := `{"patientId":"PA-7","givenName":"Asha","familyName":"Rao","phoneNumber":"9876543210"}`
	resp, err := client.Post(srv.URL+"/webhook/patient-registered", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if store.last == nil {
		t.Fatal("nothing stored")
	}
	if store.last.HospitalID != "hosp-42" {
		t.Errorf("HospitalID = %q, want hosp-42 (from cert CN)", store.last.HospitalID)
	}
	if store.last.HMSPatientID != "PA-7" {
		t.Errorf("HMSPatientID = %q, want PA-7", store.last.HMSPatientID)
	}
}

func TestWebhook_RejectsBadPayload(t *testing.T) {
	pki := newTestPKI(t)
	srv := mtlsServer(t, pki, &fakeStore{})
	client := clientWithCert(pki, pki.clientCertFor(t, "hosp-42"))

	resp, err := client.Post(srv.URL+"/webhook/patient-registered", "application/json",
		strings.NewReader(`{"patientId":"PA-7"}`)) // no phone
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestWebhook_RejectsNoClientCert(t *testing.T) {
	pki := newTestPKI(t)
	srv := mtlsServer(t, pki, &fakeStore{})
	// Client trusts the CA but presents NO client cert.
	noCert := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pki.caPool}}}

	_, err := noCert.Post(srv.URL+"/webhook/patient-registered", "application/json",
		strings.NewReader(`{}`))
	if err == nil {
		t.Fatal("expected TLS handshake error without a client cert, got nil")
	}
}
