# Front-desk consent flow — B1 (backend claim plumbing) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the backend of the front-desk-driven consent flow — reception fires a one-time code to a staged patient, and a kiosk resolves that code (only) into a verified OTP session — with no UI yet, fully API-testable end to end.

**Architecture:** notification-service gains a **hospital-scoped OTP claim**: the reception-fired send tags the OTP with the hospital + an opaque `ref` (the `hms_patient_id`) and records its `reference_id` in a per-hospital set; a resolve endpoint hash-compares an entered code against that hospital's small active set and, on the single match, runs the normal verify path. integration-service (Spec A) gains a `status` field + a set-status endpoint. admin-bff gains a reception role and a `send-code` orchestration. All new cross-service endpoints are on `/internal` (edge-blocked) and authed by the **hospital JWT** the BFFs already hold.

**Tech Stack:** Go 1.25, gin, `github.com/redis/go-redis/v9`, `shared/crypto` (`GenerateOTP`/`HashOTP`/`VerifyOTP`), `shared/middleware` (`JWTAuth`, `CtxHospitalID`), `shared/hospitaljwt` (admin-bff's token provider), miniredis for hermetic store tests.

## Global Constraints

- **Auth refinement (deviates from the spec, adopted for consistency with Spec A):** the new claim endpoints (`notification /internal/v1/otp/claim/*`) and the integration status endpoint are **hospital-JWT-gated** (`middleware.JWTAuth`), `hospital_id` read from the token claim (`middleware.CtxHospitalID`), never from the request body. They live under `/internal` so the gateway blocks them from the public edge; the BFFs reach them server-side with their existing hospital JWT. No new service-token clients.
- **OTP secrecy:** the OTP is only ever stored **hashed** (`sharedcrypto.HashOTP`); never store or log the raw code. Resolution is a hashed compare (`sharedcrypto.VerifyOTP`) over the hospital's active claim set.
- **notification stays HMS-agnostic:** `ref` is an opaque string it stores and returns; it never interprets it.
- **Claim index = one Redis key:** `claimset:{hospital_id}` (a set of active `reference_id`s), TTL = OTP expiry. The `ref` is folded into the existing `otp:{reference_id}` record value (`hash|mobile|ref`), not a separate key.
- **One code = the OTP.** Uniqueness-on-send: regenerate until the new code does not collide with any active claim in the hospital's set. A resend just fires a fresh code; the old claim expires on its own OTP TTL (no supersede bookkeeping).
- **Brute-force guard:** a per-hospital resolve-attempt cap (the per-`reference_id` verify cap does not cover code-only resolve).
- **Reception is a least-privilege role**, enforced at admin-bff (not only the SPA): reception can reach only the reception endpoints; admin/dpo endpoints reject reception.
- **Status values:** `PENDING` (webhook default) → `CODE_SENT` (admin-bff on send) → `DONE` (kiosk-bff after capture — B2). Setting status preserves the record's remaining Redis TTL.
- **PII:** mobile masked in any list/log; never in a URL. The `send-code` orchestration reads the mobile server-side and never returns it to the browser.
- Existing OTP guards unchanged: `otpExpiry = 3m`, `maxVerifyAttempts = 5`, `sendCooldown = 60s`, `maxSendsPerHour = 5`.

---

### Task 1: notification-service — claim store methods

Add the Redis operations the claim mechanic needs, alongside the existing `redisOTPStore`. The existing `otp:{refID}` value becomes `hash|mobile|ref` (ref optional); `GetOTPHash` must tolerate the extra field so the walk-in path is unaffected.

**Files:**
- Modify: `notification-service/pkg/otp/repository/redis_store.go` (holds `redisOTPStore` with `SaveOTPHash`/`GetOTPHash`/etc.)
- Modify: `notification-service/pkg/otp/repository/interface.go` (the `OTPStore` interface)
- Test: `notification-service/pkg/otp/repository/claim_store_test.go` (new)

**Interfaces:**
- Produces (added to `OTPStore`):
  - `SaveClaimOTP(ctx context.Context, refID, hash, mobile, ref, hospitalID string, ttl time.Duration) error`
  - `GetClaimOTP(ctx context.Context, refID string) (hash, mobile, ref string, err error)`
  - `ClaimMembers(ctx context.Context, hospitalID string) ([]string, error)`
  - `RemoveClaim(ctx context.Context, hospitalID, refID string) error`
  - `IncrResolveAttempts(ctx context.Context, hospitalID string, ttl time.Duration) (int64, error)`
- `GetOTPHash` unchanged signature, now tolerant of a 3-field value.

- [ ] **Step 1: Write the failing store test**

`notification-service/pkg/otp/repository/claim_store_test.go`:
```go
package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newClaimStore(t *testing.T) (OTPStore, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	return NewRedisStore(redis.NewClient(&redis.Options{Addr: mr.Addr()})), mr
}

func TestSaveAndGetClaimOTP(t *testing.T) {
	s, _ := newClaimStore(t)
	ctx := context.Background()
	if err := s.SaveClaimOTP(ctx, "ref-1", "HASH", "9876543210", "PA-1", "hosp-1", time.Minute); err != nil {
		t.Fatalf("save: %v", err)
	}
	hash, mobile, ref, err := s.GetClaimOTP(ctx, "ref-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if hash != "HASH" || mobile != "9876543210" || ref != "PA-1" {
		t.Fatalf("got (%q,%q,%q)", hash, mobile, ref)
	}
	// GetOTPHash must still work on the 3-field value (walk-in path).
	h, m, err := s.GetOTPHash(ctx, "ref-1")
	if err != nil || h != "HASH" || m != "9876543210" {
		t.Fatalf("GetOTPHash tolerance: (%q,%q,%v)", h, m, err)
	}
}

func TestClaimMembersAndRemove(t *testing.T) {
	s, _ := newClaimStore(t)
	ctx := context.Background()
	_ = s.SaveClaimOTP(ctx, "ref-1", "H1", "9000000001", "PA-1", "hosp-1", time.Minute)
	_ = s.SaveClaimOTP(ctx, "ref-2", "H2", "9000000002", "PA-2", "hosp-1", time.Minute)
	_ = s.SaveClaimOTP(ctx, "ref-9", "H9", "9000000009", "PA-9", "hosp-2", time.Minute)

	members, err := s.ClaimMembers(ctx, "hosp-1")
	if err != nil || len(members) != 2 {
		t.Fatalf("members hosp-1 = %v (err %v), want 2", members, err)
	}
	if err := s.RemoveClaim(ctx, "hosp-1", "ref-1"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	members, _ = s.ClaimMembers(ctx, "hosp-1")
	if len(members) != 1 || members[0] != "ref-2" {
		t.Fatalf("after remove = %v, want [ref-2]", members)
	}
}

func TestIncrResolveAttempts(t *testing.T) {
	s, _ := newClaimStore(t)
	ctx := context.Background()
	n, err := s.IncrResolveAttempts(ctx, "hosp-1", time.Minute)
	if err != nil || n != 1 {
		t.Fatalf("first = %d (err %v), want 1", n, err)
	}
	n, _ = s.IncrResolveAttempts(ctx, "hosp-1", time.Minute)
	if n != 2 {
		t.Fatalf("second = %d, want 2", n)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd notification-service && go test ./pkg/otp/repository/ -run 'Claim|ResolveAttempts' -v`
Expected: FAIL — `SaveClaimOTP` etc. undefined.

- [ ] **Step 3: Add the methods to the `OTPStore` interface**

In `notification-service/pkg/otp/repository/interface.go`, add to the `OTPStore` interface (leave existing methods untouched):
```go
	// SaveClaimOTP stores an OTP the same way SaveOTPHash does, plus an opaque
	// ref, and records refID in the hospital's active claim set.
	SaveClaimOTP(ctx context.Context, refID, hash, mobile, ref, hospitalID string, ttl time.Duration) error
	// GetClaimOTP returns the hash, mobile, and opaque ref for a claim OTP.
	GetClaimOTP(ctx context.Context, refID string) (hash, mobile, ref string, err error)
	// ClaimMembers returns the reference IDs currently claimed for a hospital.
	ClaimMembers(ctx context.Context, hospitalID string) ([]string, error)
	// RemoveClaim drops one reference ID from the hospital's claim set.
	RemoveClaim(ctx context.Context, hospitalID, refID string) error
	// IncrResolveAttempts counts code-resolve attempts per hospital (brute-force guard).
	IncrResolveAttempts(ctx context.Context, hospitalID string, ttl time.Duration) (int64, error)
```

- [ ] **Step 4: Implement the store methods and make `GetOTPHash` tolerant**

In `redis_store.go`, first make `GetOTPHash` tolerant: line ~66 currently reads
`parts := strings.SplitN(val, "|", 2)` with an `if len(parts) != 2` error guard — change
those two to `parts := strings.SplitN(val, "|", 3)` and `if len(parts) < 2`, still
returning `parts[0], parts[1]`. Then add:
```go
func claimSetKey(hospitalID string) string  { return fmt.Sprintf("claimset:%s", hospitalID) }
func resolveAttemptsKey(h string) string     { return fmt.Sprintf("resolve_attempts:%s", h) }

func (s *redisOTPStore) SaveClaimOTP(ctx context.Context, refID, hash, mobile, ref, hospitalID string, ttl time.Duration) error {
	// otp:{refID} = hash|mobile|ref  (same key the verify path reads)
	if err := s.client.Set(ctx, fmt.Sprintf("otp:%s", refID), fmt.Sprintf("%s|%s|%s", hash, mobile, ref), ttl).Err(); err != nil {
		return fmt.Errorf("repository.SaveClaimOTP: set otp: %w", err)
	}
	setKey := claimSetKey(hospitalID)
	if err := s.client.SAdd(ctx, setKey, refID).Err(); err != nil {
		return fmt.Errorf("repository.SaveClaimOTP: sadd: %w", err)
	}
	// Refresh the set TTL so it never outlives the OTPs it points at by much.
	if err := s.client.Expire(ctx, setKey, ttl).Err(); err != nil {
		return fmt.Errorf("repository.SaveClaimOTP: expire set: %w", err)
	}
	return nil
}

func (s *redisOTPStore) GetClaimOTP(ctx context.Context, refID string) (string, string, string, error) {
	val, err := s.client.Get(ctx, fmt.Sprintf("otp:%s", refID)).Result()
	if err == redis.Nil {
		return "", "", "", nil // expired between SMEMBERS and here
	}
	if err != nil {
		return "", "", "", fmt.Errorf("repository.GetClaimOTP: get: %w", err)
	}
	parts := strings.SplitN(val, "|", 3)
	if len(parts) < 2 {
		return "", "", "", fmt.Errorf("repository.GetClaimOTP: bad value")
	}
	ref := ""
	if len(parts) == 3 {
		ref = parts[2]
	}
	return parts[0], parts[1], ref, nil
}

func (s *redisOTPStore) ClaimMembers(ctx context.Context, hospitalID string) ([]string, error) {
	members, err := s.client.SMembers(ctx, claimSetKey(hospitalID)).Result()
	if err != nil {
		return nil, fmt.Errorf("repository.ClaimMembers: %w", err)
	}
	return members, nil
}

func (s *redisOTPStore) RemoveClaim(ctx context.Context, hospitalID, refID string) error {
	return s.client.SRem(ctx, claimSetKey(hospitalID), refID).Err()
}

func (s *redisOTPStore) IncrResolveAttempts(ctx context.Context, hospitalID string, ttl time.Duration) (int64, error) {
	key := resolveAttemptsKey(hospitalID)
	n, err := s.client.Incr(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("repository.IncrResolveAttempts: %w", err)
	}
	if n == 1 {
		if err := s.client.Expire(ctx, key, ttl).Err(); err != nil {
			return 0, fmt.Errorf("repository.IncrResolveAttempts: expire: %w", err)
		}
	}
	return n, nil
}
```
Ensure `strings` is imported in the store file.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd notification-service && go test ./pkg/otp/repository/ -v`
Expected: PASS (new claim tests + any existing repository tests).

- [ ] **Step 6: Commit**

```bash
git add notification-service/pkg/otp/repository/
git commit -m "feat(notification): claim store — hospital claim set, ref on OTP record, resolve-attempt counter"
```

---

### Task 2: notification-service — claim service + internal endpoints

Add `SendClaim` / `ResolveClaim` to the service (reusing the existing send guards + crypto), a controller, and two hospital-JWT-gated `/internal` routes. Wire the handler in `main.go`.

**Files:**
- Modify: `notification-service/pkg/otp/service/otp_service.go` (interface + methods + constants)
- Create: `notification-service/pkg/otp/model/claim.go` (result type + request bodies)
- Modify: `notification-service/pkg/otp/controller/otp_handler.go` (two handlers)
- Modify: `notification-service/pkg/routes/routes.go` (routes)
- Modify: `notification-service/cmd/server/main.go` (pass pubKey to the claim routes — already loaded)
- Test: `notification-service/pkg/otp/service/claim_service_test.go`

**Interfaces:**
- Consumes: Task 1 store methods; `sharedcrypto.GenerateOTP/HashOTP/VerifyOTP`.
- Produces:
  - `SendClaim(ctx, hospitalID, mobile, ref string) (*model.SendOTPResponse, error)`
  - `ResolveClaim(ctx, hospitalID, otp string) (*model.ClaimResolveResult, error)` where `ClaimResolveResult{SessionID, Mobile, Ref string}`
  - Routes: `POST /internal/v1/otp/claim/send` `{mobile, ref}`, `POST /internal/v1/otp/claim/resolve` `{otp}` — both under `middleware.JWTAuth`, `hospital_id` from the token.

- [ ] **Step 1: Add the model types**

`notification-service/pkg/otp/model/claim.go`:
```go
package model

// ClaimSendRequest is the body for POST /internal/v1/otp/claim/send.
// hospital_id is taken from the JWT, not the body.
type ClaimSendRequest struct {
	Mobile string `json:"mobile" binding:"required,len=10"`
	Ref    string `json:"ref" binding:"required"`
}

// ClaimResolveRequest is the body for POST /internal/v1/otp/claim/resolve.
type ClaimResolveRequest struct {
	OTP string `json:"otp" binding:"required,len=6"`
}

// ClaimResolveResult is returned to a trusted internal caller (kiosk-bff).
type ClaimResolveResult struct {
	SessionID string `json:"session_id"`
	Mobile    string `json:"mobile"`
	Ref       string `json:"ref"`
}
```

- [ ] **Step 2: Write the failing service test**

`notification-service/pkg/otp/service/claim_service_test.go`:
```go
package service

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/hiabhi-cpu/notification-service/pkg/otp/repository"
)

