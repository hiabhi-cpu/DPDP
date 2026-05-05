-- =============================================================================
-- SQLC Queries: Users
-- File: db/queries/users.sql
-- These SQL queries are read by `sqlc generate` to produce type-safe Go code.
-- Each query needs a name and a command annotation.
-- =============================================================================

-- name: CreateUser :one
-- Inserts a new user and returns the created row
INSERT INTO auth.users (
    role,
    email,
    phone,
    password_hash,
    full_name,
    org_id
) VALUES (
    $1, $2, $3, $4, $5, $6
)
RETURNING *;


-- name: GetUserByID :one
-- Fetches a single user by their UUID primary key
SELECT * FROM auth.users
WHERE id = $1 AND is_active = TRUE
LIMIT 1;


-- name: GetUserByEmail :one
-- Used during email+password login
SELECT * FROM auth.users
WHERE email = $1 AND is_active = TRUE
LIMIT 1;


-- name: GetUserByPhone :one
-- Used during OTP-based login
SELECT * FROM auth.users
WHERE phone = $1 AND is_active = TRUE
LIMIT 1;


-- name: UpdateUserVerified :one
-- Marks a user as verified (after OTP or email confirmation)
UPDATE auth.users
SET
    is_verified = TRUE,
    updated_at  = NOW()
WHERE id = $1
RETURNING *;


-- name: UpdateUserPassword :one
-- Updates a user's hashed password (password reset flow)
UPDATE auth.users
SET
    password_hash = $2,
    updated_at    = NOW()
WHERE id = $1
RETURNING *;


-- name: DeactivateUser :exec
-- Soft-delete: marks the user as inactive instead of deleting
UPDATE auth.users
SET
    is_active  = FALSE,
    updated_at = NOW()
WHERE id = $1;
