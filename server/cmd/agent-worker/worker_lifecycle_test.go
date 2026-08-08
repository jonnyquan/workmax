package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "server/contracts/agent/v1"
	"server/service/agentturn"
)

type lifecycleCloseRecorder struct {
	calls      atomic.Int32
	entered    chan struct{}
	enteredOne sync.Once
	release    <-chan struct{}
	err        error
	panicValue any
}

type lifecycleExecutionStore struct {
	agentturn.ExecutionStore
	commit func(context.Context, agentturn.CommitAttemptCommand) (agentturn.CommitAttemptResult, error)
}

func (store *lifecycleExecutionStore) CommitAttempt(
	ctx context.Context,
	command agentturn.CommitAttemptCommand,
) (agentturn.CommitAttemptResult, error) {
	if store.commit != nil {
		return store.commit(ctx, command)
	}
	return store.ExecutionStore.CommitAttempt(ctx, command)
}

func newLifecycleCloseRecorder() *lifecycleCloseRecorder {
	return &lifecycleCloseRecorder{entered: make(chan struct{})}
}

func (recorder *lifecycleCloseRecorder) Close(context.Context) error {
	recorder.calls.Add(1)
	recorder.enteredOne.Do(func() { close(recorder.entered) })
	if recorder.release != nil {
		<-recorder.release
	}
	if recorder.panicValue != nil {
		panic(recorder.panicValue)
	}
	return recorder.err
}

func lifecycleComposition(t *testing.T, probe RuntimeProbe, recorder *lifecycleCloseRecorder) *WorkerComposition {
	t.Helper()
	if probe == nil {
		probe = healthyRuntimeProbe{}
	}
	_, composition := composeForTestWithProbeAndResources(t, workerOnRollout(),
		fakeExecutor{run: func(context.Context, agentturn.ExecutionSession) (agentv1.TurnStatus, error) {
			return agentv1.TurnStatusCompleted, nil
		}}, &fakeDeliverer{}, &fakeSettlement{}, probe, recorder)

	if !workerProductionCompositionReady(composition) {
		t.Fatal("lifecycle fixture is not initially production-ready")
	}
	return composition
}

