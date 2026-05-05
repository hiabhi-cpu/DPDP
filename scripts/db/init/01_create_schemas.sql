-- =============================================================================
-- Init Script: runs once on first postgres container creation
-- Creates separate schemas for each microservice
-- Each service owns its schema — true microservice data isolation
-- =============================================================================

-- Auth service schema
CREATE SCHEMA IF NOT EXISTS auth;

-- Future service schemas (uncomment as you build each service)
-- CREATE SCHEMA IF NOT EXISTS consent;
-- CREATE SCHEMA IF NOT EXISTS audit;
-- CREATE SCHEMA IF NOT EXISTS withdrawal;
-- CREATE SCHEMA IF NOT EXISTS notification;
-- CREATE SCHEMA IF NOT EXISTS emergency;
-- CREATE SCHEMA IF NOT EXISTS report;
-- CREATE SCHEMA IF NOT EXISTS integration;
