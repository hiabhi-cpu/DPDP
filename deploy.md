# Deployment Guide — DPDP Consent Manager

How to deploy the backend services: **auth**, **consent**, **audit**,
**notification**, **emergency**, **integration**; the two BFFs (**admin-bff**,
**kiosk-bff**) that front the SPAs; and the dev **gateway**. Covers local (Docker)
and production (AWS ap-south-1). Companion docs: `DOCKER.md` (per-service compose
reference), `DPDP/scripts/db/README.md` (migrations), `HARDENING_CHANGELOG.md`.

---

## 1. Topology

```
   browser (admin-dashboard SPA)   patient kiosk (PWA)
            │                              │
            └──────────► gateway :8080 ◄───┘      (Caddy; one public origin)
                 /api/* → admin-bff :9007 · /kiosk/* → kiosk-bff :9008
                 /v1/auth/token → auth · /api/v1/{consent,audit,otp,emergency}/*
                 /internal/* and /v1/auth/service-token BLOCKED at the edge
                             │
   ┌─────────────────────────┴───────────────────────────────┐
   │ admin-bff :9007 (session cookie + CSRF; holds API key)  │
   │ kiosk-bff :9008 (stateless; holds API key)              │
   └─────────────────────────┬───────────────────────────────┘
                             │ hospital JWT
                       ┌─────▼───────────────────────────────────────┐
   hospital API key →  │  auth-service :9006  (JWT + service tokens) │
                       └─────────────────────────────────────────────┘
   patient / kiosk  →  ┌─────────────────┐   service JWT   ┌────────────────┐
   Bearer JWT          │ consent-service │ ──────────────▶ │ audit-service  │
                       │      :9000      │  outbox relay   │     :9001      │
                       └────────┬────────┘                 └────────────────┘
                                │ OTP / claim
                       ┌────────▼────────────┐
                       │ notification-service│  :9004  (Redis OTP + claim store)
                       └─────────────────────┘

   Bahmni / HMS  ──mutual TLS──►  integration-service
                                    :9443 webhook (own listener, NOT via gateway)
                                    :9009 internal read/status API (hospital JWT)
                                    (Redis-only pending store, 72h TTL)

   Shared infra:  PostgreSQL 16 (one `dpdp` DB, schema-per-service, RLS)
                  Redis 7 (OTP / claim / session / pending-registration TTL)
```

Key facts that shape deployment:

- **One database, schema-per-service** (`auth`, `consent`, `audit`,
  `notification`). Services connect as the least-privilege **`dpdp_app`** role so
  Row-Level Security is actually enforced (a superuser/BYPASSRLS connection
  silently disables RLS — see `HARDENING_CHANGELOG.md`).
- **Migrations run as an admin role**, separately from the app. Schema is applied
  by `DPDP/scripts/db/migrate.sh`, never by `/docker-entrypoint-initdb.d`.
- **Internal calls are authenticated** with short-lived RS256 service tokens.
  `auth-service` signs them; `audit-service` verifies them with the RS256 public
  key. `SERVICE_TOKEN_SECRET` is the bootstrap secret shared by issuer + caller.
- **consent-service tolerates late dependencies** — it ships audit events from a
  transactional outbox with retries, so start order is not critical.

- **emergency-service** records DPDP §7(b) emergency access (deemed consent) —
  it **never blocks** access, writes an immutable `EMERGENCY_OVERRIDE` vault row +
  a mutable `emergency.reviews` queue item, and exposes the DPO review queue. Like
  consent-service it has its own transactional outbox+relay for audit durability.

