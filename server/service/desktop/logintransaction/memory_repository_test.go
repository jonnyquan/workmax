package logintransaction

import (
	"context"
	"errors"
	"testing"
)

func TestMemoryRepositoryCompareAndSwap(t *testing.T) {
	repository := NewMemoryRepository()
	record := Record{
		ID:         "transaction-id",
		Version:    1,
		State:      StatePending,
		Request:    validCreateInput(),
		SecretHash: hashSecret("transaction-secret"),
		CreatedAt:  testStartTime,
		UpdatedAt:  testStartTime,
		ExpiresAt:  testStartTime.Add(DefaultTTL),
	}
	if err := repository.Create(context.Background(), record); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := repository.Create(context.Background(), record); !errors.Is(err, ErrRecordExists) {
		t.Fatalf("duplicate Create() error = %v, want ErrRecordExists", err)
	}

	updated, err := repository.CompareAndSwap(context.Background(), record.ID, 1, func(next *Record) error {
		next.State = StatePasswordAuthenticating
		return nil
	})
	if err != nil {
		t.Fatalf("CompareAndSwap() error = %v", err)
	}
	if updated.Version != 2 || updated.State != StatePasswordAuthenticating {
		t.Fatalf("CompareAndSwap() = %+v", updated)
	}

	mutationRan := false
	_, err = repository.CompareAndSwap(context.Background(), record.ID, 1, func(*Record) error {
		mutationRan = true
		return nil
	})
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale CompareAndSwap() error = %v, want ErrVersionConflict", err)
	}
	if mutationRan {
		t.Fatal("mutation ran for a stale expected version")
	}
}

func TestMemoryRepositoryRejectsImmutableMutation(t *testing.T) {
	repository := NewMemoryRepository()
	record := Record{
		ID:         "transaction-id",
		Version:    1,
		State:      StatePending,
		Request:    validCreateInput(),
		SecretHash: hashSecret("transaction-secret"),
		CreatedAt:  testStartTime,
		UpdatedAt:  testStartTime,
		ExpiresAt:  testStartTime.Add(DefaultTTL),
	}
	if err := repository.Create(context.Background(), record); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	_, err := repository.CompareAndSwap(context.Background(), record.ID, record.Version, func(next *Record) error {
		next.Request.Scope = "different"
		return nil
	})
	if !errors.Is(err, ErrInvariantViolation) {
		t.Fatalf("immutable CompareAndSwap() error = %v, want ErrInvariantViolation", err)
	}
	got, err := repository.Get(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got != record {
		t.Fatalf("record changed after rejected mutation: got %+v, want %+v", got, record)
	}
}

func TestMemoryRepositoryHonorsContextCancellation(t *testing.T) {
	repository := NewMemoryRepository()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := repository.Create(ctx, Record{ID: "transaction-id"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Create() error = %v, want context.Canceled", err)
	}
	if _, err := repository.Get(ctx, "transaction-id"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Get() error = %v, want context.Canceled", err)
	}
	if _, err := repository.CompareAndSwap(ctx, "transaction-id", 1, func(*Record) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("CompareAndSwap() error = %v, want context.Canceled", err)
	}
}
