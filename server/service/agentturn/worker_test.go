package agentturn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	agentv1 "server/contracts/agent/v1"
)

type testExecutor struct {
	run func(ctx context.Context, session ExecutionSession) (agentv1.TurnStatus, error)
}

func (executor testExecutor) Execute(ctx context.Context, session ExecutionSession) (agentv1.TurnStatus, error) {
	return executor.run(ctx, session)
}

func newTestWorker(t *testing.T, store ExecutionStore, executor TurnExecutor) *Worker {
	t.Helper()
	worker, err := NewWorker(store, executor, WorkerOptions{
		WorkerID:          "worker_runtime_test",
		WorkerBuildDigest: "sha256:worker-runtime-test",
		HeartbeatInterval: 5 * time.Millisecond,
		IdleBackoff:       time.Millisecond,
		DrainTimeout:      5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func testOperation(id string) OperationDraft {
	return OperationDraft{
		OperationID: id,
		Event: EventDraft{
			Type: "writer.document.delta",
			Data: json.RawMessage(fmt.Sprintf(`{"op":%q}`, id)),
		},
	}
}

// blockUntilDone waits for the worker to cancel the executor, with a guard so a
// broken heartbeat fails the test instead of hanging it.
func blockUntilDone(t *testing.T, ctx context.Context) error {
	t.Helper()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(10 * time.Second):
		t.Fatal("executor context was never cancelled")
		return nil
	}
}

func TestWorkerRunOnceDrivesTurnToCommittedTerminal(t *testing.T) {
	db, store, _, turns := newSQLClaimNextFixture(t, "worker_happy")
	turn := turns[0]

	worker := newTestWorker(t, store, testExecutor{
		run: func(_ context.Context, session ExecutionSession) (agentv1.TurnStatus, error) {
			if session.Turn().ID != turn.ID {
				return "", fmt.Errorf("session bound to %q", session.Turn().ID)
			}
			for index := 0; index < 2; index++ {
				if _, err := session.Emit(context.Background(), testOperation(fmt.Sprintf("operation_%d", index))); err != nil {
					return "", err
				}
			}
			return agentv1.TurnStatusCompleted, nil
		},
	})

	result, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce(): %v", err)
	}
	if !result.Committed || result.TerminalStatus != agentv1.TurnStatusCompleted || result.OperationsEmit != 2 {
		t.Fatalf("result = %+v, want a committed completed Turn with 2 emitted operations", result)
	}
	if result.Cancelled || result.FenceLost || result.ExecutorErr != nil {
		t.Fatalf("clean run reported trouble: %+v", result)
	}

	var state executionTestTurnState
	executionTakeTurnState(t, db, turn.ID, &state)
	if state.Status != string(agentv1.TurnStatusCompleted) || state.ActiveAttemptID != nil {
		t.Fatalf("turn state = %+v", state)
	}
	// Two emitted operations plus the terminal commit, each with its own receipt.
	if got := executionTableCount(t, db, SQLTurnOperationTable, "turn_id = ?", turn.ID); got != 3 {
		t.Fatalf("operation receipts = %d, want 3", got)
	}
	// A closed session must not let a stray goroutine append to a finished Turn.
	if _, err := worker.RunOnce(context.Background()); !errors.Is(err, ErrNoClaimableTurn) {
		t.Fatalf("second RunOnce() error = %v, want ErrNoClaimableTurn", err)
	}
}

func TestWorkerCommitsFailedWhenExecutorReturnsError(t *testing.T) {
	db, store, _, turns := newSQLClaimNextFixture(t, "worker_failed")
	turn := turns[0]
	executorErr := errors.New("domain runtime exploded")

	worker := newTestWorker(t, store, testExecutor{
		run: func(context.Context, ExecutionSession) (agentv1.TurnStatus, error) {
			return "", executorErr
		},
	})
	result, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce() error = %v, want a committed failure rather than a worker error", err)
	}
	if !result.Committed || result.TerminalStatus != agentv1.TurnStatusFailed || !errors.Is(result.ExecutorErr, executorErr) {
		t.Fatalf("result = %+v, want a committed failed Turn carrying the executor error", result)
	}
	var state executionTestTurnState
	executionTakeTurnState(t, db, turn.ID, &state)
	if state.Status != string(agentv1.TurnStatusFailed) {
		t.Fatalf("turn state = %+v", state)
	}
}

