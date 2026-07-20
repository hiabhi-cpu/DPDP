# Returning-Patient Queue Badge — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop the reception queue sending already-consented patients to a kiosk that will 409 — badge them "Already consented", disable Send code, and drop the row after 15s.

**Architecture:** consent-service gains one batch, purpose-agnostic, non-auditing endpoint (`POST /api/v1/consent/active`) answering "which of these mobiles already have consent?". integration-service's `List` calls it — forwarding the caller's hospital JWT — and flags each queue row. The admin dashboard badges flagged rows and hides them 15s after first sighting.

**Tech Stack:** Go 1.x (gin, pgx/v5, go-redis), Postgres with RLS, React + TypeScript (vitest, @testing-library/react).

**Spec:** `docs/superpowers/specs/2026-07-16-returning-patient-queue-design.md`

## Global Constraints

- **Key on mobile, never `hms_patient_id`.** Capture blocks on `patient_key` (an HMAC of the mobile) at `consent-service/pkg/consent/service/consent_service.go:150-156`. `hms_patient_id` is nullable and only set when a capture request carried one. A lookup keyed on HMS ID can disagree with capture; one keyed on mobile cannot.
- **The active test is `model.Consent.AnyActive()`**, never the `status` column. `emergency-service` writes `EMERGENCY_OVERRIDE` rows into the same table with statuses (`PENDING_RETROSPECTIVE`) not derived from the purposes map, so the two can drift. `AnyActive()` is the rule capture itself applies.
- **The new endpoint writes NO audit event.** Unlike `Check`, this is an operational lookup on a 5s timer, not a human data access. Do not call `EnqueueAudit` from it.
- **`consent.consent_vault` is `FORCE ROW LEVEL SECURITY`.** Every read must run inside a transaction that has called `setHospitalContext`. Querying `r.pool` directly returns **zero rows and no error** — indistinguishable from "nobody has consented".
- **Raw mobiles travel in request bodies only, never in a URL or a log line.**
- **The queue fails open.** Any consent-lookup failure leaves rows unbadged and actionable — never empty the reception board.

---

### Task 1: consent-service repository — `ActivePatientKeys`

**Files:**
- Modify: `consent-service/pkg/consent/repository/queries.go` (add const after `queryGetLatestByHMSPatientID`, ~line 33)
- Modify: `consent-service/pkg/consent/repository/interface.go:29` (add method to `ConsentRepository`)
- Modify: `consent-service/pkg/consent/repository/repository.go` (add method after `GetLatestByHMSPatientID`, ~line 171)
- Create: `consent-service/test/active_integration_test.go`

**Interfaces:**
- Consumes: `scanConsentRow`, `setHospitalContext`, `consentColumns` (all existing, same package). Test harness helpers `connections`, `connect`, `mustRand`, `randomPatientKey`, `newStatsPool`, `createStatsHospital`, `insertConsentRow`, `insertConsentRowV` from `consent-service/test/tenant_isolation_test.go` and `stats_integration_test.go` (same `isolation` package).
- Produces: `ActivePatientKeys(ctx context.Context, hospitalID string, patientKeys []string) (map[string]bool, error)` on `repository.ConsentRepository` — returns a map containing only the keys that are active. Absent key means not active.

- [ ] **Step 1: Write the failing integration test**

Create `consent-service/test/active_integration_test.go`:

```go
//go:build integration

// This file extends the tenant-isolation suite (see tenant_isolation_test.go for
// the package doc and the shared harness) with integration coverage for
// repository.ActivePatientKeys — the batch "has any active consent?" lookup
// behind the reception queue's already-consented badge.
//
// It must run against a REAL Postgres under the dpdp_app role. consent_vault is
// FORCE ROW LEVEL SECURITY, and the bug this suite exists to catch (querying
// outside an RLS-scoped transaction) returns zero rows with NO error — which
// looks exactly like "nobody has consented", i.e. the bug the feature fixes.
// A mock cannot prove any of this.
//
//	go test -tags=integration ./test/... -v
package isolation

import (
	"context"
	"testing"

	"github.com/hiabhi-cpu/consent-service/pkg/consent/repository"
)

// TestActivePatientKeysFindsActiveConsent is the base case: a patient with one
// active purpose is reported active, and an unknown key is simply absent.
func TestActivePatientKeysFindsActiveConsent(t *testing.T) {
	ctx := context.Background()
	adminURL, _ := connections(t)
	admin := connect(t, adminURL)
	pool := newStatsPool(t)

	hosp := createStatsHospital(t, admin)
	consented := randomPatientKey(t)
	stranger := randomPatientKey(t)

	insertConsentRow(t, admin, hosp, consented, "CONSENT_GIVEN", "ACTIVE", `{"treatment":"ACTIVE"}`)

	repo := repository.New(pool)
	active, err := repo.ActivePatientKeys(ctx, hosp, []string{consented, stranger})
	if err != nil {
		t.Fatalf("ActivePatientKeys: %v", err)
	}
	if !active[consented] {
		t.Fatalf("consented key not reported active — RLS context missing?")
	}
	if active[stranger] {
		t.Fatalf("stranger with no consent rows reported active")
	}
}

// TestActivePatientKeysLatestRowWins verifies a later full withdrawal supersedes
// the earlier grant. consent_vault is an append-only version chain, so a naive
// "any ACTIVE row exists" query would wrongly report this patient active.
func TestActivePatientKeysLatestRowWins(t *testing.T) {
	ctx := context.Background()
	adminURL, _ := connections(t)
	admin := connect(t, adminURL)
	pool := newStatsPool(t)

	hosp := createStatsHospital(t, admin)
	key := randomPatientKey(t)

	insertConsentRowV(t, admin, hosp, key, "CONSENT_GIVEN", "ACTIVE", `{"treatment":"ACTIVE"}`, 1)
	insertConsentRowV(t, admin, hosp, key, "WITHDRAWAL", "WITHDRAWN", `{"treatment":"WITHDRAWN"}`, 2)

	repo := repository.New(pool)
	active, err := repo.ActivePatientKeys(ctx, hosp, []string{key})
	if err != nil {
		t.Fatalf("ActivePatientKeys: %v", err)
	}
	if active[key] {
		t.Fatalf("fully withdrawn patient reported active — latest row not winning")
	}
}

// TestActivePatientKeysPartialWithdrawalStillActive pins the granular-consent
// rule: one purpose withdrawn while another stays active still blocks capture,
// so the queue must still badge them. This is AnyActive(), not status.
func TestActivePatientKeysPartialWithdrawalStillActive(t *testing.T) {
	ctx := context.Background()
	adminURL, _ := connections(t)
	admin := connect(t, adminURL)
	pool := newStatsPool(t)

	hosp := createStatsHospital(t, admin)
	key := randomPatientKey(t)

	insertConsentRowV(t, admin, hosp, key, "CONSENT_GIVEN", "ACTIVE",
		`{"treatment":"ACTIVE","insurance":"ACTIVE"}`, 1)
	insertConsentRowV(t, admin, hosp, key, "WITHDRAWAL", "ACTIVE",
		`{"treatment":"ACTIVE","insurance":"WITHDRAWN"}`, 2)

	repo := repository.New(pool)
	active, err := repo.ActivePatientKeys(ctx, hosp, []string{key})
	if err != nil {
		t.Fatalf("ActivePatientKeys: %v", err)
	}
	if !active[key] {
		t.Fatalf("partially withdrawn patient reported inactive — one purpose is still ACTIVE")
	}
}

// TestActivePatientKeysIsolatesByHospital proves the RLS boundary holds: asking
// hospital A about a key that only hospital B consented under must return
// nothing. A leak here would badge the wrong hospital's patients.
func TestActivePatientKeysIsolatesByHospital(t *testing.T) {
	ctx := context.Background()
	adminURL, _ := connections(t)
	admin := connect(t, adminURL)
	pool := newStatsPool(t)

	hospA := createStatsHospital(t, admin)
	hospB := createStatsHospital(t, admin)
	key := randomPatientKey(t)

	insertConsentRow(t, admin, hospB, key, "CONSENT_GIVEN", "ACTIVE", `{"treatment":"ACTIVE"}`)

	repo := repository.New(pool)
	active, err := repo.ActivePatientKeys(ctx, hospA, []string{key})
	if err != nil {
		t.Fatalf("ActivePatientKeys(A): %v", err)
	}
	if active[key] {
		t.Fatalf("hospital A sees hospital B's consent — RLS leak")
	}
}

// TestActivePatientKeysEmptyInput guards the short-circuit: no keys means no
// query, and must not error.
func TestActivePatientKeysEmptyInput(t *testing.T) {
	ctx := context.Background()
	adminURL, _ := connections(t)
	admin := connect(t, adminURL)
	pool := newStatsPool(t)

	hosp := createStatsHospital(t, admin)

	repo := repository.New(pool)
	active, err := repo.ActivePatientKeys(ctx, hosp, nil)
	if err != nil {
		t.Fatalf("ActivePatientKeys(nil): %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("empty input returned %d entries, want 0", len(active))
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd /home/reddy/Documents/Go/DPDP/consent-service && go test -tags=integration ./test/... -run TestActivePatientKeys -v`

Expected: FAIL — compile error, `active.ActivePatientKeys undefined (type repository.ConsentRepository has no field or method ActivePatientKeys)`.

If instead the whole suite **SKIPs**, `TEST_ADMIN_DATABASE_URL` / `TEST_DATABASE_URL` are unset. Export both (see the harness doc in `test/tenant_isolation_test.go`) and re-run — a skip is not a pass, and this test is worthless without a real Postgres.

- [ ] **Step 3: Add the query**

In `consent-service/pkg/consent/repository/queries.go`, after the `queryGetLatestByHMSPatientID` const (ends ~line 33), add:

```go
	// Reception-queue path: batch "which of these patients already have consent?".
	// DISTINCT ON takes the highest-version row per patient_key — the append-only
	// vault's current state for each. The caller applies AnyActive() to the
	// Purposes map rather than filtering on status here, so this stays the exact
	// question Capture asks.
	queryLatestByPatientKeys = `
		SELECT DISTINCT ON (patient_key) ` + consentColumns + `
		FROM consent.consent_vault
		WHERE hospital_id = $1 AND patient_key = ANY($2)
		ORDER BY patient_key, version DESC
	`
```

- [ ] **Step 4: Add the method to the repository interface**

In `consent-service/pkg/consent/repository/interface.go`, after the `GetByIdempotencyKey` method (line 29), add:

```go
	// ActivePatientKeys returns the subset of patientKeys whose latest row has at
	// least one ACTIVE purpose. Batch form of the question Capture asks before it
	// blocks; used by the reception queue to badge returning patients. A key
	// absent from the map is not active.
	ActivePatientKeys(ctx context.Context, hospitalID string, patientKeys []string) (map[string]bool, error)
```

- [ ] **Step 5: Implement the method**

In `consent-service/pkg/consent/repository/repository.go`, after `GetLatestByHMSPatientID` (ends line 171), add:

```go
// ActivePatientKeys returns the subset of patientKeys that currently have at
// least one active purpose.
//
// It mirrors getOneConsent's RLS shape — its own transaction with
// setHospitalContext — rather than reusing it, because getOneConsent is
// single-row. This is not optional bookkeeping: consent_vault is FORCE ROW LEVEL
// SECURITY, so querying r.pool directly returns zero rows and no error, which is
// indistinguishable from "nobody has consented".
//
// The active test is AnyActive() on the latest row, deliberately the same
// predicate Capture blocks on (consent_service.go:154) and NOT the status column
// — emergency-service writes rows to this table whose status is not derived from
// the purposes map, so the two can drift. Keeping the predicate identical is what
// guarantees the queue can never disagree with what capture will do.
func (r *pgxConsentRepository) ActivePatientKeys(ctx context.Context, hospitalID string, patientKeys []string) (map[string]bool, error) {
	active := make(map[string]bool, len(patientKeys))
	if len(patientKeys) == 0 {
		return active, nil
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("repository.ActivePatientKeys: begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := setHospitalContext(ctx, tx, hospitalID); err != nil {
		return nil, fmt.Errorf("repository.ActivePatientKeys: %w", err)
	}

	rows, err := tx.Query(ctx, queryLatestByPatientKeys, hospitalID, patientKeys)
	if err != nil {
		return nil, fmt.Errorf("repository.ActivePatientKeys: query failed: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		c, err := scanConsentRow(rows)
		if err != nil {
			return nil, fmt.Errorf("repository.ActivePatientKeys: scan failed: %w", err)
		}
		if c.AnyActive() {
			active[c.PatientKey] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("repository.ActivePatientKeys: %w", err)
	}
	return active, nil
}
```

Note: `pgx.Rows` satisfies the `pgx.Row` interface `scanConsentRow` takes (both are `Scan(dest ...any) error`), so the existing scanner is reused as-is.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd /home/reddy/Documents/Go/DPDP/consent-service && go test -tags=integration ./test/... -run TestActivePatientKeys -v`

Expected: PASS — all five tests.

- [ ] **Step 7: Run the full suite to check nothing regressed**

Run: `cd /home/reddy/Documents/Go/DPDP/consent-service && go build ./... && go test ./... && go test -tags=integration ./test/... `

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add consent-service/pkg/consent/repository/queries.go \
        consent-service/pkg/consent/repository/interface.go \
        consent-service/pkg/consent/repository/repository.go \
        consent-service/test/active_integration_test.go
git commit -m "feat(consent): add ActivePatientKeys batch lookup

Batch 'has any active consent?' keyed by patient_key, for the reception
queue. Uses AnyActive() on the latest row — the same predicate Capture
blocks on — so the queue cannot disagree with capture.

Runs in its own RLS-scoped transaction: consent_vault is FORCE RLS, so a
pool query would return zero rows with no error."
```

---

### Task 2: consent-service — `POST /api/v1/consent/active`

**Files:**
- Modify: `consent-service/pkg/consent/model/consent.go` (add request/response types after `CheckConsentResponse`, ~line 110)
- Modify: `consent-service/pkg/consent/service/consent_service.go:56` (add to `ConsentService` interface) and add the method after `Check` (~line 291)
- Modify: `consent-service/pkg/consent/controller/consent_handler.go` (add `Active` handler after `Check`, which ends at line 92)
- Modify: `consent-service/pkg/routes/routes.go:26` (register route)
- Create: `consent-service/pkg/consent/service/active_service_test.go`

**Interfaces:**
- Consumes: `repository.ConsentRepository.ActivePatientKeys` (Task 1); existing `consentService.patientKeyFor(ctx, hospitalID, mobile) (string, error)`; `secrets.Provider` (`GetSystemSalt(ctx) (string, error)`, `GetHospitalKey(ctx, hospitalID) (string, error)`).
- Produces: `ConsentService.ActiveMobiles(ctx context.Context, hospitalID string, mobiles []string) ([]string, error)`, returning the active subset **in input order**. HTTP: `POST /api/v1/consent/active`, body `{"mobiles":["9876543210"]}` → `200 {"active":["9876543210"]}`.

- [ ] **Step 1: Write the failing service test**

Create `consent-service/pkg/consent/service/active_service_test.go`:

```go
package service

import (
	"context"
	"testing"

	"github.com/hiabhi-cpu/consent-service/pkg/consent/repository"
)

// fakeActiveRepo implements just enough of ConsentRepository for the test.
type fakeActiveRepo struct {
	repository.ConsentRepository // embed for the methods we don't exercise
	gotHospitalID                string
	gotKeys                      []string
	active                       map[string]bool
}

func (f *fakeActiveRepo) ActivePatientKeys(_ context.Context, hospitalID string, keys []string) (map[string]bool, error) {
	f.gotHospitalID = hospitalID
	f.gotKeys = keys
	return f.active, nil
}

// fakeSecrets returns fixed key material so patientKeyFor is deterministic.
type fakeSecrets struct{}

func (fakeSecrets) GetSystemSalt(_ context.Context) (string, error)            { return "test-salt", nil }
func (fakeSecrets) GetHospitalKey(_ context.Context, _ string) (string, error) { return "test-key", nil }

// TestActiveMobilesMapsKeysBackToMobiles is the core of this method: it hashes
// mobiles to patient keys on the way in and must map the repo's key-keyed answer
// back to the caller's mobiles on the way out.
func TestActiveMobilesMapsKeysBackToMobiles(t *testing.T) {
	ctx := context.Background()
	sp := fakeSecrets{}
	svc := NewConsentService(&fakeActiveRepo{}, sp, nil).(*consentService)

	// Derive the key the service will compute for the "consented" mobile, so the
	// fake repo can answer in the same key space the real one would.
	consentedKey, err := svc.patientKeyFor(ctx, "hosp-1", "9876543210")
	if err != nil {
		t.Fatalf("patientKeyFor: %v", err)
	}

	repo := &fakeActiveRepo{active: map[string]bool{consentedKey: true}}
	svc = NewConsentService(repo, sp, nil).(*consentService)

	got, err := svc.ActiveMobiles(ctx, "hosp-1", []string{"9876543210", "9000000000"})
	if err != nil {
		t.Fatalf("ActiveMobiles: %v", err)
	}
	if len(got) != 1 || got[0] != "9876543210" {
		t.Fatalf("active = %v, want [9876543210]", got)
	}
	if repo.gotHospitalID != "hosp-1" {
		t.Fatalf("repo got hospital %q, want hosp-1", repo.gotHospitalID)
	}
	if len(repo.gotKeys) != 2 {
		t.Fatalf("repo got %d keys, want 2", len(repo.gotKeys))
	}
	// The raw mobile must never be handed to the repository.
	for _, k := range repo.gotKeys {
		if k == "9876543210" || k == "9000000000" {
			t.Fatalf("raw mobile %q leaked into the repository call", k)
		}
	}
}

// TestActiveMobilesPreservesInputOrder pins deterministic output — the repo
// answers with a map, whose iteration order is random.
func TestActiveMobilesPreservesInputOrder(t *testing.T) {
	ctx := context.Background()
	sp := fakeSecrets{}
	svc := NewConsentService(&fakeActiveRepo{}, sp, nil).(*consentService)

	mobiles := []string{"9111111111", "9222222222", "9333333333"}
	activeKeys := map[string]bool{}
	for _, m := range []string{"9333333333", "9111111111"} {
		k, err := svc.patientKeyFor(ctx, "hosp-1", m)
		if err != nil {
			t.Fatalf("patientKeyFor: %v", err)
		}
		activeKeys[k] = true
	}

	svc = NewConsentService(&fakeActiveRepo{active: activeKeys}, sp, nil).(*consentService)

	for i := 0; i < 20; i++ { // repeat: map order varies per iteration
		got, err := svc.ActiveMobiles(ctx, "hosp-1", mobiles)
		if err != nil {
			t.Fatalf("ActiveMobiles: %v", err)
		}
		if len(got) != 2 || got[0] != "9111111111" || got[1] != "9333333333" {
			t.Fatalf("active = %v, want [9111111111 9333333333] in input order", got)
		}
	}
}

// TestActiveMobilesEmptyResult verifies a patient with no consent yields an empty
// (non-nil) slice, so the JSON is [] rather than null.
func TestActiveMobilesEmptyResult(t *testing.T) {
	svc := NewConsentService(&fakeActiveRepo{active: map[string]bool{}}, fakeSecrets{}, nil)

	got, err := svc.ActiveMobiles(context.Background(), "hosp-1", []string{"9876543210"})
	if err != nil {
		t.Fatalf("ActiveMobiles: %v", err)
	}
	if got == nil {
		t.Fatalf("active = nil, want empty slice (marshals to [] not null)")
	}
	if len(got) != 0 {
		t.Fatalf("active = %v, want empty", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd /home/reddy/Documents/Go/DPDP/consent-service && go test ./pkg/consent/service/... -run TestActiveMobiles -v`

Expected: FAIL — compile error, `svc.ActiveMobiles undefined`.

- [ ] **Step 3: Add the model types**

In `consent-service/pkg/consent/model/consent.go`, after `CheckConsentResponse` (ends line 110), add:

```go
// ActiveConsentRequest is the body for POST /api/v1/consent/active — the batch,
// purpose-agnostic "which of these patients already have consent?" lookup behind
// the reception queue's already-consented badge. Patients are identified by
// mobile because that is what Capture's block keys on (via patient_key); an
// hms_patient_id lookup could disagree with it.
//
// The 200-entry cap is input validation at a trust boundary, not tuning. Mobiles
// are in the body, never a URL, so raw mobiles never reach logs.
type ActiveConsentRequest struct {
	Mobiles []string `json:"mobiles" binding:"required,min=1,max=200,dive,len=10"`
}

// ActiveConsentResponse returns the subset of the requested mobiles that have at
// least one active purpose, in the order they were requested.
type ActiveConsentResponse struct {
	Active []string `json:"active"`
}
```

- [ ] **Step 4: Add the service method**

In `consent-service/pkg/consent/service/consent_service.go`, add to the `ConsentService` interface after the `Check` line (line 48):

```go
	// ActiveMobiles returns the subset of mobiles with at least one active purpose,
	// in input order. Batch and purpose-agnostic — the reception queue's
	// "already consented?" question. Writes no audit event; see the impl.
	ActiveMobiles(ctx context.Context, hospitalID string, mobiles []string) ([]string, error)
```

Then add the implementation after `Check` (ends line 291):

```go
// ActiveMobiles returns the subset of mobiles that currently have at least one
// active consent purpose, in input order.
//
// Unlike Check, this writes NO audit event, and that is deliberate. Check audits
// because a doctor reading patient data is a data access. This is an operational
// lookup: the reception queue polls it on a 5-second timer to decide whether to
// badge a row, revealing nothing reception cannot already see on the screen in
// front of them. Auditing a timer would add ~240 rows/minute of noise and bury
// the real access log — that is less accountability, not more.
func (s *consentService) ActiveMobiles(ctx context.Context, hospitalID string, mobiles []string) ([]string, error) {
	keys := make([]string, 0, len(mobiles))
	mobileToKey := make(map[string]string, len(mobiles))
	for _, m := range mobiles {
		k, err := s.patientKeyFor(ctx, hospitalID, m)
		if err != nil {
			return nil, fmt.Errorf("ConsentService.ActiveMobiles: %w", err)
		}
		keys = append(keys, k)
		mobileToKey[m] = k
	}

	activeKeys, err := s.repo.ActivePatientKeys(ctx, hospitalID, keys)
	if err != nil {
		return nil, fmt.Errorf("ConsentService.ActiveMobiles: %w", err)
	}

	// Walk `mobiles`, not `activeKeys`: Go map order is random, and the response
	// should be deterministic.
	active := make([]string, 0, len(activeKeys))
	for _, m := range mobiles {
		if activeKeys[mobileToKey[m]] {
			active = append(active, m)
		}
	}
	return active, nil
}
```

- [ ] **Step 5: Run the service tests to verify they pass**

