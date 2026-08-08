package agentturn

import (
	"context"
	"errors"
	"sync/atomic"
)

// ErrAdmissionClosed is returned when a runtime component tries to start a
// new authority-bearing store call after its shared AdmissionGate closed.
var ErrAdmissionClosed = errors.New("agent runtime admission is closed")

// AdmissionGate is a shared, one-way admission boundary for a composed Agent
// runtime. A successful Acquire is the call's linearization point: that call
// is in flight and may finish even if Close happens immediately afterwards.
// An Acquire that observes Close may never enter its protected store call.
//
// Close is deliberately independent of in-flight work. It is idempotent,
// lock-free and never waits for a store, executor, provider, or drain loop.
// The runtime's contexts remain responsible for draining work that already
// acquired admission.
//
// Construct gates with NewAdmissionGate and pass the same pointer to Worker,
// Reconciler, and EffectDispatcher. A nil gate is accepted by those components
// only to preserve their legacy, not-production-wired candidate behaviour.
type AdmissionGate struct {
	closed atomic.Bool
}

func NewAdmissionGate() *AdmissionGate {
	return &AdmissionGate{}
}

// Open is a read-only startup/seal observation. Nil is never an open
// production gate. Runtime entry still calls Acquire: Open alone grants no
// authority and must not be used as a check-then-call substitute.
func (gate *AdmissionGate) Open() bool {
	return gate != nil && !gate.closed.Load()
}

// Acquire admits one authority-bearing call or returns ErrAdmissionClosed.
// It does not need a matching release: Close does not wait for in-flight work.
func (gate *AdmissionGate) Acquire() error {
	if gate == nil {
		// Nil is the explicit legacy candidate path. Production composition can
		// reject it by exact MatchesAdmissionGate checks.
		return nil
	}
	if gate.closed.Load() {
		return ErrAdmissionClosed
	}
	return nil
}

// Close permanently removes admission. It is safe to call concurrently and
// repeatedly.
func (gate *AdmissionGate) Close() {
	if gate != nil {
		gate.closed.Store(true)
	}
}

// waitForAdmissionShutdown keeps a closed gate from being reported as a loop
// failure or retried as a hot loop. The owner that closed admission cancels
// the runtime context to release each scheduler through its normal exit path.
func waitForAdmissionShutdown(ctx context.Context) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}
