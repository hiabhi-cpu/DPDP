# Tenant-isolation test suite

The load-bearing proof that one hospital can never read, mutate, or delete
another hospital's consent rows (product plan Red Line 3). A broken RLS boundary
produces **no error and no crash** — the app works and just returns the wrong
tenant's data — so this is codified to catch that silent regression.

It hits a **real Postgres** (RLS can't be mocked) using two connections that
mirror production:

| Env var | Role | Used for |
|---|---|---|
| `TEST_ADMIN_DATABASE_URL` | superuser / owner | seeding only (dpdp_app can't INSERT hospitals) |
| `TEST_DATABASE_URL` | `dpdp_app` runtime role | every isolation assertion |

If either is unset the suite **skips** (so `go test ./...` stays green without a DB).

## Run

```bash
# local docker stack must be up with init scripts applied
./test/run-isolation.sh
# or directly:
TEST_ADMIN_DATABASE_URL=... TEST_DATABASE_URL=... \
  go test -tags=integration -v ./test/...
```

## What it asserts

1. **Read isolation** — under hospital A's context, see A's rows and **zero** of
   B's, even when the query explicitly names B's keys.
2. **Fail-closed** — a query with no `app.hospital_id` set **errors**, never
   returns everything.
3. **Bogus context** — a random UUID context returns 0 rows.
4. **Append-only (app role)** — `dpdp_app` UPDATE/DELETE on `consent_vault` →
   permission denied.
5. **Append-only (trigger)** — even the owner's UPDATE is blocked by the trigger.
6. **Role privilege** — the runtime role is **not** SUPERUSER and **not**
   BYPASSRLS (the exact bug that once made RLS inert).

## CI

Phase 3 wires this into GitHub Actions to run on every deploy (a service Postgres
with the init scripts, both DSNs exported). Build tag `integration` keeps it out
of unit runs.
