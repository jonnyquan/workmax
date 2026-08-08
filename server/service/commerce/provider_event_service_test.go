package commerce

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"server/model"
	"server/utils/testutil"

	"gorm.io/gorm"
)

type providerEventProcessorFunc func(context.Context, ProviderEventSnapshot) (PreparedEvent, error)

func (fn providerEventProcessorFunc) Prepare(
	ctx context.Context,
	snapshot ProviderEventSnapshot,
) (PreparedEvent, error) {
	return fn(ctx, snapshot)
}

func TestProviderEventServiceIngestIsIdempotentAndRejectsConflicts(t *testing.T) {
	db := newProviderEventServiceTestDB(t)
	service := newProviderEventServiceForTest(t, db)
	ctx := context.Background()

	input := providerEventInputForTest("evt_ingest_idempotent")
	wantPayload := append([]byte(nil), input.Payload...)
	first, err := service.Ingest(ctx, input)
	if err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	if first.Replay {
		t.Fatal("first ingest unexpectedly reported a replay")
	}
	if first.Event.Id == 0 || first.Event.Status != model.CommerceProviderEventStatusReceived {
		t.Fatalf("first event = %+v", first.Event)
	}

	// The caller owns its byte slice. Mutating it after Ingest must not mutate
	// the durable raw payload used by retries and reconciliation.
	input.Payload[0] = '['
	var stored model.CommerceProviderEvent
	if err := db.First(&stored, first.Event.Id).Error; err != nil {
		t.Fatalf("read stored event: %v", err)
	}
	if !bytes.Equal(stored.PayloadJSON, wantPayload) {
		t.Fatalf("stored payload = %q, want %q", stored.PayloadJSON, wantPayload)
	}
	if stored.PayloadDigest != sha256Hex(wantPayload) {
		t.Fatalf("payload digest = %q, want %q", stored.PayloadDigest, sha256Hex(wantPayload))
	}

	replay, err := service.Ingest(ctx, providerEventInputForTest("evt_ingest_idempotent"))
	if err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	if !replay.Replay || replay.Event.Id != first.Event.Id {
		t.Fatalf("replay = %+v, first row id = %d", replay, first.Event.Id)
	}
	assertProviderEventCount(t, db, 1)

	conflicts := []struct {
		name   string
		mutate func(*ProviderEventInput)
	}{
		{
			name: "provider API version",
			mutate: func(candidate *ProviderEventInput) {
				candidate.ProviderAPIVersion = "2025-06-30.basil"
			},
		},
		{
			name: "event type",
			mutate: func(candidate *ProviderEventInput) {
				candidate.EventType = "invoice.payment_succeeded"
			},
		},
		{
			name: "object id",
			mutate: func(candidate *ProviderEventInput) {
				candidate.ObjectID = "cs_conflicting_object"
			},
		},
		{
			name: "payload",
			mutate: func(candidate *ProviderEventInput) {
				candidate.Payload = []byte(`{"id":"evt_ingest_idempotent","object":"event","type":"tampered"}`)
			},
		},
		{
			name: "provider creation time",
			mutate: func(candidate *ProviderEventInput) {
				createdAt := candidate.ProviderCreatedAt.Add(time.Second)
				candidate.ProviderCreatedAt = &createdAt
			},
		},
	}
	for _, testCase := range conflicts {
		t.Run(testCase.name, func(t *testing.T) {
			candidate := providerEventInputForTest("evt_ingest_idempotent")
			testCase.mutate(&candidate)
			if _, err := service.Ingest(ctx, candidate); !errors.Is(err, ErrProviderEventConflict) {
				t.Fatalf("Ingest() error = %v, want %v", err, ErrProviderEventConflict)
			}
			assertProviderEventCount(t, db, 1)
		})
	}

	// Provider account and livemode are part of the durable event namespace.
	// They may legitimately reuse an upstream event id without colliding.
	otherAccount := providerEventInputForTest("evt_ingest_idempotent")
	otherAccount.ProviderAccountID = "acct_connected"
	if result, err := service.Ingest(ctx, otherAccount); err != nil || result.Replay {
		t.Fatalf("other account ingest = %+v, %v", result, err)
	}
	otherMode := providerEventInputForTest("evt_ingest_idempotent")
	otherMode.LiveMode = true
	otherMode.Payload = []byte(`{"id":"evt_ingest_idempotent","livemode":true,"object":"event","type":"checkout.session.completed"}`)
	if result, err := service.Ingest(ctx, otherMode); err != nil || result.Replay {
		t.Fatalf("other livemode ingest = %+v, %v", result, err)
	}
	assertProviderEventCount(t, db, 3)
}

