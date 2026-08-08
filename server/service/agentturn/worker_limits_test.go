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

func newLimitedTestWorker(
	t *testing.T,
	store ExecutionStore,
	executor TurnExecutor,
	limits []PluginExecutionLimits,
	stopGrace time.Duration,
) *Worker {
	t.Helper()
	worker, err := NewWorker(store, executor, WorkerOptions{
		WorkerID:          "worker_limits_test",
		WorkerBuildDigest: "sha256:worker-limits-test",
		HeartbeatInterval: 5 * time.Millisecond,
		IdleBackoff:       time.Millisecond,
		DrainTimeout:      5 * time.Second,
		ExecutorStopGrace: stopGrace,
		PluginLimits:      limits,
	})
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func testPluginLimits(plugin agentv1.EventPluginRef, execution, progress time.Duration) []PluginExecutionLimits {
	return []PluginExecutionLimits{{
		Plugin: plugin, ExecutionTimeout: execution, ProgressTimeout: progress,
	}}
}

type observedWorkerStore struct {
	ExecutionStore
	heartbeats      atomic.Int64
	terminalCommits atomic.Int64
	heartbeat       chan struct{}
	commit          func(context.Context, CommitAttemptCommand) (CommitAttemptResult, error)
}

func (store *observedWorkerStore) HeartbeatAttempt(
	ctx context.Context,
	command HeartbeatAttemptCommand,
) (HeartbeatAttemptResult, error) {
	result, err := store.ExecutionStore.HeartbeatAttempt(ctx, command)
	if err == nil {
		store.heartbeats.Add(1)
		if store.heartbeat != nil {
			select {
			case store.heartbeat <- struct{}{}:
			default:
			}
		}
	}
	return result, err
}

func (store *observedWorkerStore) CommitAttempt(
	ctx context.Context,
	command CommitAttemptCommand,
) (CommitAttemptResult, error) {
	if command.TerminalStatus != "" {
		store.terminalCommits.Add(1)
	}
	if store.commit != nil {
		return store.commit(ctx, command)
	}
	return store.ExecutionStore.CommitAttempt(ctx, command)
}

func TestWorkerPluginExecutionLimitsAreExactValidatedAndOwned(t *testing.T) {
	_, store, _, turns := newSQLClaimNextFixture(t, "limits_config")
	executor := testExecutor{run: func(context.Context, ExecutionSession) (agentv1.TurnStatus, error) {
		return agentv1.TurnStatusCompleted, nil
	}}
	want := testPluginLimits(turns[0].Plugin, time.Minute, 10*time.Second)
	provided := append([]PluginExecutionLimits(nil), want...)
	worker := newLimitedTestWorker(t, store, executor, provided, time.Second)

	provided[0].ExecutionTimeout = 2 * time.Minute
	if !worker.MatchesPluginExecutionLimits(want) {
		t.Fatal("worker did not retain an exact defensive copy of Plugin limits")
	}
	if worker.MatchesPluginExecutionLimits(provided) {
		t.Fatal("caller mutation changed the Worker's installed Plugin limits")
	}
	if worker.MatchesPluginExecutionLimits(nil) {
		t.Fatal("bounded Worker matched an empty policy")
	}

	base := WorkerOptions{WorkerID: "w", WorkerBuildDigest: "sha256:w"}
	for name, limits := range map[string][]PluginExecutionLimits{
		"zero execution": {{Plugin: turns[0].Plugin, ProgressTimeout: time.Second}},
		"zero progress":  {{Plugin: turns[0].Plugin, ExecutionTimeout: time.Second}},
		"progress beyond execution": {{
			Plugin: turns[0].Plugin, ExecutionTimeout: time.Second, ProgressTimeout: 2 * time.Second,
		}},
		"execution beyond maximum": {{
			Plugin: turns[0].Plugin, ExecutionTimeout: MaxWorkerExecutionTimeout + time.Nanosecond,
			ProgressTimeout: time.Second,
		}},
		"duplicate release": {want[0], want[0]},
	} {
		options := base
		options.PluginLimits = limits
		if _, err := NewWorker(store, executor, options); err == nil {
			t.Fatalf("%s: invalid Plugin limits accepted", name)
		}
	}
	negativeGrace := base
	negativeGrace.ExecutorStopGrace = -time.Nanosecond
	if _, err := NewWorker(store, executor, negativeGrace); err == nil {
		t.Fatal("negative executor stop grace accepted")
	}
}

func TestWorkerMissingExactPluginLimitsFailsClosedBeforeExecute(t *testing.T) {
	db, store, _, turns := newSQLClaimNextFixture(t, "limits_missing")
	wrong := turns[0].Plugin
	wrong.Version = "9.9.9"
	var executions atomic.Int64
	worker := newLimitedTestWorker(t, store, testExecutor{
		run: func(context.Context, ExecutionSession) (agentv1.TurnStatus, error) {
			executions.Add(1)
			return agentv1.TurnStatusCompleted, nil
		},
	}, testPluginLimits(wrong, time.Second, time.Second), 50*time.Millisecond)

	result, err := worker.RunOnce(context.Background())
	if !errors.Is(err, ErrWorkerPluginLimitsUnavailable) || !errors.Is(err, ErrWorkerRestartRequired) {
		t.Fatalf("RunOnce() error = %v, want unavailable + restart-required", err)
	}
	if executions.Load() != 0 || result.Committed || !result.RestartRequired {
		t.Fatalf("missing-policy result = %+v, executions = %d", result, executions.Load())
	}
	if got := executionTableCount(t, db, SQLTurnOperationTable, "turn_id = ?", turns[0].ID); got != 0 {
		t.Fatalf("missing policy wrote %d Operations, want 0", got)
	}
	if _, err := worker.RunOnce(context.Background()); !errors.Is(err, ErrWorkerRestartRequired) {
		t.Fatalf("poisoned Worker RunOnce() error = %v, want ErrWorkerRestartRequired", err)
	}
}

func TestWorkerHeartbeatsDoNotMaskProgressTimeout(t *testing.T) {
	_, base, _, turns := newSQLClaimNextFixture(t, "limits_progress_heartbeat")
	store := &observedWorkerStore{ExecutionStore: base}
	cause := make(chan error, 1)
	worker := newLimitedTestWorker(t, store, testExecutor{
		run: func(ctx context.Context, _ ExecutionSession) (agentv1.TurnStatus, error) {
			<-ctx.Done()
			cause <- context.Cause(ctx)
			return "", ctx.Err()
		},
	}, testPluginLimits(turns[0].Plugin, time.Second, 80*time.Millisecond), 250*time.Millisecond)

	result, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce(): %v", err)
	}
	if !result.Committed || result.TerminalStatus != agentv1.TurnStatusTimeout ||
		result.LimitExceeded != WorkerLimitProgressTimeout || result.RestartRequired {
		t.Fatalf("progress-timeout result = %+v", result)
	}
	if got := <-cause; !errors.Is(got, ErrWorkerProgressTimeout) {
		t.Fatalf("executor cancellation cause = %v, want ErrWorkerProgressTimeout", got)
	}
	if store.heartbeats.Load() < 2 {
		t.Fatalf("heartbeats = %d, want proof that lease liveness did not refresh progress", store.heartbeats.Load())
	}
}

