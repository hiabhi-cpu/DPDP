# DPDP Consent Manager — Phased Plan (corrected + status)

**Last updated:** 2026-07-07
**Legend:** ✅ done · 🟡 partial / in progress · ⬜ not started · 🔺 resequenced from the original plan (moved earlier — see note) · ➕ gap item added 2026-07-07 (full-project review — see note)

> **What changed from the original phase map.** The macro-phasing (Foundation →
> Integrations → Pilot → Revenue → Scale) is right and unchanged. The corrections are
> all *within-phase pull-forwards*: the original plan consistently scheduled the
> **accountability/compliance machinery** (emergency review, breach notification,
> isolation tests, migration tooling) one beat *behind* the feature that creates the
> legal liability. In a DPDP product that is the dangerous direction to be late in, so
> those items are pulled forward here. Items marked 🔺 moved earlier than the source doc.

> **Gap review (2026-07-07).** A full-project review against the DPDP Act and the actual
> code found items the plan never mentioned. The largest are **legal, not technical**:
> §9 children/guardian consent, retention & erasure, grievance redressal (§13),
> nomination (§14), notice languages (§5(3)), DPIA. Code-verified security gaps: OTP
> endpoints have **no attempt/rate limiting**, and emergency-review `ReviewerID` is
> **client-supplied free text** (no named users anywhere). Operational: no backups/DR
> plan, observability only at P4, consent-check fail-open/fail-closed undefined, kiosk
> device identity unsolved. Rows marked ➕ below were added for these.
>
> **Version control (resolved 2026-07-07):** the project was seven single-commit
> per-service repos with the root docs untracked and cross-repo `replace ../shared`
> coupling. **Consolidated into a single monorepo at the root** — one history, root
> `.gitignore` + `go.work`; old nested `.git` dirs backed up before removal.

---

## Cross-cutting (Phase-1 hardening) — ✅ COMPLETE

Done before Phase-2 so new services inherit corrected patterns. Full detail in
`HARDENING_CHANGELOG.md`.

- ✅ #1 RLS context parameterized (`set_config`, UUID-validated) — tenant-isolation keystone
- ✅ Least-privilege `dpdp_app` DB role (`NOSUPERUSER NOBYPASSRLS`) — RLS now actually enforced
- ✅ #2 Transactional outbox for audit + background relay (at-least-once, `ON CONFLICT` dedupe)
- ✅ #3 Service-JWT internal auth (`/v1/auth/service-token`, `InternalServiceAuth`, cached client)
- ✅ #4 Raw mobile out of URL → POST body
- ✅ #5 Per-purpose consent (status map) + Grant/renew op
- ✅ #6 Sentinel errors (`errors.Is`, not string match)
- ✅ #7 Module hygiene (go 1.25 aligned; Dockerfiles build from repo-root context)
- ✅ #8 Capture idempotency (`session_id` → returns original on replay)
- ✅ Check by `hms_patient_id` (opaque HMS access path)
- ✅ Per-service `docker-compose.yml` + external infra (`dpdp-network`); `DOCKER.md`
- ✅ Versioned `v1|` patient-key prefix (makes Phase-5 key rotation possible)

---

## Phase 1 · Foundation — 🟡 backend done, frontend not started

**Goal:** Full consent flow working locally with a test hospital.

