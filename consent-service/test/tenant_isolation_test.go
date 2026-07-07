//go:build integration

// Package isolation holds the tenant-isolation test suite — the load-bearing
// proof that one hospital can never read, mutate, or delete another hospital's
// consent rows (product plan Red Line 3, the hospital data silo).
//
// A broken RLS boundary produces NO error and NO crash — the app works
// perfectly and simply returns the wrong hospital's data. That silent-failure
// mode is exactly why this is codified: it ran inert once already (services
// connected as a BYPASSRLS superuser) and nothing looked wrong. This suite must
// stay green on every change that touches the DB layer, a role grant, or an RLS
// policy.
//
// It requires a REAL Postgres (RLS is a database feature — a mock can't prove
// it) with the init scripts applied and the least-privilege dpdp_app role
// created. It is build-tagged `integration` so it never runs in plain
// `go test ./...`; run it with:
//
//	go test -tags=integration ./test/... -v
//
// Two connections are used, mirroring production exactly:
//   - TEST_ADMIN_DATABASE_URL — the superuser/owner, for SEEDING only
//     (dpdp_app deliberately cannot INSERT hospitals).
//   - TEST_DATABASE_URL       — the dpdp_app runtime role, under which every
//     ISOLATION ASSERTION runs. This is the role the services actually use.
package isolation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	// hospitalA is the fixed local-dev seed hospital (06_seed_test_hospital.sql).
	hospitalA = "a1b2c3d4-e5f6-7890-abcd-ef1234567890"
	// hospitalB is a second hospital the suite seeds via the admin connection so
	// there is a genuine neighbour to be isolated from.
	hospitalB     = "b0b0b0b0-0000-4000-8000-00000000b0b0"
	hospitalBSlug = "tenant-isolation-test-hospital-b"

	// SQLStateInsufficientPrivilege / SQLStateUndefinedObject are the Postgres
	// error codes we assert on so the tests key off codes, not message strings.
	sqlInsufficientPrivilege = "42501"
	sqlUndefinedObject       = "42704" // unrecognized run-time config parameter
	sqlRestrictViolation     = "23001" // raised by the append-only trigger (restrict_violation)
)

// ─── harness ────────────────────────────────────────────────────────────────

// connections returns (adminURL, appURL), skipping the whole suite if either is
// unset. Both must point at the same `dpdp` database.
func connections(t *testing.T) (adminURL, appURL string) {
	t.Helper()
	adminURL = os.Getenv("TEST_ADMIN_DATABASE_URL")
	appURL = os.Getenv("TEST_DATABASE_URL")
	if adminURL == "" || appURL == "" {
		t.Skip("set TEST_ADMIN_DATABASE_URL (superuser) and TEST_DATABASE_URL (dpdp_app) to run the tenant-isolation suite")
	}
	return adminURL, appURL
}

func connect(t *testing.T, url string) *pgx.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// randomPatientKey returns a fresh versioned patient key. Using unique keys per
// run keeps the suite correct against a dirty, append-only DB (consent_vault
// rows can never be deleted — even by the superuser, the trigger blocks it), so
// assertions filter on this run's keys rather than counting all hospital rows.
func randomPatientKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return "v1|" + hex.EncodeToString(b)
}

// seedConsentRow inserts one CONSENT_GIVEN row for hospitalID via the admin
// connection (superuser bypasses RLS, so seeding is context-free and honest).
func seedConsentRow(t *testing.T, admin *pgx.Conn, hospitalID, patientKey string) {
	t.Helper()
	ctx := context.Background()
	_, err := admin.Exec(ctx, `
		INSERT INTO consent.consent_vault
			(id, hospital_id, patient_key, type, status, purposes, otp_verified, artifact_hash)
		VALUES ($1, $2, $3, 'CONSENT_GIVEN', 'ACTIVE', $4, true, $5)`,
		uuid.New(), hospitalID, patientKey,
		[]byte(`{"treatment":"ACTIVE"}`),
		hex.EncodeToString(mustRand(t, 32)),
	)
	if err != nil {
		t.Fatalf("seed consent row for %s: %v", hospitalID, err)
	}
}

