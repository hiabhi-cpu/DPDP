# Flows & API Testing

How the services actually talk to each other, and a verified curl for every one.
Every command below was run against a local stack — the responses shown are real.

Companion docs: `RUN_LOCAL.md` (start it), `deploy.md` (deploy it), `DOCKER.md`.

---

## 1. The shape of the system

```
  kiosk PWA :5174          dashboard SPA :5173
        │                          │
   kiosk-bff :9008          admin-bff :9007        ← hold the hospital API key
   (stateless, no login)    (session cookie + CSRF, roles)
        │                          │
        └──────────► hospital JWT ◄┘
                          │
   ┌──────────────────────┼───────────────────────────────┐
   │                      │                               │
auth :9006          consent :9000 ──service JWT──► audit :9001
(API key → JWT)     (vault, append-only)          (ledger, append-only)
                          │
                    notification :9004  (OTP + one-time claim codes, Redis)
                          │
                    emergency :9005  (§7(b) override + DPO review queue)
                          │
                    integration :9009 internal API · :9443 mTLS webhook
                                      (pending registrations, Redis 72h TTL)
```

**Two rules that explain most 4xx responses:**

1. **Everything needs a hospital JWT** (`Authorization: Bearer $T`), minted from
   the hospital API key at auth-service. The BFFs do this for the browser; with
   curl you do it yourself.
