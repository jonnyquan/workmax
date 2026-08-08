//go:build desktop

package sync

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// StuckWarnThreshold is the consecutiveFail count at which the
// worker emits a one-shot [sync-stuck] WARN. Per-tick failures
// already log via the JobFunc's own error path; this is the
// "we've been failing in a row" signal ops wants to grep for to
// distinguish "transient hiccup" from "this user is wedged".
//
// 3 is a sensible default given the 1s/4s/16s/60s backoff
// sequence — by tick 3 we're already 21 seconds into the
// backoff, which is well past any transient network glitch.
const StuckWarnThreshold = 3

// JobFunc is the work the SyncWorker performs each tick. Returns
// nil on success or an error to trigger backoff. The worker
// catches panics — a panicking job logs + becomes a failed tick
// rather than crashing the whole sidecar.
//
// Implementations should be idempotent: a JobFunc may run multiple
// times for the same logical event because trigger debouncing
// collapses N triggers into 1 tick, not because the work itself
// repeats. (P1.B.3's job will be "fetch threads delta + write to
// SQLite"; running it twice in a row is harmless — second call
// pulls 0 deltas if nothing changed.)
type JobFunc func(ctx context.Context) error

// SyncWorker runs a single background goroutine that fires JobFunc
// on triggers (manual or periodic) with exponential backoff on
// failure. Single-writer by design: at most one JobFunc runs at
// any time, so the job body doesn't need its own concurrency
// guards on the SQLite cache.
//
// Lifecycle:
//
//	w := NewSyncWorker(job, Config{PeriodicInterval: 5 * time.Minute})
//	w.Start(ctx)           // launches goroutine
//	w.Trigger("user-pull") // optional: tells worker to run now
//	<-ctx.Done()           // worker stops + closes its done channel
//	<-w.Done()             // wait for graceful exit
//
// Triggers are coalesced via a 1-buffered channel: if the worker
// is currently running OR a trigger is already pending, additional
// triggers are dropped (the next tick will see all the work
// anyway since JobFunc reads fresh state).
type SyncWorker struct {
	job   JobFunc
	cfg   Config
	nowFn func() time.Time // injectable for tests; production = time.Now

	triggers chan triggerReason
	done     chan struct{}

	mu              sync.Mutex
	running         bool
	consecutiveFail int
	lastTickAt      time.Time
	lastError       error

	// Lifetime counters surfaced via Snapshot for /system/diagnostics.
	// Reset only on process restart — they're operational telemetry,
	// not behavior inputs.
	totalTicks   int64
	totalFails   int64
	lastDuration time.Duration

	// stuckWarnEmitted gates the one-shot [sync-stuck] WARN — emit
	// when consecutiveFail first crosses StuckWarnThreshold, then
	// stay silent until a success resets it. Without this gate the
	// log would fire once per backoff tick (1s, 4s, 16s, 60s, 60s…
	// = noisy fast then bandwidth-quiet but still spammy long-term).
	stuckWarnEmitted bool

	// Name is an optional label that prefixes the [sync-stuck]
	// WARN so ops can distinguish ThreadsSyncer from a future
	// second worker. Empty → bare "[sync-stuck]" tag.
	name string
}

// triggerReason carries the source of a trigger (startup / periodic
// / manual). Useful for diagnostics; not load-bearing for behavior.
type triggerReason struct {
	source string
	at     time.Time
}

// Config tunes the worker. Zero values are usable: PeriodicInterval=0
// disables the periodic loop (handy for tests that drive ticks
// manually via Trigger).
type Config struct {
	// PeriodicInterval is how often the worker auto-triggers without
	// any manual prompt. Production: 5 minutes (cloud-sync.md §4).
	// Zero disables — tests and the "burst mode" of an offline-just-
	// reconnected session can set 0 + drive Triggers manually.
	PeriodicInterval time.Duration

	// BackoffSequence is the wait between retries after a failed
	// JobFunc. Walks through the slice on consecutive failures,
	// pinned at the last entry once exhausted, resets to zero on
	// success.
	//
	// Default (set by NewSyncWorker if empty): 1s, 4s, 16s, 60s
	// (cloud-sync.md §4.2).
	BackoffSequence []time.Duration

	// Name labels the worker in operational log lines like
	// [sync-stuck]. Optional — empty omits the prefix. Useful when
	// running multiple workers in one process so ops can grep for
	// the specific one.
	Name string
}

