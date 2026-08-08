package agentturn

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
)

type testDeliverer struct {
	mu       sync.Mutex
	calls    []EffectDelivery
	respond  func(delivery EffectDelivery) (DeliveryReport, error)
	blockFor time.Duration
}

func (deliverer *testDeliverer) Deliver(ctx context.Context, delivery EffectDelivery) (DeliveryReport, error) {
	deliverer.mu.Lock()
	deliverer.calls = append(deliverer.calls, delivery)
	deliverer.mu.Unlock()
	if deliverer.blockFor > 0 {
		select {
		case <-ctx.Done():
			return DeliveryReport{}, ctx.Err()
		case <-time.After(deliverer.blockFor):
		}
	}
	if deliverer.respond == nil {
		return DeliveryReport{Outcome: DeliveryOutcomeDelivered}, nil
	}
	return deliverer.respond(delivery)
}

func (deliverer *testDeliverer) seen() []EffectDelivery {
	deliverer.mu.Lock()
	defer deliverer.mu.Unlock()
	return append([]EffectDelivery(nil), deliverer.calls...)
}

// seedEffects commits one Operation carrying the named effects, which is the
// only way rows legitimately enter the outbox.
func seedEffects(t *testing.T, store *SQLStore, clock *sqlExecutionTestClock, turn Turn, suffix string, topics ...string) []EffectOutboxRecord {
	t.Helper()
	claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_effect_"+suffix))
	if err != nil {
		t.Fatal(err)
	}
	drafts := make([]EffectOutboxDraft, 0, len(topics))
	for index, topic := range topics {
		drafts = append(drafts, executionTestEffect(
			fmt.Sprintf("outbox_%s_%d", suffix, index),
			topic,
			fmt.Sprintf("dedupe_%s_%d", suffix, index),
			clock.Get(),
		))
	}
	commit, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence:       claimed.Attempt.Fence(),
		OperationID: "operation_effect_" + suffix,
		Event: &EventDraft{
			Type: "writer.document.delta",
			Data: []byte(`{"seed":true}`),
		},
		Effects: drafts,
	})
	if err != nil {
		t.Fatal(err)
	}
	return commit.Effects
}

