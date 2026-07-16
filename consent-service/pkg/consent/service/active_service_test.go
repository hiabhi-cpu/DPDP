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
	gotKeys                      []string
	active                       map[string]bool
}

func (f *fakeActiveRepo) ActivePatientKeys(_ context.Context, hospitalID string, keys []string) (map[string]bool, error) {
	f.gotHospitalID = hospitalID
	f.gotKeys = keys
	return f.active, nil
}

// fakeSecrets returns fixed key material so patientKeyFor is deterministic.
type fakeSecrets struct{}

func (fakeSecrets) GetSystemSalt(_ context.Context) (string, error) { return "test-salt", nil }
func (fakeSecrets) GetHospitalKey(_ context.Context, _ string) (string, error) {
	return "test-key", nil
}

// TestActiveMobilesMapsKeysBackToMobiles is the core of this method: it hashes
// mobiles to patient keys on the way in and must map the repo's key-keyed answer
// back to the caller's mobiles on the way out.
func TestActiveMobilesMapsKeysBackToMobiles(t *testing.T) {
	ctx := context.Background()
	sp := fakeSecrets{}
	svc := NewConsentService(&fakeActiveRepo{}, sp, nil).(*consentService)

	// Derive the key the service will compute for the "consented" mobile, so the
	// fake repo can answer in the same key space the real one would.
	consentedKey, err := svc.patientKeyFor(ctx, "hosp-1", "9876543210")
	if err != nil {
		t.Fatalf("patientKeyFor: %v", err)
	}

	repo := &fakeActiveRepo{active: map[string]bool{consentedKey: true}}
	svc = NewConsentService(repo, sp, nil).(*consentService)

	got, err := svc.ActiveMobiles(ctx, "hosp-1", []string{"9876543210", "9000000000"})
	if err != nil {
		t.Fatalf("ActiveMobiles: %v", err)
	}
	if len(got) != 1 || got[0] != "9876543210" {
		t.Fatalf("active = %v, want [9876543210]", got)
	}
	if repo.gotHospitalID != "hosp-1" {
		t.Fatalf("repo got hospital %q, want hosp-1", repo.gotHospitalID)
	}
	if len(repo.gotKeys) != 2 {
		t.Fatalf("repo got %d keys, want 2", len(repo.gotKeys))
	}
	// The raw mobile must never be handed to the repository.
	for _, k := range repo.gotKeys {
		if k == "9876543210" || k == "9000000000" {
			t.Fatalf("raw mobile %q leaked into the repository call", k)
		}
	}
}

// TestActiveMobilesPreservesInputOrder pins deterministic output — the repo
// answers with a map, whose iteration order is random.
func TestActiveMobilesPreservesInputOrder(t *testing.T) {
	ctx := context.Background()
	sp := fakeSecrets{}
	svc := NewConsentService(&fakeActiveRepo{}, sp, nil).(*consentService)

	mobiles := []string{"9111111111", "9222222222", "9333333333"}
	activeKeys := map[string]bool{}
	for _, m := range []string{"9333333333", "9111111111"} {
		k, err := svc.patientKeyFor(ctx, "hosp-1", m)
		if err != nil {
			t.Fatalf("patientKeyFor: %v", err)
		}
		activeKeys[k] = true
	}

	svc = NewConsentService(&fakeActiveRepo{active: activeKeys}, sp, nil).(*consentService)

	for i := 0; i < 20; i++ { // repeat: map order varies per iteration
		got, err := svc.ActiveMobiles(ctx, "hosp-1", mobiles)
		if err != nil {
			t.Fatalf("ActiveMobiles: %v", err)
		}
		if len(got) != 2 || got[0] != "9111111111" || got[1] != "9333333333" {
			t.Fatalf("active = %v, want [9111111111 9333333333] in input order", got)
		}
	}
}

// TestActiveMobilesEmptyResult verifies a patient with no consent yields an empty
// (non-nil) slice, so the JSON is [] rather than null.
func TestActiveMobilesEmptyResult(t *testing.T) {
	svc := NewConsentService(&fakeActiveRepo{active: map[string]bool{}}, fakeSecrets{}, nil)

	got, err := svc.ActiveMobiles(context.Background(), "hosp-1", []string{"9876543210"})
	if err != nil {
		t.Fatalf("ActiveMobiles: %v", err)
	}
	if got == nil {
		t.Fatalf("active = nil, want empty slice (marshals to [] not null)")
	}
	if len(got) != 0 {
		t.Fatalf("active = %v, want empty", got)
	}
}
