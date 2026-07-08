# Admin Dashboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the Phase-1 hospital admin dashboard — login, consent stats, audit-log view, and emergency-review queue — backed by a new consent-stats endpoint and a Go BFF that keeps the hospital API key and JWTs out of the browser.

**Architecture:** Three components built in dependency order: (1) a new read-only `GET /api/v1/consent/stats` aggregate in the existing consent-service; (2) a new `admin-bff/` Go service that authenticates admin users against a new `auth.admin_users` table, holds the hospital API key server-side, exchanges it for the hospital JWT, and reverse-proxies data calls to consent/audit/emergency with the JWT attached; (3) a `frontend/admin-dashboard/` Vite + React + TS SPA that talks only to the BFF over an opaque session cookie.

**Tech Stack:** Go 1.25 + Gin + pgx (backend, matching existing services), Redis (BFF sessions, via `github.com/redis/go-redis/v9`), `golang.org/x/crypto/bcrypt` (password hashing), Vite + React 18 + TypeScript + CSS Modules + Recharts + Vitest/React Testing Library (frontend).

## Global Constraints

- **Go version:** `go 1.25` in every `go.mod` (matches existing services).
- **Go module path prefix:** `github.com/hiabhi-cpu/<service>` (e.g. `github.com/hiabhi-cpu/admin-bff`). Shared code is `github.com/hiabhi-cpu/shared` via `replace ../shared`.
- **RLS is mandatory on every consent/emergency query:** run `SELECT set_config('app.hospital_id', $1, true)` (parameterized, UUID-validated — reuse `setHospitalContext`) inside the same transaction as the query. Never string-interpolate the hospital id.
- **consent_vault is append-only:** never `UPDATE`/`DELETE`; the DB trigger enforces it. Stats are read-only.
- **hospital_id always comes from the JWT**, never the request body (enforced by `middleware.JWTAuth`).
- **Ports:** auth `9006`, consent `9000`, audit `9001`, emergency `9005`, notification `9004`, **admin-bff `9007`** (new), Vite dev server `5173`.
- **Trusted proxies:** every Gin service calls `r.SetTrustedProxies(nil)` so `ClientIP()` cannot be spoofed. Keep this in the BFF too.
- **Secrets never in browser code:** the hospital API key and all JWTs live only in the BFF process/config. The browser holds only an opaque, HttpOnly session cookie.
- **Dev test-hospital fixtures:** `hospital_id = a1b2c3d4-e5f6-7890-abcd-ef1234567890`, raw hospital API key `TEST-HOSPITAL-API-KEY-LOCAL-DEV-001`.

---

## Phase 1 — consent-service: `GET /api/v1/consent/stats`

Files touched (all under `consent-service/`):
- Create `pkg/consent/model/stats.go` — response types.
- Modify `pkg/consent/repository/queries.go` — add the four aggregate queries.
- Modify `pkg/consent/repository/interface.go` — add `GetStats`.
- Modify `pkg/consent/repository/repository.go` — implement `GetStats`.
- Modify `pkg/consent/service/consent_service.go` — add `Stats` to the interface + impl.
- Modify `pkg/consent/controller/consent_handler.go` — add `Stats` handler.
- Modify `pkg/routes/routes.go` — register `GET /api/v1/consent/stats`.
- Create `pkg/consent/service/stats_service_test.go` — unit test with a fake repo.
- Create `test/stats_integration_test.go` — RLS-scoped integration test (build tag `integration`).

### Task 1: Stats response model

**Files:**
- Create: `consent-service/pkg/consent/model/stats.go`

**Interfaces:**
- Produces: `model.ConsentStats`, `model.StatusCounts`, `model.PurposeBreakdown`, `model.ActivityCounts`, `model.EmergencyCounts` — the JSON shape returned by the endpoint and consumed by later tasks.

- [ ] **Step 1: Create the model file**

```go
package model

// ConsentStats is the read-only aggregate returned by GET /api/v1/consent/stats.
// All counts are hospital-scoped by RLS. "pending review" is intentionally absent:
// consent_vault is append-only so its dpo_review_status is frozen; the live pending
// count comes from emergency-service's GET /api/v1/emergency/pending instead.
type ConsentStats struct {
	Consents  StatusCounts       `json:"consents"`
	ByPurpose []PurposeBreakdown `json:"by_purpose"`
	Activity  ActivityCounts     `json:"activity"`
	Emergency EmergencyCounts    `json:"emergency"`
}

// StatusCounts counts patients by the aggregate status of their latest consent row.
type StatusCounts struct {
	Active        int `json:"active"`
	Withdrawn     int `json:"withdrawn"`
	TotalPatients int `json:"total_patients"`
}

// PurposeBreakdown is the active/withdrawn tally for one purpose across latest rows.
type PurposeBreakdown struct {
	Purpose   string `json:"purpose"`
	Active    int    `json:"active"`
	Withdrawn int    `json:"withdrawn"`
}

// ActivityCounts counts rows written inside the window, by consent event type.
type ActivityCounts struct {
	WindowDays  int `json:"window_days"`
	Captures    int `json:"captures"`
	Withdrawals int `json:"withdrawals"`
	Renewals    int `json:"renewals"`
}

// EmergencyCounts counts immutable emergency-override rows in the vault.
type EmergencyCounts struct {
	Overrides int `json:"overrides"`
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd consent-service && go build ./...`
Expected: no output (success).

- [ ] **Step 3: Commit**

```bash
git add consent-service/pkg/consent/model/stats.go
git commit -m "feat(consent): add stats response model"
```

### Task 2: Repository aggregate queries + `GetStats`

**Files:**
- Modify: `consent-service/pkg/consent/repository/queries.go`
- Modify: `consent-service/pkg/consent/repository/interface.go`
- Modify: `consent-service/pkg/consent/repository/repository.go`

**Interfaces:**
- Consumes: `model.ConsentStats` (Task 1); `setHospitalContext` (existing, `repository.go:98`).
- Produces: `ConsentRepository.GetStats(ctx, hospitalID string, windowDays int) (*model.ConsentStats, error)`.

- [ ] **Step 1: Add the queries** to `queries.go` (append inside the `const ( ... )` block, before the closing `)`).

```go
	// ── Stats aggregates (read-only) ──────────────────────────────────────────
	// Latest row per patient (excluding emergency rows and unknown identities),
	// counted by aggregate status. RLS scopes all of these to one hospital.
	queryStatsStatusCounts = `
		WITH latest AS (
			SELECT DISTINCT ON (patient_key) patient_key, status
			FROM consent.consent_vault
			WHERE type IN ('CONSENT_GIVEN','WITHDRAWAL','CONSENT_RENEWAL')
			  AND patient_key IS NOT NULL
			ORDER BY patient_key, version DESC
		)
		SELECT
			count(*) FILTER (WHERE status = 'ACTIVE')    AS active,
			count(*) FILTER (WHERE status = 'WITHDRAWN') AS withdrawn,
			count(*)                                     AS total
		FROM latest
	`

	queryStatsByPurpose = `
		WITH latest AS (
			SELECT DISTINCT ON (patient_key) patient_key, purposes
			FROM consent.consent_vault
			WHERE type IN ('CONSENT_GIVEN','WITHDRAWAL','CONSENT_RENEWAL')
			  AND patient_key IS NOT NULL
			ORDER BY patient_key, version DESC
		)
		SELECT kv.key AS purpose,
			count(*) FILTER (WHERE kv.value = 'ACTIVE')    AS active,
			count(*) FILTER (WHERE kv.value = 'WITHDRAWN') AS withdrawn
		FROM latest, jsonb_each_text(latest.purposes) AS kv
		GROUP BY kv.key
		ORDER BY kv.key
	`

	// make_interval keeps the window parameter a bound integer, never string-built SQL.
	queryStatsActivity = `
		SELECT
			count(*) FILTER (WHERE type = 'CONSENT_GIVEN')   AS captures,
			count(*) FILTER (WHERE type = 'WITHDRAWAL')      AS withdrawals,
			count(*) FILTER (WHERE type = 'CONSENT_RENEWAL') AS renewals
		FROM consent.consent_vault
		WHERE created_at >= now() - make_interval(days => $1)
	`

	queryStatsEmergency = `
		SELECT count(*) AS overrides
		FROM consent.consent_vault
		WHERE type = 'EMERGENCY_OVERRIDE'
	`
```

- [ ] **Step 2: Add the interface method** to `interface.go` (inside `ConsentRepository`, after `EnqueueAudit`).

```go
	// GetStats returns hospital-scoped aggregate consent statistics over a
	// rolling activity window (windowDays). Read-only; RLS-scoped.
	GetStats(ctx context.Context, hospitalID string, windowDays int) (*model.ConsentStats, error)
```

- [ ] **Step 3: Implement `GetStats`** in `repository.go` (append at end of file). Add `"github.com/hiabhi-cpu/consent-service/pkg/consent/model"` is already imported.

```go
// GetStats runs the four stats aggregates in one RLS-scoped read transaction.
func (r *pgxConsentRepository) GetStats(ctx context.Context, hospitalID string, windowDays int) (*model.ConsentStats, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("repository.GetStats: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := setHospitalContext(ctx, tx, hospitalID); err != nil {
		return nil, fmt.Errorf("repository.GetStats: %w", err)
	}

	stats := &model.ConsentStats{Activity: model.ActivityCounts{WindowDays: windowDays}}

	if err := tx.QueryRow(ctx, queryStatsStatusCounts).Scan(
		&stats.Consents.Active, &stats.Consents.Withdrawn, &stats.Consents.TotalPatients,
	); err != nil {
		return nil, fmt.Errorf("repository.GetStats: status counts: %w", err)
	}

	rows, err := tx.Query(ctx, queryStatsByPurpose)
	if err != nil {
		return nil, fmt.Errorf("repository.GetStats: by purpose: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p model.PurposeBreakdown
		if err := rows.Scan(&p.Purpose, &p.Active, &p.Withdrawn); err != nil {
			return nil, fmt.Errorf("repository.GetStats: scan purpose: %w", err)
		}
		stats.ByPurpose = append(stats.ByPurpose, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("repository.GetStats: purpose rows: %w", err)
	}

	if err := tx.QueryRow(ctx, queryStatsActivity, windowDays).Scan(
		&stats.Activity.Captures, &stats.Activity.Withdrawals, &stats.Activity.Renewals,
	); err != nil {
		return nil, fmt.Errorf("repository.GetStats: activity: %w", err)
	}

	if err := tx.QueryRow(ctx, queryStatsEmergency).Scan(&stats.Emergency.Overrides); err != nil {
		return nil, fmt.Errorf("repository.GetStats: emergency: %w", err)
	}

	if stats.ByPurpose == nil {
		stats.ByPurpose = []model.PurposeBreakdown{}
	}
	return stats, nil
}
```

- [ ] **Step 4: Verify it compiles**

Run: `cd consent-service && go build ./...`
Expected: no output (success).

- [ ] **Step 5: Commit**

```bash
git add consent-service/pkg/consent/repository/
git commit -m "feat(consent): add GetStats repository aggregates"
```

### Task 3: Service `Stats` method (TDD with a fake repo)

**Files:**
- Modify: `consent-service/pkg/consent/service/consent_service.go`
- Create: `consent-service/pkg/consent/service/stats_service_test.go`

**Interfaces:**
- Consumes: `ConsentRepository.GetStats` (Task 2).
- Produces: `ConsentService.Stats(ctx, hospitalID string, windowDays int) (*model.ConsentStats, error)`; constant `DefaultStatsWindowDays = 30`.

- [ ] **Step 1: Write the failing test** in `stats_service_test.go`.

```go
package service

import (
	"context"
	"testing"

	"github.com/hiabhi-cpu/consent-service/pkg/consent/model"
	"github.com/hiabhi-cpu/consent-service/pkg/consent/repository"
)

// fakeStatsRepo implements just enough of ConsentRepository for the stats test.
type fakeStatsRepo struct {
	repository.ConsentRepository // embed for the methods we don't exercise
	gotHospitalID                string
	gotWindow                    int
	ret                          *model.ConsentStats
}

func (f *fakeStatsRepo) GetStats(_ context.Context, hospitalID string, windowDays int) (*model.ConsentStats, error) {
	f.gotHospitalID = hospitalID
	f.gotWindow = windowDays
	return f.ret, nil
}

func TestStatsPassesHospitalAndWindow(t *testing.T) {
	repo := &fakeStatsRepo{ret: &model.ConsentStats{Consents: model.StatusCounts{Active: 3}}}
	svc := NewConsentService(repo, nil, nil)

	got, err := svc.Stats(context.Background(), "hosp-1", 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.gotHospitalID != "hosp-1" || repo.gotWindow != 7 {
		t.Fatalf("repo called with (%q,%d), want (hosp-1,7)", repo.gotHospitalID, repo.gotWindow)
	}
	if got.Consents.Active != 3 {
		t.Fatalf("active = %d, want 3", got.Consents.Active)
	}
}

func TestStatsDefaultsNonPositiveWindow(t *testing.T) {
	repo := &fakeStatsRepo{ret: &model.ConsentStats{}}
	svc := NewConsentService(repo, nil, nil)

	if _, err := svc.Stats(context.Background(), "hosp-1", 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.gotWindow != DefaultStatsWindowDays {
		t.Fatalf("window = %d, want default %d", repo.gotWindow, DefaultStatsWindowDays)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd consent-service && go test ./pkg/consent/service/ -run TestStats -v`
Expected: FAIL — `svc.Stats undefined` and `DefaultStatsWindowDays undefined` (compile error).

- [ ] **Step 3: Implement `Stats`** in `consent_service.go`. Add the constant near the other consts and the method to the interface + impl.

Add to the `const (...)` block near `reasonNoConsent`:

```go
// DefaultStatsWindowDays is the activity window used when the request omits or
// passes a non-positive window_days.
const DefaultStatsWindowDays = 30
```

Add to the `ConsentService` interface (after `Grant`):

