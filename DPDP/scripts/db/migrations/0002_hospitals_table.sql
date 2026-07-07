-- =============================================================================
-- 02_hospitals_table.sql
-- Core tenant registry.
-- NOTE: hospital_specific_key is NOT stored here — it lives in AWS Secrets
--       Manager at path: /consentmanager/hospitals/{hospital_id}/key
-- =============================================================================

CREATE TABLE IF NOT EXISTS auth.hospitals (
  id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  name            VARCHAR     NOT NULL,
  slug            VARCHAR     UNIQUE NOT NULL,         -- e.g. "apollo-bandra"
  address         TEXT,
  city            VARCHAR,
  api_key_hash    VARCHAR     UNIQUE NOT NULL,         -- bcrypt(raw_api_key, cost=12)
  webhook_url     VARCHAR,                             -- HMS callback URL
  hms_type        VARCHAR     DEFAULT 'generic'
                  CHECK (hms_type IN ('bahmni','ehospital','practo','mocdoc','generic')),
  plan_tier       VARCHAR     DEFAULT 'starter'
                  CHECK (plan_tier IN ('starter','growth','enterprise')),
  dpo_name        VARCHAR,
  dpo_email       VARCHAR,
  active          BOOLEAN     NOT NULL DEFAULT true,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Fast lookup by slug (patient portal routing)
CREATE UNIQUE INDEX IF NOT EXISTS idx_hospitals_slug   ON auth.hospitals (slug);
-- Fast lookup by api_key_hash (every API request authenticates this way)
CREATE UNIQUE INDEX IF NOT EXISTS idx_hospitals_api_key ON auth.hospitals (api_key_hash);

COMMENT ON TABLE auth.hospitals IS
  'One row per hospital tenant. hospital_specific_key stored in AWS Secrets Manager only.';
COMMENT ON COLUMN auth.hospitals.api_key_hash IS
  'bcrypt hash (cost=12) of the raw API key issued at hospital onboarding. Raw key never stored.';
