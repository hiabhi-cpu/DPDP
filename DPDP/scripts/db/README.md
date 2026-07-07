# Database migrations

Schema changes are **tracked migrations**, not run-once bootstrap scripts.

## Why this replaced `init/`

The old `scripts/db/init/*.sql` files were mounted into Postgres at
`/docker-entrypoint-initdb.d`, which runs them **once, on an empty volume, and
never again**. That is a bootstrap, not a migration system: it cannot add a
column to a live database (e.g. an RDS instance holding real pilot consents)
without wiping it, and it silently drifts as people hand-patch running DBs. See
`../../../plan-phase.md` (Phase 1, migration tooling).

Now `migrate.sh` records every applied migration in `public.schema_migrations`,
so `up` applies only what is pending — identically on a laptop and on RDS, no wipe.

## Layout

```
scripts/db/
  migrate.sh            # the runner (psql-based, dependency-free, POSIX sh)
  migrations/           # tracked, ordered, tool-agnostic plain SQL
    0001_create_schemas.sql
    ...
    0010_purpose_status.sql
  seeds/                # dev-only data — NEVER applied to a real database
    dev_seed_test_hospital.sql
```

## Commands

`migrate.sh` needs `DATABASE_URL` set to an **admin / superuser** DSN (migrations
do DDL and `CREATE ROLE`; the runtime `dpdp_app` role cannot).

```bash
export DATABASE_URL="postgres://abhi:5004@localhost:5432/dpdp?sslmode=disable"

./migrate.sh up                 # apply all pending migrations
./migrate.sh status             # show applied vs pending
./migrate.sh baseline 0010      # mark 0001..0010 applied WITHOUT running (adopt existing schema)
./migrate.sh seed               # apply seeds/*.sql — LOCAL/DEV ONLY
```

## Rules

1. **Never edit an applied migration.** Every change is a **new** file with the
   next number. Editing a file that has already run somewhere means environments
   diverge — the exact failure this system exists to prevent.
2. **Additive & reversible-in-practice.** `consent_vault` / `audit_log` are
   append-only; migrations add columns/indexes/policies, they don't rewrite data.
3. **Seeds are not migrations.** `seeds/` is dev fixtures. Running it against a
   real DB would insert a fake hospital — run only `up` in production.

## Adopting an existing volume (no wipe)

A `dpdp_postgres_data` volume created by the old `init/` scripts already has the
full schema. Register it as migrated without re-running (which would error on
duplicate policies/triggers):

```bash
DATABASE_URL=... ./migrate.sh baseline 0010
DATABASE_URL=... ./migrate.sh status     # all applied
DATABASE_URL=... ./migrate.sh up         # no-op
```

## Migration ↔ old init-script mapping

| Migration | Was |
|---|---|
| `0001_create_schemas` | `01_create_schemas` |
| `0002_hospitals_table` | `02_hospitals_table` |
| `0003_consent_vault` | `03_consent_vault` |
| `0004_audit_log` | `04_audit_log` |
| `0005_otp_sessions` | `05_otp_sessions` |
| `0006_audit_log_event_id` | `07_audit_log_event_id` |
| `0007_audit_outbox` | `08_audit_outbox` |
| `0008_consent_idempotency` | `09_consent_idempotency` |
| `0009_app_role` | `10_app_role` |
| `0010_purpose_status` | `11_purpose_status` |
| `seeds/dev_seed_test_hospital` | `06_seed_test_hospital` (was inline; now dev-only) |

## Future: goose

Migration files are plain SQL with no tool-specific markers, so adopting
[goose](https://github.com/pressly/goose) or golang-migrate later is mechanical
(add up/down markers, point it at `migrations/`). This runner exists because the
build environment is offline; the *format* is already tool-agnostic.
