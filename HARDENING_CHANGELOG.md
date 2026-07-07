# Phase-1 Hardening Changelog

**Date:** 2026-07-05
**Scope:** Cross-cutting security/correctness fixes across `shared`, `auth-service`,
`audit-service`, `consent-service` (+ DB migrations). Applied before starting
Phase-2 services so new services inherit the corrected patterns.

Companion plan: `~/.claude/plans/create-a-plan-to-elegant-chipmunk.md`.

---

## Red flags fixed

### #1 — RLS context SQL injection (tenant-isolation keystone)
Hospital isolation rested on a string-interpolated `SET LOCAL app.hospital_id = '<id>'`.
- Replaced with parameterized `SELECT set_config('app.hospital_id', $1, true)` at all
  5 sites, via a `setHospitalContext` helper (validates the id is a UUID first).
- Files: `consent-service/pkg/consent/repository/repository.go`,
  `audit-service/pkg/audit/repository/repository.go`.
- **See "RLS was inert" below — the code fix alone was not sufficient.**

### #2 — Silently-dropped audit writes → transactional outbox
Audit events were fired over HTTP with the error discarded; a consent could commit
with no audit trail (the legal shield). Now:
- Audit event is written to `consent.audit_outbox` in the **same transaction** as the
  consent row. A background relay (`consent-service/pkg/consent/outbox/relay.go`,
  2s tick) ships rows to audit-service with retries; at-least-once, deduped by
  `event_id` (`ON CONFLICT DO NOTHING`).
- Consent-service no longer calls audit-service on the request hot path.
- Files: `consent-service` repository/service/outbox + `service/audit_client.go`
  (now a token-authenticated `AuditShipper`).

### #3 — Unauthenticated internal audit endpoint → service JWT
`/internal/audit/log` trusted any caller. Now:
- auth-service issues short-lived RS256 service tokens via `POST /v1/auth/service-token`
  (bootstrap secret `SERVICE_TOKEN_SECRET`, constant-time compared).
- `shared/middleware.InternalServiceAuth` verifies the token (`role=service`, no
  `hospital_id`) and guards audit-service's `/internal` group.
- `shared/serviceauth` caches the token; the relay attaches it.

### #4 — Raw mobile in URL → POST body
`GET /consent/check?mobile=...` leaked raw mobiles into access logs. Now
`POST /api/v1/consent/check` with the mobile in the JSON body.