```go
	// Stats returns hospital-scoped aggregate consent statistics. A non-positive
	// windowDays falls back to DefaultStatsWindowDays.
	Stats(ctx context.Context, hospitalID string, windowDays int) (*model.ConsentStats, error)
```

Add the implementation (append at end of file):

```go
func (s *consentService) Stats(ctx context.Context, hospitalID string, windowDays int) (*model.ConsentStats, error) {
	if windowDays <= 0 {
		windowDays = DefaultStatsWindowDays
	}
	stats, err := s.repo.GetStats(ctx, hospitalID, windowDays)
	if err != nil {
		return nil, fmt.Errorf("ConsentService.Stats: %w", err)
	}
	return stats, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd consent-service && go test ./pkg/consent/service/ -run TestStats -v`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
git add consent-service/pkg/consent/service/
git commit -m "feat(consent): add Stats service method with window default"
```

### Task 4: Controller handler + route

**Files:**
- Modify: `consent-service/pkg/consent/controller/consent_handler.go`
- Modify: `consent-service/pkg/routes/routes.go`

**Interfaces:**
- Consumes: `ConsentService.Stats` (Task 3), `middleware.CtxHospitalID`.
- Produces: HTTP `GET /api/v1/consent/stats?window_days=` → 200 `model.ConsentStats`.

- [ ] **Step 1: Add the handler** to `consent_handler.go` (append after `Grant`). `strconv` must be imported — add it to the import block.

```go
// Stats handles GET /api/v1/consent/stats?window_days=30 — hospital-scoped
// aggregate consent statistics for the admin dashboard.
func (h *ConsentHandler) Stats(c *gin.Context) {
	hospitalID := c.GetString(middleware.CtxHospitalID)
	if hospitalID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing hospital identity"})
		return
	}

	// window_days is optional; a bad or non-positive value falls back in the
	// service to DefaultStatsWindowDays. Clamp the upper bound here.
	windowDays, err := strconv.Atoi(c.DefaultQuery("window_days", "30"))
	if err != nil || windowDays < 1 {
		windowDays = 0 // service applies the default
	}
	if windowDays > 365 {
		windowDays = 365
	}

	stats, err := h.svc.Stats(c.Request.Context(), hospitalID, windowDays)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch stats"})
		return
	}
	c.JSON(http.StatusOK, stats)
}
```

Add `"strconv"` to the imports:

```go
import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/hiabhi-cpu/consent-service/pkg/consent/model"
	"github.com/hiabhi-cpu/consent-service/pkg/consent/service"
	"github.com/hiabhi-cpu/shared/middleware"
)
```

- [ ] **Step 2: Register the route** in `routes.go` (inside the `consent` group, after `grant`).

```go
				consent.GET("/stats", consentHandler.Stats)
```

- [ ] **Step 3: Verify it compiles**

Run: `cd consent-service && go build ./...`
Expected: no output (success).

- [ ] **Step 4: Commit**

```bash
git add consent-service/pkg/consent/controller/ consent-service/pkg/routes/
git commit -m "feat(consent): expose GET /api/v1/consent/stats"
```

### Task 5: RLS integration test for stats

**Files:**
- Create: `consent-service/test/stats_integration_test.go`
- Reference: `consent-service/test/tenant_isolation_test.go` (existing harness/patterns), `consent-service/test/run-isolation.sh`.

**Interfaces:**
- Consumes: `repository.New`, `ConsentRepository.GetStats`, the existing test DB setup helpers in `test/tenant_isolation_test.go` (same package `test`, build tag `integration`).

- [ ] **Step 1: Read the existing harness** to reuse its pool/setup helpers and constants (hospital IDs, `setHospitalContext` usage, how it truncates/seeds `consent_vault`).

Run: `sed -n '1,60p' consent-service/test/tenant_isolation_test.go`
Expected: shows the `//go:build integration` tag, package name `test`, and how it builds a `pgxpool.Pool` and seeds rows for two hospitals.

- [ ] **Step 2: Write the failing test** `stats_integration_test.go`. Match the existing file's build tag, package, and pool-construction helper names (adjust the helper calls in this snippet to whatever the existing file exposes — e.g. `newTestPool(t)`, `truncateVault(t, pool)`, `insertConsentRow(...)`).

```go
//go:build integration

package test

import (
	"context"
	"testing"

	"github.com/hiabhi-cpu/consent-service/pkg/consent/repository"
)

// TestGetStatsIsolatesByHospital verifies stats never leak across hospitals: rows
// for hospital B must not affect hospital A's counts.
func TestGetStatsIsolatesByHospital(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t) // from tenant_isolation_test.go
	defer pool.Close()
	truncateVault(t, pool)

	// Two active CONSENT_GIVEN patients for hospital A, one for hospital B.
	insertConsentRow(t, pool, hospitalA, "v1|keyA1", "CONSENT_GIVEN", "ACTIVE", `{"treatment":"ACTIVE"}`)
	insertConsentRow(t, pool, hospitalA, "v1|keyA2", "CONSENT_GIVEN", "ACTIVE", `{"treatment":"ACTIVE"}`)
	insertConsentRow(t, pool, hospitalB, "v1|keyB1", "CONSENT_GIVEN", "ACTIVE", `{"treatment":"ACTIVE"}`)

	repo := repository.New(pool)

	statsA, err := repo.GetStats(ctx, hospitalA, 30)
	if err != nil {
		t.Fatalf("GetStats(A): %v", err)
	}
	if statsA.Consents.Active != 2 || statsA.Consents.TotalPatients != 2 {
		t.Fatalf("hospital A active=%d total=%d, want 2/2 (leak?)",
			statsA.Consents.Active, statsA.Consents.TotalPatients)
	}

	statsB, err := repo.GetStats(ctx, hospitalB, 30)
	if err != nil {
		t.Fatalf("GetStats(B): %v", err)
	}
	if statsB.Consents.Active != 1 {
		t.Fatalf("hospital B active=%d, want 1", statsB.Consents.Active)
	}
}

// TestGetStatsLatestRowWins verifies a withdrawal supersedes the earlier grant for
// the same patient (counted once, as withdrawn).
func TestGetStatsLatestRowWins(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	defer pool.Close()
	truncateVault(t, pool)

	insertConsentRow(t, pool, hospitalA, "v1|keyA1", "CONSENT_GIVEN", "ACTIVE", `{"treatment":"ACTIVE"}`)
	insertConsentRowV(t, pool, hospitalA, "v1|keyA1", "WITHDRAWAL", "WITHDRAWN", `{"treatment":"WITHDRAWN"}`, 2)

	stats, err := repository.New(pool).GetStats(ctx, hospitalA, 30)
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if stats.Consents.Active != 0 || stats.Consents.Withdrawn != 1 || stats.Consents.TotalPatients != 1 {
		t.Fatalf("active=%d withdrawn=%d total=%d, want 0/1/1",
			stats.Consents.Active, stats.Consents.Withdrawn, stats.Consents.TotalPatients)
	}
}
```

> If the existing harness does not expose `insertConsentRow`/`insertConsentRowV`/`newTestPool`/`truncateVault`/`hospitalA`/`hospitalB`, add small helpers in this file that mirror the inserts the existing test already performs (same columns: `id, hospital_id, patient_key, type, status, purposes, otp_verified, artifact_hash, version, created_at`). `insertConsentRowV` is `insertConsentRow` with an explicit `version`.

- [ ] **Step 3: Run to verify it fails** (before helpers exist / against empty DB it must fail meaningfully).

Run: `cd consent-service && ./test/run-isolation.sh` (or `go test -tags=integration ./test/ -run TestGetStats -v` with the test DB env the script sets)
Expected: FAIL — missing helpers or wrong counts.

- [ ] **Step 4: Add any missing helpers, then run to verify it passes**

Run: `cd consent-service && go test -tags=integration ./test/ -run TestGetStats -v`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
git add consent-service/test/stats_integration_test.go
git commit -m "test(consent): RLS-scoped integration tests for GetStats"
```

---

## Phase 2 — `admin-bff/`: the Go backend-for-frontend

New top-level Go module `admin-bff/` (module `github.com/hiabhi-cpu/admin-bff`), structured like the existing services. It authenticates admin users, holds the hospital API key, exchanges it for the hospital JWT (cached), and reverse-proxies data calls with the JWT attached. The browser only ever holds an opaque, HttpOnly session cookie.

Files created under `admin-bff/`:
- `go.mod`, `Dockerfile`, `docker-compose.yml`, `.env.example`
- `bootstrap/env.go`, `bootstrap/database.go`, `bootstrap/redis.go`
- `pkg/session/session.go` (+ `session_test.go`)
- `pkg/auth/user.go`, `pkg/auth/password.go` (+ `password_test.go`), `pkg/auth/token.go` (+ `token_test.go`)
- `pkg/httpx/csrf.go`, `pkg/httpx/cookie.go` (+ `csrf_test.go`)
- `pkg/handlers/auth_handler.go` (+ `auth_handler_test.go`), `pkg/handlers/proxy.go` (+ `proxy_test.go`)
- `pkg/middleware/session.go`
- `pkg/routes/routes.go`
- `cmd/server/main.go`, `cmd/seedadmin/main.go`
Plus repo-root `go.work` gains `./admin-bff`, and a new DB migration.

### Task 6: `auth.admin_users` migration

**Files:**
- Create: `DPDP/scripts/db/migrations/0012_admin_users.sql`

**Interfaces:**
- Produces: table `auth.admin_users(id, hospital_id, email CITEXT, password_hash, role, disabled, created_at)` with `dpdp_app` grants; consumed by the BFF user repo (Task 9) and seeder.

- [ ] **Step 1: Create the migration**

```sql
-- =============================================================================
-- 0012_admin_users.sql
-- Per-user admin/DPO accounts for the admin dashboard (Phase 1).
-- The dashboard BFF authenticates these users, then exchanges the hospital API
-- key for the hospital JWT server-side. hospital_id ties each admin to a tenant
-- so Phase-3 RBAC / multi-hospital slots in without a rewrite.
-- =============================================================================

CREATE EXTENSION IF NOT EXISTS citext;    -- case-insensitive email; enable before use

CREATE TABLE IF NOT EXISTS auth.admin_users (
  id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  hospital_id   UUID        NOT NULL REFERENCES auth.hospitals(id),
  email         CITEXT      NOT NULL UNIQUE,
  password_hash TEXT        NOT NULL,          -- bcrypt (cost 12)
  role          VARCHAR     NOT NULL DEFAULT 'admin' CHECK (role IN ('admin','dpo')),
  disabled      BOOLEAN     NOT NULL DEFAULT false,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_admin_users_hospital ON auth.admin_users (hospital_id);

-- The BFF connects as dpdp_app and looks users up by email before it knows the
-- hospital, so admin_users is NOT under RLS. Least-privilege: read + insert only
-- (password changes are a Phase-3 concern).
GRANT USAGE ON SCHEMA auth TO dpdp_app;
GRANT SELECT, INSERT ON auth.admin_users TO dpdp_app;

COMMENT ON TABLE auth.admin_users IS
  'Dashboard admin/DPO accounts. Authenticated by admin-bff; not RLS-scoped '
  '(looked up by email pre-tenant). password_hash is bcrypt cost 12.';
```

- [ ] **Step 2: Apply and verify the migration**

Run: `cd DPDP/scripts/db && ./migrate.sh up && ./migrate.sh status`
Expected: `0012_admin_users` shows as applied; no errors.

- [ ] **Step 3: Verify the table exists**

Run: `psql "$DATABASE_URL" -c '\d auth.admin_users'`
Expected: the seven columns above are listed.

- [ ] **Step 4: Commit**

```bash
git add DPDP/scripts/db/migrations/0012_admin_users.sql
git commit -m "feat(db): add auth.admin_users table for dashboard login"
```

### Task 7: BFF module scaffold (builds + serves `/health`)

**Files:**
- Create: `admin-bff/go.mod`, `admin-bff/bootstrap/env.go`, `admin-bff/bootstrap/database.go`, `admin-bff/bootstrap/redis.go`, `admin-bff/cmd/server/main.go`, `admin-bff/Dockerfile`, `admin-bff/.env.example`
- Modify: `go.work` (repo root)

**Interfaces:**
- Produces: `bootstrap.Env` (all config), `bootstrap.NewDatabase`, `bootstrap.NewRedis`; a runnable server exposing `GET /health`.

- [ ] **Step 1: Create `admin-bff/go.mod`**

```
module github.com/hiabhi-cpu/admin-bff

go 1.25

require (
	github.com/gin-gonic/gin v1.10.0
	github.com/google/uuid v1.6.0
	github.com/hiabhi-cpu/shared v0.0.0
	github.com/jackc/pgx/v5 v5.7.0
	github.com/joho/godotenv v1.5.1
	github.com/redis/go-redis/v9 v9.6.1
	golang.org/x/crypto v0.24.0
)

replace github.com/hiabhi-cpu/shared => ../shared
```

- [ ] **Step 2: Add the module to `go.work`** (repo root). Append `./admin-bff` to the `use (...)` block.

Run: `cat go.work`
Then edit so the `use` block includes `./admin-bff` alongside the existing services.

- [ ] **Step 3: Create `admin-bff/bootstrap/env.go`**

```go
package bootstrap

import (
	"fmt"
	"os"
	"time"
)

// Env holds all configuration for admin-bff loaded from environment variables.
type Env struct {
	Port                 string
	DatabaseURL          string
	RedisURL             string
	HospitalAPIKey       string // raw hospital API key — server-side secret, never sent to the browser
	AuthServiceURL       string
	ConsentServiceURL    string
	AuditServiceURL      string
	EmergencyServiceURL  string
	SessionTTL           time.Duration
	CookieSecure         bool   // false for local http dev, true in production
	StaticDir            string // path to built SPA; empty disables static serving
	AllowedOrigin        string // dev SPA origin for CSRF/allow checks, e.g. http://localhost:5173
}

// NewEnv loads and validates all required environment variables.
func NewEnv() *Env {
	return &Env{
		Port:                mustGet("ADMIN_BFF_PORT"),
		DatabaseURL:         mustGet("DATABASE_URL"),
		RedisURL:            mustGet("REDIS_URL"),
		HospitalAPIKey:      mustGet("HOSPITAL_API_KEY"),
		AuthServiceURL:      mustGet("AUTH_SERVICE_URL"),
		ConsentServiceURL:   mustGet("CONSENT_SERVICE_URL"),
		AuditServiceURL:     mustGet("AUDIT_SERVICE_URL"),
		EmergencyServiceURL: mustGet("EMERGENCY_SERVICE_URL"),
		SessionTTL:          getDurationDefault("SESSION_TTL", 8*time.Hour),
		CookieSecure:        os.Getenv("COOKIE_SECURE") == "true",
		StaticDir:           os.Getenv("STATIC_DIR"),
		AllowedOrigin:       os.Getenv("ALLOWED_ORIGIN"),
	}
}

func mustGet(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("bootstrap: required env var %q is not set", key))
	}
	return v
}

