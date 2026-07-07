-- =============================================================================
-- 03_consent_vault.sql
-- APPEND-ONLY consent artifact store.
-- Architecture rules (from product plan v2 §7):
--   - Never UPDATE or DELETE — enforced at both DB level (trigger) and app level
--   - RLS isolates each hospital's rows — cross-hospital queries return 0 rows
--   - patient_key = HMAC_SHA256(mobile + SYSTEM_SALT + hospital_key) — raw mobile NEVER stored
--   - Each change creates a NEW row with version++ and previous_id pointing to prior row
-- =============================================================================

CREATE TABLE IF NOT EXISTS consent.consent_vault (
  -- ── Identity ──────────────────────────────────────────────────────────────
  id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  hospital_id         UUID        NOT NULL REFERENCES auth.hospitals(id),
  patient_key         VARCHAR(72) NOT NULL,  -- versioned: "v1|<64-char HMAC hex>"
  hms_patient_id      VARCHAR,               -- opaque HMS ID e.g. "PA-00234"

  -- ── Consent type & status ─────────────────────────────────────────────────
  type                VARCHAR     NOT NULL
                      CHECK (type IN (
                        'CONSENT_GIVEN',
                        'WITHDRAWAL',
                        'EMERGENCY_OVERRIDE',
                        'RETROSPECTIVE_CONSENT',
                        'CONSENT_RENEWAL'
                      )),
  status              VARCHAR     NOT NULL
                      CHECK (status IN ('ACTIVE','WITHDRAWN','EXPIRED','PENDING_RETROSPECTIVE')),
  legal_basis         VARCHAR     NOT NULL DEFAULT 'EXPLICIT_CONSENT'
                      CHECK (legal_basis IN ('EXPLICIT_CONSENT','DPDP_SECTION_7B')),

  -- ── Consent content ───────────────────────────────────────────────────────
  purposes            JSONB       NOT NULL DEFAULT '[]',  -- e.g. ["treatment","insurance"]
  notice_version      VARCHAR     NOT NULL DEFAULT 'v1.0',
  language            VARCHAR(10) NOT NULL DEFAULT 'en',

  -- ── Capture metadata ──────────────────────────────────────────────────────
  otp_verified        BOOLEAN     NOT NULL DEFAULT false,
  staff_override_id   VARCHAR,               -- populated if manual (no OTP)
  kiosk_id            VARCHAR,

  -- ── Version chain (append-only history) ───────────────────────────────────
  previous_id         UUID        REFERENCES consent.consent_vault(id),
  version             INTEGER     NOT NULL DEFAULT 1,

  -- ── Integrity hash ────────────────────────────────────────────────────────
  -- SHA256 of all above fields concatenated — tamper evidence
  artifact_hash       VARCHAR(64) NOT NULL,

  -- ── Timestamps — immutable once written ───────────────────────────────────
  created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

  -- ── Emergency-specific fields (null for non-emergency rows) ───────────────
  doctor_id           VARCHAR,
  emergency_reason    VARCHAR,
  clinical_note       TEXT,
  dpo_review_status   VARCHAR     CHECK (dpo_review_status IN ('PENDING','VERIFIED','FLAGGED')),
  dpo_review_by       UUID,
  dpo_review_at       TIMESTAMPTZ,
  dpo_deadline        TIMESTAMPTZ,          -- now() + 72h for emergency records
  patient_notified_at TIMESTAMPTZ
);

-- ── Indexes ───────────────────────────────────────────────────────────────────
-- Primary lookup: "does this patient have active consent at this hospital?"
CREATE INDEX IF NOT EXISTS idx_cv_patient_hospital_status
  ON consent.consent_vault (hospital_id, patient_key, status);

-- HMS ID lookup (doctor badge check path)
CREATE INDEX IF NOT EXISTS idx_cv_hms_patient_id
  ON consent.consent_vault (hospital_id, hms_patient_id);

-- DPO emergency review queue
CREATE INDEX IF NOT EXISTS idx_cv_emergency_pending
  ON consent.consent_vault (hospital_id, dpo_review_status)
  WHERE type = 'EMERGENCY_OVERRIDE';

-- ── Row Level Security ────────────────────────────────────────────────────────
ALTER TABLE consent.consent_vault ENABLE ROW LEVEL SECURITY;
ALTER TABLE consent.consent_vault FORCE ROW LEVEL SECURITY;

-- Every SELECT/INSERT is filtered to only the current hospital_id session variable.
-- Services must run: SET LOCAL app.hospital_id = '<uuid>' before every query.
CREATE POLICY hospital_isolation ON consent.consent_vault
  USING (hospital_id = current_setting('app.hospital_id')::uuid);

-- ── Append-only trigger ───────────────────────────────────────────────────────
-- Belt-and-suspenders: blocks UPDATE/DELETE even if app code has a bug.
CREATE OR REPLACE FUNCTION consent.prevent_mutation()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION
    'consent_vault is append-only. UPDATE/DELETE are forbidden. Create a new row instead.'
    USING ERRCODE = 'restrict_violation';
END;
$$;

CREATE TRIGGER trg_consent_vault_no_update
  BEFORE UPDATE ON consent.consent_vault
  FOR EACH ROW EXECUTE FUNCTION consent.prevent_mutation();

CREATE TRIGGER trg_consent_vault_no_delete
  BEFORE DELETE ON consent.consent_vault
  FOR EACH ROW EXECUTE FUNCTION consent.prevent_mutation();

COMMENT ON TABLE consent.consent_vault IS
  'Immutable consent artifact store. Append-only enforced by trigger + RLS. '
  'Raw patient mobile is never stored — only HMAC_SHA256(mobile+SYSTEM_SALT+hospital_key).';
