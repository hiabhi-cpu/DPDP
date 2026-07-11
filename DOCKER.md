# Running the stack with Docker (per-service compose)

Each service has its own `docker-compose.yml` in its directory (polyrepo style).
Postgres and Redis are **shared external infrastructure** — provisioned once,
independently of the services (in production these are managed RDS / ElastiCache).

## 1. One-time infra bootstrap

```bash
# Shared network the services attach to
docker network create dpdp-network

# Postgres — schema-per-service DB. Schema is NOT auto-applied on the volume; it
# is applied by the migration runner in the next step.
docker run -d --name dpdp-postgres \
  --network dpdp-network --network-alias postgres \
  -e POSTGRES_USER=abhi -e POSTGRES_PASSWORD=5004 -e POSTGRES_DB=dpdp \
  -p 5432:5432 \
  -v dpdp_postgres_data:/var/lib/postgresql/data \
  postgres:16-alpine

# Redis — OTP/session store
docker run -d --name dpdp-redis \
  --network dpdp-network --network-alias redis \
  -p 6379:6379 redis:7-alpine

# Apply the schema via tracked migrations (admin DSN). `up` creates schemas,
# tables, RLS, and the dpdp_app role; `seed` adds the local test hospital.
DATABASE_URL="postgres://abhi:5004@localhost:5432/dpdp?sslmode=disable" \
  DPDP/scripts/db/migrate.sh up
DATABASE_URL="postgres://abhi:5004@localhost:5432/dpdp?sslmode=disable" \
  DPDP/scripts/db/migrate.sh seed   # local/dev only — never in prod
```

Run these from the repo root. The `--network-alias postgres`/`redis` matter — the
services connect by those hostnames. Services connect to Postgres as the
least-privilege `dpdp_app` role (created by migration `0009_app_role`) so
Row-Level Security is enforced.

> **Tip:** `DPDP/docker-compose.yml` does all of the above (postgres + redis + a
> one-shot `migrate` that runs `up` then `seed`) in one command:
> `cd DPDP && docker compose up -d`.

> **Existing volume?** If you already have a `dpdp_postgres_data` volume built by
> the old `init/` scripts, adopt it instead of wiping:
> `DATABASE_URL=... DPDP/scripts/db/migrate.sh baseline 0010` (marks the current
> schema as applied without re-running it). See `DPDP/scripts/db/README.md`.

Prereq: JWT keys must exist at `auth-service/keys/{auth_private,auth_public}.pem`.
Generate once if missing:

```bash
openssl genrsa -out auth-service/keys/auth_private.pem 2048
openssl rsa -in auth-service/keys/auth_private.pem -pubout -out auth-service/keys/auth_public.pem
```

## 2. Run the services

Each from its own directory:

```bash
cd auth-service          && docker compose up -d --build
cd ../audit-service      && docker compose up -d --build
cd ../notification-service && docker compose up -d --build
cd ../consent-service    && docker compose up -d --build
cd ../emergency-service  && docker compose up -d --build
```

Order isn't critical — consent-service and emergency-service fetch their service
token and ship audit lazily with retries, so they tolerate auth/audit starting later.

Ports: auth `9006`, audit `9001`, notification `9004`, consent `9000`,
emergency `9005`.

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

## 3. Stop

```bash
cd <service> && docker compose down          # one service
docker rm -f dpdp-postgres dpdp-redis        # infra (data kept in the volume)
docker volume rm dpdp_postgres_data          # wipe DB (re-run migrate.sh after)
docker network rm dpdp-network               # remove the shared network
```

## 4. Log volume

All services bind-mount the host `/data/logs` directory for persistent application logs:

```bash
sudo mkdir -p /data/logs
sudo chown -R 1000:1000 /data/logs
```

Run this one-time setup before the first `docker compose up`. Logs are written to:
```
/data/logs/<service-name>/<yyyy-mm-dd>/{app.log,gin.log}
```

Example after running audit-service:
```
/data/logs/audit-service/2025-07-11/app.log      # application startup logs
/data/logs/audit-service/2025-07-11/gin.log      # HTTP request logs (also tees to stdout)
```

On container restart (`docker compose restart`), log files for the current day are rotated to `*-restarted-*`.

**Environment variables:**
- `LOG_LEVEL`: Set logging verbosity (default `info`; use `trace` for verbose dev logging).
- `LOG_DIR`: Set the logs base path (default `/data/logs`).

## Notes

- **Build context** is the repo root (`context: ..`) so the `replace ../shared`
  directive resolves; each Dockerfile copies only `shared/` and its own module.
- `SERVICE_TOKEN_SECRET` must match between `auth-service` (issuer) and
  `consent-service` (client). Both default to the same dev value; override via an
  env var or a service-local `.env` for real environments.
- Keys and secrets are mounted read-only, never baked into images (`.dockerignore`).