func getDurationDefault(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		panic(fmt.Sprintf("bootstrap: %s must be a Go duration (e.g. 8h): %v", key, err))
	}
	return d
}
```

- [ ] **Step 4: Create `admin-bff/bootstrap/database.go`** (copy of the consent-service pattern).

```go
package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewDatabase creates a pgx connection pool and verifies connectivity.
func NewDatabase(ctx context.Context, databaseURL string) *pgxpool.Pool {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		panic(fmt.Sprintf("bootstrap: failed to parse DATABASE_URL: %v", err))
	}
	cfg.MaxConns = 10
	cfg.MinConns = 2
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		panic(fmt.Sprintf("bootstrap: failed to create DB pool: %v", err))
	}
	if err := pool.Ping(ctx); err != nil {
		panic(fmt.Sprintf("bootstrap: DB ping failed — is PostgreSQL running? %v", err))
	}
	return pool
}
```

- [ ] **Step 5: Create `admin-bff/bootstrap/redis.go`** (copy of the notification-service pattern).

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

- [ ] **Step 6: Create a minimal `admin-bff/cmd/server/main.go`** (health only for now; expanded in Task 14).

```go
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/joho/godotenv/autoload"

	"github.com/hiabhi-cpu/admin-bff/bootstrap"
)

func main() {
	ctx := context.Background()
	env := bootstrap.NewEnv()

	db := bootstrap.NewDatabase(ctx, env.DatabaseURL)
	defer db.Close()
	rdb := bootstrap.NewRedis(ctx, env.RedisURL)
	defer rdb.Close()

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	r.Use(gin.Recovery(), gin.Logger())

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "admin-bff"})
	})

	addr := fmt.Sprintf(":%s", env.Port)
	srv := &http.Server{Addr: addr, Handler: r, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		log.Printf("admin-bff listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("admin-bff: server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	log.Println("admin-bff: stopped")
}
```

- [ ] **Step 7: Create `admin-bff/.env.example`**

```
ADMIN_BFF_PORT=9007
DATABASE_URL=postgres://dpdp_app:dpdp_app_local_dev_pw@localhost:5432/dpdp?sslmode=disable
REDIS_URL=redis://localhost:6379/0
HOSPITAL_API_KEY=TEST-HOSPITAL-API-KEY-LOCAL-DEV-001
AUTH_SERVICE_URL=http://localhost:9006
CONSENT_SERVICE_URL=http://localhost:9000
AUDIT_SERVICE_URL=http://localhost:9001
EMERGENCY_SERVICE_URL=http://localhost:9005
SESSION_TTL=8h
COOKIE_SECURE=false
STATIC_DIR=
ALLOWED_ORIGIN=http://localhost:5173
```

- [ ] **Step 8: Create `admin-bff/Dockerfile`** (mirror an existing service Dockerfile — build context is repo root because of `replace ../shared`).

Run: `cat consent-service/Dockerfile`
Then create `admin-bff/Dockerfile` with the same multi-stage structure, changing the module path to `admin-bff` and the built binary to `./cmd/server`.

- [ ] **Step 9: Resolve deps, build, and smoke-test**

Run:
```bash
cd admin-bff && go mod tidy && go build ./... && go vet ./...
```
Expected: builds clean. Then run it against local infra and curl health:
```bash
ADMIN_BFF_PORT=9007 DATABASE_URL=... REDIS_URL=... HOSPITAL_API_KEY=x \
AUTH_SERVICE_URL=x CONSENT_SERVICE_URL=x AUDIT_SERVICE_URL=x EMERGENCY_SERVICE_URL=x \
go run ./cmd/server &
sleep 1 && curl -s localhost:9007/health && kill %1
```
Expected: `{"service":"admin-bff","status":"ok"}`.

- [ ] **Step 10: Commit**

```bash
git add admin-bff/ go.work go.work.sum
git commit -m "feat(bff): scaffold admin-bff module with health endpoint"
```

### Task 8: Session store (interface + Redis impl + in-memory fake)

**Files:**
- Create: `admin-bff/pkg/session/session.go`, `admin-bff/pkg/session/session_test.go`

**Interfaces:**
- Produces: `session.Session{UserID, Email, Role, HospitalID string}`; `session.Store` interface `Create(ctx, Session) (string, error)`, `Get(ctx, id string) (Session, error)`, `Delete(ctx, id string) error`; `session.ErrNotFound`; `session.NewRedisStore(rdb, ttl)`; `session.NewMemStore()` (test double).

- [ ] **Step 1: Write the failing test** `session_test.go` (exercises the in-memory store against the contract).

```go
package session

import (
	"context"
	"errors"
	"testing"
)

func TestMemStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	want := Session{UserID: "u1", Email: "a@b.c", Role: "admin", HospitalID: "h1"}

	id, err := s.Create(ctx, want)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == "" {
		t.Fatal("Create returned empty id")
	}

	got, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != want {
		t.Fatalf("Get = %+v, want %+v", got, want)
	}

	if err := s.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete = %v, want ErrNotFound", err)
	}
}

func TestMemStoreUnknownID(t *testing.T) {
	if _, err := NewMemStore().Get(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get unknown = %v, want ErrNotFound", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd admin-bff && go test ./pkg/session/ -v`
Expected: FAIL — undefined `NewMemStore`, `Session`, `ErrNotFound`.

- [ ] **Step 3: Implement `session.go`**

```go
// Package session stores authenticated admin sessions server-side. The browser
// holds only an opaque session id (in an HttpOnly cookie); all identity lives here.
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrNotFound means no live session exists for the given id.
var ErrNotFound = errors.New("session not found")

// Session is the identity attached to a logged-in admin.
type Session struct {
	UserID     string `json:"user_id"`
	Email      string `json:"email"`
	Role       string `json:"role"`
	HospitalID string `json:"hospital_id"`
}

// Store persists sessions keyed by an opaque id.
type Store interface {
	Create(ctx context.Context, s Session) (id string, err error)
	Get(ctx context.Context, id string) (Session, error)
	Delete(ctx context.Context, id string) error
}

// newID returns a 256-bit cryptographically-random opaque session id.
func newID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("session: rand: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// ── Redis implementation ─────────────────────────────────────────────────────

const redisPrefix = "admin_sess:"

type redisStore struct {
	rdb *redis.Client
	ttl time.Duration
}

// NewRedisStore returns a Redis-backed Store with the given session TTL.
func NewRedisStore(rdb *redis.Client, ttl time.Duration) Store {
	return &redisStore{rdb: rdb, ttl: ttl}
}

func (r *redisStore) Create(ctx context.Context, s Session) (string, error) {
	id, err := newID()
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("session: marshal: %w", err)
	}
	if err := r.rdb.Set(ctx, redisPrefix+id, payload, r.ttl).Err(); err != nil {
		return "", fmt.Errorf("session: redis set: %w", err)
	}
	return id, nil
}

func (r *redisStore) Get(ctx context.Context, id string) (Session, error) {
	payload, err := r.rdb.Get(ctx, redisPrefix+id).Bytes()
	if errors.Is(err, redis.Nil) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("session: redis get: %w", err)
	}
	var s Session
	if err := json.Unmarshal(payload, &s); err != nil {
		return Session{}, fmt.Errorf("session: unmarshal: %w", err)
	}
	return s, nil
}

func (r *redisStore) Delete(ctx context.Context, id string) error {
	if err := r.rdb.Del(ctx, redisPrefix+id).Err(); err != nil {
		return fmt.Errorf("session: redis del: %w", err)
	}
	return nil
}

// ── In-memory implementation (tests / single-instance dev) ───────────────────

type memStore struct {
	mu sync.RWMutex
	m  map[string]Session
}

// NewMemStore returns an in-memory Store. Not for multi-instance production use.
func NewMemStore() Store {
	return &memStore{m: make(map[string]Session)}
}

func (s *memStore) Create(_ context.Context, sess Session) (string, error) {
	id, err := newID()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.m[id] = sess
	s.mu.Unlock()
	return id, nil
}

func (s *memStore) Get(_ context.Context, id string) (Session, error) {
	s.mu.RLock()
	sess, ok := s.m[id]
	s.mu.RUnlock()
	if !ok {
		return Session{}, ErrNotFound
	}
	return sess, nil
}

func (s *memStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	delete(s.m, id)
	s.mu.Unlock()
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd admin-bff && go test ./pkg/session/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add admin-bff/pkg/session/
git commit -m "feat(bff): session store (redis + in-memory)"
```

### Task 9: Password hashing + admin user repository

**Files:**
- Create: `admin-bff/pkg/auth/password.go`, `admin-bff/pkg/auth/password_test.go`, `admin-bff/pkg/auth/user.go`

**Interfaces:**
- Produces: `auth.HashPassword(plain string) (string, error)`, `auth.VerifyPassword(hash, plain string) bool`; `auth.AdminUser{ID, HospitalID, Email, PasswordHash, Role string; Disabled bool}`; `auth.UserRepository` interface `GetByEmail(ctx, email string) (*AdminUser, error)`; `auth.ErrUserNotFound`; `auth.NewUserRepository(pool *pgxpool.Pool)`.

- [ ] **Step 1: Write the failing test** `password_test.go`.

```go
package auth

import "testing"

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("s3cret-dev-pw")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "s3cret-dev-pw" || hash == "" {
		t.Fatal("hash must be a non-empty transformation of the password")
	}
	if !VerifyPassword(hash, "s3cret-dev-pw") {
		t.Fatal("VerifyPassword rejected the correct password")
	}
	if VerifyPassword(hash, "wrong") {
		t.Fatal("VerifyPassword accepted the wrong password")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd admin-bff && go test ./pkg/auth/ -run TestHashAndVerify -v`
Expected: FAIL — undefined `HashPassword`/`VerifyPassword`.

- [ ] **Step 3: Implement `password.go`**

```go
// Package auth handles admin user lookup, password verification, and the
// hospital-token exchange used by the BFF.
package auth

import "golang.org/x/crypto/bcrypt"

// bcryptCost matches the hospital API-key hashing cost used elsewhere.
const bcryptCost = 12

// HashPassword returns a bcrypt hash of the plaintext password.
func HashPassword(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// VerifyPassword reports whether plain matches the stored bcrypt hash. It is
// constant-time within bcrypt and never panics on malformed hashes.
func VerifyPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd admin-bff && go test ./pkg/auth/ -run TestHashAndVerify -v`
Expected: PASS.

- [ ] **Step 5: Implement `user.go`** (DB repository — verified by the login e2e in Task 14, not a unit test).

```go
package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrUserNotFound means no admin user matched the email (or the account is unusable).
var ErrUserNotFound = errors.New("admin user not found")

// AdminUser is a dashboard login account (auth.admin_users).
type AdminUser struct {
	ID           string
	HospitalID   string
	Email        string
	PasswordHash string
	Role         string
	Disabled     bool
}

// UserRepository looks up admin users for authentication.
type UserRepository interface {
	GetByEmail(ctx context.Context, email string) (*AdminUser, error)
}

type pgxUserRepository struct {
	pool *pgxpool.Pool
}

// NewUserRepository returns a Postgres-backed UserRepository.
func NewUserRepository(pool *pgxpool.Pool) UserRepository {
	return &pgxUserRepository{pool: pool}
}

const queryGetAdminByEmail = `
	SELECT id, hospital_id, email, password_hash, role, disabled
	FROM auth.admin_users
	WHERE email = $1
`

func (r *pgxUserRepository) GetByEmail(ctx context.Context, email string) (*AdminUser, error) {
	var u AdminUser
	err := r.pool.QueryRow(ctx, queryGetAdminByEmail, email).Scan(
		&u.ID, &u.HospitalID, &u.Email, &u.PasswordHash, &u.Role, &u.Disabled,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("auth.GetByEmail: %w", err)
	}
	return &u, nil
}
```

- [ ] **Step 6: Build and commit**

Run: `cd admin-bff && go build ./...`
Expected: success.

```bash
git add admin-bff/pkg/auth/password.go admin-bff/pkg/auth/password_test.go admin-bff/pkg/auth/user.go
git commit -m "feat(bff): password hashing + admin user repository"
```

### Task 10: Hospital-token exchange client

**Files:**
- Create: `admin-bff/pkg/auth/token.go`, `admin-bff/pkg/auth/token_test.go`

**Interfaces:**
- Produces: `auth.TokenProvider` interface `Token(ctx) (string, error)`; `auth.NewHospitalTokenClient(authURL, apiKey string) *HospitalTokenClient` (implements TokenProvider, caches with a refresh window). Mirrors `shared/serviceauth.Client` but calls `POST /v1/auth/token` with `{api_key}`.

- [ ] **Step 1: Write the failing test** `token_test.go` (fake auth-service via httptest).

```go
package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHospitalTokenClientFetchesAndCaches(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/auth/token" {
			t.Errorf("path = %s, want /v1/auth/token", r.URL.Path)
		}
		var body struct {
			APIKey string `json:"api_key"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.APIKey != "raw-key" {
			t.Errorf("api_key = %q, want raw-key", body.APIKey)
		}
		calls++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "jwt-123",
			"expires_at": time.Now().Add(time.Hour),
		})
	}))
	defer srv.Close()

	c := NewHospitalTokenClient(srv.URL, "raw-key")

	tok, err := c.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "jwt-123" {
		t.Fatalf("token = %q, want jwt-123", tok)
	}
	// Second call within the validity window must be served from cache.
	if _, err := c.Token(context.Background()); err != nil {
		t.Fatalf("Token(2): %v", err)
	}
	if calls != 1 {
		t.Fatalf("auth-service called %d times, want 1 (cache miss)", calls)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd admin-bff && go test ./pkg/auth/ -run TestHospitalToken -v`
Expected: FAIL — undefined `NewHospitalTokenClient`.

- [ ] **Step 3: Implement `token.go`**

```go
package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// refreshWindow proactively refreshes before expiry so an in-flight request never
// carries a token that expires mid-call.
const refreshWindow = 60 * time.Second

// TokenProvider yields a currently-valid hospital JWT.
type TokenProvider interface {
	Token(ctx context.Context) (string, error)
}

// HospitalTokenClient exchanges the hospital API key for a hospital JWT via
// auth-service POST /v1/auth/token, caching until near expiry. Safe for concurrent use.
type HospitalTokenClient struct {
	authURL    string
	apiKey     string
	httpClient *http.Client

	mu          sync.Mutex
	cachedToken string
	expiresAt   time.Time
}

// NewHospitalTokenClient builds the client. authURL is auth-service's base URL;
// apiKey is the raw hospital API key held server-side.
func NewHospitalTokenClient(authURL, apiKey string) *HospitalTokenClient {
	return &HospitalTokenClient{
		authURL:    authURL,
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
}

type issueTokenRequest struct {
	APIKey string `json:"api_key"`
}

type issueTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Token returns a valid hospital JWT, fetching a fresh one when the cache is empty
// or inside the refresh window.
func (c *HospitalTokenClient) Token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cachedToken != "" && time.Until(c.expiresAt) > refreshWindow {
		return c.cachedToken, nil
	}

	body, err := json.Marshal(issueTokenRequest{APIKey: c.apiKey})
	if err != nil {
		return "", fmt.Errorf("auth: marshal token request: %w", err)
	}
	url := fmt.Sprintf("%s/v1/auth/token", c.authURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("auth: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("auth: request auth-service: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("auth: auth-service returned status %d", resp.StatusCode)
	}

	var tr issueTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", fmt.Errorf("auth: decode token response: %w", err)
	}
	if tr.Token == "" {
		return "", fmt.Errorf("auth: auth-service returned empty token")
	}
	c.cachedToken = tr.Token
	c.expiresAt = tr.ExpiresAt
	return c.cachedToken, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd admin-bff && go test ./pkg/auth/ -run TestHospitalToken -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add admin-bff/pkg/auth/token.go admin-bff/pkg/auth/token_test.go
git commit -m "feat(bff): hospital-token exchange client with caching"
```

### Task 11: Cookie helpers + CSRF middleware

**Files:**
- Create: `admin-bff/pkg/httpx/cookie.go`, `admin-bff/pkg/httpx/csrf.go`, `admin-bff/pkg/httpx/csrf_test.go`

**Interfaces:**
- Produces: `httpx.CookieConfig{Secure bool}`; `httpx.SetSessionCookie(c *gin.Context, id string, ttl time.Duration, cfg CookieConfig)`, `httpx.ClearSessionCookie(c, cfg)`, `httpx.SessionCookieName` const; `httpx.IssueCSRF(c, cfg)`, `httpx.CSRF(cfg) gin.HandlerFunc` (rejects unsafe methods whose `X-CSRF-Token` header != `csrf_token` cookie), `httpx.CSRFCookieName`, `httpx.SessionCookieName`.

- [ ] **Step 1: Write the failing test** `csrf_test.go`.

```go
package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func newCSRFRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CSRF(CookieConfig{Secure: false}))
	r.POST("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	return r
}

func TestCSRFAllowsSafeMethods(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	newCSRFRouter().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET code = %d, want 200", w.Code)
	}
}

