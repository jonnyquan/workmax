package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"server/service/agentturn"
)

type lockedTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func newLockedTestClock() *lockedTestClock {
	return &lockedTestClock{now: time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)}
}

func (clock *lockedTestClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *lockedTestClock) Advance(delta time.Duration) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = clock.now.Add(delta)
}

func prepareRuntimeHealth(t *testing.T, clock *lockedTestClock, freshness time.Duration) *workerRuntimeHealth {
	t.Helper()
	health := newWorkerRuntimeHealth(clock.Now, freshness, freshness)
	if !health.markCompositionReady() || !health.recordStartupProbe(true) {
		t.Fatal("failed to prepare runtime health")
	}
	return health
}

func startEveryWorkerLoop(t *testing.T, health *workerRuntimeHealth) {
	t.Helper()
	for loop := workerExecutionLoop; loop < workerLoopCount; loop++ {
		if !health.loopStarted(loop) {
			t.Fatalf("loopStarted(%d) was rejected", loop)
		}
		if !health.loopPulse(loop) {
			t.Fatalf("loopPulse(%d) was rejected", loop)
		}
	}
}

func containsReason(snapshot workerHealthSnapshot, reason workerHealthReason) bool {
	for _, candidate := range snapshot.Reasons {
		if candidate == string(reason) {
			return true
		}
	}
	return false
}

func waitForHealth(t *testing.T, health *workerRuntimeHealth, predicate func(workerHealthSnapshot) bool) workerHealthSnapshot {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		snapshot := health.Snapshot()
		if predicate(snapshot) {
			return snapshot
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("health predicate not reached; last snapshot = %+v", snapshot)
		}
	}
}

func waitForProbeObservationAt(t *testing.T, health *workerRuntimeHealth, want time.Time) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		health.mu.RLock()
		observedAt := health.probeObservedAt
		health.mu.RUnlock()
		if observedAt.Equal(want) {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("probe observation = %s, want %s", observedAt, want)
		}
	}
}

func TestRuntimeHealthLifecycleAndProbeFreshness(t *testing.T) {
	clock := newLockedTestClock()
	const freshness = 15 * time.Second
	health := newWorkerRuntimeHealth(clock.Now, freshness, freshness)

	initial := health.Snapshot()
	if initial.Phase != string(workerPhaseStarting) || !initial.Live || initial.Ready {
		t.Fatalf("initial health = %+v", initial)
	}
	wantInitialReasons := []string{
		string(reasonCompositionPending), string(reasonStartupProbePending), string(reasonLoopsStarting),
	}
	if !reflect.DeepEqual(initial.Reasons, wantInitialReasons) {
		t.Fatalf("initial reasons = %v, want %v", initial.Reasons, wantInitialReasons)
	}

	if !health.markCompositionReady() || !health.recordStartupProbe(true) {
		t.Fatal("composition/startup transition was rejected")
	}
	if !health.loopStarted(workerExecutionLoop) || !health.loopStarted(workerReconcileLoop) {
		t.Fatal("valid loop start was rejected")
	}
	if snapshot := health.Snapshot(); snapshot.Ready || !containsReason(snapshot, reasonLoopsStarting) {
		t.Fatalf("partially started health = %+v", snapshot)
	}
	if health.loopStarted(workerLoop(99)) {
		t.Fatal("unknown loop satisfied the closed-set barrier")
	}
	if !health.loopStarted(workerDispatchLoop) {
		t.Fatal("dispatch loop start was rejected")
	}
	for loop := workerExecutionLoop; loop < workerLoopCount; loop++ {
		if !health.loopPulse(loop) {
			t.Fatalf("loop pulse %d was rejected", loop)
		}
	}
	if snapshot := health.Snapshot(); snapshot.Phase != string(workerPhaseServing) || !snapshot.Live || !snapshot.Ready || len(snapshot.Reasons) != 0 {
		t.Fatalf("serving health = %+v", snapshot)
	}

	clock.Advance(freshness + time.Nanosecond)
	if snapshot := health.Snapshot(); !snapshot.Live || snapshot.Ready || !containsReason(snapshot, reasonDependencyProbeStale) {
		t.Fatalf("stale dependency health = %+v", snapshot)
	}
	if !health.recordRuntimeProbeSuccess() {
		t.Fatal("fresh dependency recheck was rejected")
	}
	for loop := workerExecutionLoop; loop < workerLoopCount; loop++ {
		health.loopPulse(loop)
	}
	if !health.Snapshot().Ready {
		t.Fatal("fresh dependency recheck did not restore readiness")
	}

	if !health.beginDrain() {
		t.Fatal("beginDrain was rejected")
	}
	if health.recordRuntimeProbeSuccess() || health.loopStarted(workerExecutionLoop) {
		t.Fatal("draining health accepted a transition back toward readiness")
	}
	for loop := workerExecutionLoop; loop < workerLoopCount; loop++ {
		health.loopExited(loop, true)
	}
	if snapshot := health.Snapshot(); snapshot.Phase != string(workerPhaseDraining) || !snapshot.Live || snapshot.Ready || !containsReason(snapshot, reasonDraining) {
		t.Fatalf("draining health = %+v", snapshot)
	}
	health.stop()
	if snapshot := health.Snapshot(); snapshot.Phase != string(workerPhaseStopped) || snapshot.Live || snapshot.Ready || !containsReason(snapshot, reasonStopped) {
		t.Fatalf("stopped health = %+v", snapshot)
	}
}

