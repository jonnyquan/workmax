package scheduler

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"server/globals"
	desktopsync "server/service/desktop/sync"
)

// Sweeper cadence + retention + per-tick batch ceiling.
//
// Cadence: hourly. Tombstones don't accumulate fast (a single user
// deletes maybe N threads/messages per day; a 1000-user org might
// produce a few thousand tombstones/day); hourly gives enough room
// for the per-tick batch ceiling to catch up without lock pressure.
//
// Retention: 90 days. Desktop clients that haven't synced in 90
// days are expected to re-sync everything from scratch on next
// connect anyway (their thread/message cursors are also stale by
// then), so dropped tombstones don't manifest as user-visible
// drift. Matches migration 20260642's doc comment.
//
// Batch: 1000 rows/tick. Bounds the per-tick DELETE so a long-
// overdue first sweep on a tombstone-heavy table doesn't lock
// for minutes. The run loop re-invokes prune until 0 rows
// remain, so this caps single-statement scope without capping
// total throughput.
const (
	defaultTombstoneSweepInterval  = 1 * time.Hour
	defaultTombstoneSweepRetention = 90 * 24 * time.Hour
	defaultTombstoneSweepBatch     = 1000
)

// TombstoneSweeper periodically GCs tombstone rows older than the
// retention window. Single-goroutine + close-on-stop + per-iteration
// panic-recover pattern.
//
// Per P1.A.5b's tombstone design, the table grows unbounded
// without GC; this sweeper closes that loop. Distinct from
// CreditReservationSweeper in that there's no idempotency /
// safety-margin concern — tombstones older than 90 days are
// definitively stale (no client could legitimately need them).
//
// Concurrency:
//   - isRunning is atomic so Start/Stop observe each other's
//     ordering without a mutex.
//   - stopChan is closed (never sent on) so Stop never blocks
//     even while sweepOnce holds the goroutine.
//   - stopOnce makes close() idempotent across repeated Stop calls.
//   - doneChan is closed by the run goroutine on exit; Stop waits
//     on it so callers observe full shutdown before the function
//     returns. Without this wait, tests (and ops shutdown) saw
//     races between in-flight sweepOnce DB access and
//     post-Stop teardown of those resources.
//   - Panic recovery is per-sweep (inside the for-loop) rather
//     than per-goroutine. A panic in sweepOnce logs + sleeps +
//     continues the loop without re-spawning, which means there's
//     never a window where the goroutine has died but Stop()
//     hasn't been told.
type TombstoneSweeper struct {
	isRunning atomic.Bool
	stopChan  chan struct{}
	doneChan  chan struct{}
	stopOnce  sync.Once
}

func NewTombstoneSweeper() *TombstoneSweeper {
	return &TombstoneSweeper{
		stopChan: make(chan struct{}),
		doneChan: make(chan struct{}),
	}
}

func (s *TombstoneSweeper) Start() {
	if !s.isRunning.CompareAndSwap(false, true) {
		return
	}
	go s.run()
}

func (s *TombstoneSweeper) Stop() {
	if !s.isRunning.CompareAndSwap(true, false) {
		return
	}
	s.stopOnce.Do(func() { close(s.stopChan) })
	<-s.doneChan // wait for run() to fully exit
}

func (s *TombstoneSweeper) run() {
	defer close(s.doneChan)
	globals.Info("Tombstone sweeper started")
	defer globals.Info("Tombstone sweeper stopped")

	ticker := time.NewTicker(defaultTombstoneSweepInterval)
	defer ticker.Stop()

	s.runSweepGuarded()

	for {
		select {
		case <-ticker.C:
			s.runSweepGuarded()
		case <-s.stopChan:
			return
		}
	}
}

// runSweepGuarded wraps sweepOnce in defer-recover so a panic in
// the sweep path is logged + paused-on but doesn't kill the
// sweeper goroutine. Keeping the recover here (per-iteration)
// rather than at the run() level means there's never a window
// where the goroutine has died but Stop() hasn't been told.
func (s *TombstoneSweeper) runSweepGuarded() {
	defer func() {
		if r := recover(); r != nil {
			globals.Error(fmt.Sprintf("Tombstone sweeper panic recovered: %v", r))
			// Pause before the next tick fires so we don't busy-loop
			// on a deterministic panic (e.g. corrupted query).
			time.Sleep(5 * time.Second)
		}
	}()
	s.sweepOnce()
}

// sweepOnce drains all eligible tombstones via repeated batched
// DELETEs until zero rows are affected. Bounded total work per
// tick = (batch * iterations); iterations are capped by
// maxIterationsPerTick so a pathologically large backlog doesn't
// monopolize the goroutine — the remainder picks up on the next
// hourly tick.
//
// Between iterations we non-blockingly peek at stopChan so Stop()
// during a long backlog terminates promptly rather than waiting
// for the iteration cap to elapse.
func (s *TombstoneSweeper) sweepOnce() {
	const maxIterationsPerTick = 50 // = 50_000 rows/tick max
	start := time.Now()
	total := int64(0)
	for i := 0; i < maxIterationsPerTick; i++ {
		select {
		case <-s.stopChan:
			globals.Info(fmt.Sprintf("[Tombstone] sweep aborted after %d rows (stop requested)", total))
			return
		default:
		}
		n, err := desktopsync.PruneTombstones(
			globals.GraDBs["system"],
			defaultTombstoneSweepRetention,
			defaultTombstoneSweepBatch,
		)
		if err != nil {
			globals.Error(fmt.Sprintf("[Tombstone] sweep iter %d failed: %v", i, err))
			return
		}
		total += n
		if n == 0 {
			break // drained
		}
	}
	// Always log a heartbeat so ops can confirm the sweeper is
	// alive without inspecting the DB.
	globals.Info(fmt.Sprintf("[Tombstone] sweep tick: %d rows pruned in %s",
		total, time.Since(start).Round(time.Millisecond)))
}