- **integration-service is the only service with a public non-gateway listener.**
  Its `POST /webhook/patient-registered` terminates **mutual TLS** on its own port
  (`:9443`): the hospital's client certificate is *both* the authentication and the
  tenant identity (`hospital_id` = the cert's CN — never read from the body). Client
  certs are per-hospital, so **issuing one is an onboarding step**. Its `:9009`
  `/internal` read/status API is hospital-JWT-gated and must stay off the public edge.

- **The BFFs hold the hospital API key server-side.** The browser never sees the key
  or the hospital JWT. `admin-bff` adds named-user login (bcrypt vs `auth.admin_users`,
  Redis sessions, double-submit CSRF) and role scoping (`admin` / `dpo` / `reception`);
  `kiosk-bff` is stateless and unauthenticated (a patient kiosk has no logged-in user).

- **Front-desk consent flow (Spec A+B):** HMS webhook → a **Redis-only** pending
  registration (72h TTL) → reception fires a one-time code (`notification` claim) →
  the patient enters only that code at a kiosk → `kiosk-bff` resolves it to a verified
  session + name → capture writes the vault row linked to `hms_patient_id`. See
  §8 for the Redis-durability caveat this implies.

Ports: gateway `8080` · consent `9000` · audit `9001` · notification `9004` ·
emergency `9005` · auth `9006` · admin-bff `9007` · kiosk-bff `9008` ·
integration `9009` (internal API) + `9443` (mTLS webhook).

---

## 2. Prerequisites

| Need | Local | Production |
|---|---|---|
| Container runtime | Docker + Compose v2 | ECS Fargate (or EKS) |
| PostgreSQL 16 | container | RDS PostgreSQL 16, **ap-south-1** |
| Redis 7 | container | ElastiCache Redis 7, ap-south-1 |
| `psql` client | on PATH (for migrations) | in the CI/migration image |
| RS256 keypair | `openssl` | generated once, stored in Secrets Manager |
| Go 1.25 | only to build/test | build in CI |

> **Region is non-negotiable.** All patient data (RDS, ElastiCache, S3) stays in
> `ap-south-1` (Mumbai) per DPDP data-residency. Do not deploy DB/cache elsewhere.

---

## 3. Configuration & secrets

### 3a. RS256 JWT keypair
auth-service signs with the private key; consent/audit/notification verify with
the public key.

```bash
mkdir -p auth-service/keys
openssl genrsa -out auth-service/keys/auth_private.pem 2048
openssl rsa -in auth-service/keys/auth_private.pem -pubout -out auth-service/keys/auth_public.pem
```

- **Local:** mounted read-only into containers (never baked into images — see
  `.dockerignore`).
- **Prod:** store both PEMs in AWS Secrets Manager; mount/inject at task start.
  Rotating the key is a Phase-5 procedure (the `v1|` patient-key prefix already
  makes lazy rotation possible).

### 3b. Shared secrets
| Secret | Used by | Notes |
|---|---|---|
| `SERVICE_TOKEN_SECRET` | auth (issuer) + consent (caller) | must **match**; constant-time compared. Dev default is shared; **override in prod**. |
| `DATABASE_URL` (app) | all services touching the DB | `dpdp_app` role — **not** the admin user |
| `DATABASE_URL` (admin) | migrations only | superuser / RDS master — never given to a service |
| hospital keys + `SYSTEM_SALT` | consent/notification (patient-key HMAC) | local: mock JSON (`AWS_SECRETS_MOCK=true`); prod: AWS Secrets Manager |

### 3c. Per-service environment

**auth-service**
```
AUTH_SERVICE_PORT=9006
DATABASE_URL=postgres://dpdp_app:…@postgres:5432/dpdp?sslmode=disable
JWT_PRIVATE_KEY_PATH=/keys/auth_private.pem
JWT_PUBLIC_KEY_PATH=/keys/auth_public.pem
JWT_EXPIRY_HOURS=24
SERVICE_TOKEN_SECRET=<shared bootstrap secret>
SERVICE_TOKEN_EXPIRY_MINUTES=10
```
**consent-service**
```
CONSENT_SERVICE_PORT=9000
DATABASE_URL=postgres://dpdp_app:…@postgres:5432/dpdp?sslmode=disable
JWT_PUBLIC_KEY_PATH=/keys/auth_public.pem
AUTH_SERVICE_URL=http://auth-service:9006
AUDIT_SERVICE_URL=http://audit-service:9001
NOTIFICATION_SERVICE_URL=http://notification-service:9004
SERVICE_TOKEN_SECRET=<shared bootstrap secret>
AWS_SECRETS_MOCK=true                 # false in prod
LOCAL_SECRETS_PATH=/secrets/local_hospital_keys.json   # local only
```
**audit-service**
```
AUDIT_SERVICE_PORT=9001
DATABASE_URL=postgres://dpdp_app:…@postgres:5432/dpdp?sslmode=disable
JWT_PUBLIC_KEY_PATH=/keys/auth_public.pem   # verifies service tokens
```
**notification-service**
```
NOTIFICATION_SERVICE_PORT=9004
REDIS_URL=redis://redis:6379/0
JWT_PUBLIC_KEY_PATH=/keys/auth_public.pem
SMS_PROVIDER=mock                     # msg91 in prod
MSG91_AUTH_KEY=…  MSG91_TEMPLATE_ID=…
```
**emergency-service**
```
EMERGENCY_SERVICE_PORT=9005
DATABASE_URL=postgres://dpdp_app:…@postgres:5432/dpdp?sslmode=disable
JWT_PUBLIC_KEY_PATH=/keys/auth_public.pem
AUTH_SERVICE_URL=http://auth-service:9006
AUDIT_SERVICE_URL=http://audit-service:9001
SERVICE_TOKEN_SECRET=<shared bootstrap secret>
AWS_SECRETS_MOCK=true                 # false in prod
LOCAL_SECRETS_PATH=/secrets/local_hospital_keys.json   # local only
```
**integration-service** (mTLS webhook + pending-registration store)
```
INTEGRATION_SERVICE_PORT=9009         # internal read/status API (hospital JWT)
INTEGRATION_WEBHOOK_PORT=9443         # mTLS webhook, own listener
REDIS_URL=redis://redis:6379/0
JWT_PUBLIC_KEY_PATH=/keys/auth_public.pem
MTLS_SERVER_CERT=/certs/server.pem    # see §3d
MTLS_SERVER_KEY=/certs/server.key
MTLS_HOSPITAL_CA=/certs/ca.pem        # CA that signs hospital CLIENT certs
```
**admin-bff** (dashboard BFF — admin / dpo / reception)
```
ADMIN_BFF_PORT=9007
DATABASE_URL=postgres://dpdp_app:…@postgres:5432/dpdp?sslmode=disable
REDIS_URL=redis://redis:6379/0
HOSPITAL_API_KEY=<raw hospital API key — server-side only>
AUTH_SERVICE_URL=http://auth-service:9006
CONSENT_SERVICE_URL=http://consent-service:9000
AUDIT_SERVICE_URL=http://audit-service:9001
EMERGENCY_SERVICE_URL=http://emergency-service:9005
INTEGRATION_URL=http://integration-service:9009      # reception queue + status
NOTIFICATION_URL=http://notification-service:9004    # reception "send code"
SESSION_TTL=8h
COOKIE_SECURE=false                   # true in prod (HTTPS)
```
**kiosk-bff** (patient kiosk BFF — stateless, no login)
```
KIOSK_BFF_PORT=9008
HOSPITAL_API_KEY=<raw hospital API key — server-side only>
AUTH_SERVICE_URL=http://auth-service:9006
NOTIFICATION_SERVICE_URL=http://notification-service:9004   # claim/resolve
CONSENT_SERVICE_URL=http://consent-service:9000
INTEGRATION_SERVICE_URL=http://integration-service:9009     # name lookup + DONE
STATIC_DIR=/app/web                   # built PWA; empty = Vite dev server
```

> **Gotcha — `INTEGRATION_URL` / `NOTIFICATION_URL` / `INTEGRATION_SERVICE_URL`
> default to `localhost`.** That works when you `go run` a BFF on the host, and
> **silently breaks inside Docker**, where services resolve each other by compose
> service name. If reception "send code" or the kiosk's code entry fails only in
> containers, this is why — set them explicitly (the compose files now do).

Each service also ships a `.env.example` — copy to `.env` and fill in.

### 3d. integration-service mTLS material

The webhook requires a **verified client cert** (`RequireAndVerifyClientCert`), and
the cert's **CN is taken as the `hospital_id`** — so the CN must equal the hospital's
UUID in `auth.hospitals`.

```bash
# Dev: generates ca.pem/ca.key, server.pem/server.key (CN=localhost),
# and <hospital_id>.pem/.key (a CLIENT cert whose CN IS the hospital id).
bash integration-service/certs/gen-dev-certs.sh <hospital_id>
```
- Certs are **gitignored** and mounted read-only (`./certs:/certs:ro`).
- **Prod:** replace the dev CA with a real/ACM Private CA. Issuing a per-hospital
  client cert is part of **hospital onboarding**; revoking it cuts that hospital's
  webhook off. Never share one client cert across hospitals — the CN is the tenant.

---

## 4. Local deployment

### Step 1 — infra + schema (one command)
```bash
cd DPDP
docker compose up -d          # postgres + redis + one-shot `migrate` (up, then seed)
```
The `migrate` container waits for Postgres to be healthy, runs
`migrate.sh up` (schemas, tables, RLS, `dpdp_app` role) then `migrate.sh seed`
(local test hospital), and exits. Confirm:
```bash
docker logs dpdp-migrate            # → "applied 14 migration(s)" (first run)
DATABASE_URL="postgres://abhi:5004@localhost:5432/dpdp?sslmode=disable" \
  DPDP/scripts/db/migrate.sh status # all applied (0001–0014)
```

> **Already have an old volume** (built by the retired `init/` scripts)?
> Don't wipe — adopt it: `migrate.sh baseline 0010` (see
> `DPDP/scripts/db/README.md`).

