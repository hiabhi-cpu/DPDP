# Deployment Guide — DPDP Consent Manager

How to deploy the backend services: **auth**, **consent**, **audit**,
**notification**, **emergency**. Covers local (Docker) and production (AWS
ap-south-1). Companion docs: `DOCKER.md` (per-service compose reference),
`DPDP/scripts/db/README.md` (migrations), `HARDENING_CHANGELOG.md`.

---

## 1. Topology

```
                       ┌───────────────────────────────────────────┐
   hospital API key →  │  auth-service :9006   (issues JWT + service tokens) │
                       └───────────────────────────────────────────┘
   patient / kiosk  →  ┌─────────────────┐   service JWT   ┌────────────────┐
   Bearer JWT          │ consent-service │ ──────────────▶ │ audit-service  │
                       │      :9000      │  outbox relay   │     :9001      │
                       └────────┬────────┘                 └────────────────┘
                                │ OTP
                       ┌────────▼────────────┐
                       │ notification-service│  :9004  (Redis OTP store)
                       └─────────────────────┘

   Shared infra:  PostgreSQL 16 (one `dpdp` DB, schema-per-service, RLS)
                  Redis 7 (OTP / session TTL)
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

Ports: auth `9006` · audit `9001` · notification `9004` · consent `9000` ·
emergency `9005`.

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

Each service also ships a `.env.example` — copy to `.env` and fill in.

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
docker logs dpdp-migrate            # → "applied 10 migration(s)" (first run)
DATABASE_URL="postgres://abhi:5004@localhost:5432/dpdp?sslmode=disable" \
  DPDP/scripts/db/migrate.sh status # all applied
```

> **Already have an old volume** (built by the retired `init/` scripts)?
> Don't wipe — adopt it: `migrate.sh baseline 0010` (see
> `DPDP/scripts/db/README.md`).

### Step 2 — keys
Generate the RS256 keypair (§3a) if `auth-service/keys/` is empty.

### Step 3 — services
```bash
cd auth-service            && docker compose up -d --build
cd ../audit-service        && docker compose up -d --build
cd ../notification-service && docker compose up -d --build
cd ../consent-service      && docker compose up -d --build
cd ../emergency-service    && docker compose up -d --build
```
(Order isn't critical; consent/emergency retry auth/audit.)

### Step 4 — smoke test
```bash
for p in 9006 9001 9004 9000 9005; do curl -sf localhost:$p/health && echo " :$p ok"; done

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

### 6b. Database roles (once)
Create the least-privilege runtime role on RDS, then hand services **only** its
credential:
```bash
DATABASE_URL="postgres://<rds-master>:<pw>@<endpoint>:5432/dpdp?sslmode=require" \
  DPDP/scripts/db/migrate.sh up      # 0009_app_role creates dpdp_app + grants
```
Change `dpdp_app`'s password from the dev default before exposing it.

### 6c. CI/CD pipeline (per deploy)
1. `go build ./... && go vet ./...` for all six modules.
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

---

## 9. Pre-deploy checklist

- [ ] `go build`/`go vet` clean across all six modules
- [ ] Tenant-isolation suite green (`consent-service/test/run-isolation.sh`)
- [ ] `migrate.sh status` → all applied on the target DB
- [ ] Services connect as `dpdp_app`, migrations ran as admin
- [ ] `SERVICE_TOKEN_SECRET` matches (auth ↔ consent) and is not the dev default
- [ ] RS256 PEMs present; `AWS_SECRETS_MOCK=false`; `sslmode=require` (prod)
- [ ] RDS + Redis in **ap-south-1**
- [ ] `seed` **not** run against prod
