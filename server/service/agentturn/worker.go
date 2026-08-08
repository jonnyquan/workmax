package agentturn

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	agentv1 "server/contracts/agent/v1"
)

var (
	ErrWorkerFenceLost     = errors.New("durable turn worker lost its execution fence")
	ErrExecutorNonTerminal = errors.New("durable turn executor returned a non-terminal status")
)

const (
	DefaultWorkerIdleBackoff       = time.Second
	DefaultWorkerHeartbeatDivisor  = 3
	DefaultWorkerDrainTimeout      = 30 * time.Second
	DefaultWorkerExecutorStopGrace = 5 * time.Second
	// heartbeatWriteTimeout bounds one shielded lease refresh. A refresh that
	// outlives a whole lease is worthless, so the lease TTL is its natural cap.
	heartbeatWriteTimeout      = DefaultAttemptLeaseTTL
	MaxWorkerIdleBackoff       = time.Minute
	MaxWorkerDrainTimeout      = 10 * time.Minute
	MaxWorkerExecutorStopGrace = time.Minute
)

// OperationDraft is one durable step an executor commits mid-Turn. It maps
// directly onto CommitAttemptCommand so the executor never assembles a fence
// or reasons about idempotency itself.
type OperationDraft struct {
	OperationID string
	Event       EventDraft
	Effects     []EffectOutboxDraft
}

// ExecutionSession is the executor's bounded view of one epoch. It exposes no
// fence, no store and no Turn mutation: the kernel keeps ownership, ordering,
// idempotency and terminal authority.
type ExecutionSession interface {
	Turn() Turn
	Attempt() TurnAttempt
	// Emit commits one non-terminal Operation. It fails once the epoch has
	// ended, so a late goroutine cannot append to a Turn the worker already
	// finished or lost.
	Emit(ctx context.Context, operation OperationDraft) (CommitAttemptResult, error)
	// CancellationRequested reports the most recent intent the heartbeat saw.
	// A cooperative executor polls this at a safe point and returns a terminal
	// status itself; an executor that blocks instead gets its context
	// cancelled.
	CancellationRequested() bool
}

// TurnExecutor runs the domain work behind one Turn and returns the terminal
// status it reached. It is the only part of execution the kernel does not own,
// which is what keeps Writer, Workbook and Media from each reimplementing
// admission, leasing, fencing, event persistence or cancellation.
//
// An executor must return promptly once its context is cancelled. The context
// is cancelled when a configured execution/progress ceiling fires, the lease
// is lost, cancellation is requested, or the worker's drain deadline passes.
type TurnExecutor interface {
	Execute(ctx context.Context, session ExecutionSession) (agentv1.TurnStatus, error)
}

type WorkerOptions struct {
	WorkerID          string
	WorkerBuildDigest string
	// AdmissionGate is the shared, one-way authority boundary for starting a
	// ClaimNext call. Nil preserves the legacy candidate behaviour.
	AdmissionGate *AdmissionGate
	// ScanLimit bounds one ClaimNext contention window. Zero uses the store default.
	ScanLimit int
	// IdleBackoff is the pause after an empty queue. Zero selects
	// DefaultWorkerIdleBackoff.
	IdleBackoff time.Duration
	// HeartbeatInterval must be well below the store's lease TTL. Zero selects
	// DefaultAttemptLeaseTTL / DefaultWorkerHeartbeatDivisor.
	HeartbeatInterval time.Duration
	// DrainTimeout is how long in-flight execution may continue after Run's
	// context is cancelled. Zero selects DefaultWorkerDrainTimeout.
	DrainTimeout time.Duration
	// ExecutorStopGrace bounds how long the kernel waits for an executor to
	// return after cancelling its epoch. If it does not return, the Worker is
	// permanently stopped and the hosting process must restart before claiming
	// more work. Zero selects DefaultWorkerExecutorStopGrace.
	ExecutorStopGrace time.Duration
	// PluginLimits enables exact per-release execution and durable-progress
	// ceilings. Empty preserves the unbounded reference-harness behavior. Once
	// non-empty, every claimed Plugin must have an exact entry; there is no
	// ID-only or default fallback.
	PluginLimits []PluginExecutionLimits
	// AttemptIDFactory produces a unique ID per claim. A durable worker can
	// supply a recoverable ID so a lost claim response is replayed onto the
	// same epoch instead of stranding a Turn until its lease lapses.
	AttemptIDFactory func() (string, error)
}

