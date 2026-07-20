package repository

const (
	// created_at is supplied by the service (not the DB default) because the
	// artifact hash is computed over it — the stored value and the hashed value
	// must be identical for the hash to be re-verifiable.
	queryInsertConsent = `
		INSERT INTO consent.consent_vault (
			id, hospital_id, patient_key, hms_patient_id, type, status, purposes,
			otp_verified, artifact_hash, idempotency_key, created_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, true, $8, $9, $10
		) RETURNING created_at, version
	`

	// Shared SELECT column list — keep in sync with the Scan order in the repo.
	consentColumns = `id, hospital_id, patient_key, hms_patient_id, type, status,
		purposes, created_at, previous_id, version, artifact_hash`

	// Identity is the PAIR (patient_key, hms_patient_id). patient_key alone is a
	// contact channel — families share one mobile, so scoping by it alone returns
	// a relative's row. See docs/superpowers/specs/2026-07-20-patient-identity-key-design.md
	queryGetLatestConsent = `
		SELECT ` + consentColumns + `
		FROM consent.consent_vault
		WHERE hospital_id = $1 AND patient_key = $2 AND hms_patient_id = $3
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
			otp_verified, previous_id, version, artifact_hash, created_at
		) VALUES (
			$1, $2, $3, $4, 'WITHDRAWAL', $5, $6, true, $7, $8, $9, $10
		) RETURNING created_at
	`

	// Renewal extends an existing chain to (re-)grant purposes. Same shape as
	// withdrawal; only the row type differs.
	queryInsertRenewal = `
		INSERT INTO consent.consent_vault (
			id, hospital_id, patient_key, hms_patient_id, type, status, purposes,
			otp_verified, previous_id, version, artifact_hash, created_at
		) VALUES (
			$1, $2, $3, $4, 'CONSENT_RENEWAL', $5, $6, true, $7, $8, $9, $10
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

	// ── Stats aggregates (read-only) ──────────────────────────────────────────
	// Latest row per patient (excluding emergency rows and unknown identities),
	// counted by aggregate status. RLS scopes all of these to one hospital.
	//
	// DISTINCT ON is the PAIR, not patient_key: a family shares one mobile and
	// therefore one patient_key, so keying on it alone would count a household
	// as a single patient and pick one member's status arbitrarily. Emergency
	// rows are already excluded, which is exactly the set migration 0015
	// guarantees has a non-null hms_patient_id.
	queryStatsStatusCounts = `
		WITH latest AS (
			SELECT DISTINCT ON (patient_key, hms_patient_id) patient_key, status
			FROM consent.consent_vault
			WHERE type <> 'EMERGENCY_OVERRIDE'
			  AND patient_key IS NOT NULL
			ORDER BY patient_key, hms_patient_id, version DESC
		)
		SELECT
			count(*) FILTER (WHERE status = 'ACTIVE')    AS active,
			count(*) FILTER (WHERE status = 'WITHDRAWN') AS withdrawn,
			count(*)                                     AS total
		FROM latest
	`

	// Same pair-scoping as queryStatsStatusCounts — see the note there.
	queryStatsByPurpose = `
		WITH latest AS (
			SELECT DISTINCT ON (patient_key, hms_patient_id) patient_key, purposes
			FROM consent.consent_vault
			WHERE type <> 'EMERGENCY_OVERRIDE'
			  AND patient_key IS NOT NULL
			ORDER BY patient_key, hms_patient_id, version DESC
		)
		SELECT kv.key AS purpose,
			count(*) FILTER (WHERE kv.value = 'ACTIVE')    AS active,
			count(*) FILTER (WHERE kv.value = 'WITHDRAWN') AS withdrawn
		FROM latest, jsonb_each_text(latest.purposes) AS kv
		GROUP BY kv.key
		ORDER BY kv.key
	`

	// make_interval keeps the window parameter a bound integer, never string-built SQL.
	queryStatsActivity = `
		SELECT
			count(*) FILTER (WHERE type = 'CONSENT_GIVEN')   AS captures,
			count(*) FILTER (WHERE type = 'WITHDRAWAL')      AS withdrawals,
			count(*) FILTER (WHERE type = 'CONSENT_RENEWAL') AS renewals
		FROM consent.consent_vault
		WHERE created_at >= now() - make_interval(days => $1)
	`

	queryStatsEmergency = `
		SELECT count(*) AS overrides
		FROM consent.consent_vault
		WHERE type = 'EMERGENCY_OVERRIDE'
	`
)
