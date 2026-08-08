// Command agent-worker runs the durable Agent Turn execution loops.
//
// It is a separate binary on purpose. The API server's composition root must
// stay free of the durable kernel until the migration gates pass, and the
// repository's negative composition gate asserts exactly that. Running the
// loops here means a worker deployment can be enabled, scaled and rolled back
// without touching the process that currently serves production traffic.
//
// The process refuses to start unless readiness derived from what it actually
// composed permits the configured rollout. Its defaults keep everything off,
// so deploying this binary without an explicit rollout is a no-op that exits
// cleanly rather than a silent traffic switch.
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"server/service/agentturn"
)

func main() {
	if err := run(os.Args[1:], productionWorkerRuntime()); err != nil {
		log.Printf("agent-worker: %v", err)
		os.Exit(1)
	}
}

var (
	errWorkerDependenciesUnavailable = errors.New("agent-worker production dependencies are unavailable")
	errWorkerRuntimeFailed           = errors.New("agent-worker runtime failed")
	errWorkerLoopFailed              = errors.New("agent-worker loop failed")
	errWorkerReadinessLost           = errors.New("agent-worker runtime readiness lost")
	errWorkerProcessQuarantined      = errors.New("agent-worker process is quarantined")
	errWorkerDatabaseCheckFailed     = errors.New("agent-worker database preflight failed")
	errWorkerDatabaseCheckTimedOut   = errors.New("agent-worker database preflight timed out")
	errWorkerDatabaseCheckCanceled   = errors.New("agent-worker database preflight canceled")
)

// workerRuntime is the process boundary around startup I/O and production
// dependency construction. Keeping every dependency behind build proves the
// Worker-off path cannot accidentally open a database or initialize a provider.
type workerRuntime struct {
	getenv        func(string) string
	readFile      func(string) ([]byte, error)
	checkDatabase func(context.Context, workerStartupSnapshot) error
	build         func(context.Context, workerStartupSnapshot) (*WorkerComposition, error)
	serve         func(context.Context, *WorkerComposition, *workerRuntimeHealth) error
	signalContext func(context.Context) (context.Context, context.CancelFunc)
	health        *workerRuntimeHealth
	probePolicy   workerProbePolicy
}

func productionWorkerRuntime() workerRuntime {
	policy := defaultWorkerProbePolicy()
	return workerRuntime{
		getenv:        os.Getenv,
		readFile:      readSecureWorkerConfig,
		checkDatabase: checkProductionWorkerDatabase,
		build:         unwiredWorkerComposition,
		serve: func(ctx context.Context, composition *WorkerComposition, health *workerRuntimeHealth) error {
			return servePreparedWorker(ctx, composition, health, policy)
		},
		signalContext: SignalContext,
		health:        newWorkerRuntimeHealth(time.Now, policy.Freshness, policy.LoopFreshness),
		probePolicy:   policy,
	}
}

// unwiredWorkerComposition is deliberately fail-closed. P0-038 defines the
// pure, pre-dependency-acquisition plan contract, but the shipped build has
// no artifact identity, promoted Plugin releases, exact Claim scope or real
// domain/Effect/Credits factories. Those dependencies arrive only with their
// own production evidence, bounded resource ownership and rollout approval.
func unwiredWorkerComposition(_ context.Context, snapshot workerStartupSnapshot) (*WorkerComposition, error) {
	// Exercise the same static gate future production wiring must pass. The
	// zero build/plan/catalog is intentional and invokes no factory or I/O.
	_, _ = validateWorkerDependencyPlan(
		snapshot,
		workerBuildIdentity{},
		workerDependencyPlan{},
		workerDependencyCatalog{},
	)
	return nil, errWorkerDependenciesUnavailable
}

type workerBuildOutcome uint8

const (
	workerBuildFailed workerBuildOutcome = iota
	workerBuildSucceeded
	workerBuildTimedOut
	workerBuildCanceled
)

