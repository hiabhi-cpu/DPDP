//go:build integration

// This file extends the tenant-isolation suite (see tenant_isolation_test.go for
// the package doc and the shared harness) with integration coverage for
// repository.ActiveHMSPatientIDs — the batch "has any active consent?" lookup
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

// TestActiveHMSPatientIDsFindsActiveConsent is the base case: a patient with one
// active purpose is reported active, and an unknown key is simply absent.
func TestActiveHMSPatientIDsFindsActiveConsent(t *testing.T) {
	ctx := context.Background()
	adminURL, _ := connections(t)
	admin := connect(t, adminURL)
	pool := newStatsPool(t)

	hosp := createStatsHospital(t, admin)
	consented := randomPatientKey(t)
	stranger := randomPatientKey(t)

	insertConsentRow(t, admin, hosp, consented, "CONSENT_GIVEN", "ACTIVE", `{"treatment":"ACTIVE"}`)

	repo := repository.New(pool)
	active, err := repo.ActiveHMSPatientIDs(ctx, hosp, []string{hmsIDFor(consented), hmsIDFor(stranger)})
	if err != nil {
		t.Fatalf("ActiveHMSPatientIDs: %v", err)
	}
	if !active[hmsIDFor(consented)] {
		t.Fatalf("consented key not reported active — RLS context missing?")
	}
	if active[hmsIDFor(stranger)] {
		t.Fatalf("stranger with no consent rows reported active")
	}
}

// TestActiveHMSPatientIDsLatestRowWins verifies a later full withdrawal supersedes
// the earlier grant. consent_vault is an append-only version chain, so a naive
// "any ACTIVE row exists" query would wrongly report this patient active.
func TestActiveHMSPatientIDsLatestRowWins(t *testing.T) {
	ctx := context.Background()
	adminURL, _ := connections(t)
	admin := connect(t, adminURL)
	pool := newStatsPool(t)

	hosp := createStatsHospital(t, admin)
	key := randomPatientKey(t)

	insertConsentRowV(t, admin, hosp, key, "CONSENT_GIVEN", "ACTIVE", `{"treatment":"ACTIVE"}`, 1)
	insertConsentRowV(t, admin, hosp, key, "WITHDRAWAL", "WITHDRAWN", `{"treatment":"WITHDRAWN"}`, 2)

	repo := repository.New(pool)
	active, err := repo.ActiveHMSPatientIDs(ctx, hosp, []string{hmsIDFor(key)})
	if err != nil {
		t.Fatalf("ActiveHMSPatientIDs: %v", err)
	}
	if active[hmsIDFor(key)] {
		t.Fatalf("fully withdrawn patient reported active — latest row not winning")
	}
}

// TestActiveHMSPatientIDsPartialWithdrawalStillActive pins the granular-consent
// rule: one purpose withdrawn while another stays active still blocks capture,
// so the queue must still badge them. This is AnyActive(), not status.
func TestActiveHMSPatientIDsPartialWithdrawalStillActive(t *testing.T) {
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
	active, err := repo.ActiveHMSPatientIDs(ctx, hosp, []string{hmsIDFor(key)})
	if err != nil {
		t.Fatalf("ActiveHMSPatientIDs: %v", err)
	}
	if !active[hmsIDFor(key)] {
		t.Fatalf("partially withdrawn patient reported inactive — one purpose is still ACTIVE")
	}
}

// TestActiveHMSPatientIDsIsolatesByHospital proves the RLS boundary holds: asking
// hospital A about a patient that only hospital B consented under must return
// nothing. A leak here would badge the wrong hospital's patients.
func TestActiveHMSPatientIDsIsolatesByHospital(t *testing.T) {
	ctx := context.Background()
	adminURL, _ := connections(t)
	admin := connect(t, adminURL)
	pool := newStatsPool(t)

	hospA := createStatsHospital(t, admin)
	hospB := createStatsHospital(t, admin)
	key := randomPatientKey(t)

	insertConsentRow(t, admin, hospB, key, "CONSENT_GIVEN", "ACTIVE", `{"treatment":"ACTIVE"}`)

	repo := repository.New(pool)
	active, err := repo.ActiveHMSPatientIDs(ctx, hospA, []string{hmsIDFor(key)})
	if err != nil {
		t.Fatalf("ActiveHMSPatientIDs(A): %v", err)
	}
	if active[hmsIDFor(key)] {
		t.Fatalf("hospital A sees hospital B's consent — RLS leak")
	}
}

// TestActiveHMSPatientIDsEmptyInput guards the short-circuit: no IDs means no
// query, and must not error.
func TestActiveHMSPatientIDsEmptyInput(t *testing.T) {
	ctx := context.Background()
	adminURL, _ := connections(t)
	admin := connect(t, adminURL)
	pool := newStatsPool(t)

	hosp := createStatsHospital(t, admin)

	repo := repository.New(pool)
	active, err := repo.ActiveHMSPatientIDs(ctx, hosp, nil)
	if err != nil {
		t.Fatalf("ActiveHMSPatientIDs(nil): %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("empty input returned %d entries, want 0", len(active))
	}
}

// The reason this lookup was re-keyed: a family shares one mobile, so keying the
// queue's "already consented?" answer on patient_key answers for the HOUSEHOLD.
// The mother consented; the son has not. Keyed by mobile, the son comes back
// active — reception badges him "Already consented" and DISABLES his Send code
// button, so he is silently denied consent capture with no error anywhere.
//
// Only real SQL can prove this: both rows share a patient_key, so a fake keyed
// by the HMS ID would pass against a query that ignored it.
func TestActiveHMSPatientIDsDiscriminatesWithinAHousehold(t *testing.T) {
	ctx := context.Background()
	adminURL, _ := connections(t)
	admin := connect(t, adminURL)
	pool := newStatsPool(t)

	hosp := createStatsHospital(t, admin)

	// ONE mobile, therefore ONE patient_key, shared by mother and son.
	householdKey := randomPatientKey(t)
	insertConsentRowFor(t, admin, hosp, householdKey, "PA-mother", "CONSENT_GIVEN", "ACTIVE", `{"treatment":"ACTIVE"}`)

	active, err := repository.New(pool).ActiveHMSPatientIDs(ctx, hosp, []string{"PA-mother", "PA-son"})
	if err != nil {
		t.Fatalf("ActiveHMSPatientIDs: %v", err)
	}
	if !active["PA-mother"] {
		t.Fatal("the mother consented but is not reported active")
	}
	if active["PA-son"] {
		t.Fatal("the son has NOT consented but is reported active — the queue would badge him " +
			"'already consented' and disable his Send code button, silently denying him capture")
	}
}
