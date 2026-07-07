-- =============================================================================
-- 08_audit_outbox.sql
-- Transactional outbox for audit events (consent-service).
--
-- Why: audit records are the legal shield. A consent write and its audit event
-- must be atomic — we must never commit a consent with no audit trail. Because all
-- services share one database, consent-service writes the audit event into this
-- outbox in the SAME transaction as the consent_vault row. A background relay then
-- ships unshipped rows to audit-service (/internal/audit/log) with retries.
--
-- id doubles as the canonical event identifier: it is sent as audit_log.event_id,
-- giving end-to-end idempotency for at-least-once delivery.
--
-- No hospital RLS: this is an internal service queue, not hospital-facing data.
-- The relay reads across all hospitals. Note the payload contains hospital_id and
-- patient_key — treat this table as sensitive internal infrastructure.
-- =============================================================================

CREATE TABLE IF NOT EXISTS consent.audit_outbox (
  id          UUID        PRIMARY KEY,          -- canonical event id == audit_log.event_id
  payload     JSONB       NOT NULL,             -- marshaled audit event
  attempts    INT         NOT NULL DEFAULT 0,
  last_error  TEXT,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  shipped_at  TIMESTAMPTZ                       -- NULL until successfully delivered
);

-- Relay scan: oldest-first over undelivered rows.
CREATE INDEX IF NOT EXISTS idx_audit_outbox_unshipped
  ON consent.audit_outbox (created_at)
  WHERE shipped_at IS NULL;

COMMENT ON TABLE consent.audit_outbox IS
  'Transactional outbox: audit events written atomically with consent_vault rows, '
  'shipped to audit-service by the relay. id == audit_log.event_id (idempotency).';
