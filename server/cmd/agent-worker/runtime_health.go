package main

import (
	"context"
	"sync"
	"time"

	"server/service/agentturn"
)

// RuntimeProbe attests that the dependencies installed in a Worker
// composition are working, not merely present. Startup may perform the more
// expensive read-only schema and adapter checks; Check is the bounded,
// recurring probe used while the loops are serving.
//
// Implementations must honor ctx and must not mutate schema or business data.
// Probe errors are deliberately reduced to a boolean at this process boundary
// so driver messages, provider responses and credentials never enter health
// state or the operator response.
type RuntimeProbe interface {
	Startup(context.Context) error
	Check(context.Context) error
}

type workerRuntimePhase string

const (
	workerPhaseStarting workerRuntimePhase = "starting"
	workerPhaseServing  workerRuntimePhase = "serving"
	workerPhaseDraining workerRuntimePhase = "draining"
	workerPhaseStopped  workerRuntimePhase = "stopped"
	workerPhaseFailed   workerRuntimePhase = "failed"
)

type workerLoop uint8

const (
	workerExecutionLoop workerLoop = iota
	workerReconcileLoop
	workerDispatchLoop
	workerLoopCount
)

const allWorkerLoops uint8 = (1 << workerLoopCount) - 1

func (loop workerLoop) valid() bool {
	return loop < workerLoopCount
}

func (loop workerLoop) bit() uint8 {
	return 1 << loop
}

type workerHealthReason string

const (
	reasonCompositionPending    workerHealthReason = "composition_pending"
	reasonStartupProbePending   workerHealthReason = "startup_probe_pending"
	reasonStartupProbeFailed    workerHealthReason = "startup_probe_failed"
	reasonLoopsStarting         workerHealthReason = "loops_starting"
	reasonDependencyProbeFailed workerHealthReason = "dependency_probe_failed"
	reasonDependencyProbeStale  workerHealthReason = "dependency_probe_stale"
	reasonLoopPulsePending      workerHealthReason = "loop_pulse_pending"
	reasonLoopPulseStale        workerHealthReason = "loop_pulse_stale"
	reasonLoopExited            workerHealthReason = "loop_exited"
	reasonRuntimeFailed         workerHealthReason = "runtime_failed"
	reasonShutdownTimeout       workerHealthReason = "shutdown_timeout"
	reasonResourceCloseFailed   workerHealthReason = "resource_close_failed"
	reasonResourceCloseTimeout  workerHealthReason = "resource_close_timeout"
	reasonDraining              workerHealthReason = "draining"
	reasonStopped               workerHealthReason = "stopped"
)

const (
	defaultWorkerProbeInterval        = 5 * time.Second
	defaultWorkerProbeTimeout         = 2 * time.Second
	defaultWorkerProbeStopGrace       = 250 * time.Millisecond
	defaultWorkerProbeFreshness       = 15 * time.Second
	defaultWorkerLoopFreshness        = 2 * time.Minute
	defaultWorkerBuildTimeout         = 30 * time.Second
	defaultWorkerShutdownTimeout      = 45 * time.Second
	defaultWorkerResourceCloseTimeout = 30 * time.Second
)

type workerProbePolicy struct {
	Interval             time.Duration
	Timeout              time.Duration
	StopGrace            time.Duration
	Freshness            time.Duration
	LoopFreshness        time.Duration
	BuildTimeout         time.Duration
	ShutdownTimeout      time.Duration
	ResourceCloseTimeout time.Duration
}

func defaultWorkerProbePolicy() workerProbePolicy {
	return workerProbePolicy{
		Interval:             defaultWorkerProbeInterval,
		Timeout:              defaultWorkerProbeTimeout,
		StopGrace:            defaultWorkerProbeStopGrace,
		Freshness:            defaultWorkerProbeFreshness,
		LoopFreshness:        defaultWorkerLoopFreshness,
		BuildTimeout:         defaultWorkerBuildTimeout,
		ShutdownTimeout:      defaultWorkerShutdownTimeout,
		ResourceCloseTimeout: defaultWorkerResourceCloseTimeout,
	}
}