func TestProviderEventServiceCommitsBusinessMutationOutboxAndTerminalStateAtomically(t *testing.T) {
	db := newProviderEventServiceTestDB(t)
	service := newProviderEventServiceForTest(t, db)
	ctx := context.Background()

	ingested, err := service.Ingest(ctx, providerEventInputForTest("evt_atomic_success"))
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	draft := OutboxDraft{
		Topic:     "commerce.order.paid",
		DedupeKey: SHA256Key("commerce.order.paid", "evt_atomic_success"),
		Payload:   json.RawMessage(`{"orderId":42,"source":"stripe"}`),
	}
	afterCommitCalls := 0
	prepareCalls := 0
	processor := providerEventProcessorFunc(func(_ context.Context, snapshot ProviderEventSnapshot) (PreparedEvent, error) {
		prepareCalls++
		if snapshot.ID != ingested.Event.Id || snapshot.EventID != "evt_atomic_success" {
			t.Fatalf("processor snapshot = %+v", snapshot)
		}
		if snapshot.AttemptCount != 1 || snapshot.ProcessingVersion != 1 {
			t.Fatalf("claim counters = attempt %d, version %d", snapshot.AttemptCount, snapshot.ProcessingVersion)
		}
		return PreparedEvent{Apply: func(_ context.Context, tx *gorm.DB, _ time.Time) (EventOutcome, error) {
			if err := insertCommerceTestEffect(tx, snapshot.ID, "order-paid"); err != nil {
				return EventOutcome{}, err
			}
			return EventOutcome{
				Status: model.CommerceProviderEventStatusProcessed,
				Kind:   "order_paid",
				Outbox: []OutboxDraft{draft},
				AfterCommit: func() {
					afterCommitCalls++
				},
			}, nil
		}}, nil
	})

	result, err := service.ProcessEvent(ctx, ingested.Event.Id, processor)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if result.EventID != "evt_atomic_success" || result.Status != model.CommerceProviderEventStatusProcessed ||
		result.OutcomeKind != "order_paid" || result.AttemptCount != 1 || result.Replay {
		t.Fatalf("process result = %+v", result)
	}
	if prepareCalls != 1 || afterCommitCalls != 1 {
		t.Fatalf("prepare calls = %d, after-commit calls = %d", prepareCalls, afterCommitCalls)
	}

	assertCommerceTestEffectCount(t, db, ingested.Event.Id, 1)
	var event model.CommerceProviderEvent
	if err := db.First(&event, ingested.Event.Id).Error; err != nil {
		t.Fatalf("read terminal event: %v", err)
	}
	if event.Status != model.CommerceProviderEventStatusProcessed || event.ProcessedAt == nil ||
		event.OutcomeKind != "order_paid" || len(event.ResultDigest) != 64 ||
		event.LeaseOwnerID != "" || event.LeaseExpiresAt != nil || event.LastErrorCode != "" {
		t.Fatalf("terminal event = %+v", event)
	}

	var outbox model.CommerceOutbox
	if err := db.Where("provider_event_id = ?", event.Id).First(&outbox).Error; err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	if outbox.Ordinal != 0 || outbox.Topic != draft.Topic || outbox.DedupeKey != draft.DedupeKey ||
		outbox.PayloadDigest != sha256Hex(draft.Payload) || !bytes.Equal(outbox.PayloadJSON, draft.Payload) ||
		outbox.Status != model.CommerceOutboxStatusPending || outbox.AvailableAt.IsZero() {
		t.Fatalf("outbox = %+v", outbox)
	}

	unexpectedPrepareCalls := 0
	replay, err := service.ProcessEvent(ctx, ingested.Event.Id, providerEventProcessorFunc(
		func(context.Context, ProviderEventSnapshot) (PreparedEvent, error) {
			unexpectedPrepareCalls++
			return PreparedEvent{}, errors.New("terminal replay must not invoke processor")
		},
	))
	if err != nil {
		t.Fatalf("terminal replay: %v", err)
	}
	if !replay.Replay || replay.Status != model.CommerceProviderEventStatusProcessed {
		t.Fatalf("terminal replay result = %+v", replay)
	}
	if unexpectedPrepareCalls != 0 || afterCommitCalls != 1 {
		t.Fatalf("replay prepare calls = %d, after-commit calls = %d", unexpectedPrepareCalls, afterCommitCalls)
	}
	assertCommerceTestEffectCount(t, db, ingested.Event.Id, 1)
	assertCommerceOutboxCount(t, db, ingested.Event.Id, 1)
}