| Status | Tag | Task |
|---|---|---|
| ✅ | DB | consent_vault + audit_log with RLS + append-only (no update/delete) — now tracked migrations in `DPDP/scripts/db/migrations/` |
| ✅ | infra | Local Postgres + Redis stack (now per-service compose + external infra) |
| ✅ | BE | `auth-service` — JWT RS256 + hospital API-key hashing (bcrypt→**SHA-256**, see note) |
| ✅ | BE | `consent-service` — capture / check / withdraw (+ grant, per-purpose) |
| ✅ | BE | `audit-service` — append-only logger, RLS enforced, idempotent ingest |
| ✅ | BE | `notification-service` — OTP send+verify, Redis TTL store |
| 🟡 | infra | Secrets: `SYSTEM_SALT` + per-hospital key — **local/env mock only**; AWS Secrets Manager not wired |
| ✅ | BE | Patient-key HMAC util in `shared/crypto` (double-keyed, `v1|` versioned) |
| ⬜ | FE | `frontend/admin-dashboard/` — React+TS, consent stats + audit view. **APIs:** auth `POST /v1/auth/token` (login), audit `GET /api/v1/audit/logs` (view — exists), consent `GET /api/v1/consent/stats` (**does not exist — must build**). See the endpoint map below. |
| ⬜ | mobile | `frontend/kiosk/` — React Native Expo, online OTP + consent form |
| ✅🔺 | test | **Tenant-isolation test suite** — pulled forward from Phase 3, now written & green (`consent-service/test/`, 6 cases: read isolation, fail-closed, bogus context, append-only ×2, role-not-privileged). Build-tagged `integration`; run via `test/run-isolation.sh`. **CI wiring still pending (Phase 3).** |
| ✅➕ | infra | **Monorepo consolidation** — was 7 single-commit per-service repos + untracked root docs, with `replace ../shared` coupling across repo boundaries. Now one root repo (`main`), root `.gitignore` + `go.work`; nested `.git` dirs backed up then removed. One CI pipeline in P3 instead of seven. |
| ⬜➕ | BE | **OTP abuse protection** in notification-service — per-mobile + per-hospital attempt limits, verify lockout, resend cooldown (Redis counters). Code has none today; blocks kiosk going public (OTP brute-force + SMS-pumping cost attack). |
| ⬜➕ | DB | **§9 schema groundwork** — vault columns for `data_principal_type` (ADULT / CHILD / GUARDIAN_CONSENT), guardian identity + relationship, and which mobile received the OTP. Migration is cheap now, painful after pilot data exists. Kiosk guardian *flow* is P2; hard gate before P3 real patients. |
| ✅🔺 | infra | **Migration tooling** — pulled forward, done. `init/` retired → tracked migrations in `DPDP/scripts/db/migrations/` (0001–0010) + `public.schema_migrations`, run by `migrate.sh` (up/status/baseline/seed). Dev seed split out of the schema. Compose applies via a one-shot `migrate` service. Files are tool-agnostic SQL (goose swap-in later is mechanical). Verified: clean from-scratch apply + baseline of the existing volume. |

> **Note — API-key hashing.** The original plan says bcrypt; the build uses SHA-256
> (fast, constant-time compare) for the hospital API key. Intentional — update the plan
> language, not the code.

### admin-dashboard — API endpoints it consumes

All hospital-facing calls send the **hospital JWT** (from `POST /v1/auth/token`) as
`Authorization: Bearer <jwt>`; each service scopes results to that hospital via RLS.
Ports: auth `9006` · consent `9000` · audit `9001` · emergency `9005`.

| Dashboard screen | Method + path | Service | Status |
|---|---|---|---|
| Login / session | `POST /v1/auth/token` (`{api_key}` → JWT) | auth | ✅ exists |
| Audit log view (paginated, hospital-scoped) | `GET /api/v1/audit/logs?page=&limit=` | audit | ✅ exists (`GetLogs`) |
| Consent stats (active vs withdrawn, checks allowed/denied, emergency count) | `GET /api/v1/consent/stats` | consent | ⬜ **must build** |
| DPO: emergency review queue (+ overdue flag) | `GET /api/v1/emergency/pending` | emergency | ✅ exists |
| DPO: record a review decision | `POST /api/v1/emergency/:id/review` | emergency | ✅ exists |
| Compliance health score | (aggregate) | consent/audit | ⬜ future (derive, or extend `/stats`) |

**Gaps to close before/with the dashboard:**
- **`GET /api/v1/consent/stats`** — the "consent stats" half of the Phase-1
  dashboard has **no endpoint today**. consent-service exposes only
  capture/check/withdraw/grant (all `POST`, patient-scoped) — nothing that returns
  hospital-level counts. Build a read-only aggregate (counts over `consent_vault`
  under RLS: active/withdrawn per purpose, captures & checks in a window,
  allowed-vs-denied). Small addition; pairs naturally with building the dashboard.
- **Audit filters** — confirm/extend `GET /api/v1/audit/logs` to accept
  `event_type` and date-range params (today it paginates via `page`/`limit`); the
  audit view will want to filter by event type and time.
- **Consent browse/detail** — there is no "list/search consents" endpoint (only
  check-by-mobile / check-by-HMS). If the dashboard needs a per-patient drill-down
  beyond the audit trail, that's an additional consent-service endpoint.
