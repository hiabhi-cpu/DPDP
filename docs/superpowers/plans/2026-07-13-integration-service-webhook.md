# integration-service (Spec A) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `integration-service` — an mTLS webhook receiver that turns a hospital HMS "patient registered" event into a short-lived, pre-filled consent-staging record other services can read.

**Architecture:** One new Go service, one process, **two listeners**: (1) a mutual-TLS `http.Server` for the public webhook where the client cert both authenticates and identifies the hospital, and (2) a normal gin app under `/internal` (hospital-JWT auth) for consumers to read pending records. State lives only in Redis with a 72h TTL — no Postgres, no vault write. A pure Bahmni adapter maps the incoming payload to our `PendingRegistration`.

**Tech Stack:** Go 1.25, gin, `github.com/redis/go-redis/v9`, `crypto/tls` + `crypto/x509` (mTLS), logrus, the repo's `shared/middleware` (JWT) and `shared/logging`. Tests use `github.com/alicebob/miniredis/v2` (new test-only dep) for a hermetic Redis and `crypto/x509` to mint certs in-test.

## Global Constraints

- Module path: `github.com/hiabhi-cpu/integration-service`; `replace github.com/hiabhi-cpu/shared => ../shared`; `go 1.25`.
- Service template mirrors `kiosk-bff`/`notification-service`: `bootstrap/`, `cmd/server/main.go`, `pkg/routes/`, `pkg/<domain>/{controller,service,repository,model}`.
- **Raw mobile numbers never appear in logs or URLs.** Mask (`98****3210`) whenever surfaced; the internal `list` endpoint returns masked mobiles, `get` returns the raw mobile (trusted consumer needs it for OTP).
- **Hospital identity is taken from credentials, never the request body.** Webhook: the mTLS client cert's CN. Internal reads: the hospital JWT's `hospital_id` claim (`middleware.CtxHospitalID`).
- Redis key shape: `pending:{hospital_id}:{hms_patient_id}`. TTL: **72h**. Upserts are idempotent (last write wins).
- `gin.New()` + `r.SetTrustedProxies(nil)` on every gin engine (audit-IP spoofing guard, same as all services).
- Ports (dev): internal HTTP `9009`, mTLS webhook `9443`.
- No consent-service change: `CaptureConsentRequest` already accepts `mobile` + optional `hms_patient_id`.

---

### Task 1: Module scaffold + PendingRegistration model + Bahmni adapter

The adapter is a pure function (no infra) — the natural first testable deliverable. This task also lays down the module so later tasks compile.

**Files:**
- Create: `integration-service/go.mod`
- Create: `integration-service/pkg/pending/model/pending.go`
- Create: `integration-service/pkg/pending/adapter/bahmni.go`
- Test: `integration-service/pkg/pending/adapter/bahmni_test.go`
- Modify: `go.work` (add `./integration-service` to the `use` block)

**Interfaces:**
- Produces: `model.PendingRegistration{HospitalID, HMSPatientID, Name, Mobile, DOB, RegisteredAt string}`
- Produces: `adapter.FromBahmni(body []byte, hospitalID string, now time.Time) (model.PendingRegistration, error)`

- [ ] **Step 1: Create the module and register it in the workspace**

`integration-service/go.mod`:
```go
module github.com/hiabhi-cpu/integration-service

go 1.25

require github.com/hiabhi-cpu/shared v0.0.0-00010101000000-000000000000

replace github.com/hiabhi-cpu/shared => ../shared
```

Add `./integration-service` to `go.work` (keep the list alphabetical):
```
use (
	./admin-bff
	./audit-service
	./auth-service
	./consent-service
	./emergency-service
	./integration-service
	./kiosk-bff
	./notification-service
	./shared
	./tools/repograph
)
```

- [ ] **Step 2: Write the model**

`integration-service/pkg/pending/model/pending.go`:
```go
package model

// PendingRegistration is a short-lived, pre-consent staging record created from
// an HMS "patient registered" webhook. It holds identity only — no consent is
// implied. Stored in Redis with a TTL; never written to the vault.
type PendingRegistration struct {
	HospitalID   string `json:"hospital_id"`    // from the mTLS client cert, never the body
	HMSPatientID string `json:"hms_patient_id"`
	Name         string `json:"name"`
	Mobile       string `json:"mobile"`         // raw; masked whenever surfaced in a list
	DOB          string `json:"dob,omitempty"`  // optional; feeds the later §9 age-gate
	RegisteredAt string `json:"registered_at"`  // RFC3339, when we received the webhook
}
```

- [ ] **Step 3: Write the failing adapter test**

