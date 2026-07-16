//go:build integration

// This file extends the tenant-isolation suite (see tenant_isolation_test.go for
// the package doc and the shared harness) with integration coverage for
// repository.ActivePatientKeys — the batch "has any active consent?" lookup
// behind the reception queue's already-consented badge.
//
// It must run against a REAL Postgres under the dpdp_app role. consent_vault is
// FORCE ROW LEVEL SECURITY, and the bug this suite exists to catch (querying
// outside an RLS-scoped transaction) returns zero rows with NO error — which
// looks exactly like "nobody has consented", i.e. the bug the feature fixes.
// A mock cannot prove any of this.
//
//	go test -tags=integration ./test/... -v
package isolation

import (
	"context"
	"testing"

	"github.com/hiabhi-cpu/consent-service/pkg/consent/repository"
)

// TestActivePatientKeysFindsActiveConsent is the base case: a patient with one
// active purpose is reported active, and an unknown key is simply absent.
func TestActivePatientKeysFindsActiveConsent(t *testing.T) {
	ctx := context.Background()
	adminURL, _ := connections(t)
	admin := connect(t, adminURL)
	pool := newStatsPool(t)

	hosp := createStatsHospital(t, admin)
	consented := randomPatientKey(t)
	stranger := randomPatientKey(t)

	insertConsentRow(t, admin, hosp, consented, "CONSENT_GIVEN", "ACTIVE", `{"treatment":"ACTIVE"}`)

	repo := repository.New(pool)
	active, err := repo.ActivePatientKeys(ctx, hosp, []string{consented, stranger})
	if err != nil {
		t.Fatalf("ActivePatientKeys: %v", err)
	}
	if !active[consented] {
		t.Fatalf("consented key not reported active — RLS context missing?")
	}
	if active[stranger] {
		t.Fatalf("stranger with no consent rows reported active")
	}
}

// TestActivePatientKeysLatestRowWins verifies a later full withdrawal supersedes
// the earlier grant. consent_vault is an append-only version chain, so a naive
// "any ACTIVE row exists" query would wrongly report this patient active.
func TestActivePatientKeysLatestRowWins(t *testing.T) {
	ctx := context.Background()
	adminURL, _ := connections(t)
	admin := connect(t, adminURL)
	pool := newStatsPool(t)

	hosp := createStatsHospital(t, admin)
	key := randomPatientKey(t)

	insertConsentRowV(t, admin, hosp, key, "CONSENT_GIVEN", "ACTIVE", `{"treatment":"ACTIVE"}`, 1)
	insertConsentRowV(t, admin, hosp, key, "WITHDRAWAL", "WITHDRAWN", `{"treatment":"WITHDRAWN"}`, 2)

	repo := repository.New(pool)
	active, err := repo.ActivePatientKeys(ctx, hosp, []string{key})
	if err != nil {
		t.Fatalf("ActivePatientKeys: %v", err)
	}
	if active[key] {
		t.Fatalf("fully withdrawn patient reported active — latest row not winning")
	}
}

// TestActivePatientKeysPartialWithdrawalStillActive pins the granular-consent
// rule: one purpose withdrawn while another stays active still blocks capture,
// so the queue must still badge them. This is AnyActive(), not status.
func TestActivePatientKeysPartialWithdrawalStillActive(t *testing.T) {
	ctx := context.Background()
	adminURL, _ := connections(t)
	admin := connect(t, adminURL)
	pool := newStatsPool(t)

	hosp := createStatsHospital(t, admin)
	key := randomPatientKey(t)

	insertConsentRowV(t, admin, hosp, key, "CONSENT_GIVEN", "ACTIVE",
		`{"treatment":"ACTIVE","insurance":"ACTIVE"}`, 1)
	insertConsentRowV(t, admin, hosp, key, "WITHDRAWAL", "ACTIVE",
		`{"treatment":"ACTIVE","insurance":"WITHDRAWN"}`, 2)

	repo := repository.New(pool)
	active, err := repo.ActivePatientKeys(ctx, hosp, []string{key})
	if err != nil {
		t.Fatalf("ActivePatientKeys: %v", err)
	}
	if !active[key] {
		t.Fatalf("partially withdrawn patient reported inactive — one purpose is still ACTIVE")
	}
}

// TestActivePatientKeysIsolatesByHospital proves the RLS boundary holds: asking
// hospital A about a key that only hospital B consented under must return
// nothing. A leak here would badge the wrong hospital's patients.
func TestActivePatientKeysIsolatesByHospital(t *testing.T) {
	ctx := context.Background()
	adminURL, _ := connections(t)
	admin := connect(t, adminURL)
	pool := newStatsPool(t)

	hospA := createStatsHospital(t, admin)
	hospB := createStatsHospital(t, admin)
	key := randomPatientKey(t)

	insertConsentRow(t, admin, hospB, key, "CONSENT_GIVEN", "ACTIVE", `{"treatment":"ACTIVE"}`)

	repo := repository.New(pool)
	active, err := repo.ActivePatientKeys(ctx, hospA, []string{key})
	if err != nil {
		t.Fatalf("ActivePatientKeys(A): %v", err)
	}
	if active[key] {
		t.Fatalf("hospital A sees hospital B's consent — RLS leak")
	}
}

// TestActivePatientKeysEmptyInput guards the short-circuit: no keys means no
// query, and must not error.
func TestActivePatientKeysEmptyInput(t *testing.T) {
	ctx := context.Background()
	adminURL, _ := connections(t)
	admin := connect(t, adminURL)
	pool := newStatsPool(t)

	hosp := createStatsHospital(t, admin)

	repo := repository.New(pool)
	active, err := repo.ActivePatientKeys(ctx, hosp, nil)
	if err != nil {
		t.Fatalf("ActivePatientKeys(nil): %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("empty input returned %d entries, want 0", len(active))
	}
}
