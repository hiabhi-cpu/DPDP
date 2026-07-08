package service

import (
	"context"
	"testing"

	"github.com/hiabhi-cpu/consent-service/pkg/consent/model"
	"github.com/hiabhi-cpu/consent-service/pkg/consent/repository"
)

// fakeStatsRepo implements just enough of ConsentRepository for the stats test.
type fakeStatsRepo struct {
	repository.ConsentRepository // embed for the methods we don't exercise
	gotHospitalID                string
	gotWindow                    int
	ret                          *model.ConsentStats
}

func (f *fakeStatsRepo) GetStats(_ context.Context, hospitalID string, windowDays int) (*model.ConsentStats, error) {
	f.gotHospitalID = hospitalID
	f.gotWindow = windowDays
	return f.ret, nil
}

func TestStatsPassesHospitalAndWindow(t *testing.T) {
	repo := &fakeStatsRepo{ret: &model.ConsentStats{Consents: model.StatusCounts{Active: 3}}}
	svc := NewConsentService(repo, nil, nil)

	got, err := svc.Stats(context.Background(), "hosp-1", 7)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.gotHospitalID != "hosp-1" || repo.gotWindow != 7 {
		t.Fatalf("repo called with (%q,%d), want (hosp-1,7)", repo.gotHospitalID, repo.gotWindow)
	}
	if got.Consents.Active != 3 {
		t.Fatalf("active = %d, want 3", got.Consents.Active)
	}
}

func TestStatsDefaultsNonPositiveWindow(t *testing.T) {
	repo := &fakeStatsRepo{ret: &model.ConsentStats{}}
	svc := NewConsentService(repo, nil, nil)

	if _, err := svc.Stats(context.Background(), "hosp-1", 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.gotWindow != DefaultStatsWindowDays {
		t.Fatalf("window = %d, want default %d", repo.gotWindow, DefaultStatsWindowDays)
	}
}
