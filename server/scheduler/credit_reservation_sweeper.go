package scheduler

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"server/globals"
	"server/service/account"
)

// Sweeper cadence + per-tick batch ceiling. The default 5-minute interval
// keeps zombie reservations bounded — a handler that crashes between Reserve
// and Finalize/Release will hold credits hostage for at most 5 min plus the
// safety margin past the reservation's TTL. The 200-row batch ceiling avoids
// long-running sweeps when a flood of stale rows builds up; the next tick
// picks up the remainder.
//
// safetyMargin pushes the cutoff back by one minute so handlers that finish
// right at expires_at don't race the sweeper to refund/finalize the same row.
const (
	defaultCreditReservationSweepInterval = 5 * time.Minute
	defaultCreditReservationSweepBatch    = 200
	defaultCreditReservationSweepMargin   = 1 * time.Minute
)

// CreditReservationSweeper periodically reclaims credits stuck in
// `reserved` state past their TTL — the safety net for handlers that crashed
// or hung between Reserve and Finalize/Release.
//
// Concurrency:
//   - isRunning is atomic so Start/Stop observe each other's
//     ordering without a mutex.
//   - stopChan is closed (never sent on) so Stop never blocks
//     while sweepOnce is mid-tick.
//   - stopOnce makes close() idempotent across repeated Stop calls.
//   - doneChan is closed by the run goroutine on exit; Stop waits
//     on it so callers observe full shutdown before returning.
//   - Panic recovery is per-sweep (inside the for-loop) rather
//     than per-goroutine, so a panic doesn't open a window where
//     the goroutine is dead but isRunning is still true.
type CreditReservationSweeper struct {
	isRunning atomic.Bool
	stopChan  chan struct{}
	doneChan  chan struct{}
	stopOnce  sync.Once
}

func NewCreditReservationSweeper() *CreditReservationSweeper {
	return &CreditReservationSweeper{
		stopChan: make(chan struct{}),
		doneChan: make(chan struct{}),
	}
}

func (s *CreditReservationSweeper) Start() {
	if !s.isRunning.CompareAndSwap(false, true) {
		return
	}
	go s.run()
}

func (s *CreditReservationSweeper) Stop() {
	if !s.isRunning.CompareAndSwap(true, false) {
		return
	}
	s.stopOnce.Do(func() { close(s.stopChan) })
	<-s.doneChan
}

func (s *CreditReservationSweeper) run() {
	defer close(s.doneChan)
	globals.Info("Credit reservation sweeper started")
	defer globals.Info("Credit reservation sweeper stopped")

	ticker := time.NewTicker(defaultCreditReservationSweepInterval)
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
// sweeper goroutine.
func (s *CreditReservationSweeper) runSweepGuarded() {
	defer func() {
		if r := recover(); r != nil {
			globals.Error(fmt.Sprintf("Credit reservation sweeper panic recovered: %v", r))
			time.Sleep(5 * time.Second)
		}
	}()
	s.sweepOnce()
}

func (s *CreditReservationSweeper) sweepOnce() {
	svc := account.NewCreditReservationService()
	start := time.Now()
	swept, failed, err := svc.SweepExpiredReservations(
		nil, // default DB
		defaultCreditReservationSweepBatch,
		defaultCreditReservationSweepMargin,
	)
	if err != nil {
		globals.Error(fmt.Sprintf("[CreditReservation] sweep tick failed: %v", err))
		return
	}
	// Always log a heartbeat so ops can confirm the sweeper is alive
	// without inspecting the DB. Without this, a deadlocked / stuck
	// sweeper looks identical to a quiet one (no zombies to expire).
	globals.Info(fmt.Sprintf("[CreditReservation] sweep tick: %d expired, %d failed in %s",
		swept, failed, time.Since(start).Round(time.Millisecond)))
}
