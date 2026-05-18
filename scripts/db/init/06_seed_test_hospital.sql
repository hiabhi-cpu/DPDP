-- =============================================================================
-- 06_seed_test_hospital.sql
-- Seeds ONE test hospital for local development.
-- API key (raw): TEST-HOSPITAL-API-KEY-LOCAL-DEV-001
-- bcrypt hash below was generated with cost=12 — matches shared/crypto.HashAPIKey()
--
-- To generate a fresh hash:
--   cd services/auth-service && go run ./cmd/hashkey/main.go TEST-HOSPITAL-API-KEY-LOCAL-DEV-001
--
-- IMPORTANT: This file runs ONCE on first docker volume creation.
--            If you change this file, run: make db-reset && make db-up
-- =============================================================================

INSERT INTO auth.hospitals (
  id,
  name,
  slug,
  address,
  city,
  api_key_hash,
  webhook_url,
  hms_type,
  plan_tier,
  dpo_name,
  dpo_email,
  active
) VALUES (
  'a1b2c3d4-e5f6-7890-abcd-ef1234567890',           -- fixed UUID for local dev predictability
  'Test Hospital Mumbai',
  'test-hospital',
  '123 Test Lane, Bandra West',
  'Mumbai',
  -- bcrypt of: TEST-HOSPITAL-API-KEY-LOCAL-DEV-001
  -- Regenerate with: make hashkey KEY=TEST-HOSPITAL-API-KEY-LOCAL-DEV-001
  '$2a$12$4H/AUj05P6mFCUTZSH/byuHi6q2Wo6jgKhhP12z.UNb25vzVyh1u6',
  'http://localhost:9099/webhook',                    -- mock HMS webhook receiver
  'generic',
  'starter',
  'Test DPO',
  'dpo@testhospital.local',
  true
) ON CONFLICT (slug) DO NOTHING;

-- ── Local hospital-specific key (used by mock secrets provider) ──────────────
-- In production: stored in AWS Secrets Manager at
--   /consentmanager/hospitals/a1b2c3d4-e5f6-7890-abcd-ef1234567890/key
-- For local dev: stored in secrets/local_hospital_keys.json (gitignored)
-- Value: TEST-HOSPITAL-SECRET-KEY-LOCAL-DEV-XYZ
-- SYSTEM_SALT (local): LOCAL-SYSTEM-SALT-FOR-DEV-ONLY-NEVER-USE-IN-PROD

COMMENT ON TABLE auth.hospitals IS
  'Seeded with test-hospital for local dev. Remove this seed before any pilot deployment.';
