package scheduler

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"server/globals"
	"server/model"
	"server/service/commerce"
	"server/utils/testutil"

	"gorm.io/gorm"
)

type reconcileProcessor struct {
	mu      sync.Mutex
	calls   int
	called  chan struct{}
	payload []byte
}

func (processor *reconcileProcessor) Prepare(
	_ context.Context,
	snapshot commerce.ProviderEventSnapshot,
) (commerce.PreparedEvent, error) {
	processor.mu.Lock()
	processor.calls++
	processor.payload = append([]byte(nil), snapshot.Payload...)
	called := processor.called
	processor.mu.Unlock()
	return commerce.PreparedEvent{Apply: func(
		context.Context,
		*gorm.DB,
		time.Time,
	) (commerce.EventOutcome, error) {
		outcome := commerce.EventOutcome{
			Status: model.CommerceProviderEventStatusIgnored,
			Kind:   "scheduler_test_ignored",
		}
		if called != nil {
			// Signal only after the durable transaction commits. Signalling from
			// Prepare would let the test cancel the SQLite context mid-write and
			// would test driver interruption rather than scheduler lifecycle.
			outcome.AfterCommit = func() {
				select {
				case called <- struct{}{}:
				default:
				}
			}
		}
		return outcome, nil
	}}, nil
}

func (processor *reconcileProcessor) callCount() int {
	processor.mu.Lock()
	defer processor.mu.Unlock()
	return processor.calls
}

func TestCommerceProviderEventReconcilerReconcileOnceDelegatesToCommerceService(t *testing.T) {
	db := testutil.NewTestDB(t)
	processor := &reconcileProcessor{}
	reconciler, err := NewCommerceProviderEventReconciler(db, processor)
	if err != nil {
		t.Fatalf("construct reconciler: %v", err)
	}
	eventID := ingestReconcilerTestEvent(t, db, "evt_reconcile_once")

	report, err := reconciler.ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("reconcile once: %v", err)
	}
	if report.Scanned != 1 || report.Ignored != 1 || report.Processed != 0 ||
		report.Retried != 0 || report.ManualReviews != 0 || report.Busy != 0 || len(report.Failures) != 0 {
		t.Fatalf("reconcile report = %+v", report)
	}
	if got := processor.callCount(); got != 1 {
		t.Fatalf("processor calls = %d, want 1", got)
	}
	var stored model.CommerceProviderEvent
	if err := db.First(&stored, eventID).Error; err != nil {
		t.Fatalf("read reconciled event: %v", err)
	}
	if stored.Status != model.CommerceProviderEventStatusIgnored || stored.OutcomeKind != "scheduler_test_ignored" {
		t.Fatalf("reconciled event = %+v", stored)
	}
}

func TestCommerceProviderEventReconcilerLoopStartStopAndRestartAreIdempotent(t *testing.T) {
	db := testutil.NewTestDB(t)
	processor := &reconcileProcessor{called: make(chan struct{}, 4)}
	reconciler, err := newCommerceProviderEventReconciler(db, processor, 5*time.Millisecond, 8)
	if err != nil {
		t.Fatalf("construct reconciler: %v", err)
	}

	reconciler.Start()
	reconciler.lifecycleMu.Lock()
	firstDone := reconciler.done
	reconciler.lifecycleMu.Unlock()
	reconciler.Start()
	reconciler.Start()
	reconciler.lifecycleMu.Lock()
	if reconciler.done != firstDone {
		t.Fatal("duplicate Start replaced the active loop")
	}
	reconciler.lifecycleMu.Unlock()

	ingestReconcilerTestEvent(t, db, "evt_reconcile_loop")
	waitForReconcileCall(t, processor.called)
	stopWithin(t, reconciler.Stop, time.Second)
	stopWithin(t, reconciler.Stop, time.Second)
	stopWithin(t, reconciler.Stop, time.Second)

	// Restarting after a complete stop creates one fresh loop and remains
	// safe; this also guards against reusing a closed done channel.
	ingestReconcilerTestEvent(t, db, "evt_reconcile_restart")
	reconciler.Start()
	waitForReconcileCall(t, processor.called)
	stopWithin(t, reconciler.Stop, time.Second)
	if got := processor.callCount(); got != 2 {
		t.Fatalf("processor calls = %d, want 2", got)
	}
}

func TestCommerceProviderEventReconcilerStopWithoutStartIsNoop(t *testing.T) {
	db := testutil.NewTestDB(t)
	reconciler, err := NewCommerceProviderEventReconciler(db, &reconcileProcessor{})
	if err != nil {
		t.Fatalf("construct reconciler: %v", err)
	}
	stopWithin(t, reconciler.Stop, time.Second)
}

