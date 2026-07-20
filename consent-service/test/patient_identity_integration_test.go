//go:build integration

// This file extends the isolation suite (see tenant_isolation_test.go for the
// package doc and shared harness) with the database-level proof that a patient
// is identified by the PAIR (patient_key, hms_patient_id).
//
// Why it must live here and not in a unit test: the service-level tests drive a
// fake repository that is itself keyed by the pair, so they cannot prove the
// SQL discriminates — they would pass against a query that ignored
// hms_patient_id entirely. Only real Postgres can settle it.
//
// The scenario is the bug this branch exists to fix: a mother and son share one
// mobile number, so they share a patient_key. Before the fix, looking a patient
// up by patient_key alone returned whichever of them wrote last.
package isolation

import (
	"context"
	"testing"

	"github.com/hiabhi-cpu/consent-service/pkg/consent/repository"
)

func TestGetLatestByPatientAndHMSDiscriminatesWithinAHousehold(t *testing.T) {
	adminURL, _ := connections(t)
	admin := connect(t, adminURL)

	// One mobile, therefore ONE patient_key, shared by two people.
	householdKey := randomPatientKey(t)
	seedConsentRowFor(t, admin, hospitalA, householdKey, "PA-mother")
	seedConsentRowFor(t, admin, hospitalA, householdKey, "PA-son")

	pool := newStatsPool(t)
	repo := repository.New(pool)
	ctx := context.Background()

	mother, err := repo.GetLatestByPatientAndHMS(ctx, hospitalA, householdKey, "PA-mother")
	if err != nil {
		t.Fatalf("lookup mother: %v", err)
	}
	if mother == nil {
		t.Fatal("mother's row not found")
	}
	if mother.HMSPatientID != "PA-mother" {
		t.Fatalf("mother lookup returned %q — the query is not scoped by hms_patient_id", mother.HMSPatientID)
	}

	son, err := repo.GetLatestByPatientAndHMS(ctx, hospitalA, householdKey, "PA-son")
	if err != nil {
		t.Fatalf("lookup son: %v", err)
	}
	if son == nil {
		t.Fatal("son's row not found")
	}
	if son.HMSPatientID != "PA-son" {
		t.Fatalf("son lookup returned %q — the query is not scoped by hms_patient_id", son.HMSPatientID)
	}

	if mother.ID == son.ID {
		t.Fatal("both lookups returned the SAME row: patient_key alone is still deciding identity")
	}

	// A third household member who has never consented must come back empty,
	// not inherit a relative's row — this is what unblocks their capture.
	absent, err := repo.GetLatestByPatientAndHMS(ctx, hospitalA, householdKey, "PA-daughter")
	if err != nil {
		t.Fatalf("lookup daughter: %v", err)
	}
	if absent != nil {
		t.Fatalf("daughter has no consent but got row %s (hms=%q) — she would be wrongly blocked from consenting",
			absent.ID, absent.HMSPatientID)
	}
}