// captureSMS records the OTP the service sent, so the test can resolve it.
type captureSMS struct{ last string }

func (c *captureSMS) SendOTP(_ context.Context, _ string, otp string) error { c.last = otp; return nil }

func newClaimService(t *testing.T) (OTPService, *captureSMS) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	store := repository.NewRedisStore(redis.NewClient(&redis.Options{Addr: mr.Addr()}))
	sms := &captureSMS{}
	return NewOTPService(store, sms), sms
}

func TestSendClaimThenResolve(t *testing.T) {
	svc, sms := newClaimService(t)
	ctx := context.Background()

	if _, err := svc.SendClaim(ctx, "hosp-1", "9876543210", "PA-1"); err != nil {
		t.Fatalf("SendClaim: %v", err)
	}
	// The patient enters the exact code that was texted.
	res, err := svc.ResolveClaim(ctx, "hosp-1", sms.last)
	if err != nil {
		t.Fatalf("ResolveClaim: %v", err)
	}
	if res.Mobile != "9876543210" || res.Ref != "PA-1" || res.SessionID == "" {
		t.Fatalf("resolve result = %+v", res)
	}
	// The resolved session validates for that mobile (capture would accept it).
	if err := svc.ValidateSession(ctx, res.SessionID, "9876543210"); err != nil {
		t.Fatalf("ValidateSession after resolve: %v", err)
	}
}