func (policy workerProbePolicy) normalized() workerProbePolicy {
	defaults := defaultWorkerProbePolicy()
	if policy.Interval <= 0 {
		policy.Interval = defaults.Interval
	}
	if policy.Timeout <= 0 {
		policy.Timeout = defaults.Timeout
	}
	if policy.StopGrace <= 0 {
		policy.StopGrace = defaults.StopGrace
	}
	if policy.Freshness <= 0 {
		policy.Freshness = defaults.Freshness
	}
	if policy.LoopFreshness <= 0 {
		policy.LoopFreshness = defaults.LoopFreshness
	}
	if policy.BuildTimeout <= 0 {
		policy.BuildTimeout = defaults.BuildTimeout
	}
	if policy.ShutdownTimeout <= 0 {
		policy.ShutdownTimeout = defaults.ShutdownTimeout
	}
	if policy.StopGrace > policy.ShutdownTimeout {
		policy.StopGrace = policy.ShutdownTimeout
	}
	if policy.ResourceCloseTimeout <= 0 {
		policy.ResourceCloseTimeout = defaults.ResourceCloseTimeout
	}
	return policy
}

// workerHealthSnapshot is an immutable, secret-free view. It intentionally
// has no timestamps, configuration digest, process identity, Plugin identity
// or raw error field.
type workerHealthSnapshot struct {
	Phase   string
	Live    bool
	Ready   bool
	Reasons []string
}

// workerRuntimeHealth separates process lifecycle from readiness. A recurring
// dependency failure is one-way: the process remains live-but-not-ready only
// while its already-owned work drains, then exits for a fresh startup probe.
type workerRuntimeHealth struct {
	mu sync.RWMutex

	now           func() time.Time
	freshness     time.Duration
	loopFreshness time.Duration
	phase         workerRuntimePhase

	compositionReady    bool
	startupObserved     bool
	startupHealthy      bool
	probeObserved       bool
	probeHealthy        bool
	probeObservedAt     time.Time
	runningLoops        uint8
	loopPulseObserved   uint8
	loopPulseAt         [workerLoopCount]time.Time
	hardReason          workerHealthReason
	readinessLossReason workerHealthReason
	resourceCloseReason workerHealthReason
	admission           *agentturn.AdmissionGate
	admissionArmed      bool
	readinessLost       chan struct{}
	readinessSignaled   bool
}

func newWorkerRuntimeHealth(now func() time.Time, freshness, loopFreshness time.Duration) *workerRuntimeHealth {
	if now == nil {
		now = time.Now
	}
	if freshness <= 0 {
		freshness = defaultWorkerProbeFreshness
	}
	if loopFreshness <= 0 {
		loopFreshness = defaultWorkerLoopFreshness
	}
	return &workerRuntimeHealth{
		now:           now,
		freshness:     freshness,
		loopFreshness: loopFreshness,
		phase:         workerPhaseStarting,
		readinessLost: make(chan struct{}),
	}
}

// bindAdmissionGate attaches Health to the exact, seal-bound runtime gate
// before composition readiness can be published. Legacy unit-level health
// tests may leave it nil; a production composition may bind it only once.
func (health *workerRuntimeHealth) bindAdmissionGate(gate *agentturn.AdmissionGate) bool {
	if health == nil || gate == nil || !gate.Open() {
		return false
	}
	health.mu.Lock()
	defer health.mu.Unlock()
	if health.phase != workerPhaseStarting || health.compositionReady ||
		health.admission != nil || !gate.Open() {
		return false
	}
	health.admission = gate
	return true
}

