-- =============================================================================
-- 0016_audit_hms_patient_index.sql
-- Make the per-patient audit trail reachable for events that have no
-- patient_key to narrow by.
--
-- Context: identity is the pair (patient_key, hms_patient_id) — see 0015.
-- audit_log.patient_key is derived from the mobile, so it names a HOUSEHOLD;
-- details->>'hms_patient_id' names the individual. For most events the pair
-- works: idx_audit_patient_key narrows to the household (a handful of rows) and
-- the JSON field picks the person.
--
-- CONSENT_MISSING_ACCESS_ATTEMPT is the exception, and the one that matters
-- most: it is written when NO consent row exists, so there is no row to read a
-- patient_key off and the column is null. Without this index, a subject-access
-- or regulator query for one patient returns their grants and withdrawals but
-- silently MISSES every unauthorised-access attempt against them.
--
-- See docs/superpowers/specs/2026-07-20-patient-identity-key-design.md
-- =============================================================================

CREATE INDEX IF NOT EXISTS idx_audit_details_hms
  ON audit.audit_log (hospital_id, (details ->> 'hms_patient_id'))
  WHERE details ? 'hms_patient_id';

COMMENT ON INDEX audit.idx_audit_details_hms IS
  'Per-patient audit lookup by HMS patient ID. Required because patient_key names a household, and is null entirely on CONSENT_MISSING_ACCESS_ATTEMPT rows (no consent row exists to derive it from).';
