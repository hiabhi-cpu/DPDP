# Run Locally

Local-only runbook: start everything, then open the frontend. For production,
config reference, and secrets, see `deploy.md`.

## The two URLs you actually open

| Frontend | URL | Serves |
|---|---|---|
| **Admin dashboard** | **http://localhost:5173** | staff login → dashboard / audit / emergency (`admin`,`dpo`) · `/reception` (`reception`) |
| **Patient kiosk** | **http://localhost:5174/kiosk/** | code-entry → consent capture (no login) |

Both are **Vite dev servers you start by hand** (`npm run dev`) — they are not in
Docker. Vite proxies `/api` → admin-bff `:9007` and `/kiosk/api` → kiosk-bff
`:9008`, so the browser stays same-origin. The kiosk needs the trailing
`/kiosk/` (its Vite `base`); plain `localhost:5174` 404s.

> **The gateway `:8080` does not serve the SPAs locally.** The BFF images ship
> no `STATIC_DIR`, so `:8080` is API-only. Use 5173 / 5174 in dev.

## Backend ports

| Port | Service | Check |
|---|---|---|
| 5432 / 6379 | postgres / redis | `docker ps` |
| 9000 | consent | `curl localhost:9000/health` |
| 9001 | audit | `curl localhost:9001/health` |
| 9004 | notification (OTP, mock SMS) | `curl localhost:9004/health` |
| 9005 | emergency | `curl localhost:9005/health` |
| 9006 | auth (JWT) | `curl localhost:9006/health` |
| 9007 | **admin-bff** ← dashboard | `curl localhost:9007/health` |
| 9008 | **kiosk-bff** ← kiosk | `curl localhost:9008/health` |
| 9009 | integration (internal API) | `curl localhost:9009/health` |
| 9443 | integration mTLS webhook | needs a client cert (§5) |
| 8080 | gateway (Caddy, API-only) | `curl localhost:8080/healthz` |

## 1. Infra + schema

```bash
cd DPDP
docker compose up -d          # postgres + redis + one-shot migrate (up, then seed)
docker logs dpdp-migrate      # → "applied 14 migration(s)" on a fresh volume
```

This also creates the shared `dpdp-network` every other compose file attaches to
as external — so run it **first**.

## 2. Keys + certs (once)

```bash
# RS256 keypair — skip if auth-service/keys/ is populated
mkdir -p auth-service/keys
openssl genrsa -out auth-service/keys/auth_private.pem 2048
openssl rsa -in auth-service/keys/auth_private.pem -pubout -out auth-service/keys/auth_public.pem

# integration-service mTLS — CN MUST be the seeded hospital id
bash integration-service/certs/gen-dev-certs.sh a1b2c3d4-e5f6-7890-abcd-ef1234567890
```

## 3. Services

```bash
cd auth-service            && docker compose up -d --build
cd ../audit-service        && docker compose up -d --build
cd ../notification-service && docker compose up -d --build
cd ../consent-service      && docker compose up -d --build
cd ../emergency-service    && docker compose up -d --build
cd ../integration-service  && docker compose up -d --build   # needs step 2 certs
cd ../admin-bff            && docker compose up -d --build
cd ../kiosk-bff            && docker compose up -d --build
cd ../gateway              && docker compose up -d --build   # optional in dev
```

Order doesn't matter — consent/emergency retry auth/audit. Verify:

```bash
for p in 9000 9001 9004 9005 9006 9007 9008 9009; do curl -sf localhost:$p/health && echo " :$p ok"; done
```

## 4. Dashboard login users

`admin-bff` authenticates against `auth.admin_users` — nothing to log in with
until you seed. One per role you need (`admin`, `dpo`, `reception`):

```bash
cd admin-bff
DATABASE_URL="postgres://abhi:5004@localhost:5432/dpdp?sslmode=disable" \
  HOSPITAL_ID=a1b2c3d4-e5f6-7890-abcd-ef1234567890 \
  EMAIL=admin@testhospital.local PASSWORD=<pw> ROLE=admin \
  go run ./cmd/seedadmin
```

`admin`/`dpo` land on `/`; `reception` lands on `/reception` and is 403'd from
audit/stats/emergency by the BFF.

## 5. Frontends

```bash
cd frontend/admin-dashboard && npm install && npm run dev   # → localhost:5173
cd frontend/kiosk           && npm install && npm run dev   # → localhost:5174/kiosk/
```

Log in at http://localhost:5173/login with the user seeded in step 4.

To put a patient in the reception queue (the kiosk needs a code from somewhere),
fire the HMS webhook over mTLS:

```bash
HOSP=a1b2c3d4-e5f6-7890-abcd-ef1234567890
cd integration-service
curl -sS --cacert certs/ca.pem --cert certs/$HOSP.pem --key certs/$HOSP.key \
  https://localhost:9443/webhook/patient-registered -H 'Content-Type: application/json' \
  -d '{"patientId":"PA-1","givenName":"Priya","familyName":"Shah","phoneNumber":"9744400033"}'
```

Then: reception UI (5173 `/reception`) → **Send code** → read the code from the
mock SMS log → enter it at the kiosk (5174).

**Where the code is.** `SMS_PROVIDER=mock` prints it to the notification-service
log, which is a **date-partitioned file on the host** — the compose file bind-mounts
`/data/logs`, so read it directly:

```
/data/logs/notification-service/<YYYY-MM-DD>/app.log
```

```bash
grep -ah "MOCK SMS" /data/logs/notification-service/$(date +%F)/app.log |
  tail -1 | sed -n 's/.*OTP: \([0-9]\{6\}\).*/\1/p'
```

Line looks like: `… msg="📱 [MOCK SMS] To: ******0033 | OTP: 098925"` — the mobile
is masked, the OTP is not (the mock *is* the delivery channel locally).

> **`docker logs dpdp-notification` will NOT show the code.** `logging.Setup`
> points logrus at the file only; just gin's request lines go to stdout. Grep the
> file, not the container.

## Stop / reset

```bash
cd <service> && docker compose down    # one service
cd DPDP && docker compose down         # infra, keeps the DB volume
cd DPDP && docker compose down -v      # wipe DB → next `up` re-migrates + re-seeds
```

## When it breaks

| Symptom | Cause |
|---|---|
| Kiosk 404 at `localhost:5174` | missing trailing `/kiosk/` |
| `:8080` returns no SPA | expected — API-only locally, use 5173/5174 |
| Dashboard loads, login 401s | no seeded user (step 4), or stale volume with a pre-SHA-256 `api_key_hash` → `down -v` |
| Reception "send code" / kiosk code entry fails **only** in Docker | BFF `INTEGRATION_URL`/`NOTIFICATION_URL` defaulting to `localhost` instead of compose service names |
| Reception queue suddenly empty | Redis restarted — pending regs, codes, and sessions are Redis-only by design. Re-fire the webhook. Consented rows are safe in Postgres. |
| Webhook TLS handshake fails | no/wrong client cert — its CN must equal the hospital id |