func (health *workerRuntimeHealth) matchesAdmissionGate(gate *agentturn.AdmissionGate) bool {
	if health == nil || gate == nil {
		return false
	}
	health.mu.RLock()
	defer health.mu.RUnlock()
	return health.admission == gate
}

func (health *workerRuntimeHealth) closeAdmissionLocked() {
	if health.admission != nil {
		health.admission.Close()
	}
}

func (health *workerRuntimeHealth) signalReadinessLossLocked() {
	if !health.readinessSignaled && health.readinessLost != nil {
		close(health.readinessLost)
		health.readinessSignaled = true
	}
}

func (health *workerRuntimeHealth) readinessLossSignal() <-chan struct{} {
	if health == nil {
		return nil
	}
	return health.readinessLost
}

func (health *workerRuntimeHealth) readyAtLocked(observedAt time.Time) bool {
	if health.phase != workerPhaseServing || !health.compositionReady ||
		!health.startupHealthy || !health.probeObserved || !health.probeHealthy ||
		health.runningLoops != allWorkerLoops ||
		health.loopPulseObserved != allWorkerLoops || health.hardReason != "" {
		return false
	}
	probeAge := observedAt.Sub(health.probeObservedAt)
	if probeAge < 0 || probeAge > health.freshness {
		return false
	}
	for loop := workerExecutionLoop; loop < workerLoopCount; loop++ {
		age := observedAt.Sub(health.loopPulseAt[loop])
		if age < 0 || age > health.loopFreshness {
			return false
		}
	}
	return health.admission == nil || health.admission.Open()
}

func (health *workerRuntimeHealth) armAdmissionIfReadyLocked(observedAt time.Time) {
	if health.admission != nil && health.readyAtLocked(observedAt) {
		health.admissionArmed = true
	}
}

func (health *workerRuntimeHealth) markCompositionReady() bool {
	if health == nil {
		return false
	}
	health.mu.Lock()
	defer health.mu.Unlock()
	if health.phase != workerPhaseStarting || health.compositionReady ||
		(health.admission != nil && !health.admission.Open()) {
		return false
	}
	health.compositionReady = true
	return true
}

func (health *workerRuntimeHealth) preparedForLoops() bool {
	if health == nil {
		return false
	}
	health.mu.RLock()
	defer health.mu.RUnlock()
	return health.phase == workerPhaseStarting && health.compositionReady &&
		health.startupObserved && health.startupHealthy && health.probeHealthy &&
		(health.admission == nil || health.admission.Open())
}

func (health *workerRuntimeHealth) recordStartupProbe(succeeded bool) bool {
	if health == nil {
		return false
	}
	health.mu.Lock()
	defer health.mu.Unlock()
	if health.phase != workerPhaseStarting || health.startupObserved ||
		(health.admission != nil && !health.admission.Open()) {
		return false
	}
	health.startupObserved = true
	health.startupHealthy = succeeded
	health.probeObserved = true
	health.probeHealthy = succeeded
	health.probeObservedAt = health.now()
	if !succeeded {
		health.closeAdmissionLocked()
		health.phase = workerPhaseFailed
		health.hardReason = reasonStartupProbeFailed
	}
	return true
}

func (health *workerRuntimeHealth) recordRuntimeProbeSuccess() bool {
	if health == nil {
		return false
	}
	health.mu.Lock()
	defer health.mu.Unlock()
	if (health.phase != workerPhaseStarting && health.phase != workerPhaseServing) ||
		(health.admission != nil && !health.admission.Open()) {
		return false
	}
	if !health.startupHealthy {
		return false
	}
	health.probeObserved = true
	health.probeHealthy = true
	health.probeObservedAt = health.now()
	health.armAdmissionIfReadyLocked(health.probeObservedAt)
	return true
}

