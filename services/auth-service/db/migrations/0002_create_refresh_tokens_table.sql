-- =============================================================================
-- Migration: 0002_create_refresh_tokens_table.sql
-- Tool: Goose
--
-- Schema: auth
-- Table: auth.refresh_tokens — tracks issued JWT refresh tokens
-- Allows token revocation (logout, suspicious activity)
-- =============================================================================

-- +goose Up
-- +goose StatementBegin

CREATE TABLE IF NOT EXISTS auth.refresh_tokens (
    id          UUID            PRIMARY KEY DEFAULT gen_random_uuid(),

    -- The user this token belongs to
    user_id     UUID            NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE,

    -- The raw refresh token string (hashed before storing)
    token_hash  VARCHAR(255)    NOT NULL UNIQUE,

    -- Token lifecycle
    expires_at  TIMESTAMPTZ     NOT NULL,
    revoked     BOOLEAN         NOT NULL DEFAULT FALSE,
    revoked_at  TIMESTAMPTZ,

    -- Audit info — where the token was issued from
    ip_address  VARCHAR(45),      -- supports IPv6
    user_agent  TEXT,

    created_at  TIMESTAMPTZ     NOT NULL DEFAULT NOW()
);

-- Index to quickly look up tokens by user (e.g., revoke all sessions for a user)
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_user_id ON auth.refresh_tokens (user_id);

-- Index to clean up expired tokens efficiently
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_expires_at ON auth.refresh_tokens (expires_at);

-- +goose StatementEnd


-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS auth.refresh_tokens;

-- +goose StatementEnd
