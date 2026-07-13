package service

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/hiabhi-cpu/notification-service/pkg/otp/repository"
)

// captureSMS records the OTP the service sent, so the test can resolve it.
type captureSMS struct{ last string }

func (c *captureSMS) SendOTP(_ context.Context, _ string, otp string) error { c.last = otp; return nil }

func newClaimService(t *testing.T) (OTPService, *captureSMS) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	store := repository.NewRedisStore(redis.NewClient(&redis.Options{Addr: mr.Addr()}))
	sms := &captureSMS{}
	return NewOTPService(store, sms), sms
}

func TestSendClaimThenResolve(t *testing.T) {
	svc, sms := newClaimService(t)
	ctx := context.Background()

	if _, err := svc.SendClaim(ctx, "hosp-1", "9876543210", "PA-1"); err != nil {
		t.Fatalf("SendClaim: %v", err)
	}
	// The patient enters the exact code that was texted.
	res, err := svc.ResolveClaim(ctx, "hosp-1", sms.last)
	if err != nil {
		t.Fatalf("ResolveClaim: %v", err)
	}
	if res.Mobile != "9876543210" || res.Ref != "PA-1" || res.SessionID == "" {
		t.Fatalf("resolve result = %+v", res)
	}
	// The resolved session validates for that mobile (capture would accept it).
	if err := svc.ValidateSession(ctx, res.SessionID, "9876543210"); err != nil {
		t.Fatalf("ValidateSession after resolve: %v", err)
	}
}

func TestResolveWrongCodeFails(t *testing.T) {
	svc, sms := newClaimService(t)
	ctx := context.Background()
	_, _ = svc.SendClaim(ctx, "hosp-1", "9876543210", "PA-1")
	wrong := "000000"
	if wrong == sms.last {
		wrong = "111111"
	}
	if _, err := svc.ResolveClaim(ctx, "hosp-1", wrong); err == nil {
		t.Fatal("expected error for wrong code")
	}
}

func TestResolveIsHospitalScoped(t *testing.T) {
	svc, sms := newClaimService(t)
	ctx := context.Background()
	_, _ = svc.SendClaim(ctx, "hosp-1", "9876543210", "PA-1")
	// A different hospital must not resolve hosp-1's code.
	if _, err := svc.ResolveClaim(ctx, "hosp-2", sms.last); err == nil {
		t.Fatal("expected hosp-2 not to resolve hosp-1's code")
	}
}