// beginReadinessLossDrain is the one-way admission latch for a recurring
// dependency failure or freshness loss. It records the first stable cause and
// moves lifecycle state to draining in the same critical section. The serving
// layer cancels all Claim loops only after this succeeds, which guarantees
// operator readiness is already false when a loop observes cancellation.
//
// The phase intentionally remains live while an already-owned Turn drains.
// Once the serving layer returns, its lifecycle owner promotes the dependency
// failure to a stable failed/restart-required result.
func (health *workerRuntimeHealth) beginReadinessLossDrain(reason workerHealthReason) bool {
	if health == nil {
		return false
	}
	switch reason {
	case reasonDependencyProbeFailed, reasonDependencyProbeStale, reasonLoopPulseStale:
	default:
		return false
	}
	health.mu.Lock()
	defer health.mu.Unlock()
	if (health.phase != workerPhaseStarting && health.phase != workerPhaseServing) ||
		!health.startupHealthy {
		return false
	}
	if reason == reasonDependencyProbeFailed {
		health.probeObserved = true
		health.probeHealthy = false
		health.probeObservedAt = health.now()
	}
	health.closeAdmissionLocked()
	health.readinessLossReason = reason
	health.phase = workerPhaseDraining
	health.signalReadinessLossLocked()
	return true
}

func (health *workerRuntimeHealth) loopStarted(loop workerLoop) bool {
	if health == nil || !loop.valid() {
		return false
	}
	health.mu.Lock()
	defer health.mu.Unlock()
	if health.phase != workerPhaseStarting && health.phase != workerPhaseServing {
		return false
	}
	if !health.compositionReady || !health.startupHealthy ||
		(health.admission != nil && !health.admission.Open()) {
		return false
	}
	health.runningLoops |= loop.bit()
	if health.runningLoops == allWorkerLoops {
		health.phase = workerPhaseServing
	}
	return true
}

// loopPulse records scheduler progress independently of business throughput.
// Idle queue scans, reconcile passes and dispatcher scans all pulse; the
// Worker heartbeat also pulses while one long-running Turn is in flight.
func (health *workerRuntimeHealth) loopPulse(loop workerLoop) bool {
	if health == nil || !loop.valid() {
		return false
	}
	health.mu.Lock()
	defer health.mu.Unlock()
	if health.phase != workerPhaseStarting && health.phase != workerPhaseServing {
		return false
	}
	if health.admission != nil && !health.admission.Open() {
		return false
	}
	if health.runningLoops&loop.bit() == 0 {
		return false
	}
	health.loopPulseObserved |= loop.bit()
	health.loopPulseAt[loop] = health.now()
	health.armAdmissionIfReadyLocked(health.loopPulseAt[loop])
	return true
}

func (health *workerRuntimeHealth) loopExited(loop workerLoop, expected bool) bool {
	if health == nil || !loop.valid() {
		return false
	}
	health.mu.Lock()
	defer health.mu.Unlock()
	health.closeAdmissionLocked()
	health.runningLoops &^= loop.bit()
	health.loopPulseObserved &^= loop.bit()
	if expected && (health.phase == workerPhaseDraining ||
		health.phase == workerPhaseFailed || health.phase == workerPhaseStopped) {
		return true
	}
	if health.phase != workerPhaseStopped {
		health.phase = workerPhaseFailed
		health.hardReason = reasonLoopExited
	}
	return true
}

func (health *workerRuntimeHealth) beginDrain() bool {
	if health == nil {
		return false
	}
	health.mu.Lock()
	defer health.mu.Unlock()
	if health.phase != workerPhaseStarting && health.phase != workerPhaseServing {
		return false
	}
	health.closeAdmissionLocked()
	health.phase = workerPhaseDraining
	return true
}

func (health *workerRuntimeHealth) failRuntime() {
	if health == nil {
		return
	}
	health.mu.Lock()
	defer health.mu.Unlock()
	if health.phase == workerPhaseStopped {
		return
	}
	health.closeAdmissionLocked()
	health.phase = workerPhaseFailed
	if health.hardReason == "" {
		if health.readinessLossReason != "" {
			health.hardReason = health.readinessLossReason
		} else {
			health.hardReason = reasonRuntimeFailed
		}
	}
}

