package scheduler

import (
	"fmt"
	"time"

	"server/globals"
	"server/service/account"
)

const (
	defaultSubscriptionCreditsInterval = time.Hour
	defaultSubscriptionCreditsBatch    = 500
)

// SubscriptionCreditsScheduler keeps deferred subscription credits out of
// hot read paths. Order/webhook flows grant immediately; this periodic job is
// the safety net for missed renewals, deploy gaps, and legacy rows.
type SubscriptionCreditsScheduler struct {
	isRunning  bool
	stopChan   chan struct{}
	lastUserID uint
}

func NewSubscriptionCreditsScheduler() *SubscriptionCreditsScheduler {
	return &SubscriptionCreditsScheduler{stopChan: make(chan struct{})}
}

func (s *SubscriptionCreditsScheduler) Start() {
	if s.isRunning {
		return
	}
	s.isRunning = true
	go s.run()
}

func (s *SubscriptionCreditsScheduler) Stop() {
	if !s.isRunning {
		return
	}
	s.isRunning = false
	s.stopChan <- struct{}{}
}

func (s *SubscriptionCreditsScheduler) run() {
	defer func() {
		if r := recover(); r != nil {
			globals.Error(fmt.Sprintf("Subscription credits scheduler panic recovered: %v", r))
			time.Sleep(5 * time.Second)
			if s.isRunning {
				go s.run()
			}
		}
	}()

	globals.Info("Subscription credits scheduler started")

	ticker := time.NewTicker(defaultSubscriptionCreditsInterval)
	defer ticker.Stop()

	s.backfillOnce()

	for {
		select {
		case <-ticker.C:
			s.backfillOnce()
		case <-s.stopChan:
			globals.Info("Subscription credits scheduler stopped")
			return
		}
	}
}

func (s *SubscriptionCreditsScheduler) backfillOnce() {
	start := time.Now()
	svc := account.NewCreditsPackService()
	ensured, skipped, failed, lastUserID, hasMore, err := svc.BackfillActiveSubscriptionCreditsAfter(nil, s.lastUserID, defaultSubscriptionCreditsBatch)
	if err != nil {
		globals.Error(fmt.Sprintf("[CreditsPack] subscription backfill tick failed: %v", err))
		return
	}
	if hasMore {
		s.lastUserID = lastUserID
	} else {
		s.lastUserID = 0
	}
	globals.Info(fmt.Sprintf("[CreditsPack] subscription backfill tick: %d ensured, %d skipped, %d failed, cursor=%d, next=%d in %s",
		ensured, skipped, failed, lastUserID, s.lastUserID, time.Since(start).Round(time.Millisecond)))
}
