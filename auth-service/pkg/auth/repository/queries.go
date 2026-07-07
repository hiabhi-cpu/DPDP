package repository

// SQL queries for the auth schema.
// All queries are scoped to auth.hospitals — no cross-schema access.
const (
	// queryListActive fetches all active hospitals so the service layer
	// can do bcrypt comparison in-process (not via SQL WHERE clause),
	// preventing timing-based enumeration of valid API keys.
	queryListActive = `
		SELECT id, name, slug, api_key_hash, active, created_at
		FROM auth.hospitals
		WHERE active = true
	`

	// queryGetByAPIKeyHash looks up a hospital in O(1) time by the SHA-256 hash
	// of its API key.
	queryGetByAPIKeyHash = `
		SELECT id, name, slug, api_key_hash, active, created_at
		FROM auth.hospitals
		WHERE api_key_hash = $1 AND active = true
	`
)