`integration-service/pkg/pending/adapter/bahmni_test.go`:
```go
package adapter

import (
	"testing"
	"time"
)

func TestFromBahmni_MapsFullPayload(t *testing.T) {
	body := []byte(`{
		"patientId": "PA-00234",
		"givenName": "Asha",
		"familyName": "Rao",
		"phoneNumber": "+91 98765 43210",
		"birthdate": "1990-05-01"
	}`)
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)

	got, err := FromBahmni(body, "hosp-1", now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.HospitalID != "hosp-1" {
		t.Errorf("HospitalID = %q, want hosp-1", got.HospitalID)
	}
	if got.HMSPatientID != "PA-00234" {
		t.Errorf("HMSPatientID = %q, want PA-00234", got.HMSPatientID)
	}
	if got.Name != "Asha Rao" {
		t.Errorf("Name = %q, want 'Asha Rao'", got.Name)
	}
	if got.Mobile != "9876543210" {
		t.Errorf("Mobile = %q, want 9876543210 (normalized)", got.Mobile)
	}
	if got.DOB != "1990-05-01" {
		t.Errorf("DOB = %q, want 1990-05-01", got.DOB)
	}
	if got.RegisteredAt != "2026-07-13T10:00:00Z" {
		t.Errorf("RegisteredAt = %q", got.RegisteredAt)
	}
}

func TestFromBahmni_OmitsDOBWhenAbsent(t *testing.T) {
	body := []byte(`{"patientId":"PA-1","givenName":"A","familyName":"B","phoneNumber":"9876543210"}`)
	got, err := FromBahmni(body, "hosp-1", time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.DOB != "" {
		t.Errorf("DOB = %q, want empty", got.DOB)
	}
}

func TestFromBahmni_RejectsMissingMobile(t *testing.T) {
	body := []byte(`{"patientId":"PA-1","givenName":"A","familyName":"B"}`)
	if _, err := FromBahmni(body, "hosp-1", time.Now()); err == nil {
		t.Fatal("expected error for missing mobile, got nil")
	}
}

func TestFromBahmni_RejectsShortMobile(t *testing.T) {
	body := []byte(`{"patientId":"PA-1","givenName":"A","familyName":"B","phoneNumber":"12345"}`)
	if _, err := FromBahmni(body, "hosp-1", time.Now()); err == nil {
		t.Fatal("expected error for short mobile, got nil")
	}
}

func TestFromBahmni_RejectsMissingPatientID(t *testing.T) {
	body := []byte(`{"givenName":"A","familyName":"B","phoneNumber":"9876543210"}`)
	if _, err := FromBahmni(body, "hosp-1", time.Now()); err == nil {
		t.Fatal("expected error for missing patientId, got nil")
	}
}
```

- [ ] **Step 4: Run the test to verify it fails**

Run: `cd integration-service && go test ./pkg/pending/adapter/ -run TestFromBahmni -v`
Expected: FAIL — `undefined: FromBahmni` (build error).

- [ ] **Step 5: Implement the adapter**

`integration-service/pkg/pending/adapter/bahmni.go`:
```go
// Package adapter maps HMS-specific registration payloads onto our neutral
// PendingRegistration. Bahmni is the first (and currently only) adapter; a
// source-based dispatch is added when a second HMS lands (P4). The envelope
// below is the documented contract we require the hospital's Bahmni webhook to
// emit; the exact Bahmni-side wiring (module/Atom-feed) is confirmed at pilot.
package adapter

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hiabhi-cpu/integration-service/pkg/pending/model"
)

// bahmniPayload is the JSON shape we accept on POST /webhook/patient-registered.
type bahmniPayload struct {
	PatientID   string `json:"patientId"`
	GivenName   string `json:"givenName"`
	FamilyName  string `json:"familyName"`
	PhoneNumber string `json:"phoneNumber"`
	Birthdate   string `json:"birthdate"`
}

// FromBahmni parses and validates a Bahmni registration payload. hospitalID is
// supplied by the caller (from the mTLS client cert), never read from the body.
func FromBahmni(body []byte, hospitalID string, now time.Time) (model.PendingRegistration, error) {
	var p bahmniPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return model.PendingRegistration{}, fmt.Errorf("adapter: invalid JSON: %w", err)
	}
	if p.PatientID == "" {
		return model.PendingRegistration{}, fmt.Errorf("adapter: patientId is required")
	}
	mobile := normalizeMobile(p.PhoneNumber)
	if len(mobile) != 10 {
		return model.PendingRegistration{}, fmt.Errorf("adapter: phoneNumber must normalize to 10 digits, got %q", mobile)
	}
	name := strings.TrimSpace(p.GivenName + " " + p.FamilyName)
	if name == "" {
		return model.PendingRegistration{}, fmt.Errorf("adapter: name is required")
	}
	return model.PendingRegistration{
		HospitalID:   hospitalID,
		HMSPatientID: p.PatientID,
		Name:         name,
		Mobile:       mobile,
		DOB:          strings.TrimSpace(p.Birthdate),
		RegisteredAt: now.UTC().Format(time.RFC3339),
	}, nil
}

// normalizeMobile strips non-digits and returns the last 10 digits, so
// "+91 98765 43210" and "9876543210" both become "9876543210".
func normalizeMobile(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	digits := b.String()
	if len(digits) > 10 {
		digits = digits[len(digits)-10:]
	}
	return digits
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd integration-service && go test ./pkg/pending/adapter/ -v`
Expected: PASS (all 5 tests).

- [ ] **Step 7: Commit**

```bash
git add integration-service/go.mod go.work integration-service/pkg/pending/model integration-service/pkg/pending/adapter
git commit -m "feat(integration-service): module scaffold + PendingRegistration model + Bahmni adapter"
```

---

### Task 2: Redis pending store (Upsert / Get / List, 72h TTL)

**Files:**
- Create: `integration-service/pkg/pending/repository/store.go`
- Test: `integration-service/pkg/pending/repository/store_test.go`
- Modify: `integration-service/go.mod` (add `redis/go-redis/v9` + test dep `alicebob/miniredis/v2`)