func TestWorkerRejectsExecutorTerminalItMayNotReport(t *testing.T) {
	_, store, _, _ := newSQLClaimNextFixture(t, "worker_bad_status", "worker_bad_stopped")

	running := newTestWorker(t, store, testExecutor{
		run: func(context.Context, ExecutionSession) (agentv1.TurnStatus, error) {
			return agentv1.TurnStatusRunning, nil
		},
	})
	result, err := running.RunOnce(context.Background())
	if !errors.Is(err, ErrExecutorNonTerminal) {
		t.Fatalf("non-terminal RunOnce() error = %v, want ErrExecutorNonTerminal", err)
	}
	if !result.Committed || result.TerminalStatus != agentv1.TurnStatusFailed {
		t.Fatalf("non-terminal result = %+v, want the Turn still committed as failed", result)
	}

	// `stopped` belongs to a recorded cancellation, not to executor choice.
	stopping := newTestWorker(t, store, testExecutor{
		run: func(context.Context, ExecutionSession) (agentv1.TurnStatus, error) {
			return agentv1.TurnStatusStopped, nil
		},
	})
	stoppedResult, err := stopping.RunOnce(context.Background())
	if !errors.Is(err, ErrExecutorNonTerminal) {
		t.Fatalf("uncancelled stopped RunOnce() error = %v, want ErrExecutorNonTerminal", err)
	}
	if stoppedResult.TerminalStatus != agentv1.TurnStatusFailed {
		t.Fatalf("uncancelled stopped result = %+v, want failed", stoppedResult)
	}
}

