package session

import (
	"context"
	"errors"
	"testing"
)

func TestMemStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	want := Session{UserID: "u1", Email: "a@b.c", Role: "admin", HospitalID: "h1"}

	id, err := s.Create(ctx, want)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == "" {
		t.Fatal("Create returned empty id")
	}

	got, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != want {
		t.Fatalf("Get = %+v, want %+v", got, want)
	}

	if err := s.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete = %v, want ErrNotFound", err)
	}
}

func TestMemStoreUnknownID(t *testing.T) {
	if _, err := NewMemStore().Get(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get unknown = %v, want ErrNotFound", err)
	}
}
