package main

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "server/contracts/agent/v1"
	"server/service/agentturn"
)

const workerOnConfigYAML = `
mysql_system:
  path: db.internal
  port: "3306"
  db-name: workmax_contract
  username: worker
  password: startup-secret
agent_platform_rollout:
  durable_turn:
    worker: on
  readiness:
    sql_store: true
    worker_lease_fencing: true
    transactional_outbox: true
    exactly_once_settlement: true
`

const workerDatabaseCheckConfigYAML = `
mysql_system:
  path: db.internal
  port: "3306"
  db-name: workmax_contract
  username: worker
  password: startup-secret
  Config: charset=utf8mb4&parseTime=True&loc=Local
agent_platform_rollout:
  durable_turn:
    worker: off
`

func staticWorkerRuntime(raw string) workerRuntime {
	return workerRuntime{
		getenv: func(string) string { return "" },
		readFile: func(string) ([]byte, error) {
			return []byte(raw), nil
		},
	}
}

func TestRunWorkerOffExitsBeforeDependencyFactory(t *testing.T) {
	const raw = `
agent_platform_rollout:
  credential:
    desktop_resource: invalid-other-role
    agent_resource: invalid-other-role
  durable_turn:
    public_api: invalid-other-role
    worker: off
  desktop:
    agent_transport: invalid-other-role
`
	runtime := staticWorkerRuntime(raw)
	runtime.health = newWorkerRuntimeHealth(time.Now, time.Minute, time.Minute)
	buildCalls := 0
	checkCalls := 0
	runtime.checkDatabase = func(context.Context, workerStartupSnapshot) error {
		checkCalls++
		return errors.New("must not be reached")
	}
	runtime.build = func(context.Context, workerStartupSnapshot) (*WorkerComposition, error) {
		buildCalls++
		return nil, errors.New("must not be reached")
	}

	if err := run(nil, runtime); err != nil {
		t.Fatalf("run() Worker-off error = %v", err)
	}
	if buildCalls != 0 {
		t.Fatalf("dependency factory calls = %d, want 0", buildCalls)
	}
	if checkCalls != 0 {
		t.Fatalf("database-check calls = %d, want 0", checkCalls)
	}
	if snapshot := runtime.health.Snapshot(); snapshot.Phase != string(workerPhaseStopped) || snapshot.Live || snapshot.Ready {
		t.Fatalf("Worker-off health = %+v, want stopped", snapshot)
	}
}

func TestRunExplicitDatabaseCheckWhileWorkerOffChecksOnceAndStartsNothing(t *testing.T) {
	runtime := staticWorkerRuntime(workerDatabaseCheckConfigYAML)
	health := newWorkerRuntimeHealth(time.Now, time.Minute, time.Minute)
	runtime.health = health
	checkCalls, buildCalls, serveCalls := 0, 0, 0
	runtime.checkDatabase = func(ctx context.Context, snapshot workerStartupSnapshot) error {
		checkCalls++
		if ctx == nil || !snapshot.DatabaseCheckRequested() || snapshot.WorkerEnabled() ||
			snapshot.MySQL().Dbname != "workmax_contract" {
			t.Fatalf("database-check snapshot = requested:%t worker:%t db:%q",
				snapshot.DatabaseCheckRequested(), snapshot.WorkerEnabled(), snapshot.MySQL().Dbname)
		}
		return nil
	}
	runtime.build = func(context.Context, workerStartupSnapshot) (*WorkerComposition, error) {
		buildCalls++
		return nil, nil
	}
	runtime.serve = func(context.Context, *WorkerComposition, *workerRuntimeHealth) error {
		serveCalls++
		return nil
	}
	runtime.signalContext = func(parent context.Context) (context.Context, context.CancelFunc) {
		return context.WithCancel(parent)
	}

	if err := run([]string{"-check-database"}, runtime); err != nil {
		t.Fatalf("run() database check error = %v", err)
	}
	if checkCalls != 1 || buildCalls != 0 || serveCalls != 0 {
		t.Fatalf("calls = check:%d build:%d serve:%d, want 1/0/0", checkCalls, buildCalls, serveCalls)
	}
	if snapshot := health.Snapshot(); snapshot.Phase != string(workerPhaseStopped) || snapshot.Live || snapshot.Ready {
		t.Fatalf("database-check health = %+v, want stopped", snapshot)
	}
}