### Step 2 — keys + certs
Generate the RS256 keypair (§3a) if `auth-service/keys/` is empty, and the
integration-service mTLS material (§3d) — the CN **must** be the seeded hospital id:
```bash
bash integration-service/certs/gen-dev-certs.sh a1b2c3d4-e5f6-7890-abcd-ef1234567890
```

### Step 3 — services
```bash
cd auth-service            && docker compose up -d --build
cd ../audit-service        && docker compose up -d --build
cd ../notification-service && docker compose up -d --build
cd ../consent-service      && docker compose up -d --build
cd ../emergency-service    && docker compose up -d --build
cd ../integration-service  && docker compose up -d --build   # needs certs from Step 2
cd ../admin-bff            && docker compose up -d --build
cd ../kiosk-bff            && docker compose up -d --build
cd ../gateway              && docker compose up -d --build   # single public origin :8080
```
(Order isn't critical; consent/emergency retry auth/audit.)

### Step 3b — dashboard users (local)
`admin-bff` authenticates named users from `auth.admin_users`. Seed one per role you
need — `ROLE` is `admin`, `dpo`, or `reception` (migration `0014` allows `reception`):
```bash
cd admin-bff
DATABASE_URL="postgres://abhi:5004@localhost:5432/dpdp?sslmode=disable" \
  HOSPITAL_ID=a1b2c3d4-e5f6-7890-abcd-ef1234567890 \
  EMAIL=reception@testhospital.local PASSWORD=<pw> ROLE=reception \
  go run ./cmd/seedadmin
```
A `reception` user lands on `/reception` (consent queue) and is **403'd** by the BFF
from audit / stats / emergency.

### Step 4 — smoke test
```bash
for p in 9006 9001 9004 9000 9005 9009 9007 9008; do curl -sf localhost:$p/health && echo " :$p ok"; done

# hospital token → capture → check
TOKEN=$(curl -s localhost:9006/v1/auth/token \
  -H 'Content-Type: application/json' \
  -d '{"api_key":"TEST-HOSPITAL-API-KEY-LOCAL-DEV-001"}' | jq -r .token)

curl -s localhost:9000/api/v1/consent/capture -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"mobile":"9000000001","session_id":"smoke-1","purposes":["treatment"],"hms_patient_id":"PA-SMOKE-1"}'

curl -s localhost:9000/api/v1/consent/check -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"mobile":"9000000001","purpose":"treatment"}'
```

### Step 4b — front-desk consent flow (Spec A+B) end-to-end
Exercises the whole vertical: mTLS webhook → reception queue → code → kiosk → vault.
```bash
HOSP=a1b2c3d4-e5f6-7890-abcd-ef1234567890
cd integration-service

# 1. HMS stages a patient over mTLS (client cert CN == hospital id)
curl -sS --cacert certs/ca.pem --cert certs/$HOSP.pem --key certs/$HOSP.key \
  https://localhost:9443/webhook/patient-registered -H 'Content-Type: application/json' \
  -d '{"patientId":"PA-SMOKE","givenName":"Priya","familyName":"Shah","phoneNumber":"9744400033"}'
# → {"status":"staged"}          (no client cert ⇒ TLS handshake fails, by design)

# 2. Reception logs in and fires the code (CSRF token ROTATES on login — re-read it)
J=/tmp/jar; rm -f $J
curl -sS -c $J localhost:9007/api/csrf >/dev/null
C1=$(grep csrf_token $J | tail -1 | awk '{print $NF}')
curl -sS -b $J -c $J -H "X-CSRF-Token: $C1" -H 'Content-Type: application/json' \
  -d '{"email":"reception@testhospital.local","password":"<pw>"}' localhost:9007/api/session
C2=$(grep csrf_token $J | tail -1 | awk '{print $NF}')     # post-login token
curl -sS -b $J localhost:9007/api/reception/registrations   # PA-SMOKE, status PENDING, mobile masked
curl -sS -b $J -H "X-CSRF-Token: $C2" -X POST \
  localhost:9007/api/reception/registrations/PA-SMOKE/send-code   # → {"status":"sent"}

# 3. Read the code from the mock SMS log, then complete at the kiosk
OTP=$(grep -arh "MOCK SMS" /data/logs/notification-service | tail -1 | grep -oE '[0-9]{6}')
RES=$(curl -sS -X POST localhost:9008/kiosk/api/claim/resolve \
  -H 'Content-Type: application/json' -d "{\"otp\":\"$OTP\"}")
echo "$RES"   # → {session_id, mobile, name:"Priya Shah", hms_patient_id:"PA-SMOKE"}
SID=$(echo "$RES" | grep -oE '"session_id":"[^"]+"' | sed 's/.*://;s/"//g')
curl -sS -X POST localhost:9008/kiosk/api/consent/capture -H 'Content-Type: application/json' \
  -d "{\"mobile\":\"9744400033\",\"session_id\":\"$SID\",\"hms_patient_id\":\"PA-SMOKE\",\"purposes\":[\"treatment\"]}"
# → 201 vault row; kiosk-bff then fires status DONE (the row leaves the reception queue)

# 4. The HMS-linked consent now resolves
curl -sS -X POST localhost:9000/api/v1/consent/check -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"hms_patient_id":"PA-SMOKE","purpose":"treatment"}'
# → {"allowed":true,...}
```

### Tenant-isolation regression (recommended before any deploy)
```bash
cd consent-service && ./test/run-isolation.sh    # 6 RLS/append-only checks
```

### Stop / reset
```bash
cd <service> && docker compose down     # a service
cd DPDP && docker compose down          # infra (keeps the volume)
cd DPDP && docker compose down -v       # wipe DB → next `up` re-migrates + re-seeds
```

---

## 5. Database migrations as a deploy step

Migrations are **decoupled from service startup** — services never migrate on
boot (a race across N tasks). Run `migrate.sh up` once per deploy, before rolling
the new service versions, using the **admin** DSN:

```bash
DATABASE_URL="postgres://<admin>:<pw>@<rds-endpoint>:5432/dpdp?sslmode=require" \
  DPDP/scripts/db/migrate.sh up
```

- Idempotent: re-running is a no-op once applied.
- Additive only (append-only tables); a new change is always a **new** numbered
  file — never edit an applied one.
- **Never run `seed` in production** (it inserts a fake test hospital).

---

## 6. Production deployment (AWS ap-south-1)

### 6a. Provision (once)
- **RDS PostgreSQL 16**, ap-south-1, private subnets, encryption at rest,
  automated backups, ≥7-year retention strategy for the audit ledger.
- **ElastiCache Redis 7**, ap-south-1.
- **Secrets Manager**: RS256 PEMs, `SERVICE_TOKEN_SECRET`, RDS creds (admin +
  `dpdp_app`), per-hospital keys + `SYSTEM_SALT`.
- **ECR** repos per service; **ECS Fargate** services behind an internal ALB;
  public ALB only in front of the hospital-facing endpoints.
- Security groups: only ECS tasks reach RDS/Redis; services reach each other over
  the private network by DNS name.
- **Public ingress mirrors the dev gateway's route table** (one manifest, two
  deployments) — TLS termination, `/internal/*` + `/v1/auth/service-token` blocked at
  the edge, per-route rate limits.
