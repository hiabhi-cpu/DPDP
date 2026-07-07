-- =============================================================================
-- 07_audit_log_event_id.sql
-- Idempotency key for the audit ledger.
-- The consent-service relay ships audit events at-least-once from a transactional
-- outbox (see 08_audit_outbox.sql). event_id lets audit-service dedupe replays via
-- ON CONFLICT DO NOTHING, so a retried delivery never creates a duplicate row.
--
-- A plain UNIQUE index is safe: Postgres treats NULLs as distinct, so pre-existing
-- rows (event_id IS NULL) do not collide.
-- =============================================================================

ALTER TABLE audit.audit_log
  ADD COLUMN IF NOT EXISTS event_id UUID;

CREATE UNIQUE INDEX IF NOT EXISTS uq_audit_log_event_id
  ON audit.audit_log (event_id);