func TestCSRFRejectsMissingToken(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	newCSRFRouter().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("POST without token code = %d, want 403", w.Code)
	}
}

func TestCSRFAcceptsMatchingToken(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: "tok-abc"})
	req.Header.Set("X-CSRF-Token", "tok-abc")
	newCSRFRouter().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST with matching token code = %d, want 200", w.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd admin-bff && go test ./pkg/httpx/ -v`
Expected: FAIL — undefined `CSRF`, `CookieConfig`, `CSRFCookieName`.

- [ ] **Step 3: Implement `cookie.go`**

```go
// Package httpx holds HTTP concerns shared across BFF handlers: cookies and CSRF.
package httpx

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	// SessionCookieName is the opaque, HttpOnly session id cookie.
	SessionCookieName = "admin_session"
	// CSRFCookieName is the JS-readable double-submit CSRF token cookie.
	CSRFCookieName = "csrf_token"
)

// CookieConfig carries deployment-specific cookie flags.
type CookieConfig struct {
	Secure bool // true in production (HTTPS); false for local http dev
}

// SetSessionCookie writes the HttpOnly, SameSite=Strict session cookie.
func SetSessionCookie(c *gin.Context, id string, ttl time.Duration, cfg CookieConfig) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     SessionCookieName,
		Value:    id,
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		Secure:   cfg.Secure,
		SameSite: http.SameSiteStrictMode,
	})
}

// ClearSessionCookie expires the session cookie.
func ClearSessionCookie(c *gin.Context, cfg CookieConfig) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   cfg.Secure,
		SameSite: http.SameSiteStrictMode,
	})
}
```

- [ ] **Step 4: Implement `csrf.go`**

```go
package httpx

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// IssueCSRF sets a fresh double-submit CSRF token cookie. It is deliberately NOT
// HttpOnly: the SPA reads it and echoes it in the X-CSRF-Token header.
func IssueCSRF(c *gin.Context, cfg CookieConfig) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     CSRFCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int((8 * time.Hour).Seconds()),
		HttpOnly: false,
		Secure:   cfg.Secure,
		SameSite: http.SameSiteStrictMode,
	})
}

// CSRF enforces the double-submit pattern on unsafe methods: the X-CSRF-Token
// header must equal the csrf_token cookie. Safe methods pass through.
func CSRF(_ CookieConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			c.Next()
			return
		}
		cookie, err := c.Cookie(CSRFCookieName)
		header := c.GetHeader("X-CSRF-Token")
		if err != nil || cookie == "" || header == "" ||
			subtle.ConstantTimeCompare([]byte(cookie), []byte(header)) != 1 {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "invalid CSRF token"})
			return
		}
		c.Next()
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd admin-bff && go test ./pkg/httpx/ -v`
Expected: PASS (all three).

- [ ] **Step 6: Commit**

```bash
git add admin-bff/pkg/httpx/
git commit -m "feat(bff): session cookies + double-submit CSRF"
```

### Task 12: Session middleware + auth handlers (login / logout / me)

**Files:**
- Create: `admin-bff/pkg/middleware/session.go`, `admin-bff/pkg/handlers/auth_handler.go`, `admin-bff/pkg/handlers/auth_handler_test.go`

**Interfaces:**
- Consumes: `session.Store`, `session.Session`, `session.ErrNotFound`, `auth.UserRepository`, `auth.VerifyPassword`, `httpx.*`.
- Produces: `middleware.RequireSession(store session.Store) gin.HandlerFunc` (sets `middleware.CtxUser` = `session.Session` in context, else 401); `handlers.NewAuthHandler(users auth.UserRepository, store session.Store, ttl time.Duration, cfg httpx.CookieConfig)`; handler methods `Login`, `Logout`, `Me`.

- [ ] **Step 1: Implement `middleware/session.go`** (no separate test — covered by handler tests + e2e).

```go
// Package middleware provides BFF-local gin middleware.
package middleware

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/hiabhi-cpu/admin-bff/pkg/session"
	"github.com/hiabhi-cpu/admin-bff/pkg/httpx"
)

// CtxUser is the gin context key holding the authenticated session.Session.
const CtxUser = "admin_user"

// RequireSession loads the session from the cookie and aborts 401 if absent/expired.
func RequireSession(store session.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := c.Cookie(httpx.SessionCookieName)
		if err != nil || id == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
			return
		}
		sess, err := store.Get(c.Request.Context(), id)
		if err != nil {
			if errors.Is(err, session.ErrNotFound) {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "session expired"})
				return
			}
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "session lookup failed"})
			return
		}
		c.Set(CtxUser, sess)
		c.Next()
	}
}
```

- [ ] **Step 2: Write the failing test** `auth_handler_test.go`.

```go
package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hiabhi-cpu/admin-bff/pkg/auth"
	"github.com/hiabhi-cpu/admin-bff/pkg/httpx"
	"github.com/hiabhi-cpu/admin-bff/pkg/session"
)

type fakeUserRepo struct {
	user *auth.AdminUser
}

func (f *fakeUserRepo) GetByEmail(_ context.Context, email string) (*auth.AdminUser, error) {
	if f.user != nil && strings.EqualFold(f.user.Email, email) {
		return f.user, nil
	}
	return nil, auth.ErrUserNotFound
}

func newAuthRouter(t *testing.T, repo auth.UserRepository) (*gin.Engine, session.Store) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	store := session.NewMemStore()
	h := NewAuthHandler(repo, store, time.Hour, httpx.CookieConfig{Secure: false})
	r := gin.New()
	r.POST("/api/session", h.Login)
	r.DELETE("/api/session", h.Logout)
	r.GET("/api/me", h.Me)
	return r, store
}

func seededRepo(t *testing.T) *fakeUserRepo {
	t.Helper()
	hash, err := auth.HashPassword("good-pw")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	return &fakeUserRepo{user: &auth.AdminUser{
		ID: "u1", HospitalID: "h1", Email: "admin@x.local",
		PasswordHash: hash, Role: "admin",
	}}
}

func TestLoginSuccessSetsCookie(t *testing.T) {
	r, _ := newAuthRouter(t, seededRepo(t))
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/session",
		strings.NewReader(`{"email":"admin@x.local","password":"good-pw"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var gotSession bool
	for _, ck := range w.Result().Cookies() {
		if ck.Name == httpx.SessionCookieName && ck.Value != "" {
			gotSession = true
		}
	}
	if !gotSession {
		t.Fatal("no session cookie set on success")
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["email"] != "admin@x.local" || body["role"] != "admin" {
		t.Fatalf("body = %v, want email+role", body)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	r, _ := newAuthRouter(t, seededRepo(t))
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/session",
		strings.NewReader(`{"email":"admin@x.local","password":"nope"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", w.Code)
	}
}

func TestLoginDisabledUser(t *testing.T) {
	repo := seededRepo(t)
	repo.user.Disabled = true
	r, _ := newAuthRouter(t, repo)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/session",
		strings.NewReader(`{"email":"admin@x.local","password":"good-pw"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("disabled login code = %d, want 401", w.Code)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd admin-bff && go test ./pkg/handlers/ -v`
Expected: FAIL — undefined `NewAuthHandler`.

- [ ] **Step 4: Implement `auth_handler.go`**

```go
// Package handlers holds the BFF HTTP handlers: auth (login/logout/me) and proxy.
package handlers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hiabhi-cpu/admin-bff/pkg/auth"
	"github.com/hiabhi-cpu/admin-bff/pkg/httpx"
	bffmw "github.com/hiabhi-cpu/admin-bff/pkg/middleware"
	"github.com/hiabhi-cpu/admin-bff/pkg/session"
)

// AuthHandler serves login, logout, and current-user endpoints.
type AuthHandler struct {
	users  auth.UserRepository
	store  session.Store
	ttl    time.Duration
	cookie httpx.CookieConfig
}

// NewAuthHandler builds an AuthHandler.
func NewAuthHandler(users auth.UserRepository, store session.Store, ttl time.Duration, cfg httpx.CookieConfig) *AuthHandler {
	return &AuthHandler{users: users, store: store, ttl: ttl, cookie: cfg}
}

type loginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

// Login handles POST /api/session. On success it creates a server-side session,
// sets the session + CSRF cookies, and returns the user's display identity.
func (h *AuthHandler) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "email and password are required"})
		return
	}

	user, err := h.users.GetByEmail(c.Request.Context(), req.Email)
	// Uniform 401 whether the user is missing, disabled, or the password is wrong —
	// never reveal which, to avoid account enumeration.
	if err != nil || user.Disabled || !auth.VerifyPassword(user.PasswordHash, req.Password) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid email or password"})
		return
	}

	id, err := h.store.Create(c.Request.Context(), session.Session{
		UserID: user.ID, Email: user.Email, Role: user.Role, HospitalID: user.HospitalID,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not start session"})
		return
	}
	httpx.SetSessionCookie(c, id, h.ttl, h.cookie)
	httpx.IssueCSRF(c, h.cookie)
	c.JSON(http.StatusOK, gin.H{"email": user.Email, "role": user.Role})
}

// Logout handles DELETE /api/session.
func (h *AuthHandler) Logout(c *gin.Context) {
	if id, err := c.Cookie(httpx.SessionCookieName); err == nil && id != "" {
		_ = h.store.Delete(c.Request.Context(), id)
	}
	httpx.ClearSessionCookie(c, h.cookie)
	c.JSON(http.StatusOK, gin.H{"status": "logged out"})
}

// CSRFToken handles GET /api/csrf. It seeds the double-submit CSRF cookie so the
// SPA has a token to echo on its first mutating request (login). GET is a safe
// method, so it passes the CSRF gate itself.
func (h *AuthHandler) CSRFToken(c *gin.Context) {
	httpx.IssueCSRF(c, h.cookie)
	c.Status(http.StatusNoContent)
}