// executeWorkerBuild gives future database/provider factories a cancellable,
// bounded process boundary. The factory contract requires honoring ctx; if it
// does not, startup still returns after the deadline. Any composition returned
// after its caller has left is immediately handed to the asynchronous,
// bounded resource reaper instead of being lost in a buffered result channel.
// That genuinely late path is best-effort because the process cannot wait for
// a factory that may never return; a rejected result already received at this
// boundary is synchronously closed before the outcome is returned.
func executeWorkerBuild(parent context.Context, timeout, closeTimeout time.Duration,
	build func(context.Context, workerStartupSnapshot) (*WorkerComposition, error),
	snapshot workerStartupSnapshot,
) (*WorkerComposition, workerBuildOutcome) {
	if parent == nil || build == nil {
		return nil, workerBuildFailed
	}
	if timeout <= 0 {
		timeout = defaultWorkerBuildTimeout
	}
	if closeTimeout <= 0 {
		closeTimeout = defaultWorkerResourceCloseTimeout
	}
	buildCtx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	type buildResult struct {
		composition *WorkerComposition
		succeeded   bool
	}
	result := make(chan buildResult)
	abandoned := make(chan struct{})
	defer close(abandoned)
	go func() {
		built := buildResult{}
		defer func() {
			_ = recover()
			select {
			case result <- built:
			case <-abandoned:
				beginWorkerCompositionClose(built.composition, closeTimeout)
			}
		}()
		composition, err := build(buildCtx, snapshot)
		built.composition = composition
		built.succeeded = err == nil
	}()

	select {
	case built := <-result:
		if parent.Err() != nil {
			_ = closeWorkerComposition(parent, closeTimeout, built.composition)
			return nil, workerBuildCanceled
		}
		if errors.Is(buildCtx.Err(), context.DeadlineExceeded) {
			_ = closeWorkerComposition(parent, closeTimeout, built.composition)
			return nil, workerBuildTimedOut
		}
		if !built.succeeded {
			_ = closeWorkerComposition(parent, closeTimeout, built.composition)
			return nil, workerBuildFailed
		}
		return built.composition, workerBuildSucceeded
	case <-parent.Done():
		return nil, workerBuildCanceled
	case <-buildCtx.Done():
		if parent.Err() != nil {
			return nil, workerBuildCanceled
		}
		return nil, workerBuildTimedOut
	}
}

type workerDatabaseCheckOutcome uint8

const (
	workerDatabaseCheckFailed workerDatabaseCheckOutcome = iota
	workerDatabaseCheckSucceeded
	workerDatabaseCheckTimedOut
	workerDatabaseCheckCanceled
)

// executeWorkerDatabaseCheck keeps the explicit preflight bounded and returns
// only stable classifications. Driver/TLS/schema errors may contain topology
// or credentials and are never allowed to cross this process boundary.
func executeWorkerDatabaseCheck(parent context.Context, timeout time.Duration,
	check func(context.Context, workerStartupSnapshot) error,
	snapshot workerStartupSnapshot,
) (error, workerDatabaseCheckOutcome) {
	if parent == nil || check == nil {
		return errWorkerDatabaseCheckFailed, workerDatabaseCheckFailed
	}
	if timeout <= 0 {
		timeout = defaultWorkerBuildTimeout
	}
	checkCtx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	result := make(chan error)
	abandoned := make(chan struct{})
	defer close(abandoned)
	go func() {
		checkErr := errWorkerDatabaseCheckFailed
		defer func() {
			_ = recover()
			select {
			case result <- stableWorkerDatabaseCheckError(checkErr):
			case <-abandoned:
			}
		}()
		checkErr = check(checkCtx, snapshot)
	}()

	select {
	case checkErr := <-result:
		if parent.Err() != nil {
			return errWorkerDatabaseCheckCanceled, workerDatabaseCheckCanceled
		}
		if errors.Is(checkCtx.Err(), context.DeadlineExceeded) {
			return errWorkerDatabaseCheckTimedOut, workerDatabaseCheckTimedOut
		}
		if checkErr != nil {
			return checkErr, workerDatabaseCheckFailed
		}
		return nil, workerDatabaseCheckSucceeded
	case <-parent.Done():
		return errWorkerDatabaseCheckCanceled, workerDatabaseCheckCanceled
	case <-checkCtx.Done():
		if parent.Err() != nil {
			return errWorkerDatabaseCheckCanceled, workerDatabaseCheckCanceled
		}
		return errWorkerDatabaseCheckTimedOut, workerDatabaseCheckTimedOut
	}
}

