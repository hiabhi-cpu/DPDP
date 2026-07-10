# API Gateway (dev) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Caddy reverse-proxy container exposing one public port (`:8080`) that routes to the five backend services and the dashboard BFF, blocking `/internal/*` and `/v1/auth/service-token` at the edge.

**Architecture:** A single `gateway/` directory (polyrepo-style, like the other services) holding a `Caddyfile`, its own `docker-compose.yml` on the external `dpdp-network`, and a `test-routes.sh` verifier. All routing lives in one ordered Caddy `route {}` block whose written order is load-bearing (`route` preserves directive order — first match wins). Per-service ports stay published in dev; the gateway *adds* the unified origin.

**Tech Stack:** Caddy 2 (`caddy:2-alpine`), Docker Compose, bash + curl for verification.

## Global Constraints

- Public port is `:8080`; backends are reached by compose service name on `dpdp-network`: `auth-service:9006`, `consent-service:9000`, `audit-service:9001`, `notification-service:9004`, `emergency-service:9005`, `admin-bff:9007`.
- Route order is the single source of truth (dev Caddyfile; prod ALB mirrors it in P3). `emergency-override` MUST precede the `/api/v1/consent/*` prefix. `/internal/*` and `/v1/auth/service-token` MUST return 403 before any proxy.
- Dev is plain HTTP (`auto_https off`). No TLS, no CORS in this change (deferred — see spec "Security headers & CORS").
- Spec: `docs/superpowers/specs/2026-07-09-api-gateway-design.md`.

---

### Task 1: Gateway container + route table + verifier

**Files:**
- Create: `gateway/Caddyfile`
- Create: `gateway/docker-compose.yml`
- Create: `gateway/test-routes.sh`
- Modify: `DOCKER.md` (add a "single public entry point" section)

**Interfaces:**
- Consumes: the running stack — auth/consent/audit/notification/emergency services + admin-bff, all on `dpdp-network` (see `DOCKER.md` steps 1–2).
- Produces: one public origin `http://localhost:8080` fronting all of the above; a `gateway/test-routes.sh` that exits non-zero on any route-table regression.

- [ ] **Step 1: Write the verifier (the failing test)**

Create `gateway/test-routes.sh`:

```bash
#!/usr/bin/env bash
# Verifies the gateway route table. Bring the full stack + gateway up first
# (see DOCKER.md). Asserts the behaviours that break if routing regresses.
set -u

BASE="${GATEWAY_BASE:-http://localhost:8080}"
CADDYFILE="$(dirname "$0")/Caddyfile"
fail=0

code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }

expect() { # desc  want  got
  if [ "$2" = "$3" ]; then echo "PASS: $1 ($3)"; else echo "FAIL: $1 — want $2 got $3"; fail=1; fi
}
expect_not() { # desc  notwant1  notwant2  got
  if [ "$4" != "$2" ] && [ "$4" != "$3" ]; then echo "PASS: $1 ($4)"; else echo "FAIL: $1 — got disallowed $4"; fail=1; fi
}

# --- Static invariant: override must be written above the consent prefix. ---
ov=$(grep -n 'path /api/v1/consent/emergency-override' "$CADDYFILE" | head -1 | cut -d: -f1)
co=$(grep -n 'path /api/v1/consent/\*' "$CADDYFILE" | head -1 | cut -d: -f1)
if [ -n "$ov" ] && [ -n "$co" ] && [ "$ov" -lt "$co" ]; then
  echo "PASS: emergency-override precedes consent prefix in Caddyfile (line $ov < $co)"
else
  echo "FAIL: emergency-override must be written before consent prefix (override=$ov consent=$co)"; fail=1
fi

# --- Runtime: edge blocks (status-distinguishable) ---
expect "/internal/* blocked"          403 "$(code -X POST "$BASE/internal/audit/log")"
expect "/v1/auth/service-token blocked" 403 "$(code -X POST "$BASE/v1/auth/service-token")"

# --- Runtime: public routes reach a backend ---
# /health has no explicit route -> catch-all -> admin-bff /health -> 200.
expect "catch-all reaches BFF (/health)" 200 "$(code "$BASE/health")"
# /v1/auth/token is public on auth-service; empty body -> 400. Proves it is
# routed to auth (not 404 from a wrong upstream) and not blocked (not 403).
expect_not "/v1/auth/token reaches auth" 404 403 "$(code -X POST -H 'Content-Type: application/json' -d '{}' "$BASE/v1/auth/token")"

[ "$fail" = 0 ] && echo "ALL PASS" || echo "FAILURES"
exit $fail
```

Make it executable:

```bash
chmod +x gateway/test-routes.sh
```

- [ ] **Step 2: Run it to verify it fails**

Run: `bash gateway/test-routes.sh`
Expected: FAIL — no `gateway/Caddyfile` yet (static check fails) and nothing on `:8080` (runtime checks fail / connection refused → `000`). Exit non-zero.

- [ ] **Step 3: Write the Caddyfile**

Create `gateway/Caddyfile`:

```
{
	auto_https off
	admin off
}

:8080 {
	# Security headers, set once at the edge for every response.
	header {
		X-Content-Type-Options nosniff
		X-Frame-Options DENY
		Referrer-Policy no-referrer
		-Server
	}

	# ponytail: add a CORS block here when the first cross-origin client ships
	# (hms-widget, P2). It also needs a frame policy — X-Frame-Options DENY
	# above breaks iframe embedding, so the widget's route overrides it.

	route {
		# Gateway liveness, independent of any upstream.
		respond /healthz 200

		# Internal + service-JWT minting must never be reachable publicly.
		@blocked path /internal/* /v1/auth/service-token
		respond @blocked 403

		# emergency-override MUST precede the /api/v1/consent/* prefix below.
		@override path /api/v1/consent/emergency-override
		reverse_proxy @override emergency-service:9005

		@consent path /api/v1/consent/*
		reverse_proxy @consent consent-service:9000

		@audit path /api/v1/audit/*
		reverse_proxy @audit audit-service:9001

		@otp path /api/v1/otp/*
		reverse_proxy @otp notification-service:9004

		@emergency path /api/v1/emergency/*
		reverse_proxy @emergency emergency-service:9005

		@auth path /v1/auth/*
		reverse_proxy @auth auth-service:9006

		# Catch-all: dashboard SPA + BFF cookie /api/* session API.
		reverse_proxy admin-bff:9007
	}
}
```

- [ ] **Step 4: Write the compose file**

Create `gateway/docker-compose.yml`:

```yaml
# gateway — single public entry point (Caddy reverse proxy).
# Requires the shared external dpdp-network and the services reachable on it
# (see ../DOCKER.md). Caddy retries upstreams, so no hard depends_on is needed.
# Run from this directory:  docker compose up -d
services:
  gateway:
    image: caddy:2-alpine
    container_name: dpdp-gateway
    ports:
      - "8080:8080"
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
    restart: unless-stopped
    networks:
      - dpdp-network
    healthcheck:
      test: ["CMD", "wget", "-q", "-O", "-", "http://localhost:8080/healthz"]
      interval: 10s
      timeout: 3s
      retries: 5
      start_period: 5s

networks:
  dpdp-network:
    external: true
```

- [ ] **Step 5: Bring the stack up and start the gateway**

Ensure infra + all services are running per `DOCKER.md` (postgres, redis, auth, consent, audit, notification, emergency, admin-bff on `dpdp-network`). Then:

```bash
cd gateway && docker compose up -d && cd ..
docker ps --filter name=dpdp-gateway --format '{{.Names}} {{.Status}}'
```

Expected: `dpdp-gateway   Up ... (healthy)` within ~30s.

- [ ] **Step 6: Run the verifier to verify it passes**

Run: `bash gateway/test-routes.sh`
Expected output (order may vary):

```
PASS: emergency-override precedes consent prefix in Caddyfile (line N < M)
PASS: /internal/* blocked (403)
PASS: /v1/auth/service-token blocked (403)
PASS: catch-all reaches BFF (/health) (200)
PASS: /v1/auth/token reaches auth (400)
ALL PASS
```

Exit code 0.

- [ ] **Step 7: Document the gateway in DOCKER.md**

Add this section to `DOCKER.md` after the "## 2. Run the services" section:

```markdown
## 2b. Single public entry point (gateway)

All services also stay reachable on their own ports for dev/tests, but the
**gateway** gives clients one public origin on `:8080` (`gateway/Caddyfile` is
the route table; prod mirrors it at the ALB in P3):

```bash
cd gateway && docker compose up -d
```

- `/v1/auth/token` → auth · `/api/v1/consent/*` → consent (exact
  `/api/v1/consent/emergency-override` → emergency) · `/api/v1/audit/*` → audit
  · `/api/v1/otp/*` → notification · `/api/v1/emergency/*` → emergency ·
  everything else → admin-bff (dashboard SPA + cookie session API).
- `/internal/*` and `/v1/auth/service-token` return **403** at the edge.
- Verify routing: `bash gateway/test-routes.sh`.
```

- [ ] **Step 8: Commit**

```bash
git add gateway/ DOCKER.md
git commit -m "feat(gateway): Caddy single public entry point on :8080

Ordered route table (emergency-override before consent prefix), /internal/*
and /v1/auth/service-token blocked at the edge, catch-all to the dashboard BFF.
Security headers set once here; CORS/TLS deferred. test-routes.sh verifies the
precedence invariant + edge blocks + backend reachability.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Self-Review

**Spec coverage:**
- One public port `:8080` — Step 4 compose. ✓
- Full route table incl. emergency-override precedence — Step 3 Caddyfile + Step 1 static check. ✓
- `/internal/*` + `/v1/auth/service-token` blocked — Step 3 `@blocked` + Step 1 runtime asserts. ✓
- Catch-all → dashboard BFF (SPA + cookie `/api/*`) — Step 3 final `reverse_proxy` + Step 1 `/health` assert. ✓
- Security headers once at the gateway — Step 3 `header` block. ✓
- CORS deferred with hook — Step 3 `ponytail:` comment. ✓
- TLS deferred — Step 3 `auto_https off`. ✓
- Per-service ports stay published in dev — unchanged (no service compose edited); noted in DOCKER.md Step 7. ✓
- Verification script — Steps 1/6. ✓

**Placeholder scan:** none — all files and commands are complete.

**Type/name consistency:** upstream host:port pairs match the Global Constraints and the spec route table; matcher names (`@override`, `@consent`, …) are internal to the one Caddyfile.
