#!/usr/bin/env bash
# Runs the kiosk live-stack suite against the real docker services.
#
# These tests stop, pause, and restart real containers and drive the real
# consent flow. They are NOT part of `npm test` — they need the stack up.
#
#   HOSPITAL_API_KEY — defaults to the local dev key
#   HOSPITAL_ID      — defaults to the seeded local hospital
#
# Codes are staged immediately before vitest runs, on purpose: a claim OTP
# expires after 3 minutes (notification-service otpExpiry), so minting them
# any earlier makes the tests fail at resolve for reasons unrelated to the code
# under test.
set -euo pipefail

cd "$(dirname "$0")/.."
ROOT="$(git rev-parse --show-toplevel)"

HOSPITAL_API_KEY="${HOSPITAL_API_KEY:-TEST-HOSPITAL-API-KEY-LOCAL-DEV-001}"
HOSPITAL_ID="${HOSPITAL_ID:-a1b2c3d4-e5f6-7890-abcd-ef1234567890}"
CERTS="$ROOT/integration-service/certs"
LOG="/data/logs/notification-service/$(date +%F)/app.log"

need() {
  curl -sf -m 2 "localhost:$1/health" >/dev/null \
    || { echo "✗ $2 (:$1) is not up — see RUN_LOCAL.md" >&2; exit 1; }
}
echo "→ checking the stack"
need 9000 consent; need 9004 notification; need 9008 kiosk-bff; need 9009 integration
docker inspect dpdp-redis >/dev/null 2>&1 || { echo "✗ dpdp-redis not found" >&2; exit 1; }
[ -f "$CERTS/ca.pem" ] || { echo "✗ dev certs missing — run integration-service/certs/gen-dev-certs.sh" >&2; exit 1; }

JWT="$(curl -sS -X POST localhost:9006/v1/auth/token \
  -H 'Content-Type: application/json' \
  -d "{\"api_key\":\"$HOSPITAL_API_KEY\"}" |
  python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')"
[ -n "$JWT" ] || { echo "✗ could not mint a hospital JWT" >&2; exit 1; }

# A fresh patient per test: patient_key is derived from the mobile, so reusing
# one that already consented would 409 instead of exercising the failure path.
RUN="$(date +%s)"
stage() { # stage <n> → echoes a fresh 6-digit code
  # Separate `local` statements on purpose: `local` is a command, so all its
  # arguments are expanded before ANY of its assignments take effect — writing
  # `local n="$1" pid="…$n"` would expand $n while it is still unset.
  local n="$1"
  local pid="LIVE-$RUN-$n"
  local mob="9$(printf '%09d' $(( (RUN + n * 7919) % 1000000000 )))"
  curl -sS --cacert "$CERTS/ca.pem" --cert "$CERTS/$HOSPITAL_ID.pem" --key "$CERTS/$HOSPITAL_ID.key" \
    https://localhost:9443/webhook/patient-registered -H 'Content-Type: application/json' \
    -d "{\"patientId\":\"$pid\",\"givenName\":\"Live\",\"familyName\":\"Test$n\",\"phoneNumber\":\"$mob\"}" >/dev/null
  curl -sS -X POST localhost:9004/internal/v1/otp/claim/send -H "Authorization: Bearer $JWT" \
    -H 'Content-Type: application/json' -d "{\"mobile\":\"$mob\",\"ref\":\"$pid\"}" >/dev/null
  sleep 1
  grep -ah "MOCK SMS" "$LOG" | tail -1 | sed -n 's/.*OTP: \([0-9]\{6\}\).*/\1/p'
}

echo "→ staging patients + codes (they expire in 3 minutes)"
export CODE_RETRY_REFUSED="$(stage 1)"
export CODE_RETRY_HUNG="$(stage 2)"
export CODE_EXPIRED="$(stage 3)"
export CODE_CODE_OUTAGE="$(stage 4)"
export CODE_CAPTURE_OUTAGE="$(stage 5)"
for v in CODE_RETRY_REFUSED CODE_RETRY_HUNG CODE_EXPIRED CODE_CODE_OUTAGE CODE_CAPTURE_OUTAGE; do
  [ -n "${!v}" ] || { echo "✗ $v is empty — is SMS_PROVIDER=mock and $LOG being written?" >&2; exit 1; }
done

echo "→ running the live suite (stops/pauses real containers)"
npx vitest run --config vitest.live.config.ts "$@"
