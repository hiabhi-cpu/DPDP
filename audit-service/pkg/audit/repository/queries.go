package repository

const (
	// Idempotent insert: a replayed delivery (same event_id) is a no-op.
	//
	// patient_key is NULLIF'd because an unresolvable data principal must read as
	// UNKNOWN, not as a patient whose key happens to be "". idx_audit_patient_key
	// is partial on IS NOT NULL, so storing '' would index every unidentifiable
	// event under one empty key — worst for CONSENT_MISSING_ACCESS_ATTEMPT, where
	// no consent row exists to resolve the key from. Those events are findable by
	// details->>'hms_patient_id' instead (see idx_audit_details_hms).
	queryInsertEvent = `
		INSERT INTO audit.audit_log (
			event_id, hospital_id, event_type, actor_id, actor_type, patient_key,
			consent_id, request_id, ip_address, details, created_at
		) VALUES (
			$1, $2, $3, $4, $5, NULLIF($6, ''), $7, $8, $9, $10, NOW()
		)
		ON CONFLICT (event_id) DO NOTHING
		RETURNING id, created_at
	`

	// Find queries will be built dynamically based on filters, but here are the bases
	queryFindEventsBase = `
		SELECT id, hospital_id, event_type, actor_id, actor_type, patient_key,
			   consent_id, request_id, COALESCE(host(ip_address), '') AS ip_address, details, created_at
		FROM audit.audit_log
		WHERE hospital_id = $1
	`
	queryCountEventsBase = `
		SELECT COUNT(*)
		FROM audit.audit_log
		WHERE hospital_id = $1
	`
)
