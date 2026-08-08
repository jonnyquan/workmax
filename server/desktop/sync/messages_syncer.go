//go:build desktop

package sync

import (
	"context"
	"log"
	"sync"
	"sync/atomic"

	"gorm.io/gorm"

	cloudproxy "server/desktop/cloud_proxy"
)

// MessagesSyncerDeps bundles what the syncer needs to construct a
// per-trigger JobFunc. Same fields as MessagesJobDeps minus the
// per-trigger params (thread_uuid + cloud_thread_id + expected_uid are passed
// in at Trigger time).
type MessagesSyncerDeps struct {
	DB          *gorm.DB
	Cloud       *cloudproxy.Client
	TokenStore  *cloudproxy.TokenStore
	CursorStore *CursorStore

	// ParentCtx is the sidecar's shutdown ctx (cancelled when the
	// process is exiting). Passed into the spawned MessagesJob so
	// cooperative cancellation (HTTP request cancel, DB-driver
	// context check) fires when the process tears down.
	//
	// Note: cancellation SIGNALS the job to exit; it does not WAIT
	// for completion. Production shutdown cancels ParentCtx, calls
	// Drain(), then closes SQLite so in-flight writes have completed.
	//
	// If nil, context.Background() is used — fine for tests, not
	// recommended for production.
	ParentCtx context.Context
}

// MessagesSyncer is the on-demand coordinator for per-thread
// message sync. Different from SyncWorker (P1.B.1):
//
//   - SyncWorker fires periodically (5min ticker) for a SINGLE
//     entity — threads. Suitable when "sync everything every N
//     minutes" matches the desired UX.
//
//   - MessagesSyncer fires ON DEMAND — when the renderer asks for
//     a thread's messages via /agent/threads/:uuid/messages, the
//     handler calls Trigger() to kick off a background sync for
//     THAT thread. The renderer's 5s polling picks up the new rows
//     once the goroutine writes them.
//
// Why on-demand vs periodic for messages:
//
//   - Messages are scoped per-thread; the cloud endpoint takes
//     thread_id. Periodic sync would need to walk every local
//     thread every tick → N HTTP calls per tick. Wasteful when
//     most users only ever look at 1-3 threads.
//   - The "user opens a thread → its messages appear" UX needs
//     low latency. On-demand triggers immediately when the user
//     clicks; periodic would wait up to 5min.
//
// Coalescing: a Trigger for a thread that's currently syncing is
// a no-op (the in-flight goroutine will pick up the latest cloud state for its
// frozen subject when it actually reads; it can never adopt a replacement
// login). Per-thread coalesce, not global — two threads can sync concurrently.
type MessagesSyncer struct {
	deps MessagesSyncerDeps

	mu            sync.Mutex
	activeThreads map[string]bool // thread_uuid → currently syncing
	// stuckCounts tracks per-thread consecutive failures (success
	// resets to 0 / removes the entry). When a thread crosses
	// StuckWarnThreshold we emit a one-shot [sync-stuck:messages]
	// WARN — parity with SyncWorker. Without this, each per-thread
	// failure logs individually but ops has no aggregate "this
	// thread has been wedged across N triggers" signal.
	stuckCounts        map[string]int
	stuckWarnedThreads map[string]bool

	// For test introspection. Production callers don't read.
	totalTriggered atomic.Int64

	// Test-only seam: override the JobFunc the syncer would
	// otherwise build via NewMessagesJob. Nil in production; tests
	// set this to inject a JobFunc that panics / returns specific
	// errors / counts invocations. Keeping it package-private and
	// nil-by-default means production behavior is unaffected.
	jobForTest func(
		threadUUID string,
		cloudThreadID, expectedUID uint64,
		lease cloudproxy.SessionLease,
	) func(context.Context) error

	// Shutdown coordination — Drain() flips closing to true and
	// waits on wg. After Drain returns, no new goroutine is in
	// flight, so the caller can safely close shared state (the
	// SQLite DB, the cloud HTTP client) without racing an in-flight
	// write.
	//
	// Without this, the sidecar's process exit can interleave with
	// an in-flight cache_writer write → SQLite WAL corruption
	// (rare but bad). Drain at shutdown is the cheap insurance.
	closing   bool
	wg        sync.WaitGroup
	drainOnce sync.Once
	drained   chan struct{}
}

