package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/hiabhi-cpu/integration-service/pkg/pending/model"
)

func newTestStore(t *testing.T) (*RedisStore, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return NewRedisStore(client), mr
}

func sampleReg(hospital, hms string) model.PendingRegistration {
	return model.PendingRegistration{
		HospitalID: hospital, HMSPatientID: hms,
		Name: "Asha Rao", Mobile: "9876543210", RegisteredAt: "2026-07-13T10:00:00Z",
	}
}

func TestUpsertAndGet(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	if err := s.Upsert(ctx, sampleReg("hosp-1", "PA-1")); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := s.Get(ctx, "hosp-1", "PA-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil || got.Mobile != "9876543210" {
		t.Fatalf("got = %+v, want the stored record", got)
	}
}

func TestGetMissingReturnsNilNil(t *testing.T) {
	s, _ := newTestStore(t)
	got, err := s.Get(context.Background(), "hosp-1", "nope")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != nil {
		t.Fatalf("got = %+v, want nil", got)
	}
}

func TestUpsertSetsTTL(t *testing.T) {
	s, mr := newTestStore(t)
	if err := s.Upsert(context.Background(), sampleReg("hosp-1", "PA-1")); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	ttl := mr.TTL("pending:hosp-1:PA-1")
	if ttl <= 0 || ttl > PendingTTL {
		t.Fatalf("ttl = %v, want (0, %v]", ttl, PendingTTL)
	}
}

func TestUpsertIsIdempotentOverwrite(t *testing.T) {
	s := mustStore(t)
	ctx := context.Background()
	_ = s.Upsert(ctx, sampleReg("hosp-1", "PA-1"))
	second := sampleReg("hosp-1", "PA-1")
	second.Name = "Updated Name"
	_ = s.Upsert(ctx, second)
	got, _ := s.Get(ctx, "hosp-1", "PA-1")
	if got.Name != "Updated Name" {
		t.Fatalf("Name = %q, want overwrite", got.Name)
	}
}

func TestListIsHospitalScoped(t *testing.T) {
	s := mustStore(t)
	ctx := context.Background()
	_ = s.Upsert(ctx, sampleReg("hosp-1", "PA-1"))
	_ = s.Upsert(ctx, sampleReg("hosp-1", "PA-2"))
	_ = s.Upsert(ctx, sampleReg("hosp-2", "PA-9"))

	list, err := s.List(ctx, "hosp-1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len(list) = %d, want 2 (hosp-2 must not leak)", len(list))
	}
}

func mustStore(t *testing.T) *RedisStore {
	t.Helper()
	s, _ := newTestStore(t)
	return s
}

func TestSetStatusPreservesRecord(t *testing.T) {
	s, mr := newTestStore(t)
	ctx := context.Background()
	_ = s.Upsert(ctx, sampleReg("hosp-1", "PA-1"))
	mr.FastForward(1 * time.Hour) // TTL now ~71h, not a fresh 72h
	if err := s.SetStatus(ctx, "hosp-1", "PA-1", "CODE_SENT"); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	// SetStatus must preserve the REMAINING ttl, not reset the 72h window.
	if ttl := mr.TTL(key("hosp-1", "PA-1")); ttl > PendingTTL-30*time.Minute {
		t.Fatalf("TTL = %v, want it preserved near 71h (SetStatus reset the retention window)", ttl)
	}
	got, _ := s.Get(ctx, "hosp-1", "PA-1")
	if got == nil || got.Status != "CODE_SENT" {
		t.Fatalf("status = %+v, want CODE_SENT", got)
	}
	if got.Mobile != "9876543210" {
		t.Fatalf("SetStatus clobbered the record: %+v", got)
	}
}

func TestSetStatusUnknownReturnsErrNotFound(t *testing.T) {
	s := mustStore(t)
	if err := s.SetStatus(context.Background(), "hosp-1", "nope", "DONE"); err != ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
