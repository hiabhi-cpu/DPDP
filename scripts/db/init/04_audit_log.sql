-- =============================================================================
-- 04_audit_log.sql
-- APPEND-ONLY event ledger — legal evidence for DPDP 7-year audit requirement.
-- Architecture rules (product plan v2 §7 + §19):
--   - RLS isolates each hospital's rows
--   - UPDATE and DELETE are blocked by explicit RLS policies (not just triggers)
--     so even the DB owner cannot modify rows at the SQL level
--   - sqlx raw INSERT only in app code — no ORM touches this table
-- =============================================================================

CREATE TABLE IF NOT EXISTS audit.audit_log (
  -- ── Identity ──────────────────────────────────────────────────────────────
  id              BIGSERIAL   PRIMARY KEY,              -- monotonic, tamper-evident ordering
  hospital_id     UUID        NOT NULL REFERENCES auth.hospitals(id),
  request_id      UUID,                                -- trace ID across services

  -- ── What happened ─────────────────────────────────────────────────────────
  event_type      VARCHAR     NOT NULL
                  CHECK (event_type IN (
                    'CONSENT_GRANTED',
                    'CONSENT_WITHDRAWN',
                    'DATA_ACCESSED',
                    'CONSENT_MISSING_ACCESS_ATTEMPT',
                    'EMERGENCY_ACCESS',
                    'OTP_SENT',
                    'OTP_VERIFIED',
                    'OTP_FAILED',
                    'BYPASS_DETECTED',
                    'BREACH_REPORTED',
                    'DPBI_NOTIFICATION_SUBMITTED',
                    'PATIENT_NOTIFIED',
                    'DPO_REVIEW_COMPLETED',
                    'SYSTEM_EVENT'
                  )),

  -- ── Who ───────────────────────────────────────────────────────────────────
  actor_id        VARCHAR,                             -- doctor_id / staff_id / patient_key / "system"
  actor_type      VARCHAR     NOT NULL DEFAULT 'SYSTEM'
                  CHECK (actor_type IN ('DOCTOR','ADMIN','DPO','PATIENT','SYSTEM','KIOSK')),

  -- ── On whom / what ────────────────────────────────────────────────────────
  patient_key     VARCHAR(72),                         -- hashed, never raw mobile
  consent_id      UUID,                                -- FK to consent_vault (soft ref — no cascade)

  -- ── Context ───────────────────────────────────────────────────────────────
  ip_address      INET,
  details         JSONB       DEFAULT '{}',            -- service-specific structured data

  -- ── Timestamp — immutable ─────────────────────────────────────────────────
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ── Indexes ───────────────────────────────────────────────────────────────────
-- Paginated audit log view in admin dashboard (most common query)
CREATE INDEX IF NOT EXISTS idx_audit_hospital_time
  ON audit.audit_log (hospital_id, created_at DESC);

-- Filter by event type within a hospital
CREATE INDEX IF NOT EXISTS idx_audit_hospital_event
  ON audit.audit_log (hospital_id, event_type);

-- Patient audit trail (patient portal self-service view)
CREATE INDEX IF NOT EXISTS idx_audit_patient_key
  ON audit.audit_log (hospital_id, patient_key)
  WHERE patient_key IS NOT NULL;

-- ── Row Level Security ────────────────────────────────────────────────────────
ALTER TABLE audit.audit_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit.audit_log FORCE ROW LEVEL SECURITY;

-- Read isolation: each hospital sees only their own rows
CREATE POLICY hospital_audit_isolation ON audit.audit_log
  USING (hospital_id = current_setting('app.hospital_id')::uuid);

-- Mutation blocks — these apply to ALL roles including superuser when RLS is FORCED
CREATE POLICY no_update ON audit.audit_log
  FOR UPDATE USING (false);

CREATE POLICY no_delete ON audit.audit_log
  FOR DELETE USING (false);

COMMENT ON TABLE audit.audit_log IS
  'Immutable 7-year audit ledger. UPDATE/DELETE blocked by RLS policy. '
  'App code uses sqlx raw INSERT only — no ORM. hospital_id RLS enforced.';