func (options *WorkerOptions) applyDefaults() {
	if options.IdleBackoff <= 0 {
		options.IdleBackoff = DefaultWorkerIdleBackoff
	}
	if options.HeartbeatInterval <= 0 {
		options.HeartbeatInterval = DefaultAttemptLeaseTTL / DefaultWorkerHeartbeatDivisor
	}
	if options.DrainTimeout <= 0 {
		options.DrainTimeout = DefaultWorkerDrainTimeout
	}
	if options.ExecutorStopGrace == 0 {
		options.ExecutorStopGrace = DefaultWorkerExecutorStopGrace
	}
	if options.AttemptIDFactory == nil {
		options.AttemptIDFactory = randomAttemptID
	}
}

func (options WorkerOptions) Validate() error {
	if err := validatePrintableASCII("workerId", options.WorkerID, MaxWorkerIDBytes); err != nil {
		return err
	}
	if err := validatePrintableASCII("workerBuildDigest", options.WorkerBuildDigest, MaxWorkerBuildDigestBytes); err != nil {
		return err
	}
	if options.ScanLimit < 0 || options.ScanLimit > MaxClaimNextScanLimit {
		return fmt.Errorf("scanLimit must be between 0 and %d", MaxClaimNextScanLimit)
	}
	if options.IdleBackoff <= 0 || options.IdleBackoff > MaxWorkerIdleBackoff {
		return fmt.Errorf("idleBackoff must be between 1ns and %s", MaxWorkerIdleBackoff)
	}
	// A heartbeat at or beyond the lease bound can never refresh in time, so a
	// worker configured that way would silently lose every epoch it claims.
	if options.HeartbeatInterval <= 0 || options.HeartbeatInterval >= MaxAttemptLeaseTTL {
		return fmt.Errorf("heartbeatInterval must be between 1ns and %s", MaxAttemptLeaseTTL)
	}
	if options.DrainTimeout <= 0 || options.DrainTimeout > MaxWorkerDrainTimeout {
		return fmt.Errorf("drainTimeout must be between 1ns and %s", MaxWorkerDrainTimeout)
	}
	if options.ExecutorStopGrace <= 0 || options.ExecutorStopGrace > MaxWorkerExecutorStopGrace {
		return fmt.Errorf("executorStopGrace must be between 1ns and %s", MaxWorkerExecutorStopGrace)
	}
	if _, _, err := normalizePluginExecutionLimits(options.PluginLimits); err != nil {
		return err
	}
	return nil
}

// Worker turns the ClaimNext / HeartbeatAttempt / CommitAttempt primitives
// into an execution loop.
//
// It is a candidate runtime, not a process: it opens no port, reads no
// configuration, registers no signal handler and is composed into nothing. It
// also owns no Settlement policy or authority. A terminal Commit may invoke an
// authority already installed on the injected Store.
type Worker struct {
	store           ExecutionStore
	executor        TurnExecutor
	options         WorkerOptions
	admission       *AdmissionGate
	pluginLimits    map[agentv1.EventPluginRef]PluginExecutionLimits
	restartRequired atomic.Bool
}

func NewWorker(store ExecutionStore, executor TurnExecutor, options WorkerOptions) (*Worker, error) {
	if store == nil || executor == nil {
		return nil, fmt.Errorf("worker requires an execution store and an executor")
	}
	options.applyDefaults()
	if err := options.Validate(); err != nil {
		return nil, err
	}
	limits, byPlugin, err := normalizePluginExecutionLimits(options.PluginLimits)
	if err != nil {
		return nil, err
	}
	options.PluginLimits = limits
	return &Worker{
		store: store, executor: executor, options: options,
		admission: options.AdmissionGate, pluginLimits: byPlugin,
	}, nil
}

// MatchesAdmissionGate proves the exact gate identity captured at
// construction. A sealed composition uses this instead of checking a mutable
// open/closed boolean, which would be both insufficient and racy.
func (worker *Worker) MatchesAdmissionGate(expected *AdmissionGate) bool {
	return worker != nil && worker.admission == expected
}