func TestWorkerKeepsHeartbeatAliveThroughCooperativeStopGrace(t *testing.T) {
	_, base, _, turns := newSQLClaimNextFixture(t, "limits_heartbeat_grace")
	store := &observedWorkerStore{ExecutionStore: base, heartbeat: make(chan struct{}, 1)}
	cancelSeen := make(chan struct{})
	allowReturn := make(chan struct{})
	worker := newLimitedTestWorker(t, store, testExecutor{
		run: func(ctx context.Context, _ ExecutionSession) (agentv1.TurnStatus, error) {
			<-ctx.Done()
			close(cancelSeen)
			<-allowReturn
			return "", ctx.Err()
		},
	}, testPluginLimits(turns[0].Plugin, time.Second, 70*time.Millisecond), 500*time.Millisecond)

	type runOutcome struct {
		result WorkerRunResult
		err    error
	}
	done := make(chan runOutcome, 1)
	go func() {
		result, err := worker.RunOnce(context.Background())
		done <- runOutcome{result: result, err: err}
	}()
	select {
	case <-cancelSeen:
	case <-time.After(time.Second):
		t.Fatal("progress ceiling did not cancel executor")
	}
	before := store.heartbeats.Load()
	deadline := time.NewTimer(250 * time.Millisecond)
	defer deadline.Stop()
	for store.heartbeats.Load() <= before {
		select {
		case <-store.heartbeat:
		case <-deadline.C:
			t.Fatal("heartbeat stopped before the cooperative executor returned")
		}
	}
	close(allowReturn)
	got := <-done
	if got.err != nil || !got.result.Committed ||
		got.result.LimitExceeded != WorkerLimitProgressTimeout {
		t.Fatalf("RunOnce() = %+v, %v", got.result, got.err)
	}
}