func TestProviderEventServiceRollsBackBusinessMutationWhenOutboxFailsAndRetries(t *testing.T) {
	db := newProviderEventServiceTestDB(t)
	service := newProviderEventServiceForTest(t, db)
	ctx := context.Background()

	ingested, err := service.Ingest(ctx, providerEventInputForTest("evt_outbox_retry"))
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if err := db.Exec(`CREATE TRIGGER test_fail_commerce_outbox_insert
		BEFORE INSERT ON w_commerce_outbox
		BEGIN
			SELECT RAISE(ABORT, 'forced commerce outbox failure');
		END`).Error; err != nil {
		t.Fatalf("install failing outbox trigger: %v", err)
	}

	draft := OutboxDraft{
		Topic:     "commerce.order.paid",
		DedupeKey: SHA256Key("commerce.order.paid", "evt_outbox_retry"),
		Payload:   json.RawMessage(`{"orderId":84}`),
	}
	prepareCalls := 0
	afterCommitCalls := 0
	processor := providerEventProcessorFunc(func(_ context.Context, snapshot ProviderEventSnapshot) (PreparedEvent, error) {
		prepareCalls++
		return PreparedEvent{Apply: func(_ context.Context, tx *gorm.DB, _ time.Time) (EventOutcome, error) {
			if err := insertCommerceTestEffect(tx, snapshot.ID, "order-paid"); err != nil {
				return EventOutcome{}, err
			}
			return EventOutcome{
				Status: model.CommerceProviderEventStatusProcessed,
				Kind:   "order_paid",
				Outbox: []OutboxDraft{draft},
				AfterCommit: func() {
					afterCommitCalls++
				},
			}, nil
		}}, nil
	})

	result, err := service.ProcessEvent(ctx, ingested.Event.Id, processor)
	if !errors.Is(err, ErrProviderEventRetryScheduled) {
		t.Fatalf("first process error = %v, want %v", err, ErrProviderEventRetryScheduled)
	}
	if result.Status != model.CommerceProviderEventStatusRetryWait || result.AttemptCount != 1 {
		t.Fatalf("first process result = %+v", result)
	}
	if afterCommitCalls != 0 {
		t.Fatalf("after-commit calls after rollback = %d, want 0", afterCommitCalls)
	}
	assertCommerceTestEffectCount(t, db, ingested.Event.Id, 0)
	assertCommerceOutboxCount(t, db, ingested.Event.Id, 0)

	var failed model.CommerceProviderEvent
	if err := db.First(&failed, ingested.Event.Id).Error; err != nil {
		t.Fatalf("read retry state: %v", err)
	}
	if failed.Status != model.CommerceProviderEventStatusRetryWait || failed.NextAttemptAt == nil ||
		failed.LastErrorCode != "processing_error" || failed.LeaseOwnerID != "" || failed.LeaseExpiresAt != nil {
		t.Fatalf("retry state = %+v", failed)
	}

	prepareCallsBeforeNotReady := prepareCalls
	if _, err := service.ProcessEvent(ctx, ingested.Event.Id, processor); !errors.Is(err, ErrProviderEventNotReady) {
		t.Fatalf("early retry error = %v, want %v", err, ErrProviderEventNotReady)
	}
	if prepareCalls != prepareCallsBeforeNotReady {
		t.Fatalf("not-ready retry invoked processor: calls %d -> %d", prepareCallsBeforeNotReady, prepareCalls)
	}

	if err := db.Exec("DROP TRIGGER test_fail_commerce_outbox_insert").Error; err != nil {
		t.Fatalf("drop failing outbox trigger: %v", err)
	}
	makeProviderEventRetryDue(t, db, ingested.Event.Id)
	result, err = service.ProcessEvent(ctx, ingested.Event.Id, processor)
	if err != nil {
		t.Fatalf("due retry: %v", err)
	}
	if result.Status != model.CommerceProviderEventStatusProcessed || result.AttemptCount != 2 {
		t.Fatalf("retry result = %+v", result)
	}
	if prepareCalls != 2 || afterCommitCalls != 1 {
		t.Fatalf("prepare calls = %d, after-commit calls = %d", prepareCalls, afterCommitCalls)
	}
	assertCommerceTestEffectCount(t, db, ingested.Event.Id, 1)
	assertCommerceOutboxCount(t, db, ingested.Event.Id, 1)
}