- **integration-service's mTLS webhook needs its own listener** — client-cert
  termination cannot share the public ALB/gateway (an ALB terminating TLS strips the
  client cert, which *is* the tenant identity). Give it a dedicated NLB/listener that
  passes TCP through to `:9443`, or terminate mTLS on the task itself. Its `:9009`
  internal API stays private.
- **Redis is load-bearing for the consent flow**, not just a cache — see §8.

### 6b. Database roles (once)
Create the least-privilege runtime role on RDS, then hand services **only** its
credential:
```bash
DATABASE_URL="postgres://<rds-master>:<pw>@<endpoint>:5432/dpdp?sslmode=require" \
  DPDP/scripts/db/migrate.sh up      # 0009_app_role creates dpdp_app + grants
```
Change `dpdp_app`'s password from the dev default before exposing it.

### 6c. CI/CD pipeline (per deploy)
1. `go build ./... && go vet ./...` for every Go module (auth, consent, audit,
   notification, emergency, integration, admin-bff, kiosk-bff, shared) **and**
   `npx vitest run` + `npx tsc --noEmit` for both frontends.
2. **Run the tenant-isolation suite** against a disposable Postgres
   (`go test -tags=integration ./test/...`) — gate the deploy on it.
3. Build + push images to ECR (build context = repo root for `replace ../shared`).
4. **`migrate.sh up`** against RDS (admin DSN from Secrets Manager).
5. Update ECS services (rolling), health checks on `/health`.
6. Post-deploy smoke test (token → capture → check) against the internal ALB.

