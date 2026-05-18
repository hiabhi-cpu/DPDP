-- =============================================================================
-- 05_otp_sessions.sql
-- Short-lived OTP session store (Postgres side — Redis is the primary TTL store).
-- This table provides: durable record of OTP attempts, audit trail for OTP_FAILED
-- events, and a recovery path if Redis restarts during an active OTP session.
--
-- Raw mobile number is NEVER stored — only mobile_hash.
-- Raw OTP is NEVER stored — only bcrypt(otp).
-- =============================================================================

CREATE TABLE IF NOT EXISTS notification.otp_sessions (
  id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  hospital_id     UUID        NOT NULL REFERENCES auth.hospitals(id),

  -- mobile_hash = HMAC_SHA256(mobile + SYSTEM_SALT + hospital_key)
  -- Same algorithm as patient_key — consistent hashing across the system
  mobile_hash     VARCHAR(72) NOT NULL,

  -- bcrypt hash of the 6-digit OTP (cost=10 — lower than API keys, TTL is short)
  otp_hash        VARCHAR     NOT NULL,

  purpose         VARCHAR     NOT NULL DEFAULT 'CONSENT'
                  CHECK (purpose IN ('CONSENT','WITHDRAWAL','PATIENT_PORTAL_LOGIN')),

  attempts        INTEGER     NOT NULL DEFAULT 0,
  max_attempts    INTEGER     NOT NULL DEFAULT 3,
  expires_at      TIMESTAMPTZ NOT NULL,
  used            BOOLEAN     NOT NULL DEFAULT false,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Lookup: is there a valid unused session for this mobile at this hospital?
CREATE INDEX IF NOT EXISTS idx_otp_mobile_hospital_active
  ON notification.otp_sessions (hospital_id, mobile_hash)
  WHERE used = false;

-- Cleanup index: find expired sessions for background purge job
CREATE INDEX IF NOT EXISTS idx_otp_expires_at
  ON notification.otp_sessions (expires_at)
  WHERE used = false;

COMMENT ON TABLE notification.otp_sessions IS
  'OTP session records. Redis is primary TTL store; this table is the durable backup. '
  'Raw mobile and raw OTP are never stored — only hashes.';
