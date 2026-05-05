-- =============================================================================
-- SQLC Queries: Refresh Tokens
-- File: db/queries/refresh_tokens.sql
-- =============================================================================

-- name: CreateRefreshToken :one
-- Stores a new hashed refresh token for a user
INSERT INTO auth.refresh_tokens (
    user_id,
    token_hash,
    expires_at,
    ip_address,
    user_agent
) VALUES (
    $1, $2, $3, $4, $5
)
RETURNING *;


-- name: GetRefreshToken :one
-- Looks up a refresh token by its hash (used during token refresh)
SELECT * FROM auth.refresh_tokens
WHERE token_hash = $1
  AND revoked = FALSE
  AND expires_at > NOW()
LIMIT 1;


-- name: RevokeRefreshToken :exec
-- Revokes a single token (logout from one device)
UPDATE auth.refresh_tokens
SET
    revoked    = TRUE,
    revoked_at = NOW()
WHERE token_hash = $1;


-- name: RevokeAllUserTokens :exec
-- Revokes all active tokens for a user (logout from all devices)
UPDATE auth.refresh_tokens
SET
    revoked    = TRUE,
    revoked_at = NOW()
WHERE user_id = $1 AND revoked = FALSE;


-- name: DeleteExpiredTokens :exec
-- Cleanup job: removes expired tokens to keep the table lean
DELETE FROM auth.refresh_tokens
WHERE expires_at < NOW();
