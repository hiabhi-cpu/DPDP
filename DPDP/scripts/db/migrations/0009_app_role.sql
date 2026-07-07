-- =============================================================================
-- 10_app_role.sql
-- Least-privilege runtime role — WITHOUT this, RLS is inert.
--
-- PostgreSQL skips ALL row-level security for superusers and for roles with the
-- BYPASSRLS attribute. If the services connect as the DB owner/superuser (e.g.
-- the local `abhi` or the RDS master user), the FORCE RLS policies on
-- consent_vault and audit_log are never consulted — the hospital data silo
-- (product plan Red Line 3) is not enforced at the DB layer.
--
-- The runtime services therefore connect as this dedicated role:
--   * NOSUPERUSER, NOBYPASSRLS  → RLS policies actually apply
--   * not the table owner        → no owner exemption (FORCE would cover it anyway)
--   * NO UPDATE/DELETE on the append-only tables → a third append-only backstop
--     alongside the trigger (consent_vault) and no_update/no_delete policies
--     (audit_log)
--
-- Admin tasks (running these migrations, container bootstrap) still use the
-- superuser in docker-compose. Only the app's DATABASE_URL uses dpdp_app.
--
-- NOTE: change the password for any non-local environment.
-- =============================================================================

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'dpdp_app') THEN
    CREATE ROLE dpdp_app
      WITH LOGIN PASSWORD 'dpdp_app_local_dev_pw'
      NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE;
  END IF;
END
$$;

-- Schema access
GRANT USAGE ON SCHEMA auth    TO dpdp_app;
GRANT USAGE ON SCHEMA consent TO dpdp_app;
GRANT USAGE ON SCHEMA audit   TO dpdp_app;

-- auth: runtime only reads hospital records (onboarding is a separate admin path)
GRANT SELECT ON auth.hospitals TO dpdp_app;

-- consent_vault: append-only — SELECT + INSERT, deliberately no UPDATE/DELETE
GRANT SELECT, INSERT ON consent.consent_vault TO dpdp_app;

-- audit_outbox: internal queue — relay marks rows shipped, so UPDATE is needed
GRANT SELECT, INSERT, UPDATE ON consent.audit_outbox TO dpdp_app;

-- audit_log: append-only ledger — SELECT + INSERT, no UPDATE/DELETE
GRANT SELECT, INSERT ON audit.audit_log TO dpdp_app;
-- BIGSERIAL id needs sequence access for INSERT
GRANT USAGE, SELECT ON SEQUENCE audit.audit_log_id_seq TO dpdp_app;
