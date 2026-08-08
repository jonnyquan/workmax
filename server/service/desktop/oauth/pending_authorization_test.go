package oauth

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func newPendingService(t *testing.T) (*PendingAuthorizationService, *time.Time) {
	t.Helper()
	s := NewPendingAuthorizationService()
	t.Cleanup(s.Stop)
	now := time.Date(2026, 5, 18, 10, 0, 0, 0, time.UTC)
	clock := &now
	s.setNowForTest(func() time.Time { return *clock })
	return s, clock
}

func sampleStoreInput() StoreInput {
	return StoreInput{
		ClientID:            "workmax-desktop",
		UID:                 42,
		RedirectURI:         "http://127.0.0.1:54321/oauth/callback",
		CodeChallenge:       "abc",
		CodeChallengeMethod: "S256",
		Scope:               "workagent",
		State:               "xyz-state",
	}
}

func TestPendingAuth_StoreConsume_Happy(t *testing.T) {
	s, _ := newPendingService(t)
	ctx := context.Background()

	id, err := s.Store(ctx, sampleStoreInput())
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty id")
	}

	pa, err := s.Consume(ctx, id)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if pa.UID != 42 || pa.ClientID != "workmax-desktop" || pa.State != "xyz-state" {
		t.Errorf("payload mismatch: %+v", pa)
	}

	// Idempotency: second Consume of same ID must fail.
	_, err = s.Consume(ctx, id)
	if !errors.Is(err, ErrPendingNotFound) {
		t.Errorf("second Consume: expected ErrPendingNotFound, got %v", err)
	}
}

func TestPendingAuth_Consume_Unknown(t *testing.T) {
	s, _ := newPendingService(t)
	_, err := s.Consume(context.Background(), "no-such-id")
	if !errors.Is(err, ErrPendingNotFound) {
		t.Errorf("expected ErrPendingNotFound, got %v", err)
	}
}

func TestPendingAuth_Consume_Expired(t *testing.T) {
	s, clock := newPendingService(t)
	ctx := context.Background()

	id, _ := s.Store(ctx, sampleStoreInput())
	*clock = clock.Add(PendingAuthorizationTTL + time.Second)

	_, err := s.Consume(ctx, id)
	if !errors.Is(err, ErrPendingNotFound) {
		t.Errorf("expected ErrPendingNotFound for expired, got %v", err)
	}
}

func TestPendingAuth_Sweep_RemovesExpired(t *testing.T) {
	s, clock := newPendingService(t)
	ctx := context.Background()

	_, _ = s.Store(ctx, sampleStoreInput())
	if s.Size() != 1 {
		t.Errorf("Size: got %d, want 1", s.Size())
	}

	*clock = clock.Add(PendingAuthorizationTTL + time.Second)
	s.sweep() // call directly; don't rely on goroutine timing in tests

	if s.Size() != 0 {
		t.Errorf("Size after sweep: got %d, want 0", s.Size())
	}
}

func TestPendingAuth_Store_DeterministicRandom(t *testing.T) {
	s, _ := newPendingService(t)
	// 16 bytes; tightly pinned to base64url encoding.
	s.setRandomForTest(bytes.NewReader([]byte("abcdefghijklmnop")))

	id, err := s.Store(context.Background(), sampleStoreInput())
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	want := "YWJjZGVmZ2hpamtsbW5vcA"
	if id != want {
		t.Errorf("id: got %q, want %q", id, want)
	}
}