func assertLifecycleClosedExactlyOnce(t *testing.T, composition *WorkerComposition,
	recorder *lifecycleCloseRecorder, wantErr error,
) {
	t.Helper()
	select {
	case <-recorder.entered:
	case <-time.After(time.Second):
		t.Fatal("runtime did not initiate composition cleanup")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := composition.Close(ctx)
	if wantErr == nil && err != nil {
		t.Fatalf("composition.Close() error = %v, want nil", err)
	}
	if wantErr != nil && !errors.Is(err, wantErr) {
		t.Fatalf("composition.Close() error = %v, want %v", err, wantErr)
	}
	if got := recorder.calls.Load(); got != 1 {
		t.Fatalf("underlying Close calls = %d, want 1", got)
	}
	if workerCompositionReady(composition) {
		t.Fatal("closed composition remained ready")
	}

	secondCtx, secondCancel := context.WithTimeout(context.Background(), time.Second)
	defer secondCancel()
	secondErr := composition.Close(secondCtx)
	if wantErr == nil && secondErr != nil {
		t.Fatalf("second composition.Close() error = %v, want nil", secondErr)
	}
	if wantErr != nil && !errors.Is(secondErr, wantErr) {
		t.Fatalf("second composition.Close() error = %v, want %v", secondErr, wantErr)
	}
	if got := recorder.calls.Load(); got != 1 {
		t.Fatalf("underlying Close calls after repeat = %d, want 1", got)
	}
}

func lifecycleRuntime(composition *WorkerComposition, health *workerRuntimeHealth) workerRuntime {
	runtime := staticWorkerRuntime(workerOnConfigYAML)
	runtime.health = health
	runtime.build = func(context.Context, workerStartupSnapshot) (*WorkerComposition, error) {
		return composition, nil
	}
	runtime.signalContext = func(parent context.Context) (context.Context, context.CancelFunc) {
		return context.WithCancel(parent)
	}
	policy := defaultWorkerProbePolicy()
	policy.BuildTimeout = 200 * time.Millisecond
	policy.Timeout = 20 * time.Millisecond
	policy.StopGrace = 20 * time.Millisecond
	policy.ResourceCloseTimeout = 30 * time.Millisecond
	runtime.probePolicy = policy
	return runtime
}

func TestExecuteWorkerBuildClosesLateCompositionAfterTimeoutExactlyOnce(t *testing.T) {
	recorder := newLifecycleCloseRecorder()
	composition := lifecycleComposition(t, nil, recorder)
	entered := make(chan struct{})
	release := make(chan struct{})

	got, outcome := executeWorkerBuild(context.Background(), 20*time.Millisecond, 50*time.Millisecond,
		func(context.Context, workerStartupSnapshot) (*WorkerComposition, error) {
			close(entered)
			<-release
			return composition, nil
		}, workerStartupSnapshot{})
	if got != nil || outcome != workerBuildTimedOut {
		t.Fatalf("executeWorkerBuild() = (%p, %v), want (nil, timed out)", got, outcome)
	}
	select {
	case <-entered:
	default:
		t.Fatal("build was not entered")
	}
	close(release)
	assertLifecycleClosedExactlyOnce(t, composition, recorder, nil)
}

func TestExecuteWorkerBuildClosesLateCompositionAfterCancellationExactlyOnce(t *testing.T) {
	recorder := newLifecycleCloseRecorder()
	composition := lifecycleComposition(t, nil, recorder)
	entered := make(chan struct{})
	release := make(chan struct{})
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		composition *WorkerComposition
		outcome     workerBuildOutcome
	}
	done := make(chan result, 1)
	go func() {
		got, outcome := executeWorkerBuild(parent, time.Second, 50*time.Millisecond,
			func(context.Context, workerStartupSnapshot) (*WorkerComposition, error) {
				close(entered)
				<-release
				return composition, nil
			}, workerStartupSnapshot{})
		done <- result{composition: got, outcome: outcome}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("build was not entered")
	}
	cancel()
	select {
	case result := <-done:
		if result.composition != nil || result.outcome != workerBuildCanceled {
			t.Fatalf("executeWorkerBuild() = (%p, %v), want (nil, canceled)",
				result.composition, result.outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled build boundary did not return")
	}
	close(release)
	assertLifecycleClosedExactlyOnce(t, composition, recorder, nil)
}

func TestExecuteWorkerBuildClosesCompositionReturnedWithError(t *testing.T) {
	recorder := newLifecycleCloseRecorder()
	composition := lifecycleComposition(t, nil, recorder)
	got, outcome := executeWorkerBuild(context.Background(), time.Second, time.Second,
		func(context.Context, workerStartupSnapshot) (*WorkerComposition, error) {
			return composition, errors.New("SECRET_BUILD_FAILURE")
		}, workerStartupSnapshot{})
	if got != nil || outcome != workerBuildFailed {
		t.Fatalf("executeWorkerBuild() = (%p, %v), want (nil, failed)", got, outcome)
	}
	if got := recorder.calls.Load(); got != 1 {
		t.Fatalf("underlying Close calls at build return = %d, want synchronous cleanup", got)
	}
	assertLifecycleClosedExactlyOnce(t, composition, recorder, nil)
}

func TestRunClosesForgedAndUnreadyCompositions(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(*WorkerComposition)
	}{
		{name: "forged seal", mutate: func(composition *WorkerComposition) {
			composition.seal = &workerCompositionSeal{}
		}},
		{name: "unready report", mutate: func(composition *WorkerComposition) {
			composition.readiness.Ready = false
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := newLifecycleCloseRecorder()
			composition := lifecycleComposition(t, nil, recorder)
			testCase.mutate(composition)
			health := newWorkerRuntimeHealth(time.Now, time.Minute, time.Minute)
			runtime := lifecycleRuntime(composition, health)
			runtime.serve = func(context.Context, *WorkerComposition, *workerRuntimeHealth) error {
				t.Fatal("serve must not run for an invalid composition")
				return nil
			}

			if err := run(nil, runtime); !errors.Is(err, errWorkerDependenciesUnavailable) {
				t.Fatalf("run() error = %v, want dependency classification", err)
			}
			assertLifecycleClosedExactlyOnce(t, composition, recorder, nil)
		})
	}
}

func TestRunHandlesCompositionAfterStartupProbeOutcomes(t *testing.T) {
	for _, testCase := range []struct {
		name            string
		probe           func(context.Context) error
		configure       func(*workerRuntime, <-chan struct{})
		wantErr         error
		wantStopped     bool
		wantQuarantined bool
	}{
		{
			name:    "error",
			probe:   func(context.Context) error { return errors.New("SECRET_PROBE_ERROR") },
			wantErr: errWorkerDependenciesUnavailable,
		},
		{
			name:    "panic",
			probe:   func(context.Context) error { panic("SECRET_PROBE_PANIC") },
			wantErr: errWorkerDependenciesUnavailable,
		},
		{
			name: "cooperative deadline",
			probe: func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			},
			wantErr: errWorkerDependenciesUnavailable,
		},
		{
			name:            "timeout",
			probe:           func(context.Context) error { return nil },
			wantErr:         errWorkerProcessQuarantined,
			wantQuarantined: true,
		},
		{
			name:  "canceled",
			probe: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() },
			configure: func(runtime *workerRuntime, entered <-chan struct{}) {
				runtime.signalContext = func(parent context.Context) (context.Context, context.CancelFunc) {
					ctx, cancel := context.WithCancel(parent)
					go func() {
						<-entered
						cancel()
					}()
					return ctx, cancel
				}
			},
			wantStopped: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			probeEntered := make(chan struct{})
			var enterOnce sync.Once
			probeFunction := testCase.probe
			var releaseProbe chan struct{}
			if testCase.name == "timeout" {
				releaseProbe = make(chan struct{})
				defer close(releaseProbe)
				probeFunction = func(context.Context) error {
					<-releaseProbe
					return nil
				}
			}
			probe := runtimeProbeFuncs{startup: func(ctx context.Context) error {
				enterOnce.Do(func() { close(probeEntered) })
				return probeFunction(ctx)
			}}
			recorder := newLifecycleCloseRecorder()
			composition := lifecycleComposition(t, probe, recorder)
			health := newWorkerRuntimeHealth(time.Now, time.Minute, time.Minute)
			runtime := lifecycleRuntime(composition, health)
			runtime.serve = func(context.Context, *WorkerComposition, *workerRuntimeHealth) error {
				t.Fatal("serve must not run after startup probe failure")
				return nil
			}
			if testCase.configure != nil {
				testCase.configure(&runtime, probeEntered)
			}

			err := run(nil, runtime)
			if testCase.wantErr == nil && err != nil {
				t.Fatalf("run() error = %v, want nil", err)
			}
			if testCase.wantErr != nil && !errors.Is(err, testCase.wantErr) {
				t.Fatalf("run() error = %v, want %v", err, testCase.wantErr)
			}
			if err != nil && strings.Contains(err.Error(), "SECRET_PROBE") {
				t.Fatalf("run() leaked probe failure: %q", err)
			}
			if testCase.wantQuarantined {
				if got := recorder.calls.Load(); got != 0 {
					t.Fatalf("resource Close calls = %d, want zero while timed-out probe may still be live", got)
				}
			} else {
				assertLifecycleClosedExactlyOnce(t, composition, recorder, nil)
			}
			snapshot := health.Snapshot()
			if testCase.wantStopped && snapshot.Phase != string(workerPhaseStopped) {
				t.Fatalf("health = %+v, want stopped", snapshot)
			}
			if testCase.wantQuarantined && (snapshot.Phase != string(workerPhaseFailed) ||
				snapshot.Live || snapshot.Ready) {
				t.Fatalf("quarantined startup-probe health = %+v, want failed", snapshot)
			}
		})
	}
}

