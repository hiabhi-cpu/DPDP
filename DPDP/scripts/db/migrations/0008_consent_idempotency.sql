-- =============================================================================
-- 09_consent_idempotency.sql
-- Idempotent consent capture (product plan §19; required for offline kiosk sync).
-- A replayed capture with the same session_id must return the original record
-- rather than creating a duplicate or erroring.
--
-- The partial unique index enforces one CONSENT_GIVEN row per (hospital, key).
-- WITHDRAWAL/renewal rows carry NULL idempotency_key and are unaffected, so the
-- append-only version chain still works.
-- =============================================================================

ALTER TABLE consent.consent_vault
  ADD COLUMN IF NOT EXISTS idempotency_key VARCHAR;

CREATE UNIQUE INDEX IF NOT EXISTS uq_consent_idempotency
  ON consent.consent_vault (hospital_id, idempotency_key)
  WHERE type = 'CONSENT_GIVEN' AND idempotency_key IS NOT NULL;
