# Patient Identity Key Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a patient's identity `(patient_key, hms_patient_id)` instead of `patient_key` alone, so families sharing one mobile number stop collapsing onto a single consent identity.

**Architecture:** `patient_key` keeps meaning "this mobile at this hospital" (a contact channel); `hms_patient_id`, already a column on `consent_vault`, means "which patient". Every identity lookup scopes by both. Key derivation is unchanged, so no artifact hash is invalidated. Separately, the OTP session gains the HMS patient ID it was already given at claim time, so the server — not the client — enforces that a verified session and the patient being consented for belong together.

**Tech Stack:** Go 1.x (gin, pgx), PostgreSQL with RLS + append-only triggers, Redis, plain-SQL tracked migrations via `DPDP/scripts/db/migrate.sh`.

**Spec:** `docs/superpowers/specs/2026-07-20-patient-identity-key-design.md`

## Global Constraints

- Identity on consent rows is the pair `(patient_key, hms_patient_id)`. Never look up a patient by `patient_key` alone.
- `ComputePatientKey` is **not** changed. Its signature and output stay exactly as they are — changing it would invalidate every `artifact_hash`.
- emergency-service is **not** touched. It writes `patient_key` and `hms_patient_id` as nullable evidence and must keep working with neither.
- `consent_vault` is append-only: no `UPDATE`, no `DELETE`. Triggers enforce this.
- Raw mobile numbers never appear in URLs, logs, or list responses.
- Go module layout is a `go.work` workspace — run `go build ./...` from each service directory, not the repo root.
- Run tests with `-race`, matching each service's `make test`.

---

### Task 1: Identity-scoped lookup on the write paths

This is the task that fixes the bug. `GetLatestByPatientKey` is the single function all three broken paths route through, so renaming and re-scoping it forces every caller to be correct.

**Files:**
- Modify: `consent-service/pkg/consent/repository/queries.go:21-25`
- Modify: `consent-service/pkg/consent/repository/interface.go:22-23`
- Modify: `consent-service/pkg/consent/repository/repository.go:151-161`
- Modify: `consent-service/pkg/consent/model/consent.go:81-90` (CaptureConsentRequest), `:112-119` (WithdrawConsentRequest), `:121-128` (GrantConsentRequest). Do **not** touch `CheckConsentRequest` at `:92-101` — that is Task 2.
- Modify: `consent-service/pkg/consent/service/consent_service.go:151,304,400`
- Test: `consent-service/pkg/consent/service/identity_test.go` (create)

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `repository.ConsentRepository.GetLatestByPatientAndHMS(ctx context.Context, hospitalID, patientKey, hmsPatientID string) (*model.Consent, error)` — replaces `GetLatestByPatientKey`. `model.CaptureConsentRequest.HMSPatientID`, `model.WithdrawConsentRequest.HMSPatientID`, `model.GrantConsentRequest.HMSPatientID` all become required strings.

- [ ] **Step 1: Write the failing test**

Create `consent-service/pkg/consent/service/identity_test.go`:

```go
package service

import (
	"context"
	"errors"
	"testing"

	"github.com/hiabhi-cpu/consent-service/pkg/consent/model"
	"github.com/hiabhi-cpu/consent-service/pkg/consent/repository"
	sharedcrypto "github.com/hiabhi-cpu/shared/crypto"
)

const (
	testSalt     = "test-system-salt"
	testHospKey  = "test-hospital-key"
	familyMobile = "9876543210"
)

// fakeIdentityRepo keys its canned rows by the full identity pair, so a lookup
// scoped only by patient_key cannot find them.
type fakeIdentityRepo struct {
	repository.ConsentRepository // embedded: methods we don't exercise panic loudly
	existing                     map[string]*model.Consent
	inserted                     []*model.Consent
}

func (f *fakeIdentityRepo) GetLatestByPatientAndHMS(_ context.Context, _, patientKey, hmsPatientID string) (*model.Consent, error) {
	return f.existing[patientKey+"|"+hmsPatientID], nil
}

func (f *fakeIdentityRepo) GetByIdempotencyKey(context.Context, string, string) (*model.Consent, error) {
	return nil, nil
}

func (f *fakeIdentityRepo) Insert(_ context.Context, c *model.Consent, _ *model.OutboxRecord) error {
	f.inserted = append(f.inserted, c)
	return nil
}

type fakeSecrets struct{}

func (fakeSecrets) GetSystemSalt(context.Context) (string, error) { return testSalt, nil }
func (fakeSecrets) GetHospitalKey(context.Context, string) (string, error) {
	return testHospKey, nil
}

type okSessions struct{}

func (okSessions) Verify(context.Context, string, string) error { return nil }

func familyKey() string {
	return sharedcrypto.ComputePatientKey(familyMobile, testSalt, testHospKey)
}

// The bug this whole change exists to fix: a mother and son share one mobile.
// Her active consent must not block his.
func TestCaptureAllowsSecondFamilyMemberOnSharedMobile(t *testing.T) {
	repo := &fakeIdentityRepo{existing: map[string]*model.Consent{
		familyKey() + "|PA-mother": {
			Purposes: map[string]model.PurposeState{"treatment": model.PurposeActive},
		},
	}}
	svc := NewConsentService(repo, fakeSecrets{}, okSessions{})

	_, created, err := svc.Capture(context.Background(), "hosp-1", "1.2.3.4", &model.CaptureConsentRequest{
		Mobile:       familyMobile,
		SessionID:    "sess-son",
		Purposes:     []string{"treatment"},
		HMSPatientID: "PA-son",
	})
	if err != nil {
		t.Fatalf("son's capture on the shared family mobile must succeed, got %v", err)
	}
	if !created {
		t.Fatal("expected a newly created consent row for the son")
	}
	if len(repo.inserted) != 1 || repo.inserted[0].HMSPatientID != "PA-son" {
		t.Fatalf("inserted = %+v, want one row for PA-son", repo.inserted)
	}
}

// The block must still fire for the SAME patient — this is the guard that keeps
// duplicate consents out, and re-scoping identity must not disable it.
func TestCaptureStillBlocksTheSamePatientTwice(t *testing.T) {
	repo := &fakeIdentityRepo{existing: map[string]*model.Consent{
		familyKey() + "|PA-mother": {
			Purposes: map[string]model.PurposeState{"treatment": model.PurposeActive},
		},
	}}
	svc := NewConsentService(repo, fakeSecrets{}, okSessions{})

	_, _, err := svc.Capture(context.Background(), "hosp-1", "1.2.3.4", &model.CaptureConsentRequest{
		Mobile:       familyMobile,
		SessionID:    "sess-mother-again",
		Purposes:     []string{"treatment"},
		HMSPatientID: "PA-mother",
	})
	if !errors.Is(err, ErrActiveConsentExists) {
		t.Fatalf("err = %v, want ErrActiveConsentExists", err)
	}
}

// Withdrawal is the mirror of the capture bug and the more dangerous half: on
// the old scoping the son's withdrawal found — and revoked — his mother's
// consent. Here he has none of his own, so there is nothing to withdraw.
func TestWithdrawDoesNotTouchARelativesConsent(t *testing.T) {
	repo := &fakeIdentityRepo{existing: map[string]*model.Consent{
		familyKey() + "|PA-mother": {
			Purposes: map[string]model.PurposeState{"treatment": model.PurposeActive},
		},
	}}
	svc := NewConsentService(repo, fakeSecrets{}, okSessions{})

	err := svc.Withdraw(context.Background(), "hosp-1", "1.2.3.4", &model.WithdrawConsentRequest{
		Mobile:       familyMobile,
		HMSPatientID: "PA-son",
		SessionID:    "sess-son",
	})
	if !errors.Is(err, ErrNoActiveConsent) {
		t.Fatalf("err = %v, want ErrNoActiveConsent — the son has no consent of his own", err)
	}
	if len(repo.withdrawn) != 0 {
		t.Fatalf("wrote %d withdrawal rows, want 0 — the mother's consent must be untouched", len(repo.withdrawn))
	}
}
```