// NewSyncWorker constructs a worker. Job is required (nil panics —
// programming error). Config can be Config{} for "use defaults".
func NewSyncWorker(job JobFunc, cfg Config) *SyncWorker {
	if job == nil {
		panic("sync: NewSyncWorker requires non-nil JobFunc")
	}
	if len(cfg.BackoffSequence) == 0 {
		cfg.BackoffSequence = defaultBackoffSequence()
	}
	return &SyncWorker{
		job:      job,
		cfg:      cfg,
		name:     cfg.Name,
		nowFn:    func() time.Time { return time.Now().UTC() },
		triggers: make(chan triggerReason, 1),
		done:     make(chan struct{}),
	}
}

func defaultBackoffSequence() []time.Duration {
	return []time.Duration{
		1 * time.Second,
		4 * time.Second,
		16 * time.Second,
		60 * time.Second,
	}
}

// Start launches the worker goroutine. Idempotent against double
// Start calls would be nice — but in practice this is called once
// from main.go right after the cloud_proxy + sidecar HTTP server
// boot, so we don't bother guarding. Calling Start twice WOULD
// run two goroutines (race on the trigger channel). If we ever
// need defense, add a sync.Once.
//
// Always fires an initial "startup" trigger so a fresh-launched
// sidecar attempts sync immediately rather than waiting up to
// PeriodicInterval.
func (w *SyncWorker) Start(ctx context.Context) {
	go w.run(ctx)
	w.Trigger("startup")
}

// Trigger asks the worker to run JobFunc as soon as it's idle.
// Non-blocking: if a trigger is already pending OR the worker is
// running, this call is silently coalesced (the next tick will
// pick up whatever state the world is in).
//
// source is logged + used for diagnostics; pass anything descriptive
// ("startup", "user-pull", "reconnect", etc.).
func (w *SyncWorker) Trigger(source string) {
	select {
	case w.triggers <- triggerReason{source: source, at: w.nowFn()}:
	default:
		// Already a trigger pending or worker is processing one —
		// drop. JobFunc will see fresh state on its next tick.
	}
}

// Done returns a channel closed when the worker goroutine exits.
// Callers should wait on this before considering shutdown complete
// (otherwise an in-flight JobFunc might still be writing SQLite
// when the process exits and corrupt the DB).
func (w *SyncWorker) Done() <-chan struct{} { return w.done }

// Snapshot exposes the worker's observable state for /health-style
// introspection. Safe to call from any goroutine.
//
// Counters (TotalTicks, TotalFails) are lifetime since process start;
// they enable ops to compute success rate over a window via two
// samples. LastDuration is the wall-clock cost of the most recent
// JobFunc invocation — useful for spotting a degrading cloud or a
// pathological cursor.
type Snapshot struct {
	Running         bool          // a JobFunc is currently executing
	ConsecutiveFail int           // count of back-to-back failures since last success
	LastTickAt      time.Time     // wall-clock of the most recent JobFunc invocation (zero = never)
	LastError       string        // most recent error, "" on success or never-run
	TotalTicks      int64         // lifetime tick count since process start
	TotalFails      int64         // lifetime failed tick count
	LastDuration    time.Duration // duration of the most recent JobFunc invocation
}

func (w *SyncWorker) Snapshot() Snapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	errStr := ""
	if w.lastError != nil {
		errStr = w.lastError.Error()
	}
	return Snapshot{
		Running:         w.running,
		ConsecutiveFail: w.consecutiveFail,
		LastTickAt:      w.lastTickAt,
		LastError:       errStr,
		TotalTicks:      w.totalTicks,
		TotalFails:      w.totalFails,
		LastDuration:    w.lastDuration,
	}
}

func (w *SyncWorker) run(ctx context.Context) {
	defer close(w.done)

	var periodicC <-chan time.Time
	if w.cfg.PeriodicInterval > 0 {
		t := time.NewTicker(w.cfg.PeriodicInterval)
		defer t.Stop()
		periodicC = t.C
	}

	// backoffTimer is reset by tickOnce on failure to schedule the
	// next attempt. nil channel = no pending backoff. The reset path
	// uses an explicit timer (vs time.After) so we can stop it on
	// ctx cancellation and avoid a goroutine leak.
	var backoffTimer *time.Timer
	defer func() {
		if backoffTimer != nil {
			backoffTimer.Stop()
		}
	}()

	for {
		var backoffC <-chan time.Time
		if backoffTimer != nil {
			backoffC = backoffTimer.C
		}

		select {
		case <-ctx.Done():
			return
		case <-w.triggers:
			// Manual or startup trigger. Any pending backoff is
			// implicitly cancelled — the trigger short-circuits the
			// retry wait.
			if backoffTimer != nil {
				backoffTimer.Stop()
				backoffTimer = nil
			}
			backoffTimer = w.tickOnce(ctx)
		case <-periodicC:
			if backoffTimer != nil {
				// Skip periodic if we're in backoff; the backoff
				// retry will fire its own attempt soon.
				continue
			}
			backoffTimer = w.tickOnce(ctx)
		case <-backoffC:
			backoffTimer = nil
			backoffTimer = w.tickOnce(ctx)
		}
	}
}

