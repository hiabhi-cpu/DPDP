-- =============================================================================
-- 0013_section9_guardian.sql
-- §9 (children / persons with disability) schema groundwork.
--
-- DPDP §9 requires a verified GUARDIAN to consent on behalf of a minor. The
-- vault today assumes every row is an adult consenting for themselves. This
-- migration adds the columns needed to record a minor + their guardian.
--
-- SCHEMA ONLY. The age-gate, guardian OTP routing, and enforcement live in the
-- P2 kiosk guardian flow (plan-phase.md). Columns are added now — while the
-- table is empty/pre-pilot — because adding them after real consent rows exist
-- means backfilling legal-evidence rows in an append-only vault.
--
-- Design decisions (see docs/superpowers/specs/2026-07-11-section9-schema-groundwork-design.md):
--   - No raw guardian PII in the vault. Guardian mobile is stored as an HMAC
--     (guardian_key) using the SAME scheme as patient_key. Guardian NAME is not
--     stored here — it belongs in the notice/receipt artifact, not the queryable
--     vault, preserving the "no raw PII" invariant.
--   - data_principal_type is ADULT/CHILD only. "Guardian consented" is implied
--     by guardian_key IS NOT NULL, not a third enum value.
--   - All columns nullable-or-defaulted: existing rows become ADULT / SELF with
--     null guardian columns. No data migration; append-only rule (rule #2) held.
--
-- Deliberately NOT added yet (add WITH the P2 flow, when the write path is known):
--   - Cross-column CHECK (CHILD => guardian_key NOT NULL): the staged kiosk write
--     path is not defined yet; a hard constraint now could fight partial writes.
--   - Index on data_principal_type: nothing reads it until the P2 flow.
-- =============================================================================

ALTER TABLE consent.consent_vault
  ADD COLUMN IF NOT EXISTS data_principal_type   VARCHAR NOT NULL DEFAULT 'ADULT'
                           CHECK (data_principal_type IN ('ADULT','CHILD')),
  ADD COLUMN IF NOT EXISTS guardian_relationship VARCHAR
                           CHECK (guardian_relationship IN ('MOTHER','FATHER','LEGAL_GUARDIAN')),
  ADD COLUMN IF NOT EXISTS guardian_key          VARCHAR(72),
  ADD COLUMN IF NOT EXISTS otp_mobile_owner      VARCHAR NOT NULL DEFAULT 'SELF'
                           CHECK (otp_mobile_owner IN ('SELF','GUARDIAN'));

COMMENT ON COLUMN consent.consent_vault.data_principal_type IS
  '§9: ADULT (self-consent) or CHILD. Guardian consent is implied by guardian_key IS NOT NULL.';
COMMENT ON COLUMN consent.consent_vault.guardian_relationship IS
  '§9 guardian relationship to the minor. Null for ADULT rows.';
COMMENT ON COLUMN consent.consent_vault.guardian_key IS
  'HMAC of the guardian mobile, same scheme as patient_key ("v1|<hex>"). Raw guardian mobile is never stored. Null for self-consent.';
COMMENT ON COLUMN consent.consent_vault.otp_mobile_owner IS
  'Whose mobile received the verification OTP: SELF or GUARDIAN. §9 accountability.';