func TestRunExplicitDatabaseCheckOverridesWorkerOnAndPlaintextNeverReachesBuild(t *testing.T) {
	runtime := staticWorkerRuntime(workerOnConfigYAML)
	checkCalls := 0
	runtime.checkDatabase = func(_ context.Context, snapshot workerStartupSnapshot) error {
		checkCalls++
		if !snapshot.WorkerEnabled() || !snapshot.DatabasePlaintextAllowed() {
			t.Fatalf("database-check snapshot = worker:%t plaintext:%t",
				snapshot.WorkerEnabled(), snapshot.DatabasePlaintextAllowed())
		}
		return nil
	}
	runtime.build = func(context.Context, workerStartupSnapshot) (*WorkerComposition, error) {
		t.Fatal("build must not run in explicit database-check mode")
		return nil, nil
	}
	runtime.serve = func(context.Context, *WorkerComposition, *workerRuntimeHealth) error {
		t.Fatal("serve must not run in explicit database-check mode")
		return nil
	}
	runtime.signalContext = func(parent context.Context) (context.Context, context.CancelFunc) {
		return context.WithCancel(parent)
	}

	if err := run([]string{"-check-database", "-allow-remote-plaintext-database"}, runtime); err != nil {
		t.Fatalf("run() database check error = %v", err)
	}
	if checkCalls != 1 {
		t.Fatalf("database-check calls = %d, want 1", checkCalls)
	}
}

func TestRunExplicitDatabaseCheckSanitizesFailureAndDoesNotBuild(t *testing.T) {
	runtime := staticWorkerRuntime(workerDatabaseCheckConfigYAML)
	buildCalls := 0
	runtime.checkDatabase = func(context.Context, workerStartupSnapshot) error {
		return errors.New("dial worker:startup-secret@db.internal failed")
	}
	runtime.build = func(context.Context, workerStartupSnapshot) (*WorkerComposition, error) {
		buildCalls++
		return nil, nil
	}
	runtime.signalContext = func(parent context.Context) (context.Context, context.CancelFunc) {
		return context.WithCancel(parent)
	}

	err := run([]string{"-check-database"}, runtime)
	if !errors.Is(err, errWorkerDatabaseCheckFailed) || buildCalls != 0 {
		t.Fatalf("run() = %v, build calls=%d; want stable failure and no build", err, buildCalls)
	}
	assertErrorOmits(t, err, "startup-secret", "db.internal", "worker:")
}

func TestRunExplicitDatabaseCheckTreatsSignalAsCanceledFailure(t *testing.T) {
	runtime := staticWorkerRuntime(workerDatabaseCheckConfigYAML)
	health := newWorkerRuntimeHealth(time.Now, time.Minute, time.Minute)
	runtime.health = health
	entered := make(chan struct{})
	runtime.checkDatabase = func(ctx context.Context, _ workerStartupSnapshot) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}
	runtime.signalContext = func(parent context.Context) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(parent)
		go func() {
			<-entered
			cancel()
		}()
		return ctx, cancel
	}

	if err := run([]string{"-check-database"}, runtime); !errors.Is(err, errWorkerDatabaseCheckCanceled) {
		t.Fatalf("run() cancellation error = %v, want stable canceled failure", err)
	}
	if snapshot := health.Snapshot(); snapshot.Phase != string(workerPhaseStopped) || snapshot.Live || snapshot.Ready {
		t.Fatalf("database-check cancellation health = %+v, want stopped", snapshot)
	}
}

func TestRunWorkerOnFailsClosedAndSanitizesFactoryError(t *testing.T) {
	runtime := staticWorkerRuntime(workerOnConfigYAML)
	buildCalls := 0
	runtime.build = func(_ context.Context, snapshot workerStartupSnapshot) (*WorkerComposition, error) {
		buildCalls++
		if !snapshot.WorkerEnabled() {
			t.Fatal("factory received a Worker-off snapshot")
		}
		return nil, errors.New("dial startup-secret@db.internal failed")
	}
	runtime.signalContext = func(parent context.Context) (context.Context, context.CancelFunc) {
		return context.WithCancel(parent)
	}
	runtime.serve = func(context.Context, *WorkerComposition, *workerRuntimeHealth) error {
		return nil
	}

	err := run(nil, runtime)
	if !errors.Is(err, errWorkerDependenciesUnavailable) {
		t.Fatalf("run() error = %v, want dependency classification", err)
	}
	if buildCalls != 1 {
		t.Fatalf("dependency factory calls = %d, want 1", buildCalls)
	}
	for _, secret := range []string{"startup-secret", "db.internal"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("run() leaked %q in %q", secret, err)
		}
	}
}