func stableWorkerDatabaseCheckError(err error) error {
	if err == nil {
		return nil
	}
	for _, stable := range []error{
		errWorkerMySQLSettings,
		errWorkerMySQLConnection,
		errWorkerMySQLNetwork,
		errWorkerMySQLTLS,
		errWorkerMySQLAuthentication,
		errWorkerMySQLDatabase,
		errWorkerMySQLSchema,
		errWorkerMySQLSchemaSession,
		errWorkerMySQLSchemaMetadata,
		errWorkerMySQLSchemaTables,
		errWorkerMySQLSchemaIndexes,
		errWorkerMySQLSchemaFKs,
		errWorkerMySQLClose,
	} {
		if errors.Is(err, stable) {
			return stable
		}
	}
	return errWorkerDatabaseCheckFailed
}

func run(args []string, runtime workerRuntime) (resultErr error) {
	policy := runtime.probePolicy.normalized()
	health := runtime.health
	if health == nil {
		health = newWorkerRuntimeHealth(time.Now, policy.Freshness, policy.LoopFreshness)
	}
	snapshot, err := loadWorkerStartupSnapshot(args, runtime.getenv, runtime.readFile)
	if err != nil {
		health.failRuntime()
		return err
	}
	if snapshot.DatabaseCheckRequested() {
		if runtime.checkDatabase == nil || runtime.signalContext == nil {
			health.failRuntime()
			return errWorkerDependenciesUnavailable
		}
		ctx, stop := runtime.signalContext(context.Background())
		if ctx == nil || stop == nil {
			health.failRuntime()
			return errWorkerDependenciesUnavailable
		}
		defer stop()
		checkErr, outcome := executeWorkerDatabaseCheck(ctx, policy.BuildTimeout, runtime.checkDatabase, snapshot)
		switch outcome {
		case workerDatabaseCheckSucceeded:
			health.stop()
			log.Printf("agent-worker: database preflight passed")
			return nil
		case workerDatabaseCheckCanceled:
			health.beginDrain()
			health.stop()
			return errWorkerDatabaseCheckCanceled
		default:
			health.failRuntime()
			return checkErr
		}
	}

	// Worker is the only role this process owns. API/Desktop rollout fields
	// cannot make it construct dependencies or remain alive.
	if !snapshot.WorkerEnabled() {
		health.stop()
		log.Printf("agent-worker: Worker role is disabled; nothing to run")
		return nil
	}
	if runtime.build == nil || runtime.serve == nil || runtime.signalContext == nil {
		health.failRuntime()
		return errWorkerDependenciesUnavailable
	}
	ctx, stop := runtime.signalContext(context.Background())
	if ctx == nil || stop == nil {
		health.failRuntime()
		return errWorkerDependenciesUnavailable
	}
	defer stop()
	composition, buildOutcome := executeWorkerBuild(
		ctx, policy.BuildTimeout, policy.ResourceCloseTimeout, runtime.build, snapshot,
	)
	if buildOutcome == workerBuildCanceled {
		health.beginDrain()
		health.stop()
		return nil
	}
	if buildOutcome != workerBuildSucceeded {
		// Dependency constructors may include a DSN, provider response or secret
		// in their errors. The process boundary deliberately returns only a
		// stable classification.
		health.failRuntime()
		return errWorkerDependenciesUnavailable
	}
	quarantined := false
	if composition != nil {
		defer func() {
			// A quarantined goroutine may still be executing provider code, an
			// Operation Emit, a loop, or a RuntimeProbe against composition-owned
			// dependencies. Only process termination can safely revoke that access;
			// closing here would race the detached work.
			if quarantined {
				return
			}
			closeErr := closeWorkerComposition(ctx, policy.ResourceCloseTimeout, composition)
			if closeErr == nil {
				if resultErr == nil {
					health.stop()
				}
				return
			}
			health.failResourceClose(errors.Is(closeErr, errWorkerResourceCloseTimedOut))
			if resultErr == nil {
				resultErr = closeErr
			}
		}()
	}
	if !workerProductionCompositionReady(composition) {
		health.failRuntime()
		return errWorkerDependenciesUnavailable
	}
	if !health.bindAdmissionGate(workerCompositionAdmissionGate(composition)) {
		health.failRuntime()
		return errWorkerDependenciesUnavailable
	}
	if !health.markCompositionReady() {
		health.failRuntime()
		return errWorkerDependenciesUnavailable
	}
	probeOutcome := runWorkerStartupProbeWithGrace(
		ctx, composition.probe, health, policy.Timeout, policy.StopGrace,
	)
	if probeOutcome == workerProbeCanceled {
		health.beginDrain()
		return nil
	}
	if probeOutcome == workerProbeDetached {
		quarantined = true
		health.failRuntime()
		return errWorkerProcessQuarantined
	}
	if probeOutcome != workerProbeSucceeded {
		return errWorkerDependenciesUnavailable
	}
	serveErr := runWorkerServeSafely(ctx, runtime.serve, composition, health)
	if errors.Is(serveErr, errWorkerProcessQuarantined) {
		quarantined = true
		health.failRuntime()
		return errWorkerProcessQuarantined
	}
	if serveErr != nil {
		// Runtime errors may carry provider responses, user content or driver
		// details. Preserve those in a future bounded telemetry pipeline, not in
		// the process-level error that main logs.
		health.failRuntime()
		return errWorkerRuntimeFailed
	}
	if ctx.Err() == nil {
		// A long-running Worker has no successful spontaneous exit. Returning
		// nil while its lifetime context is still active is an unexpected loop
		// loss, not a clean shutdown.
		health.failRuntime()
		return errWorkerRuntimeFailed
	}
	health.beginDrain()
	return nil
}