// MatchesPluginExecutionLimits proves that this Worker owns a defensive,
// exact copy of the supplied per-release policy. It is intended for a sealed
// composition's integrity check; it performs no I/O and exposes no mutable
// Worker state.
func (worker *Worker) MatchesPluginExecutionLimits(expected []PluginExecutionLimits) bool {
	if worker == nil {
		return false
	}
	_, normalized, err := normalizePluginExecutionLimits(expected)
	if err != nil || len(normalized) != len(worker.pluginLimits) {
		return false
	}
	for plugin, limits := range normalized {
		if installed, found := worker.pluginLimits[plugin]; !found || installed != limits {
			return false
		}
	}
	return true
}

// WorkerRunResult reports one claimed Turn's outcome. Committed is false when
// the worker deliberately did not write a terminal state, which happens when
// it lost the fence, drain cut execution short, exact limits were unavailable,
// or execution failed to quiesce inside stop grace. In each case the Turn stays
// recoverable through reclaim or reconciliation.
type WorkerRunResult struct {
	TurnID           agentv1.TurnID
	AttemptID        string
	TerminalStatus   agentv1.TurnStatus
	Committed        bool
	Cancelled        bool
	FenceLost        bool
	ExecutorErr      error
	Commit           CommitAttemptResult
	OperationsEmit   int
	ProgressCommits  int
	LimitExceeded    WorkerLimitKind
	ExecutorDetached bool
	RestartRequired  bool
	ExecutionWindow  time.Duration
}

// RunOnce claims at most one Turn and drives it to a terminal state.
//
// It returns ErrNoClaimableTurn when the queue is empty. A failing executor is
// not an error here: the Turn is committed as `failed` and the cause is
// reported on the result, because a domain failure is a normal Turn outcome
// rather than a worker malfunction.
func (worker *Worker) RunOnce(ctx context.Context) (WorkerRunResult, error) {
	return worker.runOnce(ctx, ctx, nil)
}