func mustRand(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return b
}

// ensureHospitalB creates the neighbour hospital once (idempotent).
func ensureHospitalB(t *testing.T, admin *pgx.Conn) {
	t.Helper()
	_, err := admin.Exec(context.Background(), `
		INSERT INTO auth.hospitals (id, name, slug, address, city, api_key_hash, hms_type, plan_tier, dpo_name, dpo_email, active)
		VALUES ($1, 'Tenant Isolation Test Hospital B', $2, 'B Lane', 'Pune',
		        'deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef',
		        'generic', 'starter', 'DPO B', 'dpo-b@test.local', true)
		ON CONFLICT (slug) DO NOTHING`,
		hospitalB, hospitalBSlug)
	if err != nil {
		t.Fatalf("ensure hospital B: %v", err)
	}
}

// countUnderContext runs, as dpdp_app, a count over the given patient keys with
// the RLS context set to hospitalID — exactly how the services query. The
// set_config is transaction-local (true), matching setHospitalContext.
func countUnderContext(t *testing.T, app *pgx.Conn, hospitalID string, keys []string) int {
	t.Helper()
	ctx := context.Background()
	tx, err := app.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('app.hospital_id', $1, true)", hospitalID); err != nil {
		t.Fatalf("set context: %v", err)
	}
	var n int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM consent.consent_vault WHERE patient_key = ANY($1)`, keys,
	).Scan(&n); err != nil {
		t.Fatalf("count under context %s: %v", hospitalID, err)
	}
	return n
}

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// ─── tests ──────────────────────────────────────────────────────────────────

// TestReadIsolation is the core guarantee: under hospital A's context the app
// sees A's rows and ZERO of B's — even when the query explicitly names B's keys.
func TestReadIsolation(t *testing.T) {
	adminURL, appURL := connections(t)
	admin := connect(t, adminURL)
	app := connect(t, appURL)
	ensureHospitalB(t, admin)

	// Seed a disjoint set of rows for each hospital, tagged with this run's keys.
	keysA := []string{randomPatientKey(t), randomPatientKey(t), randomPatientKey(t)}
	keysB := []string{randomPatientKey(t), randomPatientKey(t)}
	for _, k := range keysA {
		seedConsentRow(t, admin, hospitalA, k)
	}
	for _, k := range keysB {
		seedConsentRow(t, admin, hospitalB, k)
	}
	allKeys := append(append([]string{}, keysA...), keysB...)

	// Under A's context: see exactly A's rows, none of B's.
	if got := countUnderContext(t, app, hospitalA, allKeys); got != len(keysA) {
		t.Fatalf("context A: got %d rows, want %d (A's rows only)", got, len(keysA))
	}
	// The crisp proof: query B's own keys under A's context → RLS hides them.
	if got := countUnderContext(t, app, hospitalA, keysB); got != 0 {
		t.Fatalf("context A querying B's keys: got %d rows, want 0 — CROSS-HOSPITAL LEAK", got)
	}
	// Symmetric: under B's context, see exactly B's rows.
	if got := countUnderContext(t, app, hospitalB, allKeys); got != len(keysB) {
		t.Fatalf("context B: got %d rows, want %d (B's rows only)", got, len(keysB))
	}
}

// TestFailClosedNoContext proves the policy fails CLOSED: a query that forgets
// to set app.hospital_id errors instead of silently returning everything. The
// RLS policy uses current_setting('app.hospital_id')::uuid without missing_ok.
func TestFailClosedNoContext(t *testing.T) {
	_, appURL := connections(t)
	app := connect(t, appURL)

	// Fresh connection, no context set, plain query (no tx set_config).
	var n int
	err := app.QueryRow(context.Background(),
		`SELECT count(*) FROM consent.consent_vault`).Scan(&n)
	if err == nil {
		t.Fatalf("query with no hospital context returned %d rows — should have FAILED CLOSED", n)
	}
	if code := pgCode(err); code != sqlUndefinedObject {
		t.Logf("note: failed as expected but with code %q (%v)", code, err)
	}
}

// TestBogusContext: an attacker-controlled / malformed-but-valid UUID context
// yields zero rows, never another tenant's data.
func TestBogusContext(t *testing.T) {
	adminURL, appURL := connections(t)
	admin := connect(t, adminURL)
	app := connect(t, appURL)

	key := randomPatientKey(t)
	seedConsentRow(t, admin, hospitalA, key)

	if got := countUnderContext(t, app, uuid.New().String(), []string{key}); got != 0 {
		t.Fatalf("bogus hospital context: got %d rows, want 0", got)
	}
}

// TestAppendOnlyDeniedForApp: the runtime role has no UPDATE/DELETE privilege on
// consent_vault — the third append-only backstop (alongside the trigger and RLS).
// The grant check fires before the trigger is ever reached.
func TestAppendOnlyDeniedForApp(t *testing.T) {
	adminURL, appURL := connections(t)
	admin := connect(t, adminURL)
	app := connect(t, appURL)

	key := randomPatientKey(t)
	seedConsentRow(t, admin, hospitalA, key)

	ctx := context.Background()
	// Both statements run under a valid context so RLS is not what blocks them —
	// the missing privilege is.
	setAndExpectDenied := func(op, sql string) {
		tx, err := app.Begin(ctx)
		if err != nil {
			t.Fatalf("%s: begin: %v", op, err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, "SELECT set_config('app.hospital_id', $1, true)", hospitalA); err != nil {
			t.Fatalf("%s: set context: %v", op, err)
		}
		_, err = tx.Exec(ctx, sql, key)
		if err == nil {
			t.Fatalf("%s on consent_vault SUCCEEDED as dpdp_app — append-only broken", op)
		}
		if code := pgCode(err); code != sqlInsufficientPrivilege {
			t.Fatalf("%s: expected permission-denied (%s), got %q: %v", op, sqlInsufficientPrivilege, code, err)
		}
	}
	setAndExpectDenied("UPDATE", `UPDATE consent.consent_vault SET status='WITHDRAWN' WHERE patient_key=$1`)
	setAndExpectDenied("DELETE", `DELETE FROM consent.consent_vault WHERE patient_key=$1`)
}

// TestTriggerBlocksMutationEvenForOwner: the belt-and-suspenders trigger blocks
// UPDATE/DELETE even for a privileged connection that CAN pass the grant check
// (the superuser/owner). This proves the append-only guarantee does not rest on
// the grant alone.
func TestTriggerBlocksMutationEvenForOwner(t *testing.T) {
	adminURL, _ := connections(t)
	admin := connect(t, adminURL)

	key := randomPatientKey(t)
	seedConsentRow(t, admin, hospitalA, key)

	_, err := admin.Exec(context.Background(),
		`UPDATE consent.consent_vault SET status='WITHDRAWN' WHERE patient_key=$1`, key)
	if err == nil {
		t.Fatal("UPDATE as owner SUCCEEDED — append-only trigger not firing")
	}
	if code := pgCode(err); code != sqlRestrictViolation {
		t.Fatalf("expected append-only trigger (restrict_violation %s), got %q: %v", sqlRestrictViolation, code, err)
	}
}

// TestAppRoleIsNotPrivileged is the regression guard for the original silent
// failure: the app connected as a BYPASSRLS superuser, so RLS never applied.
// The runtime role must be neither.
func TestAppRoleIsNotPrivileged(t *testing.T) {
	_, appURL := connections(t)
	app := connect(t, appURL)

	var isSuper, bypassRLS bool
	err := app.QueryRow(context.Background(),
		`SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`,
	).Scan(&isSuper, &bypassRLS)
	if err != nil {
		t.Fatalf("read current role attributes: %v", err)
	}
	if isSuper {
		t.Error("runtime role is a SUPERUSER — RLS is silently bypassed")
	}
	if bypassRLS {
		t.Error("runtime role has BYPASSRLS — RLS is silently bypassed")
	}
}