func TestResolveWrongCodeFails(t *testing.T) {
	svc, sms := newClaimService(t)
	ctx := context.Background()
	_, _ = svc.SendClaim(ctx, "hosp-1", "9876543210", "PA-1")
	wrong := "000000"
	if wrong == sms.last {
		wrong = "111111"
	}
	if _, err := svc.ResolveClaim(ctx, "hosp-1", wrong); err == nil {
		t.Fatal("expected error for wrong code")
	}
}

func TestResolveIsHospitalScoped(t *testing.T) {
	svc, sms := newClaimService(t)
	ctx := context.Background()
	_, _ = svc.SendClaim(ctx, "hosp-1", "9876543210", "PA-1")
	// A different hospital must not resolve hosp-1's code.
	if _, err := svc.ResolveClaim(ctx, "hosp-2", sms.last); err == nil {
		t.Fatal("expected hosp-2 not to resolve hosp-1's code")
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `cd notification-service && go test ./pkg/otp/service/ -run Claim -v`
Expected: FAIL — `SendClaim`/`ResolveClaim` undefined.

- [ ] **Step 4: Implement the service methods**

In `notification-service/pkg/otp/service/otp_service.go`, add to the `OTPService` interface:
```go
	SendClaim(ctx context.Context, hospitalID, mobile, ref string) (*model.SendOTPResponse, error)
	ResolveClaim(ctx context.Context, hospitalID, otp string) (*model.ClaimResolveResult, error)
```
Add a constant near the others:
```go
	// ponytail: per-hospital code-resolve cap over the OTP window. Generous enough
	// for legit concurrent patients at pilot scale, low enough to throttle code
	// brute-forcing; raise per-hospital if a busy site trips it.
	maxResolveAttempts = 50
```
Add the methods:
```go
func (s *otpService) SendClaim(ctx context.Context, hospitalID, mobile, ref string) (*model.SendOTPResponse, error) {
	// Same abuse guards as the walk-in send.
	sends, err := s.repo.IncrHourlySends(ctx, mobile)
	if err != nil {
		return nil, fmt.Errorf("service.SendClaim: %w", err)
	}
	if sends > maxSendsPerHour {
		return nil, ErrTooManyRequests
	}
	ok, err := s.repo.AcquireSendCooldown(ctx, mobile, sendCooldown)
	if err != nil {
		return nil, fmt.Errorf("service.SendClaim: %w", err)
	}
	if !ok {
		return nil, ErrTooManyRequests
	}

	// Generate a code unique within the hospital's active claim set, so an
	// entered code maps to at most one record.
	members, err := s.repo.ClaimMembers(ctx, hospitalID)
	if err != nil {
		return nil, fmt.Errorf("service.SendClaim: %w", err)
	}
	var otp, hash string
	for tries := 0; ; tries++ {
		if tries > 20 {
			return nil, fmt.Errorf("service.SendClaim: could not generate a unique code")
		}
		otp, err = sharedcrypto.GenerateOTP()
		if err != nil {
			return nil, fmt.Errorf("service.SendClaim: generate: %w", err)
		}
		if !s.codeCollides(ctx, members, otp) {
			break
		}
	}
	hash, err = sharedcrypto.HashOTP(otp)
	if err != nil {
		return nil, fmt.Errorf("service.SendClaim: hash: %w", err)
	}
	refID := uuid.New().String()
	if err := s.repo.SaveClaimOTP(ctx, refID, hash, mobile, ref, hospitalID, otpExpiry); err != nil {
		return nil, fmt.Errorf("service.SendClaim: save: %w", err)
	}
	if err := s.smsClient.SendOTP(ctx, mobile, otp); err != nil {
		return nil, fmt.Errorf("service.SendClaim: sms: %w", err)
	}
	return &model.SendOTPResponse{ReferenceID: refID, ExpiresAt: time.Now().Add(otpExpiry)}, nil
}

// codeCollides reports whether otp matches any active claim's hash.
func (s *otpService) codeCollides(ctx context.Context, members []string, otp string) bool {
	for _, refID := range members {
		hash, _, _, err := s.repo.GetClaimOTP(ctx, refID)
		if err != nil || hash == "" {
			continue
		}
		if sharedcrypto.VerifyOTP(otp, hash) {
			return true
		}
	}
	return false
}

func (s *otpService) ResolveClaim(ctx context.Context, hospitalID, otp string) (*model.ClaimResolveResult, error) {
	attempts, err := s.repo.IncrResolveAttempts(ctx, hospitalID, otpExpiry)
	if err != nil {
		return nil, fmt.Errorf("service.ResolveClaim: %w", err)
	}
	if attempts > maxResolveAttempts {
		return nil, ErrTooManyRequests
	}
	members, err := s.repo.ClaimMembers(ctx, hospitalID)
	if err != nil {
		return nil, fmt.Errorf("service.ResolveClaim: %w", err)
	}
	for _, refID := range members {
		hash, mobile, ref, err := s.repo.GetClaimOTP(ctx, refID)
		if err != nil {
			return nil, fmt.Errorf("service.ResolveClaim: %w", err)
		}
		if hash == "" { // expired; drop from the set
			_ = s.repo.RemoveClaim(ctx, hospitalID, refID)
			continue
		}
		if !sharedcrypto.VerifyOTP(otp, hash) {
			continue
		}
		// Match. Burn the OTP + claim, mint a verified session (same as Verify).
		_ = s.repo.DeleteOTP(ctx, refID)
		_ = s.repo.RemoveClaim(ctx, hospitalID, refID)
		sessionID := uuid.New().String()
		state := model.SessionState{Mobile: mobile, Verified: true, ExpiresAt: time.Now().Add(sessionExpiry)}
		if err := s.repo.SaveSession(ctx, sessionID, state, sessionExpiry); err != nil {
			return nil, fmt.Errorf("service.ResolveClaim: save session: %w", err)
		}
		return &model.ClaimResolveResult{SessionID: sessionID, Mobile: mobile, Ref: ref}, nil
	}
	return nil, ErrInvalidOTP
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd notification-service && go test ./pkg/otp/service/ -v`
Expected: PASS (claim tests + existing service tests).

- [ ] **Step 6: Add the controller handlers**

In `notification-service/pkg/otp/controller/otp_handler.go`, add:
```go
// ClaimSend handles POST /internal/v1/otp/claim/send (hospital-JWT). Reception
// fires a code to a staged patient; hospital_id comes from the token.
func (h *OTPHandler) ClaimSend(c *gin.Context) {
	hospitalID := c.GetString(middleware.CtxHospitalID)
	if hospitalID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing hospital identity"})
		return
	}
	var req model.ClaimSendRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "mobile and ref are required"})
		return
	}
	resp, err := h.svc.SendClaim(c.Request.Context(), hospitalID, req.Mobile, req.Ref)
	if err != nil {
		if errors.Is(err, service.ErrTooManyRequests) {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many requests — try again shortly"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to send code"})
		return
	}
	c.JSON(http.StatusOK, resp)
}

// ClaimResolve handles POST /internal/v1/otp/claim/resolve (hospital-JWT). The
// kiosk submits only the code; returns a verified session + the opaque ref.
func (h *OTPHandler) ClaimResolve(c *gin.Context) {
	hospitalID := c.GetString(middleware.CtxHospitalID)
	if hospitalID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing hospital identity"})
		return
	}
	var req model.ClaimResolveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "otp is required"})
		return
	}
	res, err := h.svc.ResolveClaim(c.Request.Context(), hospitalID, req.OTP)
	if err != nil {
		if errors.Is(err, service.ErrTooManyRequests) {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many attempts — try again shortly"})
			return
		}
		// Generic — no enumeration signal.
		c.JSON(http.StatusUnauthorized, gin.H{"error": "code not recognized"})
		return
	}
	c.JSON(http.StatusOK, res)
}
```
Confirm `middleware` (`github.com/hiabhi-cpu/shared/middleware`) is imported in the handler file (add it if not).

- [ ] **Step 7: Register the routes (hospital-JWT on /internal)**

In `notification-service/pkg/routes/routes.go`, inside `Setup`, add a claim group under `/internal` gated by `JWTAuth` (NOT `InternalServiceAuth` — these are hospital-scoped):
```go
	claim := r.Group("/internal/v1/otp/claim")
	claim.Use(middleware.JWTAuth(pubKey))
	{
		claim.POST("/send", otpHandler.ClaimSend)
		claim.POST("/resolve", otpHandler.ClaimResolve)
	}
```
`main.go` already loads `pubKey` and passes it to `routes.Setup`; no main.go change needed if `Setup`'s signature already receives `pubKey` (it does).

- [ ] **Step 8: Build + full notification suite**

Run: `cd notification-service && go build ./... && go test ./...`
Expected: build clean; all packages pass.

- [ ] **Step 9: Commit**

```bash
git add notification-service/pkg/otp/ notification-service/pkg/routes/
git commit -m "feat(notification): OTP claim send/resolve (hospital-scoped, hashed match) + internal routes"
```

---

### Task 3: integration-service — status field + set-status endpoint

Extend Spec A's pending record with `status` (default `PENDING` on the webhook) and add a hospital-JWT set-status endpoint that preserves the record's TTL.

**Files:**
- Modify: `integration-service/pkg/pending/model/pending.go` (add `Status`)
- Modify: `integration-service/pkg/pending/adapter/bahmni.go` (set `Status: "PENDING"`)
- Modify: `integration-service/pkg/pending/repository/store.go` (`SetStatus` + `ErrNotFound`)
- Modify: `integration-service/pkg/pending/controller/deps.go` (add `SetStatus` to `PendingStore`)
- Modify: `integration-service/pkg/pending/controller/read.go` (add `SetStatus` handler)
- Modify: `integration-service/pkg/routes/routes.go` (add the route)
- Test: `integration-service/pkg/pending/repository/store_test.go` (add status cases), `integration-service/pkg/pending/controller/read_test.go` (add status handler cases)

**Interfaces:**
- Produces: `model.PendingRegistration.Status string`; `repository.ErrNotFound`; `(*RedisStore).SetStatus(ctx, hospitalID, hmsPatientID, status string) error`; `PendingStore.SetStatus(...)`; route `POST /internal/v1/registrations/:hms_patient_id/status` `{status}`.

- [ ] **Step 1: Add `Status` to the model and default it on the webhook**

In `model/pending.go`, add to the struct (after `DOB`):
```go
	Status       string `json:"status"`         // PENDING → CODE_SENT → DONE
```
In `adapter/bahmni.go`, in the returned `model.PendingRegistration{...}`, add:
```go
		Status:       "PENDING",
```

- [ ] **Step 2: Write the failing store test**

Add to `integration-service/pkg/pending/repository/store_test.go`:
```go
func TestSetStatusPreservesRecord(t *testing.T) {
	s := mustStore(t)
	ctx := context.Background()
	_ = s.Upsert(ctx, sampleReg("hosp-1", "PA-1"))
	if err := s.SetStatus(ctx, "hosp-1", "PA-1", "CODE_SENT"); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	got, _ := s.Get(ctx, "hosp-1", "PA-1")
	if got == nil || got.Status != "CODE_SENT" {
		t.Fatalf("status = %+v, want CODE_SENT", got)
	}
	if got.Mobile != "9876543210" {
		t.Fatalf("SetStatus clobbered the record: %+v", got)
	}
}

func TestSetStatusUnknownReturnsErrNotFound(t *testing.T) {
	s := mustStore(t)
	if err := s.SetStatus(context.Background(), "hosp-1", "nope", "DONE"); err != ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `cd integration-service && go test ./pkg/pending/repository/ -run SetStatus -v`
Expected: FAIL — `SetStatus`/`ErrNotFound` undefined.

- [ ] **Step 4: Implement `SetStatus` + `ErrNotFound`**

In `repository/store.go`, add near the top (after imports):
```go
// ErrNotFound is returned when a status update targets a missing/expired record.
var ErrNotFound = fmt.Errorf("pending registration not found")
```
And the method (reads remaining TTL so the update does not reset the 72h window):
```go
// SetStatus updates the record's status in place, preserving its remaining TTL.
func (s *RedisStore) SetStatus(ctx context.Context, hospitalID, hmsPatientID, status string) error {
	k := key(hospitalID, hmsPatientID)
	val, err := s.client.Get(ctx, k).Result()
	if err == redis.Nil {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("repository.SetStatus: get: %w", err)
	}
	var reg model.PendingRegistration
	if err := json.Unmarshal([]byte(val), &reg); err != nil {
		return fmt.Errorf("repository.SetStatus: unmarshal: %w", err)
	}
	reg.Status = status
	ttl, err := s.client.TTL(ctx, k).Result()
	if err != nil {
		return fmt.Errorf("repository.SetStatus: ttl: %w", err)
	}
	if ttl <= 0 {
		ttl = PendingTTL // no/again-persistent TTL: fall back to the standard window
	}
	b, err := json.Marshal(reg)
	if err != nil {
		return fmt.Errorf("repository.SetStatus: marshal: %w", err)
	}
	if err := s.client.Set(ctx, k, b, ttl).Err(); err != nil {
		return fmt.Errorf("repository.SetStatus: set: %w", err)
	}
	return nil
}
```

- [ ] **Step 5: Run the store tests to verify they pass**

Run: `cd integration-service && go test ./pkg/pending/repository/ -v`
Expected: PASS.

- [ ] **Step 6: Add `SetStatus` to the `PendingStore` interface and a handler**

In `controller/deps.go`, add to the interface:
```go
	SetStatus(ctx context.Context, hospitalID, hmsPatientID, status string) error
```
In `controller/read.go`, add (and a small allowed-status guard):
```go
// setStatusRequest is the body for POST .../status.
type setStatusRequest struct {
	Status string `json:"status" binding:"required"`
}

// SetStatus handles POST /internal/v1/registrations/:hms_patient_id/status.
// PENDING is only ever set by the webhook, so callers may set CODE_SENT or DONE.
func (h *ReadHandler) SetStatus(c *gin.Context) {
	hospitalID := c.GetString(middleware.CtxHospitalID)
	if hospitalID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing hospital identity"})
		return
	}
	var req setStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "status is required"})
		return
	}
	if req.Status != "CODE_SENT" && req.Status != "DONE" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "status must be CODE_SENT or DONE"})
		return
	}
	err := h.store.SetStatus(c.Request.Context(), hospitalID, c.Param("hms_patient_id"), req.Status)
	if err == repository.ErrNotFound {
		c.JSON(http.StatusNotFound, gin.H{"error": "no pending registration"})
		return
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "storage unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": req.Status})
}
```
Add the `repository` import to `read.go` (`github.com/hiabhi-cpu/integration-service/pkg/pending/repository`).

- [ ] **Step 7: Register the route**

In `integration-service/pkg/routes/routes.go`, inside `SetupInternal`'s `/internal/v1` group, add:
```go
		grp.POST("/registrations/:hms_patient_id/status", read.SetStatus)