func workerCompositionReady(composition *WorkerComposition) bool {
	if composition == nil {
		return false
	}
	readiness := composition.readiness
	return composition.seal.matches(composition) && readiness.Ready &&
		len(readiness.Blockers) == 0 && len(readiness.Overclaimed) == 0 &&
		readiness.Derived.SQLStore && readiness.Derived.WorkerLeaseFencing &&
		readiness.Derived.TransactionalOutbox && readiness.Derived.ExactlyOnceSettlement &&
		!readiness.Derived.AtomicLiveEventStream &&
		composition.store != nil && composition.worker != nil &&
		composition.reconciler != nil && composition.dispatcher != nil &&
		composition.resources != nil && composition.resources.isOpen() &&
		composition.probe != nil
}

// workerProductionCompositionReady is the process-lifecycle gate. Structural
// readiness and exact Claim/Executor/Effect scope only make a candidate. The
// private transfer seal additionally proves the acquisition Guard committed
// the exact same resource owner while its build context was still active.
func workerProductionCompositionReady(composition *WorkerComposition) bool {
	return workerCompositionReady(composition) && composition.runtimeScope != nil &&
		composition.runtimeScope.intact(
			composition.store, composition.worker, composition.reconciler, composition.dispatcher,
		) &&
		composition.ownershipTransfer != nil && composition.ownershipTransfer.intact(composition)
}

// Serve consumes a verified composition and runs it with a private health
// tracker. The caller must not reuse or close the composition concurrently.
// The process-level run path uses the same implementation with the tracker
// that a future, separately bound operator server may observe. If Serve
// returns errWorkerProcessQuarantined, a goroutine may still hold composition
// resources: the caller must terminate the process and must not Close, reuse,
// or serve the composition again. main enforces that contract with os.Exit.
func Serve(ctx context.Context, composition *WorkerComposition) (resultErr error) {
	policy := defaultWorkerProbePolicy()
	health := newWorkerRuntimeHealth(time.Now, policy.Freshness, policy.LoopFreshness)
	quarantined := false
	if composition != nil {
		defer func() {
			if quarantined {
				return
			}
			closeErr := closeWorkerComposition(ctx, policy.ResourceCloseTimeout, composition)
			if closeErr == nil {
				if resultErr == nil {
					health.stop()
				}
				return
			}
			health.failResourceClose(errors.Is(closeErr, errWorkerResourceCloseTimedOut))
			if resultErr == nil {
				resultErr = closeErr
			}
		}()
	}
	if ctx == nil || !workerProductionCompositionReady(composition) {
		return errWorkerDependenciesUnavailable
	}
	if !health.bindAdmissionGate(workerCompositionAdmissionGate(composition)) ||
		!health.markCompositionReady() {
		return errWorkerDependenciesUnavailable
	}
	probeOutcome := runWorkerStartupProbeWithGrace(
		ctx, composition.probe, health, policy.Timeout, policy.StopGrace,
	)
	if probeOutcome == workerProbeCanceled {
		health.beginDrain()
		return nil
	}
	if probeOutcome == workerProbeDetached {
		quarantined = true
		health.failRuntime()
		return errWorkerProcessQuarantined
	}
	if probeOutcome != workerProbeSucceeded {
		return errWorkerDependenciesUnavailable
	}
	serveErr := runWorkerServeSafely(ctx, func(
		serveCtx context.Context, prepared *WorkerComposition, preparedHealth *workerRuntimeHealth,
	) error {
		return servePreparedWorker(serveCtx, prepared, preparedHealth, policy)
	}, composition, health)
	if errors.Is(serveErr, errWorkerProcessQuarantined) {
		quarantined = true
		health.failRuntime()
		return errWorkerProcessQuarantined
	}
	if serveErr != nil {
		health.failRuntime()
		return errWorkerRuntimeFailed
	}
	if ctx.Err() == nil {
		health.failRuntime()
		return errWorkerRuntimeFailed
	}
	health.beginDrain()
	return nil
}