func TestWorkerFreshDurableEmitsRefreshProgressUntilExecutionTimeout(t *testing.T) {
	_, store, _, turns := newSQLClaimNextFixture(t, "limits_execution")
	worker := newLimitedTestWorker(t, store, testExecutor{
		run: func(ctx context.Context, session ExecutionSession) (agentv1.TurnStatus, error) {
			ticker := time.NewTicker(30 * time.Millisecond)
			defer ticker.Stop()
			for index := 0; index < 5; index++ {
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case <-ticker.C:
					if _, err := session.Emit(context.Background(), testOperation("limit_execution_"+time.Now().Format("150405.000000000"))); err != nil {
						return "", err
					}
				}
			}
			// The last fresh durable operation keeps the progress deadline beyond
			// the execution ceiling. Stop issuing writes with a wide margin before
			// that ceiling so the terminal commit tests watchdog precedence rather
			// than cancellation of an in-flight SQLite transaction.
			<-ctx.Done()
			return "", ctx.Err()
		},
	}, testPluginLimits(turns[0].Plugin, 220*time.Millisecond, 100*time.Millisecond), 250*time.Millisecond)

	result, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce(): %v", err)
	}
	if !result.Committed || result.TerminalStatus != agentv1.TurnStatusTimeout ||
		result.LimitExceeded != WorkerLimitExecutionTimeout || result.ProgressCommits < 2 {
		t.Fatalf("execution-timeout result = %+v", result)
	}
}

func TestWorkerReplayEmitDoesNotRefreshProgress(t *testing.T) {
	_, store, _, turns := newSQLClaimNextFixture(t, "limits_replay")
	worker := newLimitedTestWorker(t, store, testExecutor{
		run: func(ctx context.Context, session ExecutionSession) (agentv1.TurnStatus, error) {
			operation := testOperation("limit_replay")
			if _, err := session.Emit(ctx, operation); err != nil {
				return "", err
			}
			// Several completed replay writes are enough to prove they neither
			// increment durable progress nor reset the watchdog. Stop issuing
			// storage calls before the deadline so cancellation cannot interrupt a
			// SQLite transaction and turn this semantic test into a driver test.
			for replay := 0; replay < 4; replay++ {
				if _, err := session.Emit(ctx, operation); err != nil {
					return "", err
				}
			}
			<-ctx.Done()
			return "", ctx.Err()
		},
	}, testPluginLimits(turns[0].Plugin, time.Second, 90*time.Millisecond), 250*time.Millisecond)

	result, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce(): %v", err)
	}
	if !result.Committed || result.LimitExceeded != WorkerLimitProgressTimeout ||
		result.ProgressCommits != 1 || result.OperationsEmit < 2 {
		t.Fatalf("replay-progress result = %+v", result)
	}
}

func TestWorkerEmitBackgroundContextIsBoundToEpochAndQuiescesBeforeTerminal(t *testing.T) {
	_, base, _, turns := newSQLClaimNextFixture(t, "limits_emit_epoch")
	entered := make(chan struct{})
	var enterOnce sync.Once
	var operationActive atomic.Bool
	var terminalOverlap atomic.Bool
	var operationCause atomic.Value
	store := &observedWorkerStore{ExecutionStore: base}
	store.commit = func(ctx context.Context, command CommitAttemptCommand) (CommitAttemptResult, error) {
		if command.TerminalStatus != "" {
			if operationActive.Load() {
				terminalOverlap.Store(true)
			}
			return base.CommitAttempt(ctx, command)
		}
		operationActive.Store(true)
		defer operationActive.Store(false)
		enterOnce.Do(func() { close(entered) })
		<-ctx.Done()
		operationCause.Store(context.Cause(ctx))
		return CommitAttemptResult{}, ctx.Err()
	}
	worker := newLimitedTestWorker(t, store, testExecutor{
		run: func(_ context.Context, session ExecutionSession) (agentv1.TurnStatus, error) {
			_, err := session.Emit(context.Background(), testOperation("limit_background"))
			return "", err
		},
	}, testPluginLimits(turns[0].Plugin, time.Second, 80*time.Millisecond), 250*time.Millisecond)

	result, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce(): %v", err)
	}
	select {
	case <-entered:
	default:
		t.Fatal("non-terminal CommitAttempt was never entered")
	}
	if !result.Committed || result.LimitExceeded != WorkerLimitProgressTimeout || result.ProgressCommits != 0 {
		t.Fatalf("blocked-Emit result = %+v", result)
	}
	if terminalOverlap.Load() {
		t.Fatal("terminal CommitAttempt overlapped an in-flight Emit")
	}
	if got, _ := operationCause.Load().(error); !errors.Is(got, ErrWorkerProgressTimeout) {
		t.Fatalf("Emit storage context cause = %v, want ErrWorkerProgressTimeout", got)
	}
}

