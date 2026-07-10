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