func TestRunBoundsANonCooperativeDependencyFactory(t *testing.T) {
	runtime := staticWorkerRuntime(workerOnConfigYAML)
	policy := defaultWorkerProbePolicy()
	policy.BuildTimeout = 20 * time.Millisecond
	runtime.probePolicy = policy
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	runtime.build = func(context.Context, workerStartupSnapshot) (*WorkerComposition, error) {
		defer close(finished)
		close(entered)
		<-release // deliberately violates the factory context contract
		return nil, errors.New("late SECRET_FACTORY_RESULT")
	}
	runtime.serve = func(context.Context, *WorkerComposition, *workerRuntimeHealth) error { return nil }
	runtime.signalContext = func(parent context.Context) (context.Context, context.CancelFunc) {
		return context.WithCancel(parent)
	}

	startedAt := time.Now()
	err := run(nil, runtime)
	if !errors.Is(err, errWorkerDependenciesUnavailable) {
		t.Fatalf("run() error = %v, want dependency classification", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("non-cooperative factory blocked startup for %s", elapsed)
	}
	select {
	case <-entered:
	default:
		t.Fatal("dependency factory was not invoked")
	}
	close(release)
	<-finished
	if strings.Contains(err.Error(), "SECRET_FACTORY_RESULT") {
		t.Fatalf("run() leaked late factory result in %q", err)
	}
}

func TestRunTreatsSignalDuringDependencyBuildAsCleanStop(t *testing.T) {
	runtime := staticWorkerRuntime(workerOnConfigYAML)
	health := newWorkerRuntimeHealth(time.Now, time.Minute, time.Minute)
	runtime.health = health
	entered := make(chan struct{})
	runtime.build = func(ctx context.Context, _ workerStartupSnapshot) (*WorkerComposition, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	runtime.serve = func(context.Context, *WorkerComposition, *workerRuntimeHealth) error {
		t.Fatal("Serve must not run after startup cancellation")
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

	if err := run(nil, runtime); err != nil {
		t.Fatalf("run() cancellation error = %v, want clean stop", err)
	}
	if snapshot := health.Snapshot(); snapshot.Phase != string(workerPhaseStopped) || snapshot.Live || snapshot.Ready {
		t.Fatalf("health after build cancellation = %+v, want stopped", snapshot)
	}
}

func TestRunTreatsInjectedServeReturnBeforeCancellationAsFailure(t *testing.T) {
	runtime := staticWorkerRuntime(workerOnConfigYAML)
	_, composition := composeForTest(t,
		fakeExecutor{run: func(context.Context, agentturn.ExecutionSession) (agentv1.TurnStatus, error) {
			return agentv1.TurnStatusCompleted, nil
		}},
		&fakeDeliverer{}, &fakeSettlement{})
	runtime.build = func(context.Context, workerStartupSnapshot) (*WorkerComposition, error) {
		return composition, nil
	}
	serveCalls := 0
	runtime.serve = func(ctx context.Context, got *WorkerComposition, health *workerRuntimeHealth) error {
		serveCalls++
		if ctx == nil || got != composition || health == nil {
			t.Fatalf("serve received ctx=%v composition=%p, want %p", ctx, got, composition)
		}
		if snapshot := health.Snapshot(); snapshot.Live == false || snapshot.Ready {
			t.Fatalf("health before loops = %+v, want live but not ready", snapshot)
		}
		return nil
	}
	runtime.signalContext = func(parent context.Context) (context.Context, context.CancelFunc) {
		return context.WithCancel(parent)
	}

	if err := run(nil, runtime); !errors.Is(err, errWorkerRuntimeFailed) {
		t.Fatalf("run() error = %v, want unexpected runtime return failure", err)
	}
	if serveCalls != 1 {
		t.Fatalf("serve calls = %d, want 1", serveCalls)
	}
}

func TestRunWorkerOnRefusesIncompleteOrUnreadyComposition(t *testing.T) {
	for name, composition := range map[string]*WorkerComposition{
		"nil":             nil,
		"empty":           {},
		"ready flag only": {readiness: agentturn.ReadinessReport{Ready: true}},
		"components but unready": {
			store:      &agentturn.SQLStore{},
			worker:     &agentturn.Worker{},
			reconciler: &agentturn.Reconciler{},
			dispatcher: &agentturn.EffectDispatcher{},
		},
		"forged readiness without Compose seal": {
			store:      &agentturn.SQLStore{},
			worker:     &agentturn.Worker{},
			reconciler: &agentturn.Reconciler{},
			dispatcher: &agentturn.EffectDispatcher{},
			readiness: agentturn.ReadinessReport{
				Ready: true,
				Derived: agentturn.DerivedReadiness{
					SQLStore:              true,
					WorkerLeaseFencing:    true,
					TransactionalOutbox:   true,
					ExactlyOnceSettlement: true,
				},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			runtime := staticWorkerRuntime(workerOnConfigYAML)
			runtime.build = func(context.Context, workerStartupSnapshot) (*WorkerComposition, error) {
				return composition, nil
			}
			runtime.serve = func(context.Context, *WorkerComposition, *workerRuntimeHealth) error { return nil }
			runtime.signalContext = func(parent context.Context) (context.Context, context.CancelFunc) {
				return context.WithCancel(parent)
			}
			if err := run(nil, runtime); !errors.Is(err, errWorkerDependenciesUnavailable) {
				t.Fatalf("run() error = %v, want dependency classification", err)
			}
		})
	}
}

func TestRunSanitizesServeError(t *testing.T) {
	const secret = "provider-response SECRET_RUNTIME_VALUE"
	_, composition := composeForTest(t,
		fakeExecutor{run: func(context.Context, agentturn.ExecutionSession) (agentv1.TurnStatus, error) {
			return agentv1.TurnStatusCompleted, nil
		}},
		&fakeDeliverer{}, &fakeSettlement{})
	runtime := staticWorkerRuntime(workerOnConfigYAML)
	runtime.build = func(context.Context, workerStartupSnapshot) (*WorkerComposition, error) {
		return composition, nil
	}
	runtime.serve = func(context.Context, *WorkerComposition, *workerRuntimeHealth) error {
		return errors.New(secret)
	}
	runtime.signalContext = func(parent context.Context) (context.Context, context.CancelFunc) {
		return context.WithCancel(parent)
	}

	err := run(nil, runtime)
	if !errors.Is(err, errWorkerRuntimeFailed) {
		t.Fatalf("run() error = %v, want runtime classification", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("run() leaked Serve error in %q", err)
	}
}

func TestRunFailsClosedAndSanitizesStartupProbeFailure(t *testing.T) {
	for name, startup := range map[string]func(context.Context) error{
		"error": func(context.Context) error {
			return errors.New("dial startup-secret@db.internal:3306")
		},
		"panic": func(context.Context) error {
			panic("provider response SECRET_STARTUP_PROBE")
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, composition := composeForTestWithProbe(t, workerOnRollout(),
				fakeExecutor{run: func(context.Context, agentturn.ExecutionSession) (agentv1.TurnStatus, error) {
					return agentv1.TurnStatusCompleted, nil
				}}, &fakeDeliverer{}, &fakeSettlement{}, runtimeProbeFuncs{startup: startup})
			health := newWorkerRuntimeHealth(time.Now, time.Minute, time.Minute)
			runtime := staticWorkerRuntime(workerOnConfigYAML)
			runtime.health = health
			runtime.build = func(context.Context, workerStartupSnapshot) (*WorkerComposition, error) {
				return composition, nil
			}
			serveCalls := 0
			runtime.serve = func(context.Context, *WorkerComposition, *workerRuntimeHealth) error {
				serveCalls++
				return nil
			}
			runtime.signalContext = func(parent context.Context) (context.Context, context.CancelFunc) {
				return context.WithCancel(parent)
			}

			err := run(nil, runtime)
			if !errors.Is(err, errWorkerDependenciesUnavailable) {
				t.Fatalf("run() error = %v, want dependency classification", err)
			}
			if serveCalls != 0 {
				t.Fatalf("Serve calls = %d, want 0", serveCalls)
			}
			for _, secret := range []string{"startup-secret", "db.internal", "SECRET_STARTUP_PROBE", "provider response"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("run() leaked %q in %q", secret, err)
				}
			}
			snapshot := health.Snapshot()
			if snapshot.Phase != string(workerPhaseFailed) || snapshot.Live || snapshot.Ready ||
				!containsReason(snapshot, reasonStartupProbeFailed) {
				t.Fatalf("startup-probe health = %+v", snapshot)
			}
		})
	}
}

func TestRunTreatsSignalDuringStartupProbeAsCleanStop(t *testing.T) {
	probeEntered := make(chan struct{})
	probe := runtimeProbeFuncs{startup: func(ctx context.Context) error {
		close(probeEntered)
		<-ctx.Done()
		return ctx.Err()
	}}
	_, composition := composeForTestWithProbe(t, workerOnRollout(),
		fakeExecutor{run: func(context.Context, agentturn.ExecutionSession) (agentv1.TurnStatus, error) {
			return agentv1.TurnStatusCompleted, nil
		}}, &fakeDeliverer{}, &fakeSettlement{}, probe)
	health := newWorkerRuntimeHealth(time.Now, time.Minute, time.Minute)
	runtime := staticWorkerRuntime(workerOnConfigYAML)
	runtime.health = health
	runtime.build = func(context.Context, workerStartupSnapshot) (*WorkerComposition, error) {
		return composition, nil
	}
	serveCalls := 0
	runtime.serve = func(context.Context, *WorkerComposition, *workerRuntimeHealth) error {
		serveCalls++
		return nil
	}
	runtime.signalContext = func(parent context.Context) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(parent)
		go func() {
			<-probeEntered
			cancel()
		}()
		return ctx, cancel
	}

	if err := run(nil, runtime); err != nil {
		t.Fatalf("run() cancellation error = %v, want clean stop", err)
	}
	if serveCalls != 0 {
		t.Fatalf("Serve calls = %d, want 0", serveCalls)
	}
	if snapshot := health.Snapshot(); snapshot.Phase != string(workerPhaseStopped) || snapshot.Live || snapshot.Ready {
		t.Fatalf("health after startup-probe cancellation = %+v, want stopped", snapshot)
	}
}

func TestRunReadinessLossStopsNewClaimsDrainsOwnedTurnAndRequiresRestart(t *testing.T) {
	var failProbe atomic.Bool
	probe := runtimeProbeFuncs{check: func(context.Context) error {
		if failProbe.Load() {
			return errors.New("SECRET_RECURRING_PROBE_FAILURE")
		}
		return nil
	}}
	executionStarted := make(chan struct{})
	releaseExecution := make(chan struct{})
	secondExecution := make(chan struct{}, 1)
	var executionCalls atomic.Int64
	executor := fakeExecutor{run: func(context.Context, agentturn.ExecutionSession) (agentv1.TurnStatus, error) {
		if executionCalls.Add(1) == 1 {
			close(executionStarted)
			<-releaseExecution
			return agentv1.TurnStatusCompleted, nil
		}
		select {
		case secondExecution <- struct{}{}:
		default:
		}
		return agentv1.TurnStatusCompleted, nil
	}}
	recorder := newLifecycleCloseRecorder()
	db, composition := composeForTestWithProbeAndResources(
		t, workerOnRollout(), executor, &fakeDeliverer{}, &fakeSettlement{}, probe, recorder,
	)
	first := testTurn("readiness_drain_owned")
	admit(t, composition.store, first)

	policy := workerProbePolicy{
		Interval: 2 * time.Millisecond, Timeout: 100 * time.Millisecond,
		Freshness: time.Second, LoopFreshness: time.Second,
		BuildTimeout: time.Second, ShutdownTimeout: time.Second,
		ResourceCloseTimeout: time.Second,
	}.normalized()
	health := newWorkerRuntimeHealth(time.Now, policy.Freshness, policy.LoopFreshness)
	runtime := staticWorkerRuntime(workerOnConfigYAML)
	runtime.health = health
	runtime.probePolicy = policy
	runtime.build = func(context.Context, workerStartupSnapshot) (*WorkerComposition, error) {
		return composition, nil
	}
	runtime.serve = func(ctx context.Context, got *WorkerComposition, gotHealth *workerRuntimeHealth) error {
		return servePreparedWorker(ctx, got, gotHealth, policy)
	}
	runtime.signalContext = func(parent context.Context) (context.Context, context.CancelFunc) {
		return context.WithCancel(parent)
	}

	done := make(chan error, 1)
	go func() { done <- run(nil, runtime) }()
	select {
	case <-executionStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("Worker did not Claim the first Turn")
	}
	second := testTurn("readiness_no_second_claim")
	admit(t, composition.store, second)

	failProbe.Store(true)
	waitForHealth(t, health, func(snapshot workerHealthSnapshot) bool {
		return snapshot.Phase == string(workerPhaseDraining) && snapshot.Live && !snapshot.Ready &&
			containsReason(snapshot, reasonDraining) &&
			containsReason(snapshot, reasonDependencyProbeFailed)
	})
	if gate := workerCompositionAdmissionGate(composition); gate == nil || gate.Open() ||
		!errors.Is(gate.Acquire(), agentturn.ErrAdmissionClosed) {
		t.Fatal("readiness loss became visible before the exact AdmissionGate closed")
	}
	select {
	case <-secondExecution:
		t.Fatal("Worker executed a second Turn after readiness was lost")
	default:
	}
	close(releaseExecution)

	select {
	case err := <-done:
		if !errors.Is(err, errWorkerRuntimeFailed) {
			t.Fatalf("run() error = %v, want stable restart-required runtime failure", err)
		}
		if strings.Contains(err.Error(), "SECRET_RECURRING_PROBE_FAILURE") {
			t.Fatalf("run() leaked recurring probe error in %q", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("readiness-loss drain did not complete")
	}

	if calls := executionCalls.Load(); calls != 1 {
		t.Fatalf("executor calls = %d, want only the already-owned Turn", calls)
	}
	var secondAttempts int64
	if err := db.Table("w_agent_turn_attempt").Where("turn_id = ?", second.ID).
		Count(&secondAttempts).Error; err != nil {
		t.Fatalf("count second Turn attempts: %v", err)
	}
	if secondAttempts != 0 {
		t.Fatalf("second Turn attempts = %d, want zero Claims after readiness loss", secondAttempts)
	}
	firstState, err := composition.store.GetOwned(
		context.Background(), first.PrincipalID, first.ThreadID, first.ID,
	)
	if err != nil || firstState.Status != agentv1.TurnStatusCompleted {
		t.Fatalf("drained first Turn = status %q error %v, want completed", firstState.Status, err)
	}
	secondState, err := composition.store.GetOwned(
		context.Background(), second.PrincipalID, second.ThreadID, second.ID,
	)
	if err != nil || secondState.Status != agentv1.TurnStatusQueued {
		t.Fatalf("unclaimed second Turn = status %q error %v, want queued", secondState.Status, err)
	}
	final := health.Snapshot()
	if final.Phase != string(workerPhaseFailed) || final.Live || final.Ready ||
		!containsReason(final, reasonDependencyProbeFailed) ||
		containsReason(final, reasonRuntimeFailed) {
		t.Fatalf("final readiness-loss health = %+v", final)
	}
	assertLifecycleClosedExactlyOnce(t, composition, recorder, nil)
}

func TestLogWorkerResultSanitizesExecutorError(t *testing.T) {
	const secret = "user-content SECRET_EXECUTOR_VALUE"
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)

	logWorkerResult(agentturn.WorkerRunResult{
		TurnID: "turn_safe", AttemptID: "attempt_safe",
	}, errors.New(secret))
	if strings.Contains(output.String(), secret) {
		t.Fatalf("worker log leaked executor error in %q", output.String())
	}
	for _, identity := range []string{"turn_safe", "attempt_safe", "reason=runtime_error"} {
		if !strings.Contains(output.String(), identity) {
			t.Fatalf("worker log = %q, want %q", output.String(), identity)
		}
	}
}