// NewMessagesSyncer constructs the coordinator. Validates deps up
// front; panics on nil DB / Cloud / TokenStore / CursorStore
// because those are programming errors at wire-up time.
func NewMessagesSyncer(deps MessagesSyncerDeps) *MessagesSyncer {
	if deps.DB == nil || deps.Cloud == nil || deps.TokenStore == nil || deps.CursorStore == nil {
		panic("sync: NewMessagesSyncer requires non-nil DB, Cloud, TokenStore, CursorStore")
	}
	if deps.ParentCtx == nil {
		deps.ParentCtx = context.Background()
	}
	return &MessagesSyncer{
		deps:               deps,
		activeThreads:      make(map[string]bool, 4),
		stuckCounts:        make(map[string]int, 4),
		stuckWarnedThreads: make(map[string]bool, 4),
		drained:            make(chan struct{}),
	}
}

// Trigger fires a one-shot messages sync for the given thread and account.
// expectedUID is the authenticated subject that selected the local thread; it
// is frozen before the goroutine starts and must match every token used by the
// job, including a token rotated during 401 recovery.
// Returns true if a new sync was started, false otherwise. False
// covers three cases:
//
//  1. Invalid input — empty threadUUID, zero cloudThreadID, or zero expectedUID
//     (defensive guard; caller bug at wire-up time);
//  2. Coalesce — a sync for this thread is already in flight; the
//     in-flight read will pick up whatever cloud state existed at
//     read time for its frozen subject, including any change that motivated
//     this trigger;
//  3. Shutdown — Drain has closed the syncer to new work.
//
// Asynchronous: returns immediately. The actual sync runs on a
// goroutine. Failures are logged + dropped — the next Trigger for
// this thread will retry from the cursor that was saved before
// the failure (idempotent retry).
//
// A panic in the JobFunc is recovered in runSync; the goroutine
// exits cleanly, the active-flag is cleared (so subsequent
// Triggers aren't deadlocked), and the panic is logged.
func (s *MessagesSyncer) Trigger(threadUUID string, cloudThreadID, expectedUID uint64) bool {
	return s.triggerForSession(cloudproxy.SessionLease{}, threadUUID, cloudThreadID, expectedUID)
}

// triggerForSession is used by ThreadsJob periodic fan-out. requiredLease is
// the thread tick's frozen epoch; a non-zero value must still be current and
// must equal the lease frozen for this message job. The job context is bound to
// the syncer's long-lived parent rather than the short-lived thread-tick
// context, so successful fan-out can outlive the tick while logout/re-login
// still cancels it.
func (s *MessagesSyncer) triggerForSession(
	requiredLease cloudproxy.SessionLease,
	threadUUID string,
	cloudThreadID, expectedUID uint64,
) bool {
	if threadUUID == "" || cloudThreadID == 0 || expectedUID == 0 {
		return false
	}
	if requiredLease.Epoch() != 0 {
		if err := requiredLease.Check(); err != nil {
			return false
		}
	}
	lease, err := s.deps.TokenStore.AcquireSessionLease()
	if err != nil {
		return false
	}
	if requiredLease.Epoch() != 0 && !lease.SameSession(requiredLease) {
		return false
	}
	jobCtx, releaseLease := lease.BindContext(s.deps.ParentCtx)
	if err := checkSessionContext(jobCtx, lease); err != nil {
		releaseLease()
		return false
	}
	if requiredLease.Epoch() != 0 {
		if err := requiredLease.Check(); err != nil {
			releaseLease()
			return false
		}
	}
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		releaseLease()
		return false
	}
	if s.activeThreads[threadUUID] {
		s.mu.Unlock()
		releaseLease()
		return false
	}
	s.activeThreads[threadUUID] = true
	// Add while holding the same mutex Drain uses to flip closing.
	// This prevents the WaitGroup misuse window where Drain starts
	// Wait() on a zero counter while a concurrent Trigger is between
	// its closed-state check and wg.Add(1).
	s.wg.Add(1)
	s.mu.Unlock()
	s.totalTriggered.Add(1)

	// expectedUID is passed by value into the goroutine. It is the immutable
	// subject selected by the trigger even if login replaces TokenStore before
	// the goroutine reaches its first AcquireAccessToken.
	go s.runSync(jobCtx, releaseLease, lease, threadUUID, cloudThreadID, expectedUID)
	return true
}

