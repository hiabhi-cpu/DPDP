package service

import (
	"context"
	"errors"
	"testing"

	"github.com/hiabhi-cpu/consent-service/pkg/consent/model"
	"github.com/hiabhi-cpu/consent-service/pkg/consent/repository"
	sharedcrypto "github.com/hiabhi-cpu/shared/crypto"
)

const (
	testSalt     = "test-system-salt"
	testHospKey  = "test-hospital-key"
	familyMobile = "9876543210"
)

// fakeIdentityRepo keys its canned rows by the full identity pair, so a lookup
// scoped only by patient_key cannot find them.
type fakeIdentityRepo struct {
	repository.ConsentRepository // embedded: methods we don't exercise panic loudly
	existing                     map[string]*model.Consent
	hmsRows                      map[string]*model.Consent
	inserted                     []*model.Consent
	withdrawn                    []*model.Consent
}

func (f *fakeIdentityRepo) GetLatestByPatientAndHMS(_ context.Context, _, patientKey, hmsPatientID string) (*model.Consent, error) {
	return f.existing[patientKey+"|"+hmsPatientID], nil
}

func (f *fakeIdentityRepo) GetLatestByHMSPatientID(_ context.Context, _, hmsPatientID string) (*model.Consent, error) {
	return f.hmsRows[hmsPatientID], nil
}

func (f *fakeIdentityRepo) EnqueueAudit(context.Context, *model.OutboxRecord) error { return nil }

func (f *fakeIdentityRepo) GetByIdempotencyKey(context.Context, string, string) (*model.Consent, error) {
	return nil, nil
}

func (f *fakeIdentityRepo) Insert(_ context.Context, c *model.Consent, _ *model.OutboxRecord) error {
	f.inserted = append(f.inserted, c)
	return nil
}

func (f *fakeIdentityRepo) InsertWithdrawn(_ context.Context, c *model.Consent, _ *model.OutboxRecord) error {
	f.withdrawn = append(f.withdrawn, c)
	return nil
}

type fakeSecrets struct{}

func (fakeSecrets) GetSystemSalt(context.Context) (string, error) { return testSalt, nil }
func (fakeSecrets) GetHospitalKey(context.Context, string) (string, error) {
	return testHospKey, nil
}

type okSessions struct{}

func (okSessions) Verify(context.Context, string, string) error { return nil }

func familyKey() string {
	return sharedcrypto.ComputePatientKey(familyMobile, testSalt, testHospKey)
}

// The bug this whole change exists to fix: a mother and son share one mobile.
// Her active consent must not block his.
func TestCaptureAllowsSecondFamilyMemberOnSharedMobile(t *testing.T) {
	repo := &fakeIdentityRepo{existing: map[string]*model.Consent{
		familyKey() + "|PA-mother": {
			Purposes: map[string]model.PurposeState{"treatment": model.PurposeActive},
		},
	}}
	svc := NewConsentService(repo, fakeSecrets{}, okSessions{})

	_, created, err := svc.Capture(context.Background(), "hosp-1", "1.2.3.4", &model.CaptureConsentRequest{
		Mobile:       familyMobile,
		SessionID:    "sess-son",
		Purposes:     []string{"treatment"},
		HMSPatientID: "PA-son",
	})
	if err != nil {
		t.Fatalf("son's capture on the shared family mobile must succeed, got %v", err)
	}
	if !created {
		t.Fatal("expected a newly created consent row for the son")
	}
	if len(repo.inserted) != 1 || repo.inserted[0].HMSPatientID != "PA-son" {
		t.Fatalf("inserted = %+v, want one row for PA-son", repo.inserted)
	}
}

// The block must still fire for the SAME patient — this is the guard that keeps
// duplicate consents out, and re-scoping identity must not disable it.
func TestCaptureStillBlocksTheSamePatientTwice(t *testing.T) {
	repo := &fakeIdentityRepo{existing: map[string]*model.Consent{
		familyKey() + "|PA-mother": {
			Purposes: map[string]model.PurposeState{"treatment": model.PurposeActive},
		},
	}}
	svc := NewConsentService(repo, fakeSecrets{}, okSessions{})

	_, _, err := svc.Capture(context.Background(), "hosp-1", "1.2.3.4", &model.CaptureConsentRequest{
		Mobile:       familyMobile,
		SessionID:    "sess-mother-again",
		Purposes:     []string{"treatment"},
		HMSPatientID: "PA-mother",
	})
	if !errors.Is(err, ErrActiveConsentExists) {
		t.Fatalf("err = %v, want ErrActiveConsentExists", err)
	}
}

// Withdrawal is the mirror of the capture bug and the more dangerous half: on
// the old scoping the son's withdrawal found — and revoked — his mother's
// consent. Here he has none of his own, so there is nothing to withdraw.
func TestWithdrawDoesNotTouchARelativesConsent(t *testing.T) {
	repo := &fakeIdentityRepo{existing: map[string]*model.Consent{
		familyKey() + "|PA-mother": {
			Purposes: map[string]model.PurposeState{"treatment": model.PurposeActive},
		},
	}}
	svc := NewConsentService(repo, fakeSecrets{}, okSessions{})

	err := svc.Withdraw(context.Background(), "hosp-1", "1.2.3.4", &model.WithdrawConsentRequest{
		Mobile:       familyMobile,
		HMSPatientID: "PA-son",
		SessionID:    "sess-son",
	})
	if !errors.Is(err, ErrNoActiveConsent) {
		t.Fatalf("err = %v, want ErrNoActiveConsent — the son has no consent of his own", err)
	}
	if len(repo.withdrawn) != 0 {
		t.Fatalf("wrote %d withdrawal rows, want 0 — the mother's consent must be untouched", len(repo.withdrawn))
	}
}

// Check must answer for the patient named by hms_patient_id, not for whichever
// family member on that mobile consented most recently.
func TestCheckAnswersForTheNamedPatient(t *testing.T) {
	repo := &fakeIdentityRepo{hmsRows: map[string]*model.Consent{
		"PA-son": {
			PatientKey: familyKey(),
			Purposes:   map[string]model.PurposeState{"treatment": model.PurposeWithdrawn},
		},
	}}
	svc := NewConsentService(repo, fakeSecrets{}, okSessions{})

	resp, err := svc.Check(context.Background(), "hosp-1", "1.2.3.4", &model.CheckConsentRequest{
		HMSPatientID: "PA-son",
		Purpose:      "treatment",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Allowed {
		t.Fatal("son withdrew treatment; check must not report allowed")
	}
}
