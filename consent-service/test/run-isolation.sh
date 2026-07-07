#!/usr/bin/env bash
# Runs the tenant-isolation suite against a real Postgres.
#
#   TEST_ADMIN_DATABASE_URL — superuser/owner, used ONLY to seed test data
#                             (dpdp_app cannot INSERT hospitals).
#   TEST_DATABASE_URL       — the dpdp_app runtime role; every isolation
#                             assertion runs under it.
#
# Both must point at the same `dpdp` database with the init scripts applied.
# Defaults match the local docker stack; override via the environment.
set -euo pipefail

export TEST_ADMIN_DATABASE_URL="${TEST_ADMIN_DATABASE_URL:-postgres://abhi:5004@localhost:5432/dpdp?sslmode=disable}"
export TEST_DATABASE_URL="${TEST_DATABASE_URL:-postgres://dpdp_app:dpdp_app_local_dev_pw@localhost:5432/dpdp?sslmode=disable}"

cd "$(dirname "$0")/.."
echo "→ tenant-isolation suite (app role = dpdp_app)"
go test -tags=integration -v ./test/...
