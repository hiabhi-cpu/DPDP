-- =============================================================================
-- 0015_patient_identity.sql
-- Identity on a consent row is the PAIR (patient_key, hms_patient_id).
--
-- patient_key is HMAC(mobile) — it identifies a CONTACT CHANNEL, not a person.
-- A family routinely shares one mobile number, so patient_key alone collapses
-- every member of a household onto one identity. hms_patient_id names the
-- individual.
--
-- See docs/superpowers/specs/2026-07-20-patient-identity-key-design.md
--
-- Key derivation is UNCHANGED, so no artifact_hash is invalidated and no row
-- needs rehashing.
--
-- This migration does not delete data. If it fails with a constraint violation,
-- the database holds consent rows with no hms_patient_id — decide deliberately
-- whether to backfill or truncate. Pre-pilot, truncate (see the plan). A
-- tracked migration must never silently wipe an append-only evidence table.
-- =============================================================================

ALTER TABLE consent.consent_vault
  ADD CONSTRAINT chk_consent_rows_have_hms_patient_id
  CHECK (type = 'EMERGENCY_OVERRIDE' OR hms_patient_id IS NOT NULL);

COMMENT ON CONSTRAINT chk_consent_rows_have_hms_patient_id ON consent.consent_vault IS
  'Consent rows must name a patient. Only EMERGENCY_OVERRIDE is exempt: an unconscious patient may have neither mobile nor HMS ID.';

COMMENT ON COLUMN consent.consent_vault.patient_key IS
  'HMAC of the mobile ("v1|<hex>"), hospital-scoped. A CONTACT CHANNEL, not an identity — families share one number. Identity is (patient_key, hms_patient_id).';

COMMENT ON COLUMN consent.consent_vault.hms_patient_id IS
  'Opaque HMS patient ID e.g. "PA-00234". Names the individual. Required on all consent rows; null only on EMERGENCY_OVERRIDE.';

COMMENT ON TABLE consent.consent_vault IS
  'Immutable consent artifact store. Append-only enforced by trigger + RLS. '
  'Raw patient mobile is never stored — only HMAC_SHA256(mobile+SYSTEM_SALT+hospital_key). '
  'A patient is identified by (patient_key, hms_patient_id), never patient_key alone.';
