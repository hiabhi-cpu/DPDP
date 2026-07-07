#!/bin/sh
# =============================================================================
# migrate.sh — minimal, dependency-free SQL migration runner (psql-based).
#
# Why this exists: the old scripts/db/init/ files ran ONCE on an empty Docker
# volume and never again (Postgres /docker-entrypoint-initdb.d semantics). That
# is a bootstrap, not a migration system — it cannot evolve a live database
# (e.g. an RDS instance holding real pilot data). This runner tracks which
# migrations have been applied to a given database in public.schema_migrations,
# so `up` applies only what is pending, identically on a laptop and on RDS, with
# no wipe.
#
# The migration files are plain, tool-agnostic SQL (no goose/-migrate markers),
# so swapping in another tool later is mechanical.
#
# Requires: `psql` on PATH and DATABASE_URL pointing at an ADMIN / superuser DSN.
#   Migrations do DDL and CREATE ROLE — the runtime `dpdp_app` role deliberately
#   cannot. Keep migrations on the admin credential; keep the app on dpdp_app.
#
# Usage:
#   DATABASE_URL=postgres://user:pw@host:5432/dpdp?sslmode=disable ./migrate.sh <command>
#
# Commands:
#   up                  apply all pending migrations (default)
#   status              show applied vs pending migrations
#   baseline <version>  mark every migration up to and including <version> as
#                       applied WITHOUT running it — adopt a DB that already has
#                       the schema (e.g. an existing volume built from init/).
#   seed                apply dev seed data (seeds/*.sql) — LOCAL / DEV ONLY.
# =============================================================================
set -eu

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
MIGRATIONS_DIR="${MIGRATIONS_DIR:-$SCRIPT_DIR/migrations}"
SEEDS_DIR="${SEEDS_DIR:-$SCRIPT_DIR/seeds}"

if [ -z "${DATABASE_URL:-}" ]; then
  echo "error: DATABASE_URL is not set (needs an admin/superuser DSN)" >&2
  exit 1
fi

# -X ignores ~/.psqlrc; ON_ERROR_STOP makes any SQL error fail the command.
# client_min_messages=warning hides routine NOTICEs from bookkeeping queries.
psql_q() { PGOPTIONS='-c client_min_messages=warning' psql "$DATABASE_URL" -X -qtA -v ON_ERROR_STOP=1 "$@"; }

ensure_table() {
  psql_q -c "CREATE TABLE IF NOT EXISTS public.schema_migrations (
    version    TEXT        PRIMARY KEY,
    name       TEXT        NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
  );" >/dev/null
}

# version_of <path> → leading numeric token, e.g. 0003_consent_vault.sql → 0003
version_of() { basename "$1" .sql | cut -d_ -f1; }

is_applied() {
  found=$(psql_q -c "SELECT 1 FROM public.schema_migrations WHERE version = '$1' LIMIT 1;")
  [ "$found" = "1" ]
}

cmd_up() {
  ensure_table
  applied=0
  for f in "$MIGRATIONS_DIR"/*.sql; do
    [ -e "$f" ] || continue
    v=$(version_of "$f")
    name=$(basename "$f" .sql)
    if is_applied "$v"; then continue; fi
    echo "→ applying $name"
    # --single-transaction wraps the migration AND its bookkeeping insert in one
    # transaction: either both land or neither does. No partially-applied state.
    psql "$DATABASE_URL" -X -q -v ON_ERROR_STOP=1 --single-transaction \
      -f "$f" \
      -c "INSERT INTO public.schema_migrations (version, name) VALUES ('$v', '$name');"
    applied=$((applied + 1))
  done
  if [ "$applied" -eq 0 ]; then
    echo "already up to date"
  else
    echo "applied $applied migration(s)"
  fi
}

cmd_status() {
  ensure_table
  echo "  status   version  name"
  echo "  -------  -------  ----------------------------------"
  for f in "$MIGRATIONS_DIR"/*.sql; do
    [ -e "$f" ] || continue
    v=$(version_of "$f")
    name=$(basename "$f" .sql)
    if is_applied "$v"; then mark="applied"; else mark="PENDING"; fi
    printf "  %-7s  %-7s  %s\n" "$mark" "$v" "$name"
  done
}

# baseline <version>: adopt an existing schema. Marks all migrations with a
# version <= the argument as applied, without executing them. Zero-padded
# versions (0001..) compare correctly as strings.
cmd_baseline() {
  target="${1:-}"
  if [ -z "$target" ]; then
    echo "usage: migrate.sh baseline <version>   e.g. baseline 0010" >&2
    exit 1
  fi
  ensure_table
  marked=0
  for f in "$MIGRATIONS_DIR"/*.sql; do
    [ -e "$f" ] || continue
    v=$(version_of "$f")
    name=$(basename "$f" .sql)
    if [ "$v" \> "$target" ]; then continue; fi
    if is_applied "$v"; then continue; fi
    psql_q -c "INSERT INTO public.schema_migrations (version, name) VALUES ('$v', '$name');" >/dev/null
    echo "→ baselined $name (marked applied, not run)"
    marked=$((marked + 1))
  done
  [ "$marked" -eq 0 ] && echo "nothing to baseline (already recorded)" || echo "baselined $marked migration(s)"
}

cmd_seed() {
  for f in "$SEEDS_DIR"/*.sql; do
    [ -e "$f" ] || { echo "no seed files in $SEEDS_DIR"; return 0; }
    echo "→ seeding $(basename "$f")"
    psql "$DATABASE_URL" -X -q -v ON_ERROR_STOP=1 --single-transaction -f "$f"
  done
}

cmd="${1:-up}"
case "$cmd" in
  up)       cmd_up ;;
  status)   cmd_status ;;
  baseline) shift; cmd_baseline "${1:-}" ;;
  seed)     cmd_seed ;;
  *) echo "unknown command: $cmd (want: up | status | baseline <v> | seed)" >&2; exit 1 ;;
esac