**Interfaces:**
- Consumes: `model.PendingRegistration`
- Produces:
  - `repository.NewRedisStore(client *redis.Client) *RedisStore`
  - `(*RedisStore).Upsert(ctx, reg model.PendingRegistration) error`
  - `(*RedisStore).Get(ctx, hospitalID, hmsPatientID string) (*model.PendingRegistration, error)` — returns `(nil, nil)` when absent/expired
  - `(*RedisStore).List(ctx, hospitalID string) ([]model.PendingRegistration, error)`
  - `const PendingTTL = 72 * time.Hour`

- [ ] **Step 1: Add dependencies**

Run:
```bash
cd integration-service
go get github.com/redis/go-redis/v9@v9.5.1
go get github.com/alicebob/miniredis/v2@v2.32.1
```
(Versions align with what the workspace already resolves for go-redis; miniredis is test-only.)

- [ ] **Step 2: Write the failing store test**

`integration-service/pkg/pending/repository/store_test.go`:
```go
package repository

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/hiabhi-cpu/integration-service/pkg/pending/model"
)

func newTestStore(t *testing.T) (*RedisStore, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return NewRedisStore(client), mr
}

func sampleReg(hospital, hms string) model.PendingRegistration {
	return model.PendingRegistration{
		HospitalID: hospital, HMSPatientID: hms,
		Name: "Asha Rao", Mobile: "9876543210", RegisteredAt: "2026-07-13T10:00:00Z",
	}
}

func TestUpsertAndGet(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	if err := s.Upsert(ctx, sampleReg("hosp-1", "PA-1")); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := s.Get(ctx, "hosp-1", "PA-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil || got.Mobile != "9876543210" {
		t.Fatalf("got = %+v, want the stored record", got)
	}
}

func TestGetMissingReturnsNilNil(t *testing.T) {
	s, _ := newTestStore(t)
	got, err := s.Get(context.Background(), "hosp-1", "nope")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != nil {
		t.Fatalf("got = %+v, want nil", got)
	}
}

func TestUpsertSetsTTL(t *testing.T) {
	s, mr := newTestStore(t)
	if err := s.Upsert(context.Background(), sampleReg("hosp-1", "PA-1")); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	ttl := mr.TTL("pending:hosp-1:PA-1")
	if ttl <= 0 || ttl > PendingTTL {
		t.Fatalf("ttl = %v, want (0, %v]", ttl, PendingTTL)
	}
}

func TestUpsertIsIdempotentOverwrite(t *testing.T) {
	s := mustStore(t)
	ctx := context.Background()
	_ = s.Upsert(ctx, sampleReg("hosp-1", "PA-1"))
	second := sampleReg("hosp-1", "PA-1")
	second.Name = "Updated Name"
	_ = s.Upsert(ctx, second)
	got, _ := s.Get(ctx, "hosp-1", "PA-1")
	if got.Name != "Updated Name" {
		t.Fatalf("Name = %q, want overwrite", got.Name)
	}
}

func TestListIsHospitalScoped(t *testing.T) {
	s := mustStore(t)
	ctx := context.Background()
	_ = s.Upsert(ctx, sampleReg("hosp-1", "PA-1"))
	_ = s.Upsert(ctx, sampleReg("hosp-1", "PA-2"))
	_ = s.Upsert(ctx, sampleReg("hosp-2", "PA-9"))

	list, err := s.List(ctx, "hosp-1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len(list) = %d, want 2 (hosp-2 must not leak)", len(list))
	}
}

func mustStore(t *testing.T) *RedisStore {
	t.Helper()
	s, _ := newTestStore(t)
	return s
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `cd integration-service && go test ./pkg/pending/repository/ -v`
Expected: FAIL — `undefined: RedisStore` / `NewRedisStore`.

- [ ] **Step 4: Implement the store**

`integration-service/pkg/pending/repository/store.go`:
```go
package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/hiabhi-cpu/integration-service/pkg/pending/model"
)

// PendingTTL is how long a pre-staged registration survives if unused.
const PendingTTL = 72 * time.Hour

// RedisStore persists PendingRegistration records under
// pending:{hospital_id}:{hms_patient_id} with a TTL.
type RedisStore struct {
	client *redis.Client
}

func NewRedisStore(client *redis.Client) *RedisStore {
	return &RedisStore{client: client}
}

func key(hospitalID, hmsPatientID string) string {
	return fmt.Sprintf("pending:%s:%s", hospitalID, hmsPatientID)
}

// Upsert stores (or overwrites) a record with the standard TTL. Idempotent.
func (s *RedisStore) Upsert(ctx context.Context, reg model.PendingRegistration) error {
	b, err := json.Marshal(reg)
	if err != nil {
		return fmt.Errorf("repository.Upsert: marshal: %w", err)
	}
	if err := s.client.Set(ctx, key(reg.HospitalID, reg.HMSPatientID), b, PendingTTL).Err(); err != nil {
		return fmt.Errorf("repository.Upsert: redis set: %w", err)
	}
	return nil
}

// Get returns the record, or (nil, nil) if it is absent or expired.
func (s *RedisStore) Get(ctx context.Context, hospitalID, hmsPatientID string) (*model.PendingRegistration, error) {
	val, err := s.client.Get(ctx, key(hospitalID, hmsPatientID)).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("repository.Get: redis get: %w", err)
	}
	var reg model.PendingRegistration
	if err := json.Unmarshal([]byte(val), &reg); err != nil {
		return nil, fmt.Errorf("repository.Get: unmarshal: %w", err)
	}
	return &reg, nil
}

