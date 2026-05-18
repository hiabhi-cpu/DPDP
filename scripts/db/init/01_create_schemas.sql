-- =============================================================================
-- Init Script: runs once on first postgres container creation
-- Creates separate schemas for each microservice
-- Each service owns its schema — true microservice data isolation
-- =============================================================================

-- Auth service schema
CREATE SCHEMA IF NOT EXISTS auth;

-- Consent vault (core consent engine)
CREATE SCHEMA IF NOT EXISTS consent;

-- Append-only audit log
CREATE SCHEMA IF NOT EXISTS audit;

-- OTP sessions & notifications
CREATE SCHEMA IF NOT EXISTS notification;

-- Future schemas (Phase 2+)
-- CREATE SCHEMA IF NOT EXISTS withdrawal;
-- CREATE SCHEMA IF NOT EXISTS emergency;
-- CREATE SCHEMA IF NOT EXISTS report;
-- CREATE SCHEMA IF NOT EXISTS integration;