func TestProviderEventServiceReconcileProcessesDueWorkAndClassifiesOutcomes(t *testing.T) {
	db := newProviderEventServiceTestDB(t)
	service := newProviderEventServiceForTest(t, db)
	ctx := context.Background()

	events := make(map[string]model.CommerceProviderEvent)
	for _, eventID := range []string{
		"evt_reconcile_processed",
		"evt_reconcile_ignored",
		"evt_reconcile_rescheduled",
		"evt_reconcile_manual",
		"evt_reconcile_due_retry",
		"evt_reconcile_expired_lease",
		"evt_reconcile_future_retry",
	} {
		ingested, err := service.Ingest(ctx, providerEventInputForTest(eventID))
		if err != nil {
			t.Fatalf("ingest %s: %v", eventID, err)
		}
		events[eventID] = ingested.Event
	}

	clock := time.Now().UTC()
	historicalCreatedAt := clock.Add(-3 * time.Hour)
	historicalUpdatedAt := clock.Add(-2 * time.Hour)
	past := clock.Add(-time.Hour)
	if err := db.Model(&model.CommerceProviderEvent{}).
		Where("id = ?", events["evt_reconcile_expired_lease"].Id).
		Updates(map[string]any{
			"status":             model.CommerceProviderEventStatusProcessing,
			"attempt_count":      1,
			"processing_version": 1,
			"lease_owner_id":     "abandoned-worker",
			"lease_expires_at":   past,
			"created_at":         historicalCreatedAt,
			"updated_at":         historicalUpdatedAt,
		}).Error; err != nil {
		t.Fatalf("seed expired lease: %v", err)
	}
	if err := db.Model(&model.CommerceProviderEvent{}).
		Where("id = ?", events["evt_reconcile_due_retry"].Id).
		Updates(map[string]any{
			"status":             model.CommerceProviderEventStatusRetryWait,
			"attempt_count":      1,
			"processing_version": 1,
			"next_attempt_at":    past,
			"last_error_code":    "provider_unavailable",
			"created_at":         historicalCreatedAt,
			"updated_at":         historicalUpdatedAt,
		}).Error; err != nil {
		t.Fatalf("seed due retry: %v", err)
	}
	future := time.Now().UTC().Add(time.Hour)
	if err := db.Model(&model.CommerceProviderEvent{}).
		Where("id = ?", events["evt_reconcile_future_retry"].Id).
		Updates(map[string]any{
			"status":             model.CommerceProviderEventStatusRetryWait,
			"attempt_count":      1,
			"processing_version": 1,
			"next_attempt_at":    future,
			"last_error_code":    "provider_unavailable",
			"updated_at":         time.Now().UTC(),
		}).Error; err != nil {
		t.Fatalf("seed future retry: %v", err)
	}

	prepareCalls := make(map[string]int)
	processor := providerEventProcessorFunc(func(_ context.Context, snapshot ProviderEventSnapshot) (PreparedEvent, error) {
		prepareCalls[snapshot.EventID]++
		switch snapshot.EventID {
		case "evt_reconcile_rescheduled":
			return PreparedEvent{}, RetryableError("provider_unavailable", errors.New("temporary provider outage"))
		case "evt_reconcile_manual":
			return PreparedEvent{}, ManualReviewError("invalid_provider_facts", errors.New("facts require review"))
		case "evt_reconcile_ignored":
			return PreparedEvent{Apply: func(context.Context, *gorm.DB, time.Time) (EventOutcome, error) {
				return EventOutcome{Status: model.CommerceProviderEventStatusIgnored, Kind: "unsupported_event"}, nil
			}}, nil
		default:
			return PreparedEvent{Apply: func(context.Context, *gorm.DB, time.Time) (EventOutcome, error) {
				return EventOutcome{Status: model.CommerceProviderEventStatusProcessed, Kind: "reconciled"}, nil
			}}, nil
		}
	})

	report, err := service.ReconcileOnce(ctx, processor, 16)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if report.Scanned != 6 || report.Processed != 3 || report.Ignored != 1 ||
		report.Retried != 1 || report.ManualReviews != 1 || report.Busy != 0 || len(report.Failures) != 0 {
		t.Fatalf("reconcile report = %+v", report)
	}
	if prepareCalls["evt_reconcile_future_retry"] != 0 {
		t.Fatalf("future retry prepare calls = %d, want 0", prepareCalls["evt_reconcile_future_retry"])
	}
	for _, eventID := range []string{
		"evt_reconcile_processed",
		"evt_reconcile_ignored",
		"evt_reconcile_rescheduled",
		"evt_reconcile_manual",
		"evt_reconcile_due_retry",
		"evt_reconcile_expired_lease",
	} {
		if prepareCalls[eventID] != 1 {
			t.Fatalf("%s prepare calls = %d, want 1", eventID, prepareCalls[eventID])
		}
	}

	assertProviderEventState(t, db, events["evt_reconcile_processed"].Id, model.CommerceProviderEventStatusProcessed, 1, 1)
	assertProviderEventState(t, db, events["evt_reconcile_ignored"].Id, model.CommerceProviderEventStatusIgnored, 1, 1)
	assertProviderEventState(t, db, events["evt_reconcile_rescheduled"].Id, model.CommerceProviderEventStatusRetryWait, 1, 1)
	assertProviderEventState(t, db, events["evt_reconcile_manual"].Id, model.CommerceProviderEventStatusManualReview, 1, 1)
	assertProviderEventState(t, db, events["evt_reconcile_due_retry"].Id, model.CommerceProviderEventStatusProcessed, 2, 2)
	assertProviderEventState(t, db, events["evt_reconcile_expired_lease"].Id, model.CommerceProviderEventStatusProcessed, 2, 2)
	assertProviderEventState(t, db, events["evt_reconcile_future_retry"].Id, model.CommerceProviderEventStatusRetryWait, 1, 1)
}

func TestProviderEventServicePrepareDeadlineIsStrictlyInsideLease(t *testing.T) {
	db := newProviderEventServiceTestDB(t)
	const (
		leaseTTL       = 500 * time.Millisecond
		prepareTimeout = 200 * time.Millisecond
		failureTimeout = 100 * time.Millisecond
	)
	service, err := NewProviderEventService(db, ProviderEventServiceOptions{
		WorkerID:              "commerce-deadline-test",
		LeaseTTL:              leaseTTL,
		PrepareTimeout:        prepareTimeout,
		FailurePersistTimeout: failureTimeout,
		MaxAttempts:           3,
		BaseBackoff:           time.Second,
		MaxBackoff:            time.Second,
	})
	if err != nil {
		t.Fatalf("new provider event service: %v", err)
	}
	ingested, err := service.Ingest(context.Background(), providerEventInputForTest("evt_prepare_deadline"))
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}

	startedAt := time.Now()
	var prepareDeadline time.Time
	result, err := service.ProcessEvent(
		context.Background(),
		ingested.Event.Id,
		providerEventProcessorFunc(func(ctx context.Context, _ ProviderEventSnapshot) (PreparedEvent, error) {
			var ok bool
			prepareDeadline, ok = ctx.Deadline()
			if !ok {
				return PreparedEvent{}, errors.New("prepare context has no deadline")
			}
			return PreparedEvent{Apply: func(context.Context, *gorm.DB, time.Time) (EventOutcome, error) {
				return EventOutcome{Status: model.CommerceProviderEventStatusIgnored, Kind: "deadline_checked"}, nil
			}}, nil
		}),
	)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if result.Status != model.CommerceProviderEventStatusIgnored {
		t.Fatalf("result = %+v", result)
	}
	if !prepareDeadline.After(startedAt) || !prepareDeadline.Before(startedAt.Add(leaseTTL)) {
		t.Fatalf("prepare deadline %v is not strictly inside lease ending before %v", prepareDeadline, startedAt.Add(leaseTTL))
	}
	if budget := prepareDeadline.Sub(startedAt); budget > prepareTimeout+25*time.Millisecond {
		t.Fatalf("prepare deadline budget = %s, configured %s", budget, prepareTimeout)
	}
}