// List returns all pending records for one hospital.
// ponytail: SCAN over a per-hospital prefix. If a single hospital ever holds
// thousands of concurrent pending records, add a per-hospital index set (SADD)
// — unneeded at pilot scale (a few live registrations at a time).
func (s *RedisStore) List(ctx context.Context, hospitalID string) ([]model.PendingRegistration, error) {
	pattern := fmt.Sprintf("pending:%s:*", hospitalID)
	var out []model.PendingRegistration
	iter := s.client.Scan(ctx, 0, pattern, 100).Iterator()
	for iter.Next(ctx) {
		val, err := s.client.Get(ctx, iter.Val()).Result()
		if err == redis.Nil {
			continue // expired between SCAN and GET
		}
		if err != nil {
			return nil, fmt.Errorf("repository.List: redis get: %w", err)
		}
		var reg model.PendingRegistration
		if err := json.Unmarshal([]byte(val), &reg); err != nil {
			return nil, fmt.Errorf("repository.List: unmarshal: %w", err)
		}
		out = append(out, reg)
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("repository.List: scan: %w", err)
	}
	return out, nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd integration-service && go test ./pkg/pending/repository/ -v`
Expected: PASS (all 5 tests).

- [ ] **Step 6: Commit**

```bash
git add integration-service/go.mod integration-service/go.sum integration-service/pkg/pending/repository
git commit -m "feat(integration-service): Redis pending store (upsert/get/list, 72h TTL)"
```

---

### Task 3: mTLS webhook receiver

Wires the adapter + store behind a mutual-TLS handler that derives `hospital_id` from the client cert's CN. The test mints a CA + server + client cert in-process and drives a real TLS handshake.

**Files:**
- Create: `integration-service/pkg/pending/controller/webhook.go`
- Create: `integration-service/pkg/pending/controller/testcerts_test.go` (in-test cert helpers)
- Test: `integration-service/pkg/pending/controller/webhook_test.go`

**Interfaces:**
- Consumes: `adapter.FromBahmni`, `*repository.RedisStore`
- Produces:
  - `controller.NewWebhookHandler(store PendingStore) *WebhookHandler`
  - `(*WebhookHandler).PatientRegistered(c *gin.Context)` — reads `c.Request.TLS.PeerCertificates[0].Subject.CommonName` as `hospital_id`
  - `type PendingStore interface { Upsert(ctx, model.PendingRegistration) error; Get(...); List(...) }` (so the handler takes an interface, not the concrete store)

- [ ] **Step 1: Define the store interface the handlers depend on**

Add to a new file `integration-service/pkg/pending/controller/deps.go`:
```go
package controller

import (
	"context"

	"github.com/hiabhi-cpu/integration-service/pkg/pending/model"
)

// PendingStore is the slice of the repository the controllers need. Defined here
// (consumer side) so handlers depend on behavior, not the concrete Redis store.
type PendingStore interface {
	Upsert(ctx context.Context, reg model.PendingRegistration) error
	Get(ctx context.Context, hospitalID, hmsPatientID string) (*model.PendingRegistration, error)
	List(ctx context.Context, hospitalID string) ([]model.PendingRegistration, error)
}
```

- [ ] **Step 2: Add the in-test cert helper**

`integration-service/pkg/pending/controller/testcerts_test.go`:
```go
package controller

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"
)

// testPKI mints a CA, a server cert, and client certs signed by that CA, so
// tests can drive a genuine mTLS handshake without touching disk.
type testPKI struct {
	caCert   *x509.Certificate
	caKey    *ecdsa.PrivateKey
	caPool   *x509.CertPool
	serverTL tls.Certificate
}

func newTestPKI(t *testing.T) *testPKI {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	caCert, _ := x509.ParseCertificate(caDER)
	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	p := &testPKI{caCert: caCert, caKey: caKey, caPool: pool}
	p.serverTL = p.issue(t, "localhost", true)
	return p
}

// issue returns a tls.Certificate whose leaf CN == cn, signed by the CA.
func (p *testPKI) issue(t *testing.T, cn string, server bool) tls.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	if server {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		tmpl.DNSNames = []string{"localhost"}
		tmpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	} else {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, p.caCert, &key.PublicKey, p.caKey)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: mustParse(der)}
}

func mustParse(der []byte) *x509.Certificate {
	c, _ := x509.ParseCertificate(der)
	return c
}

// clientCertFor returns a client tls.Certificate whose CN is the hospital id.
func (p *testPKI) clientCertFor(t *testing.T, hospitalID string) tls.Certificate {
	return p.issue(t, hospitalID, false)
}
```

- [ ] **Step 3: Write the failing webhook test**

`integration-service/pkg/pending/controller/webhook_test.go`:
```go
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
```

- [ ] **Step 4: Run the test to verify it fails**

Run: `cd integration-service && go test ./pkg/pending/controller/ -run TestWebhook -v`
Expected: FAIL — `undefined: NewWebhookHandler`.

- [ ] **Step 5: Implement the webhook handler**

`integration-service/pkg/pending/controller/webhook.go`:
```go
package controller

import (
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"github.com/hiabhi-cpu/integration-service/pkg/pending/adapter"
)

// WebhookHandler receives HMS registration webhooks over mTLS.
type WebhookHandler struct {
	store PendingStore
}

func NewWebhookHandler(store PendingStore) *WebhookHandler {
	return &WebhookHandler{store: store}
}