```

- [ ] **Step 8: Add a handler test**

Add to `integration-service/pkg/pending/controller/read_test.go` (the `mapStore` there must gain a `SetStatus` method — add it):
```go
func (m *mapStore) SetStatus(_ context.Context, hospitalID, hms, status string) error {
	for i := range m.recs {
		if m.recs[i].HospitalID == hospitalID && m.recs[i].HMSPatientID == hms {
			m.recs[i].Status = status
			return nil
		}
	}
	return repository.ErrNotFound
}
```
(Import `repository` in the test.) And a test:
```go
func TestSetStatus_UpdatesAndRejectsBadValue(t *testing.T) {
	store := &mapStore{recs: []model.PendingRegistration{
		{HospitalID: "hosp-1", HMSPatientID: "PA-1", Name: "Asha", Mobile: "9876543210", Status: "PENDING"},
	}}
	r := readRouter(store, "hosp-1")
	// register the status route on the same test router group:
	// (readRouter builds /internal/v1; add the POST there)

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
```
Update `readRouter` in the test to also register the status route:
```go
	grp.POST("/registrations/:hms_patient_id/status", h.SetStatus)
```

- [ ] **Step 9: Build + full integration suite**

Run: `cd integration-service && go build ./... && go test ./...`
Expected: build clean; all pass.

- [ ] **Step 10: Commit**

```bash
git add integration-service/
git commit -m "feat(integration-service): pending-record status (PENDING→CODE_SENT→DONE) + set-status endpoint"
```

---

### Task 4: admin-bff — reception role + queue + send-code orchestration

Add the `reception` role gate, a queue proxy to Spec A's list, and the `send-code` orchestrator (integration get → notification claim-send → integration CODE_SENT). Wire env + Deps in `main.go`.

**Files:**
- Create: `admin-bff/pkg/middleware/role.go` (`RequireRole`)
- Create: `admin-bff/pkg/handlers/reception.go` (the orchestrator)
- Modify: `admin-bff/pkg/routes/routes.go` (Deps + routes + role gates)
- Modify: `admin-bff/bootstrap/env.go` (integration + notification base URLs)
- Modify: `admin-bff/cmd/server/main.go` (build the new proxies/handler, pass into Deps)
- Test: `admin-bff/pkg/middleware/role_test.go`, `admin-bff/pkg/handlers/reception_test.go`

**Interfaces:**
- Consumes: `session.Session{Role, HospitalID}`, `bffmw.CtxUser`, `bffmw.RequireSession`, `handlers.Proxy` (hospital-JWT forwarder), `hospitaljwt.TokenProvider`.
- Produces: `bffmw.RequireRole(roles ...string) gin.HandlerFunc`; `handlers.NewReceptionHandler(integrationBase, notificationBase string, token hospitaljwt.TokenProvider) *ReceptionHandler` with `SendCode(c *gin.Context)`; routes `GET /api/v1/reception/registrations`, `POST /api/v1/reception/registrations/:hms/send-code`.

- [ ] **Step 1: Write the failing role-middleware test**

`admin-bff/pkg/middleware/role_test.go`:
```go
package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hiabhi-cpu/admin-bff/pkg/session"
)