func TestRuntimeHealthFailsClosedOnStartupOrLoopFailure(t *testing.T) {
	clock := newLockedTestClock()
	health := newWorkerRuntimeHealth(clock.Now, time.Minute, time.Minute)
	health.markCompositionReady()
	if !health.recordStartupProbe(false) {
		t.Fatal("startup failure was not recorded")
	}
	if snapshot := health.Snapshot(); snapshot.Live || snapshot.Ready || snapshot.Phase != string(workerPhaseFailed) ||
		!reflect.DeepEqual(snapshot.Reasons, []string{string(reasonStartupProbeFailed)}) {
		t.Fatalf("startup-failed health = %+v", snapshot)
	}
	if health.recordRuntimeProbeSuccess() || health.beginDrain() {
		t.Fatal("failed startup recovered through a later transition")
	}

	health = prepareRuntimeHealth(t, clock, time.Minute)
	startEveryWorkerLoop(t, health)
	health.loopExited(workerReconcileLoop, false)
	if snapshot := health.Snapshot(); snapshot.Live || snapshot.Ready || snapshot.Phase != string(workerPhaseFailed) ||
		!reflect.DeepEqual(snapshot.Reasons, []string{string(reasonLoopExited)}) {
		t.Fatalf("loop-failed health = %+v", snapshot)
	}
	health.stop()
	if snapshot := health.Snapshot(); snapshot.Phase != string(workerPhaseFailed) {
		t.Fatalf("failed health was hidden by Stop: %+v", snapshot)
	}
}

func TestRuntimeHealthSnapshotIsCopySafeAndClockRollbackIsStale(t *testing.T) {
	clock := newLockedTestClock()
	health := prepareRuntimeHealth(t, clock, time.Minute)
	startEveryWorkerLoop(t, health)

	snapshot := health.Snapshot()
	snapshot.Reasons = append(snapshot.Reasons, "SECRET_SHOULD_NOT_PERSIST")
	if next := health.Snapshot(); len(next.Reasons) != 0 || !next.Ready {
		t.Fatalf("caller mutated health state through snapshot: %+v", next)
	}

	clock.Advance(-time.Second)
	if next := health.Snapshot(); next.Ready || !containsReason(next, reasonDependencyProbeStale) {
		t.Fatalf("clock rollback did not fail closed: %+v", next)
	}
}

func TestWorkerProbeReducesErrorsAndPanicsToStableHealth(t *testing.T) {
	for name, probe := range map[string]RuntimeProbe{
		"error": runtimeProbeFuncs{startup: func(context.Context) error {
			return errors.New("mysql://worker:SECRET@db.internal")
		}},
		"panic": runtimeProbeFuncs{startup: func(context.Context) error {
			panic("provider response SECRET_PROVIDER_VALUE")
		}},
	} {
		t.Run(name, func(t *testing.T) {
			clock := newLockedTestClock()
			health := newWorkerRuntimeHealth(clock.Now, time.Minute, time.Minute)
			health.markCompositionReady()
			if outcome := runWorkerStartupProbe(context.Background(), probe, health, time.Second); outcome == workerProbeSucceeded {
				t.Fatal("unsafe probe result was accepted")
			}
			snapshot := health.Snapshot()
			serialized := strings.Join(append([]string{snapshot.Phase}, snapshot.Reasons...), " ")
			for _, secret := range []string{"SECRET", "db.internal", "mysql://", "provider response"} {
				if strings.Contains(serialized, secret) {
					t.Fatalf("health leaked %q in %q", secret, serialized)
				}
			}
			if !reflect.DeepEqual(snapshot.Reasons, []string{string(reasonStartupProbeFailed)}) {
				t.Fatalf("probe failure reasons = %v", snapshot.Reasons)
			}
		})
	}
}