func TestProviderEventServicePrepareTimeoutPersistsRetryBeforeLeaseExpires(t *testing.T) {
	db := newProviderEventServiceTestDB(t)
	const (
		leaseTTL       = 400 * time.Millisecond
		prepareTimeout = 30 * time.Millisecond
	)
	service := newProviderEventServiceWithBudgetsForTest(t, db, ProviderEventServiceOptions{
		WorkerID:              "commerce-timeout-test",
		LeaseTTL:              leaseTTL,
		PrepareTimeout:        prepareTimeout,
		FailurePersistTimeout: 100 * time.Millisecond,
	})
	ingested, err := service.Ingest(context.Background(), providerEventInputForTest("evt_prepare_timeout"))
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}

	startedAt := time.Now()
	result, err := service.ProcessEvent(
		context.Background(),
		ingested.Event.Id,
		providerEventProcessorFunc(func(ctx context.Context, _ ProviderEventSnapshot) (PreparedEvent, error) {
			<-ctx.Done()
			return PreparedEvent{}, ctx.Err()
		}),
	)
	if !errors.Is(err, ErrProviderEventRetryScheduled) {
		t.Fatalf("process error = %v, want %v", err, ErrProviderEventRetryScheduled)
	}
	if result.Status != model.CommerceProviderEventStatusRetryWait || result.AttemptCount != 1 {
		t.Fatalf("result = %+v", result)
	}
	if elapsed := time.Since(startedAt); elapsed >= leaseTTL {
		t.Fatalf("timeout transition took %s, lease is %s", elapsed, leaseTTL)
	}
	assertProviderEventRetryState(t, db, ingested.Event.Id, 1, "processing_error")
}

func TestProviderEventServiceApplyTimeoutRollsBackAndPersistsRetryBeforeLeaseExpires(t *testing.T) {
	db := newProviderEventServiceTestDB(t)
	const (
		leaseTTL        = 400 * time.Millisecond
		completeTimeout = 35 * time.Millisecond
	)
	service := newProviderEventServiceWithBudgetsForTest(t, db, ProviderEventServiceOptions{
		WorkerID:              "commerce-apply-timeout-test",
		LeaseTTL:              leaseTTL,
		PrepareTimeout:        50 * time.Millisecond,
		CompleteTimeout:       completeTimeout,
		FailurePersistTimeout: 100 * time.Millisecond,
	})
	ingested, err := service.Ingest(context.Background(), providerEventInputForTest("evt_apply_timeout"))
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}

	startedAt := time.Now()
	result, err := service.ProcessEvent(
		context.Background(),
		ingested.Event.Id,
		providerEventProcessorFunc(func(context.Context, ProviderEventSnapshot) (PreparedEvent, error) {
			return PreparedEvent{Apply: func(ctx context.Context, tx *gorm.DB, _ time.Time) (EventOutcome, error) {
				if err := insertCommerceTestEffect(tx, ingested.Event.Id, "must-roll-back"); err != nil {
					return EventOutcome{}, err
				}
				<-ctx.Done()
				return EventOutcome{}, ctx.Err()
			}}, nil
		}),
	)
	if !errors.Is(err, ErrProviderEventRetryScheduled) {
		t.Fatalf("process error = %v, want %v", err, ErrProviderEventRetryScheduled)
	}
	if result.Status != model.CommerceProviderEventStatusRetryWait || result.AttemptCount != 1 {
		t.Fatalf("result = %+v", result)
	}
	if elapsed := time.Since(startedAt); elapsed >= leaseTTL {
		t.Fatalf("apply timeout transition took %s, lease is %s", elapsed, leaseTTL)
	}
	assertCommerceTestEffectCount(t, db, ingested.Event.Id, 0)
	assertProviderEventRetryState(t, db, ingested.Event.Id, 1, "processing_error")
}