func routerWithRole(role string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(CtxUser, session.Session{Role: role}); c.Next() })
	r.GET("/reception", RequireRole("reception"), func(c *gin.Context) { c.Status(http.StatusOK) })
	return r
}

func TestRequireRoleAllowsMatch(t *testing.T) {
	w := httptest.NewRecorder()
	routerWithRole("reception").ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/reception", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", w.Code)
	}
}

func TestRequireRoleBlocksMismatch(t *testing.T) {
	w := httptest.NewRecorder()
	routerWithRole("admin").ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/reception", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", w.Code)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd admin-bff && go test ./pkg/middleware/ -run RequireRole -v`
Expected: FAIL — `RequireRole` undefined.

- [ ] **Step 3: Implement `RequireRole`**

`admin-bff/pkg/middleware/role.go`:
```go
package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/hiabhi-cpu/admin-bff/pkg/session"
)

// RequireRole aborts with 403 unless the session's role is one of the allowed
// roles. Must run after RequireSession (which sets CtxUser).
func RequireRole(roles ...string) gin.HandlerFunc {
	allowed := make(map[string]bool, len(roles))
	for _, r := range roles {
		allowed[r] = true
	}
	return func(c *gin.Context) {
		v, ok := c.Get(CtxUser)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
			return
		}
		if !allowed[v.(session.Session).Role] {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		c.Next()
	}
}
```

- [ ] **Step 4: Run to verify pass**

Run: `cd admin-bff && go test ./pkg/middleware/ -run RequireRole -v`
Expected: PASS.

- [ ] **Step 5: Write the failing reception-handler test**

`admin-bff/pkg/handlers/reception_test.go` — stand up fake integration + notification servers and assert `SendCode` calls them in order with the right bodies:
```go
package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	bffmw "github.com/hiabhi-cpu/admin-bff/pkg/middleware"
	"github.com/hiabhi-cpu/admin-bff/pkg/session"
)

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
	r.Use(func(c *gin.Context) { c.Set(bffmw.CtxUser, session.Session{Role: "reception", HospitalID: "hosp-1"}); c.Next() })
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
```
admin-bff's handlers package has no stub provider, so add this tiny local one at the top of the test file (implements `hospitaljwt.TokenProvider`):
```go
type stubToken string