func TestProbeHardTimeoutBoundsANonCooperativeImplementation(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	startedAt := time.Now()
	outcome := executeWorkerProbe(context.Background(), 20*time.Millisecond, func(context.Context) error {
		defer close(finished)
		close(entered)
		<-release // deliberately ignores its context
		return errors.New("late SECRET_PROBE_RESULT")
	})
	if outcome != workerProbeDetached {
		t.Fatalf("probe outcome = %d, want detached", outcome)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("non-cooperative probe blocked for %s", elapsed)
	}
	select {
	case <-entered:
	default:
		t.Fatal("probe was not invoked")
	}
	close(release)
	<-finished
}

func TestProbeDeadlineWaitsForCooperativeQuiescence(t *testing.T) {
	finished := make(chan struct{})
	outcome := executeWorkerProbeWithGrace(
		context.Background(),
		20*time.Millisecond,
		20*time.Millisecond,
		func(ctx context.Context) error {
			defer close(finished)
			<-ctx.Done()
			return ctx.Err()
		},
	)
	if outcome != workerProbeTimedOut {
		t.Fatalf("probe outcome = %d, want quiesced deadline", outcome)
	}
	select {
	case <-finished:
	default:
		t.Fatal("timed-out classification preceded probe quiescence")
	}
}

func TestProbeClassificationUsesCompletionBoundaryNotReceiverSchedule(t *testing.T) {
	deadline := time.Now().Add(time.Minute)
	before := deadline.Add(-time.Nanosecond)
	after := deadline.Add(time.Nanosecond)
	for _, testCase := range []struct {
		name               string
		parent             context.Context
		parentOwnsDeadline bool
		result             workerProbeCallResult
		want               workerProbeOutcome
	}{
		{
			name:   "success completed before boundary",
			parent: context.Background(), result: workerProbeCallResult{
				succeeded: true, completedAt: before,
			}, want: workerProbeSucceeded,
		},
		{
			name:   "failure completed before boundary",
			parent: context.Background(), result: workerProbeCallResult{
				completedAt: before,
			}, want: workerProbeFailed,
		},
		{
			name:   "completion after owned timeout",
			parent: context.Background(), result: workerProbeCallResult{
				succeeded: true, completedAt: after,
			}, want: workerProbeTimedOut,
		},
		{
			name:   "completion after parent deadline",
			parent: context.Background(), parentOwnsDeadline: true, result: workerProbeCallResult{
				succeeded: true, completedAt: after,
			}, want: workerProbeCanceled,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := classifyQuiescedWorkerProbe(
				testCase.parent, deadline, testCase.parentOwnsDeadline, testCase.result,
			); got != testCase.want {
				t.Fatalf("classification = %d, want %d", got, testCase.want)
			}
		})
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if got := classifyQuiescedWorkerProbe(canceled, deadline, false, workerProbeCallResult{
		succeeded: true, completedAt: before,
	}); got != workerProbeCanceled {
		t.Fatalf("caller-canceled classification = %d, want canceled", got)
	}
}

func TestStartupProbeCancellationDoesNotMasqueradeAsDependencyFailure(t *testing.T) {
	clock := newLockedTestClock()
	health := newWorkerRuntimeHealth(clock.Now, time.Minute, time.Minute)
	health.markCompositionReady()
	entered := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	probe := runtimeProbeFuncs{startup: func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}}
	done := make(chan workerProbeOutcome, 1)
	go func() { done <- runWorkerStartupProbe(ctx, probe, health, time.Second) }()
	<-entered
	cancel()
	if outcome := <-done; outcome != workerProbeCanceled {
		t.Fatalf("probe outcome = %d, want canceled", outcome)
	}
	snapshot := health.Snapshot()
	if snapshot.Phase != string(workerPhaseStarting) || snapshot.Ready ||
		!containsReason(snapshot, reasonStartupProbePending) || containsReason(snapshot, reasonStartupProbeFailed) {
		t.Fatalf("health after startup cancellation = %+v", snapshot)
	}
}