Run: `cd /home/reddy/Documents/Go/DPDP/consent-service && go test ./pkg/consent/service/... -run TestActiveMobiles -v`

Expected: PASS — all three tests.

- [ ] **Step 6: Add the controller handler**

In `consent-service/pkg/consent/controller/consent_handler.go`, add after `Check` ends (line 92):

```go
// Active handles POST /api/v1/consent/active — batch "which of these patients
// already have an active consent?". The reception queue uses it to badge
// returning patients instead of sending them to a kiosk whose capture will 409.
func (h *ConsentHandler) Active(c *gin.Context) {
	hospitalID := c.GetString(middleware.CtxHospitalID)
	if hospitalID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing hospital identity"})
		return
	}

	var req model.ActiveConsentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "mobiles must be 1-200 entries of 10 digits"})
		return
	}

	active, err := h.svc.ActiveMobiles(c.Request.Context(), hospitalID, req.Mobiles)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to look up consent"})
		return
	}

	c.JSON(http.StatusOK, model.ActiveConsentResponse{Active: active})
}
```

- [ ] **Step 7: Register the route**

In `consent-service/pkg/routes/routes.go`, after the `check` line (line 26), add:

```go
				consent.POST("/active", consentHandler.Active)
```

- [ ] **Step 8: Verify the whole service builds and tests pass**

Run: `cd /home/reddy/Documents/Go/DPDP/consent-service && go build ./... && go test ./...`

Expected: PASS.

- [ ] **Step 9: Verify the endpoint end-to-end against the running stack**

Bring up the stack per `RUN_LOCAL.md` / `DOCKER.md`, obtain a hospital JWT the way `FLOWS.md` documents, then:

```bash
curl -s -X POST http://localhost:9000/api/v1/consent/active \
  -H "Authorization: Bearer $HOSPITAL_JWT" \
  -H "Content-Type: application/json" \
  -d '{"mobiles":["9876543210","9000000000"]}'
```

Expected: `200` with `{"active":[...]}` — containing `9876543210` only if that mobile has captured consent at this hospital. Then confirm the two guards:

```bash
# over-cap and malformed input are rejected
curl -s -o /dev/null -w '%{http_code}\n' -X POST http://localhost:9000/api/v1/consent/active \
  -H "Authorization: Bearer $HOSPITAL_JWT" -H "Content-Type: application/json" \
  -d '{"mobiles":["123"]}'                       # expect 400
curl -s -o /dev/null -w '%{http_code}\n' -X POST http://localhost:9000/api/v1/consent/active \
  -H "Content-Type: application/json" -d '{"mobiles":["9876543210"]}'   # expect 401
```

Then confirm **no audit rows were written** by the calls above — this is the design promise, and nothing else tests it:

```bash
docker exec -i dpdp-postgres psql -U postgres -d dpdp -c \
  "SELECT count(*) FROM consent.audit_outbox WHERE created_at > now() - interval '2 minutes';"
```

Expected: unchanged by the `/active` calls (0 new rows, absent other activity).

- [ ] **Step 10: Commit**

```bash
git add consent-service/pkg/consent/model/consent.go \
        consent-service/pkg/consent/service/consent_service.go \
        consent-service/pkg/consent/service/active_service_test.go \
        consent-service/pkg/consent/controller/consent_handler.go \
        consent-service/pkg/routes/routes.go
git commit -m "feat(consent): add POST /api/v1/consent/active

Batch, purpose-agnostic 'which of these mobiles already have consent?'
for the reception queue. Keyed by mobile because that is what capture's
block keys on.

Writes no audit event: this is a timer-driven operational lookup, not a
human data access. Auditing it would bury the real access log."
```

---

### Task 3: integration-service — `List` flags consented rows

**Files:**
- Create: `integration-service/pkg/pending/consent/client.go`
- Modify: `integration-service/pkg/pending/controller/deps.go` (add `ConsentChecker`)
- Modify: `integration-service/pkg/pending/controller/read.go:13-55` (`ReadHandler`, `NewReadHandler`, `listItem`, `List`)
- Modify: `integration-service/pkg/pending/controller/read_test.go:50-60` (`readRouter` signature + existing call sites)
- Modify: `integration-service/bootstrap/env.go` (add `ConsentServiceURL`)
- Modify: `integration-service/cmd/server/main.go:39-41` (wire the client)
- Modify: `integration-service/.env.example`, `integration-service/docker-compose.yml`

**Interfaces:**
- Consumes: `POST /api/v1/consent/active` (Task 2) — body `{"mobiles":[...]}` → `200 {"active":[...]}`.
- Produces: `listItem` JSON gains `"consented": bool`. `controller.NewReadHandler(store PendingStore, consent ConsentChecker) *ReadHandler` — **signature change**, existing callers must be updated. `consent.NewClient(baseURL string) *Client` with `ActiveMobiles(ctx context.Context, authHeader string, mobiles []string) (map[string]bool, error)`.

- [ ] **Step 1: Write the failing tests**

In `integration-service/pkg/pending/controller/read_test.go`, add the fake after `mapStore`'s methods (after line 47):

```go
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
```

Replace `readRouter` (lines 50-60) with:

```go
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
```

Update every existing `readRouter(store, "hosp-1")` call in this file to `readRouter(store, &fakeChecker{}, "hosp-1")` (a `nil`-map fake reports nobody consented, which is the pre-existing behaviour those tests assert).

Then add the new tests:

```go
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
```

Add `"errors"` and `"strings"` to the test file's imports (`"strings"` is already imported at line 8; add `"errors"`).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd /home/reddy/Documents/Go/DPDP/integration-service && go test ./pkg/pending/controller/... -v`

Expected: FAIL — compile errors: `undefined: ConsentChecker`, `too many arguments in call to NewReadHandler`, `it.Consented undefined`.

- [ ] **Step 3: Add the `ConsentChecker` interface**

In `integration-service/pkg/pending/controller/deps.go`, after the `PendingStore` interface, add:

```go
// ConsentChecker is the slice of consent-service the read API needs: given the
// caller's Authorization header and a batch of raw mobiles, which of them already
// have an active consent? Defined here (consumer side) alongside PendingStore.
type ConsentChecker interface {
	ActiveMobiles(ctx context.Context, authHeader string, mobiles []string) (map[string]bool, error)
}
```

- [ ] **Step 4: Write the consent client**

Create `integration-service/pkg/pending/consent/client.go`:

```go
// Package consent talks to consent-service's batch "already consented?" lookup,
// so the reception queue can badge returning patients rather than sending them to
// a kiosk whose capture will 409.
package consent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Client calls consent-service's POST /api/v1/consent/active.
type Client struct {
	baseURL string
	client  *http.Client
}

// NewClient returns a Client for consent-service at baseURL.
//
// The 3s timeout is deliberately shorter than the reception queue's 5s poll
// interval: a slow consent-service must not make list requests pile up. On
// timeout the caller fails open and renders the queue unbadged.
func NewClient(baseURL string) *Client {
	return &Client{baseURL: baseURL, client: &http.Client{Timeout: 3 * time.Second}}
}

// ActiveMobiles returns a set of the mobiles that currently have an active
// consent. Mobiles go in the body, never the URL, so raw mobiles never reach an
// access log.
//
// authHeader is the caller's hospital JWT, forwarded verbatim. integration-service
// and consent-service verify the same auth-service key and the token carries no
// audience claim, so the token that authorised this request already authorises the
// downstream call — no second credential, and no privilege gained: admin-bff's
// token can call consent-service directly anyway.
func (c *Client) ActiveMobiles(ctx context.Context, authHeader string, mobiles []string) (map[string]bool, error) {
	body, err := json.Marshal(map[string][]string{"mobiles": mobiles})
	if err != nil {
		return nil, fmt.Errorf("consent.ActiveMobiles: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/consent/active", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("consent.ActiveMobiles: new request: %w", err)
	}
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("consent.ActiveMobiles: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("consent.ActiveMobiles: status %d", resp.StatusCode)
	}

	var out struct {
		Active []string `json:"active"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("consent.ActiveMobiles: decode: %w", err)
	}

	active := make(map[string]bool, len(out.Active))
	for _, m := range out.Active {
		active[m] = true
	}
	return active, nil
}
```

- [ ] **Step 5: Enrich `List`**

In `integration-service/pkg/pending/controller/read.go`, replace lines 12-55 (from the `ReadHandler` comment through the end of `List`) with:

```go
// ReadHandler serves the internal, hospital-scoped read API.
type ReadHandler struct {
	store   PendingStore
	consent ConsentChecker
}

func NewReadHandler(store PendingStore, consent ConsentChecker) *ReadHandler {
	return &ReadHandler{store: store, consent: consent}
}

// listItem is the masked shape returned by List (no raw mobile on a list).
type listItem struct {
	HMSPatientID string `json:"hms_patient_id"`
	Name         string `json:"name"`
	Mobile       string `json:"mobile"` // masked
	RegisteredAt string `json:"registered_at"`
	Status       string `json:"status"`
	Consented    bool   `json:"consented"` // already has an active consent; no action needed
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

	// Ask consent-service which of these patients already consented, so reception
	// can see "no action" instead of burning an SMS and a walk on a capture that
	// will 409. Keyed by the RAW mobile: capture blocks on the mobile-derived
	// patient_key, so this is the only key that cannot disagree with it. Mobiles
	// leave here server-side only — the response below is still masked.
	//
	// Fails open. On any error the flags stay false and the queue renders exactly
	// as it did before this lookup existed: a consent-service blip costs a wasted
	// SMS, never an empty reception board.
	consented := map[string]bool{}
	if len(recs) > 0 {
		mobiles := make([]string, 0, len(recs))
		for _, r := range recs {
			mobiles = append(mobiles, r.Mobile)
		}
		got, cerr := h.consent.ActiveMobiles(c.Request.Context(), c.GetHeader("Authorization"), mobiles)
		if cerr != nil {
			log.Warnf("integration-service: consent lookup failed, queue renders unbadged: %v", cerr)
		} else {
			consented = got
		}
	}

	items := make([]listItem, 0, len(recs))
	for _, r := range recs {
		items = append(items, listItem{
			HMSPatientID: r.HMSPatientID,
			Name:         r.Name,
			Mobile:       maskMobile(r.Mobile),
			RegisteredAt: r.RegisteredAt,
			Status:       r.Status,
			Consented:    consented[r.Mobile],
		})
	}
	c.JSON(http.StatusOK, items)
}
```

Add `log "github.com/sirupsen/logrus"` to the imports of `read.go` (it is not currently imported there; `webhook.go` in the same package already uses this alias).

- [ ] **Step 6: Run the controller tests to verify they pass**

Run: `cd /home/reddy/Documents/Go/DPDP/integration-service && go test ./pkg/pending/controller/... -v`

Expected: PASS — the three new tests plus the existing ones.

- [ ] **Step 7: Wire the client and add config**

In `integration-service/bootstrap/env.go`, add to the `Env` struct:

```go
	ConsentServiceURL string // consent-service base URL for the "already consented?" lookup
```

and to `NewEnv()`:

```go
		ConsentServiceURL: mustGet("CONSENT_SERVICE_URL"),
```

In `integration-service/cmd/server/main.go`, add the import:

```go
	"github.com/hiabhi-cpu/integration-service/pkg/pending/consent"
```

and replace line 41 (`readHandler := controller.NewReadHandler(store)`) with:

```go
	consentClient := consent.NewClient(env.ConsentServiceURL)
	readHandler := controller.NewReadHandler(store, consentClient)