func (s stubToken) Token(context.Context) (string, error) { return string(s), nil }
```
(add `"context"` and `"github.com/hiabhi-cpu/shared/hospitaljwt"` to the test imports; the `hospitaljwt` import is only needed if you annotate the type — the interface is satisfied structurally, so it can be omitted.)

- [ ] **Step 6: Run the test to verify it fails**

Run: `cd admin-bff && go test ./pkg/handlers/ -run SendCode -v`
Expected: FAIL — `NewReceptionHandler` undefined.

- [ ] **Step 7: Implement the reception handler**

`admin-bff/pkg/handlers/reception.go`:
```go
package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	bffmw "github.com/hiabhi-cpu/admin-bff/pkg/middleware"
	"github.com/hiabhi-cpu/admin-bff/pkg/session"
	"github.com/hiabhi-cpu/shared/hospitaljwt"
)

// ReceptionHandler orchestrates "send code": read the staged patient's mobile,
// fire the OTP claim, mark the record CODE_SENT. All downstream calls carry the
// hospital JWT; the mobile is read server-side and never returned to the browser.
type ReceptionHandler struct {
	integrationBase  string
	notificationBase string
	token            hospitaljwt.TokenProvider
	client           *http.Client
}

func NewReceptionHandler(integrationBase, notificationBase string, token hospitaljwt.TokenProvider) *ReceptionHandler {
	return &ReceptionHandler{
		integrationBase:  integrationBase,
		notificationBase: notificationBase,
		token:            token,
		client:           &http.Client{Timeout: 10 * time.Second},
	}
}

