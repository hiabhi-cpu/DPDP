# API Gateway (dev) — single public entry point

**Date:** 2026-07-09
**Status:** design approved
**Plan row:** Phase 1 · infra · "API gateway (dev) — single public entry point"

## Problem

Today each service publishes its own port (auth 9006 · consent 9000 · audit 9001 ·
notification 9004 · emergency 9005) and the dashboard BFF publishes 9007. Every
client must know the topology. There is no single public origin, no one place for
security headers / CORS, and no edge that blocks the internal (`/internal/*`) and
service-token endpoints from being reached publicly.

## Decision

Add a **Caddy** reverse-proxy container in compose exposing **one public port `:8080`**.
Caddy over nginx because its `route {}` block preserves written directive order, so
the "emergency-override matches before the consent prefix" precedence is explicit
rather than resting on nginx `location` longest-prefix rules. The whole config is
~30 lines.

Scope (decided 2026-07-09): the gateway fronts **everything, including the dashboard** —
one origin for the whole system. The BFF sits behind it (BFF owns sessions/key custody;
gateway owns topology). This is clean because the SPA's cookie API lives under `/api/*`
without a `v1` segment (`/api/csrf`, `/api/session`, `/api/consent/stats`, …) while all
public service routes are `/api/v1/*` or `/v1/auth/*` — the two never collide, so a
single catch-all → BFF disambiguates by path.

## Route table (canonical manifest)

This table is the single source of truth. In dev it is the Caddyfile; in prod (P3)
the AWS ALB path rules mirror it exactly so dev and prod routing cannot drift.

Ordered, first-match-wins inside one Caddy `route {}`:

| Order | Match | Target | Notes |
|---|---|---|---|
| 1 | `path /internal/* /v1/auth/service-token` | **403** | Internal + service-JWT minting never public. `service-token` blocked here so it 403s before the broader `/v1/auth/*` proxy below. |
| 2 | `path /api/v1/consent/emergency-override` | emergency-service:9005 | Exact path; **must precede** the consent prefix. |
| 3 | `path /api/v1/consent/*` | consent-service:9000 | |
| 4 | `path /api/v1/audit/*` | audit-service:9001 | |
| 5 | `path /api/v1/otp/*` | notification-service:9004 | |
| 6 | `path /api/v1/emergency/*` | emergency-service:9005 | pending + `:id/review` |
| 7 | `path /v1/auth/*` | auth-service:9006 | Only `/v1/auth/token` reachable — `service-token` already 403'd at row 1. |
| 8 | (catch-all) | admin-bff:9007 | Dashboard SPA + cookie `/api/*` session API. |

### Note vs the raw plan text

The plan's blanket `/v1/auth/*` → auth would also expose `/v1/auth/service-token`,
the internal service-JWT minting endpoint (as sensitive as `/internal/*`). Row 1
blocks it at the edge — a correction to the plan text, not the code.

## Security headers & CORS

- Security headers set **once** at the gateway: `X-Content-Type-Options: nosniff`,
  `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, strip `Server`.
- **CORS deferred.** Everything is same-origin today, so a CORS block would allow-list
  clients that do not exist yet. Add it when the first cross-origin client ships
  (`hms-widget`, P2) — which also needs its own frame policy (DENY breaks embedding).
  Marked with a `ponytail:` comment at the insertion point in the Caddyfile.
- **TLS deferred.** Dev is plain HTTP `:8080`; prod terminates TLS at the ALB (P3).

## Layout

New `gateway/` directory (polyrepo-style, matching the other services):

- `gateway/Caddyfile` — the route table above.
- `gateway/docker-compose.yml` — `caddy:2-alpine`, on external `dpdp-network`,
  publishes `8080:8080`, mounts the Caddyfile read-only. No hard `depends_on`
  (Caddy retries upstreams).
- `gateway/test-routes.sh` — verification (below).

Per-service `ports:` stay published in dev. Existing tests, `test/run-isolation.sh`,
and manual curl hit `localhost:9000` etc. directly; the gateway *adds* the unified
public origin rather than hiding the services. Making services private in dev
(removing each `ports:`) is a one-line-per-file follow-up, not part of this change.

## Verification

`gateway/test-routes.sh` curls through `:8080` and asserts the behaviours that break
if the route table regresses:

1. `POST /api/v1/consent/emergency-override` reaches **emergency**, not consent
   (precedence — the load-bearing ordering).
2. `GET /internal/audit/log` (or any `/internal/*`) → **403**.
3. `POST /v1/auth/service-token` → **403**.
4. `POST /api/v1/consent/check` reaches **consent** (service routing works).
5. `GET /` reaches the **BFF / SPA** (catch-all works).

No framework — a bash script with `curl -s -o /dev/null -w '%{http_code}'` asserts.
Run against a live stack (`docker compose up` in each service + gateway).

## Out of scope

- Removing per-service published ports (dev convenience + existing tests rely on them).
- CORS config (deferred to hms-widget, P2).
- TLS (deferred to ALB, P3).
- Production ALB route rules (P3 · "Production ingress = same gateway route table").
- Per-route rate limiting (P3, at the edge in prod).