`Withdraw` writes through `InsertWithdrawn`, so the fake needs to record that too. Add the field and method to `fakeIdentityRepo`:

```go
type fakeIdentityRepo struct {
	repository.ConsentRepository // embedded: methods we don't exercise panic loudly
	existing                     map[string]*model.Consent
	inserted                     []*model.Consent
	withdrawn                    []*model.Consent
}

func (f *fakeIdentityRepo) InsertWithdrawn(_ context.Context, c *model.Consent, _ *model.OutboxRecord) error {
	f.withdrawn = append(f.withdrawn, c)
	return nil
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd consent-service && go test ./pkg/consent/service/ -run "TestCapture|TestWithdraw" -v`
Expected: FAIL to compile — `undefined: (*fakeIdentityRepo).GetLatestByPatientAndHMS` does not satisfy the interface, and `CaptureConsentRequest` has no matching lookup. This confirms the test is exercising the new contract.

- [ ] **Step 3: Re-scope the query**

In `consent-service/pkg/consent/repository/queries.go`, change `queryGetLatestConsent`:

```go
	// Identity is the PAIR (patient_key, hms_patient_id). patient_key alone is a
	// contact channel — families share one mobile, so scoping by it alone returns
	// a relative's row. See docs/superpowers/specs/2026-07-20-patient-identity-key-design.md
	queryGetLatestConsent = `
		SELECT ` + consentColumns + `
		FROM consent.consent_vault
		WHERE hospital_id = $1 AND patient_key = $2 AND hms_patient_id = $3
		ORDER BY version DESC LIMIT 1
	`
```

- [ ] **Step 4: Rename the repository method**

In `consent-service/pkg/consent/repository/interface.go`, replace the `GetLatestByPatientKey` declaration:

```go
	// GetLatestByPatientAndHMS returns the most recent consent row (any status)
	// for one patient — identified by the pair (patient_key, hms_patient_id) —
	// or nil. Callers inspect its per-purpose Purposes map for current state.
	GetLatestByPatientAndHMS(ctx context.Context, hospitalID, patientKey, hmsPatientID string) (*model.Consent, error)
```

In `consent-service/pkg/consent/repository/repository.go`, replace lines 151-161:

```go
// GetLatestByPatientAndHMS returns the most recent consent row for one patient
// (regardless of aggregate status), or nil if none exists. It returns the latest
// row even when fully withdrawn, because Check and Withdraw both need the current
// per-purpose map — the caller inspects Purposes to decide per purpose.
func (r *pgxConsentRepository) GetLatestByPatientAndHMS(ctx context.Context, hospitalID, patientKey, hmsPatientID string) (*model.Consent, error) {
	c, err := r.getOneConsent(ctx, hospitalID, queryGetLatestConsent, hospitalID, patientKey, hmsPatientID)
	if err != nil {
		return nil, fmt.Errorf("repository.GetLatestByPatientAndHMS: %w", err)
	}
	return c, nil
}
```

- [ ] **Step 5: Require the HMS ID on the three write requests**

In `consent-service/pkg/consent/model/consent.go`, replace the `CaptureConsentRequest` block (and its doc comment) at lines 81-90:

```go
// CaptureConsentRequest is the body for POST /api/v1/consent/capture.
// HMSPatientID is the hospital's own opaque patient ID (from the HMS webhook)
// and is REQUIRED: identity is the pair (mobile, hms_patient_id), because a
// family shares one mobile number.
type CaptureConsentRequest struct {
	Mobile       string   `json:"mobile" binding:"required,len=10"`
	SessionID    string   `json:"session_id" binding:"required"`
	Purposes     []string `json:"purposes" binding:"required,min=1"`
	HMSPatientID string   `json:"hms_patient_id" binding:"required"`
}
```

Add the same field to `WithdrawConsentRequest` and `GrantConsentRequest`:

```go
type WithdrawConsentRequest struct {
	Mobile       string   `json:"mobile" binding:"required,len=10"`
	HMSPatientID string   `json:"hms_patient_id" binding:"required"`
	SessionID    string   `json:"session_id" binding:"required"`
	Purposes     []string `json:"purposes"`
}

type GrantConsentRequest struct {
	Mobile       string   `json:"mobile" binding:"required,len=10"`
	HMSPatientID string   `json:"hms_patient_id" binding:"required"`
	SessionID    string   `json:"session_id" binding:"required"`
	Purposes     []string `json:"purposes" binding:"required,min=1"`
}
```