// PatientRegistered handles POST /webhook/patient-registered.
// hospital_id is the client cert's CN — the TLS layer has already verified the
// cert chains to our hospital CA (RequireAndVerifyClientCert), so a valid,
// non-empty CN is an authenticated hospital identity.
func (h *WebhookHandler) PatientRegistered(c *gin.Context) {
	if c.Request.TLS == nil || len(c.Request.TLS.PeerCertificates) == 0 {
		c.JSON(http.StatusForbidden, gin.H{"error": "client certificate required"})
		return
	}
	hospitalID := c.Request.TLS.PeerCertificates[0].Subject.CommonName
	if hospitalID == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "client certificate has no hospital identity (empty CN)"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20)) // 1 MB cap
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cannot read body"})
		return
	}

	reg, err := adapter.FromBahmni(body, hospitalID, time.Now())
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid registration payload"})
		return
	}

	if err := h.store.Upsert(c.Request.Context(), reg); err != nil {
		log.Errorf("integration-service: upsert failed for hospital %s: %v", hospitalID, err)
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "storage unavailable, retry"})
		return
	}

	// Never log the mobile or name.
	log.Infof("integration-service: staged registration hospital=%s hms_patient_id=%s", hospitalID, reg.HMSPatientID)
	c.JSON(http.StatusOK, gin.H{"status": "staged"})
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd integration-service && go test ./pkg/pending/controller/ -run TestWebhook -v`
Expected: PASS (3 tests).

- [ ] **Step 7: Commit**

```bash
git add integration-service/pkg/pending/controller
git commit -m "feat(integration-service): mTLS webhook receiver (hospital_id from client cert CN)"
```

---

### Task 4: Internal read API (list masked + get)

**Files:**
- Create: `integration-service/pkg/pending/controller/read.go`
- Test: `integration-service/pkg/pending/controller/read_test.go`

**Interfaces:**
- Consumes: `PendingStore`, `middleware.CtxHospitalID`
- Produces:
  - `controller.NewReadHandler(store PendingStore) *ReadHandler`
  - `(*ReadHandler).List(c *gin.Context)` — hospital from `c.GetString(middleware.CtxHospitalID)`; mobiles masked
  - `(*ReadHandler).Get(c *gin.Context)` — path param `hms_patient_id`; raw mobile; 404 when absent

- [ ] **Step 1: Write the failing read test**

`integration-service/pkg/pending/controller/read_test.go`:
```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd integration-service && go test ./pkg/pending/controller/ -run 'TestList|TestGet' -v`
Expected: FAIL — `undefined: NewReadHandler`.

- [ ] **Step 3: Implement the read handlers**

`integration-service/pkg/pending/controller/read.go`:
```go
package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/hiabhi-cpu/shared/middleware"
)

// ReadHandler serves the internal, hospital-scoped read API.
type ReadHandler struct {
	store PendingStore
}

func NewReadHandler(store PendingStore) *ReadHandler {
	return &ReadHandler{store: store}
}

// listItem is the masked shape returned by List (no raw mobile on a list).
type listItem struct {
	HMSPatientID string `json:"hms_patient_id"`
	Name         string `json:"name"`
	Mobile       string `json:"mobile"` // masked
	RegisteredAt string `json:"registered_at"`
}

// List handles GET /internal/v1/registrations — pending records for the
// hospital in the JWT. Mobiles are masked; the reception queue only needs to
// recognise a patient, not read their number.
func (h *ReadHandler) List(c *gin.Context) {
	hospitalID := c.GetString(middleware.CtxHospitalID)
	if hospitalID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing hospital identity"})
		return
	}
	recs, err := h.store.List(c.Request.Context(), hospitalID)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "storage unavailable"})
		return
	}
	items := make([]listItem, 0, len(recs))
	for _, r := range recs {
		items = append(items, listItem{
			HMSPatientID: r.HMSPatientID,
			Name:         r.Name,
			Mobile:       maskMobile(r.Mobile),
			RegisteredAt: r.RegisteredAt,
		})
	}
	c.JSON(http.StatusOK, items)
}

// Get handles GET /internal/v1/registrations/:hms_patient_id — one record with
// the RAW mobile (a trusted consumer needs it to send the OTP).
func (h *ReadHandler) Get(c *gin.Context) {
	hospitalID := c.GetString(middleware.CtxHospitalID)
	if hospitalID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing hospital identity"})
		return
	}
	reg, err := h.store.Get(c.Request.Context(), hospitalID, c.Param("hms_patient_id"))
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "storage unavailable"})
		return
	}
	if reg == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "no pending registration"})
		return
	}
	c.JSON(http.StatusOK, reg)
}

