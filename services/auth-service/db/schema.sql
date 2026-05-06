-- =============================================================================
-- SQLC Schema File — auth-service
-- This file is READ BY SQLC ONLY to understand table structure.
-- It is NOT a migration file (Goose manages actual migrations).
-- Keep in sync with db/migrations/*.sql
-- =============================================================================

CREATE SCHEMA IF NOT EXISTS auth;

CREATE TABLE auth.users (
    id            UUID            PRIMARY KEY DEFAULT gen_random_uuid(),
    role          VARCHAR(20)     NOT NULL CHECK (role IN ('data_principal', 'data_fiduciary', 'dpo')),
    email         VARCHAR(255)    UNIQUE,
    phone         VARCHAR(20)     UNIQUE,
    password_hash VARCHAR(255),
    full_name     VARCHAR(255)    NOT NULL,
    org_id        VARCHAR(100),
    is_active     BOOLEAN         NOT NULL DEFAULT TRUE,
    is_verified   BOOLEAN         NOT NULL DEFAULT FALSE,
    created_at    TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ     NOT NULL DEFAULT NOW()
);

CREATE TABLE auth.refresh_tokens (
    id          UUID            PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID            NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE,
    token_hash  VARCHAR(255)    NOT NULL UNIQUE,
    expires_at  TIMESTAMPTZ     NOT NULL,
    revoked     BOOLEAN         NOT NULL DEFAULT FALSE,
    revoked_at  TIMESTAMPTZ,
    ip_address  VARCHAR(45),
    user_agent  TEXT,
    created_at  TIMESTAMPTZ     NOT NULL DEFAULT NOW()
);
