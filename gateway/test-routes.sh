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

# --- Static invariant: override must be written above the consent prefix. ---
ov=$(grep -n 'path /api/v1/consent/emergency-override' "$CADDYFILE" | head -1 | cut -d: -f1)
co=$(grep -n 'path /api/v1/consent/\*' "$CADDYFILE" | head -1 | cut -d: -f1)
if [ -n "$ov" ] && [ -n "$co" ] && [ "$ov" -lt "$co" ]; then
  echo "PASS: emergency-override precedes consent prefix in Caddyfile (line $ov < $co)"
else
  echo "FAIL: emergency-override must be written before consent prefix (override=$ov consent=$co)"; fail=1
fi

# Each matcher must proxy to its intended upstream. A mis-pointed upstream
# leaves line order intact, so the ordering check alone can't catch it — assert
# every target explicitly. (Runtime can't tell these apart: without a JWT the
# JWT-guarded services all return 401 regardless of which one answered.)
for pair in \
  override:emergency-service:9005 \
  consent:consent-service:9000 \
  audit:audit-service:9001 \
  otp:notification-service:9004 \
  emergency:emergency-service:9005 \
  auth:auth-service:9006 \
  kiosk:kiosk-bff:9008; do
  name="${pair%%:*}"; want="${pair#*:}"
  if grep -qE "reverse_proxy[[:space:]]+@${name}[[:space:]]+${want}" "$CADDYFILE"; then
    echo "PASS: @${name} upstream is ${want}"
  else
    echo "FAIL: @${name} must proxy to ${want}"; fail=1
  fi
done

# --- Runtime: edge blocks (status-distinguishable) ---
expect "/internal/* blocked"          403 "$(code -X POST "$BASE/internal/audit/log")"
expect "/v1/auth/service-token blocked" 403 "$(code -X POST "$BASE/v1/auth/service-token")"

# --- Runtime: public routes reach a backend ---
# /health has no explicit route -> catch-all -> admin-bff /health -> 200.
expect "catch-all reaches BFF (/health)" 200 "$(code "$BASE/health")"
# /v1/auth/token is public on auth-service; empty body -> 400 (missing api_key).
# Asserting 400 exactly proves it reached auth's handler — not a 404 from a wrong
# upstream, a 403 edge-block, or a 502 from auth being unreachable.
expect "/v1/auth/token reaches auth" 400 "$(code -X POST -H 'Content-Type: application/json' -d '{}' "$BASE/v1/auth/token")"

# /kiosk/api/otp/send is routed by @kiosk to kiosk-bff, which proxies on to
# notification-service. We don't know the exact downstream status (depends on
# auth/notification wiring), so just assert it's not a 404 — a 404 here would
# mean @kiosk isn't matching and the request fell through to the admin-bff
# catch-all instead.
kiosk_code="$(code -X POST -H 'Content-Type: application/json' -d '{}' "$BASE/kiosk/api/otp/send")"
if [ "$kiosk_code" != 404 ]; then
  echo "PASS: /kiosk/api/otp/send reaches kiosk-bff (not 404, got $kiosk_code)"
else
  echo "FAIL: /kiosk/api/otp/send returned 404 — @kiosk route not matching"; fail=1
fi

[ "$fail" = 0 ] && echo "ALL PASS" || echo "FAILURES"
exit $fail