### 6d. Config in prod
- `AWS_SECRETS_MOCK=false`; real hospital keys from Secrets Manager.
- `SMS_PROVIDER=msg91` with real `MSG91_*`.
- Distinct `SERVICE_TOKEN_SECRET`; PEMs injected from Secrets Manager (not mounted
  from a repo path).
- `sslmode=require` on all DSNs.

---

## 7. Health & verification

| Check | How |
|---|---|
| Liveness | `GET /health` on each port (200) |
| Migrations current | `migrate.sh status` → all applied |
| RLS enforced | tenant-isolation suite green; app connects as `dpdp_app` |
| Audit durable | capture with audit-service down → still 201, outbox row queued; on restart relay ships exactly once |
| Internal auth | `POST /internal/audit/log` with no/invalid service token → 401 |
| mTLS enforced | webhook **without** a client cert → TLS handshake fails (no HTTP response); with a cert → staged under the cert's CN |
| Edge blocks internals | `/internal/*` and `/v1/auth/service-token` via `:8080` → blocked |
| Reception least-privilege | a `reception` session → `GET /api/audit/logs` and `/api/consent/stats` → **403** |
| Front-desk flow | §4b end-to-end: staged → send-code → resolve → capture 201 → status `DONE` → check-by-`hms_patient_id` `allowed:true` |

