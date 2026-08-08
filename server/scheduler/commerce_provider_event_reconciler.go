package scheduler

import (
	"context"
	"fmt"
	"sync"
	"time"

	"server/globals"
	"server/service/commerce"

	"gorm.io/gorm"
)

const (
	defaultCommerceProviderEventReconcileInterval = 5 * time.Second
	defaultCommerceProviderEventReconcileBatch    = commerce.DefaultProviderEventBatch
	commerceProviderEventReconcilerWorkerID       = "commerce-provider-event-reconciler"
)

// CommerceProviderEventReconciler is the runtime safety net for durable
// provider events that were accepted but not completed by the inline webhook
// path. The commerce service remains the sole owner of claiming, fencing,
// retry, and terminal-state transitions; this type only supplies the periodic
// lifecycle around ProviderEventService.ReconcileOnce.
//
// A reconciler may be started and stopped repeatedly. Concurrent duplicate
// Start/Stop calls are idempotent, and Stop waits for the active loop to leave
// before returning.
type CommerceProviderEventReconciler struct {
	service   *commerce.ProviderEventService
	processor commerce.ProviderEventProcessor
	interval  time.Duration
	batch     int

	lifecycleMu sync.Mutex
	running     bool
	cancel      context.CancelFunc
	done        chan struct{}
}

// NewCommerceProviderEventReconciler constructs the production reconciler.
// The database and provider-specific processor are injected by the composition
// root so the scheduler package does not depend on API/callback packages.
func NewCommerceProviderEventReconciler(
	db *gorm.DB,
	processor commerce.ProviderEventProcessor,
) (*CommerceProviderEventReconciler, error) {
	return newCommerceProviderEventReconciler(
		db,
		processor,
		defaultCommerceProviderEventReconcileInterval,
		defaultCommerceProviderEventReconcileBatch,
	)
}

func newCommerceProviderEventReconciler(
	db *gorm.DB,
	processor commerce.ProviderEventProcessor,
	interval time.Duration,
	batch int,
) (*CommerceProviderEventReconciler, error) {
	if processor == nil {
		return nil, fmt.Errorf("commerce provider event reconciler requires a processor")
	}
	if interval <= 0 {
		return nil, fmt.Errorf("commerce provider event reconcile interval must be positive")
	}
	if batch <= 0 || batch > commerce.MaxProviderEventBatch {
		return nil, fmt.Errorf("commerce provider event reconcile batch must be between 1 and %d", commerce.MaxProviderEventBatch)
	}
	service, err := commerce.NewProviderEventService(db, commerce.ProviderEventServiceOptions{
		WorkerID: commerceProviderEventReconcilerWorkerID,
	})
	if err != nil {
		return nil, err
	}
	return &CommerceProviderEventReconciler{
		service: service, processor: processor, interval: interval, batch: batch,
	}, nil
}

// ReconcileOnce runs one bounded inbox pass. It is exported for operational
// hooks and deterministic tests; all durable state-machine work is delegated
// to the commerce service.
func (reconciler *CommerceProviderEventReconciler) ReconcileOnce(
	ctx context.Context,
) (commerce.ReconcileReport, error) {
	if reconciler == nil || reconciler.service == nil || reconciler.processor == nil {
		return commerce.ReconcileReport{}, fmt.Errorf("commerce provider event reconciler is not initialized")
	}
	return reconciler.service.ReconcileOnce(ctx, reconciler.processor, reconciler.batch)
}

func (reconciler *CommerceProviderEventReconciler) Start() {
	if reconciler == nil {
		return
	}
	reconciler.lifecycleMu.Lock()
	if reconciler.running {
		reconciler.lifecycleMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	reconciler.running = true
	reconciler.cancel = cancel
	reconciler.done = done
	reconciler.lifecycleMu.Unlock()

	go reconciler.run(ctx, done)
}

func (reconciler *CommerceProviderEventReconciler) Stop() {
	if reconciler == nil {
		return
	}
	reconciler.lifecycleMu.Lock()
	if !reconciler.running {
		reconciler.lifecycleMu.Unlock()
		return
	}
	cancel := reconciler.cancel
	done := reconciler.done
	reconciler.lifecycleMu.Unlock()

	cancel()
	<-done
}

func (reconciler *CommerceProviderEventReconciler) run(ctx context.Context, done chan struct{}) {
	defer func() {
		reconciler.lifecycleMu.Lock()
		if reconciler.done == done {
			reconciler.running = false
			reconciler.cancel = nil
			reconciler.done = nil
		}
		close(done)
		reconciler.lifecycleMu.Unlock()
	}()

	globals.Info("Commerce provider event reconciler started")
	defer globals.Info("Commerce provider event reconciler stopped")

	ticker := time.NewTicker(reconciler.interval)
	defer ticker.Stop()

	reconciler.reconcileGuarded(ctx)
	for {
		select {
		case <-ticker.C:
			reconciler.reconcileGuarded(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (reconciler *CommerceProviderEventReconciler) reconcileGuarded(ctx context.Context) {
	defer func() {
		if recover() != nil {
			// A provider payload or processor error may contain customer data.
			// Deliberately do not interpolate the recovered value into logs.
			globals.Error("[CommerceEvent] reconcile panic recovered")
		}
	}()

	startedAt := time.Now()
	report, err := reconciler.ReconcileOnce(ctx)
	if err != nil {
		if ctx.Err() == nil {
			// Do not log err: processor and driver errors can embed raw provider
			// payloads. The heartbeat exposes failure counts without that risk.
			globals.Error("[CommerceEvent] reconcile tick failed")
		}
		return
	}
	globals.Info(fmt.Sprintf(
		"[CommerceEvent] reconcile tick: scanned=%d processed=%d ignored=%d retried=%d manual_review=%d busy=%d failures=%d in %s",
		report.Scanned,
		report.Processed,
		report.Ignored,
		report.Retried,
		report.ManualReviews,
		report.Busy,
		len(report.Failures),
		time.Since(startedAt).Round(time.Millisecond),
	))
}