// maskMobile keeps the first 2 and last 4 digits: 9876543210 -> 98****3210.
func maskMobile(m string) string {
	if len(m) != 10 {
		return "****"
	}
	return m[:2] + "****" + m[6:]
}
```

- [ ] **Step 4: Run the full controller package tests**

Run: `cd integration-service && go test ./pkg/pending/controller/ -v`
Expected: PASS (webhook + read tests).

- [ ] **Step 5: Commit**

```bash
git add integration-service/pkg/pending/controller/read.go integration-service/pkg/pending/controller/read_test.go
git commit -m "feat(integration-service): internal read API (list masked + get raw, hospital-scoped)"
```

---

### Task 5: Assembly — bootstrap, dual-listener main, routes, certs, Docker, live e2e

Ties everything into a runnable service and verifies it end-to-end against real Redis with real certs.

**Files:**
- Create: `integration-service/bootstrap/env.go`
- Create: `integration-service/bootstrap/redis.go`
- Create: `integration-service/pkg/routes/routes.go`
- Create: `integration-service/cmd/server/main.go`
- Create: `integration-service/certs/gen-dev-certs.sh`
- Create: `integration-service/Dockerfile`
- Create: `integration-service/docker-compose.yml`
- Create: `integration-service/.env.example`
- Create: `integration-service/.gitignore` (ignore `certs/*.pem`, `.env`)

**Interfaces:**
- Consumes: everything above; `middleware.JWTAuth`, `middleware.LoadPublicKey`, `bootstrap.NewRedis`.

- [ ] **Step 1: bootstrap/env.go**

```go
package bootstrap

import (
	"fmt"
	"os"
)

// Env holds integration-service configuration from environment variables.
type Env struct {
	InternalPort     string // internal read API (hospital-JWT auth)
	WebhookPort      string // mTLS webhook listener
	RedisURL         string
	JWTPublicKeyPath string // verifies hospital JWTs on /internal
	ServerCertPath   string // mTLS server cert
	ServerKeyPath    string // mTLS server key
	HospitalCAPath   string // CA that signs hospital client certs
}

func NewEnv() *Env {
	return &Env{
		InternalPort:     mustGet("INTEGRATION_SERVICE_PORT"),
		WebhookPort:      mustGet("INTEGRATION_WEBHOOK_PORT"),
		RedisURL:         mustGet("REDIS_URL"),
		JWTPublicKeyPath: mustGet("JWT_PUBLIC_KEY_PATH"),
		ServerCertPath:   mustGet("MTLS_SERVER_CERT"),
		ServerKeyPath:    mustGet("MTLS_SERVER_KEY"),
		HospitalCAPath:   mustGet("MTLS_HOSPITAL_CA"),
	}
}

func mustGet(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("bootstrap: required env var %q is not set", key))
	}
	return v
}
```

- [ ] **Step 2: bootstrap/redis.go** (identical pattern to notification-service)

```go
package bootstrap

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// NewRedis creates a Redis client and verifies connectivity.
func NewRedis(ctx context.Context, redisURL string) *redis.Client {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		panic(fmt.Sprintf("bootstrap: failed to parse REDIS_URL: %v", err))
	}
	client := redis.NewClient(opts)
	if err := client.Ping(ctx).Err(); err != nil {
		panic(fmt.Sprintf("bootstrap: Redis ping failed: %v", err))
	}
	return client
}
```

- [ ] **Step 3: pkg/routes/routes.go**

```go
package routes

import (
	"crypto/rsa"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/hiabhi-cpu/integration-service/pkg/pending/controller"
	"github.com/hiabhi-cpu/shared/middleware"
)

// SetupInternal registers the hospital-JWT-scoped read API on the internal engine.
func SetupInternal(r *gin.Engine, read *controller.ReadHandler, pubKey *rsa.PublicKey) {
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "integration-service"})
	})
	grp := r.Group("/internal/v1")
	grp.Use(middleware.JWTAuth(pubKey)) // sets middleware.CtxHospitalID from the hospital JWT
	{
		grp.GET("/registrations", read.List)
		grp.GET("/registrations/:hms_patient_id", read.Get)
	}
}

// SetupWebhook registers the mTLS webhook on the webhook engine.
func SetupWebhook(r *gin.Engine, webhook *controller.WebhookHandler) {
	r.POST("/webhook/patient-registered", webhook.PatientRegistered)
}
```

- [ ] **Step 4: cmd/server/main.go** (two listeners, graceful shutdown)

```go
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/joho/godotenv/autoload"
	log "github.com/sirupsen/logrus"

	"github.com/hiabhi-cpu/integration-service/bootstrap"
	"github.com/hiabhi-cpu/integration-service/pkg/pending/controller"
	"github.com/hiabhi-cpu/integration-service/pkg/pending/repository"
	"github.com/hiabhi-cpu/integration-service/pkg/routes"
	"github.com/hiabhi-cpu/shared/logging"
	"github.com/hiabhi-cpu/shared/middleware"
)

func main() {
	logging.Setup("integration-service")
	ctx := context.Background()
	env := bootstrap.NewEnv()

	redisClient := bootstrap.NewRedis(ctx, env.RedisURL)
	defer redisClient.Close()

	pubKey, err := middleware.LoadPublicKey(env.JWTPublicKeyPath)
	if err != nil {
		log.Fatalf("integration-service: failed to load JWT public key: %v", err)
	}

	store := repository.NewRedisStore(redisClient)
	webhookHandler := controller.NewWebhookHandler(store)
	readHandler := controller.NewReadHandler(store)

	gin.SetMode(gin.ReleaseMode)

	// Internal read API (normal HTTP; hospital-JWT auth).
	internal := gin.New()
	_ = internal.SetTrustedProxies(nil)
	internal.Use(gin.Recovery(), gin.Logger())
	routes.SetupInternal(internal, readHandler, pubKey)
	internalSrv := &http.Server{
		Addr: ":" + env.InternalPort, Handler: internal,
		ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second,
	}

	// mTLS webhook listener (own port; client cert required + verified).
	webhook := gin.New()
	_ = webhook.SetTrustedProxies(nil)
	webhook.Use(gin.Recovery(), gin.Logger())
	routes.SetupWebhook(webhook, webhookHandler)
	tlsCfg, err := mtlsConfig(env)
	if err != nil {
		log.Fatalf("integration-service: mTLS config: %v", err)
	}
	webhookSrv := &http.Server{
		Addr: ":" + env.WebhookPort, Handler: webhook, TLSConfig: tlsCfg,
		ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second,
	}

	go func() {
		log.Printf("integration-service: internal API on :%s", env.InternalPort)
		if err := internalSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("integration-service: internal server error: %v", err)
		}
	}()
	go func() {
		log.Printf("integration-service: mTLS webhook on :%s", env.WebhookPort)
		// Certs are already in TLSConfig, so pass empty paths.
		if err := webhookSrv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			log.Fatalf("integration-service: webhook server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("integration-service: shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = internalSrv.Shutdown(shutdownCtx)
	_ = webhookSrv.Shutdown(shutdownCtx)
	log.Println("integration-service: stopped")
}

// mtlsConfig loads the server cert and the hospital CA, requiring a verified
// client cert on every webhook connection.
func mtlsConfig(env *bootstrap.Env) (*tls.Config, error) {
	serverCert, err := tls.LoadX509KeyPair(env.ServerCertPath, env.ServerKeyPath)
	if err != nil {
		return nil, fmt.Errorf("load server keypair: %w", err)
	}
	caPEM, err := os.ReadFile(env.HospitalCAPath)
	if err != nil {
		return nil, fmt.Errorf("read hospital CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("hospital CA PEM contained no certs")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS12,
	}, nil
}
```

- [ ] **Step 5: certs/gen-dev-certs.sh** (dev CA + server + one hospital client cert)

```bash
#!/usr/bin/env bash
# Generates dev mTLS material for integration-service:
#   ca.pem/ca.key        — the hospital CA (server trusts client certs it signs)
#   server.pem/server.key — the webhook server cert (CN=localhost)
#   hosp-1.pem/hosp-1.key — a test hospital CLIENT cert (CN = hospital_id)
# The CN of a client cert IS the hospital_id the webhook will trust.
# Run:  bash integration-service/certs/gen-dev-certs.sh
set -euo pipefail
cd "$(dirname "$0")"

HOSPITAL_ID="${1:-hosp-1}"

# CA
openssl ecparam -name prime256v1 -genkey -noout -out ca.key
openssl req -x509 -new -key ca.key -sha256 -days 3650 -subj "/CN=dpdp-dev-hospital-ca" -out ca.pem

# Server cert (CN=localhost, SAN localhost + 127.0.0.1)
openssl ecparam -name prime256v1 -genkey -noout -out server.key
openssl req -new -key server.key -subj "/CN=localhost" -out server.csr
openssl x509 -req -in server.csr -CA ca.pem -CAkey ca.key -CAcreateserial -days 825 -sha256 \
  -extfile <(printf "subjectAltName=DNS:localhost,IP:127.0.0.1\nextendedKeyUsage=serverAuth") \
  -out server.pem

# Client cert for one hospital (CN = hospital_id)
openssl ecparam -name prime256v1 -genkey -noout -out "${HOSPITAL_ID}.key"
openssl req -new -key "${HOSPITAL_ID}.key" -subj "/CN=${HOSPITAL_ID}" -out "${HOSPITAL_ID}.csr"
openssl x509 -req -in "${HOSPITAL_ID}.csr" -CA ca.pem -CAkey ca.key -CAcreateserial -days 825 -sha256 \
  -extfile <(printf "extendedKeyUsage=clientAuth") \
  -out "${HOSPITAL_ID}.pem"

rm -f ./*.csr ./*.srl
echo "Generated CA, server, and client cert for hospital_id=${HOSPITAL_ID} in $(pwd)"
```

- [ ] **Step 6: .env.example, .gitignore, Dockerfile, docker-compose.yml**

`integration-service/.env.example`:
```bash
# ─── Redis ───────────────────────────────────────────────────────────────────
REDIS_URL=redis://localhost:6379/0

# ─── Ports ───────────────────────────────────────────────────────────────────
INTEGRATION_SERVICE_PORT=9009
INTEGRATION_WEBHOOK_PORT=9443

# ─── JWT (verifies hospital tokens on /internal) ─────────────────────────────
JWT_PUBLIC_KEY_PATH=../auth-service/keys/auth_public.pem

# ─── mTLS webhook material (run certs/gen-dev-certs.sh) ───────────────────────
MTLS_SERVER_CERT=./certs/server.pem
MTLS_SERVER_KEY=./certs/server.key
MTLS_HOSPITAL_CA=./certs/ca.pem
```

`integration-service/.gitignore`:
```
.env
certs/*.pem
certs/*.key
certs/*.srl
```

`integration-service/Dockerfile` (mirror notification-service; build `./cmd/server`):
```dockerfile
# syntax=docker/dockerfile:1
# Build context MUST be the repo root (go.mod uses replace ../shared).
#   docker build -f integration-service/Dockerfile -t integration-service .
FROM golang:1.25-alpine AS builder
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY shared/go.mod shared/go.sum ./shared/
COPY integration-service/go.mod integration-service/go.sum ./integration-service/
WORKDIR /app/integration-service
RUN go mod download
WORKDIR /app
COPY shared/ ./shared/
COPY integration-service/ ./integration-service/
WORKDIR /app/integration-service
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o /out/integration-service ./cmd/server

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /out/integration-service .
RUN adduser -D appuser
USER appuser
EXPOSE 9009 9443
CMD ["./integration-service"]
```

`integration-service/docker-compose.yml`:
```yaml
# integration-service — standalone compose (polyrepo style).
# Requires shared EXTERNAL infra (redis on dpdp-network) — see ../DOCKER.md.
# Generate dev certs first:  bash certs/gen-dev-certs.sh
services:
  integration-service:
    container_name: dpdp-integration
    build:
      context: ..
      dockerfile: integration-service/Dockerfile
    environment:
      INTEGRATION_SERVICE_PORT: "9009"
      INTEGRATION_WEBHOOK_PORT: "9443"
      REDIS_URL: "redis://redis:6379/0"
      JWT_PUBLIC_KEY_PATH: /keys/auth_public.pem
      MTLS_SERVER_CERT: /certs/server.pem
      MTLS_SERVER_KEY: /certs/server.key
      MTLS_HOSPITAL_CA: /certs/ca.pem
    ports:
      - "9009:9009"
      - "9443:9443"
    volumes:
      - ../auth-service/keys:/keys:ro
      - ./certs:/certs:ro
      - /data/logs:/data/logs
    restart: unless-stopped
    networks:
      - dpdp-network
    healthcheck:
      test: ["CMD", "wget", "-q", "-O", "-", "http://localhost:9009/health"]
      interval: 10s
      timeout: 3s
      retries: 5
      start_period: 10s

networks:
  dpdp-network:
    external: true
```

- [ ] **Step 7: Build the whole service**

Run: `cd integration-service && go build ./... && go test ./...`
Expected: build succeeds; all package tests PASS.

- [ ] **Step 8: Live end-to-end verification (real Redis, real certs)**

Run:
```bash
cd integration-service
bash certs/gen-dev-certs.sh hosp-1
cp .env.example .env
# Ensure a local Redis is up (docker: `docker run -d -p 6379:6379 redis:7-alpine` if needed)
go run ./cmd/server &
sleep 2

# 1) POST a registration over mTLS as hosp-1
curl -sS --cacert certs/ca.pem --cert certs/hosp-1.pem --key certs/hosp-1.key \
  https://localhost:9443/webhook/patient-registered \
  -H 'Content-Type: application/json' \
  -d '{"patientId":"PA-777","givenName":"Asha","familyName":"Rao","phoneNumber":"9876543210","birthdate":"1990-05-01"}'
# Expected: {"status":"staged"}

# 2) Confirm no client cert is rejected at the TLS layer
curl -sS --cacert certs/ca.pem https://localhost:9443/webhook/patient-registered -d '{}' || echo "REJECTED (expected)"

# 3) Read it back via the internal API using a hospital JWT for hosp-1.
#    Mint the JWT the same way other services get one — from auth-service:
#    POST /v1/auth/token with the hosp-1 API key (see auth-service/README or the
#    seed data). Export it as TOKEN, then:
curl -sS -H "Authorization: Bearer $TOKEN" http://localhost:9009/internal/v1/registrations
# Expected: [{"hms_patient_id":"PA-777","name":"Asha Rao","mobile":"98****3210",...}]
curl -sS -H "Authorization: Bearer $TOKEN" http://localhost:9009/internal/v1/registrations/PA-777
# Expected: full record incl "mobile":"9876543210"

kill %1
```
Confirm: webhook stores under the cert's hospital, no-cert is rejected, list masks the mobile and is hospital-scoped, get returns the raw mobile. If the hospital JWT for `hosp-1` isn't readily mintable in dev, verify steps 1–2 (the mTLS + storage path) and assert the internal read via the unit tests already covering scoping/masking.

- [ ] **Step 9: Commit**

```bash
git add integration-service/bootstrap integration-service/pkg/routes integration-service/cmd \
        integration-service/certs/gen-dev-certs.sh integration-service/Dockerfile \
        integration-service/docker-compose.yml integration-service/.env.example integration-service/.gitignore
git commit -m "feat(integration-service): dual-listener assembly (mTLS webhook + internal read) + dev certs + Docker; live-verified e2e"
```

---

## Self-Review

**Spec coverage** (each spec section → task):
- mTLS webhook, own listener, cert→hospital_id → Task 3 (handler) + Task 5 (listener/TLS config).
- Bahmni adapter → Task 1.
- Redis pending store, 72h TTL, idempotent → Task 2.
- Internal read API (list + get), hospital-JWT scoped, masking → Task 4 + Task 5 routes.
- Cert tooling → Task 5 Step 5.
- PII handling (mask on surface, no logs, Redis-only, TTL) → enforced in Tasks 2–4 + webhook log line.
- Error table (bad cert, malformed→400, redis down→503, dup→idempotent, unknown→404, bad JWT→401) → Tasks 2–4 + Task 5.
- Testing (adapter, mTLS handshake, store TTL, internal auth/scoping, live e2e) → Tasks 1–5.
- Out-of-scope items (Spec B, §9 logic, 2nd adapter, prod CA) → not built; noted in plan intro.

**Placeholder scan:** No TBD/TODO; every code step shows full code; commands have expected output. `read.go` does not import `model` (types are inferred from the store's return values), so there is no unused-import wart.

**Type consistency:** `PendingRegistration` fields, `PendingStore` interface (Upsert/Get/List signatures), `NewWebhookHandler`/`NewReadHandler`, `maskMobile` (2+4 mask), `PendingTTL`, and Redis key shape are identical across Tasks 1–5. Ports (9009 internal / 9443 webhook) consistent between env, main, Dockerfile, compose, and the e2e script.