func TestProviderEventServiceRejectsBudgetsThatReachLeaseBoundary(t *testing.T) {
	db := newProviderEventServiceTestDB(t)
	for _, testCase := range []struct {
		name    string
		options ProviderEventServiceOptions
	}{
		{
			name: "prepare reaches lease",
			options: ProviderEventServiceOptions{
				LeaseTTL: 100 * time.Millisecond, PrepareTimeout: 100 * time.Millisecond,
				CompleteTimeout: time.Millisecond, FailurePersistTimeout: time.Millisecond,
			},
		},
		{
			name: "completion reaches remaining lease",
			options: ProviderEventServiceOptions{
				LeaseTTL: 100 * time.Millisecond, PrepareTimeout: 40 * time.Millisecond,
				CompleteTimeout: 60 * time.Millisecond, FailurePersistTimeout: time.Millisecond,
			},
		},
		{
			name: "failure persistence leaves less than safety reserve",
			options: ProviderEventServiceOptions{
				LeaseTTL: 100 * time.Millisecond, PrepareTimeout: 40 * time.Millisecond,
				CompleteTimeout: 30 * time.Millisecond, FailurePersistTimeout: 26 * time.Millisecond,
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			testCase.options.WorkerID = "invalid-budget-test"
			testCase.options.MaxAttempts = 3
			testCase.options.BaseBackoff = time.Second
			testCase.options.MaxBackoff = time.Second
			if _, err := NewProviderEventService(db, testCase.options); err == nil {
				t.Fatal("lease-boundary budget was accepted")
			}
		})
	}
	defaultReserve := DefaultProviderEventLeaseTTL - DefaultProviderEventPrepareTimeout -
		DefaultProviderEventCompleteTimeout - DefaultProviderEventFailurePersistTimeout
	if want := providerEventLeaseSafetyReserve(DefaultProviderEventLeaseTTL); defaultReserve < want {
		t.Fatalf("default lease reserve = %s, want at least %s", defaultReserve, want)
	}
}

func TestProviderEventServiceCanceledParentPersistsRetryWithDetachedContext(t *testing.T) {
	db := newProviderEventServiceTestDB(t)
	service := newProviderEventServiceWithBudgetsForTest(t, db, ProviderEventServiceOptions{
		WorkerID:              "commerce-cancel-test",
		LeaseTTL:              500 * time.Millisecond,
		PrepareTimeout:        250 * time.Millisecond,
		FailurePersistTimeout: 100 * time.Millisecond,
	})
	ingested, err := service.Ingest(context.Background(), providerEventInputForTest("evt_prepare_parent_cancel"))
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}

	parent, cancelParent := context.WithCancel(context.Background())
	result, err := service.ProcessEvent(
		parent,
		ingested.Event.Id,
		providerEventProcessorFunc(func(ctx context.Context, _ ProviderEventSnapshot) (PreparedEvent, error) {
			cancelParent()
			<-ctx.Done()
			return PreparedEvent{}, ctx.Err()
		}),
	)
	if !errors.Is(err, ErrProviderEventRetryScheduled) {
		t.Fatalf("process error = %v, want %v", err, ErrProviderEventRetryScheduled)
	}
	if !errors.Is(parent.Err(), context.Canceled) {
		t.Fatalf("parent error = %v, want context canceled", parent.Err())
	}
	if result.Status != model.CommerceProviderEventStatusRetryWait || result.AttemptCount != 1 {
		t.Fatalf("result = %+v", result)
	}
	assertProviderEventRetryState(t, db, ingested.Event.Id, 1, "processing_error")
}

func TestProviderEventServiceExpiredClaimAtAttemptBudgetMovesDirectlyToManualReview(t *testing.T) {
	for _, maxAttempts := range []int{3, MaxProviderEventAttempts} {
		t.Run(fmt.Sprintf("max_%d", maxAttempts), func(t *testing.T) {
			db := newProviderEventServiceTestDB(t)
			service := newProviderEventServiceWithBudgetsForTest(t, db, ProviderEventServiceOptions{
				WorkerID:              "commerce-exhausted-test",
				LeaseTTL:              time.Second,
				PrepareTimeout:        500 * time.Millisecond,
				FailurePersistTimeout: 100 * time.Millisecond,
				MaxAttempts:           maxAttempts,
			})
			ingested, err := service.Ingest(
				context.Background(),
				providerEventInputForTest(fmt.Sprintf("evt_expired_budget_%d", maxAttempts)),
			)
			if err != nil {
				t.Fatalf("ingest: %v", err)
			}
			seedExpiredProviderEventClaim(t, db, ingested.Event.Id, maxAttempts)

			prepareCalls := 0
			result, err := service.ProcessEvent(
				context.Background(),
				ingested.Event.Id,
				providerEventProcessorFunc(func(context.Context, ProviderEventSnapshot) (PreparedEvent, error) {
					prepareCalls++
					return PreparedEvent{}, errors.New("attempt budget must stop prepare")
				}),
			)
			if !errors.Is(err, ErrProviderEventManualReview) {
				t.Fatalf("process error = %v, want %v", err, ErrProviderEventManualReview)
			}
			if prepareCalls != 0 {
				t.Fatalf("prepare calls = %d, want 0", prepareCalls)
			}
			if result.Status != model.CommerceProviderEventStatusManualReview || result.AttemptCount != maxAttempts {
				t.Fatalf("result = %+v", result)
			}

			var stored model.CommerceProviderEvent
			if err := db.First(&stored, ingested.Event.Id).Error; err != nil {
				t.Fatalf("read manual-review event: %v", err)
			}
			if stored.Status != model.CommerceProviderEventStatusManualReview ||
				stored.AttemptCount != maxAttempts || stored.ProcessingVersion != int64(maxAttempts) ||
				stored.LeaseOwnerID != "" || stored.LeaseExpiresAt != nil || stored.NextAttemptAt != nil ||
				stored.LastErrorCode != providerEventAttemptBudgetExhaustedErrorCode {
				t.Fatalf("manual-review event = %+v", stored)
			}
		})
	}
}