// tickOnce runs JobFunc once and returns a non-nil timer if the
// job failed (= caller should wait on it before re-running).
// Returns nil on success — the worker idles until the next
// trigger / periodic tick.
//
// Updates the lifetime counters (TotalTicks/TotalFails) and the
// LastDuration gauge so Snapshot's caller sees up-to-date metrics.
func (w *SyncWorker) tickOnce(ctx context.Context) *time.Timer {
	w.markRunning(true)
	defer w.markRunning(false)

	startedAt := w.nowFn()
	err := safeJob(ctx, w.job)
	duration := w.nowFn().Sub(startedAt)

	w.mu.Lock()
	w.lastTickAt = startedAt
	w.lastDuration = duration
	w.lastError = err
	w.totalTicks++
	if err == nil {
		w.consecutiveFail = 0
		// Success — reset the stuck-warn gate so the NEXT stuck
		// stretch emits a fresh WARN. Without this reset, a flap
		// (stuck → recover → stuck again) would only log once at
		// the very first stuck.
		w.stuckWarnEmitted = false
		w.mu.Unlock()
		return nil
	}
	w.totalFails++
	w.consecutiveFail++
	// Snapshot the values we need for the (possible) WARN before
	// releasing the lock — logging while holding the mutex would
	// serialize a log syscall with concurrent Snapshot() calls.
	shouldWarn := w.consecutiveFail == StuckWarnThreshold && !w.stuckWarnEmitted
	consecutiveFail := w.consecutiveFail
	lastErrMsg := ""
	if err != nil {
		lastErrMsg = err.Error()
	}
	if shouldWarn {
		w.stuckWarnEmitted = true
	}
	delay := w.backoffDelayLocked()
	w.mu.Unlock()

	if shouldWarn {
		w.emitStuckWarn(consecutiveFail, lastErrMsg)
	}
	return time.NewTimer(delay)
}

// emitStuckWarn writes the one-shot [sync-stuck] WARN log line.
// Includes the worker's optional Name (so 'threads' vs a future
// second worker are distinguishable) and the most recent error
// so ops doesn't have to also grep per-tick logs.
func (w *SyncWorker) emitStuckWarn(consecutiveFail int, errMsg string) {
	prefix := "[sync-stuck]"
	if w.name != "" {
		prefix = fmt.Sprintf("[sync-stuck:%s]", w.name)
	}
	log.Printf("%s worker has failed %d consecutive ticks (threshold %d); last error: %s",
		prefix, consecutiveFail, StuckWarnThreshold, errMsg)
}

// safeJob wraps job() with panic recovery. A panicking JobFunc
// surfaces as a regular error so the backoff path engages instead
// of killing the sidecar process.
func safeJob(ctx context.Context, job JobFunc) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &panicError{recovered: r}
		}
	}()
	return job(ctx)
}

type panicError struct {
	recovered any
}

func (e *panicError) Error() string {
	return "sync: job panicked: " + sprintfAny(e.recovered)
}

// sprintfAny renders the recovered panic value for the panicError
// message. String + error get the cheap fast paths; anything else
// (struct literal, integer, etc.) falls through to %v so ops can
// see WHAT panicked rather than an opaque sentinel.
func sprintfAny(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if err, ok := v.(error); ok {
		return err.Error()
	}
	return fmt.Sprintf("%v", v)
}

// backoffDelayLocked picks the wait time given consecutiveFail.
// Caller MUST hold w.mu.
func (w *SyncWorker) backoffDelayLocked() time.Duration {
	idx := w.consecutiveFail - 1 // first failure → cfg.BackoffSequence[0]
	if idx < 0 {
		idx = 0
	}
	if idx >= len(w.cfg.BackoffSequence) {
		idx = len(w.cfg.BackoffSequence) - 1
	}
	return w.cfg.BackoffSequence[idx]
}

func (w *SyncWorker) markRunning(running bool) {
	w.mu.Lock()
	w.running = running
	w.mu.Unlock()
}
