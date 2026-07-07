package repository

const (
	// Immutable evidence row in the shared consent ledger. patient_key may be NULL
	// (unknown identity); purposes is empty ({}) — an emergency access is deemed
	// consent under §7(b), not a purpose grant. otp_verified=false (no OTP).
	queryInsertAccessVault = `
		INSERT INTO consent.consent_vault (
			id, hospital_id, patient_key, hms_patient_id, type, status, legal_basis,
			purposes, otp_verified, doctor_id, emergency_reason, clinical_note,
			dpo_deadline, artifact_hash
		) VALUES (
			$1, $2, $3, $4, 'EMERGENCY_OVERRIDE', 'PENDING_RETROSPECTIVE', 'DPDP_SECTION_7B',
			'{}'::jsonb, false, $5, $6, $7, $8, $9
		) RETURNING created_at
	`

	// Mutable DPO review-queue row, keyed by the vault row id.
	queryInsertReview = `
		INSERT INTO emergency.reviews (
			access_id, hospital_id, emergency_ref, doctor_id, emergency_reason,
			clinical_note, hms_patient_id, review_status, dpo_deadline
		) VALUES ($1, $2, $3, $4, $5, $6, $7, 'PENDING', $8)
	`

	// Pending queue for a hospital, oldest deadline first. `overdue` is derived.
	queryListPending = `
		SELECT access_id, emergency_ref, doctor_id, emergency_reason, clinical_note,
		       hms_patient_id, review_status, dpo_deadline,
		       (review_status = 'PENDING' AND dpo_deadline < now()) AS overdue,
		       reviewed_by, reviewed_at, created_at
		FROM emergency.reviews
		WHERE hospital_id = $1 AND review_status = 'PENDING'
		ORDER BY dpo_deadline ASC
		LIMIT $2
	`

	// Record a DPO decision. Guarded on review_status='PENDING' so a second review
	// of the same access is a no-op (0 rows affected → 404/conflict in the service).
	queryRecordReview = `
		UPDATE emergency.reviews
		SET review_status = $3, reviewed_by = $4, reviewed_at = now()
		WHERE access_id = $1 AND hospital_id = $2 AND review_status = 'PENDING'
	`

	// ── Audit outbox (transactional) ──────────────────────────────────────────
	queryInsertOutbox = `
		INSERT INTO emergency.audit_outbox (id, payload) VALUES ($1, $2)
	`
	queryFetchUnshippedOutbox = `
		SELECT id, payload FROM emergency.audit_outbox
		WHERE shipped_at IS NULL
		ORDER BY created_at
		LIMIT $1
	`
	queryMarkOutboxShipped = `
		UPDATE emergency.audit_outbox SET shipped_at = now() WHERE id = $1
	`
	queryMarkOutboxFailed = `
		UPDATE emergency.audit_outbox
		SET attempts = attempts + 1, last_error = $2
		WHERE id = $1
	`
)
