# Admin Dashboard — Design Spec

**Date:** 2026-07-08
**Status:** Approved (brainstorming) — pending implementation plan
**Phase:** Phase 1 · Foundation (`plan-phase.md`, first frontend)

## Purpose

Give a hospital its first visible surface into the DPDP consent system: an admin
can log in, see consent statistics, browse the audit log, and (as DPO) review
emergency-override access. Today nothing is visible to a hospital — the backend
consent flow works end to end but has no UI.

Scope for this build:

1. Admin **login** (per-user accounts).
2. **Consent stats** dashboard.
3. **Audit log** view (paginated, filterable).
4. **Emergency review** queue (pending overrides + record a decision).

Plus one backend gap it depends on: a new read-only `GET /api/v1/consent/stats`.

## Decisions (locked during brainstorming)

| Decision | Choice | Why |
|---|---|---|
| Auth model | **Go BFF proxy** | Hospital API key must never ship in browser code; per-user login required from the start (`plan-phase.md` notes). BFF keeps the key server-side, presents per-user login, and removes the need for CORS on domain services. |
| Screens | Login + Stats + Audit + **Emergency review** | The emergency `pending`/`review` endpoints already exist, so pulling that DPO slice forward is cheap. |
| Admin user store | **`auth.admin_users` Postgres table**, seeded with one admin | Real accounts, ready for Phase-3 RBAC/DPO split without a rewrite. |
| Frontend stack | **Vite + React + TS**, plain **CSS Modules**, **Recharts** | Matches the "simple, white background, few colors, hospital-like" look; no heavy UI framework fighting the palette. |
| BFF sessions | **Redis-backed** | Matches existing infra (notification-service already uses Redis); survives restarts and horizontal scale. |
| "Checks allowed/denied" stat | **Deferred** | Consent checks are audit events, not vault rows; adding this cleanly needs a separate audit-side aggregate. Not built in v1. |

## Architecture

The browser talks only to the BFF, same-origin. No JWT or API key ever reaches
browser code; the browser holds only an opaque session cookie.

```
┌─────────────┐   session cookie    ┌──────────────┐   Bearer <hospital JWT>   ┌───────────────┐
│  React SPA  │ ──────────────────▶ │   admin-bff  │ ────────────────────────▶ │ auth /consent │
│ (browser)   │ ◀────────────────── │   (Go)       │ ◀──────────────────────── │ audit /emerg. │
└─────────────┘   JSON              └──────────────┘                           └───────────────┘
                                       │  holds hospital API key (secret)
                                       │  admin_users login (bcrypt)
                                       │  server-side session (Redis)
```

### Components

**`admin-bff/`** — new top-level Go service, structured like the existing services.

- `bootstrap/` — env, Redis, HTTP clients to the domain services.
- `pkg/session/` — Redis session store (create / load / destroy, TTL).
- `pkg/auth/` — `admin_users` repository + bcrypt verify + login handler; obtains
  and caches the hospital JWT from auth-service (re-mints on expiry).
- `pkg/proxy/` — per-service reverse proxy that injects `Authorization: Bearer <jwt>`;
  for emergency review it also injects `reviewer_id` from the session user.
- `pkg/routes/` — route table (below).
- `cmd/main.go` — wiring + static file serving of the built SPA.

Routes (all under `/api`, cookie-authenticated except login):

| Method + path | Action |
|---|---|
| `POST /api/session` | Login: verify creds → mint/store hospital JWT → set session cookie |
| `DELETE /api/session` | Logout: destroy session, clear cookie |
| `GET /api/me` | Current user identity for the SPA |
| `GET /api/consent/stats` | Proxy → consent-service `GET /api/v1/consent/stats` |
| `GET /api/audit/logs` | Proxy → audit-service `GET /api/v1/audit/logs` |
| `GET /api/emergency/pending` | Proxy → emergency-service `GET /api/v1/emergency/pending` |
| `POST /api/emergency/:id/review` | Proxy → emergency-service; `reviewer_id` injected server-side |