func TestRunCanceledNonCooperativeStartupProbeQuarantinesWithoutClosingResources(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	probe := runtimeProbeFuncs{startup: func(context.Context) error {
		defer close(finished)
		close(entered)
		<-release
		return errors.New("SECRET_LATE_STARTUP_AFTER_CANCEL")
	}}
	recorder := newLifecycleCloseRecorder()
	composition := lifecycleComposition(t, probe, recorder)
	health := newWorkerRuntimeHealth(time.Now, time.Minute, time.Minute)
	runtime := lifecycleRuntime(composition, health)
	runtime.probePolicy.Timeout = time.Second
	runtime.serve = func(context.Context, *WorkerComposition, *workerRuntimeHealth) error {
		t.Fatal("serve must not run after a detached startup probe")
		return nil
	}
	runtime.signalContext = func(parent context.Context) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(parent)
		go func() {
			<-entered
			cancel()
		}()
		return ctx, cancel
	}

	err := run(nil, runtime)
	if !errors.Is(err, errWorkerProcessQuarantined) {
		t.Fatalf("run() error = %v, health=%+v gate_open=%t, want process quarantine",
			err, health.Snapshot(), workerCompositionAdmissionGate(composition).Open())
	}
	if strings.Contains(err.Error(), "SECRET_LATE_STARTUP_AFTER_CANCEL") {
		t.Fatalf("run() leaked detached startup detail: %q", err)
	}
	if got := recorder.calls.Load(); got != 0 {
		t.Fatalf("resource Close calls = %d, want zero for detached startup probe", got)
	}
	if gate := workerCompositionAdmissionGate(composition); gate == nil || gate.Open() {
		t.Fatal("detached startup probe left AdmissionGate open")
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("released startup probe did not return")
	}
}

