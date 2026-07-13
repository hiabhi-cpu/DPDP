package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newClaimStore(t *testing.T) (OTPStore, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	return NewRedisStore(redis.NewClient(&redis.Options{Addr: mr.Addr()})), mr
}

func TestSaveAndGetClaimOTP(t *testing.T) {
	s, _ := newClaimStore(t)
	ctx := context.Background()
	if err := s.SaveClaimOTP(ctx, "ref-1", "HASH", "9876543210", "PA-1", "hosp-1", time.Minute); err != nil {
		t.Fatalf("save: %v", err)
	}
	hash, mobile, ref, err := s.GetClaimOTP(ctx, "ref-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if hash != "HASH" || mobile != "9876543210" || ref != "PA-1" {
		t.Fatalf("got (%q,%q,%q)", hash, mobile, ref)
	}
	// GetOTPHash must still work on the 3-field value (walk-in path).
	h, m, err := s.GetOTPHash(ctx, "ref-1")
	if err != nil || h != "HASH" || m != "9876543210" {
		t.Fatalf("GetOTPHash tolerance: (%q,%q,%v)", h, m, err)
	}
}

func TestClaimMembersAndRemove(t *testing.T) {
	s, _ := newClaimStore(t)
	ctx := context.Background()
	_ = s.SaveClaimOTP(ctx, "ref-1", "H1", "9000000001", "PA-1", "hosp-1", time.Minute)
	_ = s.SaveClaimOTP(ctx, "ref-2", "H2", "9000000002", "PA-2", "hosp-1", time.Minute)
	_ = s.SaveClaimOTP(ctx, "ref-9", "H9", "9000000009", "PA-9", "hosp-2", time.Minute)

	members, err := s.ClaimMembers(ctx, "hosp-1")
	if err != nil || len(members) != 2 {
		t.Fatalf("members hosp-1 = %v (err %v), want 2", members, err)
	}
	if err := s.RemoveClaim(ctx, "hosp-1", "ref-1"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	members, _ = s.ClaimMembers(ctx, "hosp-1")
	if len(members) != 1 || members[0] != "ref-2" {
		t.Fatalf("after remove = %v, want [ref-2]", members)
	}
}

func TestIncrResolveAttempts(t *testing.T) {
	s, _ := newClaimStore(t)
	ctx := context.Background()
	n, err := s.IncrResolveAttempts(ctx, "hosp-1", time.Minute)
	if err != nil || n != 1 {
		t.Fatalf("first = %d (err %v), want 1", n, err)
	}
	n, _ = s.IncrResolveAttempts(ctx, "hosp-1", time.Minute)
	if n != 2 {
		t.Fatalf("second = %d, want 2", n)
	}
}