func (worker *Worker) runOnce(claimCtx, execCtx context.Context, pulse func()) (WorkerRunResult, error) {
	if worker.restartRequired.Load() {
		return WorkerRunResult{RestartRequired: true}, ErrWorkerRestartRequired
	}
	if err := contextError(claimCtx); err != nil {
		return WorkerRunResult{}, err
	}
	if err := contextError(execCtx); err != nil {
		return WorkerRunResult{}, err
	}
	attemptID, err := worker.options.AttemptIDFactory()
	if err != nil {
		return WorkerRunResult{}, err
	}
	if err := worker.admission.Acquire(); err != nil {
		return WorkerRunResult{}, err
	}
	claim, err := worker.store.ClaimNext(claimCtx, ClaimNextCommand{
		AttemptID:         attemptID,
		WorkerID:          worker.options.WorkerID,
		WorkerBuildDigest: worker.options.WorkerBuildDigest,
		ScanLimit:         worker.options.ScanLimit,
	})
	if err != nil {
		return WorkerRunResult{}, err
	}

	result := WorkerRunResult{TurnID: claim.Turn.ID, AttemptID: claim.Attempt.ID}
	limits, limited := worker.pluginLimits[claim.Turn.Plugin]
	if len(worker.pluginLimits) > 0 && !limited {
		// A partially wired policy is a process configuration failure, not a
		// domain failure. Do not run an unbounded fallback and do not invent a
		// terminal result for work this process was not authorized to execute.
		worker.restartRequired.Store(true)
		result.RestartRequired = true
		return result, errors.Join(ErrWorkerPluginLimitsUnavailable, ErrWorkerRestartRequired)
	}
	session := &workerSession{
		worker:     worker,
		turn:       claim.Turn,
		attempt:    claim.Attempt,
		progress:   make(chan struct{}, 1),
		emitPermit: make(chan struct{}, 1),
	}
	session.emitPermit <- struct{}{}

	// Lease liveness and executor lifetime are deliberately separate. A soft
	// ceiling cancels the executor while the heartbeat keeps the fence live
	// through its bounded stop grace and the authoritative terminal commit.
	executorCtx, stopExecutor := context.WithCancelCause(execCtx)
	heartbeatCtx, stopHeartbeat := context.WithCancel(execCtx)
	defer stopExecutor(context.Canceled)
	defer stopHeartbeat()
	session.epochCtx = executorCtx
	heartbeatDone := worker.startHeartbeat(heartbeatCtx, session, stopExecutor, pulse)

	started := time.Now()
	executorDone := make(chan workerExecutorOutcome, 1)
	go func() {
		terminal, executorErr := worker.executor.Execute(executorCtx, session)
		executorDone <- workerExecutorOutcome{terminal: terminal, err: executorErr}
	}()

	var enforced *PluginExecutionLimits
	if limited {
		enforced = &limits
	}
	outcome, executorReturned, limit := waitForWorkerExecutor(executorCtx, session.progress, executorDone, enforced)
	result.ExecutionWindow = time.Since(started)
	if limit.Valid() {
		// Cancel the epoch before waiting. Emit binds even a caller-supplied
		// Background context to this cancellation, so a timed-out executor cannot
		// keep a storage mutation alive merely by choosing a different context.
		stopExecutor(limit.err())
	} else {
		stopExecutor(context.Canceled)
	}
	stopDeadline := time.Now().Add(worker.options.ExecutorStopGrace)
	session.close()
	if !executorReturned {
		outcome, executorReturned = waitForWorkerExecutorStop(executorDone, time.Until(stopDeadline))
	}
	if !executorReturned {
		// Go cannot kill an arbitrary goroutine. Permanently poison this Worker
		// instance so Run cannot claim another Turn and accumulate leaked domain
		// executions; the supervisor must replace the process.
		worker.restartRequired.Store(true)
		result.ExecutorDetached = true
		result.RestartRequired = true
	}
	if executorReturned && !session.waitForEmitStop(time.Until(stopDeadline)) {
		// Execute returned, but a child goroutine is still inside Emit. Treat the
		// epoch as non-cooperative: a terminal write must not race that Operation.
		worker.restartRequired.Store(true)
		result.RestartRequired = true
	}

	result.OperationsEmit = int(session.emitted.Load())
	result.ProgressCommits = int(session.progressCommits.Load())
	if executorReturned {
		result.ExecutorErr = outcome.err
	}
	result.LimitExceeded = limit

	if result.RestartRequired {
		// A hard non-cooperative timeout cannot be made safe by pretending the
		// goroutine stopped. Revoke its Session, stop refreshing the lease and
		// leave the epoch uncommitted for reclaim; settlement must not run.
		stopHeartbeat()
		<-heartbeatDone
		result.Cancelled = session.CancellationRequested()
		result.FenceLost = session.fenceLost.Load()
		if result.FenceLost {
			return result, workerRunError(ErrWorkerFenceLost, true)
		}
		if err := contextError(execCtx); err != nil {
			return result, workerRunError(err, true)
		}
		return result, ErrWorkerRestartRequired
	}

	// Serialize the final ownership observation and terminal Commit against a
	// heartbeat round trip. The heartbeat remains active until this point, so a
	// long execution does not create a lease-expiry window just before commit.
	session.leaseMu.Lock()
	result.Cancelled = session.CancellationRequested()
	result.FenceLost = session.fenceLost.Load()
	if result.Cancelled {
		result.LimitExceeded = ""
	}
	if result.FenceLost {
		session.leaseMu.Unlock()
		stopHeartbeat()
		<-heartbeatDone
		return result, ErrWorkerFenceLost
	}
	if err := contextError(execCtx); err != nil {
		session.leaseMu.Unlock()
		stopHeartbeat()
		<-heartbeatDone
		// Drain deadline passed. Leave the Turn claimable: its lease lapses
		// and reclaim or reconciliation decides what happens next.
		return result, err
	}

	var terminal agentv1.TurnStatus
	var statusErr error
	if result.Cancelled {
		terminal = agentv1.TurnStatusStopped
	} else if result.LimitExceeded.Valid() {
		terminal = agentv1.TurnStatusTimeout
	} else {
		terminal, statusErr = worker.resolveTerminal(outcome.terminal, outcome.err, false)
	}
	commit, commitErr := worker.store.CommitAttempt(execCtx, CommitAttemptCommand{
		Fence:          session.attempt.Fence(),
		OperationID:    terminalOperationID(session.attempt.ID),
		TerminalStatus: terminal,
	})
	if errors.Is(commitErr, ErrAttemptCancelled) && terminal != agentv1.TurnStatusStopped {
		// The durable cancellation may have landed after the last heartbeat.
		// The failed transaction wrote no receipt, so retry the same terminal
		// Operation ID with the cancellation-owned status.
		session.cancelRequested.Store(true)
		result.Cancelled = true
		result.LimitExceeded = ""
		statusErr = nil
		terminal = agentv1.TurnStatusStopped
		commit, commitErr = worker.store.CommitAttempt(execCtx, CommitAttemptCommand{
			Fence:          session.attempt.Fence(),
			OperationID:    terminalOperationID(session.attempt.ID),
			TerminalStatus: terminal,
		})
	}
	stopHeartbeat()
	session.leaseMu.Unlock()
	<-heartbeatDone
	result.TerminalStatus = terminal
	result.FenceLost = session.fenceLost.Load()
	if commitErr != nil {
		if isOwnershipLoss(commitErr) {
			result.FenceLost = true
			return result, ErrWorkerFenceLost
		}
		return result, commitErr
	}
	result.Commit = commit
	result.Committed = true
	return result, statusErr
}