// runWorkerServeSafely catches panics at the injected runtime boundary and
// drops both raw errors and panic values. Either may contain provider output,
// SQL details or user content. The one exception is the stable quarantine
// classification: losing it would let lifecycle cleanup race detached work.
func runWorkerServeSafely(ctx context.Context,
	serve func(context.Context, *WorkerComposition, *workerRuntimeHealth) error,
	composition *WorkerComposition, health *workerRuntimeHealth,
) (resultErr error) {
	if ctx == nil || serve == nil || composition == nil || health == nil {
		return errWorkerRuntimeFailed
	}
	defer func() {
		if recover() != nil {
			resultErr = errWorkerRuntimeFailed
		}
	}()
	serveErr := serve(ctx, composition, health)
	switch {
	case serveErr == nil:
		return nil
	case errors.Is(serveErr, errWorkerProcessQuarantined),
		errors.Is(serveErr, agentturn.ErrWorkerRestartRequired):
		return errWorkerProcessQuarantined
	default:
		return errWorkerRuntimeFailed
	}
}

type workerLoopSet [workerLoopCount]func(context.Context) error

func compositionWorkerLoops(composition *WorkerComposition, health *workerRuntimeHealth) workerLoopSet {
	return workerLoopSet{
		workerExecutionLoop: func(ctx context.Context) error {
			return composition.worker.RunWithPulse(ctx, func(result agentturn.WorkerRunResult, err error) {
				logWorkerResult(result, err)
			}, func() { health.loopPulse(workerExecutionLoop) })
		},
		workerReconcileLoop: func(ctx context.Context) error {
			return composition.reconciler.RunWithPulse(ctx, func(report agentturn.ReconcileReport, err error) {
				if err != nil {
					log.Printf("agent-worker: reconcile pass failed reason=runtime_error")
					return
				}
				if len(report.Retired) > 0 || len(report.Failures) > 0 {
					log.Printf("agent-worker: reconciled retired=%d skipped=%d failed=%d",
						len(report.Retired), report.Skipped, len(report.Failures))
				}
			}, func() { health.loopPulse(workerReconcileLoop) })
		},
		workerDispatchLoop: func(ctx context.Context) error {
			return composition.dispatcher.RunWithPulse(ctx, func(report agentturn.EffectDispatchReport, err error) {
				if err != nil {
					log.Printf("agent-worker: dispatch pass failed reason=runtime_error")
					return
				}
				if report.Claimed > 0 {
					log.Printf("agent-worker: dispatched delivered=%d retried=%d dead=%d failed=%d",
						report.Delivered, report.Retried, report.DeadLettered, len(report.Failures))
				}
			}, func() { health.loopPulse(workerDispatchLoop) })
		},
	}
}

