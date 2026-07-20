//go:build integration

// This file extends the tenant-isolation suite (see tenant_isolation_test.go
// for the package-level doc comment and the shared harness) with integration
// coverage for repository.GetStats:
//
//   - stats must be hospital-scoped by RLS exactly like every other query —
//     one hospital's activity must never move another hospital's counts.
//   - "latest row wins": consent_vault is an append-only version chain, so the
//     stats aggregates key off the highest `version` per patient_key, not a
//     naive row count.
//
// GetStats (unlike the reads in tenant_isolation_test.go) aggregates ALL rows
// for a hospital_id — it takes no patient-key filter. consent_vault can never
// be cleaned between tests (no DELETE grant for dpdp_app, and even the owner
// is blocked by the append-only trigger — see tenant_isolation_test.go), so
// counting against the shared, ever-growing hospitalA/hospitalB fixtures would
// make exact-count assertions flaky and order-dependent. Instead, each test
// below creates its OWN brand-new hospital (mirroring ensureHospitalB's
// INSERT), giving a guaranteed-empty vault to assert exact counts against.
package isolation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"testing"

	"github.com/google/uuid"
	"github.com/hiabhi-cpu/consent-service/pkg/consent/repository"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// newStatsPool opens a pgxpool.Pool against the dpdp_app runtime role.
// GetStats manages its own RLS-scoped transaction internally (it calls
// setHospitalContext itself), so — unlike the raw *pgx.Conn used elsewhere in
// this package — callers only need a pool to hand to repository.New.
func newStatsPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	_, appURL := connections(t)
	pool, err := pgxpool.New(context.Background(), appURL)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// createStatsHospital inserts a fresh, uniquely-identified hospital via the
// admin connection — same columns as ensureHospitalB's INSERT — so this
// test's GetStats counts start from a guaranteed-empty consent_vault.
func createStatsHospital(t *testing.T, admin *pgx.Conn) string {
	t.Helper()
	id := uuid.New().String()
	slug := "stats-test-" + id
	apiKeyHash := hex.EncodeToString(mustRand(t, 32)) // unique per hospital (api_key_hash is UNIQUE)
	_, err := admin.Exec(context.Background(), `
		INSERT INTO auth.hospitals (id, name, slug, address, city, api_key_hash, hms_type, plan_tier, dpo_name, dpo_email, active)
		VALUES ($1, 'Stats Test Hospital', $2, 'S Lane', 'Pune', $3,
		        'generic', 'starter', 'DPO S', 'dpo-s@test.local', true)`,
		id, slug, apiKeyHash)
	if err != nil {
		t.Fatalf("create stats test hospital: %v", err)
	}
	return id
}

// insertConsentRow seeds one consent_vault row directly via the admin
// connection — bypasses RLS, so hospital_id is set explicitly and honestly,
// exactly like seedConsentRow. version defaults to 1.
func insertConsentRow(t *testing.T, admin *pgx.Conn, hospitalID, patientKey, evType, status, purposesJSON string) {
	t.Helper()
	insertConsentRowV(t, admin, hospitalID, patientKey, evType, status, purposesJSON, 1)
}

// insertConsentRowV is insertConsentRow with an explicit version, letting a
// test write two rows for the same patient_key to exercise "latest row wins".
func insertConsentRowV(t *testing.T, admin *pgx.Conn, hospitalID, patientKey, evType, status, purposesJSON string, version int) {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	_, err := admin.Exec(context.Background(), `
		INSERT INTO consent.consent_vault
			(id, hospital_id, patient_key, hms_patient_id, type, status, purposes, otp_verified, artifact_hash, version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, true, $8, $9)`,
		uuid.New(), hospitalID, patientKey,
		// Consent rows must name a patient (migration 0015). Derived from
		// patient_key so every row in one patient's version chain carries the
		// SAME id, as real data does — "latest row wins" depends on those rows
		// being one patient.
		hmsIDFor(patientKey),
		evType, status, []byte(purposesJSON),
		hex.EncodeToString(b), version,
	)
	if err != nil {
		t.Fatalf("insert consent row (%s/%s v%d): %v", evType, status, version, err)
	}
}

// insertConsentRowFor is insertConsentRow with an explicit hms_patient_id, for
// tests that need two DIFFERENT PEOPLE under one patient_key — a family sharing
// a mobile. The derived id used elsewhere cannot express that case, because it
// is a pure function of patient_key.
func insertConsentRowFor(t *testing.T, admin *pgx.Conn, hospitalID, patientKey, hmsPatientID, evType, status, purposesJSON string) {
	t.Helper()
	_, err := admin.Exec(context.Background(), `
		INSERT INTO consent.consent_vault
			(id, hospital_id, patient_key, hms_patient_id, type, status, purposes, otp_verified, artifact_hash, version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, true, $8, 1)`,
		uuid.New(), hospitalID, patientKey, hmsPatientID,
		evType, status, []byte(purposesJSON),
		hex.EncodeToString(mustRand(t, 32)),
	)
	if err != nil {
		t.Fatalf("insert consent row for %s (%s/%s): %v", hmsPatientID, evType, status, err)
	}
}