type workerExecutorOutcome struct {
	terminal agentv1.TurnStatus
	err      error
}

func waitForWorkerExecutor(
	ctx context.Context,
	progress <-chan struct{},
	done <-chan workerExecutorOutcome,
	limits *PluginExecutionLimits,
) (workerExecutorOutcome, bool, WorkerLimitKind) {
	var executionTimer *time.Timer
	var executionDeadline <-chan time.Time
	var progressTimer *time.Timer
	var progressDeadline <-chan time.Time
	var progressUpdates <-chan struct{}
	if limits != nil {
		executionTimer = time.NewTimer(limits.ExecutionTimeout)
		executionDeadline = executionTimer.C
		progressTimer = time.NewTimer(limits.ProgressTimeout)
		progressDeadline = progressTimer.C
		progressUpdates = progress
		defer executionTimer.Stop()
		defer progressTimer.Stop()
	}

	for {
		select {
		case outcome := <-done:
			return outcome, true, ""
		default:
		}
		select {
		case outcome := <-done:
			return outcome, true, ""
		case <-ctx.Done():
			select {
			case outcome := <-done:
				return outcome, true, ""
			default:
				return workerExecutorOutcome{}, false, ""
			}
		case <-executionDeadline:
			select {
			case outcome := <-done:
				return outcome, true, ""
			default:
				return workerExecutorOutcome{}, false, WorkerLimitExecutionTimeout
			}
		case <-progressDeadline:
			// A successful durable Commit may have raced the timer delivery. Give
			// the persisted signal priority; a heartbeat never enters this channel.
			select {
			case <-progress:
				resetWorkerTimer(progressTimer, limits.ProgressTimeout)
				continue
			default:
			}
			select {
			case outcome := <-done:
				return outcome, true, ""
			default:
				return workerExecutorOutcome{}, false, WorkerLimitProgressTimeout
			}
		case <-progressUpdates:
			resetWorkerTimer(progressTimer, limits.ProgressTimeout)
		}
	}
}

func resetWorkerTimer(timer *time.Timer, duration time.Duration) {
	if timer == nil {
		return
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}

func waitForWorkerExecutorStop(
	done <-chan workerExecutorOutcome,
	grace time.Duration,
) (workerExecutorOutcome, bool) {
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case outcome := <-done:
		return outcome, true
	case <-timer.C:
		return workerExecutorOutcome{}, false
	}
}

func workerRunError(primary error, restartRequired bool) error {
	if !restartRequired {
		return primary
	}
	return errors.Join(primary, ErrWorkerRestartRequired)
}

// resolveTerminal picks the Turn's terminal state. Cancellation wins over an
// executor error because a cancelled executor usually reports its context
// error, and recording that as `failed` would misattribute a user-requested
// stop as a product failure.
func (worker *Worker) resolveTerminal(reported agentv1.TurnStatus, executorErr error, cancelled bool) (agentv1.TurnStatus, error) {
	if cancelled {
		return agentv1.TurnStatusStopped, nil
	}
	if executorErr != nil {
		return agentv1.TurnStatusFailed, nil
	}
	if !reported.Valid() || !reported.Terminal() {
		return agentv1.TurnStatusFailed, fmt.Errorf("%w: %q", ErrExecutorNonTerminal, reported)
	}
	if reported == agentv1.TurnStatusStopped {
		// Only a recorded cancellation intent may produce `stopped`; the store
		// enforces this, so translate rather than fail the commit.
		return agentv1.TurnStatusFailed, fmt.Errorf("%w: executor reported stopped without a cancellation", ErrExecutorNonTerminal)
	}
	return reported, nil
}