func TestWorkerHardNonCooperativeTimeoutRequiresRestartWithoutTerminalCommit(t *testing.T) {
	db, base, _, turns := newSQLClaimNextFixture(t, "limits_hard_timeout")
	store := &observedWorkerStore{ExecutionStore: base}
	release := make(chan struct{})
	executorReturned := make(chan struct{})
	worker := newLimitedTestWorker(t, store, testExecutor{
		run: func(context.Context, ExecutionSession) (agentv1.TurnStatus, error) {
			defer close(executorReturned)
			<-release
			return agentv1.TurnStatusCompleted, nil
		},
	}, testPluginLimits(turns[0].Plugin, time.Second, 60*time.Millisecond), 40*time.Millisecond)

	started := time.Now()
	result, err := worker.RunOnce(context.Background())
	elapsed := time.Since(started)
	if !errors.Is(err, ErrWorkerRestartRequired) {
		t.Fatalf("RunOnce() error = %v, want ErrWorkerRestartRequired", err)
	}
	if result.Committed || !result.ExecutorDetached || !result.RestartRequired ||
		result.LimitExceeded != WorkerLimitProgressTimeout {
		t.Fatalf("hard-timeout result = %+v", result)
	}
	if elapsed > 750*time.Millisecond {
		t.Fatalf("hard timeout returned after %s, want bounded stop grace", elapsed)
	}
	if store.terminalCommits.Load() != 0 {
		t.Fatalf("hard timeout attempted %d terminal commits, want 0", store.terminalCommits.Load())
	}
	if got := executionTableCount(t, db, SQLTurnOperationTable, "turn_id = ?", turns[0].ID); got != 0 {
		t.Fatalf("hard timeout persisted %d Operations, want 0", got)
	}
	if _, err := worker.RunOnce(context.Background()); !errors.Is(err, ErrWorkerRestartRequired) {
		t.Fatalf("second RunOnce() error = %v, want poisoned Worker", err)
	}
	runCtx, stopRun := context.WithCancel(context.Background())
	if err := worker.Run(runCtx, nil); !errors.Is(err, ErrWorkerRestartRequired) {
		t.Fatalf("poisoned Worker Run() error = %v, want fatal ErrWorkerRestartRequired", err)
	}
	stopRun()
	close(release)
	select {
	case <-executorReturned:
	case <-time.After(time.Second):
		t.Fatal("released test executor did not return")
	}
}

func TestWorkerHardNonCooperativeEmitDoesNotBlockSealOrRaceTerminal(t *testing.T) {
	_, base, _, turns := newSQLClaimNextFixture(t, "limits_hard_emit")
	store := &observedWorkerStore{ExecutionStore: base}
	operationEntered := make(chan struct{})
	releaseOperation := make(chan struct{})
	var once sync.Once
	store.commit = func(ctx context.Context, command CommitAttemptCommand) (CommitAttemptResult, error) {
		if command.TerminalStatus != "" {
			return base.CommitAttempt(ctx, command)
		}
		once.Do(func() { close(operationEntered) })
		<-releaseOperation // Deliberately violates the Context contract.
		return CommitAttemptResult{}, context.Canceled
	}
	executorReturned := make(chan struct{})
	worker := newLimitedTestWorker(t, store, testExecutor{
		run: func(_ context.Context, session ExecutionSession) (agentv1.TurnStatus, error) {
			defer close(executorReturned)
			_, err := session.Emit(context.Background(), testOperation("limit_hard_emit"))
			return "", err
		},
	}, testPluginLimits(turns[0].Plugin, time.Second, 60*time.Millisecond), 40*time.Millisecond)

	started := time.Now()
	result, err := worker.RunOnce(context.Background())
	if !errors.Is(err, ErrWorkerRestartRequired) || result.Committed || !result.RestartRequired {
		t.Fatalf("RunOnce() = %+v, %v", result, err)
	}
	if time.Since(started) > 750*time.Millisecond {
		t.Fatal("Session sealing waited without a bound for a non-cooperative Emit")
	}
	select {
	case <-operationEntered:
	default:
		t.Fatal("non-cooperative Emit did not enter the store")
	}
	if store.terminalCommits.Load() != 0 {
		t.Fatalf("non-cooperative Emit raced %d terminal commits, want 0", store.terminalCommits.Load())
	}
	close(releaseOperation)
	select {
	case <-executorReturned:
	case <-time.After(time.Second):
		t.Fatal("released Emit executor did not return")
	}
}

