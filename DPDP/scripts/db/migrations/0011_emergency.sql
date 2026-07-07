-- =============================================================================
-- 0011_emergency.sql
-- Emergency access (DPDP Act Section 7(b) — deemed consent) + DPO review queue.
-- Product plan v2 §12.
--
-- Design:
--   * The IMMUTABLE access record is a consent_vault row (type EMERGENCY_OVERRIDE,
--     legal_basis DPDP_SECTION_7B) — the legal evidence that access happened. It
--     is never mutated (append-only trigger + no UPDATE grant for dpdp_app).
--   * The MUTABLE DPO review workflow lives here in emergency.reviews. The
--     consent_vault has dpo_review_* columns, but consent_vault is append-only —
--     they cannot be UPDATEd — so the review state machine needs its own table.
--   * emergency.audit_outbox mirrors consent.audit_outbox: emergency-service
--     writes its EMERGENCY_ACCESS / DPO_REVIEW_COMPLETED audit events here in the
--     same transaction as the domain write, and its relay ships them.
-- =============================================================================

-- ── consent_vault: identity may be UNKNOWN in an emergency ───────────────────
-- An unconscious patient with no mobile on file has no patient_key. Relax the
-- NOT NULL so an emergency access can be recorded without identity. Consent
-- flows always set it, so nothing else is affected.
ALTER TABLE consent.consent_vault ALTER COLUMN patient_key DROP NOT NULL;

CREATE SCHEMA IF NOT EXISTS emergency;

-- ── DPO review queue — MUTABLE operational workflow ──────────────────────────
CREATE TABLE IF NOT EXISTS emergency.reviews (
  -- access_id == the consent_vault EMERGENCY_OVERRIDE row id (the evidence row).
  access_id        UUID        PRIMARY KEY REFERENCES consent.consent_vault(id),
  hospital_id      UUID        NOT NULL REFERENCES auth.hospitals(id),
  emergency_ref    VARCHAR     NOT NULL,          -- human ref, e.g. EMRG-2026-0042

  -- Denormalized display fields so the queue is self-contained (no vault join).
  doctor_id        VARCHAR     NOT NULL,
  emergency_reason VARCHAR     NOT NULL,
  clinical_note    VARCHAR(200),
  hms_patient_id   VARCHAR,

  -- Review state machine.
  review_status    VARCHAR     NOT NULL DEFAULT 'PENDING'
                   CHECK (review_status IN ('PENDING','VERIFIED','FLAGGED')),
  dpo_deadline     TIMESTAMPTZ NOT NULL,          -- created_at + 72h
  reviewed_by      VARCHAR,
  reviewed_at      TIMESTAMPTZ,
  escalation_sent_at  TIMESTAMPTZ,                -- set when a >72h overdue alert fires (follow-up)
  patient_notified_at TIMESTAMPTZ,                -- set when the §12.5 SMS is sent (follow-up)
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Pending / overdue queue scan, per hospital.
CREATE INDEX IF NOT EXISTS idx_emergency_reviews_pending
  ON emergency.reviews (hospital_id, review_status, dpo_deadline);

-- Row Level Security: a hospital's DPO sees only that hospital's queue.
ALTER TABLE emergency.reviews ENABLE ROW LEVEL SECURITY;
ALTER TABLE emergency.reviews FORCE ROW LEVEL SECURITY;
CREATE POLICY hospital_isolation ON emergency.reviews
  USING (hospital_id = current_setting('app.hospital_id')::uuid);

COMMENT ON TABLE emergency.reviews IS
  'Mutable DPO review workflow for emergency (Section 7(b)) accesses. The immutable '
  'access record is the consent_vault EMERGENCY_OVERRIDE row (access_id). Overdue is '
  'derived: review_status=PENDING AND dpo_deadline < now().';

-- ── Audit outbox (transactional) — internal queue, no RLS ────────────────────
CREATE TABLE IF NOT EXISTS emergency.audit_outbox (
  id          UUID        PRIMARY KEY,            -- canonical event id == audit_log.event_id
  payload     JSONB       NOT NULL,
  attempts    INT         NOT NULL DEFAULT 0,
  last_error  TEXT,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  shipped_at  TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_emergency_outbox_unshipped
  ON emergency.audit_outbox (created_at)
  WHERE shipped_at IS NULL;

COMMENT ON TABLE emergency.audit_outbox IS
  'Transactional outbox: emergency audit events written atomically with the domain '
  'write, shipped to audit-service by the relay. id == audit_log.event_id (idempotency).';

-- ── Least-privilege grants for the runtime role ──────────────────────────────
GRANT USAGE ON SCHEMA emergency TO dpdp_app;
-- reviews is a mutable workflow table → UPDATE is allowed (unlike the vault).
GRANT SELECT, INSERT, UPDATE ON emergency.reviews TO dpdp_app;
-- outbox: relay marks rows shipped → needs UPDATE.
GRANT SELECT, INSERT, UPDATE ON emergency.audit_outbox TO dpdp_app;