// startHeartbeat keeps the lease alive for as long as the epoch runs. Losing
// the lease, being fenced, or observing a cancellation all cancel the
// executor: the first two mean the worker no longer owns the Turn, the third
// means it must stop.
func (worker *Worker) startHeartbeat(ctx context.Context, session *workerSession, stopRun context.CancelCauseFunc, pulse func()) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(worker.options.HeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			session.leaseMu.Lock()
			if contextError(ctx) != nil {
				session.leaseMu.Unlock()
				return
			}
			// The lease refresh is one short write. Tearing it down mid-flight
			// achieves nothing — the lease was either extended or it was not —
			// and an aborted write transaction can leave the connection
			// unusable for the terminal commit that immediately follows. So the
			// write is shielded from cancellation and bounded on its own;
			// only the loop reacts to the run context.
			writeCtx, cancelWrite := context.WithTimeout(
				context.WithoutCancel(ctx), heartbeatWriteTimeout)
			beat, err := worker.store.HeartbeatAttempt(writeCtx, HeartbeatAttemptCommand{
				Fence: session.attempt.Fence(),
			})
			cancelWrite()
			var stopCause error
			if err != nil {
				if isOwnershipLoss(err) {
					session.fenceLost.Store(true)
					stopCause = ErrWorkerFenceLost
				}
			} else if beat.CancelRequestedAt != nil {
				session.cancelRequested.Store(true)
				stopCause = ErrAttemptCancelled
			}
			ctxStopped := contextError(ctx) != nil
			session.leaseMu.Unlock()
			if err == nil {
				observeLoopPulse(pulse)
			}
			if stopCause != nil {
				stopRun(stopCause)
				return
			}
			if ctxStopped {
				return
			}
			if err != nil {
				// A transient store failure is not proof of lost ownership.
				// Keep beating; the lease decides.
				continue
			}
		}
	}()
	return done
}

func isOwnershipLoss(err error) bool {
	for _, loss := range []error{
		ErrAttemptFenced, ErrAttemptLeaseExpired, ErrAttemptNotFound, ErrTurnTerminal,
	} {
		if errors.Is(err, loss) {
			return true
		}
	}
	return false
}

// Run loops until ctx is cancelled, then drains.
//
// Drain is deliberate: cancelling ctx stops the worker claiming new work, but
// in-flight execution keeps its own deadline so a shutting-down worker
// finishes and commits the Turn it already owns instead of abandoning it to a
// lease timeout.
func (worker *Worker) Run(ctx context.Context, observe func(WorkerRunResult, error)) error {
	return worker.RunWithPulse(ctx, observe, nil)
}

// RunWithPulse is Run plus a process-health pulse that fires for every queue
// cycle and lease-heartbeat round trip. It observes scheduler progress rather
// than business throughput: an empty queue still pulses, and a long Turn
// pulses while its lease heartbeat remains active.
func (worker *Worker) RunWithPulse(ctx context.Context, observe func(WorkerRunResult, error), pulse func()) error {
	drainCtx, cancelDrain := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelDrain()
	go func() {
		select {
		case <-ctx.Done():
		case <-drainCtx.Done():
			return
		}
		timer := time.NewTimer(worker.options.DrainTimeout)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-drainCtx.Done():
		}
		cancelDrain()
	}()

	for {
		if err := contextError(ctx); err != nil {
			return err
		}
		observeLoopPulse(pulse)
		result, err := worker.runOnce(ctx, drainCtx, pulse)
		if observe != nil && (err == nil ||
			(!errors.Is(err, ErrNoClaimableTurn) && !errors.Is(err, ErrAdmissionClosed))) {
			observe(result, err)
		}
		switch {
		case err == nil:
			continue
		case errors.Is(err, ErrAdmissionClosed):
			return waitForAdmissionShutdown(ctx)
		case errors.Is(err, ErrWorkerRestartRequired), errors.Is(err, ErrWorkerPluginLimitsUnavailable):
			// A non-cooperative executor or incomplete exact policy is a process
			// failure. Continuing would either accumulate leaked goroutines or run
			// later claims under a policy this Worker cannot enforce.
			return err
		case errors.Is(err, ErrNoClaimableTurn):
			if sleepErr := sleepContext(ctx, worker.options.IdleBackoff); sleepErr != nil {
				return sleepErr
			}
		case errors.Is(err, ErrWorkerFenceLost), errors.Is(err, ErrExecutorNonTerminal):
			// One Turn's epoch ended badly. That is not a reason to stop
			// serving every other queued Turn.
			continue
		case contextError(ctx) != nil:
			return contextError(ctx)
		default:
			// Store-level failures back off rather than spin.
			if sleepErr := sleepContext(ctx, worker.options.IdleBackoff); sleepErr != nil {
				return sleepErr
			}
		}
	}
}

