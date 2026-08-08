package logintransaction

import (
	"context"
	"sync"
)

// MemoryRepository is a concurrency-safe Phase 1 repository for unit tests and
// single-process development. It is not a production multi-instance store.
type MemoryRepository struct {
	mu      sync.RWMutex
	records map[string]Record
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{records: make(map[string]Record)}
}

func (r *MemoryRepository) Create(ctx context.Context, record Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, exists := r.records[record.ID]; exists {
		return ErrRecordExists
	}
	r.records[record.ID] = record
	return nil
}

func (r *MemoryRepository) Get(ctx context.Context, id string) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	record, ok := r.records[id]
	if !ok {
		return Record{}, ErrRecordNotFound
	}
	return record, nil
}

func (r *MemoryRepository) CompareAndSwap(
	ctx context.Context,
	id string,
	expectedVersion uint64,
	mutate Mutation,
) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}

	current, ok := r.records[id]
	if !ok {
		return Record{}, ErrRecordNotFound
	}
	if current.Version != expectedVersion {
		return Record{}, ErrVersionConflict
	}
	if mutate == nil {
		return Record{}, ErrInvariantViolation
	}

	next := current
	if err := mutate(&next); err != nil {
		return Record{}, err
	}
	if immutableRecordFieldsChanged(current, next) {
		return Record{}, ErrInvariantViolation
	}
	next.Version = current.Version + 1
	r.records[id] = next
	return next, nil
}

func immutableRecordFieldsChanged(before, after Record) bool {
	return before.ID != after.ID ||
		before.Request != after.Request ||
		before.SecretHash != after.SecretHash ||
		!before.CreatedAt.Equal(after.CreatedAt) ||
		!before.ExpiresAt.Equal(after.ExpiresAt)
}