func TestRunKernelRestartSignalsQuarantineWithoutClosingResources(t *testing.T) {
	for _, source := range []string{"detached executor", "detached Emit"} {
		t.Run(source, func(t *testing.T) {
			recorder := newLifecycleCloseRecorder()
			composition := lifecycleComposition(t, nil, recorder)
			health := newWorkerRuntimeHealth(time.Now, time.Minute, time.Minute)
			runtime := lifecycleRuntime(composition, health)
			runtime.serve = func(context.Context, *WorkerComposition, *workerRuntimeHealth) error {
				// The service kernel proves both non-cooperative Execute and Emit
				// paths return ErrWorkerRestartRequired. This test owns the process
				// boundary that must retain that stable signal while dropping detail.
				return errors.Join(agentturn.ErrWorkerRestartRequired,
					errors.New("SECRET_DETACHED_WORK_DETAIL"))
			}

			err := run(nil, runtime)
			if !errors.Is(err, errWorkerProcessQuarantined) {
				t.Fatalf("run() error = %v, want process quarantine", err)
			}
			if strings.Contains(err.Error(), "SECRET_DETACHED_WORK_DETAIL") {
				t.Fatalf("run() leaked detached-work detail: %q", err)
			}
			if got := recorder.calls.Load(); got != 0 {
				t.Fatalf("resource Close calls = %d, want zero for %s", got, source)
			}
			snapshot := health.Snapshot()
			if snapshot.Phase != string(workerPhaseFailed) || snapshot.Live || snapshot.Ready {
				t.Fatalf("quarantined health = %+v, want failed", snapshot)
			}
		})
	}
}