// Me handles GET /api/me — returns the current user, or 401 (via RequireSession).
func (h *AuthHandler) Me(c *gin.Context) {
	v, ok := c.Get(bffmw.CtxUser)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return
	}
	sess := v.(session.Session)
	c.JSON(http.StatusOK, gin.H{"email": sess.Email, "role": sess.Role})
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd admin-bff && go test ./pkg/handlers/ -v`
Expected: PASS (login success/wrong-password/disabled).

- [ ] **Step 6: Commit**

```bash
git add admin-bff/pkg/middleware/ admin-bff/pkg/handlers/auth_handler.go admin-bff/pkg/handlers/auth_handler_test.go
git commit -m "feat(bff): session middleware + login/logout/me handlers"
```

### Task 13: Reverse-proxy handlers (JWT injection + reviewer injection)

**Files:**
- Create: `admin-bff/pkg/handlers/proxy.go`, `admin-bff/pkg/handlers/proxy_test.go`

**Interfaces:**
- Consumes: `auth.TokenProvider`, `session.Session`, `middleware.CtxUser`.
- Produces: `handlers.NewProxy(base string, token auth.TokenProvider) *Proxy`; methods `ForwardGet(c)` (proxies GET with `Authorization: Bearer`, preserving query string and the downstream status/body) and `ForwardReview(c)` (reads the session user, injects `reviewer_id` into the JSON body, POSTs to `/api/v1/emergency/:id/review`).

- [ ] **Step 1: Write the failing test** `proxy_test.go` (fake downstream via httptest).

```go
package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	bffmw "github.com/hiabhi-cpu/admin-bff/pkg/middleware"
	"github.com/hiabhi-cpu/admin-bff/pkg/session"
)

type staticToken struct{ tok string }

func (s staticToken) Token(_ context.Context) (string, error) { return s.tok, nil }

func TestForwardGetInjectsBearerAndQuery(t *testing.T) {
	var gotAuth, gotQuery string
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer down.Close()

	gin.SetMode(gin.TestMode)
	p := NewProxy(down.URL, staticToken{tok: "jwt-xyz"})
	r := gin.New()
	r.GET("/api/consent/stats", func(c *gin.Context) { p.ForwardGet(c, "/api/v1/consent/stats") })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/consent/stats?window_days=7", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", w.Code)
	}
	if gotAuth != "Bearer jwt-xyz" {
		t.Fatalf("downstream Authorization = %q, want Bearer jwt-xyz", gotAuth)
	}
	if gotQuery != "window_days=7" {
		t.Fatalf("downstream query = %q, want window_days=7", gotQuery)
	}
}