func effectRow(t *testing.T, db *gorm.DB, outboxID string) sqlEffectOutboxRow {
	t.Helper()
	var row sqlEffectOutboxRow
	if err := db.Where("outbox_id = ?", outboxID).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

func newTestDispatcher(t *testing.T, store EffectOutboxStore, deliverer Deliverer, owner string) *EffectDispatcher {
	t.Helper()
	dispatcher, err := NewEffectDispatcher(store, deliverer, EffectDispatcherOptions{
		LeaseOwnerID:    owner,
		IdleBackoff:     time.Millisecond,
		DeliveryTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

func TestEffectDispatcherDeliversAndLeavesAnImmutableReceipt(t *testing.T) {
	db, store, clock, turns := newSQLClaimNextFixture(t, "effect_ok")
	seeded := seedEffects(t, store, clock, turns[0], "ok", "writer.document.index", "writer.document.audit")

	deliverer := &testDeliverer{}
	report, err := newTestDispatcher(t, store, deliverer, "dispatcher_ok").DispatchOnce(context.Background())
	if err != nil {
		t.Fatalf("DispatchOnce(): %v", err)
	}
	if report.Claimed != 2 || report.Delivered != 2 || len(report.Failures) != 0 {
		t.Fatalf("report = %+v, want 2 claimed and 2 delivered", report)
	}

	// The dedupe key the provider saw must be the one the Operation committed:
	// a redelivery that minted a fresh key would defeat provider idempotency.
	seen := deliverer.seen()
	if len(seen) != 2 {
		t.Fatalf("deliverer saw %d calls, want 2", len(seen))
	}
	byOutbox := map[string]EffectDelivery{}
	for _, call := range seen {
		byOutbox[call.OutboxID] = call
	}
	for _, record := range seeded {
		call, ok := byOutbox[record.OutboxID]
		if !ok {
			t.Fatalf("effect %q was never delivered", record.OutboxID)
		}
		if call.DedupeKey != record.DedupeKey || call.Topic != record.Topic {
			t.Fatalf("delivery %+v does not match committed record %+v", call, record)
		}
		if call.DeliveryAttempts != 1 || call.Fence.DispatchToken != 1 {
			t.Fatalf("first delivery = %+v, want attempt 1 and dispatch fence 1", call)
		}
		row := effectRow(t, db, record.OutboxID)
		if row.Status != string(EffectStatusDelivered) || row.DeliveredAt == nil ||
			row.LeaseOwnerID != nil || row.LeaseExpiresAt != nil || row.LastErrorCode != nil {
			t.Fatalf("delivered row = %+v", row)
		}
	}

	// A drained outbox reports empty rather than looping on delivered rows.
	if _, err := newTestDispatcher(t, store, deliverer, "dispatcher_ok").DispatchOnce(context.Background()); !errors.Is(err, ErrNoClaimableEffects) {
		t.Fatalf("drained DispatchOnce() error = %v, want ErrNoClaimableEffects", err)
	}
}

func TestEffectDispatcherRetriesWithBackoffThenDeadLetters(t *testing.T) {
	db, store, clock, turns := newSQLClaimNextFixture(t, "effect_retry")
	seeded := seedEffects(t, store, clock, turns[0], "retry", "writer.document.index")
	outboxID := seeded[0].OutboxID

	deliverer := &testDeliverer{respond: func(EffectDelivery) (DeliveryReport, error) {
		return DeliveryReport{Outcome: DeliveryOutcomeRetry, ErrorCode: "provider_503"}, nil
	}}
	dispatcher := newTestDispatcher(t, store, deliverer, "dispatcher_retry")

	for attempt := int64(1); attempt <= DefaultMaxDeliveryAttempts; attempt++ {
		report, err := dispatcher.DispatchOnce(context.Background())
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		row := effectRow(t, db, outboxID)
		if row.DeliveryAttempts != attempt {
			t.Fatalf("attempt %d: delivery_attempts = %d", attempt, row.DeliveryAttempts)
		}
		if attempt < DefaultMaxDeliveryAttempts {
			if report.Retried != 1 || row.Status != string(EffectStatusPending) {
				t.Fatalf("attempt %d: report = %+v, row = %+v, want a scheduled retry", attempt, report, row)
			}
			// Backoff must actually push the row out of reach, or the loop
			// would hot-spin on the same failing effect.
			if !row.AvailableAt.After(clock.Get()) {
				t.Fatalf("attempt %d: available_at %s is not after now %s", attempt, row.AvailableAt, clock.Get())
			}
			if row.LeaseOwnerID != nil || row.LeaseExpiresAt != nil {
				t.Fatalf("attempt %d: retry kept a lease: %+v", attempt, row)
			}
			if _, err := dispatcher.DispatchOnce(context.Background()); !errors.Is(err, ErrNoClaimableEffects) {
				t.Fatalf("attempt %d: backed-off effect was reclaimed early: %v", attempt, err)
			}
			clock.Set(row.AvailableAt)
			continue
		}
		if report.DeadLettered != 1 || row.Status != string(EffectStatusDeadLetter) ||
			row.DeadLetteredAt == nil || row.LastErrorCode == nil || *row.LastErrorCode != "provider_503" {
			t.Fatalf("final attempt: report = %+v, row = %+v, want a dead letter", report, row)
		}
	}
	if _, err := dispatcher.DispatchOnce(context.Background()); !errors.Is(err, ErrNoClaimableEffects) {
		t.Fatalf("dead-lettered effect was reclaimed: %v", err)
	}
}

func TestEffectDispatcherNeverGuessesAnUnknownOutcome(t *testing.T) {
	db, store, clock, turns := newSQLClaimNextFixture(t, "effect_unknown", "effect_unknown_safe")

	// An ambiguous failure on a provider that cannot deduplicate must not be
	// re-sent: that is exactly how a duplicate side effect reaches a user.
	unsafe := seedEffects(t, store, clock, turns[0], "unsafe", "writer.document.index")
	unsafeDispatcher := newTestDispatcher(t, store, &testDeliverer{
		respond: func(EffectDelivery) (DeliveryReport, error) {
			return DeliveryReport{Outcome: DeliveryOutcomeUnknown, ErrorCode: "provider_timeout"}, nil
		},
	}, "dispatcher_unsafe")
	report, err := unsafeDispatcher.DispatchOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.DeadLettered != 1 || report.Retried != 0 {
		t.Fatalf("unknown-on-non-idempotent report = %+v, want a dead letter", report)
	}
	row := effectRow(t, db, unsafe[0].OutboxID)
	if row.Status != string(EffectStatusDeadLetter) || row.DeliveryAttempts != 1 {
		t.Fatalf("unknown-on-non-idempotent row = %+v", row)
	}

	// The same ambiguity is retryable once the Deliverer states the provider
	// deduplicates on the key the row already carries.
	safe := seedEffects(t, store, clock, turns[1], "safe", "writer.document.audit")
	safeDeliverer := &testDeliverer{respond: func(EffectDelivery) (DeliveryReport, error) {
		return DeliveryReport{
			Outcome: DeliveryOutcomeUnknown, ErrorCode: "provider_timeout", IdempotentRetrySafe: true,
		}, nil
	}}
	safeReport, err := newTestDispatcher(t, store, safeDeliverer, "dispatcher_safe").DispatchOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if safeReport.Retried != 1 || safeReport.DeadLettered != 0 {
		t.Fatalf("unknown-on-idempotent report = %+v, want a retry", safeReport)
	}
	safeRow := effectRow(t, db, safe[0].OutboxID)
	if safeRow.Status != string(EffectStatusPending) {
		t.Fatalf("unknown-on-idempotent row = %+v", safeRow)
	}

	// A Deliverer that errors outright is also unknown, not retryable.
	erroring := newTestDispatcher(t, store, &testDeliverer{
		respond: func(EffectDelivery) (DeliveryReport, error) {
			return DeliveryReport{}, errors.New("connection reset")
		},
	}, "dispatcher_erroring")
	clock.Set(safeRow.AvailableAt)
	errReport, err := erroring.DispatchOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if errReport.DeadLettered != 1 {
		t.Fatalf("deliverer-error report = %+v, want a dead letter", errReport)
	}
	if got := effectRow(t, db, safe[0].OutboxID); got.LastErrorCode == nil || *got.LastErrorCode != "deliverer_error" {
		t.Fatalf("deliverer-error row = %+v", got)
	}
}

func TestEffectDispatchLeaseIsFencedAgainstASupersededDispatcher(t *testing.T) {
	db, store, clock, turns := newSQLClaimNextFixture(t, "effect_fence")
	seeded := seedEffects(t, store, clock, turns[0], "fence", "writer.document.index")
	outboxID := seeded[0].OutboxID

	first, err := store.ClaimEffects(context.Background(), ClaimEffectsCommand{LeaseOwnerID: "dispatcher_first"})
	if err != nil {
		t.Fatal(err)
	}
	// The first dispatcher stalls past its lease and a replacement takes over.
	clock.Set(first[0].LeaseExpiresAt)
	second, err := store.ClaimEffects(context.Background(), ClaimEffectsCommand{LeaseOwnerID: "dispatcher_second"})
	if err != nil {
		t.Fatal(err)
	}
	if second[0].Fence.DispatchToken != first[0].Fence.DispatchToken+1 || second[0].DeliveryAttempts != 2 {
		t.Fatalf("reclaimed delivery = %+v, want the next dispatch fence", second[0])
	}

	if _, err := store.CompleteEffect(context.Background(), CompleteEffectCommand{
		Fence:  first[0].Fence,
		Report: DeliveryReport{Outcome: DeliveryOutcomeDelivered},
	}); !errors.Is(err, ErrEffectFenced) {
		t.Fatalf("superseded CompleteEffect() error = %v, want ErrEffectFenced", err)
	}
	if got := effectRow(t, db, outboxID); got.Status != string(EffectStatusDelivering) {
		t.Fatalf("superseded dispatcher disturbed the live lease: %+v", got)
	}

	// A different owner holding the right token is still not the lease holder.
	if _, err := store.CompleteEffect(context.Background(), CompleteEffectCommand{
		Fence: EffectDispatchFence{
			OutboxID: outboxID, DispatchToken: second[0].Fence.DispatchToken, LeaseOwnerID: "dispatcher_impostor",
		},
		Report: DeliveryReport{Outcome: DeliveryOutcomeDelivered},
	}); !errors.Is(err, ErrEffectFenced) {
		t.Fatalf("impostor CompleteEffect() error = %v, want ErrEffectFenced", err)
	}

	final, err := store.CompleteEffect(context.Background(), CompleteEffectCommand{
		Fence:  second[0].Fence,
		Report: DeliveryReport{Outcome: DeliveryOutcomeDelivered},
	})
	if err != nil || final.Status != EffectStatusDelivered || final.Replay {
		t.Fatalf("live CompleteEffect() = %+v, %v", final, err)
	}
	// Repeating the same completion is a replay, not a second delivery.
	replay, err := store.CompleteEffect(context.Background(), CompleteEffectCommand{
		Fence:  second[0].Fence,
		Report: DeliveryReport{Outcome: DeliveryOutcomeDelivered},
	})
	if err != nil || !replay.Replay || replay.Status != EffectStatusDelivered {
		t.Fatalf("repeat CompleteEffect() = %+v, %v", replay, err)
	}
	if got := effectRow(t, db, outboxID); got.DeliveryAttempts != 2 {
		t.Fatalf("replay changed the attempt count: %+v", got)
	}
}

func TestEffectDispatcherPartitionsByTopicAndDrainsBacklog(t *testing.T) {
	_, store, clock, turns := newSQLClaimNextFixture(t, "effect_topic_a", "effect_topic_b")
	seedEffects(t, store, clock, turns[0], "index", "writer.document.index", "writer.document.index2")
	seedEffects(t, store, clock, turns[1], "audit", "writer.document.audit")

	indexer := &testDeliverer{}
	dispatcher, err := NewEffectDispatcher(store, indexer, EffectDispatcherOptions{
		LeaseOwnerID: "dispatcher_topic", IdleBackoff: time.Millisecond, DeliveryTimeout: time.Second,
		Topics: []string{"writer.document.index", "writer.document.index2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := dispatcher.DispatchOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Claimed != 2 || report.Delivered != 2 {
		t.Fatalf("partitioned report = %+v, want only the two index effects", report)
	}
	for _, call := range indexer.seen() {
		if call.Topic == "writer.document.audit" {
			t.Fatalf("dispatcher crossed its topic partition: %+v", call)
		}
	}
	// The audit effect is untouched and still claimable by its own partition.
	auditor := &testDeliverer{}
	auditDispatcher, err := NewEffectDispatcher(store, auditor, EffectDispatcherOptions{
		LeaseOwnerID: "dispatcher_audit", IdleBackoff: time.Millisecond, DeliveryTimeout: time.Second,
		Topics: []string{"writer.document.audit"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if auditReport, err := auditDispatcher.DispatchOnce(context.Background()); err != nil || auditReport.Delivered != 1 {
		t.Fatalf("audit DispatchOnce() = %+v, %v", auditReport, err)
	}
}

func TestEffectDispatcherKeepsAnOwnedTopicPartition(t *testing.T) {
	db, store, clock, turns := newSQLClaimNextFixture(t, "effect_topic_copy_index", "effect_topic_copy_audit")
	index := seedEffects(t, store, clock, turns[0], "copy_index", "writer.document.index")
	audit := seedEffects(t, store, clock, turns[1], "copy_audit", "writer.document.audit")

	topics := []string{"writer.document.index"}
	deliverer := &testDeliverer{}
	dispatcher, err := NewEffectDispatcher(store, deliverer, EffectDispatcherOptions{
		LeaseOwnerID: "dispatcher_topic_copy", IdleBackoff: time.Millisecond, DeliveryTimeout: time.Second,
		Topics: topics,
	})
	if err != nil {
		t.Fatal(err)
	}

	// The caller first rewrites the backing array, then clears its own view.
	// Neither action may change the dispatcher's already-sealed partition.
	topics[0] = "writer.document.audit"
	topics = topics[:0]

	report, err := dispatcher.DispatchOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Claimed != 1 || report.Delivered != 1 {
		t.Fatalf("report = %+v, want exactly one index delivery", report)
	}
	seen := deliverer.seen()
	if len(seen) != 1 || seen[0].Topic != "writer.document.index" {
		t.Fatalf("deliveries = %+v, want only the original index topic", seen)
	}
	if got := effectRow(t, db, index[0].OutboxID); got.Status != string(EffectStatusDelivered) {
		t.Fatalf("index row = %+v, want delivered", got)
	}
	if got := effectRow(t, db, audit[0].OutboxID); got.Status != string(EffectStatusPending) {
		t.Fatalf("audit row = %+v, want untouched pending", got)
	}
}

func TestClaimEffectRechecksExactTopicUnderLockBeforeLeasing(t *testing.T) {
	db, store, clock, turns := newSQLClaimNextFixture(t, "effect_topic_lock")
	seeded := seedEffects(t, store, clock, turns[0], "topic_lock", "writer.Export")
	before := effectRow(t, db, seeded[0].OutboxID)

	delivery, claimed, err := store.claimEffect(
		context.Background(), seeded[0].OutboxID, "dispatcher_topic_lock",
		[]string{"writer.export"},
	)
	if !errors.Is(err, ErrEffectScopeMismatch) || claimed || delivery.OutboxID != "" {
		t.Fatalf("out-of-scope claim = %+v, claimed=%v, err=%v", delivery, claimed, err)
	}
	after := effectRow(t, db, seeded[0].OutboxID)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("case-mismatched locked claim changed durable state:\n before=%+v\n after=%+v", before, after)
	}

	delivery, claimed, err = store.claimEffect(
		context.Background(), seeded[0].OutboxID, "dispatcher_topic_lock",
		[]string{"writer.Export"},
	)
	if err != nil || !claimed || delivery.Topic != "writer.Export" {
		t.Fatalf("exact-topic claim = %+v, claimed=%v, err=%v", delivery, claimed, err)
	}
}

func TestEffectDispatcherRunDrainsThenStops(t *testing.T) {
	_, store, clock, turns := newSQLClaimNextFixture(t, "effect_run")
	seeded := seedEffects(t, store, clock, turns[0], "run",
		"writer.t0", "writer.t1", "writer.t2", "writer.t3")

	dispatcher := newTestDispatcher(t, store, &testDeliverer{}, "dispatcher_run")
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	var mu sync.Mutex
	delivered := 0
	done := make(chan struct{})
	runErr := make(chan error, 1)
	go func() {
		runErr <- dispatcher.Run(ctx, func(report EffectDispatchReport, err error) {
			if err != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			delivered += report.Delivered
			if delivered >= len(seeded) {
				select {
				case <-done:
				default:
					close(done)
				}
			}
		})
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("dispatcher did not drain the outbox")
	}
	stop()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
}

func TestNewEffectDispatcherRejectsUnsafeConfiguration(t *testing.T) {
	_, store, _, _ := newSQLClaimNextFixture(t, "effect_config")
	deliverer := &testDeliverer{}
	base := EffectDispatcherOptions{LeaseOwnerID: "d"}

	if _, err := NewEffectDispatcher(nil, deliverer, base); err == nil {
		t.Fatal("nil store accepted")
	}
	if _, err := NewEffectDispatcher(store, nil, base); err == nil {
		t.Fatal("nil deliverer accepted")
	}
	for name, mutate := range map[string]func(*EffectDispatcherOptions){
		"missing owner":   func(o *EffectDispatcherOptions) { o.LeaseOwnerID = "" },
		"oversized batch": func(o *EffectDispatcherOptions) { o.BatchLimit = MaxEffectClaimBatch + 1 },
		// A delivery outlasting its lease lets two dispatchers hit the provider.
		"delivery outlasts lease": func(o *EffectDispatcherOptions) { o.DeliveryTimeout = DefaultEffectLeaseTTL },
		"idle backoff too long":   func(o *EffectDispatcherOptions) { o.IdleBackoff = MaxWorkerIdleBackoff + time.Second },
	} {
		options := base
		mutate(&options)
		if _, err := NewEffectDispatcher(store, deliverer, options); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	if err := (DeliveryReport{Outcome: DeliveryOutcomeDelivered, ErrorCode: "why"}).Validate(); err == nil {
		t.Fatal("a delivered report carrying an error code was accepted")
	}
	if err := (DeliveryReport{Outcome: "invented"}).Validate(); err == nil {
		t.Fatal("an unknown outcome name was accepted")
	}
}

func TestEffectRetryDelayGrowsAndIsCapped(t *testing.T) {
	previous := time.Duration(0)
	for attempts := int64(1); attempts <= 20; attempts++ {
		delay := effectRetryDelay(attempts, 0)
		if delay < previous {
			t.Fatalf("attempt %d: delay %s went backwards from %s", attempts, delay, previous)
		}
		if delay > MaxEffectRetryBackoff {
			t.Fatalf("attempt %d: delay %s exceeds the cap", attempts, delay)
		}
		previous = delay
	}
	if effectRetryDelay(20, 0) != MaxEffectRetryBackoff {
		t.Fatal("backoff never reached its cap")
	}
	// A provider hint may extend the pause but never shorten the schedule.
	if got := effectRetryDelay(1, time.Minute); got != time.Minute {
		t.Fatalf("longer hint = %s, want it honoured", got)
	}
	if got := effectRetryDelay(4, time.Nanosecond); got != 8*DefaultEffectRetryBackoff {
		t.Fatalf("shorter hint = %s, want the dispatcher schedule", got)
	}
}