func TestRunRealWorkerDetachedExecutorAndEmitQuarantineWithoutClosingResources(t *testing.T) {
	for _, source := range []string{"executor", "emit"} {
		t.Run(source, func(t *testing.T) {
			recorder := newLifecycleCloseRecorder()
			composition := lifecycleComposition(t, nil, recorder)
			turn := testTurn("quarantine_real_" + source)
			admit(t, composition.store, turn)

			release := make(chan struct{})
			executionEntered := make(chan struct{})
			executionReturned := make(chan struct{})
			var enterOnce sync.Once
			store := &lifecycleExecutionStore{ExecutionStore: composition.store}
			if source == "emit" {
				store.commit = func(
					ctx context.Context,
					command agentturn.CommitAttemptCommand,
				) (agentturn.CommitAttemptResult, error) {
					if command.TerminalStatus != "" {
						return store.ExecutionStore.CommitAttempt(ctx, command)
					}
					enterOnce.Do(func() { close(executionEntered) })
					<-release // deliberately violates the epoch-bound Emit context
					return agentturn.CommitAttemptResult{}, errors.New("SECRET_LATE_EMIT_RESULT")
				}
			}
			executor := fakeExecutor{run: func(
				_ context.Context,
				session agentturn.ExecutionSession,
			) (agentv1.TurnStatus, error) {
				defer close(executionReturned)
				if source == "executor" {
					enterOnce.Do(func() { close(executionEntered) })
					<-release // deliberately ignores the epoch cancellation
					return agentv1.TurnStatusCompleted, nil
				}
				_, err := session.Emit(context.Background(), agentturn.OperationDraft{
					OperationID: "operation_quarantine_emit",
					Event: agentturn.EventDraft{
						Type: "writer.document.delta",
						Data: json.RawMessage(`{"patch":"detached"}`),
					},
				})
				return "", err
			}}
			worker, err := agentturn.NewWorker(store, executor, agentturn.WorkerOptions{
				WorkerID:          "worker_quarantine_integration",
				WorkerBuildDigest: "sha256:worker-quarantine-integration",
				HeartbeatInterval: 5 * time.Millisecond,
				IdleBackoff:       time.Millisecond,
				DrainTimeout:      time.Second,
				ExecutorStopGrace: 20 * time.Millisecond,
				PluginLimits: []agentturn.PluginExecutionLimits{{
					Plugin: turn.Plugin, ExecutionTimeout: 200 * time.Millisecond,
					ProgressTimeout: 20 * time.Millisecond,
				}},
			})
			if err != nil {
				t.Fatalf("NewWorker(): %v", err)
			}

			health := newWorkerRuntimeHealth(time.Now, time.Minute, time.Minute)
			runtime := lifecycleRuntime(composition, health)
			runtime.serve = func(
				ctx context.Context,
				_ *WorkerComposition,
				gotHealth *workerRuntimeHealth,
			) error {
				return superviseWorkerLoops(ctx, workerLoopSet{
					workerExecutionLoop: func(loopCtx context.Context) error {
						return worker.RunWithPulse(loopCtx, nil,
							func() { gotHealth.loopPulse(workerExecutionLoop) })
					},
					workerReconcileLoop: func(loopCtx context.Context) error {
						gotHealth.loopPulse(workerReconcileLoop)
						<-loopCtx.Done()
						return loopCtx.Err()
					},
					workerDispatchLoop: func(loopCtx context.Context) error {
						gotHealth.loopPulse(workerDispatchLoop)
						<-loopCtx.Done()
						return loopCtx.Err()
					},
				}, gotHealth, time.Second)
			}

			err = run(nil, runtime)
			if !errors.Is(err, errWorkerProcessQuarantined) {
				t.Fatalf("run() error = %v, want real Worker process quarantine", err)
			}
			select {
			case <-executionEntered:
			default:
				t.Fatalf("detached %s path was not entered", source)
			}
			if got := recorder.calls.Load(); got != 0 {
				t.Fatalf("resource Close calls = %d, want zero for detached %s", got, source)
			}
			if strings.Contains(err.Error(), "SECRET_LATE_EMIT_RESULT") {
				t.Fatalf("run() leaked detached Emit detail: %q", err)
			}

			close(release)
			select {
			case <-executionReturned:
			case <-time.After(time.Second):
				t.Fatalf("released %s did not return", source)
			}
		})
	}
}

