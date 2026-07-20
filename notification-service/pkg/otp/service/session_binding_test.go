package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/hiabhi-cpu/notification-service/pkg/otp/model"
	"github.com/hiabhi-cpu/notification-service/pkg/otp/repository"
)

// newSessionService mirrors newClaimService (claim_service_test.go) but hands
// back the store, so a test can plant a SessionState directly.
func newSessionService(t *testing.T) (OTPService, repository.OTPStore) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	store := repository.NewRedisStore(redis.NewClient(&redis.Options{Addr: mr.Addr()}))
	return NewOTPService(store, &captureSMS{}), store
}

// A session minted for the son must not authorize a capture naming the mother,
// even though both share the mobile the OTP was sent to.
func TestValidateSessionRejectsMismatchedPatient(t *testing.T) {
	ctx := context.Background()
	svc, repo := newSessionService(t)

	if err := repo.SaveSession(ctx, "sess-1", model.SessionState{
		Mobile:    "9876543210",
		Ref:       "PA-son",
		Verified:  true,
		ExpiresAt: time.Now().Add(time.Minute),
	}, time.Minute); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	if err := svc.ValidateSession(ctx, "sess-1", "9876543210", "PA-son"); err != nil {
		t.Fatalf("matching patient must validate, got %v", err)
	}

	err := svc.ValidateSession(ctx, "sess-1", "9876543210", "PA-mother")
	if !errors.Is(err, ErrSessionNotVerified) {
		t.Fatalf("err = %v, want ErrSessionNotVerified for a different family member", err)
	}
}

// A session with no ref cannot name a patient, so it cannot consent for one.
func TestValidateSessionRejectsRefLessSession(t *testing.T) {
	ctx := context.Background()
	svc, repo := newSessionService(t)

	if err := repo.SaveSession(ctx, "sess-2", model.SessionState{
		Mobile:    "9876543210",
		Verified:  true,
		ExpiresAt: time.Now().Add(time.Minute),
	}, time.Minute); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	err := svc.ValidateSession(ctx, "sess-2", "9876543210", "PA-son")
	if !errors.Is(err, ErrSessionNotVerified) {
		t.Fatalf("err = %v, want ErrSessionNotVerified for a ref-less session", err)
	}
}
