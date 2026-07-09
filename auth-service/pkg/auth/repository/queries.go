package repository

// SQL queries for the auth schema.
// All queries are scoped to auth.hospitals — no cross-schema access.
const (
	// queryGetByAPIKeyHash looks up a hospital in O(1) time by the SHA-256 hash
	// of its API key.
	queryGetByAPIKeyHash = `
		SELECT id, name, slug, api_key_hash, active, created_at
		FROM auth.hospitals
		WHERE api_key_hash = $1 AND active = true
	`
)