---

## 8. Operational notes & rollback

- **App rollback ≠ schema rollback.** Migrations are additive; rolling back a
  service image is safe against a newer schema. Do **not** auto-run down-migrations
  against the audit ledger.
- **Stale-volume gotcha (local):** an old volume seeded before the
  bcrypt→SHA-256 API-key change holds a stale `api_key_hash` → token 401s. Fresh
  volumes are correct; otherwise re-seed or update the row.
- **`dpdp_app` is intentionally powerless:** no UPDATE/DELETE on
  `consent_vault`/`audit_log`. A "permission denied" on those in app logs is the
  append-only guarantee working, not a bug.
- **Secrets never in images:** keys/secrets are mounted/injected read-only;
  `.dockerignore` keeps them out of build context.
- **A Redis restart empties the front-desk queue.** Pending registrations (72h TTL),
  OTP claims, verified sessions, and admin-bff logins live **only** in Redis — by
  design (transient staging, no PII in a durable store). A restart/failover of a
  non-persistent Redis loses all of them: the reception queue goes empty, in-flight
  codes stop resolving, and staff are logged out. **Nothing already consented is
  lost** — the vault is Postgres. Recovery = the HMS re-fires webhooks (or reception
  re-stages) and re-sends codes. If that's unacceptable at pilot, enable AOF/RDB
  persistence (or a replicated ElastiCache) **before** go-live — it's a deliberate
  design trade, not a bug.