func TestRunRecurringProbeTimeoutQuarantinesWithoutClosingResources(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	probe := runtimeProbeFuncs{check: func(context.Context) error {
		defer close(finished)
		close(entered)
		<-release // deliberately violates the RuntimeProbe context contract
		return errors.New("SECRET_LATE_RECURRING_PROBE_RESULT")
	}}
	recorder := newLifecycleCloseRecorder()
	composition := lifecycleComposition(t, probe, recorder)
	policy := defaultWorkerProbePolicy()
	policy.Interval = time.Millisecond
	policy.Timeout = 20 * time.Millisecond
	policy.StopGrace = 20 * time.Millisecond
	policy.ShutdownTimeout = time.Second
	health := newWorkerRuntimeHealth(time.Now, policy.Freshness, policy.LoopFreshness)
	runtime := lifecycleRuntime(composition, health)
	runtime.probePolicy = policy
	runtime.serve = func(ctx context.Context, got *WorkerComposition, gotHealth *workerRuntimeHealth) error {
		return servePreparedWorker(ctx, got, gotHealth, policy)
	}

	err := run(nil, runtime)
	if !errors.Is(err, errWorkerProcessQuarantined) {
		t.Fatalf("run() error = %v, want process quarantine", err)
	}
	if strings.Contains(err.Error(), "SECRET_LATE_RECURRING_PROBE_RESULT") {
		t.Fatalf("run() leaked recurring probe detail: %q", err)
	}
	select {
	case <-entered:
	default:
		t.Fatal("recurring probe was not invoked")
	}
	if got := recorder.calls.Load(); got != 0 {
		t.Fatalf("resource Close calls = %d, want zero while recurring probe is detached", got)
	}
	if snapshot := health.Snapshot(); snapshot.Phase != string(workerPhaseFailed) ||
		snapshot.Live || snapshot.Ready || !containsReason(snapshot, reasonDependencyProbeFailed) {
		t.Fatalf("recurring-probe quarantine health = %+v", snapshot)
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("released recurring probe did not return")
	}
}

func TestRunCooperativeRecurringProbeDeadlineClosesResources(t *testing.T) {
	finished := make(chan struct{})
	probe := runtimeProbeFuncs{check: func(ctx context.Context) error {
		defer close(finished)
		<-ctx.Done()
		return ctx.Err()
	}}
	recorder := newLifecycleCloseRecorder()
	composition := lifecycleComposition(t, probe, recorder)
	policy := defaultWorkerProbePolicy()
	policy.Interval = time.Millisecond
	policy.Timeout = 20 * time.Millisecond
	policy.StopGrace = 20 * time.Millisecond
	policy.ShutdownTimeout = time.Second
	health := newWorkerRuntimeHealth(time.Now, policy.Freshness, policy.LoopFreshness)
	runtime := lifecycleRuntime(composition, health)
	runtime.probePolicy = policy
	runtime.serve = func(ctx context.Context, got *WorkerComposition, gotHealth *workerRuntimeHealth) error {
		return servePreparedWorker(ctx, got, gotHealth, policy)
	}

	err := run(nil, runtime)
	if !errors.Is(err, errWorkerRuntimeFailed) {
		t.Fatalf("run() error = %v, want stable readiness-loss runtime failure", err)
	}
	select {
	case <-finished:
	default:
		t.Fatal("runtime returned before cooperative probe fully quiesced")
	}
	assertLifecycleClosedExactlyOnce(t, composition, recorder, nil)
}