func (health *workerRuntimeHealth) failShutdownTimeout() {
	if health == nil {
		return
	}
	health.mu.Lock()
	defer health.mu.Unlock()
	if health.phase == workerPhaseStopped {
		return
	}
	health.closeAdmissionLocked()
	health.phase = workerPhaseFailed
	if health.hardReason == "" {
		health.hardReason = reasonShutdownTimeout
	}
}

// failResourceClose may move a stopped process to failed because resource
// release is part of the process lifecycle. Earlier hard failures retain
// precedence; the process-level return value follows the same rule.
func (health *workerRuntimeHealth) failResourceClose(timedOut bool) {
	if health == nil {
		return
	}
	health.mu.Lock()
	defer health.mu.Unlock()
	health.closeAdmissionLocked()
	health.phase = workerPhaseFailed
	health.compositionReady = false
	health.runningLoops = 0
	health.loopPulseObserved = 0
	closeReason := reasonResourceCloseFailed
	if timedOut {
		closeReason = reasonResourceCloseTimeout
	}
	health.resourceCloseReason = closeReason
	if health.hardReason == "" {
		health.hardReason = closeReason
	}
}

func (health *workerRuntimeHealth) stop() {
	if health == nil {
		return
	}
	health.mu.Lock()
	defer health.mu.Unlock()
	if health.phase == workerPhaseFailed {
		return
	}
	health.closeAdmissionLocked()
	health.phase = workerPhaseStopped
	health.runningLoops = 0
}

