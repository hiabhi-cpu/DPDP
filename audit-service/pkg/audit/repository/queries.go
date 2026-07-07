package repository

const (
	// Idempotent insert: a replayed delivery (same event_id) is a no-op.
	queryInsertEvent = `
		INSERT INTO audit.audit_log (
			event_id, hospital_id, event_type, actor_id, actor_type, patient_key,
			consent_id, request_id, ip_address, details, created_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NOW()
		)
		ON CONFLICT (event_id) DO NOTHING
		RETURNING id, created_at
	`

	// Find queries will be built dynamically based on filters, but here are the bases
	queryFindEventsBase = `
		SELECT id, hospital_id, event_type, actor_id, actor_type, patient_key,
			   consent_id, request_id, ip_address, details, created_at
		FROM audit.audit_log
		WHERE hospital_id = $1
	`
	queryCountEventsBase = `
		SELECT COUNT(*)
		FROM audit.audit_log
		WHERE hospital_id = $1
	`
)