func TestRunCanceledNonCooperativeRecurringProbeQuarantinesWithoutClosingResources(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	probe := runtimeProbeFuncs{check: func(context.Context) error {
		defer close(finished)
		close(entered)
		<-release
		return errors.New("SECRET_LATE_RECURRING_AFTER_CANCEL")
	}}
	recorder := newLifecycleCloseRecorder()
	composition := lifecycleComposition(t, probe, recorder)
	policy := defaultWorkerProbePolicy()
	policy.Interval = time.Millisecond
	policy.Timeout = time.Second
	policy.StopGrace = 20 * time.Millisecond
	policy.ShutdownTimeout = time.Second
	health := newWorkerRuntimeHealth(time.Now, policy.Freshness, policy.LoopFreshness)
	runtime := lifecycleRuntime(composition, health)
	runtime.probePolicy = policy
	runtime.serve = func(ctx context.Context, got *WorkerComposition, gotHealth *workerRuntimeHealth) error {
		return servePreparedWorker(ctx, got, gotHealth, policy)
	}
	var cancelRuntime context.CancelFunc
	runtime.signalContext = func(parent context.Context) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(parent)
		cancelRuntime = cancel
		return ctx, cancel
	}

	done := make(chan error, 1)
	go func() { done <- run(nil, runtime) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("recurring probe was not invoked")
	}
	cancelRuntime()
	select {
	case err := <-done:
		if !errors.Is(err, errWorkerProcessQuarantined) {
			t.Fatalf("run() error = %v, want process quarantine", err)
		}
		if strings.Contains(err.Error(), "SECRET_LATE_RECURRING_AFTER_CANCEL") {
			t.Fatalf("run() leaked detached recurring detail: %q", err)
		}
	case <-time.After(time.Second):
		t.Fatal("detached recurring probe did not trigger bounded quarantine")
	}
	if got := recorder.calls.Load(); got != 0 {
		t.Fatalf("resource Close calls = %d, want zero for detached recurring probe", got)
	}
	if gate := workerCompositionAdmissionGate(composition); gate == nil || gate.Open() {
		t.Fatal("detached recurring probe left AdmissionGate open")
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("released recurring probe did not return")
	}
}

func TestServeStartupProbeTimeoutQuarantinesWithoutClosingResources(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	probe := runtimeProbeFuncs{startup: func(context.Context) error {
		defer close(finished)
		close(entered)
		<-release // deliberately violates the RuntimeProbe context contract
		return nil
	}}
	recorder := newLifecycleCloseRecorder()
	composition := lifecycleComposition(t, probe, recorder)

	err := Serve(context.Background(), composition)
	if !errors.Is(err, errWorkerProcessQuarantined) {
		t.Fatalf("Serve() error = %v, want process quarantine", err)
	}
	select {
	case <-entered:
	default:
		t.Fatal("startup probe was not invoked")
	}
	if got := recorder.calls.Load(); got != 0 {
		t.Fatalf("resource Close calls = %d, want zero while startup probe is detached", got)
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("released startup probe did not return")
	}
}

func TestRunClosesCompositionAfterServeOutcomes(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		serve     func(context.Context, context.CancelFunc) error
		wantError error
	}{
		{
			name: "error",
			serve: func(context.Context, context.CancelFunc) error {
				return errors.New("SECRET_SERVE_ERROR")
			},
			wantError: errWorkerRuntimeFailed,
		},
		{
			name: "panic",
			serve: func(context.Context, context.CancelFunc) error {
				panic("SECRET_SERVE_PANIC")
			},
			wantError: errWorkerRuntimeFailed,
		},
		{
			name:      "nil while context remains valid",
			serve:     func(context.Context, context.CancelFunc) error { return nil },
			wantError: errWorkerRuntimeFailed,
		},
		{
			name: "normal cancellation",
			serve: func(ctx context.Context, cancel context.CancelFunc) error {
				cancel()
				<-ctx.Done()
				return nil
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := newLifecycleCloseRecorder()
			composition := lifecycleComposition(t, nil, recorder)
			health := newWorkerRuntimeHealth(time.Now, time.Minute, time.Minute)
			runtime := lifecycleRuntime(composition, health)
			var lifetimeCancel context.CancelFunc
			runtime.signalContext = func(parent context.Context) (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(parent)
				lifetimeCancel = cancel
				return ctx, cancel
			}
			runtime.serve = func(ctx context.Context, _ *WorkerComposition, _ *workerRuntimeHealth) error {
				return testCase.serve(ctx, lifetimeCancel)
			}

			err := run(nil, runtime)
			if testCase.wantError == nil && err != nil {
				t.Fatalf("run() error = %v, want nil", err)
			}
			if testCase.wantError != nil && !errors.Is(err, testCase.wantError) {
				t.Fatalf("run() error = %v, want %v", err, testCase.wantError)
			}
			if err != nil && strings.Contains(err.Error(), "SECRET_SERVE") {
				t.Fatalf("run() leaked serve failure: %q", err)
			}
			assertLifecycleClosedExactlyOnce(t, composition, recorder, nil)
		})
	}
}