```

In `integration-service/.env.example`, append:

```
# ─── consent-service (reception queue "already consented?" lookup) ────────────
CONSENT_SERVICE_URL=http://localhost:9000
```

In `integration-service/docker-compose.yml`, add to the `environment:` block after `REDIS_URL`:

```yaml
      CONSENT_SERVICE_URL: "http://consent-service:9000"
```

Also update the header comment on line 2 to note the new dependency:

```yaml
# Requires shared EXTERNAL infra (redis + consent-service on dpdp-network) — see ../DOCKER.md.
```

- [ ] **Step 8: Verify the service builds and the full suite passes**

Run: `cd /home/reddy/Documents/Go/DPDP/integration-service && go build ./... && go test ./...`

Expected: PASS.

- [ ] **Step 9: Verify end-to-end against the running stack**

Restart integration-service so it picks up `CONSENT_SERVICE_URL`, then with a hospital JWT (per `FLOWS.md`):

```bash
curl -s http://localhost:9009/internal/v1/registrations \
  -H "Authorization: Bearer $HOSPITAL_JWT" | jq '.[] | {hms_patient_id, consented}'
```

Expected: every row carries a `consented` field. Stage a patient who already has consent (per `FLOWS.md`'s webhook curl) and confirm their row shows `"consented": true`, while a fresh patient shows `false`.

Then verify fail-open for real — this is the behaviour the whole feature degrades to:

```bash
docker stop dpdp-consent-service   # or the consent-service container name in your stack
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:9009/internal/v1/registrations \
  -H "Authorization: Bearer $HOSPITAL_JWT"     # expect 200, rows still present
docker start dpdp-consent-service
```

Expected: `200` with all rows present and `consented: false`, plus a warning in the integration-service logs. Not a 502, and not an empty array.

- [ ] **Step 10: Commit**

```bash
git add integration-service/pkg/pending/consent/client.go \
        integration-service/pkg/pending/controller/deps.go \
        integration-service/pkg/pending/controller/read.go \
        integration-service/pkg/pending/controller/read_test.go \
        integration-service/bootstrap/env.go \
        integration-service/cmd/server/main.go \
        integration-service/.env.example \
        integration-service/docker-compose.yml
git commit -m "feat(integration): flag already-consented rows in the queue

List now asks consent-service which staged patients already consented and
sets consented on each row. Keyed by raw mobile (server-side only; the
response stays masked) because that is what capture blocks on.

Forwards the caller's hospital JWT rather than minting one — same signing
key, no audience claim, no privilege gained.

Fails open: a consent-service outage leaves the board unbadged and usable."
```

---

### Task 4: admin-dashboard — badge, then vanish

**Files:**
- Modify: `frontend/admin-dashboard/src/api/types.ts` (`PendingRow`)
- Modify: `frontend/admin-dashboard/src/pages/Reception.tsx`
- Modify: `frontend/admin-dashboard/src/pages/Reception.module.css`
- Modify: `frontend/admin-dashboard/src/pages/Reception.test.tsx`

**Interfaces:**
- Consumes: `GET /api/reception/registrations` (proxied to Task 3's `List`) — each row now carries `consented: boolean`.
- Produces: no exports beyond the existing `Reception` component.

- [ ] **Step 1: Write the failing test**

In `frontend/admin-dashboard/src/pages/Reception.test.tsx`, update the import on line 1 to include `act`:

```tsx
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor, act } from "@testing-library/react";
```

Add `consented: false` to each of the three existing fixture rows (lines 11-13), then add:

```tsx
const consentedRow = {
  hms_patient_id: "PA-4",
  name: "Meera",
  mobile: "95****1111",
  status: "PENDING",
  registered_at: "2026-07-16T10:00:00Z",
  consented: true,
};

it("badges an already-consented row and disables its action", async () => {
  (api.receptionRegistrations as any).mockResolvedValue([consentedRow]);
  render(<Reception />);

  await waitFor(() => expect(screen.getByText("Meera")).toBeInTheDocument());
  expect(screen.getByText(/already consented/i)).toBeInTheDocument();
  expect(screen.getByRole("button", { name: /send code/i })).toBeDisabled();
});

// The regression test for the trap: the queue re-polls every 5s and hands back a
// fresh `rows` array each time. Arming the hide-timer per render would reset it
// on every poll, so a 15s timer inside a 5s poll would never fire and the row
// would never disappear. Two polls must land inside the window without resetting it.
it("drops a consented row after 15s, surviving intervening polls", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  (api.receptionRegistrations as any).mockResolvedValue([consentedRow]);
  render(<Reception />);

  await waitFor(() => expect(screen.getByText("Meera")).toBeInTheDocument());

  // t+10s: two 5s polls have landed. The row must still be here.
  await act(async () => {
    await vi.advanceTimersByTimeAsync(10_000);
  });
  expect(screen.getByText("Meera")).toBeInTheDocument();

  // t+16s: past the 15s window.
  await act(async () => {
    await vi.advanceTimersByTimeAsync(6_000);
  });
  await waitFor(() => expect(screen.queryByText("Meera")).not.toBeInTheDocument());

  vi.useRealTimers();
});

