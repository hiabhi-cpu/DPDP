package repository

const (
	queryInsertConsent = `
		INSERT INTO consent.consent_vault (
			id, hospital_id, patient_key, hms_patient_id, type, status, purposes,
			otp_verified, artifact_hash, idempotency_key
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, true, $8, $9
		) RETURNING created_at, version
	`

	// Shared SELECT column list — keep in sync with the Scan order in the repo.
	consentColumns = `id, hospital_id, patient_key, hms_patient_id, type, status,
		purposes, created_at, previous_id, version, artifact_hash`

	queryGetLatestConsent = `
		SELECT ` + consentColumns + `
		FROM consent.consent_vault
		WHERE hospital_id = $1 AND patient_key = $2
		ORDER BY version DESC LIMIT 1
	`

	// Doctor/HMS access path: identify the patient by the hospital's opaque HMS ID.
	queryGetLatestByHMSPatientID = `
		SELECT ` + consentColumns + `
		FROM consent.consent_vault
		WHERE hospital_id = $1 AND hms_patient_id = $2
		ORDER BY version DESC LIMIT 1
	`

	queryGetByIdempotencyKey = `
		SELECT ` + consentColumns + `
		FROM consent.consent_vault
		WHERE hospital_id = $1 AND idempotency_key = $2 AND type = 'CONSENT_GIVEN'
		ORDER BY version DESC LIMIT 1
	`

	// status is parameterized: a partial withdrawal that leaves other purposes
	// active yields an ACTIVE aggregate row; a full withdrawal yields WITHDRAWN.
	// hms_patient_id is carried forward so the latest row is always resolvable by it.
	queryInsertWithdrawn = `
		INSERT INTO consent.consent_vault (
			id, hospital_id, patient_key, hms_patient_id, type, status, purposes,
			otp_verified, previous_id, version, artifact_hash
		) VALUES (
			$1, $2, $3, $4, 'WITHDRAWAL', $5, $6, true, $7, $8, $9
		) RETURNING created_at
	`

	// Renewal extends an existing chain to (re-)grant purposes. Same shape as
	// withdrawal; only the row type differs.
	queryInsertRenewal = `
		INSERT INTO consent.consent_vault (
			id, hospital_id, patient_key, hms_patient_id, type, status, purposes,
			otp_verified, previous_id, version, artifact_hash
		) VALUES (
			$1, $2, $3, $4, 'CONSENT_RENEWAL', $5, $6, true, $7, $8, $9
		) RETURNING created_at
	`

	// ── Audit outbox (transactional) ──────────────────────────────────────────
	queryInsertOutbox = `
		INSERT INTO consent.audit_outbox (id, payload) VALUES ($1, $2)
	`
	queryFetchUnshippedOutbox = `
		SELECT id, payload FROM consent.audit_outbox
		WHERE shipped_at IS NULL
		ORDER BY created_at
		LIMIT $1
	`
	queryMarkOutboxShipped = `
		UPDATE consent.audit_outbox SET shipped_at = now() WHERE id = $1
	`
	queryMarkOutboxFailed = `
		UPDATE consent.audit_outbox
		SET attempts = attempts + 1, last_error = $2
		WHERE id = $1
	`
)