// TestGetStatsIsolatesByHospital verifies stats never leak across hospitals:
// rows for hospital B must not affect hospital A's counts.
func TestGetStatsIsolatesByHospital(t *testing.T) {
	ctx := context.Background()
	adminURL, _ := connections(t)
	admin := connect(t, adminURL)
	pool := newStatsPool(t)

	hospA := createStatsHospital(t, admin)
	hospB := createStatsHospital(t, admin)

	// Two active CONSENT_GIVEN patients for hospital A, one for hospital B.
	insertConsentRow(t, admin, hospA, randomPatientKey(t), "CONSENT_GIVEN", "ACTIVE", `{"treatment":"ACTIVE"}`)
	insertConsentRow(t, admin, hospA, randomPatientKey(t), "CONSENT_GIVEN", "ACTIVE", `{"treatment":"ACTIVE"}`)
	insertConsentRow(t, admin, hospB, randomPatientKey(t), "CONSENT_GIVEN", "ACTIVE", `{"treatment":"ACTIVE"}`)

	repo := repository.New(pool)

	statsA, err := repo.GetStats(ctx, hospA, 30)
	if err != nil {
		t.Fatalf("GetStats(A): %v", err)
	}
	if statsA.Consents.Active != 2 || statsA.Consents.TotalPatients != 2 {
		t.Fatalf("hospital A active=%d total=%d, want 2/2 (leak?)",
			statsA.Consents.Active, statsA.Consents.TotalPatients)
	}

	statsB, err := repo.GetStats(ctx, hospB, 30)
	if err != nil {
		t.Fatalf("GetStats(B): %v", err)
	}
	if statsB.Consents.Active != 1 || statsB.Consents.TotalPatients != 1 {
		t.Fatalf("hospital B active=%d total=%d, want 1/1", statsB.Consents.Active, statsB.Consents.TotalPatients)
	}
}

// TestGetStatsLatestRowWins verifies a withdrawal supersedes the earlier grant
// for the same patient (counted once, as withdrawn) — GetStats keys off the
// highest version per patient_key, not a raw row count.
func TestGetStatsLatestRowWins(t *testing.T) {
	ctx := context.Background()
	adminURL, _ := connections(t)
	admin := connect(t, adminURL)
	pool := newStatsPool(t)

	hosp := createStatsHospital(t, admin)
	key := randomPatientKey(t)

	insertConsentRow(t, admin, hosp, key, "CONSENT_GIVEN", "ACTIVE", `{"treatment":"ACTIVE"}`)
	insertConsentRowV(t, admin, hosp, key, "WITHDRAWAL", "WITHDRAWN", `{"treatment":"WITHDRAWN"}`, 2)

	stats, err := repository.New(pool).GetStats(ctx, hosp, 30)
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if stats.Consents.Active != 0 || stats.Consents.Withdrawn != 1 || stats.Consents.TotalPatients != 1 {
		t.Fatalf("active=%d withdrawn=%d total=%d, want 0/1/1",
			stats.Consents.Active, stats.Consents.Withdrawn, stats.Consents.TotalPatients)
	}
}

// A family shares one mobile and therefore one patient_key. Stats must count
// them as the number of PEOPLE, not the number of phone numbers: keying the
// per-patient rollup on patient_key alone collapses a household into one
// counted patient and picks one member's status arbitrarily.
//
// This is the reporting-path echo of the bug this branch fixes elsewhere, and
// no per-task review owned it — stats belongs to none of capture/check/
// withdraw/grant.
func TestGetStatsCountsFamilyMembersSeparately(t *testing.T) {
	ctx := context.Background()
	adminURL, _ := connections(t)
	admin := connect(t, adminURL)
	pool := newStatsPool(t)

	hosp := createStatsHospital(t, admin)

	// ONE household key, two people: the mother active, the son withdrawn.
	householdKey := randomPatientKey(t)
	insertConsentRowFor(t, admin, hosp, householdKey, "PA-mother", "CONSENT_GIVEN", "ACTIVE", `{"treatment":"ACTIVE"}`)
	insertConsentRowFor(t, admin, hosp, householdKey, "PA-son", "WITHDRAWAL", "WITHDRAWN", `{"treatment":"WITHDRAWN"}`)

	stats, err := repository.New(pool).GetStats(ctx, hosp, 30)
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}

	if stats.Consents.TotalPatients != 2 {
		t.Fatalf("total_patients = %d, want 2 — a shared mobile is one patient_key but two data principals",
			stats.Consents.TotalPatients)
	}
	if stats.Consents.Active != 1 || stats.Consents.Withdrawn != 1 {
		t.Fatalf("active=%d withdrawn=%d, want 1/1 — one member's status is standing in for the household's",
			stats.Consents.Active, stats.Consents.Withdrawn)
	}
}