- **Docker vs `go run` env drift:** the BFFs' integration/notification URLs default to
  `localhost`. In containers they must be compose service names (§3c gotcha) — the
  symptom is reception "send code" and kiosk code entry failing *only* in Docker.
- **mTLS client certs are per-hospital and identity-bearing.** The CN is the tenant;
  a wrong-CN cert stages patients under the wrong hospital. Rotate/revoke per hospital.

---

## 9. Pre-deploy checklist

- [ ] `go build`/`go vet` clean across all Go modules (auth, consent, audit,
      notification, emergency, integration, admin-bff, kiosk-bff, shared)
- [ ] Frontend suites green (`frontend/kiosk`, `frontend/admin-dashboard`: `npx vitest run`)
- [ ] Tenant-isolation suite green (`consent-service/test/run-isolation.sh`)
- [ ] `migrate.sh status` → all applied (through **0014**) on the target DB
- [ ] Services connect as `dpdp_app`, migrations ran as admin
- [ ] `SERVICE_TOKEN_SECRET` matches (auth ↔ consent) and is not the dev default
- [ ] RS256 PEMs present; `AWS_SECRETS_MOCK=false`; `sslmode=require` (prod)
- [ ] **integration-service:** mTLS server cert + hospital CA mounted; each hospital's
      **client cert CN == its `hospital_id`**; `:9443` on its own listener (not the ALB);
      `:9009` private
- [ ] **BFFs:** `INTEGRATION_URL` / `NOTIFICATION_URL` / `INTEGRATION_SERVICE_URL` set to
      real hostnames (not the `localhost` defaults); `COOKIE_SECURE=true`; API key
      injected from Secrets Manager
- [ ] **Redis persistence** decided (AOF/RDB or replicated) — a restart drops the
      pending queue, live codes, and sessions (§8)
- [ ] Edge blocks `/internal/*` and `/v1/auth/service-token`
- [ ] RDS + Redis in **ap-south-1**
- [ ] `seed` **not** run against prod (and no `reception`/admin users seeded there)
