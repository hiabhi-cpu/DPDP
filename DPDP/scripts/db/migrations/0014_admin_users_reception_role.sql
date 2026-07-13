-- =============================================================================
-- 0014_admin_users_reception_role.sql
-- Add the 'reception' role to auth.admin_users.
--
-- The front-desk-driven consent flow (Spec B) introduces a least-privilege
-- RECEPTION role: front-desk staff who can see the "awaiting consent" queue and
-- fire a one-time code to a staged patient, but nothing else (no audit, no
-- consent stats, no emergency review). admin-bff enforces the endpoint scoping;
-- this migration just lets the role value exist, since 0012 pinned the CHECK
-- constraint to ('admin','dpo').
-- =============================================================================

ALTER TABLE auth.admin_users DROP CONSTRAINT IF EXISTS admin_users_role_check;

ALTER TABLE auth.admin_users
  ADD CONSTRAINT admin_users_role_check
  CHECK (role IN ('admin', 'dpo', 'reception'));

COMMENT ON COLUMN auth.admin_users.role IS
  'admin | dpo | reception. reception = front-desk consent-queue operator (Spec B), least-privilege.';
