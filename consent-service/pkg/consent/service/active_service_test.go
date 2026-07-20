package service

import (
	"context"
	"testing"

	"github.com/hiabhi-cpu/consent-service/pkg/consent/repository"
)

// fakeActiveRepo implements just enough of ConsentRepository for the test.
type fakeActiveRepo struct {
	repository.ConsentRepository // embed for the methods we don't exercise
	gotHospitalID                string
	gotIDs                       []string
	active                       map[string]bool
}

func (f *fakeActiveRepo) ActiveHMSPatientIDs(_ context.Context, hospitalID string, ids []string) (map[string]bool, error) {
	f.gotHospitalID = hospitalID
	f.gotIDs = ids
	return f.active, nil
}

// The lookup passes HMS patient IDs straight through: no mobile is hashed, and
// none is even accepted. This used to map mobiles to patient keys and back,
// which is exactly what made it answer for a household rather than a person.
func TestActiveHMSPatientIDsPassesIDsThrough(t *testing.T) {
	repo := &fakeActiveRepo{active: map[string]bool{"PA-mother": true}}
	svc := NewConsentService(repo, fakeSecrets{}, nil)

	got, err := svc.ActiveHMSPatientIDs(context.Background(), "hosp-1", []string{"PA-mother", "PA-son"})
	if err != nil {
		t.Fatalf("ActiveHMSPatientIDs: %v", err)
	}
	if len(got) != 1 || got[0] != "PA-mother" {
		t.Fatalf("active = %v, want [PA-mother] — the son has not consented", got)
	}
	if repo.gotHospitalID != "hosp-1" {
		t.Fatalf("repo got hospital %q, want hosp-1", repo.gotHospitalID)
	}
	if len(repo.gotIDs) != 2 || repo.gotIDs[0] != "PA-mother" || repo.gotIDs[1] != "PA-son" {
		t.Fatalf("repo got %v, want both IDs unchanged", repo.gotIDs)
	}
}

// Output order is pinned: the repo answers with a map, whose iteration order is
// random, so the response must follow the caller's input order.
func TestActiveHMSPatientIDsPreservesInputOrder(t *testing.T) {
	ids := []string{"PA-001", "PA-002", "PA-003"}
	repo := &fakeActiveRepo{active: map[string]bool{"PA-003": true, "PA-001": true}}
	svc := NewConsentService(repo, fakeSecrets{}, nil)

	for i := 0; i < 20; i++ { // repeat: map order varies per iteration
		got, err := svc.ActiveHMSPatientIDs(context.Background(), "hosp-1", ids)
		if err != nil {
			t.Fatalf("ActiveHMSPatientIDs: %v", err)
		}
		if len(got) != 2 || got[0] != "PA-001" || got[1] != "PA-003" {
			t.Fatalf("active = %v, want [PA-001 PA-003] in input order", got)
		}
	}
}

// A patient with no consent yields an empty (non-nil) slice, so the JSON is []
// rather than null.
func TestActiveHMSPatientIDsEmptyResult(t *testing.T) {
	svc := NewConsentService(&fakeActiveRepo{active: map[string]bool{}}, fakeSecrets{}, nil)

	got, err := svc.ActiveHMSPatientIDs(context.Background(), "hosp-1", []string{"PA-001"})
	if err != nil {
		t.Fatalf("ActiveHMSPatientIDs: %v", err)
	}
	if got == nil {
		t.Fatalf("active = nil, want empty slice (marshals to [] not null)")
	}
	if len(got) != 0 {
		t.Fatalf("active = %v, want empty", got)
	}
}