it("keeps a not-yet-consented row on the board indefinitely", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  (api.receptionRegistrations as any).mockResolvedValue([rows[0]]);
  render(<Reception />);

  await waitFor(() => expect(screen.getByText("Asha")).toBeInTheDocument());
  await act(async () => {
    await vi.advanceTimersByTimeAsync(30_000);
  });
  expect(screen.getByText("Asha")).toBeInTheDocument();

  vi.useRealTimers();
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd /home/reddy/Documents/Go/DPDP/frontend/admin-dashboard && npx vitest run src/pages/Reception.test.tsx`

Expected: FAIL — "Unable to find an element with the text: /already consented/i".

- [ ] **Step 3: Add `consented` to the row type**

In `frontend/admin-dashboard/src/api/types.ts`, in `PendingRow`, add after `status`:

```ts
  consented: boolean; // already has an active consent — no action needed
```

- [ ] **Step 4: Implement the badge and the auto-hide**

Replace `frontend/admin-dashboard/src/pages/Reception.tsx` with:

```tsx
import { useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError } from "../api/client";
import type { PendingRow } from "../api/types";
import { DataTable, type Column } from "../components/DataTable";
import styles from "./Reception.module.css";

const POLL_MS = 5000;
// How long an already-consented row stays on the board before it drops off:
// long enough for reception to see the patient was handled, short enough that
// the queue stays a list of things to actually do.
const HIDE_CONSENTED_MS = 15000;

export function Reception() {
  const [rows, setRows] = useState<PendingRow[]>([]);
  const [error, setError] = useState("");
  const [sending, setSending] = useState<Record<string, boolean>>({});
  const [hidden, setHidden] = useState<Record<string, boolean>>({});
  const hideTimers = useRef<Record<string, ReturnType<typeof setTimeout>>>({});

  const load = useCallback(async () => {
    try {
      const all = await api.receptionRegistrations();
      // Completion by disappearance: DONE rows leave the queue.
      setRows(all.filter((r) => r.status !== "DONE"));
      setError("");
    } catch {
      setError("Could not load the queue.");
    }
  }, []);

  useEffect(() => {
    load();
    const t = setInterval(load, POLL_MS);
    return () => clearInterval(t);
  }, [load]);

  // Arm the hide timer ONCE per patient, on first sighting. The poll above hands
  // back a fresh `rows` array every 5s, so re-arming per render would reset a 15s
  // timer that then never fires — the row would stay forever.
  useEffect(() => {
    for (const r of rows) {
      if (r.consented && !hideTimers.current[r.hms_patient_id]) {
        hideTimers.current[r.hms_patient_id] = setTimeout(
          () => setHidden((h) => ({ ...h, [r.hms_patient_id]: true })),
          HIDE_CONSENTED_MS,
        );
      }
    }
  }, [rows]);

  useEffect(() => {
    const timers = hideTimers.current;
    return () => Object.values(timers).forEach(clearTimeout);
  }, []);

  async function send(hms: string) {
    setSending((s) => ({ ...s, [hms]: true }));
    try {
      await api.sendCode(hms);
      await load();
    } catch (e) {
      setError(e instanceof ApiError && e.status === 429 ? "Please wait before resending." : "Could not send the code.");
    } finally {
      setSending((s) => ({ ...s, [hms]: false }));
    }
  }

  const columns: Column<PendingRow>[] = [
    { key: "name", header: "Patient", render: (r) => r.name },
    { key: "mobile", header: "Mobile", render: (r) => r.mobile },
    {
      key: "status",
      header: "Status",
      render: (r) =>
        r.consented ? (
          <span className={styles.badge} data-status="CONSENTED">Already consented — no action</span>
        ) : (
          <span className={styles.badge} data-status={r.status}>{r.status === "CODE_SENT" ? "Code sent" : "Awaiting"}</span>
        ),
    },
    {
      key: "action",
      header: "",
      render: (r) => (
        <button
          className={styles.action}
          disabled={r.consented || !!sending[r.hms_patient_id]}
          onClick={() => send(r.hms_patient_id)}
        >
          {r.status === "CODE_SENT" ? "Resend" : "Send code"}
        </button>
      ),
    },
  ];

  const visible = rows.filter((r) => !hidden[r.hms_patient_id]);

  return (
    <div className={styles.wrap}>
      <h1>Consent queue</h1>
      {error && <p className={styles.error} role="alert">{error}</p>}
      <DataTable columns={columns} rows={visible} empty="No patients awaiting consent." />
    </div>
  );
}
```

- [ ] **Step 5: Style the badge**

In `frontend/admin-dashboard/src/pages/Reception.module.css`, append:

```css
.badge[data-status="CONSENTED"] { background: #e6f4ea; }
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd /home/reddy/Documents/Go/DPDP/frontend/admin-dashboard && npx vitest run src/pages/Reception.test.tsx`

Expected: PASS — all five tests (two pre-existing, three new).

- [ ] **Step 7: Typecheck and run the whole frontend suite**

Run: `cd /home/reddy/Documents/Go/DPDP/frontend/admin-dashboard && npx tsc --noEmit && npm test`

Expected: PASS, no type errors.

- [ ] **Step 8: Verify in the real dashboard**

With the full stack up, log into the admin dashboard as reception and open the Consent queue. Stage a patient who already holds an active consent (`FLOWS.md`'s webhook curl with that patient's `hms_patient_id`).

Expected: their row appears badged "Already consented — no action" with **Send code greyed out**, and disappears roughly 15 seconds later without a page refresh. A freshly-registered patient's row appears as "Awaiting" with a live Send code button and stays put.

- [ ] **Step 9: Commit**

```bash
git add frontend/admin-dashboard/src/api/types.ts \
        frontend/admin-dashboard/src/pages/Reception.tsx \
        frontend/admin-dashboard/src/pages/Reception.module.css \
        frontend/admin-dashboard/src/pages/Reception.test.tsx
git commit -m "feat(dashboard): badge already-consented rows, drop after 15s

A returning patient shows as 'Already consented — no action' with Send
code disabled, then leaves the board after 15s so the queue stays a list
of things to actually do.

The hide timer is armed once per patient via a ref: the 5s poll replaces
rows, so re-arming per render would reset a 15s timer that never fires."
```

---

## Verification

After Task 4, walk the original bug end to end:

1. Capture consent for a patient at the kiosk (per `FLOWS.md`).
2. Re-fire the HMS registration webhook for that same patient — the bug's trigger.
3. Open the reception queue.

**Before this change:** the row shows `PENDING` with a live Send code button, and following it through ends in a kiosk 409.
**After:** the row shows "Already consented — no action", Send code is disabled, and the row leaves the board after ~15s. No SMS, no walk.