func TestWorkerObservesCancellationThroughHeartbeatAndStops(t *testing.T) {
	db, store, clock, turns := newSQLClaimNextFixture(t, "worker_cancel")
	turn := turns[0]

	worker := newTestWorker(t, store, testExecutor{
		run: func(ctx context.Context, session ExecutionSession) (agentv1.TurnStatus, error) {
			if session.CancellationRequested() {
				return "", fmt.Errorf("cancellation observed before it was requested")
			}
			if _, err := store.RequestCancel(context.Background(), turn.PrincipalID, turn.ThreadID, turn.ID,
				clock.Get(), sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"cancellationRequested":true}`)); err != nil {
				return "", err
			}
			// A blocking executor must still be stopped by the heartbeat.
			return "", blockUntilDone(t, ctx)
		},
	})

	result, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce(): %v", err)
	}
	if !result.Cancelled || !result.Committed || result.TerminalStatus != agentv1.TurnStatusStopped {
		t.Fatalf("result = %+v, want a committed stopped Turn", result)
	}
	var state executionTestTurnState
	executionTakeTurnState(t, db, turn.ID, &state)
	if state.Status != string(agentv1.TurnStatusStopped) || state.ActiveAttemptID != nil {
		t.Fatalf("cancelled turn state = %+v", state)
	}
}

func TestWorkerAbandonsTurnWithoutCommittingWhenFenceIsLost(t *testing.T) {
	db, store, clock, turns := newSQLClaimNextFixture(t, "worker_fenced")
	turn := turns[0]

	var superseding ClaimAttemptResult
	worker := newTestWorker(t, store, testExecutor{
		run: func(ctx context.Context, session ExecutionSession) (agentv1.TurnStatus, error) {
			// Simulate a partition: this epoch's lease lapses and another
			// worker takes the Turn while execution is still running.
			clock.Set(session.Attempt().LeaseExpiresAt)
			claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_superseding"))
			if err != nil {
				return "", err
			}
			superseding = claimed
			return "", blockUntilDone(t, ctx)
		},
	})

	result, err := worker.RunOnce(context.Background())
	if !errors.Is(err, ErrWorkerFenceLost) {
		t.Fatalf("RunOnce() error = %v, want ErrWorkerFenceLost", err)
	}
	if result.Committed || !result.FenceLost {
		t.Fatalf("result = %+v, want an uncommitted fence-lost outcome", result)
	}
	// The superseding epoch must still own the Turn: a fenced worker that
	// wrote a terminal state here would have destroyed live work.
	var state executionTestTurnState
	executionTakeTurnState(t, db, turn.ID, &state)
	if state.Status != string(agentv1.TurnStatusRunning) ||
		state.ActiveAttemptID == nil || *state.ActiveAttemptID != superseding.Attempt.ID {
		t.Fatalf("fenced worker disturbed the live epoch: %+v", state)
	}
	if got := executionTableCount(t, db, SQLTurnOperationTable, "turn_id = ?", turn.ID); got != 0 {
		t.Fatalf("fenced worker wrote %d operation receipts, want 0", got)
	}
}

func TestWorkerEmitIsRefusedAfterTheEpochEnds(t *testing.T) {
	_, store, _, _ := newSQLClaimNextFixture(t, "worker_late_emit")

	var leaked ExecutionSession
	worker := newTestWorker(t, store, testExecutor{
		run: func(_ context.Context, session ExecutionSession) (agentv1.TurnStatus, error) {
			leaked = session
			return agentv1.TurnStatusCompleted, nil
		},
	})
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := leaked.Emit(context.Background(), testOperation("operation_late")); err == nil {
		t.Fatal("a closed session accepted a late Emit")
	}
}

func TestWorkerRunProcessesQueueThenDrains(t *testing.T) {
	const queued = 3
	suffixes := make([]string, 0, queued)
	for index := 0; index < queued; index++ {
		suffixes = append(suffixes, fmt.Sprintf("worker_loop_%02d", index))
	}
	_, store, _, turns := newSQLClaimNextFixture(t, suffixes...)

	worker := newTestWorker(t, store, testExecutor{
		run: func(context.Context, ExecutionSession) (agentv1.TurnStatus, error) {
			return agentv1.TurnStatusCompleted, nil
		},
	})

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	var mu sync.Mutex
	done := make(chan struct{})
	completed := map[agentv1.TurnID]bool{}
	runErr := make(chan error, 1)
	go func() {
		runErr <- worker.Run(ctx, func(result WorkerRunResult, err error) {
			if err != nil || !result.Committed {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			completed[result.TurnID] = true
			if len(completed) == queued {
				select {
				case <-done:
				default:
					close(done)
				}
			}
		})
	}()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("worker did not drain the queue")
	}
	stop()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, turn := range turns {
		if !completed[turn.ID] {
			t.Fatalf("turn %q was never executed", turn.ID)
		}
	}
}

func TestNewWorkerRejectsUnsafeConfiguration(t *testing.T) {
	_, store, _, _ := newSQLClaimNextFixture(t, "worker_config")
	executor := testExecutor{run: func(context.Context, ExecutionSession) (agentv1.TurnStatus, error) {
		return agentv1.TurnStatusCompleted, nil
	}}
	base := WorkerOptions{WorkerID: "w", WorkerBuildDigest: "sha256:w"}

	if _, err := NewWorker(nil, executor, base); err == nil {
		t.Fatal("nil store accepted")
	}
	if _, err := NewWorker(store, nil, base); err == nil {
		t.Fatal("nil executor accepted")
	}
	for name, mutate := range map[string]func(*WorkerOptions){
		"missing worker id": func(o *WorkerOptions) { o.WorkerID = "" },
		"missing digest":    func(o *WorkerOptions) { o.WorkerBuildDigest = "" },
		"oversized scan":    func(o *WorkerOptions) { o.ScanLimit = MaxClaimNextScanLimit + 1 },
		"idle backoff too long": func(o *WorkerOptions) {
			o.IdleBackoff = MaxWorkerIdleBackoff + time.Second
		},
		// A heartbeat that cannot beat inside the lease bound loses every epoch.
		"heartbeat beyond lease bound": func(o *WorkerOptions) {
			o.HeartbeatInterval = MaxAttemptLeaseTTL
		},
		"drain too long": func(o *WorkerOptions) { o.DrainTimeout = MaxWorkerDrainTimeout + time.Second },
	} {
		options := base
		mutate(&options)
		if _, err := NewWorker(store, executor, options); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}

	// Defaults must produce a usable worker.
	worker, err := NewWorker(store, executor, base)
	if err != nil {
		t.Fatal(err)
	}
	if worker.options.HeartbeatInterval >= DefaultAttemptLeaseTTL {
		t.Fatalf("default heartbeat %s does not fit inside the lease TTL %s",
			worker.options.HeartbeatInterval, DefaultAttemptLeaseTTL)
	}
}