func TestLoopPulseFreshnessExpiresWithoutBusinessTrafficDependency(t *testing.T) {
	clock := newLockedTestClock()
	health := newWorkerRuntimeHealth(clock.Now, 10*time.Minute, time.Minute)
	health.markCompositionReady()
	health.recordStartupProbe(true)
	startEveryWorkerLoop(t, health)
	if !health.Snapshot().Ready {
		t.Fatal("fresh loop pulses did not produce readiness")
	}
	clock.Advance(time.Minute + time.Nanosecond)
	if snapshot := health.Snapshot(); snapshot.Ready || !containsReason(snapshot, reasonLoopPulseStale) ||
		containsReason(snapshot, reasonDependencyProbeStale) {
		t.Fatalf("stale loop pulse health = %+v", snapshot)
	}
	for loop := workerExecutionLoop; loop < workerLoopCount; loop++ {
		health.loopPulse(loop)
	}
	if !health.Snapshot().Ready {
		t.Fatal("fresh loop pulses did not restore readiness")
	}
}

func TestHealthSnapshotClosesAdmissionBeforePublishingFreshnessLoss(t *testing.T) {
	for name, testCase := range map[string]struct {
		probeFreshness time.Duration
		loopFreshness  time.Duration
		wantReason     workerHealthReason
	}{
		"dependency probe": {
			probeFreshness: time.Minute,
			loopFreshness:  10 * time.Minute,
			wantReason:     reasonDependencyProbeStale,
		},
		"loop pulse": {
			probeFreshness: 10 * time.Minute,
			loopFreshness:  time.Minute,
			wantReason:     reasonLoopPulseStale,
		},
	} {
		t.Run(name, func(t *testing.T) {
			clock := newLockedTestClock()
			gate := agentturn.NewAdmissionGate()
			health := newWorkerRuntimeHealth(
				clock.Now, testCase.probeFreshness, testCase.loopFreshness,
			)
			if !health.bindAdmissionGate(gate) || !health.markCompositionReady() ||
				!health.recordStartupProbe(true) {
				t.Fatal("failed to prepare admission-bound Health")
			}
			startEveryWorkerLoop(t, health)
			if snapshot := health.Snapshot(); !snapshot.Ready || !gate.Open() {
				t.Fatalf("initial Health/Gate = %+v/%t, want ready/open", snapshot, gate.Open())
			}

			clock.Advance(time.Minute + time.Nanosecond)
			snapshot := health.Snapshot()
			if snapshot.Ready || snapshot.Phase != string(workerPhaseDraining) ||
				!containsReason(snapshot, reasonDraining) ||
				!containsReason(snapshot, testCase.wantReason) {
				t.Fatalf("freshness-loss snapshot = %+v", snapshot)
			}
			if gate.Open() || !errors.Is(gate.Acquire(), agentturn.ErrAdmissionClosed) {
				t.Fatal("Snapshot published freshness loss before closing AdmissionGate")
			}
			select {
			case <-health.readinessLossSignal():
			default:
				t.Fatal("freshness loss did not signal the serving supervisor")
			}
			if health.recordRuntimeProbeSuccess() || gate.Open() {
				t.Fatal("late probe success healed a one-way admission loss")
			}
		})
	}
}

func TestRuntimeProbeMonitorLatchesFailureAndRequiresRestart(t *testing.T) {
	clock := newLockedTestClock()
	health := prepareRuntimeHealth(t, clock, time.Minute)
	startEveryWorkerLoop(t, health)

	checks := make(chan error, 1)
	var checkCalls atomic.Int64
	probe := runtimeProbeFuncs{check: func(context.Context) error {
		checkCalls.Add(1)
		return <-checks
	}}
	ticks := make(chan time.Time, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan workerProbeOutcome, 1)
	go func() {
		done <- monitorWorkerRuntimeProbeTicks(ctx, probe, health, time.Second, ticks)
	}()

	checks <- errors.New("SECRET_DB_DRIVER_ERROR")
	ticks <- clock.Now()
	if outcome := <-done; outcome != workerProbeFailed {
		t.Fatalf("monitor outcome = %d, want failed", outcome)
	}
	snapshot := health.Snapshot()
	if snapshot.Phase != string(workerPhaseDraining) || !snapshot.Live || snapshot.Ready ||
		!containsReason(snapshot, reasonDraining) ||
		!containsReason(snapshot, reasonDependencyProbeFailed) {
		t.Fatalf("latched dependency health = %+v", snapshot)
	}
	if health.recordRuntimeProbeSuccess() {
		t.Fatal("draining runtime accepted an in-process readiness recovery")
	}
	ticks <- clock.Now()
	if calls := checkCalls.Load(); calls != 1 {
		t.Fatalf("checks after latch = %d, want 1", calls)
	}
}