### #6 — String-matched errors → sentinel errors
Handlers branched on `err.Error() == "..."`. Introduced
`service.ErrActiveConsentExists` / `ErrNoActiveConsent`, matched with `errors.Is`
(reusing auth-service's existing pattern). Duplicate capture → 409, withdraw-none → 404.

### #7 — Module hygiene
- Go version aligned to `go 1.25` in all 5 `go.mod` (were split 1.22/1.25).
- Dockerfiles rewritten to build from **repo-root context** so the
  `replace github.com/hiabhi-cpu/shared => ../shared` directive resolves
  (was `COPY . .` from the service dir — could never see `../shared`).

### #8 — Capture idempotency
Replays returned 409 instead of the original record (needed for offline-kiosk sync).
- Added `consent_vault.idempotency_key` + partial unique index on
  `(hospital_id, idempotency_key) WHERE type='CONSENT_GIVEN'`.
- Capture returns the existing record (200) on replay of the same `session_id`;
  new record → 201. Race-safe via the unique index + `ErrDuplicateIdempotency`.

### #5 — Per-purpose withdrawal (data-model redesign) — DONE
The model conflated record status with per-purpose status: withdrawal was
all-or-nothing and Check ignored purpose. Redesigned so consent is granular.
- `consent_vault.purposes` now means a **per-purpose status map**
  (`{"treatment":"ACTIVE","insurance":"WITHDRAWN"}`), not a flat array. The latest
  row is the current state; row `status` is the derived aggregate (ACTIVE if any
  purpose active). Append-only version chain unchanged.
- `Withdraw` takes `purposes` (empty = withdraw all): carries the map forward and
  flips only the targeted, currently-active purposes. Partial withdrawal keeps the
  row aggregate ACTIVE. Audit records the delta (`withdrawn_purposes`).
- `Check` requires a `purpose` (400 if missing); returns `no_consent` (never
  granted) vs `consent_withdrawn` (granted then withdrawn) — plan §11.
- `GetActiveByPatientKey` → `GetLatestByPatientKey` (returns latest row regardless
  of status; callers read the map). Withdrawal insert status is parameterized.
- Migration `11_purpose_status.sql` (doc/marker — column is already JSONB).
- Files: `consent-service` model/repository/queries/service/controller.
- **Scope decisions:** (1) re-granting a withdrawn purpose while another is active
  is deferred — a follow-up "renew/grant" op; Capture still 409s only while some
  purpose is active (re-capture after a *full* withdrawal works). (2) Check requires
  an explicit purpose.
- **Verified live:** capture `[treatment,insurance]` → both allowed; withdraw
  `[insurance]` → treatment still allowed, insurance `consent_withdrawn`, research
  `no_consent`; withdraw-all → both denied; withdraw-none-active → 404; check
  without purpose → 400. Version chain v1 GIVEN → v2 partial (aggregate ACTIVE) →
  v3 full (WITHDRAWN), all chained via `previous_id`.

### Grant / renew operation (follow-up to #5) — DONE
Re-granting a withdrawn purpose (or adding a new one) was impossible: Capture 409s
while any purpose is active and always starts a new root row (`version=1`,
`previous_id=NULL`), so it can't extend a chain. Added a Grant op — the mirror of
Withdraw.
- `POST /api/v1/consent/grant` `{mobile, session_id, purposes[]}` → appends a
  `CONSENT_RENEWAL` row: carries the map forward, flips requested purposes to
  ACTIVE, `version+1`, `previous_id` = latest. Handles both re-grant and add-new.
- Requires an existing chain (`ErrNoConsentToRenew` → 404 if none; first consent
  still goes through Capture). Capture's "409 if any purpose active" is unchanged.
- **Idempotency = option A (no-op guard):** if every requested purpose is already
  active, no row is written and the current state is returned (200). A new row →
  201. Repeated grants are therefore safe without an extra unique index.
- Repo: shared `insertChainedRow` helper now backs both `InsertWithdrawn` and
  `InsertRenewal` (identical column shape; only row type differs).
- Files: `consent-service` model/queries/repository/service/controller/routes.
- **Verified live:** capture `[treatment,insurance]` → withdraw `insurance` →
  **grant `insurance` back (201, type CONSENT_RENEWAL)** → both active; repeat
  grant → 200 no-op (no new version); grant new `research` → 201; grant onto
  unknown patient → 404. One continuous chain v1 GIVEN → v2 WITHDRAWAL → v3/v4
  RENEWAL, contiguous versions, no fork; audit `CONSENT_GRANTED` recorded
  `granted_purposes`.
- **Still deferred:** hard replay idempotency for renewals (option B — a unique
  index covering renewal `session_id`); not needed given the no-op guard.

### Check by `hms_patient_id` (plan §11) — DONE
The doctor/HMS access path is meant to check consent by the hospital's opaque HMS
patient ID (non-PII), but `hms_patient_id` was never persisted, so it was unusable.
- Capture now accepts + stores `hms_patient_id` (optional — a kiosk without HMS
  integration may omit it). It is carried forward onto withdrawal/renewal rows so
  the latest row is always resolvable by HMS ID.
- Check accepts EITHER `hms_patient_id` (HMS path) OR `mobile` (kiosk/portal path)
  plus the required `purpose`; the handler enforces exactly one. HMS lookup uses
  the existing `(hospital_id, hms_patient_id)` index; no schema migration (the
  column already existed in `03_consent_vault.sql`).
- Repo refactor: NULL-safe `scanConsentRow` + a shared `getOneConsent` helper now
  back all single-row getters, plus a new `GetLatestByHMSPatientID`.
- Files: `consent-service` model/queries/repository/service/controller.
- **Verified live (rebuilt containerized consent-service):** capture with an
  `hms_patient_id` → check-by-HMS treatment/insurance allowed, unknown HMS id
  `no_consent`; withdraw insurance → check-by-HMS treatment still allowed,
  insurance `consent_withdrawn` (proving carry-forward: `hms_patient_id` present on
  both the GIVEN and WITHDRAWAL rows, so HMS lookup resolves the latest state);
  check-by-mobile returns the same consent_id (both paths agree); capture without
  an HMS id still works (backward compatible); both-ids / neither-id → 400.

### Dockerfile dependency-layer caching
All four Dockerfiles now COPY `go.mod`/`go.sum` (this module + the replaced
`shared`) and run `go mod download` BEFORE copying source, so code-only changes
rebuild without re-downloading modules (offline-friendly once the module layer is
cached).

> ⚠️ Deploy note: the consent-service **container image** was not rebuilt in this
> session — the build sandbox had no outbound network for `go mod download`. The
> code is complete and verified via a host run; the running container still holds
> the previous image. Deploy with (when network is available):
> `cd consent-service && docker compose up -d --build`.

---

## Latent bugs found while fixing #2 (audit had NEVER persisted)

The dropped-audit-error (#2) had been masking three hard failures — audit writes were
failing silently the whole time:

1. **Wrong table name.** Audit INSERT/SELECT targeted `audit.events`; the table is
   `audit.audit_log`. Every write errored. Fixed in
   `audit-service/pkg/audit/repository/queries.go`.
2. **Invalid event types.** Service emitted `CONSENT_CAPTURED` / `CONSENT_CHECKED`,
   which are not in the `audit_log.event_type` CHECK constraint. Remapped to valid
   values: `CONSENT_GRANTED`, `DATA_ACCESSED`, `CONSENT_MISSING_ACCESS_ATTEMPT`,
   `CONSENT_WITHDRAWN`.
3. **Raw mobile as `actor_id`.** Capture/withdraw wrote `req.Mobile` (raw PII) into
   the audit `actor_id`. Changed to the hashed `patient_key`.

Also robustness: audit repo now parses `ip_address` to `netip.Addr` before insert
(the `INET` column) instead of passing a bare string.

Separately, an earlier fix (pre-changelog) had left a compile break: `crypto_test.go`
still expected `HashAPIKey` to return `(hash, err)` after the bcrypt→SHA-256 refactor.
Corrected.

---

## RLS was inert — least-privilege DB role added

**Finding:** PostgreSQL skips ALL row-level security for **superusers** and roles with
**`BYPASSRLS`**. The services connected as `abhi` (superuser + BYPASSRLS), so the
`FORCE ROW LEVEL SECURITY` policies on `consent_vault`/`audit_log` were never consulted
— the hospital data silo (Red Line 3) was not DB-enforced. Verified: a query under a
bogus `app.hospital_id` returned all rows.

**Fix:** `scripts/db/init/10_app_role.sql` creates `dpdp_app`
(`NOSUPERUSER NOBYPASSRLS`, non-owner) with least-privilege grants:
- `auth.hospitals`: SELECT only
- `consent.consent_vault`: SELECT, INSERT (no UPDATE/DELETE → 3rd append-only backstop)
- `consent.audit_outbox`: SELECT, INSERT, UPDATE (relay marks shipped)
- `audit.audit_log`: SELECT, INSERT + sequence USAGE (no UPDATE/DELETE)

`DATABASE_URL` now uses `dpdp_app`; the superuser is admin-only (compose + migrations).

**Proof (as `dpdp_app`):** bogus hospital context → 0 rows; correct context → 5 rows;
`UPDATE consent_vault` / `DELETE audit_log` → permission denied; full app flow passes
with no permission errors.

> ⚠️ RLS policies use `current_setting('app.hospital_id')::uuid` **without**
> `missing_ok`, so a query that forgets to set the context **errors** (fail-closed)
> rather than leaking. Any new DB path must call `setHospitalContext` first.

---

## DB migrations added (ordered init scripts)

| File | Purpose |
|------|---------|
| `07_audit_log_event_id.sql` | `event_id` + unique index (idempotent audit ingest) |
| `08_audit_outbox.sql` | `consent.audit_outbox` transactional outbox table |
| `09_consent_idempotency.sql` | `idempotency_key` + partial unique index |
| `10_app_role.sql` | least-privilege `dpdp_app` role + grants |
| `11_purpose_status.sql` | per-purpose status map (doc/marker; column already JSONB) |

> **Superseded by tracked migrations (2026-07-06).** The run-once `init/` scripts
> were retired in favour of `DPDP/scripts/db/migrations/` (0001–0010) +
> `public.schema_migrations`, applied by `DPDP/scripts/db/migrate.sh`. Old volumes
> are adopted with `migrate.sh baseline 0010`. See the section below and
> `DPDP/scripts/db/README.md`.

---

## Migration tooling — init scripts → tracked migrations (2026-07-06)

**Problem:** `scripts/db/init/*.sql` ran only once, on an empty Docker volume
(`/docker-entrypoint-initdb.d` semantics) — a bootstrap, not a migration system.
It could not evolve a live DB (e.g. RDS with pilot data) without a wipe, and
silently drifted as running DBs were hand-patched (the stale `api_key_hash` and
the "01–11 don't re-run" issues both trace here).

**Fix:**
- `DPDP/scripts/db/migrate.sh` — dependency-free psql runner (POSIX sh; runs on
  the host and in the `postgres:16-alpine` one-shot). Tracks applied versions in
  `public.schema_migrations`. Commands: `up`, `status`, `baseline <version>`,
  `seed`. Each migration + its bookkeeping insert run in one `--single-transaction`.
- `DPDP/scripts/db/migrations/` — the 11 init scripts renumbered contiguously to
  `0001…0010` (content unchanged, tool-agnostic plain SQL). The dev seed
  (`06_seed_test_hospital`) is **split out** to `seeds/dev_seed_test_hospital.sql`
  so it can never run against a real database.
- `init/` deleted (single source of truth). `DPDP/docker-compose.yml` no longer
  mounts `initdb.d`; a one-shot `migrate` service runs `up` then `seed` after
  Postgres is healthy. `DOCKER.md` bootstrap updated to call `migrate.sh`.
- Migrations run as the **admin** DSN (DDL + `CREATE ROLE`); the app stays on
  `dpdp_app`. Migrations are decoupled from service startup (no N-task race).
- **Verified:** clean from-scratch apply on a throwaway DB (all 10 in order →
  4 schemas, 5 tables, RLS forced, `dpdp_app` cannot UPDATE the vault, 10 rows in
  `schema_migrations`), idempotent re-run is a no-op, and `baseline 0010` adopts
  the existing running volume without re-running (which would error on duplicate
  policies/triggers).
- Files/docs: `DPDP/scripts/db/{migrate.sh,migrations/,seeds/,README.md}`,
  `DPDP/docker-compose.yml`, `DOCKER.md`, and the new top-level `deploy.md`.

---

## emergency-service — DPDP §7(b) emergency access + DPO review queue (2026-07-06)

First Phase-2 service (product plan §12). Built on the hardened patterns so it
inherited them for free (RLS context helper, transactional outbox + relay,
service-JWT audit auth, sentinel errors). Runs on port **9005**.

**Endpoints (hospital JWT):**
- `POST /api/v1/consent/emergency-override` — **never blocks**; a well-formed
  request always returns `{allowed:true, emergency_id, access_id}` (201). Only a
  malformed request is rejected (missing fields → 400, invalid reason → 400).
- `GET /api/v1/emergency/pending` — DPO review queue (`overdue` derived:
  `PENDING AND dpo_deadline < now()`).
- `POST /api/v1/emergency/:id/review` — DPO decision `VERIFIED`/`FLAGGED`; a
  second review of the same access → 404 (no-op guard).

**Data model (migration `0011_emergency.sql`):**
- The **immutable** access record is a `consent_vault` row (`EMERGENCY_OVERRIDE`,
  `legal_basis=DPDP_SECTION_7B`, `status=PENDING_RETROSPECTIVE`) — legal evidence,
  never mutated.
- `consent_vault.patient_key` relaxed to **NULLABLE**: an unconscious/unidentified
  patient still gets access, recorded without a key. (Isolation suite re-run — no
  regression.)
- The **mutable** DPO workflow lives in a new `emergency` schema
  (`emergency.reviews`, RLS-isolated per hospital) — because `consent_vault` is
  append-only, the schema author's `dpo_review_*`-on-vault intent is impossible;
  the review state machine needs its own table.
- Own `emergency.audit_outbox` + relay (mirrors consent-service) so its audit
  trail is durable and independent of consent-service.

**Verified live (host binary vs containerized infra):** override with known and
with **unknown** identity → both 201 (vault `patient_key` NULL for the unknown
one); invalid reason → 400 (never a block); pending queue lists both with 72h
deadlines; `VERIFIED` review → 200 then re-review → 404; audit relay shipped
`EMERGENCY_ACCESS` ×2 + `DPO_REVIEW_COMPLETED` to `audit_log` (outbox drained);
RLS on `emergency.reviews` returns 0 under a bogus hospital context.

**Deferred (follow-ups):** patient-notification SMS (§12.5), retrospective
consent linkage (§12.6), auto-escalation email on >72h overdue, and the DPO
**dashboard UI** (Phase 3). `escalation_sent_at`/`patient_notified_at` columns are
already in `emergency.reviews` as hooks.

Files: `emergency-service/**` (new module), `DPDP/scripts/db/migrations/0011_emergency.sql`.

---

## Verification performed (live stack)

- `go build` / `go vet` / `go test` clean across all 5 modules.
- End-to-end: Token → Capture (201) → replay (200, same id) → dup/new-session (409) →
  Check via POST (allowed) → Withdraw (200) → Check (denied) → withdraw-none (404).
- #2 durability: capture with audit-service **down** still 201, event queued
  (pending, retrying); on restart the relay shipped it **exactly once**; forced
  re-ship of a shipped row left `audit_log` count unchanged (ON CONFLICT dedupe).
- #3: `/internal/audit/log` → 401 (no token / hospital token / wrong secret),
  processes with a valid service token.
- RLS isolation + append-only denial proven under `dpdp_app` (see above).

---

## Follow-ups / deferred

- **mTLS** for internal traffic — service JWT is the MVP hardening.
- **Repo layout drift — RESOLVED (per-service compose).** The `services/…`-based
  `Makefile` and the common `DPDP/docker-compose.yml` were both removed. Each
  service now has its **own** `docker-compose.yml` (polyrepo style), built from
  repo-root context (for `replace ../shared`), connecting as `dpdp_app`, wired by
  service-name DNS over a shared **external** `dpdp-network`. Postgres + Redis are
  external infra (provisioned independently; managed DB in prod) — bootstrap and
  run instructions in `DOCKER.md`. Keys/secrets mounted read-only; `.dockerignore`
  keeps them out of images. `.env` JWT/secret paths corrected. Verified: all four
  services up from their own composes → capture/check + cross-compose audit relay
  (token from auth-service, ship to audit-service) working over the external net.
  (Key generation, previously `make gen-keys`, is in `DOCKER.md`; `go mod tidy` is
  now per-module.)
- **Stale-volume note** — an existing DB volume seeded before the bcrypt→SHA-256
  refactor holds a bcrypt `api_key_hash`; token issuance 401s until the row is updated
  to the SHA-256 value (already correct in `06_seed_test_hospital.sql` for fresh volumes).