func TestWorkerTerminalOwnershipLossIsReportedAsFenceLoss(t *testing.T) {
	_, base, _, turns := newSQLClaimNextFixture(t, "limits_terminal_fence")
	store := &observedWorkerStore{ExecutionStore: base}
	store.commit = func(ctx context.Context, command CommitAttemptCommand) (CommitAttemptResult, error) {
		if command.TerminalStatus != "" {
			return CommitAttemptResult{}, ErrAttemptFenced
		}
		return base.CommitAttempt(ctx, command)
	}
	worker := newLimitedTestWorker(t, store, testExecutor{
		run: func(context.Context, ExecutionSession) (agentv1.TurnStatus, error) {
			return agentv1.TurnStatusCompleted, nil
		},
	}, testPluginLimits(turns[0].Plugin, time.Second, time.Second), 100*time.Millisecond)

	result, err := worker.RunOnce(context.Background())
	if !errors.Is(err, ErrWorkerFenceLost) || result.Committed || !result.FenceLost {
		t.Fatalf("terminal-fence result = %+v, error = %v", result, err)
	}
}

func TestWorkerObservedCancellationWinsOverPluginCeiling(t *testing.T) {
	_, store, clock, turns := newSQLClaimNextFixture(t, "limits_cancel_priority")
	turn := turns[0]
	worker := newLimitedTestWorker(t, store, testExecutor{
		run: func(ctx context.Context, _ ExecutionSession) (agentv1.TurnStatus, error) {
			if _, err := store.RequestCancel(
				context.Background(), turn.PrincipalID, turn.ThreadID, turn.ID, clock.Get(),
				sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"cancellationRequested":true}`),
			); err != nil {
				return "", err
			}
			<-ctx.Done()
			return "", ctx.Err()
		},
	}, testPluginLimits(turn.Plugin, time.Second, 500*time.Millisecond), 250*time.Millisecond)

	result, err := worker.RunOnce(context.Background())
	if err != nil || !result.Committed || !result.Cancelled ||
		result.TerminalStatus != agentv1.TurnStatusStopped || result.LimitExceeded != "" {
		t.Fatalf("cancellation-priority result = %+v, error = %v", result, err)
	}
}

func TestWorkerLifecycleDeadlineWinsWithoutTerminalCommit(t *testing.T) {
	db, store, _, turns := newSQLClaimNextFixture(t, "limits_drain_priority")
	worker := newLimitedTestWorker(t, store, testExecutor{
		run: func(ctx context.Context, _ ExecutionSession) (agentv1.TurnStatus, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	}, testPluginLimits(turns[0].Plugin, time.Second, time.Second), 250*time.Millisecond)
	execCtx, cancelExec := context.WithTimeout(context.Background(), 70*time.Millisecond)
	defer cancelExec()

	result, err := worker.runOnce(context.Background(), execCtx, nil)
	if !errors.Is(err, context.DeadlineExceeded) || result.Committed ||
		result.TerminalStatus != "" || result.LimitExceeded != "" {
		t.Fatalf("lifecycle-deadline result = %+v, error = %v", result, err)
	}
	if got := executionTableCount(t, db, SQLTurnOperationTable, "turn_id = ?", turns[0].ID); got != 0 {
		t.Fatalf("lifecycle deadline persisted %d terminal Operations, want 0", got)
	}
}
