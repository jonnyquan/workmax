package agentturn

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "server/contracts/agent/v1"
)

type admissionBlockingCall struct {
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int64
}

func newAdmissionBlockingCall() admissionBlockingCall {
	return admissionBlockingCall{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
}

func (call *admissionBlockingCall) run(ctx context.Context) error {
	call.calls.Add(1)
	select {
	case call.entered <- struct{}{}:
	default:
	}
	select {
	case <-call.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type admissionTestStore struct {
	claim     admissionBlockingCall
	reconcile admissionBlockingCall
	effects   admissionBlockingCall
}

func newAdmissionTestStore() *admissionTestStore {
	return &admissionTestStore{
		claim:     newAdmissionBlockingCall(),
		reconcile: newAdmissionBlockingCall(),
		effects:   newAdmissionBlockingCall(),
	}
}

func (store *admissionTestStore) ClaimNext(ctx context.Context, _ ClaimNextCommand) (ClaimAttemptResult, error) {
	if err := store.claim.run(ctx); err != nil {
		return ClaimAttemptResult{}, err
	}
	return ClaimAttemptResult{}, ErrNoClaimableTurn
}

func (*admissionTestStore) ClaimAttempt(context.Context, ClaimAttemptCommand) (ClaimAttemptResult, error) {
	return ClaimAttemptResult{}, errors.New("unexpected ClaimAttempt")
}

func (*admissionTestStore) HeartbeatAttempt(context.Context, HeartbeatAttemptCommand) (HeartbeatAttemptResult, error) {
	return HeartbeatAttemptResult{}, errors.New("unexpected HeartbeatAttempt")
}

func (*admissionTestStore) CommitAttempt(context.Context, CommitAttemptCommand) (CommitAttemptResult, error) {
	return CommitAttemptResult{}, errors.New("unexpected CommitAttempt")
}

func (*admissionTestStore) ListReclaimableTurns(context.Context, ReclaimQuery) ([]ReclaimableTurn, error) {
	return []ReclaimableTurn{{
		TurnID: "turn_admission", Status: agentv1.TurnStatusQueued,
		Reason: ReclaimReasonAttemptsExhausted,
	}}, nil
}

func (store *admissionTestStore) ReconcileTerminal(ctx context.Context, _ ReconcileCommand) (ReconcileResult, error) {
	if err := store.reconcile.run(ctx); err != nil {
		return ReconcileResult{}, err
	}
	return ReconcileResult{}, nil
}

func (store *admissionTestStore) ClaimEffects(ctx context.Context, _ ClaimEffectsCommand) ([]EffectDelivery, error) {
	if err := store.effects.run(ctx); err != nil {
		return nil, err
	}
	return nil, ErrNoClaimableEffects
}

func (*admissionTestStore) CompleteEffect(context.Context, CompleteEffectCommand) (CompleteEffectResult, error) {
	return CompleteEffectResult{}, errors.New("unexpected CompleteEffect")
}

func admissionTestExecutor(context.Context, ExecutionSession) (agentv1.TurnStatus, error) {
	return agentv1.TurnStatusCompleted, nil
}

func newAdmissionWorker(t *testing.T, store ExecutionStore, gate *AdmissionGate) *Worker {
	t.Helper()
	worker, err := NewWorker(store, testExecutor{run: admissionTestExecutor}, WorkerOptions{
		WorkerID:          "worker_admission",
		WorkerBuildDigest: "sha256:worker-admission",
		AdmissionGate:     gate,
		IdleBackoff:       time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func newAdmissionReconciler(t *testing.T, store *admissionTestStore, gate *AdmissionGate) *Reconciler {
	t.Helper()
	reconciler, err := NewReconciler(store, store, ReconcilerOptions{
		ReconcilerID:          "reconciler_admission",
		ReconcilerBuildDigest: "sha256:reconciler-admission",
		AdmissionGate:         gate,
		Interval:              time.Millisecond,
		JitterFraction:        0,
	})
	if err != nil {
		t.Fatal(err)
	}
	return reconciler
}

func newAdmissionDispatcher(t *testing.T, store EffectOutboxStore, gate *AdmissionGate) *EffectDispatcher {
	t.Helper()
	dispatcher, err := NewEffectDispatcher(store, &testDeliverer{}, EffectDispatcherOptions{
		LeaseOwnerID:    "dispatcher_admission",
		AdmissionGate:   gate,
		IdleBackoff:     time.Millisecond,
		DeliveryTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

func waitAdmissionEntry(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("protected store call did not start")
	}
}

func closeAdmissionWithoutWaiting(t *testing.T, gate *AdmissionGate) {
	t.Helper()
	closed := make(chan struct{})
	go func() {
		gate.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("AdmissionGate.Close waited for in-flight work")
	}
}

func TestAdmissionGateCloseLinearizesComponentStoreEntry(t *testing.T) {
	t.Run("worker ClaimNext", func(t *testing.T) {
		gate := NewAdmissionGate()
		store := newAdmissionTestStore()
		worker := newAdmissionWorker(t, store, gate)

		first := make(chan error, 1)
		go func() {
			_, err := worker.RunOnce(context.Background())
			first <- err
		}()
		waitAdmissionEntry(t, store.claim.entered)
		closeAdmissionWithoutWaiting(t, gate)
		close(store.claim.release)
		if err := <-first; !errors.Is(err, ErrNoClaimableTurn) {
			t.Fatalf("in-flight RunOnce() error = %v, want ErrNoClaimableTurn", err)
		}

		if _, err := worker.RunOnce(context.Background()); !errors.Is(err, ErrAdmissionClosed) {
			t.Fatalf("post-close RunOnce() error = %v, want ErrAdmissionClosed", err)
		}
		if got := store.claim.calls.Load(); got != 1 {
			t.Fatalf("ClaimNext calls = %d, want only the pre-close in-flight call", got)
		}
	})

	t.Run("reconciler mutation", func(t *testing.T) {
		gate := NewAdmissionGate()
		store := newAdmissionTestStore()
		reconciler := newAdmissionReconciler(t, store, gate)

		first := make(chan error, 1)
		go func() {
			_, err := reconciler.ReconcileOnce(context.Background())
			first <- err
		}()
		waitAdmissionEntry(t, store.reconcile.entered)
		closeAdmissionWithoutWaiting(t, gate)
		close(store.reconcile.release)
		if err := <-first; err != nil {
			t.Fatalf("in-flight ReconcileOnce() error = %v", err)
		}

		if _, err := reconciler.ReconcileOnce(context.Background()); !errors.Is(err, ErrAdmissionClosed) {
			t.Fatalf("post-close ReconcileOnce() error = %v, want ErrAdmissionClosed", err)
		}
		if got := store.reconcile.calls.Load(); got != 1 {
			t.Fatalf("ReconcileTerminal calls = %d, want only the pre-close in-flight call", got)
		}
	})

	t.Run("dispatcher ClaimEffects", func(t *testing.T) {
		gate := NewAdmissionGate()
		store := newAdmissionTestStore()
		dispatcher := newAdmissionDispatcher(t, store, gate)

		first := make(chan error, 1)
		go func() {
			_, err := dispatcher.DispatchOnce(context.Background())
			first <- err
		}()
		waitAdmissionEntry(t, store.effects.entered)
		closeAdmissionWithoutWaiting(t, gate)
		close(store.effects.release)
		if err := <-first; !errors.Is(err, ErrNoClaimableEffects) {
			t.Fatalf("in-flight DispatchOnce() error = %v, want ErrNoClaimableEffects", err)
		}

		if _, err := dispatcher.DispatchOnce(context.Background()); !errors.Is(err, ErrAdmissionClosed) {
			t.Fatalf("post-close DispatchOnce() error = %v, want ErrAdmissionClosed", err)
		}
		if got := store.effects.calls.Load(); got != 1 {
			t.Fatalf("ClaimEffects calls = %d, want only the pre-close in-flight call", got)
		}
	})
}

func TestAdmissionGateIsOneWayIdempotentAndRaceSafe(t *testing.T) {
	gate := NewAdmissionGate()
	if !gate.Open() {
		t.Fatal("new AdmissionGate is not open")
	}
	var absent *AdmissionGate
	if absent.Open() {
		t.Fatal("nil AdmissionGate reported open")
	}
	const contenders = 128
	start := make(chan struct{})
	var ready sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(contenders)
	done.Add(contenders)
	for index := 0; index < contenders; index++ {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			_ = gate.Acquire() // Either side of Close is a valid linearization.
		}()
	}
	ready.Wait()
	close(start)
	gate.Close()
	gate.Close()
	done.Wait()
	if gate.Open() {
		t.Fatal("closed AdmissionGate reopened")
	}

	for index := 0; index < contenders; index++ {
		if err := gate.Acquire(); !errors.Is(err, ErrAdmissionClosed) {
			t.Fatalf("post-close Acquire(%d) error = %v, want stable ErrAdmissionClosed", index, err)
		}
	}
}

func TestComponentsCaptureExactSharedAdmissionGateIdentity(t *testing.T) {
	store := newAdmissionTestStore()
	shared := NewAdmissionGate()
	other := NewAdmissionGate()

	workerOptions := WorkerOptions{
		WorkerID: "worker_binding", WorkerBuildDigest: "sha256:worker-binding",
		AdmissionGate: shared,
	}
	worker, err := NewWorker(store, testExecutor{run: admissionTestExecutor}, workerOptions)
	if err != nil {
		t.Fatal(err)
	}
	workerOptions.AdmissionGate = other

	reconcilerOptions := ReconcilerOptions{
		ReconcilerID: "reconciler_binding", ReconcilerBuildDigest: "sha256:reconciler-binding",
		AdmissionGate: shared,
	}
	reconciler, err := NewReconciler(store, store, reconcilerOptions)
	if err != nil {
		t.Fatal(err)
	}
	reconcilerOptions.AdmissionGate = other

	dispatcherOptions := EffectDispatcherOptions{LeaseOwnerID: "dispatcher_binding", AdmissionGate: shared}
	dispatcher, err := NewEffectDispatcher(store, &testDeliverer{}, dispatcherOptions)
	if err != nil {
		t.Fatal(err)
	}
	dispatcherOptions.AdmissionGate = other

	if !worker.MatchesAdmissionGate(shared) || !reconciler.MatchesAdmissionGate(shared) ||
		!dispatcher.MatchesAdmissionGate(shared) {
		t.Fatal("components did not retain the exact shared AdmissionGate identity")
	}
	if worker.MatchesAdmissionGate(other) || reconciler.MatchesAdmissionGate(other) ||
		dispatcher.MatchesAdmissionGate(other) {
		t.Fatal("component authority changed when caller-owned options were mutated")
	}
	if worker.MatchesAdmissionGate(nil) || reconciler.MatchesAdmissionGate(nil) ||
		dispatcher.MatchesAdmissionGate(nil) {
		t.Fatal("sealed components matched the nil legacy gate")
	}
}

func TestClosedAdmissionRunLoopsWaitForContextCancellation(t *testing.T) {
	tests := map[string]func(context.Context, func(), *atomic.Int64) error{
		"worker": func(ctx context.Context, pulse func(), observed *atomic.Int64) error {
			gate := NewAdmissionGate()
			gate.Close()
			return newAdmissionWorker(t, newAdmissionTestStore(), gate).RunWithPulse(ctx,
				func(WorkerRunResult, error) { observed.Add(1) }, pulse)
		},
		"reconciler": func(ctx context.Context, pulse func(), observed *atomic.Int64) error {
			gate := NewAdmissionGate()
			gate.Close()
			store := newAdmissionTestStore()
			return newAdmissionReconciler(t, store, gate).RunWithPulse(ctx,
				func(ReconcileReport, error) { observed.Add(1) }, pulse)
		},
		"dispatcher": func(ctx context.Context, pulse func(), observed *atomic.Int64) error {
			gate := NewAdmissionGate()
			gate.Close()
			return newAdmissionDispatcher(t, newAdmissionTestStore(), gate).RunWithPulse(ctx,
				func(EffectDispatchReport, error) { observed.Add(1) }, pulse)
		},
	}

	for name, run := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			pulsed := make(chan struct{}, 1)
			done := make(chan error, 1)
			var observed atomic.Int64
			go func() {
				done <- run(ctx, func() {
					select {
					case pulsed <- struct{}{}:
					default:
					}
				}, &observed)
			}()
			waitAdmissionEntry(t, pulsed)

			select {
			case err := <-done:
				t.Fatalf("closed-admission loop returned before context cancellation: %v", err)
			case <-time.After(25 * time.Millisecond):
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("Run error = %v, want context.Canceled", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("closed-admission loop did not exit after context cancellation")
			}
			if got := observed.Load(); got != 0 {
				t.Fatalf("closed admission was reported to observer %d times as a pass failure", got)
			}
		})
	}
}