2. **Writing consent needs a live OTP-verified session.** consent-service calls
   notification's `/internal/v1/otp/session/validate` before it writes any vault
   row, so an invented `session_id` is **403**, never 201. `capture`, `withdraw`
   and `grant` all enforce this — `check` does not (it's a read).

---

## 2. Get a token (every flow starts here)

```bash
T=$(curl -s localhost:9006/v1/auth/token \
  -H 'Content-Type: application/json' \
  -d '{"api_key":"TEST-HOSPITAL-API-KEY-LOCAL-DEV-001"}' | jq -r .token)
```

The API key maps to one hospital; `hospital_id` is read from the JWT, never from
a request body. Valid 24h.

---

## 3. The three consent flows

### Flow A — kiosk self-serve (patient knows their mobile)

```
mobile → otp/send → 📱 code → otp/verify → session_id → consent/capture → vault row
```

### Flow B — front-desk (Spec A+B; patient never types a mobile)

```
HMS ──mTLS──► webhook          stages into Redis (72h TTL), status PENDING
                 │             ⚠️  staging does NOT send a code
reception UI ──► send-code ──► 📱 one-time code
patient at kiosk ──► claim/resolve (code only) ──► session_id + name
                 └─► consent/capture ──► vault row ──► status DONE (leaves queue)
```

### Flow C — emergency override (DPDP §7(b), deemed consent)

```
doctor → emergency-override → ALWAYS allowed:true (never blocks care)
                            → immutable EMERGENCY_OVERRIDE vault row
                            → mutable emergency.reviews queue item → DPO reviews
```

---

## 4. Per-service curl tests

### Health — all services

```bash
for p in 9000 9001 9004 9005 9006 9007 9008 9009; do
  curl -sf localhost:$p/health >/dev/null && echo ":$p ok" || echo ":$p DOWN"
done
curl -sf localhost:8080/healthz && echo " gateway ok"
```

### auth-service `:9006`

```bash
# hospital API key → JWT
curl -s localhost:9006/v1/auth/token -H 'Content-Type: application/json' \
  -d '{"api_key":"TEST-HOSPITAL-API-KEY-LOCAL-DEV-001"}'
# → {"token":"eyJhbGciOiJSUzI1NiIs…"}
```

`POST /v1/auth/service-token` also exists (internal RS256 tokens for
service-to-service calls). It is **blocked at the gateway** on purpose.

### notification-service `:9004` — OTP

```bash
M=9000000042
REF=$(curl -s localhost:9004/api/v1/otp/send -H "Authorization: Bearer $T" \
  -H 'Content-Type: application/json' -d "{\"mobile\":\"$M\"}" | jq -r .reference_id)

# SMS_PROVIDER=mock ⇒ the code goes to the LOG FILE, not stdout:
OTP=$(grep -ah "MOCK SMS" /data/logs/notification-service/$(date +%F)/app.log |
  tail -1 | sed -n 's/.*OTP: \([0-9]\{6\}\).*/\1/p')

SID=$(curl -s localhost:9004/api/v1/otp/verify -H "Authorization: Bearer $T" \
  -H 'Content-Type: application/json' \
  -d "{\"reference_id\":\"$REF\",\"otp\":\"$OTP\",\"mobile\":\"$M\"}" | jq -r .session_id)
echo "$SID"   # → 07a038a2-4abb-4c5e-99c2-7e0347152123
```

### consent-service `:9000` — the vault

```bash
# capture (needs $SID from above)
curl -s localhost:9000/api/v1/consent/capture -H "Authorization: Bearer $T" \
  -H 'Content-Type: application/json' \
  -d "{\"mobile\":\"$M\",\"session_id\":\"$SID\",\"purposes\":[\"treatment\"],\"hms_patient_id\":\"PA-SMOKE-1\"}"
# → {"id":"b3609cb4…","type":"CONSENT_GIVEN","status":"ACTIVE","purposes":{"treatment":"ACTIVE"}}

# check — by mobile OR hms_patient_id (purpose is required)
curl -s localhost:9000/api/v1/consent/check -H "Authorization: Bearer $T" \
  -H 'Content-Type: application/json' -d "{\"mobile\":\"$M\",\"purpose\":\"treatment\"}"
# → {"allowed":true,"consent_id":"b3609cb4…"}
curl -s localhost:9000/api/v1/consent/check -H "Authorization: Bearer $T" \
  -H 'Content-Type: application/json' -d '{"hms_patient_id":"PA-SMOKE-1","purpose":"treatment"}'

# withdraw  (append-only: writes a new row, never deletes)
curl -s localhost:9000/api/v1/consent/withdraw -H "Authorization: Bearer $T" \
  -H 'Content-Type: application/json' \
  -d "{\"mobile\":\"$M\",\"session_id\":\"$SID\",\"purposes\":[\"treatment\"]}"
# → {"status":"withdrawn"}     then check → {"allowed":false,"reason":"consent_withdrawn"}

# grant — re-grant/extend an EXISTING chain (first-time consent uses /capture)
curl -s localhost:9000/api/v1/consent/grant -H "Authorization: Bearer $T" \
  -H 'Content-Type: application/json' \
  -d "{\"mobile\":\"$M\",\"session_id\":\"$SID\",\"purposes\":[\"treatment\"]}"

# stats (dashboard)
curl -s localhost:9000/api/v1/consent/stats -H "Authorization: Bearer $T"
# → {"consents":{"active":32,"withdrawn":1,"total_patients":33},"by_purpose":[…]}
```

> `check` distinguishes `no_consent` (never granted) from `consent_withdrawn`
> (granted, then withdrawn) — different reasons, both `allowed:false`.

### audit-service `:9001` — the ledger

```bash
curl -s "localhost:9001/api/v1/audit/logs?page=1&limit=2&event_type=CONSENT_GRANTED" \
  -H "Authorization: Bearer $T"
# → {"events":[{"id":68,"event_type":"CONSENT_GRANTED","actor_id":"v1|40ec85f0…",…}]}
```

Query params: `page` (default 1), `limit` (default 50), `event_type` (optional).
Writes only arrive via `POST /internal/audit/log` with a **service token** —
unauthenticated calls are 401, and the gateway blocks `/internal/*` outright.

### emergency-service `:9005` — §7(b)

```bash
# override — ALWAYS allowed, even with no consent on file. Care is never blocked.
curl -s localhost:9005/api/v1/consent/emergency-override -H "Authorization: Bearer $T" \
  -H 'Content-Type: application/json' \
  -d '{"hms_patient_id":"PA-1","doctor_id":"DR-9","emergency_reason":"unconscious","clinical_note":"trauma intake"}'
# → {"allowed":true,"emergency_id":"EMRG-2026-97F7254C","access_id":"97f7254c…"}

# DPO review queue
curl -s localhost:9005/api/v1/emergency/pending -H "Authorization: Bearer $T"

# review a specific access (decision: VERIFIED | FLAGGED)
AID=$(curl -s localhost:9005/api/v1/emergency/pending -H "Authorization: Bearer $T" |
  jq -r '.pending[0].access_id')
curl -s -X POST localhost:9005/api/v1/emergency/$AID/review -H "Authorization: Bearer $T" \
  -H 'Content-Type: application/json' -d '{"decision":"VERIFIED","reviewer_id":"DPO-1"}'
# → {"access_id":"97f7254c…","decision":"VERIFIED","status":"reviewed"}
```

### integration-service `:9443` (mTLS webhook) + `:9009` (internal API)

```bash
HOSP=a1b2c3d4-e5f6-7890-abcd-ef1234567890
cd integration-service

# HMS stages a patient. The client cert's CN IS the hospital_id — it is both the
# authentication and the tenant identity. Body's hospital is never trusted.
curl -sS --cacert certs/ca.pem --cert certs/$HOSP.pem --key certs/$HOSP.key \
  https://localhost:9443/webhook/patient-registered -H 'Content-Type: application/json' \
  -d '{"patientId":"PA-2","givenName":"Ravi","familyName":"Kumar","phoneNumber":"9744422255"}'
# → {"status":"staged"}          ⚠️  staged ≠ code sent (see Flow B)

# queue (mobile masked), one registration, status update
curl -s localhost:9009/internal/v1/registrations -H "Authorization: Bearer $T"
# → [{"hms_patient_id":"PA-2","name":"Ravi Kumar","mobile":"97****2255","status":"PENDING"}]
curl -s localhost:9009/internal/v1/registrations/PA-2 -H "Authorization: Bearer $T"
curl -s -X POST localhost:9009/internal/v1/registrations/PA-2/status \
  -H "Authorization: Bearer $T" -H 'Content-Type: application/json' -d '{"status":"DONE"}'
```

> **`patientId` is an upsert key.** Staging `PA-1` twice does not create two
> queue rows — the second overwrites the first (including the phone). Use
> distinct ids to stage multiple patients.

### kiosk-bff `:9008` — stateless, no login

The one-time code **is** the authentication; there is no session cookie.

```bash
# resolve a code → verified session + patient name (kiosk shows the name)
curl -s -X POST localhost:9008/kiosk/api/claim/resolve \
  -H 'Content-Type: application/json' -d '{"otp":"273366"}'
# → {"hms_patient_id":"PA-2","mobile":"9744422255","name":"Ravi Kumar","session_id":"476422d9…"}

curl -s -X POST localhost:9008/kiosk/api/consent/capture -H 'Content-Type: application/json' \
  -d '{"mobile":"9744422255","session_id":"476422d9…","hms_patient_id":"PA-2","purposes":["treatment"]}'
# → 201 vault row; kiosk-bff then fires status DONE in the background
```

### admin-bff `:9007` — session cookie + CSRF

```bash
J=/tmp/jar; rm -f $J
curl -sS -c $J localhost:9007/api/csrf >/dev/null
C1=$(grep csrf_token $J | tail -1 | awk '{print $NF}')

# login — the CSRF token ROTATES on login, so re-read it afterwards
curl -sS -b $J -c $J -H "X-CSRF-Token: $C1" -H 'Content-Type: application/json' \
  -d '{"email":"reception@testhospital.local","password":"<pw>"}' localhost:9007/api/session
# → {"email":"reception@testhospital.local","role":"reception"}
C2=$(grep csrf_token $J | tail -1 | awk '{print $NF}')

curl -sS -b $J localhost:9007/api/me                          # → {"email":…,"role":"reception"}
curl -sS -b $J localhost:9007/api/reception/registrations     # → 200, mobiles masked
curl -sS -b $J -H "X-CSRF-Token: $C2" -X POST \
  localhost:9007/api/reception/registrations/PA-2/send-code   # → {"status":"sent"}
curl -sS -b $J -H "X-CSRF-Token: $C2" -X DELETE localhost:9007/api/session   # logout
```

`admin` / `dpo` sessions additionally reach `/api/consent/stats`,
`/api/audit/logs`, `/api/emergency/pending` and `POST /api/emergency/:id/review`.

No session → 401:
```bash
curl -s -o /dev/null -w '%{http_code}\n' localhost:9007/api/me    # → 401
```

Seed users with `go run ./cmd/seedadmin` (see `RUN_LOCAL.md` §4).

---

## 5. End-to-end: Flow A (kiosk self-serve)

```bash
T=$(curl -s localhost:9006/v1/auth/token -H 'Content-Type: application/json' \
  -d '{"api_key":"TEST-HOSPITAL-API-KEY-LOCAL-DEV-001"}' | jq -r .token)
M=9000000042

REF=$(curl -s localhost:9004/api/v1/otp/send -H "Authorization: Bearer $T" \
  -H 'Content-Type: application/json' -d "{\"mobile\":\"$M\"}" | jq -r .reference_id)
sleep 1
OTP=$(grep -ah "MOCK SMS" /data/logs/notification-service/$(date +%F)/app.log |
  tail -1 | sed -n 's/.*OTP: \([0-9]\{6\}\).*/\1/p')
SID=$(curl -s localhost:9004/api/v1/otp/verify -H "Authorization: Bearer $T" \
  -H 'Content-Type: application/json' \
  -d "{\"reference_id\":\"$REF\",\"otp\":\"$OTP\",\"mobile\":\"$M\"}" | jq -r .session_id)

curl -s localhost:9000/api/v1/consent/capture -H "Authorization: Bearer $T" \
  -H 'Content-Type: application/json' \
  -d "{\"mobile\":\"$M\",\"session_id\":\"$SID\",\"purposes\":[\"treatment\"],\"hms_patient_id\":\"PA-SMOKE-1\"}"
curl -s localhost:9000/api/v1/consent/check -H "Authorization: Bearer $T" \
  -H 'Content-Type: application/json' -d "{\"mobile\":\"$M\",\"purpose\":\"treatment\"}"
# → {"allowed":true,"consent_id":…}
```

## 6. End-to-end: Flow B (front-desk) — the whole vertical

Webhook → queue → code → kiosk → vault → DONE. Verified output inline.

```bash
HOSP=a1b2c3d4-e5f6-7890-abcd-ef1234567890; MOB=9744422255; D=$(date +%F)
T=$(curl -s localhost:9006/v1/auth/token -H 'Content-Type: application/json' \
  -d '{"api_key":"TEST-HOSPITAL-API-KEY-LOCAL-DEV-001"}' | jq -r .token)
cd integration-service

# 1. HMS stages the patient over mTLS
curl -sS --cacert certs/ca.pem --cert certs/$HOSP.pem --key certs/$HOSP.key \
  https://localhost:9443/webhook/patient-registered -H 'Content-Type: application/json' \
  -d "{\"patientId\":\"PA-2\",\"givenName\":\"Ravi\",\"familyName\":\"Kumar\",\"phoneNumber\":\"$MOB\"}"
# → {"status":"staged"}

# 2. Reception fires the code (UI: /reception → Send code. This is what it calls.)
curl -s localhost:9004/internal/v1/otp/claim/send -H "Authorization: Bearer $T" \
  -H 'Content-Type: application/json' -d "{\"mobile\":\"$MOB\",\"ref\":\"PA-2\"}"
# → {"reference_id":"cc9b88d7…","expires_at":"2026-07-15T20:29:58+05:30"}

# 3. Patient enters ONLY the code at the kiosk
sleep 1
OTP=$(grep -ah "MOCK SMS" /data/logs/notification-service/$D/app.log |
  tail -1 | sed -n 's/.*OTP: \([0-9]\{6\}\).*/\1/p')
RES=$(curl -s -X POST localhost:9008/kiosk/api/claim/resolve \
  -H 'Content-Type: application/json' -d "{\"otp\":\"$OTP\"}")
echo "$RES"   # → {"hms_patient_id":"PA-2","mobile":"…","name":"Ravi Kumar","session_id":"476422d9…"}
SID=$(echo "$RES" | jq -r .session_id)

# 4. Capture at the kiosk
curl -s -X POST localhost:9008/kiosk/api/consent/capture -H 'Content-Type: application/json' \
  -d "{\"mobile\":\"$MOB\",\"session_id\":\"$SID\",\"hms_patient_id\":\"PA-2\",\"purposes\":[\"treatment\"]}"
# → 201 {"id":"630c03b8…",…}

# 5. The HMS-linked consent resolves, and the queue row is DONE
curl -s localhost:9000/api/v1/consent/check -H "Authorization: Bearer $T" \
  -H 'Content-Type: application/json' -d '{"hms_patient_id":"PA-2","purpose":"treatment"}'
# → {"allowed":true,"consent_id":"630c03b8…"}
curl -s localhost:9009/internal/v1/registrations/PA-2 -H "Authorization: Bearer $T"
# → {"hms_patient_id":"PA-2","status":"DONE",…}
```

## 7. End-to-end: Flow C (emergency)

```bash
curl -s localhost:9005/api/v1/consent/emergency-override -H "Authorization: Bearer $T" \
  -H 'Content-Type: application/json' \
  -d '{"hms_patient_id":"PA-1","doctor_id":"DR-9","emergency_reason":"unconscious","clinical_note":"trauma intake"}'
# → {"allowed":true,…}   ← true even with zero consent on file, by design

AID=$(curl -s localhost:9005/api/v1/emergency/pending -H "Authorization: Bearer $T" |
  jq -r '.pending[0].access_id')
curl -s -X POST localhost:9005/api/v1/emergency/$AID/review -H "Authorization: Bearer $T" \
  -H 'Content-Type: application/json' -d '{"decision":"VERIFIED","reviewer_id":"DPO-1"}'
# → {"decision":"VERIFIED","status":"reviewed"}
```

---

## 8. Guardrails — these SHOULD fail

A green run here matters as much as the happy paths.

```bash
# Fabricated session_id → 403, no vault row written
curl -s localhost:9000/api/v1/consent/capture -H "Authorization: Bearer $T" \
  -H 'Content-Type: application/json' \
  -d '{"mobile":"9000000001","session_id":"made-up","purposes":["treatment"]}'
# → {"error":"otp session invalid or expired — verify OTP first"}

# No token → 401
curl -s -o /dev/null -w '%{http_code}\n' localhost:9000/api/v1/consent/stats            # → 401

# Gateway blocks internals and service-token minting
curl -s -o /dev/null -w '%{http_code}\n' localhost:8080/internal/v1/registrations       # → 403
curl -s -o /dev/null -w '%{http_code}\n' -X POST localhost:8080/v1/auth/service-token   # → 403
curl -s -o /dev/null -w '%{http_code}\n' -X POST localhost:8080/v1/auth/token \
  -H 'Content-Type: application/json' -d '{"api_key":"TEST-HOSPITAL-API-KEY-LOCAL-DEV-001"}'  # → 200

# mTLS: no client cert → TLS handshake fails (no HTTP response at all)
curl -sS --cacert integration-service/certs/ca.pem \
  https://localhost:9443/webhook/patient-registered -d '{}'
# → curl: (55) … tlsv13 alert certificate required

# admin-bff: no session → 401
curl -s -o /dev/null -w '%{http_code}\n' localhost:9007/api/me                          # → 401

# CSRF is enforced: a POST with a valid session but NO X-CSRF-Token → 403
curl -s -o /dev/null -w '%{http_code}\n' -b $J -X POST \
  localhost:9007/api/reception/registrations/PA-1/send-code                             # → 403

# reception is least-privilege — with a RECEPTION session (verified, all 403):
for e in /api/audit/logs /api/consent/stats /api/emergency/pending; do
  curl -s -o /dev/null -w "$e %{http_code}\n" -b $J localhost:9007$e
done
# → /api/audit/logs 403 · /api/consent/stats 403 · /api/emergency/pending 403
# …while its own queue still works:
curl -s -o /dev/null -w '%{http_code}\n' -b $J localhost:9007/api/reception/registrations  # → 200
```

Tenant isolation (RLS, append-only) has its own suite:

```bash
cd consent-service && ./test/run-isolation.sh    # 6 checks
```

---

## 9. Gotchas that cost debugging time

| Symptom | Cause |
|---|---|
| `{"status":"staged"}` but no code in the log | staging never sends — reception must fire **send-code** (Flow B) |
| `otp session invalid or expired` on capture | `session_id` must come from `otp/verify` or `claim/resolve`, and it expires |
| OTP not in `docker logs` | logrus is **file-only**: `/data/logs/<svc>/<date>/app.log`. Only gin lines hit stdout. |
| Staging twice gives one queue row | `patientId` upserts — same id overwrites |
| Queue empty after a Redis restart | pending regs / codes / sessions are Redis-only by design; vault rows are safe in Postgres |
| Second webhook has the old phone | see upsert above — the newest staging wins |
| `send-code` / code entry fails **only** in Docker | BFF `INTEGRATION_URL` / `NOTIFICATION_URL` still on `localhost` defaults instead of compose service names |