func TestCommerceProviderEventReconcilerConcurrentLifecycleIsRaceSafe(t *testing.T) {
	db := testutil.NewTestDB(t)
	reconciler, err := newCommerceProviderEventReconciler(db, &reconcileProcessor{}, time.Hour, 1)
	if err != nil {
		t.Fatalf("construct reconciler: %v", err)
	}

	var starts sync.WaitGroup
	for index := 0; index < 16; index++ {
		starts.Add(1)
		go func() {
			defer starts.Done()
			reconciler.Start()
		}()
	}
	starts.Wait()

	var stops sync.WaitGroup
	for index := 0; index < 16; index++ {
		stops.Add(1)
		go func() {
			defer stops.Done()
			reconciler.Stop()
		}()
	}
	allStopped := make(chan struct{})
	go func() {
		stops.Wait()
		close(allStopped)
	}()
	select {
	case <-allStopped:
	case <-time.After(time.Second):
		t.Fatal("concurrent Stop calls deadlocked")
	}

	reconciler.lifecycleMu.Lock()
	running := reconciler.running
	reconciler.lifecycleMu.Unlock()
	if running {
		t.Fatal("reconciler remained running after concurrent Stop calls")
	}
}

func TestCommerceProviderEventReconcilerDoesNotLogProcessorPayloadErrors(t *testing.T) {
	db := testutil.NewTestDB(t)
	const privatePayload = "customer-email@example.test card-token-secret"
	processor := reconcileProcessorFunc(func(
		context.Context,
		commerce.ProviderEventSnapshot,
	) (commerce.PreparedEvent, error) {
		return commerce.PreparedEvent{}, errors.New(privatePayload)
	})
	reconciler, err := NewCommerceProviderEventReconciler(db, processor)
	if err != nil {
		t.Fatalf("construct reconciler: %v", err)
	}
	ingestReconcilerTestEvent(t, db, "evt_reconcile_private_error")

	var logs bytes.Buffer
	previousOutput := globals.Logger.Out
	globals.Logger.SetOutput(&logs)
	t.Cleanup(func() { globals.Logger.SetOutput(previousOutput) })

	reconciler.reconcileGuarded(context.Background())
	if strings.Contains(logs.String(), privatePayload) {
		t.Fatalf("reconciler log leaked processor payload: %q", logs.String())
	}
	if !strings.Contains(logs.String(), "retried=1") {
		t.Fatalf("reconciler log omitted safe failure heartbeat: %q", logs.String())
	}
}

func TestCommerceProviderEventReconcilerRejectsInvalidDependencies(t *testing.T) {
	db := testutil.NewTestDB(t)
	if _, err := NewCommerceProviderEventReconciler(nil, &reconcileProcessor{}); err == nil {
		t.Fatal("nil database was accepted")
	}
	if _, err := NewCommerceProviderEventReconciler(db, nil); err == nil {
		t.Fatal("nil processor was accepted")
	}
	if _, err := newCommerceProviderEventReconciler(db, &reconcileProcessor{}, 0, 1); err == nil {
		t.Fatal("zero interval was accepted")
	}
	if _, err := newCommerceProviderEventReconciler(db, &reconcileProcessor{}, time.Second, commerce.MaxProviderEventBatch+1); err == nil {
		t.Fatal("oversized batch was accepted")
	}
}

type reconcileProcessorFunc func(
	context.Context,
	commerce.ProviderEventSnapshot,
) (commerce.PreparedEvent, error)

func (processor reconcileProcessorFunc) Prepare(
	ctx context.Context,
	snapshot commerce.ProviderEventSnapshot,
) (commerce.PreparedEvent, error) {
	return processor(ctx, snapshot)
}

func ingestReconcilerTestEvent(t *testing.T, db *gorm.DB, eventID string) uint {
	t.Helper()
	service, err := commerce.NewProviderEventService(db, commerce.ProviderEventServiceOptions{
		WorkerID: "commerce-reconciler-test-ingest",
	})
	if err != nil {
		t.Fatalf("construct ingest service: %v", err)
	}
	createdAt := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	result, err := service.Ingest(context.Background(), commerce.ProviderEventInput{
		Provider:              "test-provider",
		ProviderAccountID:     "acct_scheduler_test",
		ProviderAPIVersion:    "2026-08-07",
		EventID:               eventID,
		EventType:             "scheduler.test",
		ObjectID:              "object_" + eventID,
		ProviderCreatedAt:     &createdAt,
		VerificationKeyDigest: commerce.VerificationKeyDigest("scheduler-test-key"),
		Payload:               []byte(`{"private_customer_value":"must-never-be-logged"}`),
	})
	if err != nil {
		t.Fatalf("ingest %s: %v", eventID, err)
	}
	return result.Event.Id
}

func waitForReconcileCall(t *testing.T, called <-chan struct{}) {
	t.Helper()
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("reconciler loop did not process an event")
	}
}