func TestForwardReviewInjectsReviewerID(t *testing.T) {
	var gotBody map[string]any
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"reviewed"}`))
	}))
	defer down.Close()

	gin.SetMode(gin.TestMode)
	p := NewProxy(down.URL, staticToken{tok: "jwt-xyz"})
	r := gin.New()
	// Simulate RequireSession having set the user.
	r.POST("/api/emergency/:id/review", func(c *gin.Context) {
		c.Set(bffmw.CtxUser, session.Session{UserID: "u1", Email: "dpo@x.local", Role: "dpo"})
		p.ForwardReview(c)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/emergency/abc-123/review",
		strings.NewReader(`{"decision":"VERIFIED"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if gotBody["reviewer_id"] != "dpo@x.local" {
		t.Fatalf("reviewer_id = %v, want dpo@x.local (server-injected)", gotBody["reviewer_id"])
	}
	if gotBody["decision"] != "VERIFIED" {
		t.Fatalf("decision = %v, want VERIFIED (preserved)", gotBody["decision"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd admin-bff && go test ./pkg/handlers/ -run TestForward -v`
Expected: FAIL — undefined `NewProxy`.

- [ ] **Step 3: Implement `proxy.go`**

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

	"github.com/hiabhi-cpu/admin-bff/pkg/auth"
	bffmw "github.com/hiabhi-cpu/admin-bff/pkg/middleware"
	"github.com/hiabhi-cpu/admin-bff/pkg/session"
)

// Proxy forwards a request to one downstream service, attaching the hospital JWT.
type Proxy struct {
	base   string
	token  auth.TokenProvider
	client *http.Client
}

// NewProxy builds a Proxy for the given downstream base URL.
func NewProxy(base string, token auth.TokenProvider) *Proxy {
	return &Proxy{base: base, token: token, client: &http.Client{Timeout: 10 * time.Second}}
}

// bearer fetches a hospital JWT or writes 502 and reports failure.
func (p *Proxy) bearer(c *gin.Context) (string, bool) {
	tok, err := p.token.Token(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "auth upstream unavailable"})
		return "", false
	}
	return tok, true
}

// pipe copies a downstream response back to the client (status + body).
func pipe(c *gin.Context, resp *http.Response) {
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	c.Data(resp.StatusCode, ct, body)
}

// ForwardGet proxies a GET to base+path, preserving the query string and adding
// the Bearer hospital JWT. downstreamPath is the target service's route.
func (p *Proxy) ForwardGet(c *gin.Context, downstreamPath string) {
	tok, ok := p.bearer(c)
	if !ok {
		return
	}
	url := p.base + downstreamPath
	if raw := c.Request.URL.RawQuery; raw != "" {
		url += "?" + raw
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, url, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "bad upstream request"})
		return
	}
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := p.client.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "upstream unavailable"})
		return
	}
	pipe(c, resp)
}

// ForwardReview proxies POST /api/emergency/:id/review, injecting reviewer_id from
// the authenticated session so it is never client-supplied free text.
func (p *Proxy) ForwardReview(c *gin.Context) {
	v, ok := c.Get(bffmw.CtxUser)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return
	}
	sess := v.(session.Session)

	var body map[string]any
	if err := c.ShouldBindJSON(&body); err != nil {
		body = map[string]any{}
	}
	body["reviewer_id"] = sess.Email // server-injected identity

	payload, err := json.Marshal(body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "bad review payload"})
		return
	}

	tok, ok := p.bearer(c)
	if !ok {
		return
	}
	id := c.Param("id")
	url := fmt.Sprintf("%s/api/v1/emergency/%s/review", p.base, id)
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "bad upstream request"})
		return
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "upstream unavailable"})
		return
	}
	pipe(c, resp)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd admin-bff && go test ./pkg/handlers/ -v`
Expected: PASS (all handler + proxy tests).

- [ ] **Step 5: Commit**

```bash
git add admin-bff/pkg/handlers/proxy.go admin-bff/pkg/handlers/proxy_test.go
git commit -m "feat(bff): reverse-proxy with JWT + reviewer-id injection"
```

### Task 14: Routes, main wiring, seeder, compose

**Files:**
- Create: `admin-bff/pkg/routes/routes.go`, `admin-bff/cmd/seedadmin/main.go`, `admin-bff/docker-compose.yml`
- Modify: `admin-bff/cmd/server/main.go` (wire everything)

**Interfaces:**
- Consumes: everything from Tasks 7–13.
- Produces: the full route table below; a `seedadmin` CLI that inserts one bcrypt admin.

- [ ] **Step 1: Implement `pkg/routes/routes.go`**

```go
package routes

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/hiabhi-cpu/admin-bff/pkg/handlers"
	"github.com/hiabhi-cpu/admin-bff/pkg/httpx"
	bffmw "github.com/hiabhi-cpu/admin-bff/pkg/middleware"
	"github.com/hiabhi-cpu/admin-bff/pkg/session"
)

// Deps bundles what the routes need.
type Deps struct {
	Auth      *handlers.AuthHandler
	Store     session.Store
	Cookie    httpx.CookieConfig
	Consent   *handlers.Proxy
	Audit     *handlers.Proxy
	Emergency *handlers.Proxy
}

// Setup registers all BFF routes.
func Setup(r *gin.Engine, d Deps) {
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "admin-bff"})
	})

	api := r.Group("/api")
	api.Use(httpx.CSRF(d.Cookie))
	{
		// Public auth endpoints. GET /csrf seeds the double-submit token so the
		// SPA can send X-CSRF-Token on its first mutating request (login).
		api.GET("/csrf", d.Auth.CSRFToken)
		api.POST("/session", d.Auth.Login)
		api.DELETE("/session", d.Auth.Logout)

		// Authenticated endpoints.
		authed := api.Group("")
		authed.Use(bffmw.RequireSession(d.Store))
		{
			authed.GET("/me", d.Auth.Me)
			authed.GET("/consent/stats", func(c *gin.Context) { d.Consent.ForwardGet(c, "/api/v1/consent/stats") })
			authed.GET("/audit/logs", func(c *gin.Context) { d.Audit.ForwardGet(c, "/api/v1/audit/logs") })
			authed.GET("/emergency/pending", func(c *gin.Context) { d.Emergency.ForwardGet(c, "/api/v1/emergency/pending") })
			authed.POST("/emergency/:id/review", d.Emergency.ForwardReview)
		}
	}
}
```

- [ ] **Step 2: Rewrite `cmd/server/main.go`** to wire everything (replace the Task 7 skeleton body).

```go
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/joho/godotenv/autoload"

	"github.com/hiabhi-cpu/admin-bff/bootstrap"
	"github.com/hiabhi-cpu/admin-bff/pkg/auth"
	"github.com/hiabhi-cpu/admin-bff/pkg/handlers"
	"github.com/hiabhi-cpu/admin-bff/pkg/httpx"
	"github.com/hiabhi-cpu/admin-bff/pkg/routes"
	"github.com/hiabhi-cpu/admin-bff/pkg/session"
)

func main() {
	ctx := context.Background()
	env := bootstrap.NewEnv()

	db := bootstrap.NewDatabase(ctx, env.DatabaseURL)
	defer db.Close()
	rdb := bootstrap.NewRedis(ctx, env.RedisURL)
	defer rdb.Close()

	cookieCfg := httpx.CookieConfig{Secure: env.CookieSecure}
	users := auth.NewUserRepository(db)
	store := session.NewRedisStore(rdb, env.SessionTTL)
	tokens := auth.NewHospitalTokenClient(env.AuthServiceURL, env.HospitalAPIKey)

	deps := routes.Deps{
		Auth:      handlers.NewAuthHandler(users, store, env.SessionTTL, cookieCfg),
		Store:     store,
		Cookie:    cookieCfg,
		Consent:   handlers.NewProxy(env.ConsentServiceURL, tokens),
		Audit:     handlers.NewProxy(env.AuditServiceURL, tokens),
		Emergency: handlers.NewProxy(env.EmergencyServiceURL, tokens),
	}

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	r.Use(gin.Recovery(), gin.Logger())
	routes.Setup(r, deps)

	// Serve the built SPA (and SPA-fallback) when STATIC_DIR is set.
	if env.StaticDir != "" {
		r.Use(static.ServeSPA(env.StaticDir)) // see note below
	}

	addr := fmt.Sprintf(":%s", env.Port)
	srv := &http.Server{Addr: addr, Handler: r, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		log.Printf("admin-bff listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("admin-bff: server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	log.Println("admin-bff: stopped")
}
```

> **Static serving note:** for Phase 1, dev runs the SPA on the Vite dev server (Task 19) and `STATIC_DIR` is empty, so delete the `if env.StaticDir != ""` block and the `static` import for now (production static serving is a follow-up). Keep `main.go` free of unused imports — remove the `static.ServeSPA` line entirely.

- [ ] **Step 3: Implement `cmd/seedadmin/main.go`** (one-shot admin seeder — avoids a bcrypt hash literal in SQL).

```go
// Command seedadmin inserts one admin user with a bcrypt password hash.
// Usage: EMAIL=admin@testhospital.local PASSWORD=admin-dev-password \
//        HOSPITAL_ID=a1b2c3d4-e5f6-7890-abcd-ef1234567890 DATABASE_URL=... \
//        go run ./cmd/seedadmin
package main

import (
	"context"
	"log"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hiabhi-cpu/admin-bff/pkg/auth"
)

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("seedadmin: %s is required", k)
	}
	return v
}

func main() {
	ctx := context.Background()
	email := mustEnv("EMAIL")
	password := mustEnv("PASSWORD")
	hospitalID := mustEnv("HOSPITAL_ID")
	role := os.Getenv("ROLE")
	if role == "" {
		role = "admin"
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		log.Fatalf("seedadmin: hash: %v", err)
	}

	pool, err := pgxpool.New(ctx, mustEnv("DATABASE_URL"))
	if err != nil {
		log.Fatalf("seedadmin: db: %v", err)
	}
	defer pool.Close()

	_, err = pool.Exec(ctx,
		`INSERT INTO auth.admin_users (hospital_id, email, password_hash, role)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (email) DO NOTHING`,
		hospitalID, email, hash, role)
	if err != nil {
		log.Fatalf("seedadmin: insert: %v", err)
	}
	log.Printf("seedadmin: ensured admin %s (role=%s)", email, role)
}
```

- [ ] **Step 4: Create `admin-bff/docker-compose.yml`** (mirror consent-service's compose; env from `.env.example`; port `9007`; on `dpdp-network`).

Run: `cat consent-service/docker-compose.yml`
Then adapt: service name `admin-bff`, container `dpdp-admin-bff`, `ADMIN_BFF_PORT: "9007"`, add `REDIS_URL`, `HOSPITAL_API_KEY`, `CONSENT_SERVICE_URL`, `AUDIT_SERVICE_URL`, `EMERGENCY_SERVICE_URL`, ports `9007:9007`, healthcheck on `/health`.

- [ ] **Step 5: Build, vet, and run the full unit suite**

Run: `cd admin-bff && go build ./... && go vet ./... && go test ./...`
Expected: builds clean; all package tests PASS.

- [ ] **Step 6: End-to-end smoke test against local infra** (requires postgres, redis, auth-service, consent-service running — see `DOCKER.md`).

```bash
# 1. Seed an admin.
cd admin-bff && EMAIL=admin@testhospital.local PASSWORD=admin-dev-password \
  HOSPITAL_ID=a1b2c3d4-e5f6-7890-abcd-ef1234567890 \
  DATABASE_URL="postgres://dpdp_app:dpdp_app_local_dev_pw@localhost:5432/dpdp?sslmode=disable" \
  go run ./cmd/seedadmin

# 2. Run the BFF (env from .env.example / your local values), then:
COOKIE=$(curl -s -c - -X POST localhost:9007/api/session \
  -H 'Content-Type: application/json' \
  -d '{"email":"admin@testhospital.local","password":"admin-dev-password"}' \
  -o /dev/null -w '%{http_code}')
echo "login status: $COOKIE"   # expect 200

# 3. Use the cookie jar to hit a proxied endpoint.
curl -s -b cookies.txt localhost:9007/api/consent/stats | head
```
Expected: login returns 200 and sets `admin_session`; `/api/consent/stats` returns the stats JSON (proxied, JWT-injected). A request without the cookie returns 401.

- [ ] **Step 7: Commit**

```bash
git add admin-bff/pkg/routes/ admin-bff/cmd/ admin-bff/docker-compose.yml
git commit -m "feat(bff): routes, main wiring, admin seeder, compose"
```

---

## Phase 3 — `frontend/admin-dashboard/`: the React SPA

Vite + React + TS. Talks only to `/api/*` (same-origin; in dev, Vite proxies `/api` → BFF `:9007`). Restrained palette: white base, one primary, three status hues. Recharts for the two charts.

Files created under `frontend/admin-dashboard/`:
- `package.json`, `vite.config.ts`, `tsconfig.json`, `index.html`, `vitest.setup.ts`
- `src/main.tsx`, `src/App.tsx`
- `src/styles/tokens.css`
- `src/api/types.ts`, `src/api/client.ts` (+ `client.test.ts`)
- `src/auth/AuthContext.tsx`, `src/components/ProtectedRoute.tsx`
- `src/pages/Login.tsx` (+ `Login.test.tsx`), `src/pages/Dashboard.tsx` (+ `Dashboard.test.tsx`), `src/pages/Audit.tsx`, `src/pages/Emergency.tsx`
- `src/components/StatTile.tsx`, `src/components/DataTable.tsx`, `src/components/Modal.tsx`, `src/components/AppShell.tsx`
- component CSS Modules alongside each.

### Task 15: Scaffold Vite + React + TS + tooling

**Files:**
- Create the project via the Vite scaffolder, then add config files.

**Interfaces:**
- Produces: a running dev server on `:5173` that proxies `/api` to `http://localhost:9007`; `npm test` runs Vitest; `src/styles/tokens.css` design tokens.

- [ ] **Step 1: Scaffold and install**

Run:
```bash
cd frontend && npm create vite@latest admin-dashboard -- --template react-ts
cd admin-dashboard
npm install
npm install react-router-dom recharts
npm install -D vitest @testing-library/react @testing-library/jest-dom @testing-library/user-event jsdom
```
Expected: `frontend/admin-dashboard/` created; deps installed.

- [ ] **Step 2: Replace `vite.config.ts`** (dev proxy + Vitest).

```ts
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      // Same-origin from the browser; Vite forwards /api to the BFF server-side.
      "/api": { target: "http://localhost:9007", changeOrigin: true },
    },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./vitest.setup.ts"],
  },
});
```

- [ ] **Step 3: Create `vitest.setup.ts`**

```ts
import "@testing-library/jest-dom";
```

- [ ] **Step 4: Add the test script** to `package.json` (`scripts` block): `"test": "vitest run"`, `"test:watch": "vitest"`.

- [ ] **Step 5: Create `src/styles/tokens.css`** (the palette — white base, one primary, status hues).

```css
:root {
  --bg: #ffffff;
  --surface: #f6f8fa;
  --border: #e5e7eb;
  --text: #111827;
  --text-muted: #6b7280;

  --primary: #0f766e;         /* calm medical teal — the single accent */
  --primary-contrast: #ffffff;

  --status-active: #15803d;   /* green  */
  --status-withdrawn: #b45309;/* amber  */
  --status-danger: #b91c1c;   /* red    */

  --radius: 8px;
  --shadow: 0 1px 3px rgba(17, 24, 39, 0.08);
  --font: system-ui, -apple-system, "Segoe UI", Roboto, sans-serif;
}

* { box-sizing: border-box; }
body { margin: 0; font-family: var(--font); color: var(--text); background: var(--surface); }
a { color: var(--primary); }
button { font-family: inherit; cursor: pointer; }
```

- [ ] **Step 6: Import tokens** in `src/main.tsx` (add `import "./styles/tokens.css";` at the top). Verify the dev server boots.

Run: `npm run dev`
Expected: Vite serves on `http://localhost:5173` with no errors (Ctrl-C to stop).

- [ ] **Step 7: Commit**

```bash
git add frontend/admin-dashboard/
git commit -m "chore(fe): scaffold admin-dashboard (vite+react+ts, tokens, vitest)"
```

### Task 16: API client + types (TDD)

**Files:**
- Create: `frontend/admin-dashboard/src/api/types.ts`, `src/api/client.ts`, `src/api/client.test.ts`

**Interfaces:**
- Produces types mirroring the backend: `ConsentStats`, `AuditLogPage`, `AuditEvent`, `EmergencyPending`, `ReviewItem`, `Me`.
- Produces `api` object: `getCsrf()`, `login(email,password)`, `logout()`, `me()`, `getStats(windowDays)`, `getAuditLogs(params)`, `getEmergencyPending()`, `reviewEmergency(id, decision)`. All send `credentials: "include"`; mutating calls attach `X-CSRF-Token` from the `csrf_token` cookie. Non-2xx throws `ApiError{status, message}`.

- [ ] **Step 1: Create `src/api/types.ts`**

```ts
export interface StatusCounts { active: number; withdrawn: number; total_patients: number; }
export interface PurposeBreakdown { purpose: string; active: number; withdrawn: number; }
export interface ActivityCounts { window_days: number; captures: number; withdrawals: number; renewals: number; }
export interface EmergencyCounts { overrides: number; }
export interface ConsentStats {
  consents: StatusCounts;
  by_purpose: PurposeBreakdown[];
  activity: ActivityCounts;
  emergency: EmergencyCounts;
}

export interface AuditEvent {
  id: number;
  event_type: string;
  actor_id: string;
  actor_type: string;
  patient_key: string;
  ip_address: string;
  details: Record<string, unknown>;
  created_at: string;
}
export interface AuditLogPage { events: AuditEvent[]; total: number; page: number; limit: number; }

export interface ReviewItem {
  access_id: string;
  emergency_id: string;
  doctor_id: string;
  emergency_reason: string;
  clinical_note: string;
  hms_patient_id?: string;
  review_status: string;
  dpo_deadline: string;
  overdue: boolean;
  created_at: string;
}
export interface EmergencyPending { pending: ReviewItem[]; total: number; }

export interface Me { email: string; role: string; }
```

- [ ] **Step 2: Write the failing test** `src/api/client.test.ts`.

```ts
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { api, ApiError } from "./client";

describe("api client", () => {
  beforeEach(() => {
    document.cookie = "csrf_token=tok-123";
  });
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("attaches the CSRF header on mutating requests", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ email: "a@b.c", role: "admin" }), { status: 200 }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await api.login("a@b.c", "pw");

    const [, init] = fetchMock.mock.calls[0];
    expect(init.headers["X-CSRF-Token"]).toBe("tok-123");
    expect(init.credentials).toBe("include");
  });

  it("throws ApiError on non-2xx", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ error: "bad" }), { status: 401 }),
    ));
    await expect(api.getStats(30)).rejects.toBeInstanceOf(ApiError);
  });
});
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd frontend/admin-dashboard && npx vitest run src/api/client.test.ts`
Expected: FAIL — cannot resolve `./client`.

- [ ] **Step 4: Implement `src/api/client.ts`**

```ts
import type { ConsentStats, AuditLogPage, EmergencyPending, Me } from "./types";

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
    this.name = "ApiError";
  }
}

function csrfToken(): string {
  const m = document.cookie.match(/(?:^|;\s*)csrf_token=([^;]+)/);
  return m ? decodeURIComponent(m[1]) : "";
}

type Method = "GET" | "POST" | "DELETE";

async function request<T>(method: Method, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {};
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (method !== "GET") headers["X-CSRF-Token"] = csrfToken();

  const res = await fetch(path, {
    method,
    credentials: "include",
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });

  if (res.status === 204) return undefined as T;
  const text = await res.text();
  const data = text ? JSON.parse(text) : undefined;
  if (!res.ok) {
    const message = (data && (data.error as string)) || `request failed (${res.status})`;
    throw new ApiError(res.status, message);
  }
  return data as T;
}

export const api = {
  getCsrf: () => request<void>("GET", "/api/csrf"),
  login: (email: string, password: string) =>
    request<Me>("POST", "/api/session", { email, password }),
  logout: () => request<void>("DELETE", "/api/session"),
  me: () => request<Me>("GET", "/api/me"),
  getStats: (windowDays: number) =>
    request<ConsentStats>("GET", `/api/consent/stats?window_days=${windowDays}`),
  getAuditLogs: (params: { page: number; limit: number; event_type?: string }) => {
    const q = new URLSearchParams({ page: String(params.page), limit: String(params.limit) });
    if (params.event_type) q.set("event_type", params.event_type);
    return request<AuditLogPage>("GET", `/api/audit/logs?${q.toString()}`);
  },
  getEmergencyPending: () => request<EmergencyPending>("GET", "/api/emergency/pending"),
  reviewEmergency: (id: string, decision: "VERIFIED" | "FLAGGED") =>
    request<{ status: string }>("POST", `/api/emergency/${id}/review`, { decision }),
};
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd frontend/admin-dashboard && npx vitest run src/api/client.test.ts`
Expected: PASS (both cases).

- [ ] **Step 6: Commit**

```bash
git add frontend/admin-dashboard/src/api/
git commit -m "feat(fe): typed API client with CSRF + ApiError"
```

### Task 17: Auth context, ProtectedRoute, Login page (TDD)

**Files:**
- Create: `src/auth/AuthContext.tsx`, `src/components/ProtectedRoute.tsx`, `src/pages/Login.tsx`, `src/pages/Login.module.css`, `src/pages/Login.test.tsx`

**Interfaces:**
- Produces: `AuthProvider`, `useAuth()` → `{ user: Me | null; loading: boolean; login(email,password); logout() }`; `ProtectedRoute` (redirects to `/login` when `!user`); `Login` page.

- [ ] **Step 1: Implement `src/auth/AuthContext.tsx`**

```tsx
import { createContext, useContext, useEffect, useState, ReactNode } from "react";
import { api } from "../api/client";
import type { Me } from "../api/types";

interface AuthState {
  user: Me | null;
  loading: boolean;
  login: (email: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
}

const AuthCtx = createContext<AuthState | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<Me | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    // Seed a CSRF token, then restore any existing session.
    (async () => {
      try {
        await api.getCsrf();
        setUser(await api.me());
      } catch {
        setUser(null);
      } finally {
        setLoading(false);
      }
    })();
  }, []);

  const login = async (email: string, password: string) => {
    const me = await api.login(email, password);
    setUser(me);
  };
  const logout = async () => {
    await api.logout();
    setUser(null);
  };

  return <AuthCtx.Provider value={{ user, loading, login, logout }}>{children}</AuthCtx.Provider>;
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthCtx);
  if (!ctx) throw new Error("useAuth must be used within AuthProvider");
  return ctx;
}
```

- [ ] **Step 2: Implement `src/components/ProtectedRoute.tsx`**

```tsx
import { ReactNode } from "react";
import { Navigate } from "react-router-dom";
import { useAuth } from "../auth/AuthContext";

export function ProtectedRoute({ children }: { children: ReactNode }) {
  const { user, loading } = useAuth();
  if (loading) return <div style={{ padding: 24 }}>Loading…</div>;
  if (!user) return <Navigate to="/login" replace />;
  return <>{children}</>;
}
```

- [ ] **Step 3: Implement `src/pages/Login.module.css`**

```css
.wrap { min-height: 100vh; display: grid; place-items: center; background: var(--surface); }
.card {
  width: 340px; background: var(--bg); padding: 32px;
  border: 1px solid var(--border); border-radius: var(--radius); box-shadow: var(--shadow);
}
.title { margin: 0 0 20px; font-size: 20px; }
.field { display: flex; flex-direction: column; gap: 6px; margin-bottom: 14px; }
.field label { font-size: 13px; color: var(--text-muted); }
.field input { padding: 9px 10px; border: 1px solid var(--border); border-radius: 6px; font-size: 14px; }
.button {
  width: 100%; padding: 10px; margin-top: 6px; border: 0; border-radius: 6px;
  background: var(--primary); color: var(--primary-contrast); font-size: 14px; font-weight: 600;
}
.button:disabled { opacity: 0.6; }
.error { color: var(--status-danger); font-size: 13px; margin: 0 0 12px; }
```

- [ ] **Step 4: Write the failing test** `src/pages/Login.test.tsx`.

```tsx
import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { Login } from "./Login";

const loginMock = vi.fn();
vi.mock("../auth/AuthContext", () => ({
  useAuth: () => ({ user: null, loading: false, login: loginMock, logout: vi.fn() }),
}));

function renderLogin() {
  return render(
    <MemoryRouter>
      <Login />
    </MemoryRouter>,
  );
}

describe("Login", () => {
  it("submits entered credentials", async () => {
    loginMock.mockResolvedValueOnce(undefined);
    renderLogin();
    await userEvent.type(screen.getByLabelText(/email/i), "admin@x.local");
    await userEvent.type(screen.getByLabelText(/password/i), "pw");
    await userEvent.click(screen.getByRole("button", { name: /sign in/i }));
    expect(loginMock).toHaveBeenCalledWith("admin@x.local", "pw");
  });

  it("shows an error when login fails", async () => {
    loginMock.mockRejectedValueOnce(new Error("invalid email or password"));
    renderLogin();
    await userEvent.type(screen.getByLabelText(/email/i), "admin@x.local");
    await userEvent.type(screen.getByLabelText(/password/i), "bad");
    await userEvent.click(screen.getByRole("button", { name: /sign in/i }));
    expect(await screen.findByText(/invalid email or password/i)).toBeInTheDocument();
  });
});
```

- [ ] **Step 5: Run test to verify it fails**

Run: `cd frontend/admin-dashboard && npx vitest run src/pages/Login.test.tsx`
Expected: FAIL — cannot resolve `./Login`.

- [ ] **Step 6: Implement `src/pages/Login.tsx`**

```tsx
import { FormEvent, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useAuth } from "../auth/AuthContext";
import styles from "./Login.module.css";

export function Login() {
  const { login } = useAuth();
  const navigate = useNavigate();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError("");
    setBusy(true);
    try {
      await login(email, password);
      navigate("/", { replace: true });
    } catch (err) {
      setError(err instanceof Error ? err.message : "login failed");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className={styles.wrap}>
      <form className={styles.card} onSubmit={onSubmit}>
        <h1 className={styles.title}>Consent Manager — Admin</h1>
        {error && <p className={styles.error}>{error}</p>}
        <div className={styles.field}>
          <label htmlFor="email">Email</label>
          <input id="email" type="email" value={email}
            onChange={(e) => setEmail(e.target.value)} autoComplete="username" required />
        </div>
        <div className={styles.field}>
          <label htmlFor="password">Password</label>
          <input id="password" type="password" value={password}
            onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" required />
        </div>
        <button className={styles.button} type="submit" disabled={busy}>
          {busy ? "Signing in…" : "Sign in"}
        </button>
      </form>
    </div>
  );
}
```

- [ ] **Step 7: Run tests to verify they pass**

Run: `cd frontend/admin-dashboard && npx vitest run src/pages/Login.test.tsx`
Expected: PASS (both cases).

- [ ] **Step 8: Commit**

```bash
git add frontend/admin-dashboard/src/auth/ frontend/admin-dashboard/src/components/ProtectedRoute.tsx frontend/admin-dashboard/src/pages/Login.*
git commit -m "feat(fe): auth context, protected route, login page"
```

### Task 18: Shared components — StatTile, DataTable, Modal, AppShell

**Files:**
- Create: `src/components/StatTile.tsx` (+ `.module.css`), `src/components/DataTable.tsx` (+ `.module.css`), `src/components/Modal.tsx` (+ `.module.css`), `src/components/AppShell.tsx` (+ `.module.css`)

**Interfaces:**
- Produces: `StatTile({label, value, tone?})` where `tone` ∈ `"default" | "active" | "withdrawn" | "danger"`; `DataTable({columns, rows})` generic table; `Modal({open, title, onClose, children})`; `AppShell` (nav + logout, renders `<Outlet/>`).

- [ ] **Step 1: `StatTile.tsx` + `StatTile.module.css`**

```tsx
import styles from "./StatTile.module.css";

type Tone = "default" | "active" | "withdrawn" | "danger";

export function StatTile({ label, value, tone = "default" }: { label: string; value: number; tone?: Tone }) {
  return (
    <div className={styles.tile}>
      <span className={styles.label}>{label}</span>
      <span className={`${styles.value} ${styles[tone]}`}>{value}</span>
    </div>
  );
}
```

```css
.tile {
  background: var(--bg); border: 1px solid var(--border); border-radius: var(--radius);
  padding: 16px 18px; box-shadow: var(--shadow); display: flex; flex-direction: column; gap: 6px; min-width: 130px;
}
.label { font-size: 13px; color: var(--text-muted); }
.value { font-size: 28px; font-weight: 700; }
.default { color: var(--text); }
.active { color: var(--status-active); }
.withdrawn { color: var(--status-withdrawn); }
.danger { color: var(--status-danger); }
```

- [ ] **Step 2: `DataTable.tsx` + `DataTable.module.css`**

```tsx
import { ReactNode } from "react";
import styles from "./DataTable.module.css";

export interface Column<T> { key: string; header: string; render: (row: T) => ReactNode; }

export function DataTable<T>({ columns, rows, empty }: { columns: Column<T>[]; rows: T[]; empty?: string }) {
  if (rows.length === 0) return <p className={styles.empty}>{empty ?? "Nothing to show."}</p>;
  return (
    <div className={styles.scroll}>
      <table className={styles.table}>
        <thead>
          <tr>{columns.map((c) => <th key={c.key}>{c.header}</th>)}</tr>
        </thead>
        <tbody>
          {rows.map((row, i) => (
            <tr key={i}>{columns.map((c) => <td key={c.key}>{c.render(row)}</td>)}</tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
```

```css
.scroll { overflow-x: auto; }
.table { width: 100%; border-collapse: collapse; background: var(--bg); font-size: 14px; }
.table th, .table td { text-align: left; padding: 10px 12px; border-bottom: 1px solid var(--border); }
.table th { color: var(--text-muted); font-weight: 600; font-size: 12px; text-transform: uppercase; }
.empty { color: var(--text-muted); padding: 24px; text-align: center; }
```

- [ ] **Step 3: `Modal.tsx` + `Modal.module.css`**

```tsx
import { ReactNode } from "react";
import styles from "./Modal.module.css";

export function Modal({ open, title, onClose, children }:
  { open: boolean; title: string; onClose: () => void; children: ReactNode }) {
  if (!open) return null;
  return (
    <div className={styles.backdrop} onClick={onClose}>
      <div className={styles.dialog} onClick={(e) => e.stopPropagation()} role="dialog" aria-label={title}>
        <div className={styles.head}>
          <h2 className={styles.title}>{title}</h2>
          <button className={styles.close} onClick={onClose} aria-label="Close">×</button>
        </div>
        {children}
      </div>
    </div>
  );
}
```

```css
.backdrop { position: fixed; inset: 0; background: rgba(17,24,39,0.4); display: grid; place-items: center; }
.dialog { width: 420px; max-width: 92vw; background: var(--bg); border-radius: var(--radius); padding: 20px; box-shadow: var(--shadow); }
.head { display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; }
.title { margin: 0; font-size: 17px; }
.close { border: 0; background: none; font-size: 22px; line-height: 1; color: var(--text-muted); }
```

- [ ] **Step 4: `AppShell.tsx` + `AppShell.module.css`**

```tsx
import { NavLink, Outlet, useNavigate } from "react-router-dom";
import { useAuth } from "../auth/AuthContext";
import styles from "./AppShell.module.css";

export function AppShell() {
  const { user, logout } = useAuth();
  const navigate = useNavigate();
  const onLogout = async () => { await logout(); navigate("/login", { replace: true }); };
  const cls = ({ isActive }: { isActive: boolean }) => (isActive ? `${styles.link} ${styles.active}` : styles.link);

  return (
    <div>
      <header className={styles.bar}>
        <span className={styles.brand}>Consent Manager</span>
        <nav className={styles.nav}>
          <NavLink to="/" end className={cls}>Dashboard</NavLink>
          <NavLink to="/audit" className={cls}>Audit</NavLink>
          <NavLink to="/emergency" className={cls}>Emergency</NavLink>
        </nav>
        <span className={styles.spacer} />
        <span className={styles.user}>{user?.email}</span>
        <button className={styles.logout} onClick={onLogout}>Log out</button>
      </header>
      <main className={styles.main}><Outlet /></main>
    </div>
  );
}
```

```css
.bar { display: flex; align-items: center; gap: 18px; padding: 0 20px; height: 56px; background: var(--bg); border-bottom: 1px solid var(--border); }
.brand { font-weight: 700; color: var(--primary); }
.nav { display: flex; gap: 6px; }
.link { padding: 8px 12px; border-radius: 6px; text-decoration: none; color: var(--text-muted); font-size: 14px; }
.active { color: var(--primary); background: var(--surface); font-weight: 600; }
.spacer { flex: 1; }
.user { font-size: 13px; color: var(--text-muted); }
.logout { border: 1px solid var(--border); background: var(--bg); padding: 7px 12px; border-radius: 6px; font-size: 13px; }
.main { padding: 24px; max-width: 1100px; margin: 0 auto; }
```

- [ ] **Step 5: Type-check and commit**

Run: `cd frontend/admin-dashboard && npx tsc --noEmit`
Expected: no errors.

```bash
git add frontend/admin-dashboard/src/components/
git commit -m "feat(fe): shared StatTile, DataTable, Modal, AppShell"
```

### Task 19: Dashboard page — stats tiles + charts (TDD)

**Files:**
- Create: `src/pages/Dashboard.tsx`, `src/pages/Dashboard.module.css`, `src/pages/Dashboard.test.tsx`

**Interfaces:**
- Consumes: `api.getStats`, `api.getEmergencyPending`, `StatTile`, Recharts.
- Produces: `Dashboard` page — four tiles (active, withdrawn, emergency overrides, pending review [from emergency endpoint]), a donut (active vs withdrawn), a bar chart (by purpose), an activity row, and a window selector (7/30/90 days).

- [ ] **Step 1: Write the failing test** `src/pages/Dashboard.test.tsx`.

```tsx
import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { Dashboard } from "./Dashboard";

vi.mock("../api/client", () => ({
  api: {
    getStats: vi.fn().mockResolvedValue({
      consents: { active: 128, withdrawn: 14, total_patients: 142 },
      by_purpose: [{ purpose: "treatment", active: 120, withdrawn: 6 }],
      activity: { window_days: 30, captures: 51, withdrawals: 9, renewals: 3 },
      emergency: { overrides: 7 },
    }),
    getEmergencyPending: vi.fn().mockResolvedValue({ pending: [], total: 2 }),
  },
}));

// Recharts needs a sized container in jsdom; stub ResizeObserver.
vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });

describe("Dashboard", () => {
  it("renders stat tiles from the stats + pending endpoints", async () => {
    render(<Dashboard />);
    expect(await screen.findByText("128")).toBeInTheDocument();  // active
    expect(await screen.findByText("14")).toBeInTheDocument();   // withdrawn
    expect(await screen.findByText("7")).toBeInTheDocument();    // overrides
    expect(await screen.findByText("2")).toBeInTheDocument();    // pending (from emergency)
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd frontend/admin-dashboard && npx vitest run src/pages/Dashboard.test.tsx`
Expected: FAIL — cannot resolve `./Dashboard`.

- [ ] **Step 3: Implement `src/pages/Dashboard.module.css`**

```css
.tiles { display: flex; flex-wrap: wrap; gap: 14px; margin-bottom: 22px; }
.charts { display: grid; grid-template-columns: 1fr 1fr; gap: 18px; }
@media (max-width: 760px) { .charts { grid-template-columns: 1fr; } }
.card { background: var(--bg); border: 1px solid var(--border); border-radius: var(--radius); box-shadow: var(--shadow); padding: 16px; }
.card h3 { margin: 0 0 12px; font-size: 14px; color: var(--text-muted); }
.toolbar { display: flex; align-items: center; gap: 10px; margin-bottom: 16px; }
.toolbar select { padding: 7px 10px; border: 1px solid var(--border); border-radius: 6px; }
.activity { display: flex; gap: 24px; margin-top: 8px; font-size: 14px; color: var(--text-muted); }
.activity b { color: var(--text); }
```

- [ ] **Step 4: Implement `src/pages/Dashboard.tsx`**

```tsx
import { useEffect, useState } from "react";
import {
  PieChart, Pie, Cell, BarChart, Bar, XAxis, YAxis, Tooltip, ResponsiveContainer, Legend,
} from "recharts";
import { api } from "../api/client";
import type { ConsentStats } from "../api/types";
import { StatTile } from "../components/StatTile";
import styles from "./Dashboard.module.css";

const ACTIVE = "#15803d";
const WITHDRAWN = "#b45309";

export function Dashboard() {
  const [windowDays, setWindowDays] = useState(30);
  const [stats, setStats] = useState<ConsentStats | null>(null);
  const [pending, setPending] = useState(0);
  const [error, setError] = useState("");

  useEffect(() => {
    let alive = true;
    setError("");
    Promise.all([api.getStats(windowDays), api.getEmergencyPending()])
      .then(([s, p]) => { if (alive) { setStats(s); setPending(p.total); } })
      .catch((e) => { if (alive) setError(e instanceof Error ? e.message : "failed to load"); });
    return () => { alive = false; };
  }, [windowDays]);

  if (error) return <p style={{ color: "var(--status-danger)" }}>{error}</p>;
  if (!stats) return <p>Loading…</p>;

  const donut = [
    { name: "Active", value: stats.consents.active },
    { name: "Withdrawn", value: stats.consents.withdrawn },
  ];

  return (
    <div>
      <div className={styles.toolbar}>
        <label htmlFor="win">Window</label>
        <select id="win" value={windowDays} onChange={(e) => setWindowDays(Number(e.target.value))}>
          <option value={7}>Last 7 days</option>
          <option value={30}>Last 30 days</option>
          <option value={90}>Last 90 days</option>
        </select>
      </div>

      <div className={styles.tiles}>
        <StatTile label="Active consents" value={stats.consents.active} tone="active" />
        <StatTile label="Withdrawn" value={stats.consents.withdrawn} tone="withdrawn" />
        <StatTile label="Emergency overrides" value={stats.emergency.overrides} />
        <StatTile label="Pending review" value={pending} tone={pending > 0 ? "danger" : "default"} />
      </div>

      <div className={styles.charts}>
        <div className={styles.card}>
          <h3>Active vs withdrawn</h3>
          <ResponsiveContainer width="100%" height={240}>
            <PieChart>
              <Pie data={donut} dataKey="value" nameKey="name" innerRadius={60} outerRadius={90}>
                <Cell fill={ACTIVE} /><Cell fill={WITHDRAWN} />
              </Pie>
              <Legend /><Tooltip />
            </PieChart>
          </ResponsiveContainer>
        </div>
        <div className={styles.card}>
          <h3>By purpose</h3>
          <ResponsiveContainer width="100%" height={240}>
            <BarChart data={stats.by_purpose}>
              <XAxis dataKey="purpose" fontSize={12} /><YAxis allowDecimals={false} fontSize={12} />
              <Tooltip /><Legend />
              <Bar dataKey="active" name="Active" fill={ACTIVE} />
              <Bar dataKey="withdrawn" name="Withdrawn" fill={WITHDRAWN} />
            </BarChart>
          </ResponsiveContainer>
        </div>
      </div>

      <div className={styles.activity}>
        <span>Captures <b>{stats.activity.captures}</b></span>
        <span>Withdrawals <b>{stats.activity.withdrawals}</b></span>
        <span>Renewals <b>{stats.activity.renewals}</b></span>
        <span>· last {stats.activity.window_days} days</span>
      </div>
    </div>
  );
}
```

> **Chart colors:** before finalizing, load the `dataviz` skill and reconcile the two chart hues with `references/palette.md`; keep active=green / withdrawn=amber consistent with the tiles.

- [ ] **Step 5: Run test to verify it passes**

Run: `cd frontend/admin-dashboard && npx vitest run src/pages/Dashboard.test.tsx`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add frontend/admin-dashboard/src/pages/Dashboard.*
git commit -m "feat(fe): dashboard page with stat tiles + charts"
```

### Task 20: Audit page — table, filter, pagination

**Files:**
- Create: `src/pages/Audit.tsx`, `src/pages/Audit.module.css`

**Interfaces:**
- Consumes: `api.getAuditLogs`, `DataTable`.
- Produces: `Audit` page — event-type filter, prev/next pagination against `total`, expandable details.

- [ ] **Step 1: Implement `src/pages/Audit.module.css`**

```css
.toolbar { display: flex; gap: 10px; align-items: center; margin-bottom: 16px; }
.toolbar input, .toolbar select { padding: 7px 10px; border: 1px solid var(--border); border-radius: 6px; }
.pager { display: flex; gap: 12px; align-items: center; justify-content: center; margin-top: 16px; }
.pager button { padding: 7px 12px; border: 1px solid var(--border); border-radius: 6px; background: var(--bg); }
.pager button:disabled { opacity: 0.5; }
.details { font-family: ui-monospace, monospace; font-size: 12px; color: var(--text-muted); white-space: pre-wrap; }
.mask { color: var(--text-muted); }
```

- [ ] **Step 2: Implement `src/pages/Audit.tsx`**

```tsx
import { useEffect, useState } from "react";
import { api } from "../api/client";
import type { AuditEvent, AuditLogPage } from "../api/types";
import { DataTable, Column } from "../components/DataTable";
import styles from "./Audit.module.css";

const LIMIT = 25;

function maskKey(k: string): string {
  if (!k) return "—";
  return k.length > 12 ? `${k.slice(0, 8)}…${k.slice(-4)}` : k;
}

const columns: Column<AuditEvent>[] = [
  { key: "time", header: "Time", render: (e) => new Date(e.created_at).toLocaleString() },
  { key: "type", header: "Event", render: (e) => e.event_type },
  { key: "actor", header: "Actor", render: (e) => `${e.actor_type}:${e.actor_id}` },
  { key: "patient", header: "Patient", render: (e) => <span className={styles.mask}>{maskKey(e.patient_key)}</span> },
  { key: "ip", header: "IP", render: (e) => e.ip_address || "—" },
  { key: "details", header: "Details", render: (e) => <span className={styles.details}>{JSON.stringify(e.details)}</span> },
];

export function Audit() {
  const [page, setPage] = useState(1);
  const [eventType, setEventType] = useState("");
  const [filterInput, setFilterInput] = useState("");
  const [data, setData] = useState<AuditLogPage | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    let alive = true;
    setError("");
    api.getAuditLogs({ page, limit: LIMIT, event_type: eventType || undefined })
      .then((d) => { if (alive) setData(d); })
      .catch((e) => { if (alive) setError(e instanceof Error ? e.message : "failed to load"); });
    return () => { alive = false; };
  }, [page, eventType]);

  const totalPages = data ? Math.max(1, Math.ceil(data.total / LIMIT)) : 1;

  return (
    <div>
      <div className={styles.toolbar}>
        <input placeholder="Filter by event type (e.g. CONSENT_GRANTED)"
          value={filterInput} onChange={(e) => setFilterInput(e.target.value)} style={{ width: 320 }} />
        <button onClick={() => { setPage(1); setEventType(filterInput.trim()); }}>Apply</button>
        {eventType && <button onClick={() => { setFilterInput(""); setEventType(""); setPage(1); }}>Clear</button>}
      </div>

      {error && <p style={{ color: "var(--status-danger)" }}>{error}</p>}
      <DataTable columns={columns} rows={data?.events ?? []} empty="No audit events." />

      <div className={styles.pager}>
        <button disabled={page <= 1} onClick={() => setPage((p) => p - 1)}>← Prev</button>
        <span>Page {page} / {totalPages}</span>
        <button disabled={page >= totalPages} onClick={() => setPage((p) => p + 1)}>Next →</button>
      </div>
    </div>
  );
}
```

- [ ] **Step 3: Type-check and commit**

Run: `cd frontend/admin-dashboard && npx tsc --noEmit`
Expected: no errors.

```bash
git add frontend/admin-dashboard/src/pages/Audit.*
git commit -m "feat(fe): audit log page with filter + pagination"
```

### Task 21: Emergency page — pending queue + review modal (TDD)

**Files:**
- Create: `src/pages/Emergency.tsx`, `src/pages/Emergency.module.css`, `src/pages/Emergency.test.tsx`

**Interfaces:**
- Consumes: `api.getEmergencyPending`, `api.reviewEmergency`, `DataTable`, `Modal`.
- Produces: `Emergency` page — pending overrides with overdue badges; a "Review" action opening a modal that records VERIFIED/FLAGGED (reviewer identity is server-injected by the BFF).

- [ ] **Step 1: Write the failing test** `src/pages/Emergency.test.tsx`.

```tsx
import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Emergency } from "./Emergency";

const item = {
  access_id: "acc-1", emergency_id: "EMRG-1", doctor_id: "D-12",
  emergency_reason: "life_threatening", clinical_note: "trauma",
  review_status: "PENDING", dpo_deadline: new Date(Date.now() + 3600e3).toISOString(),
  overdue: false, created_at: new Date().toISOString(),
};

const getPending = vi.fn().mockResolvedValue({ pending: [item], total: 1 });
const review = vi.fn().mockResolvedValue({ status: "reviewed" });
vi.mock("../api/client", () => ({ api: { getEmergencyPending: () => getPending(), reviewEmergency: (...a: unknown[]) => review(...a) } }));

describe("Emergency", () => {
  it("lists pending overrides and submits a VERIFIED review", async () => {
    render(<Emergency />);
    expect(await screen.findByText("D-12")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /review/i }));
    await userEvent.click(await screen.findByRole("button", { name: /mark verified/i }));
    expect(review).toHaveBeenCalledWith("acc-1", "VERIFIED");
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd frontend/admin-dashboard && npx vitest run src/pages/Emergency.test.tsx`
Expected: FAIL — cannot resolve `./Emergency`.

- [ ] **Step 3: Implement `src/pages/Emergency.module.css`**

```css
.badge { padding: 2px 8px; border-radius: 999px; font-size: 12px; font-weight: 600; }
.overdue { background: #fde2e1; color: var(--status-danger); }
.ontime { background: #e7f2ec; color: var(--status-active); }
.actions { display: flex; gap: 10px; margin-top: 18px; justify-content: flex-end; }
.verify { background: var(--status-active); color: #fff; border: 0; padding: 9px 14px; border-radius: 6px; }
.flag { background: var(--status-danger); color: #fff; border: 0; padding: 9px 14px; border-radius: 6px; }
.review { border: 1px solid var(--border); background: var(--bg); padding: 6px 12px; border-radius: 6px; }
.note { color: var(--text-muted); font-size: 14px; }
```

- [ ] **Step 4: Implement `src/pages/Emergency.tsx`**

```tsx
import { useCallback, useEffect, useState } from "react";
import { api } from "../api/client";
import type { ReviewItem } from "../api/types";
import { DataTable, Column } from "../components/DataTable";
import { Modal } from "../components/Modal";
import styles from "./Emergency.module.css";

export function Emergency() {
  const [rows, setRows] = useState<ReviewItem[]>([]);
  const [selected, setSelected] = useState<ReviewItem | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    setError("");
    api.getEmergencyPending()
      .then((d) => setRows(d.pending))
      .catch((e) => setError(e instanceof Error ? e.message : "failed to load"));
  }, []);

  useEffect(() => { load(); }, [load]);

  const submit = async (decision: "VERIFIED" | "FLAGGED") => {
    if (!selected) return;
    setBusy(true);
    try {
      await api.reviewEmergency(selected.access_id, decision);
      setSelected(null);
      load();
    } catch (e) {
      setError(e instanceof Error ? e.message : "review failed");
    } finally {
      setBusy(false);
    }
  };

  const columns: Column<ReviewItem>[] = [
    { key: "doctor", header: "Doctor", render: (r) => r.doctor_id },
    { key: "reason", header: "Reason", render: (r) => r.emergency_reason },
    { key: "note", header: "Note", render: (r) => <span className={styles.note}>{r.clinical_note}</span> },
    { key: "deadline", header: "Deadline", render: (r) => (
      <span className={`${styles.badge} ${r.overdue ? styles.overdue : styles.ontime}`}>
        {r.overdue ? "Overdue" : new Date(r.dpo_deadline).toLocaleString()}
      </span>
    ) },
    { key: "action", header: "", render: (r) => (
      <button className={styles.review} onClick={() => setSelected(r)}>Review</button>
    ) },
  ];

  return (
    <div>
      <h2 style={{ marginTop: 0 }}>Emergency review queue</h2>
      {error && <p style={{ color: "var(--status-danger)" }}>{error}</p>}
      <DataTable columns={columns} rows={rows} empty="No pending emergency reviews." />

      <Modal open={selected !== null} title="Record review decision" onClose={() => setSelected(null)}>
        {selected && (
          <div>
            <p className={styles.note}>
              <b>{selected.doctor_id}</b> · {selected.emergency_reason}<br />
              {selected.clinical_note}
            </p>
            <div className={styles.actions}>
              <button className={styles.flag} disabled={busy} onClick={() => submit("FLAGGED")}>Flag</button>
              <button className={styles.verify} disabled={busy} onClick={() => submit("VERIFIED")}>Mark verified</button>
            </div>
          </div>
        )}
      </Modal>
    </div>
  );
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd frontend/admin-dashboard && npx vitest run src/pages/Emergency.test.tsx`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add frontend/admin-dashboard/src/pages/Emergency.*
git commit -m "feat(fe): emergency review queue with decision modal"
```

### Task 22: Router wiring + full verification

**Files:**
- Modify: `src/App.tsx`, `src/main.tsx`

**Interfaces:**
- Consumes: all pages, `AuthProvider`, `AppShell`, `ProtectedRoute`.
- Produces: the wired SPA — `/login` public; `/`, `/audit`, `/emergency` protected under `AppShell`.

- [ ] **Step 1: Implement `src/App.tsx`**

```tsx
import { BrowserRouter, Routes, Route } from "react-router-dom";
import { AuthProvider } from "./auth/AuthContext";
import { ProtectedRoute } from "./components/ProtectedRoute";
import { AppShell } from "./components/AppShell";
import { Login } from "./pages/Login";
import { Dashboard } from "./pages/Dashboard";
import { Audit } from "./pages/Audit";
import { Emergency } from "./pages/Emergency";

export default function App() {
  return (
    <AuthProvider>
      <BrowserRouter>
        <Routes>
          <Route path="/login" element={<Login />} />
          <Route element={<ProtectedRoute><AppShell /></ProtectedRoute>}>
            <Route path="/" element={<Dashboard />} />
            <Route path="/audit" element={<Audit />} />
            <Route path="/emergency" element={<Emergency />} />
          </Route>
        </Routes>
      </BrowserRouter>
    </AuthProvider>
  );
}
```

- [ ] **Step 2: Replace `src/main.tsx`** (tokens import + App).

```tsx
import React from "react";
import ReactDOM from "react-dom/client";
import "./styles/tokens.css";
import App from "./App";

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
```

- [ ] **Step 3: Delete Vite scaffolding leftovers** not used (`src/App.css`, `src/index.css`, `src/assets/react.svg` if referenced). Ensure no dangling imports.

Run: `cd frontend/admin-dashboard && npx tsc --noEmit`
Expected: no errors.

- [ ] **Step 4: Run the full frontend test suite**

Run: `cd frontend/admin-dashboard && npx vitest run`
Expected: all tests PASS (client, Login, Dashboard, Emergency).

- [ ] **Step 5: Full-stack manual verification** (all backend services + BFF running per `DOCKER.md`, admin seeded per Task 14).

```bash
cd frontend/admin-dashboard && npm run dev
```
Then in the browser at `http://localhost:5173`:
1. You are redirected to `/login`.
2. Sign in with `admin@testhospital.local` / `admin-dev-password` → lands on the Dashboard with tiles + charts.
3. Navigate to Audit → rows load, filter + pagination work.
4. Navigate to Emergency → pending queue loads; open Review → Mark verified → row clears on reload.
5. Log out → redirected to `/login`; hitting `/` again redirects back to `/login`.

Expected: every step works; the browser network tab shows only same-origin `/api/*` calls (no JWT or API key anywhere in the browser).

- [ ] **Step 6: Commit**

```bash
git add frontend/admin-dashboard/src/App.tsx frontend/admin-dashboard/src/main.tsx
git commit -m "feat(fe): wire router, auth, and app shell"
```

---

## Final integration checklist

- [ ] `cd consent-service && go test ./... && go test -tags=integration ./test/ -run TestGetStats` — green.
- [ ] `cd admin-bff && go build ./... && go vet ./... && go test ./...` — green.
- [ ] `cd frontend/admin-dashboard && npx tsc --noEmit && npx vitest run` — green.
- [ ] Migration `0012` applied; `seedadmin` created the admin.
- [ ] End-to-end browser flow (Task 22 Step 5) passes with real services.
- [ ] Browser DevTools confirm no hospital API key or JWT is ever present client-side — only the `admin_session` (HttpOnly) and `csrf_token` cookies.
- [ ] Update `plan-phase.md`: mark the `frontend/admin-dashboard/` row and the `GET /api/v1/consent/stats` gap as done.