// Snapshot returns a derived view. Probe freshness is evaluated at read time,
// so readiness expires even if a stuck scheduler stops producing callbacks.
func (health *workerRuntimeHealth) Snapshot() workerHealthSnapshot {
	if health == nil {
		return workerHealthSnapshot{
			Phase: string(workerPhaseFailed), Ready: false, Live: false,
			Reasons: []string{string(reasonRuntimeFailed)},
		}
	}
	health.mu.Lock()
	observedAt := health.now()
	probeFresh := false
	if health.probeObserved {
		age := observedAt.Sub(health.probeObservedAt)
		probeFresh = age >= 0 && age <= health.freshness
	}
	loopPulsesFresh := health.loopPulseObserved == allWorkerLoops
	if loopPulsesFresh {
		for loop := workerExecutionLoop; loop < workerLoopCount; loop++ {
			age := observedAt.Sub(health.loopPulseAt[loop])
			if age < 0 || age > health.loopFreshness {
				loopPulsesFresh = false
				break
			}
		}
	}
	gateOpen := health.admission == nil || health.admission.Open()
	stateReady := health.phase == workerPhaseServing && health.compositionReady &&
		health.startupHealthy && health.probeHealthy && probeFresh &&
		health.runningLoops == allWorkerLoops && loopPulsesFresh && health.hardReason == ""
	ready := stateReady && gateOpen
	if ready && health.admission != nil {
		health.admissionArmed = true
	}

	// Freshness is derived at read time. Once this process has actually reached
	// Ready, Snapshot must revoke admission before it can publish a stale view;
	// otherwise /readyz=false and a concurrent Claim could disagree about the
	// process's authority until the next monitor tick. The monitor observes the
	// latched reason and performs cancellation/drain; the gate cannot reopen if
	// a late success refreshes a timestamp in the meantime.
	if health.admission != nil && health.admissionArmed && gateOpen &&
		health.phase == workerPhaseServing && health.compositionReady &&
		health.startupHealthy && health.runningLoops == allWorkerLoops &&
		health.hardReason == "" {
		lossReason := workerHealthReason("")
		switch {
		case health.probeObserved && health.probeHealthy && !probeFresh:
			lossReason = reasonDependencyProbeStale
		case health.loopPulseObserved == allWorkerLoops && !loopPulsesFresh:
			lossReason = reasonLoopPulseStale
		}
		if lossReason != "" {
			health.admission.Close()
			gateOpen = false
			ready = false
			if health.readinessLossReason == "" {
				health.readinessLossReason = lossReason
			}
			health.phase = workerPhaseDraining
			health.signalReadinessLossLocked()
		}
	}

	phase := health.phase
	compositionReady := health.compositionReady
	startupObserved := health.startupObserved
	startupHealthy := health.startupHealthy
	probeHealthy := health.probeHealthy
	runningLoops := health.runningLoops
	loopPulseObserved := health.loopPulseObserved
	hardReason := health.hardReason
	readinessLossReason := health.readinessLossReason
	resourceCloseReason := health.resourceCloseReason
	hasAdmission := health.admission != nil
	health.mu.Unlock()

	live := phase == workerPhaseStarting || phase == workerPhaseServing || phase == workerPhaseDraining

	reasons := make([]string, 0, 2)
	appendReason := func(reason workerHealthReason) {
		if reason == "" {
			return
		}
		for _, existing := range reasons {
			if existing == string(reason) {
				return
			}
		}
		reasons = append(reasons, string(reason))
	}
	switch phase {
	case workerPhaseFailed:
		if hardReason == "" {
			hardReason = reasonRuntimeFailed
		}
		appendReason(hardReason)
		if resourceCloseReason != "" && resourceCloseReason != hardReason {
			appendReason(resourceCloseReason)
		}
	case workerPhaseDraining:
		appendReason(reasonDraining)
		if readinessLossReason != "" {
			appendReason(readinessLossReason)
		}
	case workerPhaseStopped:
		appendReason(reasonStopped)
	default:
		if !compositionReady {
			appendReason(reasonCompositionPending)
		}
		if !startupObserved {
			appendReason(reasonStartupProbePending)
		} else if !startupHealthy {
			appendReason(reasonStartupProbeFailed)
		}
		if runningLoops != allWorkerLoops {
			appendReason(reasonLoopsStarting)
		} else if loopPulseObserved != allWorkerLoops {
			appendReason(reasonLoopPulsePending)
		} else if !loopPulsesFresh {
			appendReason(reasonLoopPulseStale)
		}
		if startupHealthy {
			switch {
			case !probeHealthy:
				appendReason(reasonDependencyProbeFailed)
			case !probeFresh:
				appendReason(reasonDependencyProbeStale)
			}
		}
		if readinessLossReason != "" {
			appendReason(readinessLossReason)
		} else if hasAdmission && !gateOpen {
			appendReason(reasonDraining)
		}
	}
	if ready {
		reasons = []string{}
	}
	return workerHealthSnapshot{
		Phase: string(phase), Live: live, Ready: ready, Reasons: reasons,
	}
}

type workerProbeOutcome uint8

const (
	workerProbeFailed workerProbeOutcome = iota
	workerProbeSucceeded
	workerProbeTimedOut
	workerProbeCanceled
	workerProbeDetached
)

// executeWorkerProbe is the unit-level compatibility wrapper. Production
// paths pass their normalized stop grace explicitly.
func executeWorkerProbe(parent context.Context, timeout time.Duration, check func(context.Context) error) workerProbeOutcome {
	stopGrace := defaultWorkerProbeStopGrace
	if timeout > 0 && timeout < stopGrace {
		stopGrace = timeout
	}
	return executeWorkerProbeWithGrace(parent, timeout, stopGrace, check)
}

