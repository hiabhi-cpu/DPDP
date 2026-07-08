-- =============================================================================
-- 0012_admin_users.sql
-- Per-user admin/DPO accounts for the admin dashboard (Phase 1).
-- The dashboard BFF authenticates these users, then exchanges the hospital API
-- key for the hospital JWT server-side. hospital_id ties each admin to a tenant
-- so Phase-3 RBAC / multi-hospital slots in without a rewrite.
-- =============================================================================

CREATE EXTENSION IF NOT EXISTS citext;    -- case-insensitive email; enable before use

CREATE TABLE IF NOT EXISTS auth.admin_users (
  id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  hospital_id   UUID        NOT NULL REFERENCES auth.hospitals(id),
  email         CITEXT      NOT NULL UNIQUE,
  password_hash TEXT        NOT NULL,          -- bcrypt (cost 12)
  role          VARCHAR     NOT NULL DEFAULT 'admin' CHECK (role IN ('admin','dpo')),
  disabled      BOOLEAN     NOT NULL DEFAULT false,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_admin_users_hospital ON auth.admin_users (hospital_id);

-- The BFF connects as dpdp_app and looks users up by email before it knows the
-- hospital, so admin_users is NOT under RLS. Least-privilege: read + insert only
-- (password changes are a Phase-3 concern).
GRANT USAGE ON SCHEMA auth TO dpdp_app;
GRANT SELECT, INSERT ON auth.admin_users TO dpdp_app;

COMMENT ON TABLE auth.admin_users IS
  'Dashboard admin/DPO accounts. Authenticated by admin-bff; not RLS-scoped '
  '(looked up by email pre-tenant). password_hash is bcrypt cost 12.';