func TestRuntimeProbeFailureRaceWithCallerCancellationPrefersCleanDrain(t *testing.T) {
	clock := newLockedTestClock()
	health := prepareRuntimeHealth(t, clock, time.Minute)
	startEveryWorkerLoop(t, health)
	entered := make(chan struct{})
	release := make(chan struct{})
	probe := runtimeProbeFuncs{check: func(ctx context.Context) error {
		close(entered)
		<-release
		return errors.New("SECRET_LATE_PROBE_FAILURE")
	}}
	ticks := make(chan time.Time, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan workerProbeOutcome, 1)
	go func() {
		done <- monitorWorkerRuntimeProbeTicks(ctx, probe, health, time.Second, ticks)
	}()
	ticks <- clock.Now()
	<-entered
	cancel()
	close(release)
	if outcome := <-done; outcome != workerProbeCanceled {
		t.Fatalf("monitor outcome = %d, want caller cancellation", outcome)
	}
	if snapshot := health.Snapshot(); snapshot.Phase != string(workerPhaseServing) ||
		containsReason(snapshot, reasonDependencyProbeFailed) {
		t.Fatalf("caller cancellation was misclassified as dependency loss: %+v", snapshot)
	}
}

func TestRuntimeProbeMonitorTimeoutLatchesDrainAndQuarantinesLateCheck(t *testing.T) {
	clock := newLockedTestClock()
	health := prepareRuntimeHealth(t, clock, time.Minute)
	startEveryWorkerLoop(t, health)
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	probe := runtimeProbeFuncs{check: func(context.Context) error {
		defer close(finished)
		close(entered)
		<-release // deliberately violates the Context contract
		return nil
	}}
	ticks := make(chan time.Time, 1)
	done := make(chan workerProbeOutcome, 1)
	go func() {
		done <- monitorWorkerRuntimeProbeTicks(
			context.Background(), probe, health, 20*time.Millisecond, ticks,
		)
	}()
	ticks <- clock.Now()
	<-entered
	select {
	case outcome := <-done:
		if outcome != workerProbeDetached {
			t.Fatalf("monitor outcome = %d, want detached", outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("non-cooperative recurring probe blocked the monitor")
	}
	if snapshot := health.Snapshot(); snapshot.Phase != string(workerPhaseDraining) ||
		!snapshot.Live || snapshot.Ready ||
		!containsReason(snapshot, reasonDependencyProbeFailed) {
		t.Fatalf("timed-out recurring probe health = %+v", snapshot)
	}
	close(release)
	<-finished
}

func TestRuntimeProbeMonitorTurnsDerivedFreshnessLossIntoAdmissionDrain(t *testing.T) {
	for name, testCase := range map[string]struct {
		probeFreshness time.Duration
		loopFreshness  time.Duration
		advance        time.Duration
		wantReason     workerHealthReason
	}{
		"dependency probe stale": {
			probeFreshness: time.Minute,
			loopFreshness:  10 * time.Minute,
			advance:        time.Minute + time.Nanosecond,
			wantReason:     reasonDependencyProbeStale,
		},
		"loop pulse stale": {
			probeFreshness: 10 * time.Minute,
			loopFreshness:  time.Minute,
			advance:        time.Minute + time.Nanosecond,
			wantReason:     reasonLoopPulseStale,
		},
	} {
		t.Run(name, func(t *testing.T) {
			clock := newLockedTestClock()
			health := newWorkerRuntimeHealth(
				clock.Now, testCase.probeFreshness, testCase.loopFreshness,
			)
			if !health.markCompositionReady() || !health.recordStartupProbe(true) {
				t.Fatal("failed to prepare runtime health")
			}
			startEveryWorkerLoop(t, health)
			var checks atomic.Int64
			checkDone := make(chan struct{}, 1)
			probe := runtimeProbeFuncs{check: func(context.Context) error {
				checks.Add(1)
				checkDone <- struct{}{}
				return nil
			}}
			ticks := make(chan time.Time, 2)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan workerProbeOutcome, 1)
			go func() {
				done <- monitorWorkerRuntimeProbeTicks(ctx, probe, health, time.Second, ticks)
			}()

			// The first healthy cycle arms the one-way controller. Initial
			// loop-pulse pending state must never be confused with readiness
			// that was reached and subsequently lost.
			clock.Advance(time.Nanosecond)
			refreshedAt := clock.Now()
			ticks <- clock.Now()
			<-checkDone
			waitForProbeObservationAt(t, health, refreshedAt)
			clock.Advance(testCase.advance)
			ticks <- clock.Now()
			if outcome := <-done; outcome != workerProbeFailed {
				t.Fatalf("monitor outcome = %d, want derived readiness loss", outcome)
			}
			if calls := checks.Load(); calls != 1 {
				t.Fatalf("checks = %d, want stale snapshot to drain before a second Check", calls)
			}
			snapshot := health.Snapshot()
			if snapshot.Phase != string(workerPhaseDraining) || !snapshot.Live || snapshot.Ready ||
				!containsReason(snapshot, reasonDraining) ||
				!containsReason(snapshot, testCase.wantReason) {
				t.Fatalf("freshness-loss health = %+v", snapshot)
			}
		})
	}
}

func TestRuntimeProbeMonitorCancellationQuarantinesStuckCheckAfterGrace(t *testing.T) {
	clock := newLockedTestClock()
	health := prepareRuntimeHealth(t, clock, time.Minute)
	startEveryWorkerLoop(t, health)
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	probe := runtimeProbeFuncs{check: func(context.Context) error {
		defer close(finished)
		close(entered)
		<-release // deliberately ignores cancellation
		return nil
	}}
	ticks := make(chan time.Time, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan workerProbeOutcome, 1)
	go func() {
		done <- monitorWorkerRuntimeProbeTicksWithGrace(
			ctx, probe, health, time.Hour, 20*time.Millisecond, ticks,
		)
	}()
	ticks <- clock.Now()
	<-entered
	cancel()
	select {
	case outcome := <-done:
		if outcome != workerProbeDetached {
			t.Fatalf("monitor outcome = %d, want detached", outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime probe monitor blocked shutdown")
	}
	close(release)
	<-finished
}

func TestSupervisorPublishesClosedAdmissionBeforeCleanLoopCancellation(t *testing.T) {
	clock := newLockedTestClock()
	gate := agentturn.NewAdmissionGate()
	health := newWorkerRuntimeHealth(clock.Now, time.Minute, time.Minute)
	if !health.bindAdmissionGate(gate) || !health.markCompositionReady() ||
		!health.recordStartupProbe(true) {
		t.Fatal("failed to prepare admission-bound Health")
	}
	type observation struct {
		gateOpen bool
		phase    string
	}
	observed := make(chan observation, workerLoopCount)
	loops := workerLoopSet{}
	for loop := workerExecutionLoop; loop < workerLoopCount; loop++ {
		loopID := loop
		loops[loop] = func(ctx context.Context) error {
			health.loopPulse(loopID)
			<-ctx.Done()
			observed <- observation{
				gateOpen: gate.Open(),
				phase:    health.Snapshot().Phase,
			}
			return ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- superviseWorkerLoops(ctx, loops, health, time.Second) }()
	waitForHealth(t, health, func(snapshot workerHealthSnapshot) bool { return snapshot.Ready })
	cancel()
	for index := 0; index < int(workerLoopCount); index++ {
		select {
		case got := <-observed:
			if got.gateOpen || got.phase != string(workerPhaseDraining) {
				t.Fatalf("loop observed cancellation before admission drain: %+v", got)
			}
		case <-time.After(time.Second):
			t.Fatal("loop did not observe clean cancellation")
		}
	}
	if err := <-done; err != nil {
		t.Fatalf("superviseWorkerLoops(): %v", err)
	}
}

func TestSupervisorPublishesClosedAdmissionBeforeSiblingFailureCancellation(t *testing.T) {
	clock := newLockedTestClock()
	gate := agentturn.NewAdmissionGate()
	health := newWorkerRuntimeHealth(clock.Now, time.Minute, time.Minute)
	if !health.bindAdmissionGate(gate) || !health.markCompositionReady() ||
		!health.recordStartupProbe(true) {
		t.Fatal("failed to prepare admission-bound Health")
	}
	fail := make(chan struct{})
	observed := make(chan workerHealthSnapshot, 2)
	loops := workerLoopSet{
		workerExecutionLoop: func(context.Context) error {
			health.loopPulse(workerExecutionLoop)
			<-fail
			return errors.New("SECRET_UNEXPECTED_LOOP_FAILURE")
		},
	}
	for loop := workerReconcileLoop; loop < workerLoopCount; loop++ {
		loopID := loop
		loops[loop] = func(ctx context.Context) error {
			health.loopPulse(loopID)
			<-ctx.Done()
			observed <- health.Snapshot()
			return ctx.Err()
		}
	}
	done := make(chan error, 1)
	go func() {
		done <- superviseWorkerLoops(context.Background(), loops, health, time.Second)
	}()
	waitForHealth(t, health, func(snapshot workerHealthSnapshot) bool { return snapshot.Ready })
	close(fail)
	for index := 0; index < 2; index++ {
		select {
		case snapshot := <-observed:
			if gate.Open() || snapshot.Phase != string(workerPhaseFailed) || snapshot.Ready {
				t.Fatalf("sibling observed failure cancellation before gate/Health: %+v", snapshot)
			}
		case <-time.After(time.Second):
			t.Fatal("sibling loop did not observe failure cancellation")
		}
	}
	if err := <-done; !errors.Is(err, errWorkerLoopFailed) {
		t.Fatalf("superviseWorkerLoops() error = %v, want loop failure", err)
	}
}

func TestSupervisorFailsFastAndCancelsSiblingLoops(t *testing.T) {
	clock := newLockedTestClock()
	health := prepareRuntimeHealth(t, clock, time.Minute)
	fail := make(chan struct{})
	siblingCancelled := make(chan struct{}, 2)
	loops := workerLoopSet{
		workerExecutionLoop: func(context.Context) error {
			health.loopPulse(workerExecutionLoop)
			<-fail
			return errors.New("SECRET_EXECUTION_FAILURE")
		},
		workerReconcileLoop: func(ctx context.Context) error {
			health.loopPulse(workerReconcileLoop)
			<-ctx.Done()
			siblingCancelled <- struct{}{}
			return ctx.Err()
		},
		workerDispatchLoop: func(ctx context.Context) error {
			health.loopPulse(workerDispatchLoop)
			<-ctx.Done()
			siblingCancelled <- struct{}{}
			return ctx.Err()
		},
	}
	done := make(chan error, 1)
	go func() { done <- superviseWorkerLoops(context.Background(), loops, health, time.Second) }()
	waitForHealth(t, health, func(snapshot workerHealthSnapshot) bool { return snapshot.Ready })
	close(fail)

	select {
	case err := <-done:
		if !errors.Is(err, errWorkerLoopFailed) || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("supervisor error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor did not return after a loop failed")
	}
	if len(siblingCancelled) != 2 {
		t.Fatalf("cancelled siblings = %d, want 2", len(siblingCancelled))
	}
	if snapshot := health.Snapshot(); snapshot.Live || snapshot.Ready ||
		!containsReason(snapshot, reasonLoopExited) {
		t.Fatalf("health after loop failure = %+v", snapshot)
	}
}

func TestSupervisorPropagatesKernelRestartAsSecretFreeQuarantine(t *testing.T) {
	clock := newLockedTestClock()
	health := prepareRuntimeHealth(t, clock, time.Minute)
	siblingCancelled := make(chan struct{}, 2)
	loops := workerLoopSet{
		workerExecutionLoop: func(context.Context) error {
			health.loopPulse(workerExecutionLoop)
			return errors.Join(agentturn.ErrWorkerRestartRequired,
				errors.New("SECRET_EXECUTOR_OR_EMIT_DETAIL"))
		},
		workerReconcileLoop: func(ctx context.Context) error {
			health.loopPulse(workerReconcileLoop)
			<-ctx.Done()
			siblingCancelled <- struct{}{}
			return ctx.Err()
		},
		workerDispatchLoop: func(ctx context.Context) error {
			health.loopPulse(workerDispatchLoop)
			<-ctx.Done()
			siblingCancelled <- struct{}{}
			return ctx.Err()
		},
	}

	err := superviseWorkerLoops(context.Background(), loops, health, time.Second)
	if !errors.Is(err, errWorkerProcessQuarantined) {
		t.Fatalf("supervisor error = %v, want process quarantine", err)
	}
	if strings.Contains(err.Error(), "SECRET_EXECUTOR_OR_EMIT_DETAIL") {
		t.Fatalf("supervisor leaked kernel detail: %q", err)
	}
	if got := len(siblingCancelled); got != 2 {
		t.Fatalf("cancelled siblings = %d, want 2", got)
	}
}

func TestSupervisorMarksDrainingBeforeCancellingLoops(t *testing.T) {
	clock := newLockedTestClock()
	health := prepareRuntimeHealth(t, clock, time.Minute)
	observed := make(chan workerHealthSnapshot, workerLoopCount)
	var loops workerLoopSet
	for loop := workerExecutionLoop; loop < workerLoopCount; loop++ {
		loopID := loop
		loops[loop] = func(ctx context.Context) error {
			health.loopPulse(loopID)
			<-ctx.Done()
			observed <- health.Snapshot()
			return ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- superviseWorkerLoops(ctx, loops, health, time.Second) }()
	waitForHealth(t, health, func(snapshot workerHealthSnapshot) bool { return snapshot.Ready })
	cancel()

	for loop := workerExecutionLoop; loop < workerLoopCount; loop++ {
		snapshot := <-observed
		if snapshot.Phase != string(workerPhaseDraining) || !snapshot.Live || snapshot.Ready {
			t.Fatalf("loop observed cancellation before drain transition: %+v", snapshot)
		}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("supervisor shutdown error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor did not complete shutdown")
	}
	if snapshot := health.Snapshot(); snapshot.Phase != string(workerPhaseDraining) || !snapshot.Live || snapshot.Ready {
		t.Fatalf("health after loop drain = %+v", snapshot)
	}
}

func TestSupervisorBoundsANonCooperativeDrain(t *testing.T) {
	clock := newLockedTestClock()
	health := prepareRuntimeHealth(t, clock, time.Minute)
	release := make(chan struct{})
	stubbornDone := make(chan struct{})
	var loops workerLoopSet
	for loop := workerExecutionLoop; loop < workerLoopCount; loop++ {
		loopID := loop
		if loop == workerExecutionLoop {
			loops[loop] = func(context.Context) error {
				defer close(stubbornDone)
				health.loopPulse(loopID)
				<-release // deliberately ignores cancellation
				return nil
			}
			continue
		}
		loops[loop] = func(ctx context.Context) error {
			health.loopPulse(loopID)
			<-ctx.Done()
			return ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- superviseWorkerLoops(ctx, loops, health, 20*time.Millisecond) }()
	waitForHealth(t, health, func(snapshot workerHealthSnapshot) bool { return snapshot.Ready })
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, errWorkerProcessQuarantined) {
			t.Fatalf("supervisor error = %v, want process quarantine", err)
		}
	case <-time.After(time.Second):
		t.Fatal("non-cooperative loop blocked supervisor shutdown")
	}
	if snapshot := health.Snapshot(); snapshot.Live || snapshot.Ready ||
		!containsReason(snapshot, reasonShutdownTimeout) {
		t.Fatalf("health after drain timeout = %+v", snapshot)
	}
	close(release)
	<-stubbornDone
}

func TestRuntimeHealthConcurrentSnapshotsRemainRaceFree(t *testing.T) {
	clock := newLockedTestClock()
	health := prepareRuntimeHealth(t, clock, time.Minute)
	startEveryWorkerLoop(t, health)
	var wait sync.WaitGroup
	for index := 0; index < 16; index++ {
		wait.Add(1)
		go func(seed int) {
			defer wait.Done()
			for iteration := 0; iteration < 200; iteration++ {
				if seed%2 == 0 {
					health.recordRuntimeProbeSuccess()
				} else {
					_ = health.Snapshot()
				}
			}
		}(index)
	}
	wait.Wait()
	var snapshots atomic.Int64
	for index := 0; index < 10; index++ {
		if health.Snapshot().Live {
			snapshots.Add(1)
		}
	}
	if snapshots.Load() != 10 {
		t.Fatal("concurrent probe observations corrupted lifecycle state")
	}
}