// executeWorkerProbeWithGrace does not call an implementation "canceled" or
// "timed out" until its goroutine has fully unwound. After either boundary it
// gives the implementation one independent, bounded stop grace. Failure to
// quiesce is workerProbeDetached: only process termination can then make
// composition resource ownership safe again.
func executeWorkerProbeWithGrace(
	parent context.Context,
	timeout time.Duration,
	stopGrace time.Duration,
	check func(context.Context) error,
) workerProbeOutcome {
	if parent == nil || check == nil {
		return workerProbeFailed
	}
	if timeout <= 0 {
		timeout = defaultWorkerProbeTimeout
	}
	if stopGrace <= 0 {
		stopGrace = defaultWorkerProbeStopGrace
	}
	probeCtx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	probeDeadline, _ := probeCtx.Deadline()
	parentDeadline, parentHasDeadline := parent.Deadline()
	parentOwnsDeadline := parentHasDeadline && !parentDeadline.After(probeDeadline)
	result := make(chan workerProbeCallResult, 1)
	go func() {
		callResult := workerProbeCallResult{}
		defer func() {
			_ = recover()
			if callResult.completedAt.IsZero() {
				callResult.completedAt = time.Now()
			}
			result <- callResult
		}()
		callResult.succeeded = check(probeCtx) == nil
		callResult.completedAt = time.Now()
	}()

	classifyQuiesced := func(callResult workerProbeCallResult) workerProbeOutcome {
		return classifyQuiescedWorkerProbe(
			parent, probeDeadline, parentOwnsDeadline, callResult,
		)
	}

	select {
	case callResult := <-result:
		return classifyQuiesced(callResult)
	case <-parent.Done():
	case <-probeCtx.Done():
	}

	// Ensure the implementation sees cancellation even when the parent and its
	// derived deadline became ready in the same scheduler turn.
	cancel()
	timer := time.NewTimer(stopGrace)
	defer timer.Stop()
	select {
	case callResult := <-result:
		return classifyQuiesced(callResult)
	case <-timer.C:
		// Prefer a completed unwind if the timer and result became ready
		// together. A result that arrives after this observation is detached.
		select {
		case callResult := <-result:
			return classifyQuiesced(callResult)
		default:
			return workerProbeDetached
		}
	}
}

type workerProbeCallResult struct {
	succeeded   bool
	completedAt time.Time
}

func classifyQuiescedWorkerProbe(
	parent context.Context,
	probeDeadline time.Time,
	parentOwnsDeadline bool,
	result workerProbeCallResult,
) workerProbeOutcome {
	if parent != nil && parent.Err() != nil {
		return workerProbeCanceled
	}
	// Classify against when the implementation actually unwound, not when the
	// receiving goroutine happened to be scheduled. This prevents a buffered
	// success/failure completed before the deadline from becoming a false
	// timeout merely because result and deadline were both ready in select.
	if result.completedAt.IsZero() || !result.completedAt.Before(probeDeadline) {
		if parentOwnsDeadline {
			return workerProbeCanceled
		}
		return workerProbeTimedOut
	}
	if result.succeeded {
		return workerProbeSucceeded
	}
	return workerProbeFailed
}

func runWorkerStartupProbe(ctx context.Context, probe RuntimeProbe, health *workerRuntimeHealth, timeout time.Duration) workerProbeOutcome {
	return runWorkerStartupProbeWithGrace(
		ctx, probe, health, timeout, defaultWorkerProbeStopGrace,
	)
}

func runWorkerStartupProbeWithGrace(
	ctx context.Context,
	probe RuntimeProbe,
	health *workerRuntimeHealth,
	timeout time.Duration,
	stopGrace time.Duration,
) workerProbeOutcome {
	if probe == nil || health == nil {
		return workerProbeFailed
	}
	outcome := executeWorkerProbeWithGrace(ctx, timeout, stopGrace, probe.Startup)
	switch outcome {
	case workerProbeSucceeded:
		if !health.recordStartupProbe(true) {
			return workerProbeFailed
		}
	case workerProbeFailed, workerProbeTimedOut, workerProbeDetached:
		health.recordStartupProbe(false)
	}
	return outcome
}