**`frontend/admin-dashboard/`** — Vite + React + TS SPA.

- `src/api/` — thin fetch client to the BFF (`/api/*`), credentials: include.
- `src/auth/` — session/user React context; redirects to login on 401.
- `src/pages/` — `Login`, `Dashboard`, `Audit`, `Emergency`.
- `src/components/` — `StatTile`, chart wrappers (Recharts), `DataTable`, `Modal`.
- `src/styles/` — `tokens.css` (palette) + CSS Modules per component.

**consent-service** — new `GET /api/v1/consent/stats` (see below).

### Auth & session flow

1. **Login:** SPA `POST /api/session {email,password}` → BFF verifies bcrypt against
   `auth.admin_users` → BFF calls auth-service `POST /v1/auth/token {api_key}` (key is
   a server-side secret) → receives hospital JWT → stores `{jwt, user}` in a
   Redis-backed session → sets an **HTTP-only, SameSite=Strict, Secure** cookie.
   Response body carries only the user's display identity.
2. **Data request:** SPA sends cookie → BFF loads session, attaches the hospital JWT,
   proxies to the target service, returns JSON. If the hospital JWT is expired, the
   BFF re-mints it transparently from the stored api_key.
3. **CSRF:** mutating requests (`POST /api/session`, `DELETE /api/session`,
   `POST /api/emergency/:id/review`) require a double-submit CSRF token in addition
   to SameSite=Strict.
4. **Logout:** `DELETE /api/session` destroys the Redis session and clears the cookie.

This also closes a flagged gap for the emergency path: `reviewer_id` is set from the
authenticated admin, not client-supplied free text.

## New backend endpoint — `GET /api/v1/consent/stats`

Read-only aggregate over `consent.consent_vault`, under the existing
`SET LOCAL app.hospital_id` RLS pattern (parameterized, UUID-validated — reuse
`setHospitalContext`). "Current state per patient" = latest version row per
`patient_key` (`DISTINCT ON (patient_key) ... ORDER BY patient_key, version DESC`).

Query params: `window_days` (default 30) bounds the activity window.

Response:

```json
{
  "consents":  { "active": 128, "withdrawn": 14, "total_patients": 142 },
  "by_purpose":[ {"purpose":"treatment","active":120,"withdrawn":6},
                 {"purpose":"insurance","active":40,"withdrawn":8} ],
  "activity":  { "window_days": 30, "captures": 51, "withdrawals": 9, "renewals": 3 },
  "emergency": { "overrides": 7 }
}
```

Sources, all in the vault:

- `consents.active/withdrawn` — count of patients whose latest row aggregates to
  ACTIVE vs WITHDRAWN. `total_patients` — distinct `patient_key`.
- `by_purpose` — active/withdrawn tally from the `purposes` JSONB map on latest rows.
- `activity` — row counts by `type` (`CONSENT_GIVEN` / `WITHDRAWAL` / `CONSENT_RENEWAL`)
  with `created_at` inside the window.
- `emergency.overrides` — count of `type='EMERGENCY_OVERRIDE'` rows.

**"Pending review" is deliberately NOT in this endpoint.** `consent_vault` is
append-only, so its `dpo_review_status` is frozen at `PENDING` and cannot count
open reviews. The live pending count lives in `emergency.reviews` (mutable), which
is the emergency-service's domain — the Dashboard sources the "pending review" tile
from the existing `GET /api/v1/emergency/pending` response's `total`, not from stats.

Registered on the existing consent `/api/v1/consent` group behind `JWTAuth`.

## Data model change

New migration `DPDP/scripts/db/migrations/0012_admin_users.sql`:

```sql
CREATE EXTENSION IF NOT EXISTS citext;    -- case-insensitive email; enable before use

CREATE TABLE IF NOT EXISTS auth.admin_users (
  id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  hospital_id   UUID        NOT NULL REFERENCES auth.hospitals(id),
  email         CITEXT      NOT NULL UNIQUE,
  password_hash TEXT        NOT NULL,          -- bcrypt
  role          VARCHAR     NOT NULL DEFAULT 'admin' CHECK (role IN ('admin','dpo')),
  disabled      BOOLEAN     NOT NULL DEFAULT false,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

Plus a dev seed creating one admin for the test hospital (email + bcrypt hash). The
BFF is configured for a single hospital in Phase 1 (holds that hospital's api_key);
`hospital_id` on the row leaves multi-hospital open for later.

## Screens

```
LOGIN                          DASHBOARD (stats)
┌────────────────────┐         ┌───────────────────────────────────────────┐
│   [Hospital Name]  │         │ Hospital Name        Dashboard│Audit│Emerg │
│  Email  [_______]  │         │ ┌Active┐ ┌Withdrawn┐ ┌Emerg.┐ ┌Pending┐    │
│  Pass   [_______]  │         │ │ 128  │ │   14    │ │  7   │ │  2 ⚠  │    │
│      [  Sign in ]  │         │ ┌ Active vs Withdrawn ┐ ┌ By purpose ────┐ │
└────────────────────┘         │ │      (donut)        │ │   (bar chart)  │ │
                               │ └─────────────────────┘ └────────────────┘ │
                               │ Last 30 days ▾   Captures 51 · Withdr. 9   │
                               └───────────────────────────────────────────┘
AUDIT LOG                                EMERGENCY REVIEW
┌───────────────────────────────────┐    ┌──────────────────────────────────┐
│ Event type ▾  [date range] [Apply]│    │ Doctor  Reason  Note  Age  Action │
│ Time │ Event │ Actor │ IP │ ⋯     │    │ D-12 │ trauma│ … │ 4h ⚠ │[Review] │
│ ◀ prev   page 1/6   next ▶        │    │ Review → modal: VERIFIED/FLAGGED  │
└───────────────────────────────────┘    └──────────────────────────────────┘
```

- **Login:** centered card on white; hospital name; email + password; primary button.
- **Dashboard:** four stat tiles (active, withdrawn, emergency overrides from
  `GET /api/consent/stats`; pending review from `GET /api/emergency/pending` `total`,
  with a warning accent when > 0), a donut (active vs withdrawn), a bar chart (by
  purpose), and an activity line with a window selector.
- **Audit:** table (time, event_type, actor, masked patient_key, IP, expandable
  details); filters for `event_type` and date range; prev/next pagination against
  `total`.
- **Emergency:** table of pending overrides (doctor_id, reason, clinical_note age,
  overdue badge from `dpo_deadline`); "Review" opens a modal to record VERIFIED or
  FLAGGED with a note. Reviewer identity is the logged-in admin (server-injected).

### Palette

White background (`#ffffff`), light-gray surfaces (`#f6f8fa`), one primary (calm
medical teal/blue), three status hues — green (active/verified), amber
(withdrawn/overdue), red (flagged/denied). Near-black text, muted gray secondary.
Exact chart colors finalized against the `dataviz` skill at build time.

## Error handling

- **BFF:** bad creds → 401; absent/expired session → 401 (SPA redirects to login);
  downstream service unreachable → 502 with a generic message (never leak internals);
  CSRF failure → 403.
- **SPA:** top-level error boundary; per-screen loading / empty / error states.

## Testing

- **consent-stats endpoint:** Go tests including RLS cross-hospital isolation (reuse
  the tenant-isolation patterns in `consent-service/test/`) — counts scoped per
  hospital; a second hospital's context returns its own totals, never the other's.
- **BFF:** handler tests — login (valid/invalid), session create/load/destroy, proxy
  injects Bearer + reviewer identity, CSRF enforcement, 502 on downstream failure.
- **SPA:** Vitest + React Testing Library — login flow, tiles rendered from mocked
  stats, audit filter + pagination, emergency review modal submit.

## Out of scope (YAGNI)

Full RBAC, multi-hospital switching, compliance health score, grievance redressal,
per-patient consent drill-down/search, and the checks-allowed/denied metric. The
`role` column and per-user model leave room for Phase-3 RBAC without a rewrite, but
none of that is built now.