- [ ] **Step 6: Update the three service call sites**

In `consent-service/pkg/consent/service/consent_service.go`, in `Capture` (around line 151):

```go
	existing, err := s.repo.GetLatestByPatientAndHMS(ctx, hospitalID, patientKey, req.HMSPatientID)
```

In `Withdraw` (around line 304):

```go
	existing, err := s.repo.GetLatestByPatientAndHMS(ctx, hospitalID, patientKey, req.HMSPatientID)
```

In `Grant` — find the `GetLatestByPatientKey` call in the `Grant` body (starts line 399) and make the identical change:

```go
	existing, err := s.repo.GetLatestByPatientAndHMS(ctx, hospitalID, patientKey, req.HMSPatientID)
```

- [ ] **Step 7: Run the tests**

Run: `cd consent-service && go test ./... -race`
Expected: PASS. Both new tests green, `stats_service_test.go` still green.

- [ ] **Step 8: Verify nothing else referenced the old name**

Run: `cd /home/reddy/Documents/Go/DPDP && grep -rn "GetLatestByPatientKey" --include="*.go" .`
Expected: no output.

- [ ] **Step 9: Commit**

```bash
git add consent-service/pkg/consent/
git commit -m "fix(consent): scope patient identity to (patient_key, hms_patient_id)

A family shares one mobile, so patient_key alone identified a household,
not a person: the second member could never consent, because capture found
a relative's active row and returned 409."
```

---

### Task 2: Check drops mobile entirely

**Files:**
- Modify: `consent-service/pkg/consent/model/consent.go:92-101` (the `CheckConsentRequest` block — line numbers as they stand after Task 1, which does not touch this struct)
- Modify: `consent-service/pkg/consent/controller/consent_handler.go:60-92`
- Modify: `consent-service/pkg/consent/service/consent_service.go:220-270`
- Test: `consent-service/pkg/consent/service/identity_test.go` (append)

**Interfaces:**
- Consumes: `GetLatestByPatientAndHMS` from Task 1 (not called here, but the file must still compile against it).
- Produces: `model.CheckConsentRequest{HMSPatientID string, Purpose string}` — the `Mobile` field is gone.

No in-repo caller sends `mobile` to `/check` — verified by grep across Go and TypeScript — so this is a safe removal, not a breaking change to a live client.

- [ ] **Step 1: Write the failing test**

Append to `consent-service/pkg/consent/service/identity_test.go`:

```go
// Check must answer for the patient named by hms_patient_id, not for whichever
// family member on that mobile consented most recently.
func TestCheckAnswersForTheNamedPatient(t *testing.T) {
	repo := &fakeIdentityRepo{hmsRows: map[string]*model.Consent{
		"PA-son": {
			PatientKey: familyKey(),
			Purposes:   map[string]model.PurposeState{"treatment": model.PurposeWithdrawn},
		},
	}}
	svc := NewConsentService(repo, fakeSecrets{}, okSessions{})

	resp, err := svc.Check(context.Background(), "hosp-1", "1.2.3.4", &model.CheckConsentRequest{
		HMSPatientID: "PA-son",
		Purpose:      "treatment",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Allowed {
		t.Fatal("son withdrew treatment; check must not report allowed")
	}
}
```

Extend `fakeIdentityRepo` with the HMS-keyed map and its lookup, and add the audit enqueue that `Check` needs:

```go
type fakeIdentityRepo struct {
	repository.ConsentRepository
	existing  map[string]*model.Consent
	hmsRows   map[string]*model.Consent
	inserted  []*model.Consent
	withdrawn []*model.Consent
}

func (f *fakeIdentityRepo) GetLatestByHMSPatientID(_ context.Context, _, hmsPatientID string) (*model.Consent, error) {
	return f.hmsRows[hmsPatientID], nil
}

func (f *fakeIdentityRepo) EnqueueAudit(context.Context, *model.OutboxRecord) error { return nil }
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd consent-service && go test ./pkg/consent/service/ -run TestCheckAnswersForTheNamedPatient -v`
Expected: FAIL to compile — `CheckConsentRequest` still has a `Mobile` field and the handler's dual-identity rule still exists.

- [ ] **Step 3: Drop Mobile from the request model**

In `consent-service/pkg/consent/model/consent.go`, replace the `CheckConsentRequest` block and its doc comment:

```go
// CheckConsentRequest is the body for POST /api/v1/consent/check. Purpose is
// required — checks are purpose-scoped (plan §11). The patient is identified by
// hms_patient_id, which is opaque and non-PII. Mobile is deliberately absent: it
// identifies a household, not a person, so it can only ever select the wrong
// family member.
type CheckConsentRequest struct {
	HMSPatientID string `json:"hms_patient_id" binding:"required"`
	Purpose      string `json:"purpose" binding:"required"`
}
```

- [ ] **Step 4: Delete the dual-identity branch in the handler**

In `consent-service/pkg/consent/controller/consent_handler.go`, replace the body of `Check` between the JSON bind and the service call — delete lines 72-83 entirely (the `hasMobile`/`hasHMS` block and the mobile length check). The handler becomes:

```go
	var req model.CheckConsentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "hms_patient_id and purpose are required"})
		return
	}

	resp, err := h.svc.Check(c.Request.Context(), hospitalID, c.ClientIP(), &req)
```

Also update the handler's doc comment on line 58-59:

```go
// Check handles POST /api/consent/v1/check. The patient is named by
// hms_patient_id — opaque and non-PII, so no raw mobile reaches access logs.
```

- [ ] **Step 5: Simplify the service**

In `consent-service/pkg/consent/service/consent_service.go`, replace lines 220-246 (the top of `Check` through the identity resolution) with:

```go
func (s *consentService) Check(ctx context.Context, hospitalID, ip string, req *model.CheckConsentRequest) (*model.CheckConsentResponse, error) {
	// Identity is the HMS patient ID. patientKey is read off the found row and
	// used only for the audit record — it is never a lookup input here.
	latest, err := s.repo.GetLatestByHMSPatientID(ctx, hospitalID, req.HMSPatientID)
	if err != nil {
		return nil, fmt.Errorf("ConsentService.Check: %w", err)
	}
	var patientKey string
	if latest != nil {
		patientKey = latest.PatientKey
	}
```

Then replace the conditional details block at lines 266-269 with an unconditional one:

```go
	details := map[string]any{
		"purpose":        req.Purpose,
		"allowed":        resp.Allowed,
		"reason":         resp.Reason,
		"hms_patient_id": req.HMSPatientID,
	}
```

- [ ] **Step 6: Run the tests**

Run: `cd consent-service && go test ./... -race`
Expected: PASS.

- [ ] **Step 7: Confirm no caller still sends mobile to check**

Run: `cd /home/reddy/Documents/Go/DPDP && grep -rn "consent/v1/check" --include="*.go" --include="*.ts" --include="*.tsx" .`
Expected: matches only inside `consent-service` (route registration, handler comment). No client sends a body at all.

- [ ] **Step 8: Commit**

```bash
git add consent-service/pkg/consent/
git commit -m "fix(consent): identify check by hms_patient_id only

Mobile identifies a household, not a person, so a mobile-only check returned
whichever family member consented last. Removing the field also deletes the
exactly-one-of-mobile-or-hms validation branch."
```

---

### Task 3: Carry the HMS patient ID into audit events

**Files:**
- Modify: `consent-service/pkg/consent/service/consent_service.go` (the `details` maps in `Capture`, `Withdraw`, `Grant`)
- Test: `consent-service/pkg/consent/service/identity_test.go` (append)

**Interfaces:**
- Consumes: `model.CaptureConsentRequest.HMSPatientID` from Task 1.
- Produces: every consent audit event's `details` JSON carries `hms_patient_id`.

`audit_log.patient_key` and `actor_id` stay household-scoped, and `idx_audit_patient_key` still narrows to the household — the JSON field is what makes the per-patient trail recoverable from there.

- [ ] **Step 1: Write the failing test**

Append to `consent-service/pkg/consent/service/identity_test.go`:

```go
// The audit trail must name the individual. actor_id/patient_key are derived
// from the shared mobile, so without hms_patient_id in details a family's
// events are indistinguishable.
func TestCaptureAuditCarriesHMSPatientID(t *testing.T) {
	repo := &fakeIdentityRepo{existing: map[string]*model.Consent{}}
	svc := NewConsentService(repo, fakeSecrets{}, okSessions{})

	_, _, err := svc.Capture(context.Background(), "hosp-1", "1.2.3.4", &model.CaptureConsentRequest{
		Mobile:       familyMobile,
		SessionID:    "sess-son",
		Purposes:     []string{"treatment"},
		HMSPatientID: "PA-son",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var event AuditEvent
	if err := json.Unmarshal(repo.lastOutbox.Payload, &event); err != nil {
		t.Fatalf("unmarshal outbox payload: %v", err)
	}
	if got := event.Details["hms_patient_id"]; got != "PA-son" {
		t.Fatalf("details[hms_patient_id] = %v, want PA-son", got)
	}
}
```

Add `encoding/json` to the test imports, and capture the outbox record in the fake:

```go
type fakeIdentityRepo struct {
	repository.ConsentRepository
	existing   map[string]*model.Consent
	hmsRows    map[string]*model.Consent
	inserted   []*model.Consent
	withdrawn  []*model.Consent
	lastOutbox *model.OutboxRecord
}

func (f *fakeIdentityRepo) Insert(_ context.Context, c *model.Consent, o *model.OutboxRecord) error {
	f.inserted = append(f.inserted, c)
	f.lastOutbox = o
	return nil
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd consent-service && go test ./pkg/consent/service/ -run TestCaptureAuditCarriesHMSPatientID -v`
Expected: FAIL — `details[hms_patient_id] = <nil>, want PA-son`.

- [ ] **Step 3: Add the field to the three details maps**

Three maps in `consent-service/pkg/consent/service/consent_service.go`. `Check`'s map (line 281, built into the `details` variable) was already handled in Task 2.

`Capture`, line 201 — currently a one-liner, becomes:

```go
		Details: map[string]any{
			"purposes":       req.Purposes,
			"session_id":     req.SessionID,
			"hms_patient_id": req.HMSPatientID,
		},
```

`Withdraw`, line 377:

```go
		Details: map[string]any{
			"session_id":         req.SessionID,
			"previous_id":        existing.ID,
			"withdrawn_purposes": withdrawn,
			"remaining_status":   withdrawnConsent.Status,
			"hms_patient_id":     req.HMSPatientID,
		},
```

`Grant`, line 471:

```go
		Details: map[string]any{
			"session_id":       req.SessionID,
			"previous_id":      existing.ID,
			"granted_purposes": granted,
			"renewal":          true,
			"hms_patient_id":   req.HMSPatientID,
		},
```

- [ ] **Step 4: Run the tests**

Run: `cd consent-service && go test ./... -race`
Expected: PASS.

- [ ] **Step 5: Add the ceiling comment**

Above the `Capture` details map, record the deliberate shortcut:

```go
	// ponytail: hms_patient_id rides in details JSONB rather than its own audit_log
	// column. idx_audit_patient_key narrows to the household (a handful of rows),
	// then this field picks the individual — fine at pilot scale. If per-household
	// audit volume grows, promote it to a column with its own index.
```

- [ ] **Step 6: Commit**

```bash
git add consent-service/pkg/consent/service/
git commit -m "feat(consent): carry hms_patient_id in consent audit details

patient_key and actor_id are derived from the shared family mobile, so the
audit trail could not distinguish which family member acted."
```

---

### Task 4: Bind the OTP session to the patient

**Files:**
- Modify: `notification-service/pkg/otp/model/otp.go:23-34`
- Modify: `notification-service/pkg/otp/service/otp_service.go:18-21,125-151,246`
- Modify: `notification-service/pkg/otp/controller/otp_handler.go:65-86`
- Test: `notification-service/pkg/otp/service/session_binding_test.go` (create)