func monitorWorkerRuntimeProbe(ctx context.Context, probe RuntimeProbe, health *workerRuntimeHealth, policy workerProbePolicy) workerProbeOutcome {
	policy = policy.normalized()
	ticker := time.NewTicker(policy.Interval)
	defer ticker.Stop()
	return monitorWorkerRuntimeProbeTicksWithGrace(
		ctx, probe, health, policy.Timeout, policy.StopGrace, ticker.C,
	)
}

// monitorWorkerRuntimeProbeTicks latches the first failed or timed-out
// recurring check. Successful checks refresh readiness, but a failure cannot
// be healed in-process: the serving layer drains and a fresh process must pass
// the complete startup probe before it may Claim again.
func monitorWorkerRuntimeProbeTicks(ctx context.Context, probe RuntimeProbe, health *workerRuntimeHealth, timeout time.Duration, ticks <-chan time.Time) workerProbeOutcome {
	stopGrace := defaultWorkerProbeStopGrace
	if timeout > 0 && timeout < stopGrace {
		stopGrace = timeout
	}
	return monitorWorkerRuntimeProbeTicksWithGrace(ctx, probe, health, timeout, stopGrace, ticks)
}

func monitorWorkerRuntimeProbeTicksWithGrace(
	ctx context.Context,
	probe RuntimeProbe,
	health *workerRuntimeHealth,
	timeout time.Duration,
	stopGrace time.Duration,
	ticks <-chan time.Time,
) workerProbeOutcome {
	if ctx == nil || probe == nil || health == nil || ticks == nil {
		return workerProbeFailed
	}
	armed := health.Snapshot().Ready
	for {
		select {
		case <-ctx.Done():
			return workerProbeCanceled
		case <-health.readinessLossSignal():
			return workerProbeFailed
		case <-ticks:
		}
		if ctx.Err() != nil {
			return workerProbeCanceled
		}
		snapshot := health.Snapshot()
		if snapshot.Ready {
			armed = true
		} else if armed {
			if reason := recurringReadinessLossReason(snapshot); reason != "" {
				if snapshot.Phase == string(workerPhaseDraining) {
					return workerProbeFailed
				}
				if health.beginReadinessLossDrain(reason) {
					return workerProbeFailed
				}
				if ctx.Err() != nil {
					return workerProbeCanceled
				}
			}
		}
		outcome := executeWorkerProbeWithGrace(ctx, timeout, stopGrace, probe.Check)
		switch outcome {
		case workerProbeSucceeded:
			health.recordRuntimeProbeSuccess()
			if health.Snapshot().Ready {
				armed = true
			}
		case workerProbeDetached:
			health.beginReadinessLossDrain(reasonDependencyProbeFailed)
			return outcome
		case workerProbeFailed, workerProbeTimedOut:
			if ctx.Err() != nil {
				return workerProbeCanceled
			}
			if !health.beginReadinessLossDrain(reasonDependencyProbeFailed) && ctx.Err() != nil {
				return workerProbeCanceled
			}
			return outcome
		case workerProbeCanceled:
			return outcome
		}
	}
}

// recurringReadinessLossReason reduces a derived snapshot back to the small
// closed set that requires admission shutdown. It runs only after the process
// has reached Ready once, so loop_pulse_pending during initial startup cannot
// trip the latch. Unexpected loop exits are already handled by the loop
// supervisor and retain their stronger reason.
func recurringReadinessLossReason(snapshot workerHealthSnapshot) workerHealthReason {
	if (snapshot.Phase != string(workerPhaseServing) &&
		snapshot.Phase != string(workerPhaseDraining)) || snapshot.Ready {
		return ""
	}
	for _, expected := range []workerHealthReason{
		reasonDependencyProbeFailed,
		reasonDependencyProbeStale,
		reasonLoopPulseStale,
	} {
		for _, observed := range snapshot.Reasons {
			if observed == string(expected) {
				return expected
			}
		}
	}
	return ""
}