func TestProviderEventServiceProviderCreatedAtRequiresCanonicalMicroseconds(t *testing.T) {
	db := newProviderEventServiceTestDB(t)
	service := newProviderEventServiceForTest(t, db)
	ctx := context.Background()

	for _, testCase := range []struct {
		name      string
		createdAt time.Time
	}{
		{
			name:      "sub-microsecond precision",
			createdAt: time.Date(2026, time.August, 7, 8, 30, 0, 123456789, time.UTC),
		},
		{
			name:      "non-UTC offset",
			createdAt: time.Date(2026, time.August, 7, 16, 30, 0, 123456000, time.FixedZone("UTC+8", 8*60*60)),
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			input := providerEventInputForTest("evt_noncanonical_" + strings.ReplaceAll(testCase.name, " ", "_"))
			input.ProviderCreatedAt = &testCase.createdAt
			if _, err := service.Ingest(ctx, input); err == nil {
				t.Fatal("non-canonical provider time was accepted")
			}
		})
	}
	assertProviderEventCount(t, db, 0)

	canonical := time.Date(2026, time.August, 7, 8, 30, 0, 123456000, time.UTC)
	input := providerEventInputForTest("evt_canonical_microseconds")
	input.ProviderCreatedAt = &canonical
	first, err := service.Ingest(ctx, input)
	if err != nil {
		t.Fatalf("canonical ingest: %v", err)
	}
	replay, err := service.Ingest(ctx, input)
	if err != nil {
		t.Fatalf("canonical replay: %v", err)
	}
	if !replay.Replay || replay.Event.Id != first.Event.Id ||
		replay.Event.ProviderCreatedAt == nil || !replay.Event.ProviderCreatedAt.Equal(canonical) {
		t.Fatalf("canonical replay = %+v", replay)
	}
	assertProviderEventCount(t, db, 1)

	// MySQL parseTime with loc=Local scans DATETIME(6) in the configured local
	// Location. The instant and microseconds remain authoritative even though
	// the returned time no longer carries UTC as its Location.
	localStored := replay.Event
	localInstant := canonical.In(time.FixedZone("Asia/Shanghai-test", 8*60*60))
	localStored.ProviderCreatedAt = &localInstant
	if err := validateStoredEvent(localStored); err != nil {
		t.Fatalf("local-zone stored instant was rejected: %v", err)
	}
	nonCanonicalStored := localInstant.Add(time.Nanosecond)
	localStored.ProviderCreatedAt = &nonCanonicalStored
	if err := validateStoredEvent(localStored); !errors.Is(err, ErrCommerceStoreIntegrity) {
		t.Fatalf("sub-microsecond stored instant error = %v, want integrity error", err)
	}
}

func newProviderEventServiceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testutil.NewPersistentTestDB(t)
	if err := db.Exec(`CREATE TABLE test_commerce_effect (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		provider_event_id INTEGER NOT NULL UNIQUE,
		value TEXT NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`).Error; err != nil {
		t.Fatalf("install commerce business-effect test table: %v", err)
	}
	return db
}

func newProviderEventServiceForTest(t *testing.T, db *gorm.DB) *ProviderEventService {
	t.Helper()
	service, err := NewProviderEventService(db, ProviderEventServiceOptions{
		WorkerID:    "commerce-test-worker",
		LeaseTTL:    5 * time.Minute,
		MaxAttempts: 3,
		BaseBackoff: time.Minute,
		MaxBackoff:  time.Minute,
	})
	if err != nil {
		t.Fatalf("new provider event service: %v", err)
	}
	return service
}

func newProviderEventServiceWithBudgetsForTest(
	t *testing.T,
	db *gorm.DB,
	options ProviderEventServiceOptions,
) *ProviderEventService {
	t.Helper()
	if options.MaxAttempts == 0 {
		options.MaxAttempts = 3
	}
	if options.BaseBackoff == 0 {
		options.BaseBackoff = time.Second
	}
	if options.MaxBackoff == 0 {
		options.MaxBackoff = time.Second
	}
	service, err := NewProviderEventService(db, options)
	if err != nil {
		t.Fatalf("new provider event service: %v", err)
	}
	return service
}

func providerEventInputForTest(eventID string) ProviderEventInput {
	createdAt := time.Date(2026, time.August, 7, 8, 30, 0, 0, time.UTC)
	return ProviderEventInput{
		Provider:              "stripe",
		ProviderAccountID:     "acct_platform",
		ProviderAPIVersion:    "2024-06-20",
		EventID:               eventID,
		EventType:             "checkout.session.completed",
		ObjectID:              "cs_" + eventID,
		ProviderCreatedAt:     &createdAt,
		VerificationKeyDigest: VerificationKeyDigest("whsec_test_epoch_1"),
		Payload: []byte(fmt.Sprintf(
			`{"created":1786091400,"id":%q,"livemode":false,"object":"event","type":"checkout.session.completed"}`,
			eventID,
		)),
	}
}

