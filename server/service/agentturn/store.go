package agentturn

import (
	"context"
	"fmt"
	"time"

	agentv1 "server/contracts/agent/v1"
)

// Store is the persistence boundary for the durable Turn kernel.
//
// Admit, AppendEvent, Transition and RequestCancel are atomic operations.
// A SQL implementation should use a unique index on
// (principal_id, thread_id, idempotency_key), lock the Turn sequence row while
// appending, and commit each state mutation with its event in one transaction.
// It must also enforce unique (turn_id, sequence) and (turn_id, event_id)
// indexes. Replay must enforce Principal and Thread ownership before returning
// any event data.
//
// The `at` arguments on Transition and RequestCancel are validated caller
// intent, not storage authority. A durable implementation must stamp the
// persisted lifecycle columns from the same clock that produced created_at, so
// admission and later mutations cannot order backwards across processes with
// skewed wall clocks. SQLStore uses the database clock; the test-only
// MemoryStore has no storage clock and records the supplied timestamps
// directly.
//
// AppendEvent and Transition are legacy/reference unfenced
// operations. SQLStore rejects them after a Turn has entered a fenced epoch;
// a future production worker must use the companion ExecutionStore instead.
// Neither interface by itself supplies queueing, worker loops, reconciliation,
// effect dispatch or production composition/readiness.
type Store interface {
	Admit(ctx context.Context, candidate Turn, initial EventDraft) (AdmissionRecord, error)
	GetOwned(ctx context.Context, principalID PrincipalID, threadID agentv1.ThreadID, turnID agentv1.TurnID) (Turn, error)
	AppendEvent(ctx context.Context, turnID agentv1.TurnID, draft EventDraft) (agentv1.EventEnvelope, error)
	Transition(ctx context.Context, turnID agentv1.TurnID, to agentv1.TurnStatus, at time.Time, event EventDraft) (TransitionResult, error)
	RequestCancel(ctx context.Context, principalID PrincipalID, threadID agentv1.ThreadID, turnID agentv1.TurnID, at time.Time, event EventDraft) (CancelResult, error)
	Replay(ctx context.Context, principalID PrincipalID, threadID agentv1.ThreadID, turnID agentv1.TurnID, query ReplayQuery) (ReplayRecord, error)
}

// CanTransition is the server-authoritative state machine. Same-state writes
// are idempotent. Stopped additionally requires a recorded cancellation
// intent, which Store enforces atomically.
func CanTransition(from, to agentv1.TurnStatus) bool {
	if !from.Valid() || !to.Valid() {
		return false
	}
	if from == to {
		return true
	}
	if from.Terminal() {
		return false
	}
	switch from {
	case agentv1.TurnStatusQueued:
		switch to {
		case agentv1.TurnStatusRunning,
			agentv1.TurnStatusStopped,
			agentv1.TurnStatusFailed,
			agentv1.TurnStatusTimeout:
			return true
		}
	case agentv1.TurnStatusRunning:
		return to.Terminal()
	}
	return false
}

func transitionError(from, to agentv1.TurnStatus) error {
	if from.Terminal() && from != to {
		return fmt.Errorf("%w: cannot move from %q to %q", ErrTurnTerminal, from, to)
	}
	return fmt.Errorf("%w: cannot move from %q to %q", ErrInvalidTransition, from, to)
}