func observeLoopPulse(pulse func()) {
	if pulse == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	pulse()
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type workerSession struct {
	worker     *Worker
	turn       Turn
	attempt    TurnAttempt
	epochCtx   context.Context
	progress   chan struct{}
	emitPermit chan struct{}

	cancelRequested atomic.Bool
	fenceLost       atomic.Bool
	emitted         atomic.Int64
	progressCommits atomic.Int64
	ended           atomic.Bool

	// leaseMu serializes HeartbeatAttempt with the one terminal CommitAttempt.
	// emitPermit provides a cancellable, bounded linearization point between
	// closing the Session and any non-terminal CommitAttempt already in flight.
	leaseMu sync.Mutex
}

func (session *workerSession) Turn() Turn                  { return cloneTurn(session.turn) }
func (session *workerSession) Attempt() TurnAttempt        { return session.attempt }
func (session *workerSession) CancellationRequested() bool { return session.cancelRequested.Load() }

func (session *workerSession) close() {
	session.ended.Store(true)
}

func (session *workerSession) waitForEmitStop(timeout time.Duration) bool {
	if timeout <= 0 {
		return false
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-session.emitPermit:
		session.emitPermit <- struct{}{}
		return true
	case <-timer.C:
		return false
	}
}

func (session *workerSession) Emit(ctx context.Context, operation OperationDraft) (CommitAttemptResult, error) {
	if err := contextError(ctx); err != nil {
		return CommitAttemptResult{}, err
	}
	if session.ended.Load() {
		return CommitAttemptResult{}, fmt.Errorf("execution session is closed")
	}
	commitCtx, release, err := bindWorkerEpochContext(ctx, session.epochCtx)
	if err != nil {
		return CommitAttemptResult{}, err
	}
	defer release()
	select {
	case <-commitCtx.Done():
		cause := context.Cause(commitCtx)
		if cause != nil {
			return CommitAttemptResult{}, cause
		}
		return CommitAttemptResult{}, commitCtx.Err()
	case <-session.emitPermit:
	}
	defer func() { session.emitPermit <- struct{}{} }()
	if session.ended.Load() {
		return CommitAttemptResult{}, fmt.Errorf("execution session is closed")
	}
	if session.fenceLost.Load() {
		return CommitAttemptResult{}, ErrWorkerFenceLost
	}
	event := operation.Event
	result, err := session.worker.store.CommitAttempt(commitCtx, CommitAttemptCommand{
		Fence:       session.attempt.Fence(),
		OperationID: operation.OperationID,
		Event:       &event,
		Effects:     operation.Effects,
	})
	if err != nil {
		if isOwnershipLoss(err) {
			session.fenceLost.Store(true)
		}
		return CommitAttemptResult{}, err
	}
	session.emitted.Add(1)
	if !result.Replay {
		session.progressCommits.Add(1)
		select {
		case session.progress <- struct{}{}:
		default:
		}
	}
	return result, nil
}

// bindWorkerEpochContext makes the execution epoch authoritative even when a
// domain executor passes context.Background to Emit. Either caller
// cancellation or epoch cancellation stops the storage call.
func bindWorkerEpochContext(caller, epoch context.Context) (context.Context, func(), error) {
	if err := contextError(caller); err != nil {
		return nil, nil, err
	}
	if err := contextError(epoch); err != nil {
		cause := context.Cause(epoch)
		if cause != nil {
			return nil, nil, cause
		}
		return nil, nil, err
	}
	bound, cancel := context.WithCancelCause(caller)
	stop := context.AfterFunc(epoch, func() {
		cause := context.Cause(epoch)
		if cause == nil {
			cause = epoch.Err()
		}
		cancel(cause)
	})
	return bound, func() {
		stop()
		cancel(context.Canceled)
	}, nil
}

// terminalOperationID is derived from the Attempt so a retried terminal commit
// resolves against the immutable Operation receipt instead of creating a
// second one.
func terminalOperationID(attemptID string) string {
	return "terminal:" + attemptID
}

func randomAttemptID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return "att_" + hex.EncodeToString(buffer), nil
}