**Interfaces:**
- Consumes: nothing from earlier tasks (different service).
- Produces: `model.SessionState{Mobile, Ref, Verified, ExpiresAt}`; `model.ValidateSessionRequest{SessionID, Mobile, HMSPatientID}`; `service.OTPService.ValidateSession(ctx context.Context, sessionID, mobile, hmsPatientID string) error`. Wire format for `POST /internal/v1/otp/session/validate` gains `"hms_patient_id"`.

The claim store already keeps `ref` — the HMS patient ID — beside the mobile, and `ResolveClaim` has it in hand when it mints the session. It is currently dropped on the floor.

- [ ] **Step 1: Write the failing test**

Create `notification-service/pkg/otp/service/session_binding_test.go`. `claim_service_test.go` in this package already builds a service over `miniredis` via `newClaimService(t)`, but that helper returns the SMS spy, not the store — these tests need to plant a session directly, so they add a sibling helper in the same shape.

```go
package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/hiabhi-cpu/notification-service/pkg/otp/model"
	"github.com/hiabhi-cpu/notification-service/pkg/otp/repository"
)

// newSessionService mirrors newClaimService (claim_service_test.go) but hands
// back the store, so a test can plant a SessionState directly.
func newSessionService(t *testing.T) (OTPService, repository.OTPStore) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	store := repository.NewRedisStore(redis.NewClient(&redis.Options{Addr: mr.Addr()}))
	return NewOTPService(store, &captureSMS{}), store
}

// A session minted for the son must not authorize a capture naming the mother,
// even though both share the mobile the OTP was sent to.
func TestValidateSessionRejectsMismatchedPatient(t *testing.T) {
	ctx := context.Background()
	svc, repo := newSessionService(t)

	if err := repo.SaveSession(ctx, "sess-1", model.SessionState{
		Mobile:    "9876543210",
		Ref:       "PA-son",
		Verified:  true,
		ExpiresAt: time.Now().Add(time.Minute),
	}, time.Minute); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	if err := svc.ValidateSession(ctx, "sess-1", "9876543210", "PA-son"); err != nil {
		t.Fatalf("matching patient must validate, got %v", err)
	}

	err := svc.ValidateSession(ctx, "sess-1", "9876543210", "PA-mother")
	if !errors.Is(err, ErrSessionNotVerified) {
		t.Fatalf("err = %v, want ErrSessionNotVerified for a different family member", err)
	}
}

// A session with no ref cannot name a patient, so it cannot consent for one.
func TestValidateSessionRejectsRefLessSession(t *testing.T) {
	ctx := context.Background()
	svc, repo := newSessionService(t)

	if err := repo.SaveSession(ctx, "sess-2", model.SessionState{
		Mobile:    "9876543210",
		Verified:  true,
		ExpiresAt: time.Now().Add(time.Minute),
	}, time.Minute); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	err := svc.ValidateSession(ctx, "sess-2", "9876543210", "PA-son")
	if !errors.Is(err, ErrSessionNotVerified) {
		t.Fatalf("err = %v, want ErrSessionNotVerified for a ref-less session", err)
	}
}
```


- [ ] **Step 2: Run test to verify it fails**

Run: `cd notification-service && go test ./pkg/otp/service/ -run TestValidateSession -v`
Expected: FAIL to compile — `model.SessionState` has no field `Ref`, and `ValidateSession` takes 3 arguments, not 4.

- [ ] **Step 3: Add Ref to the session state and the validate request**

In `notification-service/pkg/otp/model/otp.go`:

```go
// SessionState is what gets stored in Redis after successful verification.
// Ref is the HMS patient ID the OTP was issued for. It is what makes the
// session name a PERSON rather than a phone: a family shares one mobile, so
// mobile alone cannot say who consented.
type SessionState struct {
	Mobile    string    `json:"mobile"`
	Ref       string    `json:"ref"`
	Verified  bool      `json:"verified"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ValidateSessionRequest is the body for POST /internal/v1/otp/session/validate.
type ValidateSessionRequest struct {
	SessionID    string `json:"session_id" binding:"required"`
	Mobile       string `json:"mobile" binding:"required,len=10"`
	HMSPatientID string `json:"hms_patient_id" binding:"required"`
}
```

- [ ] **Step 4: Enforce the pairing in the service**

In `notification-service/pkg/otp/service/otp_service.go`, update the interface declaration at line 21:

```go
	// ValidateSession reports whether sessionID is a live, OTP-verified session
	// for BOTH the mobile and the HMS patient ID it was issued for.
	ValidateSession(ctx context.Context, sessionID, mobile, hmsPatientID string) error
```

And the implementation at lines 139-151:

```go
// ValidateSession confirms a live verified session exists for
// (sessionID, mobile, hmsPatientID). The mobile must match the number the OTP
// was sent to, and the ref must match the patient it was issued for — a family
// shares one mobile, so the mobile alone does not identify who is consenting.
// A session with no ref cannot name a patient and is always rejected.
func (s *otpService) ValidateSession(ctx context.Context, sessionID, mobile, hmsPatientID string) error {
	state, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("service.ValidateSession: %w", err)
	}
	if state == nil || !state.Verified || state.Mobile != mobile {
		return ErrSessionNotVerified
	}
	if state.Ref == "" || state.Ref != hmsPatientID {
		return ErrSessionNotVerified
	}
	return nil
}
```

- [ ] **Step 5: Populate Ref when the claim mints a session**

In `ResolveClaim` (around line 246), the `ref` is already in scope:

```go
		state := model.SessionState{Mobile: mobile, Ref: ref, Verified: true, ExpiresAt: time.Now().Add(sessionExpiry)}
```

Leave the generic `Verify` path (around line 126) untouched — it has no ref, so the sessions it mints stay unable to authorize a capture. That is intended. Add a comment there:

```go
	// No Ref: the walk-in OTP path does not name a patient, so sessions it mints
	// cannot authorize a consent capture. A patient portal must supply the HMS
	// patient ID to mint a usable session.
	state := model.SessionState{
		Mobile:    req.Mobile,
		Verified:  true,
		ExpiresAt: time.Now().Add(sessionExpiry),
	}