func TestRunClassifiesResourceCloseFailureAndTimeoutAndPreservesEarlierRuntimeFailure(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		closeError error
		blockClose bool
		serveError error
		wantError  error
		wantReason workerHealthReason
	}{
		{
			name:       "close error after clean cancellation",
			closeError: errors.New("SECRET_CLOSE_ERROR"),
			wantError:  errWorkerResourceCloseFailed,
			wantReason: reasonResourceCloseFailed,
		},
		{
			name:       "close timeout after clean cancellation",
			blockClose: true,
			wantError:  errWorkerResourceCloseTimedOut,
			wantReason: reasonResourceCloseTimeout,
		},
		{
			name:       "earlier runtime error wins over close error",
			closeError: errors.New("SECRET_CLOSE_ERROR"),
			serveError: errors.New("SECRET_RUNTIME_ERROR"),
			wantError:  errWorkerRuntimeFailed,
			wantReason: reasonRuntimeFailed,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var release chan struct{}
			recorder := newLifecycleCloseRecorder()
			recorder.err = testCase.closeError
			if testCase.blockClose {
				release = make(chan struct{})
				recorder.release = release
				defer close(release)
			}
			composition := lifecycleComposition(t, nil, recorder)
			health := newWorkerRuntimeHealth(time.Now, time.Minute, time.Minute)
			runtime := lifecycleRuntime(composition, health)
			var lifetimeCancel context.CancelFunc
			runtime.signalContext = func(parent context.Context) (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(parent)
				lifetimeCancel = cancel
				return ctx, cancel
			}
			runtime.serve = func(ctx context.Context, _ *WorkerComposition, _ *workerRuntimeHealth) error {
				if testCase.serveError != nil {
					return testCase.serveError
				}
				lifetimeCancel()
				<-ctx.Done()
				return nil
			}

			started := time.Now()
			err := run(nil, runtime)
			if !errors.Is(err, testCase.wantError) {
				t.Fatalf("run() error = %v, want %v", err, testCase.wantError)
			}
			if strings.Contains(err.Error(), "SECRET_CLOSE") || strings.Contains(err.Error(), "SECRET_RUNTIME") {
				t.Fatalf("run() leaked raw lifecycle error: %q", err)
			}
			if testCase.blockClose && time.Since(started) > time.Second {
				t.Fatalf("run() exceeded resource close hard bound: %s", time.Since(started))
			}
			assertLifecycleClosedExactlyOnce(t, composition, recorder, testCase.wantReason.closeError())
			snapshot := health.Snapshot()
			if snapshot.Phase != string(workerPhaseFailed) || snapshot.Live || snapshot.Ready ||
				!containsReason(snapshot, testCase.wantReason) {
				t.Fatalf("health after close failure = %+v, want failed with %s", snapshot, testCase.wantReason)
			}
		})
	}
}

func (reason workerHealthReason) closeError() error {
	if reason == reasonResourceCloseTimeout {
		return errWorkerResourceCloseTimedOut
	}
	return errWorkerResourceCloseFailed
}

func TestClosingCompositionPermanentlyInvalidatesSealedReadiness(t *testing.T) {
	recorder := newLifecycleCloseRecorder()
	composition := lifecycleComposition(t, nil, recorder)
	if !workerCompositionReady(composition) {
		t.Fatal("composition was not ready before close")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := composition.Close(ctx); err != nil {
		t.Fatalf("composition.Close() error = %v", err)
	}
	if workerCompositionReady(composition) {
		t.Fatal("sealed composition remained ready after close")
	}
	if got := recorder.calls.Load(); got != 1 {
		t.Fatalf("underlying Close calls = %d, want 1", got)
	}
}