// runSync builds + invokes a one-shot MessagesJob, cleans up the
// active flag on exit (so the next Trigger for the same thread
// can fire). Recovers from panics in the job so a bug in the
// sync code path doesn't crash the sidecar — the cleanup defer
// still runs, the panic gets logged, and the next Trigger for
// the same thread proceeds normally.
func (s *MessagesSyncer) runSync(
	jobCtx context.Context,
	releaseLease context.CancelFunc,
	lease cloudproxy.SessionLease,
	threadUUID string,
	cloudThreadID, expectedUID uint64,
) {
	defer s.wg.Done()
	defer releaseLease()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("messages syncer (%s): panic recovered: %v", threadUUID, r)
		}
		s.mu.Lock()
		delete(s.activeThreads, threadUUID)
		s.mu.Unlock()
	}()

	var job func(context.Context) error
	if s.jobForTest != nil {
		job = s.jobForTest(threadUUID, cloudThreadID, expectedUID, lease)
	} else {
		job = NewMessagesJob(MessagesJobDeps{
			DB:            s.deps.DB,
			Cloud:         s.deps.Cloud,
			TokenStore:    s.deps.TokenStore,
			CursorStore:   s.deps.CursorStore,
			ThreadUUID:    threadUUID,
			CloudThreadID: cloudThreadID,
			ExpectedUID:   expectedUID,
			ExpectedLease: lease,
		})
	}
	err := job(jobCtx)
	if err != nil {
		log.Printf("messages syncer (%s): %v", threadUUID, err)
	}
	s.recordOutcome(threadUUID, err)
}

// recordOutcome updates the per-thread consecutive-failure counter
// and, when a thread crosses StuckWarnThreshold, emits a one-shot
// [sync-stuck:messages] WARN. Success clears the counter so a
// recovered thread re-arms the warn for any future stuck stretch.
//
// Mirrors SyncWorker's stuck-warn semantics (worker.go) — same
// threshold constant, same one-shot-per-stretch model. The map
// tracks thread-keyed state rather than a single counter because
// MessagesSyncer is per-thread.
func (s *MessagesSyncer) recordOutcome(threadUUID string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err == nil {
		// Recovery: reset the counter + clear the warned flag so a
		// future stuck stretch on the same thread re-emits.
		delete(s.stuckCounts, threadUUID)
		delete(s.stuckWarnedThreads, threadUUID)
		return
	}

	s.stuckCounts[threadUUID]++
	if s.stuckCounts[threadUUID] == StuckWarnThreshold && !s.stuckWarnedThreads[threadUUID] {
		s.stuckWarnedThreads[threadUUID] = true
		// Snapshot the count before unlocking to avoid logging while
		// holding the mutex (would serialize log writes with Trigger).
		count := s.stuckCounts[threadUUID]
		errMsg := err.Error()
		// We release the lock for the log call by emitting via a
		// goroutine — but a synchronous log is fine here since the
		// runSync goroutine isn't contended (per-thread coalesce
		// guarantees one runSync per thread at a time). Keep simple.
		log.Printf("[sync-stuck:messages] thread %s has failed %d consecutive ticks (threshold %d); last error: %s",
			threadUUID, count, StuckWarnThreshold, errMsg)
	}
}

// ActiveCount returns the number of threads currently being synced.
// Useful for diagnostics / introspection; not used in production
// hot paths.
func (s *MessagesSyncer) ActiveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.activeThreads)
}

// TotalTriggered returns the lifetime count of Trigger() calls
// that actually started a new sync (i.e. weren't coalesced away).
// Test introspection; safe to call from any goroutine.
func (s *MessagesSyncer) TotalTriggered() int64 {
	return s.totalTriggered.Load()
}

// Drain blocks until all in-flight per-thread goroutines have
// finished, and prevents new Triggers from starting work. Call
// once at process shutdown — after Drain returns, the caller can
// safely close the underlying SQLite DB and cloud HTTP client
// without racing an in-flight cache_writer write.
//
// Idempotent: subsequent calls return immediately without
// re-blocking. Safe to invoke from any goroutine.
//
// Drain does NOT cancel in-flight work — it WAITS. The JobFunc
// path already respects ParentCtx cancellation; production wiring
// typically cancels ParentCtx first (so jobs see ctx.Done and
// exit early) and THEN calls Drain to wait for the wind-down.
// Drain on its own works even without ParentCtx wiring; in-flight
// jobs simply complete naturally.
func (s *MessagesSyncer) Drain() {
	<-s.beginDrain()
}

// DrainContext starts the same one-way drain as Drain but lets shutdown code
// keep its wall-clock budget. A deadline never reopens the syncer: new Trigger
// calls remain rejected, and the shared drained channel closes when the last
// cooperative job eventually exits. Callers must not close SQLite after a
// timeout because an outstanding job may still own a read or write.
func (s *MessagesSyncer) DrainContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-s.beginDrain():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *MessagesSyncer) beginDrain() <-chan struct{} {
	s.drainOnce.Do(func() {
		s.mu.Lock()
		s.closing = true
		s.mu.Unlock()
		go func() {
			s.wg.Wait()
			close(s.drained)
		}()
	})
	return s.drained
}