```

- [ ] **Step 6: Pass it through the handler**

In `notification-service/pkg/otp/controller/otp_handler.go`, update `ValidateSession`:

```go
	var req model.ValidateSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session_id, mobile and hms_patient_id are required"})
		return
	}

	if err := h.svc.ValidateSession(c.Request.Context(), req.SessionID, req.Mobile, req.HMSPatientID); err != nil {
```

- [ ] **Step 7: Fix the existing claim test**

`notification-service/pkg/otp/service/claim_service_test.go:46` calls `ValidateSession` with three arguments. Update it to pass the ref the claim was created with — read the test to find which ref it uses (`PA-1` in the store fixtures) and pass that:

```go
	if err := svc.ValidateSession(ctx, res.SessionID, "9876543210", res.Ref); err != nil {
		t.Fatalf("ValidateSession after resolve: %v", err)
	}
```

- [ ] **Step 8: Run the tests**

Run: `cd notification-service && go test ./... -race`
Expected: PASS. This includes the existing claim suite, which now proves the ref survives the round trip through Redis.

- [ ] **Step 9: Commit**

```bash
git add notification-service/pkg/otp/
git commit -m "fix(otp): bind the verified session to the patient, not just the mobile

The claim already carried the HMS patient ID beside the mobile and dropped it
when minting the session, so an OTP issued for one family member could
authorize a capture naming another."
```

---

### Task 5: consent-service sends the patient to the verifier

**Files:**
- Modify: `consent-service/pkg/consent/service/session_client.go:19-24,42-53`
- Modify: `consent-service/pkg/consent/service/consent_service.go` (three `sessions.Verify` call sites)
- Test: `consent-service/pkg/consent/service/identity_test.go` (update `okSessions`)

**Interfaces:**
- Consumes: the wire format from Task 4 — `POST /internal/v1/otp/session/validate` now requires `hms_patient_id`.
- Produces: `SessionVerifier.Verify(ctx context.Context, sessionID, mobile, hmsPatientID string) error`.

- [ ] **Step 1: Write the failing test**

Append to `consent-service/pkg/consent/service/identity_test.go` — replace `okSessions` with a recording fake and assert the pairing is forwarded:

```go
type recordingSessions struct {
	gotSession string
	gotMobile  string
	gotHMS     string
}

func (r *recordingSessions) Verify(_ context.Context, sessionID, mobile, hmsPatientID string) error {
	r.gotSession, r.gotMobile, r.gotHMS = sessionID, mobile, hmsPatientID
	return nil
}

// The verifier must be told WHICH patient the capture is for, or the pairing of
// session to patient is enforced only by the client's good manners.
func TestCaptureForwardsPatientToSessionVerifier(t *testing.T) {
	repo := &fakeIdentityRepo{existing: map[string]*model.Consent{}}
	sessions := &recordingSessions{}
	svc := NewConsentService(repo, fakeSecrets{}, sessions)

	_, _, err := svc.Capture(context.Background(), "hosp-1", "1.2.3.4", &model.CaptureConsentRequest{
		Mobile:       familyMobile,
		SessionID:    "sess-son",
		Purposes:     []string{"treatment"},
		HMSPatientID: "PA-son",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sessions.gotHMS != "PA-son" || sessions.gotMobile != familyMobile {
		t.Fatalf("Verify got (%q,%q), want (%q,PA-son)", sessions.gotMobile, sessions.gotHMS, familyMobile)
	}
}
```

Delete the old `okSessions` type and replace its uses in the earlier tests with `&recordingSessions{}`.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd consent-service && go test ./pkg/consent/service/ -run TestCaptureForwardsPatient -v`
Expected: FAIL to compile — `*recordingSessions` does not implement `SessionVerifier` (wrong number of arguments).

- [ ] **Step 3: Widen the verifier interface**

In `consent-service/pkg/consent/service/session_client.go`:

```go
// SessionVerifier confirms that a session_id presented with a capture,
// withdrawal, or grant is a live OTP-verified session for the same mobile AND
// the same patient. The patient half matters because a family shares one
// mobile: without it, an OTP issued for one member authorizes a consent named
// for another.
type SessionVerifier interface {
	// Verify returns nil when the session is valid, ErrSessionNotVerified when
	// notification-service rejects it, and a wrapped error on transport failure
	// (fail closed — an unreachable verifier must never admit a consent).
	Verify(ctx context.Context, sessionID, mobile, hmsPatientID string) error
}
```

And the request struct plus implementation:

```go
type validateSessionRequest struct {
	SessionID    string `json:"session_id"`
	Mobile       string `json:"mobile"`
	HMSPatientID string `json:"hms_patient_id"`
}

func (v *httpSessionVerifier) Verify(ctx context.Context, sessionID, mobile, hmsPatientID string) error {
	token, err := v.tokens.Token(ctx)
	if err != nil {
		return fmt.Errorf("SessionVerifier: get service token: %w", err)
	}

	body, err := json.Marshal(validateSessionRequest{
		SessionID:    sessionID,
		Mobile:       mobile,
		HMSPatientID: hmsPatientID,
	})
```

Leave the rest of the method — including the fail-closed status handling — exactly as it is.

- [ ] **Step 4: Update the three call sites**

In `consent-service/pkg/consent/service/consent_service.go`, in `Capture`, `Withdraw`, and `Grant`:

```go
	if err := s.sessions.Verify(ctx, req.SessionID, req.Mobile, req.HMSPatientID); err != nil {
```

- [ ] **Step 5: Run the tests**

Run: `cd consent-service && go test ./... -race && go build ./...`
Expected: PASS, clean build.

- [ ] **Step 6: Commit**

```bash
git add consent-service/pkg/consent/service/
git commit -m "fix(consent): send the patient to the session verifier

Pairing the verified session with the patient being consented for was left to
the client; now the server checks it."
```

---

### Task 6: Migration and data wipe

**Files:**
- Create: `DPDP/scripts/db/migrations/0015_patient_identity.sql`

**Interfaces:**
- Consumes: nothing (schema only).
- Produces: `chk_consent_rows_have_hms_patient_id` on `consent.consent_vault`.

The migration deliberately does **not** truncate. A tracked migration that silently wipes an append-only legal-evidence table is dangerous on any database that later holds real consents. If rows violate the constraint, `ALTER` fails loudly — which is the correct failure, because it forces a human decision. The wipe is a separate, deliberate operator step below.

- [ ] **Step 1: Write the migration**

Create `DPDP/scripts/db/migrations/0015_patient_identity.sql`:

```sql
-- =============================================================================
-- 0015_patient_identity.sql
-- Identity on a consent row is the PAIR (patient_key, hms_patient_id).
--
-- patient_key is HMAC(mobile) — it identifies a CONTACT CHANNEL, not a person.
-- A family routinely shares one mobile number, so patient_key alone collapses
-- every member of a household onto one identity. hms_patient_id names the
-- individual.
--
-- See docs/superpowers/specs/2026-07-20-patient-identity-key-design.md
--
-- Key derivation is UNCHANGED, so no artifact_hash is invalidated and no row
-- needs rehashing.
--
-- This migration does not delete data. If it fails with a constraint violation,
-- the database holds consent rows with no hms_patient_id — decide deliberately
-- whether to backfill or truncate. Pre-pilot, truncate (see the plan).
-- =============================================================================

ALTER TABLE consent.consent_vault
  ADD CONSTRAINT chk_consent_rows_have_hms_patient_id
  CHECK (type = 'EMERGENCY_OVERRIDE' OR hms_patient_id IS NOT NULL);

COMMENT ON CONSTRAINT chk_consent_rows_have_hms_patient_id ON consent.consent_vault IS
  'Consent rows must name a patient. Only EMERGENCY_OVERRIDE is exempt: an '
  'unconscious patient may have neither mobile nor HMS ID.';

COMMENT ON COLUMN consent.consent_vault.patient_key IS
  'HMAC of the mobile ("v1|<hex>"), hospital-scoped. A CONTACT CHANNEL, not an '
  'identity — families share one number. Identity is (patient_key, hms_patient_id).';

COMMENT ON COLUMN consent.consent_vault.hms_patient_id IS
  'Opaque HMS patient ID e.g. "PA-00234". Names the individual. Required on all '
  'consent rows; null only on EMERGENCY_OVERRIDE.';

COMMENT ON TABLE consent.consent_vault IS
  'Immutable consent artifact store. Append-only enforced by trigger + RLS. '
  'Raw patient mobile is never stored — only HMAC_SHA256(mobile+SYSTEM_SALT+hospital_key). '
  'A patient is identified by (patient_key, hms_patient_id), never patient_key alone.';
```

- [ ] **Step 2: Correct the stale header comment**

In `DPDP/scripts/db/migrations/0003_consent_vault.sql`, line 7 currently reads:

```
--   - patient_key = HMAC_SHA256(mobile + SYSTEM_SALT + hospital_key) — raw mobile NEVER stored
```

Append the correction on the following line, leaving the original text intact (it is an applied migration; the file is a historical record):

```
--     NOTE (0015): patient_key identifies a CONTACT CHANNEL, not a patient.
--     Families share a mobile. Identity is (patient_key, hms_patient_id).
```

- [ ] **Step 3: Wipe the disposable consent data**

Confirmed with the product owner: no existing consent data needs to be kept. Run against the local stack as the admin role, before applying the migration:

```bash
cd /home/reddy/Documents/Go/DPDP/DPDP
docker compose exec -T postgres psql -U "$POSTGRES_ADMIN_USER" -d "$POSTGRES_DB" \
  -c "TRUNCATE consent.consent_vault;"
```

The append-only triggers do not block this — they are `FOR EACH ROW BEFORE UPDATE/DELETE`, and `TRUNCATE` does not fire them. `audit_log` and the outbox tables gain no constraint and are left alone; OTP sessions expire in minutes on their own.

If `$POSTGRES_ADMIN_USER` is not set in your environment, read the admin credentials from `DPDP/docker-compose.yml` — the migration container uses the same ones.

- [ ] **Step 4: Apply the migration**

```bash
cd /home/reddy/Documents/Go/DPDP/DPDP && ./scripts/db/migrate.sh up
```

Expected: `0015_patient_identity` applied, then `status` shows no pending migrations.

- [ ] **Step 5: Verify the constraint bites**

```bash
docker compose exec -T postgres psql -U "$POSTGRES_ADMIN_USER" -d "$POSTGRES_DB" -c \
  "INSERT INTO consent.consent_vault (hospital_id, patient_key, type, status, artifact_hash)
   SELECT id, 'v1|deadbeef', 'CONSENT_GIVEN', 'ACTIVE', 'x' FROM auth.hospitals LIMIT 1;"
```

Expected: `ERROR: new row for relation "consent_vault" violates check constraint "chk_consent_rows_have_hms_patient_id"`.

Then confirm the emergency exemption still admits a row:

```bash
docker compose exec -T postgres psql -U "$POSTGRES_ADMIN_USER" -d "$POSTGRES_DB" -c \
  "INSERT INTO consent.consent_vault (hospital_id, patient_key, type, status, artifact_hash)
   SELECT id, NULL, 'EMERGENCY_OVERRIDE', 'ACTIVE', 'x' FROM auth.hospitals LIMIT 1;"
```

Expected: `INSERT 0 1`. Leave the row — the vault is append-only and this is a valid unknown-identity emergency record.

- [ ] **Step 6: Commit**

```bash
git add DPDP/scripts/db/migrations/
git commit -m "feat(db): require hms_patient_id on consent rows

Identity is (patient_key, hms_patient_id). Only EMERGENCY_OVERRIDE is exempt,
where an unconscious patient may have neither mobile nor HMS ID."
```

---

### Task 7: End-to-end verification

**Files:**
- Modify: none expected. If the kiosk needs a change, this task has found a real regression.

**Interfaces:**
- Consumes: everything from Tasks 1-6.
- Produces: evidence the change is complete and the client contract held.

The kiosk already sends `hms_patient_id` on capture (`kiosk-bff/pkg/handlers/claim.go:113`, taken from `claim.Ref`), so the live-stack fixture should pass untouched. That is the point of running it: it is the evidence, not a formality.

- [ ] **Step 1: Build every service in the workspace**

```bash
cd /home/reddy/Documents/Go/DPDP
for s in consent-service notification-service kiosk-bff admin-bff integration-service emergency-service audit-service auth-service gateway; do
  (cd "$s" && go build ./...) || echo "BUILD FAILED: $s"
done
```

Expected: no `BUILD FAILED` lines. emergency-service must build untouched — it still calls `ComputePatientKey` with the original signature.

- [ ] **Step 2: Run every service's unit tests**

```bash
cd /home/reddy/Documents/Go/DPDP
for s in consent-service notification-service kiosk-bff admin-bff integration-service emergency-service audit-service auth-service; do
  (cd "$s" && go test ./... -race) || echo "TESTS FAILED: $s"
done
```

Expected: no `TESTS FAILED` lines.

- [ ] **Step 3: Run the consent-service integration suite**

```bash
cd /home/reddy/Documents/Go/DPDP/consent-service && go test ./test/... -tags=integration -race -v
```

Expected: PASS. This exercises the re-scoped query against real Postgres with RLS. If `tenant_isolation_test.go` calls the renamed repository method, fix the call — the rename is intentional and the test must follow it.

- [ ] **Step 4: Run the kiosk live-stack fixture**

```bash
cd /home/reddy/Documents/Go/DPDP/frontend/kiosk && npm test
```

Expected: PASS, unchanged. This is the evidence that the capture contract did not break for the real client.

- [ ] **Step 5: Walk the family case through the running stack**

Bring the stack up per `RUN_LOCAL.md`, then:

1. Stage two registrations via the webhook with the **same** `phoneNumber` and **different** `patientId` (`PA-mother`, `PA-son`).
2. Send a code for `PA-mother` from the reception queue, complete consent at the kiosk.
3. Send a code for `PA-son`, complete consent at the kiosk.

Expected: the son's capture returns 201. Before this change it returned 409 with "you have already given consent". This is the bug, reproduced and fixed, against real services.

- [ ] **Step 6: Update the plan-phase log**

In `plan-phase.md`, mark the returning-patient row as partly explained by this fix — the family-mobile 409 and the returning-patient 409 present identically to reception. Do not mark the reception-queue item done: this change does not add the queue notice.

- [ ] **Step 7: Commit**

```bash
git add plan-phase.md
git commit -m "docs(plan): record the patient identity fix"
```

---

### Task 8: Re-key the reception queue's consent lookup — ON THE QUEUE BRANCH, AFTER THIS MERGES

**This task does not run on `fix/patient-identity-key`.** The code it changes lives on the unmerged branch `feat/returning-patient-queue` (11 commits ahead of main, 19 behind as of 2026-07-20). Sequence:

1. Land Tasks 1-7 and merge `fix/patient-identity-key` to main.
2. Rebase `feat/returning-patient-queue` onto main.
3. Do this task there, then merge it.

**Why this is a blocker, not a nicety.** That branch answers "has this patient already consented?" with `ActiveMobiles(ctx, hospitalID, mobiles []string)`, keyed by `patientKeyFor(mobile)` — a household lookup. Merged as-is, a son whose mother consented is badged "Already consented — no action" **and his Send code button is disabled**. Reception never sends him a code, nothing errors, and he is silently denied consent capture. That is strictly worse than the wasted-walk dead-end the branch was built to fix, and this identity change is what makes it reachable: capture would finally accept him at the same moment the queue stops offering him a code.

**Files (paths as they exist on that branch):**
- Modify: `consent-service/pkg/consent/repository/` — the `ActivePatientKeys` query and its interface entry
- Modify: `consent-service/pkg/consent/service/consent_service.go` — `ActiveMobiles`
- Modify: `consent-service/pkg/consent/controller/` + routes — the `/api/v1/consent/active` request model
- Modify: `integration-service/pkg/pending/consent/client.go` — the batch sender
- Test: the existing suites on that branch for each of the above

**The change:** `ActiveMobiles(ctx, hospitalID, mobiles []string) ([]string, error)` becomes `ActiveHMSPatientIDs(ctx, hospitalID, hmsPatientIDs []string) ([]string, error)`, backed by a query over `hms_patient_id` + `AnyActive()` rather than `patient_key`. The staged registration already carries `HMSPatientID` alongside `Mobile`, so integration-service sends the field it already has — and stops sending raw mobiles to consent-service on every poll, which is a privacy improvement in its own right.

Everything else on that branch carries over untouched: the no-audit-write property, the 200-per-chunk cap, the dedupe-and-chunk sender, the whole-batch fail-open, and the 3s `lookupTimeout` bounding the entire chunked lookup.

- [ ] **Step 1: Re-verify this task against that branch before starting.** It was written from the branch's ledger, not from its code. Read `ActiveMobiles` and the client, confirm the shapes above, and correct this task where it drifted.
- [ ] **Step 2: Write the failing test** — a batch containing two HMS IDs that share one mobile, where only one has consented, must return exactly that one.
- [ ] **Step 3: Re-key the repository query, service method, endpoint model, and sender.**
- [ ] **Step 4: Run both services' suites, including the integration-tagged ones.**
- [ ] **Step 5: Verify on the live stack** — stage two registrations sharing a mobile with different `patientId`, consent as one, and confirm the queue badges only that one.
- [ ] **Step 6: Commit.**

---

## Already built, do not rebuild

The **returning-patient reception-queue notice** is implemented and final-review-clean on `feat/returning-patient-queue` (plan `docs/superpowers/plans/2026-07-16-returning-patient-queue.md` and spec `docs/superpowers/specs/2026-07-16-returning-patient-queue-design.md`, both on that branch). It flags consented rows in integration-service's `List`, and the dashboard renders the badge with a 15-second auto-hide.

It was independently re-designed during the 2026-07-20 brainstorm that produced this plan, because the branch was never checked. The designs agree on nearly everything; where they differ, **the branch is the implementation of record**. Its only defect is the mobile keying, which is Task 8.