func insertCommerceTestEffect(tx *gorm.DB, eventRowID uint, value string) error {
	return tx.Exec(
		"INSERT INTO test_commerce_effect (provider_event_id, value) VALUES (?, ?)",
		eventRowID, value,
	).Error
}

func makeProviderEventRetryDue(t *testing.T, db *gorm.DB, eventRowID uint) {
	t.Helper()
	clock := time.Now().UTC()
	result := db.Model(&model.CommerceProviderEvent{}).
		Where("id = ? AND status = ?", eventRowID, model.CommerceProviderEventStatusRetryWait).
		Updates(map[string]any{
			"created_at":      clock.Add(-3 * time.Hour),
			"updated_at":      clock.Add(-2 * time.Hour),
			"next_attempt_at": clock.Add(-time.Hour),
		})
	if result.Error != nil {
		t.Fatalf("make retry due: %v", result.Error)
	}
	if result.RowsAffected != 1 {
		t.Fatalf("make retry due affected %d rows, want 1", result.RowsAffected)
	}
}

func seedExpiredProviderEventClaim(t *testing.T, db *gorm.DB, eventRowID uint, attempts int) {
	t.Helper()
	clock := time.Now().UTC()
	result := db.Model(&model.CommerceProviderEvent{}).
		Where("id = ? AND status = ?", eventRowID, model.CommerceProviderEventStatusReceived).
		Updates(map[string]any{
			"status":             model.CommerceProviderEventStatusProcessing,
			"attempt_count":      attempts,
			"processing_version": attempts,
			"lease_owner_id":     "crashed-worker",
			"lease_expires_at":   clock.Add(-time.Hour),
			"created_at":         clock.Add(-3 * time.Hour),
			"updated_at":         clock.Add(-2 * time.Hour),
		})
	if result.Error != nil {
		t.Fatalf("seed expired provider event claim: %v", result.Error)
	}
	if result.RowsAffected != 1 {
		t.Fatalf("seed expired provider event claim affected %d rows, want 1", result.RowsAffected)
	}
}

func assertProviderEventRetryState(
	t *testing.T,
	db *gorm.DB,
	eventRowID uint,
	wantAttempts int,
	wantErrorCode string,
) {
	t.Helper()
	var event model.CommerceProviderEvent
	if err := db.First(&event, eventRowID).Error; err != nil {
		t.Fatalf("read retry event: %v", err)
	}
	if event.Status != model.CommerceProviderEventStatusRetryWait || event.AttemptCount != wantAttempts ||
		event.NextAttemptAt == nil || event.LeaseOwnerID != "" || event.LeaseExpiresAt != nil ||
		event.LastErrorCode != wantErrorCode {
		t.Fatalf("retry event = %+v", event)
	}
}

func assertProviderEventCount(t *testing.T, db *gorm.DB, want int64) {
	t.Helper()
	var got int64
	if err := db.Model(&model.CommerceProviderEvent{}).Count(&got).Error; err != nil {
		t.Fatalf("count provider events: %v", err)
	}
	if got != want {
		t.Fatalf("provider event count = %d, want %d", got, want)
	}
}

func assertCommerceTestEffectCount(t *testing.T, db *gorm.DB, eventRowID uint, want int64) {
	t.Helper()
	var got int64
	if err := db.Table("test_commerce_effect").Where("provider_event_id = ?", eventRowID).Count(&got).Error; err != nil {
		t.Fatalf("count commerce business effects: %v", err)
	}
	if got != want {
		t.Fatalf("business-effect count = %d, want %d", got, want)
	}
}

func assertCommerceOutboxCount(t *testing.T, db *gorm.DB, eventRowID uint, want int64) {
	t.Helper()
	var got int64
	if err := db.Model(&model.CommerceOutbox{}).Where("provider_event_id = ?", eventRowID).Count(&got).Error; err != nil {
		t.Fatalf("count commerce outbox rows: %v", err)
	}
	if got != want {
		t.Fatalf("commerce outbox count = %d, want %d", got, want)
	}
}

func assertProviderEventState(
	t *testing.T,
	db *gorm.DB,
	eventRowID uint,
	wantStatus string,
	wantAttempts int,
	wantVersion int64,
) {
	t.Helper()
	var event model.CommerceProviderEvent
	if err := db.First(&event, eventRowID).Error; err != nil {
		t.Fatalf("read provider event %d: %v", eventRowID, err)
	}
	if event.Status != wantStatus || event.AttemptCount != wantAttempts || event.ProcessingVersion != wantVersion {
		t.Fatalf(
			"provider event %d state = status %q, attempts %d, version %d; want %q, %d, %d",
			eventRowID, event.Status, event.AttemptCount, event.ProcessingVersion,
			wantStatus, wantAttempts, wantVersion,
		)
	}
}
