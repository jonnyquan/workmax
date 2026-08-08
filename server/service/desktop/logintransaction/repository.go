package logintransaction

import (
	"context"
	"errors"
	"time"
)

var (
	ErrRecordNotFound     = errors.New("desktop login transaction repository: record not found")
	ErrRecordExists       = errors.New("desktop login transaction repository: record already exists")
	ErrVersionConflict    = errors.New("desktop login transaction repository: version conflict")
	ErrInvariantViolation = errors.New("desktop login transaction repository: immutable field changed")
)

// Record is the persistence model shared with repository implementations.
// SecretHash, GoogleStateHash, and ExchangeTokenHash contain SHA-256 digests,
// never the corresponding bearer values. Request.OAuthState and
// GoogleCodeVerifier must be recoverable but protected at rest by a shared
// repository implementation; the in-memory Phase 1 repository keeps them
// process-local.
type Record struct {
	ID      string
	Version uint64
	State   State
	Request CreateInput

	SecretHash         [32]byte
	GoogleStateHash    [32]byte
	GoogleCodeVerifier string
	ExchangeTokenHash  [32]byte

	UserID     uint
	AuthMethod AuthMethod
	// PasswordFailures is intentionally not exposed through Snapshot or the
	// HTTP status response; it is persisted only for admission control.
	PasswordFailures    uint16
	LastPasswordFailure time.Time

	CreatedAt       time.Time
	UpdatedAt       time.Time
	ExpiresAt       time.Time
	AuthenticatedAt time.Time
	ExchangedAt     time.Time
}

// Mutation runs while the repository owns the compare-and-swap critical
// section. Returning an error aborts the update.
type Mutation func(record *Record) error

// Repository is the minimum persistence contract needed by the state machine.
// CompareAndSwap must atomically verify expectedVersion, run Mutation against a
// copy, preserve immutable fields, increment Version exactly once, and commit.
type Repository interface {
	Create(ctx context.Context, record Record) error
	Get(ctx context.Context, id string) (Record, error)
	CompareAndSwap(ctx context.Context, id string, expectedVersion uint64, mutate Mutation) (Record, error)
}
