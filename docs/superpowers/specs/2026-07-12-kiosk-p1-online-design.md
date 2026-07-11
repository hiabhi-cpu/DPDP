# Kiosk (P1, online) — Design

**Date:** 2026-07-12
**Status:** Approved (design), ready for implementation plan
**Scope:** Phase-1 kiosk only — online OTP + consent form. Offline queue, §9 guardian
flow, per-device provisioning, and multi-language are **deferred to their planned P2
slices** (see `plan-phase.md` Phase 2).

## Goal

The last unstarted Phase-1 piece: a patient-facing consent app that a hospital can run
on **any device — an Android phone, an iPhone, or a fixed kiosk tablet** — with a layout
that flexes to the screen and works on roughly any device from the last ~5 years.

## Key decision: responsive PWA, not React Native

The original plan pencilled in "React Native Expo." The requirements — *any device, both
iPhone and Android, responsive to screen size, 5-year-old hardware, give hospitals
options* — invert that choice:

- **Native (Expo)** means app-store distribution, an Apple Developer account for iPhone,
  per-platform builds, update lag, and a second toolchain. A kiosk becomes an installed,
  locked-down app.
- **Responsive PWA** runs in any browser from a URL — Android, iPhone, any tablet, any
  laptop — with no app store, no Apple account, instant updates, and it **reuses the
  exact stack already in `admin-dashboard`** (Vite + React + TS).

Every stated requirement is a bullseye for the PWA. Offline (a P2 requirement) is handled
later by a service worker + IndexedDB rather than WatermelonDB.

## Architecture

```
Patient's phone / kiosk tablet / any browser
        │  same-origin HTTPS
        ▼
  frontend/kiosk/     responsive PWA  (Vite + React + TS)
        │  /kiosk/api/*
        ▼
  kiosk-bff/          new minimal Go BFF  (per-hospital; API key in env → mints hospital JWT)
        │  Authorization: Bearer <hospital JWT>
        ▼
  gateway :8080       /api/v1/otp/* → notification-service · /api/v1/consent/* → consent-service
```

The PWA never holds the hospital API key or JWT — the same constraint the dashboard hit,
solved the same way. The kiosk-bff is **stateless and public**: no login, no session, no
CSRF (a patient kiosk has no authenticated user). It only: hold key → mint/cache JWT →
proxy two endpoint groups → serve the PWA's static files. Full per-device credentials are
a **P2** hardening (`plan-phase.md` "Kiosk device identity"); P1 keeps the key in the
BFF's env, one deployment per hospital.

### Why a new service and not admin-bff

`admin-bff` is built around admin login, sessions, and CSRF — the wrong shape for an
unauthenticated public surface, and mixing a public patient endpoint into the admin
service is a security smell. The kiosk-bff is separate but small: one handler file plus
the JWT client below.

### Reuse: import the existing token client, don't refactor

`admin-bff/pkg/auth/token.go` already implements exactly the API-key→JWT exchange with
near-expiry caching that the kiosk-bff needs (`HospitalTokenClient`, `TokenProvider`).
`go.work` already unifies the modules, so **kiosk-bff imports `admin-bff/pkg/auth`
directly** — zero changes to admin-bff, no duplication. Promote it to `shared/` **only
if** that cross-module import turns ugly; don't pay for the refactor in P1 on
speculation.

## Kiosk flow (screens)

Single-column wizard, one step per screen:

1. **Welcome / language** — "Start". Language picker is a stub (real multi-language is P2).
2. **Mobile entry** — patient types mobile → `POST /kiosk/api/otp/send`.
3. **OTP verify** — 6-digit entry → `POST /kiosk/api/otp/verify` → receives `session_id`.
4. **Consent notice + purposes** — shows §5 notice text + per-purpose grant/decline
   toggles (per-purpose consent already modelled in the vault). Notice text + purpose
   list are **bundled in the PWA at build time** in P1 (a static JSON import) — no
   endpoint. A dynamic notice endpoint arrives in P2 when content becomes
   multi-language/managed.
5. **Confirm** → `POST /kiosk/api/consent/capture` (with `session_id`) → **done screen**,
   then **auto-reset to step 1 after a timeout** so no patient data is left on a shared
   kiosk screen.

**Errors** (OTP expired, attempts exceeded — the 429s notification-service already
returns) surface inline with a retry, no crash. **No guardian/§9 branch** (P2).

## Responsive + device-compatibility strategy

- **Fluid, not fixed.** Single-column layout with flexbox, relative units, and `clamp()`
  for font/spacing scaling — no hard-coded pixel widths. The same layout fills a portrait
  phone and a landscape kiosk tablet; both orientations handled.
- **Large touch targets** — buttons/inputs ≥44px min height (accessibility + real fingers
  on a kiosk).
- **Browser baseline, not device age.** On Android, Chrome updates independently of the
  OS; on iPhone, Safari rides iOS updates that reach ~6-year-old hardware — so a
  "5-year-old device" is fine *as long as its browser is current*. Set a `browserslist`
  floor (≈2021+ evergreen, Vite's default territory) and avoid bleeding-edge JS/CSS.
- **Ancient-browser fallback.** No detection code and no polyfills. Vite emits
  `<script type="module">`; browsers too old to run the app skip module scripts natively,
  so a plain `<script nomodule>` (or static markup) shows a "please update your browser"
  message for free. `ponytail:` the module/nomodule split *is* the detection.
- **PWA installability** — a web manifest so a hospital can "add to home screen" for a
  fullscreen kiosk look. **No service worker in P1** (offline is P2); the manifest alone
  is one file.

## kiosk-bff API surface

Same-origin, public (no auth from the browser). BFF injects the cached hospital JWT on
every upstream call.

| Method + path | Proxies to | Notes |
|---|---|---|
| `POST /kiosk/api/otp/send` `{mobile}` | notification send | rate-limit 429s pass through |
| `POST /kiosk/api/otp/verify` `{mobile, otp}` | notification verify | returns `{session_id}` |
| `POST /kiosk/api/consent/capture` `{session_id, mobile, purposes[]}` | consent capture | |
| `GET  /kiosk/*` | static PWA build | serves the responsive app, same origin |

**Config (env):** `HOSPITAL_API_KEY`, `AUTH_URL`, `GATEWAY_URL`, listen port (9008).

## Gateway wiring

Add one route to `gateway/Caddyfile` **before** the admin-bff catch-all:

```
@kiosk path /kiosk/*
reverse_proxy @kiosk kiosk-bff:9008
```

The kiosk-bff serves both `/kiosk/` (PWA) and `/kiosk/api/*` — one origin, so the browser
makes only same-origin calls. `gateway/test-routes.sh` gains a case for the kiosk route.

## Testing

- **kiosk-bff (Go):** key→JWT mint/cache; each proxy handler; assert the JWT/API key
  **never appears in a response to the browser**. One integration test against the live
  gateway (mirrors the existing tenant-isolation suite pattern).
- **PWA (Vitest + Testing Library, existing stack):** the wizard state machine (step
  transitions, OTP-expired retry, reset-on-done); layout renders with no fixed pixel
  widths. One end-to-end happy path against a running kiosk-bff.

## Explicitly out of scope (P2 and later)

- Offline capture / service worker / IndexedDB sync (P2).
- §9 age-gate + guardian identity + OTP-to-guardian minor path (P2).
- Per-device provisioning / rotating device credentials (P2).
- Multi-language notice text and UI (P2 / P4).
- Dynamic notice endpoint / content management (P1 bundles static notice text in the PWA).