- **CORS + auth for browser** — services are currently service-to-service/JWT with
  no CORS config; a browser SPA needs CORS allow-listing and a browser-appropriate
  login flow (the API-key `POST /v1/auth/token` is fine for a trusted admin login,
  but the raw hospital API key must not ship in frontend code — proxy or exchange it).
- **➕ Named users** — login today yields a *hospital-level* JWT; there is no concept of
  an individual admin/DPO user anywhere. Design the dashboard's auth around per-user
  accounts from the start (even if a single admin initially) so P3 RBAC + reviewer
  attribution slot in without a rewrite.

**Phase-1 done when:** consent captured → vault written → audit logged → badge in dashboard.
**Backend half is met**; the two frontends + the pulled-forward test/migration items remain.

---

## Phase 2 · Integrations + Offline — ⬜ not started

**Goal:** Works with real HMS; works offline on bad WiFi.

| Status | Tag | Task |
|---|---|---|
| ⬜ | BE | `integration-service` — generic webhook receiver (`POST /webhook/patient-registered`), **mTLS** |
| ⬜ | BE | Bahmni adapter — map Bahmni patient payload → our schema |
| ⬜ | mobile | Kiosk offline mode — WatermelonDB SQLite queue, idempotency keys, auto-sync on reconnect (server side already idempotent via #8) |
| ⬜ | FE | `frontend/hms-widget/` — vanilla JS <50KB, PostMessage, green/yellow/red badge |
| ✅🔺 | BE | `emergency-service` — `POST /v1/consent/emergency-override` (always allowed, never blocks), immutable `EMERGENCY_OVERRIDE` vault row (§7(b), `legal_basis=DPDP_SECTION_7B`, identity optional), own transactional outbox+relay. Steps 5–6 (patient-notify SMS, retrospective consent) deferred. |
| ✅🔺 | BE | **Minimal DPO review queue** (API) — `GET /v1/emergency/pending` (overdue derived) + `POST /v1/emergency/:id/review` (VERIFIED/FLAGGED, already-reviewed→404). Mutable `emergency.reviews` (RLS-isolated). Coupled with the override per fix ①. **DPO dashboard UI still Phase 3.** |
| ⬜ | BE | WhatsApp Business API in notification-service (consent + withdrawal) |
| ⬜ | BE | Bypass-detection job — nightly HMS-access vs consent-check diff → `BYPASS_DETECTED` (see fix ③ — confirm HMS emits access telemetry) |
| ⬜ | mobile | i18next in kiosk — Hindi, Marathi, Kannada, Tamil |
| 🔺 | BE | **Breach-notification flow (72h DPBI countdown + draft + bulk SMS)** — pulled forward from Phase 3; must be operational *before* the first real patient record lands in Phase 3 |
| ⬜➕ | product | **Consent-check availability semantics** — decide fail-open vs fail-closed for the HMS badge when consent-service is unreachable; document the policy and build it into the widget/HMS contract. Must be decided *before* `hms-widget` ships — it's a compliance posture, not an implementation detail. |
| ⬜➕ | mobile | **Kiosk §9 guardian flow** — DOB/age gate in the consent form → guardian identity + relationship capture; OTP goes to the guardian's mobile for minors. Uses the P1 schema groundwork; hard gate before P3 real patients. |
| ⬜➕ | mobile | **Kiosk device identity** — per-device provisioning (enrollment token → device credential); the hospital API key must not ship in the Expo bundle (same exposure the dashboard note flags, worse on a physical kiosk). |
| ⬜➕ | FE | **Notice-text language pack (§5(3))** — the consent *notice text* (not the full UI) in the 8th-Schedule languages needed for pilot-region patients, by end of P2. Full 22-language UI stays P4; `notice_version`+`language` per row already exist in the vault. |

**Fixes applied vs original plan:**
- **① emergency-service + DPO review queue ship together.** Original put the override in
  Phase 2 and its reviewer in Phase 3 → a whole phase of unreviewed break-glass access
  (the exact DPDP failure mode). Coupled here.
- **② breach notification moved to end of Phase 2 / gate for Phase-3 go-live.** It must
  be live the day real patient data exists, not built during the same phase.
- **③ bypass-detection data-source check.** The nightly diff needs an HMS *access* feed;
  integration-service only receives *registration* webhooks. Confirm the HMS actually
  emits access events, or this job has nothing to diff.

**Phase-2 done when:** Bahmni fires webhook → kiosk captures offline → syncs → badge in HMS.

---

## Phase 3 · Pilot — 3 Hospitals — ⬜ not started

**Goal:** Real patients, real data, 3 free hospitals live.

| Status | Tag | Task |
|---|---|---|
| ⬜ | FE | `frontend/patient-portal/` — hospital-branded per-subdomain, OTP login, consent view + withdrawal |
| ⬜ | FE | DPO dashboard (full) — emergency review queue, 72h deadline alerts, compliance health score. **APIs:** emergency `GET /api/v1/emergency/pending` + `POST /api/v1/emergency/:id/review` (both exist); deadline alerts use the `overdue`/`dpo_deadline` fields already returned; health score needs an aggregate endpoint (see map above). |
| ⬜ | BE | `report-service` — consent PDF receipt, monthly compliance report (artifact hash already captured from Phase 1) |
| ⬜ | BE | `withdrawal-service` — self-service withdrawal + correction/deletion requests |
| — | — | ~~Breach notification~~ — **moved to Phase 2** (fix ②) |
| ⬜ | infra | Deploy to **AWS ap-south-1** — ECS Fargate + RDS Mumbai + ElastiCache + S3 (**NON-NEGOTIABLE region**) |
| ⬜ | infra | GitHub Actions CI/CD → ECR → ECS; **tenant-isolation suite runs on every deploy** (suite itself now built in Phase 1) |
| ⬜ | BE | Hospital onboarding script — provision hospital_id, API key, Secrets Manager entry, subdomain |
| 🔺 | infra | Wire **AWS Secrets Manager** (finishes the Phase-1 🟡 mock) — real per-hospital keys + `SYSTEM_SALT` in the region |
| ⬜➕ | BE | **Retention & erasure machinery** — retention policy per data class; purge job (expired OTP sessions + mobile numbers already indexed for it in `0005`); erasure-request handling reconciled with the append-only vault/audit design. *Design before P3 build starts* — "we never delete anything" collides with §12 rights and the draft-Rules erasure timelines. |
| ⬜➕ | FE | **Grievance redressal (§13)** — grievance submission + status tracking in patient-portal; grievance-officer queue in the DPO dashboard. Legally required before real patients. |
| ⬜➕ | BE | **Named users + RBAC** — per-user accounts (admin / DPO / staff) under each hospital; emergency-review `ReviewedBy` taken from the authenticated user's JWT (today `ReviewerID` is client-supplied free text — unverifiable accountability). |
| ⬜➕ | infra | **Backups + DR** — RDS automated backups **plus a tested restore**, documented RPO/RTO. The consent vault is legal evidence; an untested backup is not a backup. |
| ⬜➕ | infra | **Observability baseline** — structured logs, error alerting, consent-check latency metrics/SLO. Ships *with* the AWS deploy, not P4; pilot hospitals must not discover outages before we do. |
| ⬜➕ | compliance | **DPIA + basic security assessment** — hospital-scale health data ⇒ likely Significant Data Fiduciary (DPIA + audit obligations); plus a basic pen test. Both are go-live gates alongside breach notification, not P5/SOC 2 work. |

**Phase-3 done when:** 3 hospitals live, DPO reviewing emergency access, real consents flowing.

---

## Phase 4 · Revenue — 10 Paying Hospitals — ⬜ not started

**Goal:** ₹15–25L ARR, self-serve onboarding.

| Status | Tag | Task |
|---|---|---|
| ⬜ | FE | Self-serve onboarding UI — signup → API key → webhook configured → live in 1 day |
| ⬜ | BE | Razorpay billing — monthly/annual subscription + plan-activation webhook |
| ⬜ | BE | eHospital + Practo adapters in integration-service (pilot feedback) |
| ⬜ | mobile | Full 22-language consent forms — complete i18next pack |
| ⬜ | FE | Monthly compliance PDF — hospital branding, NABH-ready |
| ⬜ | infra | SLA monitoring — CloudWatch 99.9% uptime alert for consent-check, SNS alerting |
| ⬜➕ | FE | **Nomination (§14)** — patient portal: nominate a person to exercise the patient's rights on death or incapacity. Deferred past pilot deliberately; must not be forgotten. |

**Phase-4 done when:** 10 paying hospitals, Razorpay running, self-serve works.

---

## Phase 5 · Scale — 50+ Hospitals — ⬜ not started

**Goal:** Association endorsement, Series A data, SOC 2.

| Status | Tag | Task |
|---|---|---|
| ⬜ | BE | White-label API for MocDoc / eHospital partnership |
| ⬜ | infra | SOC 2 Type II prep — evidence collection, anomaly detection, access reviews |
| ⬜ | mobile | Patient mobile app — React Native (iOS + Android) |
| ⬜ | infra | DPDP Consent Manager registration prep (Nov 2026) — ₹2Cr net worth, DPBI application |
| ⬜ | BE | Key rotation — lazy migration + emergency rotation (**enabled by** the Phase-1 `v1|` key versioning) |

**Phase-5 done when:** 50 hospitals, HMS partnership live, SOC 2 in progress.

---

## Summary of resequencing (why each moved)

| Item | Was | Now | Reason |
|---|---|---|---|
| DPO review queue (minimal) | P3 | P2 | Emergency override must be reviewed as soon as it can be created |
| Breach notification (72h) | P3 | P2 | Must be live before the first real patient record (P3) |
| Tenant-isolation test suite | P3 (CI only) | P1 | Product's #1 correctness property; build the test, not just the gate |
| Migration tooling | (implicit init scripts) | P1 | Init scripts can't evolve a live schema across 3 pilot hospitals |
| AWS Secrets Manager | P1 (implied) | finished P3 | Realistically lands with the AWS deploy; P1 stays on a mock |

## Gap additions (2026-07-07) — index

From a full-project review against the DPDP Act and the code. Rows marked ➕ above.

| Gap | Phase | Why that phase |
|---|---|---|
| Monorepo consolidation — ✅ done | P1 (now) | Was 7 disconnected single-commit repos + untracked docs; everything downstream assumes one history |
| OTP abuse protection | P1 | Verified absent in code; blocks kiosk exposure |
| §9 children/guardian — schema | P1 | Cheap before data exists, painful after |
| Named-user login design (dashboard) | P1 | Avoid auth rewrite when P3 RBAC lands |
| Consent-check fail-open/closed policy | P2 | Must precede hms-widget |
| §9 guardian flow (kiosk) | P2 | Hard gate before real patients |
| Kiosk device identity | P2 | API key can't ship in the app bundle |
| Notice-text languages (§5(3)) | P2 | Pilot patients must get notice they understand |
| Retention & erasure | P3 (design earlier) | §12 rights + Rules timelines vs append-only design |
| Grievance redressal (§13) | P3 | Required channel before real patients |
| Named users + RBAC | P3 | Reviewer attribution is free text today |
| Backups + DR (tested restore) | P3 | Vault is legal evidence |
| Observability baseline | P3 | Was P4; pilot can't run blind |
| DPIA + security assessment | P3 gate | Likely-SDF obligations; health data at go-live |
| Nomination (§14) | P4 | Deliberate deferral, tracked so it isn't lost |

## Not-yet-started, highest-leverage next steps

1. ~~**Tenant-isolation test suite**~~ (P1 🔺) — ✅ done (`consent-service/test/`); CI wiring remains for Phase 3.
2. ~~**Migration tooling**~~ (P1 🔺) — ✅ done (`DPDP/scripts/db/migrate.sh` + tracked migrations); see `DPDP/scripts/db/README.md` and `deploy.md`.
3. ~~**`emergency-service` + minimal DPO review queue**~~ (P2, coupled) — ✅ done
   (override + pending + review APIs, verified live). Remaining emergency work:
   patient-notification SMS (§12.5) + retrospective consent (§12.6), and the DPO
   **dashboard UI** (Phase 3).
4. ~~**➕ Monorepo consolidation**~~ — ✅ done (2026-07-07): single root repo,
   root `.gitignore` + `go.work`; unblocks worktrees, review workflow, single CI.
5. **First frontend** (`admin-dashboard`) — nothing is currently visible to a
   hospital. Audit view can call the existing `GET /api/v1/audit/logs`, but the
   consent-stats screen needs a new `GET /api/v1/consent/stats` first (see the
   endpoint map in Phase 1). Design its login for named users (➕ note above).
6. **➕ OTP abuse protection** — small, self-contained, closes a real attack
   surface before any kiosk work makes the OTP endpoints patient-facing.
7. **➕ §9 schema migration** — one migration file now saves a pilot-data
   migration later; the kiosk guardian flow (P2) depends on it.