// servePreparedWorker starts the recurring dependency probe and the three
// closed-set loops. Static composition and the startup probe must already have
// passed. The first recurring probe failure is a one-way admission latch: it
// removes readiness before cancelling the loop contexts, so no later Turn or
// Effect Claim can start. Worker.RunWithPulse keeps the Turn it already owns on
// its independent, bounded drain context and continues fencing heartbeats until
// that Turn commits or the drain deadline expires. A replacement process must
// pass startup readiness again; this process never talks itself back into
// serving after a runtime dependency failure.
//
// On a clean caller cancellation this layer leaves health in draining; the
// lifecycle owner closes composition resources before publishing stopped.
func servePreparedWorker(ctx context.Context, composition *WorkerComposition, health *workerRuntimeHealth, policy workerProbePolicy) error {
	policy = policy.normalized()
	if ctx == nil || !workerProductionCompositionReady(composition) || health == nil ||
		!health.matchesAdmissionGate(workerCompositionAdmissionGate(composition)) ||
		!health.preparedForLoops() {
		return errWorkerDependenciesUnavailable
	}

	serveCtx, cancelServe := context.WithCancel(ctx)
	loopsServing := make(chan struct{})
	loopsDone := make(chan error, 1)
	go func() {
		loopsDone <- superviseWorkerLoopsStarted(
			serveCtx,
			compositionWorkerLoops(composition, health),
			health,
			policy.ShutdownTimeout,
			loopsServing,
		)
	}()
	select {
	case <-loopsServing:
		// All three loops have crossed the closed-set start barrier. Starting
		// recurring probes only now prevents an immediate failed check from
		// moving Health to draining before the supervisor can start the loops.
	case err := <-loopsDone:
		cancelServe()
		return err
	}

	probeDone := make(chan workerProbeOutcome, 1)
	go func() {
		outcome := monitorWorkerRuntimeProbe(serveCtx, composition.probe, health, policy)
		if outcome == workerProbeFailed || outcome == workerProbeTimedOut ||
			outcome == workerProbeDetached {
			// beginReadinessLossDrain ran inside the monitor before this
			// cancellation, so /readyz cannot race a newly cancelled Claim loop
			// and still report ready.
			cancelServe()
		}
		probeDone <- outcome
	}()

	readinessLost := false
	var err error
	select {
	case err = <-loopsDone:
	case <-health.readinessLossSignal():
		readinessLost = true
		cancelServe()
		err = <-loopsDone
	}
	cancelServe()
	probeOutcome := <-probeDone
	select {
	case <-health.readinessLossSignal():
		readinessLost = true
	default:
	}
	if errors.Is(err, errWorkerProcessQuarantined) || probeOutcome == workerProbeDetached {
		return errWorkerProcessQuarantined
	}
	if readinessLost || (err == nil && probeOutcome == workerProbeFailed) {
		return errWorkerReadinessLost
	}
	return err
}

// superviseWorkerLoops makes lifecycle ordering explicit: shutdown first
// revokes readiness, then cancels the loops; an unexpected return first marks
// the process failed, then cancels its siblings. The supervisor owns loops,
// not composition resources, so successful completion remains draining until
// run or Serve finishes cleanup. This also fixes the previous behavior where
// one dead loop could leave wait.Wait blocked on the others.
func superviseWorkerLoops(ctx context.Context, loops workerLoopSet, health *workerRuntimeHealth, shutdownTimeout time.Duration) error {
	return superviseWorkerLoopsStarted(ctx, loops, health, shutdownTimeout, nil)
}