func (h *ReceptionHandler) do(ctx *gin.Context, method, url string, body any) (*http.Response, error) {
	tok, err := h.token.Token(ctx.Request.Context())
	if err != nil {
		return nil, fmt.Errorf("token: %w", err)
	}
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx.Request.Context(), method, url, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return h.client.Do(req)
}

// SendCode handles POST /api/v1/reception/registrations/:hms/send-code.
func (h *ReceptionHandler) SendCode(c *gin.Context) {
	sess := c.MustGet(bffmw.CtxUser).(session.Session)
	hms := c.Param("hms")

	// 1. Read the staged patient (raw mobile — stays server-side).
	resp, err := h.do(c, http.MethodGet, h.integrationBase+"/internal/v1/registrations/"+hms, nil)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "registration lookup failed"})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		c.JSON(http.StatusNotFound, gin.H{"error": "no staged registration"})
		return
	}
	if resp.StatusCode != http.StatusOK {
		c.JSON(http.StatusBadGateway, gin.H{"error": "registration lookup failed"})
		return
	}
	var reg struct {
		Mobile string `json:"mobile"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reg); err != nil || reg.Mobile == "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": "registration lookup failed"})
		return
	}

	// 2. Fire the OTP claim (hospital_id from the JWT; ref = hms).
	cr, err := h.do(c, http.MethodPost, h.notificationBase+"/internal/v1/otp/claim/send",
		map[string]string{"mobile": reg.Mobile, "ref": hms})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "could not send code"})
		return
	}
	defer cr.Body.Close()
	if cr.StatusCode == http.StatusTooManyRequests {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "please wait before resending"})
		return
	}
	if cr.StatusCode != http.StatusOK {
		c.JSON(http.StatusBadGateway, gin.H{"error": "could not send code"})
		return
	}

	// 3. Mark CODE_SENT (best-effort — the code is already out).
	sr, err := h.do(c, http.MethodPost, h.integrationBase+"/internal/v1/registrations/"+hms+"/status",
		map[string]string{"status": "CODE_SENT"})
	if err == nil {
		sr.Body.Close()
	}
	_ = sess // hospital scoping is enforced downstream by the hospital JWT
	c.JSON(http.StatusOK, gin.H{"status": "sent"})
}
```

- [ ] **Step 8: Run the handler test to verify it passes**

Run: `cd admin-bff && go test ./pkg/handlers/ -run SendCode -v`
Expected: PASS.

- [ ] **Step 9: Wire env, Deps, routes, and role gates**

In `admin-bff/bootstrap/env.go`, add two fields to `Env` and populate them (mirror the existing service-URL fields):
```go
	IntegrationURL  string
	NotificationURL string
```
```go
	IntegrationURL:  getOrDefault("INTEGRATION_URL", "http://localhost:9009"),
	NotificationURL: getOrDefault("NOTIFICATION_URL", "http://localhost:9004"),
```
(Use the same `mustGet`/`getOrDefault` helper the file already defines.)

In `admin-bff/pkg/routes/routes.go`: add to `Deps`:
```go
	Integration *handlers.Proxy
	Reception   *handlers.ReceptionHandler
```
Gate the existing admin/dpo endpoints and add the reception group. Replace the `authed` block so admin/dpo endpoints require those roles and reception gets its own group:
```go
		authed := api.Group("")
		authed.Use(bffmw.RequireSession(d.Store))
		{
			authed.GET("/me", d.Auth.Me)

			staff := authed.Group("")
			staff.Use(bffmw.RequireRole("admin", "dpo"))
			{
				staff.GET("/consent/stats", func(c *gin.Context) { d.Consent.ForwardGet(c, "/api/v1/consent/stats") })
				staff.GET("/audit/logs", func(c *gin.Context) { d.Audit.ForwardGet(c, "/api/v1/audit/logs") })
				staff.GET("/emergency/pending", func(c *gin.Context) { d.Emergency.ForwardGet(c, "/api/v1/emergency/pending") })
				staff.POST("/emergency/:id/review", d.Emergency.ForwardReview)
			}

			reception := authed.Group("/reception")
			reception.Use(bffmw.RequireRole("reception"))
			{
				reception.GET("/registrations", func(c *gin.Context) { d.Integration.ForwardGet(c, "/internal/v1/registrations") })
				reception.POST("/registrations/:hms/send-code", d.Reception.SendCode)
			}
		}
```

In `admin-bff/cmd/server/main.go`, the hospital-JWT token client is the local var `tokens` (`tokens := hospitaljwt.NewHospitalTokenClient(...)`), used to build the existing proxies (`handlers.NewProxy(env.ConsentServiceURL, tokens)`). Build the new proxy + handler with it:
```go
	integrationProxy := handlers.NewProxy(env.IntegrationURL, tokens)
	receptionHandler := handlers.NewReceptionHandler(env.IntegrationURL, env.NotificationURL, tokens)
```
and in the `routes.Deps{...}` literal (alongside `Consent:`/`Audit:`/`Emergency:`) add `Integration: integrationProxy, Reception: receptionHandler,`.

- [ ] **Step 10: Build + full admin-bff suite**

Run: `cd admin-bff && go build ./... && go test ./...`
Expected: build clean; all pass.

- [ ] **Step 11: Commit**

```bash
git add admin-bff/
git commit -m "feat(admin-bff): reception role + consent queue proxy + send-code orchestration"
```

---

### Task 5: Live end-to-end (API) + a reception seed

Verify the whole backend path against real services and Redis — no UI. Seed a reception user, stage a patient via the Spec A mTLS webhook, fire the code as reception, read the mock-SMS code from the notification log, resolve it, and confirm the status flips.

**Files:**
- Modify: `admin-bff/cmd/seedadmin/main.go` (allow seeding a `reception` role — likely just a `-role` flag already; confirm) — or document the SQL insert.
- Create: `docs/superpowers/plans/B1-e2e.md` is NOT needed; this task's deliverable is the verified run recorded in the task report.

- [ ] **Step 1: Bring up the stack**

Run (real infra; integration-service dev certs already generated in Spec A, else regenerate):
```bash
cd /home/reddy/Documents/Go/DPDP
# ensure Redis + Postgres are up (docker) and migrations applied
# start: auth-service, notification-service, integration-service, admin-bff (each `go run ./cmd/server &` with their .env)
```
Confirm each `/health` returns ok.

- [ ] **Step 2: Seed a reception user**

Confirm `admin-bff/cmd/seedadmin` supports `-role reception` (it inserts into `auth.admin_users` with a bcrypt hash). If it hard-codes admin/dpo, add `reception` to its allowed roles. Seed one:
```bash
cd admin-bff && go run ./cmd/seedadmin -hospital <hospital_id> -email reception@test -password test1234 -role reception
```

- [ ] **Step 3: Stage a patient (Spec A webhook, mTLS)**

```bash
cd integration-service
curl -sS --cacert certs/ca.pem --cert certs/<hospital_id>.pem --key certs/<hospital_id>.key \
  https://localhost:9443/webhook/patient-registered -H 'Content-Type: application/json' \
  -d '{"patientId":"PA-B1","givenName":"Asha","familyName":"Rao","phoneNumber":"9876543210"}'
# Expect {"status":"staged"}
```
(The cert CN must equal the seeded `hospital_id`.)

- [ ] **Step 4: Log in as reception + fire the code**

```bash
# login (cookie jar), grab CSRF, then send-code
curl -sS -c /tmp/jar -b /tmp/jar http://localhost:<admin-bff-port>/api/csrf
curl -sS -c /tmp/jar -b /tmp/jar -H 'X-CSRF-Token: <token>' -H 'Content-Type: application/json' \
  -d '{"email":"reception@test","password":"test1234"}' http://localhost:<admin-bff-port>/api/session
curl -sS -c /tmp/jar -b /tmp/jar -H 'X-CSRF-Token: <token>' -X POST \
  http://localhost:<admin-bff-port>/api/v1/reception/registrations/PA-B1/send-code
# Expect {"status":"sent"}
```
Confirm the reception session is BLOCKED from an admin endpoint:
```bash
curl -sS -c /tmp/jar -b /tmp/jar http://localhost:<admin-bff-port>/api/v1/audit/logs -o /dev/null -w '%{http_code}\n'
# Expect 403
```

- [ ] **Step 5: Read the code + resolve it**

The mock SMS client logs the OTP (masked mobile). Read the code from the notification-service log, then resolve it directly (mint a hospital JWT for `<hospital_id>` the same way Spec A's e2e did, or reuse that token):
```bash
curl -sS -H "Authorization: Bearer $HOSPITAL_JWT" -H 'Content-Type: application/json' \
  -d '{"otp":"<code-from-log>"}' \
  http://localhost:9004/internal/v1/otp/claim/resolve
# Expect {"session_id":"...","mobile":"9876543210","ref":"PA-B1"}
```
Confirm the queue now shows `CODE_SENT`:
```bash
curl -sS -c /tmp/jar -b /tmp/jar http://localhost:<admin-bff-port>/api/v1/reception/registrations
# The PA-B1 row has "status":"code_sent" (or CODE_SENT) and a MASKED mobile.
```

- [ ] **Step 6: Confirm capture works with the resolved session (proves B2 will link)**

Using the returned `session_id` + `mobile`, capture with `hms_patient_id=PA-B1` through consent-service (hospital JWT), then set the record `DONE`:
consent-service's `CaptureConsentRequest` is `{mobile, session_id, purposes []string, hms_patient_id}` — `purposes` is a plain list of purpose names:
```bash
curl -sS -H "Authorization: Bearer $HOSPITAL_JWT" -H 'Content-Type: application/json' \
  -d '{"mobile":"9876543210","session_id":"<sid>","hms_patient_id":"PA-B1","purposes":["treatment"]}' \
  http://localhost:9000/api/v1/consent/capture
# Expect 201 with a vault row carrying hms_patient_id
curl -sS -H "Authorization: Bearer $HOSPITAL_JWT" -X POST -H 'Content-Type: application/json' \
  -d '{"status":"DONE"}' http://localhost:9009/internal/v1/registrations/PA-B1/status
# Expect {"status":"DONE"}
```

- [ ] **Step 7: Record the run + commit any seed change**

Write the actual commands + outputs into the task report (what verified live). If `seedadmin` needed a `reception` role addition, commit it:
```bash
git add admin-bff/cmd/seedadmin/
git commit -m "chore(admin-bff): allow seeding the reception role"
```

---

## Self-Review

**Spec coverage** (spec section → task):
- notification hospital-scoped OTP claim (send/resolve, one Redis key, hashed match, uniqueness-on-send, resolve cap) → Tasks 1–2.
- integration `status` + set-status (TTL-preserving) → Task 3.
- admin-bff reception role (least-privilege, server-enforced) + queue + send-code orchestration → Task 4.
- Live e2e (webhook → send-code → resolve → status) → Task 5.
- B2 (reception UI, kiosk code-entry UI, kiosk-bff resolve/capture wiring, DONE-marking) → **not in B1** (separate plan).
- Auth: spec said service-token; **plan uses hospital-JWT** (Global Constraints note) — consistent with Spec A, surfaced for the reviewer.

**Placeholder scan:** No TBD/TODO. Each code step shows full code. The previously-uncertain
names are now pinned: notification store file is `redis_store.go` (GetOTPHash uses
`strings.SplitN(val,"|",2)` at ~L66); admin-bff's token client var is `tokens`; consent
`CaptureConsentRequest.Purposes` is `[]string`. The one remaining runtime unknown is the
exact `<hospital_id>` / port values, which are environment-specific and supplied at run time
in Task 5.

**Type consistency:** `SaveClaimOTP/GetClaimOTP/ClaimMembers/RemoveClaim/IncrResolveAttempts` signatures match between interface (Task 1) and service use (Task 2). `ClaimResolveResult{SessionID,Mobile,Ref}` consistent. `SetStatus(ctx, hospitalID, hmsPatientID, status)` matches across store/interface/handler/mapStore (Task 3). `RequireRole(...string)` and `NewReceptionHandler(integrationBase, notificationBase, token)` consistent across Task 4 test + impl + main wiring. Status strings `PENDING`/`CODE_SENT`/`DONE` used verbatim everywhere.