// superviseWorkerLoopsStarted is superviseWorkerLoops plus an optional
// one-shot start barrier owned by the caller. It closes serving only after all
// closed-set loops registered with Health and their shared start gate opened.
// Validation failures return without closing it, so callers must also select
// the supervisor result while waiting.
func superviseWorkerLoopsStarted(
	ctx context.Context,
	loops workerLoopSet,
	health *workerRuntimeHealth,
	shutdownTimeout time.Duration,
	serving chan<- struct{},
) error {
	if ctx == nil || health == nil || !health.preparedForLoops() {
		return errWorkerDependenciesUnavailable
	}
	if shutdownTimeout <= 0 {
		shutdownTimeout = defaultWorkerShutdownTimeout
	}
	for _, loop := range loops {
		if loop == nil {
			health.failRuntime()
			return errWorkerLoopFailed
		}
	}
	if ctx.Err() != nil {
		health.beginDrain()
		return nil
	}

	serveCtx, cancelLoops := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelLoops()
	stopRequested := make(chan struct{})
	var stopOnce sync.Once
	requestStop := func() {
		stopOnce.Do(func() {
			close(stopRequested)
			cancelLoops()
		})
	}
	supervisorDone := make(chan struct{})
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-ctx.Done():
			// This write happens before loop cancellation, so /readyz cannot
			// race a draining Worker and report ready.
			health.beginDrain()
			requestStop()
		case <-supervisorDone:
		}
	}()

	var wait sync.WaitGroup
	var started sync.WaitGroup
	startGate := make(chan struct{})
	failures := make(chan struct{}, workerLoopCount)
	quarantines := make(chan struct{}, 1)
	for id, loop := range loops {
		loopID := workerLoop(id)
		runLoop := loop
		wait.Add(1)
		started.Add(1)
		go func() {
			health.loopStarted(loopID)
			started.Done()
			<-startGate
			defer wait.Done()
			loopErr := runWorkerLoopSafely(serveCtx, runLoop)
			expected := serveCtx.Err() != nil
			health.loopExited(loopID, expected)
			if errors.Is(loopErr, agentturn.ErrWorkerRestartRequired) ||
				errors.Is(loopErr, errWorkerProcessQuarantined) {
				select {
				case quarantines <- struct{}{}:
				default:
				}
				requestStop()
				return
			}
			if !expected {
				failures <- struct{}{}
				requestStop()
			}
		}()
	}
	started.Wait()
	close(startGate)
	if serving != nil {
		close(serving)
	}
	waitDone := make(chan struct{})
	go func() {
		wait.Wait()
		close(waitDone)
	}()
	timedOut := false
	select {
	case <-waitDone:
	case <-stopRequested:
		timer := time.NewTimer(shutdownTimeout)
		select {
		case <-waitDone:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
			timedOut = true
			health.failShutdownTimeout()
		}
	}
	close(supervisorDone)
	<-watcherDone

	if timedOut || len(quarantines) > 0 {
		return errWorkerProcessQuarantined
	}
	if len(failures) > 0 {
		return errWorkerLoopFailed
	}
	return nil
}

// runWorkerLoopSafely treats a panic exactly like any other unexpected loop
// exit and intentionally discards the panic value. Panic text and ordinary
// errors can contain provider responses, SQL details or user content and must
// not cross the process boundary. ErrWorkerRestartRequired is retained because
// it is a stable kernel classification that forbids composition cleanup.
func runWorkerLoopSafely(ctx context.Context, loop func(context.Context) error) (resultErr error) {
	if ctx == nil || loop == nil {
		return errWorkerLoopFailed
	}
	defer func() {
		if recover() != nil {
			resultErr = errWorkerLoopFailed
		}
	}()
	loopErr := loop(ctx)
	switch {
	case loopErr == nil:
		return nil
	case errors.Is(loopErr, agentturn.ErrWorkerRestartRequired):
		return agentturn.ErrWorkerRestartRequired
	case errors.Is(loopErr, errWorkerProcessQuarantined):
		return errWorkerProcessQuarantined
	default:
		return errWorkerLoopFailed
	}
}

// logWorkerResult records outcomes without leaking Turn content. Only
// identities, states and counts are logged; event payloads never are.
func logWorkerResult(result agentturn.WorkerRunResult, err error) {
	switch {
	case errors.Is(err, agentturn.ErrNoClaimableTurn):
		return
	case errors.Is(err, agentturn.ErrWorkerFenceLost):
		log.Printf("agent-worker: turn=%s attempt=%s lost its fence and committed nothing",
			result.TurnID, result.AttemptID)
	case err != nil:
		log.Printf("agent-worker: turn=%s attempt=%s failed reason=runtime_error",
			result.TurnID, result.AttemptID)
	default:
		log.Printf("agent-worker: turn=%s attempt=%s status=%s operations=%d cancelled=%t",
			result.TurnID, result.AttemptID, result.TerminalStatus, result.OperationsEmit, result.Cancelled)
	}
}

// SignalContext cancels on SIGINT or SIGTERM so a deploy rollout drains rather
// than killing in-flight Turns.
func SignalContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
}
