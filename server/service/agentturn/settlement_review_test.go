package agentturn

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	agentv1 "server/contracts/agent/v1"
)

type testSettlementReviewAuthority struct {
	*testSettlementAuthority

	mu                   sync.Mutex
	holds                []SettlementReviewHoldCommand
	measurements         []MeasureSettlementReviewUsageCommand
	providerMeasurements []MeasureSettlementReviewProviderUsageCommand
	resolutions          []SettlementReviewResolutionAuthorityCommand
	holdFail             error
	measurementFail      error
	resolutionFail       error
	usedUnits            int64
	reservedUnits        int64
}

func newTestSettlementReviewAuthority(t *testing.T, db *gorm.DB) *testSettlementReviewAuthority {
	t.Helper()
	if err := db.Exec(`CREATE TABLE test_agent_settlement_review_hold (
		settlement_key TEXT PRIMARY KEY,
		review_id TEXT NOT NULL UNIQUE,
		request_digest TEXT NOT NULL
	)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE test_agent_settlement_review_resolution (
		resolution_id TEXT PRIMARY KEY,
		decision_digest TEXT NOT NULL UNIQUE,
		used_units INTEGER NOT NULL,
		reserved_units INTEGER NOT NULL,
		receipt_digest TEXT NOT NULL
	)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE test_agent_settlement_review_usage (
		evidence_id TEXT PRIMARY KEY,
		review_id TEXT NOT NULL UNIQUE,
		usage_source_digest TEXT NOT NULL UNIQUE,
		used_units INTEGER NOT NULL
	)`).Error; err != nil {
		t.Fatal(err)
	}
	return &testSettlementReviewAuthority{
		testSettlementAuthority: newTestSettlementAuthority(), usedUnits: 7, reservedUnits: 100,
	}
}

func (authority *testSettlementReviewAuthority) HoldForReview(tx *gorm.DB, command SettlementReviewHoldCommand) error {
	if err := command.Validate(); err != nil {
		return err
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.holdFail != nil {
		return authority.holdFail
	}
	if err := tx.Exec(`INSERT INTO test_agent_settlement_review_hold
		(settlement_key, review_id, request_digest) VALUES (?, ?, ?)`,
		command.Review.SettlementKey, command.Review.ReviewID, command.Review.RequestDigest,
	).Error; err != nil {
		return err
	}
	authority.holds = append(authority.holds, command)
	return nil
}

func (authority *testSettlementReviewAuthority) held() []SettlementReviewHoldCommand {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	return append([]SettlementReviewHoldCommand(nil), authority.holds...)
}

func (authority *testSettlementReviewAuthority) setHoldFailure(err error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.holdFail = err
}

func testSettlementReviewDigest(parts ...string) string {
	hash := sha256.New()
	settlementReviewHashParts(hash, parts...)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func (authority *testSettlementReviewAuthority) MeasureReview(
	tx *gorm.DB,
	command MeasureSettlementReviewUsageCommand,
) (SettlementReviewUsageAuthorityReceipt, error) {
	if err := command.Validate(); err != nil {
		return SettlementReviewUsageAuthorityReceipt{}, err
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.measurementFail != nil {
		return SettlementReviewUsageAuthorityReceipt{}, authority.measurementFail
	}
	receipt := SettlementReviewUsageAuthorityReceipt{
		EvidenceID: command.EvidenceID, ReviewID: command.Review.ReviewID,
		TurnID: command.Review.TurnID, SettlementKey: command.Review.SettlementKey,
		ReviewRequestDigest: command.Review.RequestDigest, Plugin: command.Plugin,
		BillingPolicyKey:      "writer.turn.v1",
		PricingSnapshotDigest: testSettlementReviewDigest("pricing", "writer.turn.v1"),
		MeterKey:              "workmax.test.meter", MeterVersion: "1.0.0",
		MeterBuildDigest:   testSettlementReviewDigest("meter-build", "1.0.0"),
		UsageSourceDigest:  testSettlementReviewDigest("usage-source", command.Review.ReviewID),
		MeasurementDigest:  testSettlementReviewDigest("measurement", command.Review.ReviewID),
		UsedUnits:          authority.usedUnits,
		MeterReceiptDigest: testSettlementReviewDigest("meter-receipt", command.Review.ReviewID),
	}
	if err := tx.Exec(`INSERT INTO test_agent_settlement_review_usage
		(evidence_id, review_id, usage_source_digest, used_units) VALUES (?, ?, ?, ?)`,
		receipt.EvidenceID, receipt.ReviewID, receipt.UsageSourceDigest, receipt.UsedUnits,
	).Error; err != nil {
		return SettlementReviewUsageAuthorityReceipt{}, err
	}
	authority.measurements = append(authority.measurements, command)
	return receipt, nil
}

func (authority *testSettlementReviewAuthority) measured() []MeasureSettlementReviewUsageCommand {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	return append([]MeasureSettlementReviewUsageCommand(nil), authority.measurements...)
}

func (authority *testSettlementReviewAuthority) MeasureProviderUsage(
	tx *gorm.DB,
	command MeasureSettlementReviewProviderUsageCommand,
) (SettlementReviewProviderUsageAuthorityReceipt, error) {
	if err := command.Validate(); err != nil {
		return SettlementReviewProviderUsageAuthorityReceipt{}, err
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.measurementFail != nil {
		return SettlementReviewProviderUsageAuthorityReceipt{}, authority.measurementFail
	}
	receipt := SettlementReviewProviderUsageAuthorityReceipt{
		MeasurementDigest: testSettlementReviewDigest("provider-measurement", command.Review.ReviewID),
		UsedUnits:         authority.usedUnits,
	}
	if err := tx.Exec(`INSERT INTO test_agent_settlement_review_usage
		(evidence_id, review_id, usage_source_digest, used_units) VALUES (?, ?, ?, ?)`,
		command.EvidenceID, command.Review.ReviewID, command.UsageSourceDigest, receipt.UsedUnits,
	).Error; err != nil {
		return SettlementReviewProviderUsageAuthorityReceipt{}, err
	}
	authority.providerMeasurements = append(authority.providerMeasurements, command)
	return receipt, nil
}

func (authority *testSettlementReviewAuthority) providerMeasured() []MeasureSettlementReviewProviderUsageCommand {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	return append([]MeasureSettlementReviewProviderUsageCommand(nil), authority.providerMeasurements...)
}

func (authority *testSettlementReviewAuthority) setMeasurementFailure(err error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.measurementFail = err
}

func (authority *testSettlementReviewAuthority) setUsedUnits(units int64) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.usedUnits = units
}

func (authority *testSettlementReviewAuthority) ResolveReview(
	tx *gorm.DB,
	command SettlementReviewResolutionAuthorityCommand,
) (SettlementReviewResolutionAuthorityReceipt, error) {
	if err := command.Validate(); err != nil {
		return SettlementReviewResolutionAuthorityReceipt{}, err
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.resolutionFail != nil {
		return SettlementReviewResolutionAuthorityReceipt{}, authority.resolutionFail
	}
	if command.UsedUnits > authority.reservedUnits {
		return SettlementReviewResolutionAuthorityReceipt{}, ErrSettlementReviewUnitsExceedReserved
	}
	receipt := SettlementReviewResolutionAuthorityReceipt{
		ResolutionID: command.ResolutionID, DecisionDigest: command.DecisionDigest,
		EvidenceID: command.Evidence.EvidenceID, EvidenceDigest: command.Evidence.EvidenceDigest,
		PricingSnapshotDigest: command.Evidence.PricingSnapshotDigest,
		UsedUnits:             command.UsedUnits, ReservedUnits: authority.reservedUnits,
		ReceiptDigest: "sha256:" + strings.Repeat("a", 64),
	}
	if err := tx.Exec(`INSERT INTO test_agent_settlement_review_resolution
		(resolution_id, decision_digest, used_units, reserved_units, receipt_digest)
		VALUES (?, ?, ?, ?, ?)`, receipt.ResolutionID, receipt.DecisionDigest,
		receipt.UsedUnits, receipt.ReservedUnits, receipt.ReceiptDigest).Error; err != nil {
		return SettlementReviewResolutionAuthorityReceipt{}, err
	}
	authority.resolutions = append(authority.resolutions, command)
	return receipt, nil
}

func (authority *testSettlementReviewAuthority) resolved() []SettlementReviewResolutionAuthorityCommand {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	return append([]SettlementReviewResolutionAuthorityCommand(nil), authority.resolutions...)
}

func (authority *testSettlementReviewAuthority) setResolutionFailure(err error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.resolutionFail = err
}

func (authority *testSettlementReviewAuthority) setReservedUnits(units int64) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.reservedUnits = units
}

func TestSettlementReviewTerminalizesAmbiguousReleaseAndReplays(t *testing.T) {
	db, store, clock, turns := newSQLClaimNextFixture(t, "review_terminal")
	turn := turns[0]
	authority := newTestSettlementReviewAuthority(t, db)
	store.WithSettlementAuthority(authority)

	claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_review_terminal"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_review_partial",
		Event: &EventDraft{Type: "writer.document.delta", Data: json.RawMessage(`{"partial":true}`)},
		Effects: []EffectOutboxDraft{
			executionTestEffect("outbox_review_partial", "writer.document.publish", "review-partial", clock.Get()),
		},
	}); err != nil {
		t.Fatal(err)
	}

	terminal := CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_review_timeout",
		TerminalStatus: agentv1.TurnStatusTimeout,
	}
	result, err := store.CommitAttempt(context.Background(), terminal)
	if err != nil {
		t.Fatalf("CommitAttempt() error = %v", err)
	}
	assertExecutorSettlementReview(t, result, turn.ID, "operation_review_timeout", 1, 1, 0)
	var eventPayload reconcileEventData
	if err := json.Unmarshal(result.Event.Data, &eventPayload); err != nil ||
		eventPayload.Reconciled || eventPayload.SettlementReviewID != result.SettlementReview.ReviewID ||
		eventPayload.SettlementReviewDigest != result.SettlementReview.RequestDigest {
		t.Fatalf("executor Review event marker = %+v, %v", eventPayload, err)
	}
	if result.TurnStatus != agentv1.TurnStatusTimeout || result.Attempt.Status != AttemptStatusTimeout {
		t.Fatalf("terminal review result = %+v", result)
	}
	if calls := authority.committed(); len(calls) != 0 {
		t.Fatalf("review release reached Settle: %+v", calls)
	}
	if holds := authority.held(); len(holds) != 1 || holds[0].Review.ReviewID != result.SettlementReview.ReviewID {
		t.Fatalf("review holds = %+v", holds)
	}
	if got := executionTableCount(t, db, "test_agent_settlement_review_hold", "settlement_key = ?", result.SettlementReview.SettlementKey); got != 1 {
		t.Fatalf("commercial review markers = %d, want 1", got)
	}

	var state executionTestTurnState
	executionTakeTurnState(t, db, turn.ID, &state)
	if state.Status != string(agentv1.TurnStatusTimeout) || state.ActiveAttemptID != nil || state.FinishedAt == nil {
		t.Fatalf("reviewed Turn state = %+v", state)
	}
	assertEffectReviewHeld(t, db, "outbox_review_partial")
	if _, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_after_review")); !errors.Is(err, ErrTurnTerminal) {
		t.Fatalf("ClaimAttempt() after review = %v, want ErrTurnTerminal", err)
	}
	if _, err := store.ClaimNext(context.Background(), claimNextCommand("attempt_next_after_review")); !errors.Is(err, ErrNoClaimableTurn) {
		t.Fatalf("ClaimNext() after review = %v, want ErrNoClaimableTurn", err)
	}
	if _, err := store.ClaimEffects(context.Background(), ClaimEffectsCommand{LeaseOwnerID: "dispatcher_after_review"}); !errors.Is(err, ErrNoClaimableEffects) {
		t.Fatalf("ClaimEffects() after review = %v, want ErrNoClaimableEffects", err)
	}

	reviews, err := store.ListSettlementReviews(context.Background(), ListSettlementReviewsQuery{})
	if err != nil || len(reviews) != 1 || reviews[0] != *result.SettlementReview {
		t.Fatalf("ListSettlementReviews() = %+v, %v", reviews, err)
	}
	replay, err := store.CommitAttempt(context.Background(), terminal)
	if err != nil || !replay.Replay || replay.SettlementReview == nil ||
		replay.SettlementReview.ReviewID != result.SettlementReview.ReviewID {
		t.Fatalf("review replay = %+v, %v", replay, err)
	}
	if len(authority.held()) != 1 || executionTableCount(t, db, SQLSettlementReviewTable, "turn_id = ?", turn.ID) != 1 {
		t.Fatal("review replay opened a second hold")
	}
}

func TestSettlementReviewHoldsCurrentTerminalEffects(t *testing.T) {
	db, store, clock, turns := newSQLClaimNextFixture(t, "review_current_effect")
	turn := turns[0]
	authority := newTestSettlementReviewAuthority(t, db)
	store.WithSettlementAuthority(authority)
	claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_review_current"))
	if err != nil {
		t.Fatal(err)
	}

	result, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_review_current",
		TerminalStatus: agentv1.TurnStatusFailed,
		Effects: []EffectOutboxDraft{
			executionTestEffect("outbox_review_current", "writer.document.publish", "review-current", clock.Get()),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertExecutorSettlementReview(t, result, turn.ID, "operation_review_current", 0, 0, 1)
	if len(result.Effects) != 1 || result.Effects[0].Status != string(EffectStatusReviewHold) {
		t.Fatalf("terminal review effects = %+v", result.Effects)
	}
	assertEffectReviewHeld(t, db, "outbox_review_current")
}

func TestSettlementReviewHoldFailureRollsBackKernelAndCommercialMarker(t *testing.T) {
	db, store, clock, turns := newSQLClaimNextFixture(t, "review_rollback")
	turn := turns[0]
	authority := newTestSettlementReviewAuthority(t, db)
	store.WithSettlementAuthority(authority)
	claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_review_rollback"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_review_rollback_partial",
		Event: &EventDraft{Type: "writer.document.delta", Data: json.RawMessage(`{"partial":true}`)},
		Effects: []EffectOutboxDraft{
			executionTestEffect("outbox_review_rollback", "writer.document.publish", "review-rollback", clock.Get()),
		},
	}); err != nil {
		t.Fatal(err)
	}
	var before executionTestTurnState
	executionTakeTurnState(t, db, turn.ID, &before)
	beforeEvents := executionTableCount(t, db, SQLTurnEventTable, "turn_id = ?", turn.ID)
	authority.setHoldFailure(errors.New("ledger unavailable with secret marker"))

	_, err = store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_review_rollback_terminal",
		TerminalStatus: agentv1.TurnStatusFailed,
	})
	if !errors.Is(err, ErrSettlementReviewFailed) || (err != nil && strings.Contains(err.Error(), "secret marker")) {
		t.Fatalf("CommitAttempt() error = %v, want sanitized ErrSettlementReviewFailed", err)
	}
	var after executionTestTurnState
	executionTakeTurnState(t, db, turn.ID, &after)
	assertSettlementGuardTurnUnchanged(t, before, after)
	if got := executionTableCount(t, db, SQLTurnEventTable, "turn_id = ?", turn.ID); got != beforeEvents {
		t.Fatalf("event count = %d, want %d", got, beforeEvents)
	}
	if got := executionTableCount(t, db, SQLTurnOperationTable, "turn_id = ?", turn.ID); got != 1 {
		t.Fatalf("operation count = %d, want only prior Operation", got)
	}
	if executionTableCount(t, db, SQLSettlementReviewTable, "turn_id = ?", turn.ID) != 0 ||
		executionTableCount(t, db, "test_agent_settlement_review_hold", "settlement_key <> ?", "") != 0 {
		t.Fatal("failed commercial hold left a Review or marker")
	}
	var effect sqlEffectOutboxRow
	if err := db.Table(SQLEffectOutboxTable).Where("outbox_id = ?", "outbox_review_rollback").Take(&effect).Error; err != nil {
		t.Fatal(err)
	}
	if effect.Status != string(EffectStatusPending) {
		t.Fatalf("rolled-back Effect status = %q, want pending", effect.Status)
	}
}

func TestSettlementReviewFencesDeliveryThatDidNotCompleteBeforeHold(t *testing.T) {
	db, store, clock, turns := newSQLClaimNextFixture(t, "review_delivery_fence")
	turn := turns[0]
	authority := newTestSettlementReviewAuthority(t, db)
	store.WithSettlementAuthority(authority)
	claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_review_delivery"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_review_delivery_partial",
		Event: &EventDraft{Type: "writer.document.delta", Data: json.RawMessage(`{"partial":true}`)},
		Effects: []EffectOutboxDraft{
			executionTestEffect("outbox_review_delivery", "writer.document.publish", "review-delivery", clock.Get()),
		},
	}); err != nil {
		t.Fatal(err)
	}
	deliveries, err := store.ClaimEffects(context.Background(), ClaimEffectsCommand{LeaseOwnerID: "dispatcher_review_delivery"})
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("ClaimEffects() = %+v, %v", deliveries, err)
	}

	if _, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_review_delivery_terminal",
		TerminalStatus: agentv1.TurnStatusTimeout,
	}); err != nil {
		t.Fatal(err)
	}
	assertEffectReviewHeld(t, db, "outbox_review_delivery")
	if _, err := store.CompleteEffect(context.Background(), CompleteEffectCommand{
		Fence: deliveries[0].Fence, Report: DeliveryReport{Outcome: DeliveryOutcomeDelivered},
	}); !errors.Is(err, ErrEffectFenced) {
		t.Fatalf("late CompleteEffect() = %v, want ErrEffectFenced", err)
	}
	assertEffectReviewHeld(t, db, "outbox_review_delivery")
}

func TestSettlementReviewReconcileTerminalizesAndStopsReclaim(t *testing.T) {
	db, store, clock, turns := newSQLClaimNextFixture(t, "review_reconcile")
	turn := turns[0]
	authority := newTestSettlementReviewAuthority(t, db)
	store.WithSettlementAuthority(authority)

	first, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_review_reconcile_a"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence: first.Attempt.Fence(), OperationID: "operation_review_reconcile_partial",
		Event: &EventDraft{Type: "writer.document.delta", Data: json.RawMessage(`{"partial":true}`)},
		Effects: []EffectOutboxDraft{
			executionTestEffect("outbox_review_reconcile", "writer.document.publish", "review-reconcile", clock.Get()),
		},
	}); err != nil {
		t.Fatal(err)
	}
	clock.Set(first.Attempt.LeaseExpiresAt)
	last := first
	for index := 1; index < DefaultMaxTurnAttempts; index++ {
		last, err = store.ClaimAttempt(context.Background(), executionClaimCommand(
			turn.ID, fmt.Sprintf("attempt_review_reconcile_%c", 'a'+index)))
		if err != nil {
			t.Fatal(err)
		}
		clock.Set(last.Attempt.LeaseExpiresAt)
	}

	command := ReconcileCommand{
		TurnID: turn.ID, Reason: ReclaimReasonAttemptsExhausted,
		ReconcilerID: "reconciler_review", ReconcilerBuildDigest: "sha256:reconciler-review",
	}
	result, err := store.ReconcileTerminal(context.Background(), command)
	if err != nil || !result.Changed || result.SettlementReview == nil {
		t.Fatalf("ReconcileTerminal() = %+v, %v", result, err)
	}
	review := result.SettlementReview
	if review.Source != SettlementReviewSourceReconcile || review.AttemptID != "" || review.OperationID != "" ||
		review.TerminalStatus != agentv1.TurnStatusTimeout || review.Evidence.PriorOperationCount != 1 ||
		review.Evidence.PriorEffectCount != 1 || review.Evidence.CurrentEffectCount != 0 {
		t.Fatalf("reconcile review = %+v", review)
	}
	var eventPayload reconcileEventData
	if err := json.Unmarshal(result.Event.Data, &eventPayload); err != nil ||
		eventPayload.SettlementReviewID != review.ReviewID ||
		eventPayload.SettlementReviewDigest != review.RequestDigest {
		t.Fatalf("reconcile Review event marker = %+v, %v", eventPayload, err)
	}
	assertEffectReviewHeld(t, db, "outbox_review_reconcile")
	if rows, err := store.ListReclaimableTurns(context.Background(), ReclaimQuery{}); err != nil || len(rows) != 0 {
		t.Fatalf("ListReclaimableTurns() after review = %+v, %v", rows, err)
	}
	replay, err := store.ReconcileTerminal(context.Background(), command)
	if err != nil || replay.Changed || replay.SettlementReview == nil || replay.SettlementReview.ReviewID != review.ReviewID {
		t.Fatalf("reconcile review replay = %+v, %v", replay, err)
	}
	if len(authority.held()) != 1 {
		t.Fatalf("reconcile replay holds = %+v", authority.held())
	}
}

func TestSettlementReviewReconcileAcceptsExecutorReviewThatWonTheRace(t *testing.T) {
	_, store, _, turnID := createReviewedTerminalForTest(t, "review_executor_won_reconcile")
	result, err := store.ReconcileTerminal(context.Background(), ReconcileCommand{
		TurnID: turnID, Reason: ReclaimReasonAttemptsExhausted,
		ReconcilerID:          "reconciler_after_executor_review",
		ReconcilerBuildDigest: "sha256:reconciler-after-executor-review",
	})
	if err != nil || result.Changed || result.SettlementReview == nil ||
		result.SettlementReview.Source != SettlementReviewSourceExecutor ||
		result.Turn.ID != turnID || result.TerminalStatus != agentv1.TurnStatusTimeout {
		t.Fatalf("ReconcileTerminal() after executor Review = %+v, %v", result, err)
	}
}

func TestSettlementReviewReconcileDetectsMissingExecutorReviewWithoutEffects(t *testing.T) {
	db, store, _, turns := newSQLClaimNextFixture(t, "review_executor_missing_receipt")
	turn := turns[0]
	authority := newTestSettlementReviewAuthority(t, db)
	store.WithSettlementAuthority(authority)
	claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(
		turn.ID, "attempt_review_executor_missing_receipt"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_review_executor_missing_partial",
		Event: &EventDraft{Type: "writer.document.delta", Data: json.RawMessage(`{"partial":true}`)},
	}); err != nil {
		t.Fatal(err)
	}
	terminal := CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_review_executor_missing_terminal",
		TerminalStatus: agentv1.TurnStatusTimeout,
	}
	if result, err := store.CommitAttempt(context.Background(), terminal); err != nil || result.SettlementReview == nil {
		t.Fatalf("terminal CommitAttempt() = %+v, %v", result, err)
	}
	if executionTableCount(t, db, SQLEffectOutboxTable, "turn_id = ?", turn.ID) != 0 {
		t.Fatal("executor missing-Review fixture unexpectedly has Effects")
	}
	if err := db.Table(SQLSettlementReviewTable).Where("turn_id = ?", turn.ID).
		Delete(&sqlSettlementReviewRow{}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReconcileTerminal(context.Background(), ReconcileCommand{
		TurnID: turn.ID, Reason: ReclaimReasonAttemptsExhausted,
		ReconcilerID:          "reconciler_executor_missing_review",
		ReconcilerBuildDigest: "sha256:reconciler-executor-missing-review",
	}); !errors.Is(err, ErrStoreIntegrity) {
		t.Fatalf("ReconcileTerminal() after missing executor Review = %v, want ErrStoreIntegrity", err)
	}
}

func TestSettlementReviewReconcileReplayDetectsMissingReviewWithoutEffects(t *testing.T) {
	db, store, clock, turns := newSQLClaimNextFixture(t, "review_reconcile_missing_receipt")
	turn := turns[0]
	authority := newTestSettlementReviewAuthority(t, db)
	store.WithSettlementAuthority(authority)

	first, err := store.ClaimAttempt(context.Background(), executionClaimCommand(
		turn.ID, "attempt_review_reconcile_missing_a"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence: first.Attempt.Fence(), OperationID: "operation_review_reconcile_missing_partial",
		Event: &EventDraft{Type: "writer.document.delta", Data: json.RawMessage(`{"partial":true}`)},
	}); err != nil {
		t.Fatal(err)
	}
	clock.Set(first.Attempt.LeaseExpiresAt)
	for index := 1; index < DefaultMaxTurnAttempts; index++ {
		claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(
			turn.ID, fmt.Sprintf("attempt_review_reconcile_missing_%d", index)))
		if err != nil {
			t.Fatal(err)
		}
		clock.Set(claimed.Attempt.LeaseExpiresAt)
	}
	command := ReconcileCommand{
		TurnID: turn.ID, Reason: ReclaimReasonAttemptsExhausted,
		ReconcilerID:          "reconciler_review_missing",
		ReconcilerBuildDigest: "sha256:reconciler-review-missing",
	}
	result, err := store.ReconcileTerminal(context.Background(), command)
	if err != nil || result.SettlementReview == nil || result.SettlementReview.Evidence.PriorEffectCount != 0 {
		t.Fatalf("initial ReconcileTerminal() = %+v, %v", result, err)
	}
	if err := db.Table(SQLSettlementReviewTable).Where("turn_id = ?", turn.ID).
		Delete(&sqlSettlementReviewRow{}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReconcileTerminal(context.Background(), command); !errors.Is(err, ErrStoreIntegrity) {
		t.Fatalf("ReconcileTerminal() after missing Review = %v, want ErrStoreIntegrity", err)
	}
}

func TestSettlementReviewReconcileReplayDetectsMissingCurrentEffect(t *testing.T) {
	db, store, clock, turns := newSQLClaimNextFixture(t, "review_reconcile_missing_current_effect")
	turn := turns[0]
	authority := newTestSettlementReviewAuthority(t, db)
	store.WithSettlementAuthority(authority)
	claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(
		turn.ID, "attempt_review_reconcile_missing_current_effect"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_review_reconcile_missing_current_effect",
		TerminalStatus: agentv1.TurnStatusFailed,
		Effects: []EffectOutboxDraft{executionTestEffect(
			"outbox_review_reconcile_missing_current_effect", "writer.document.publish",
			"review-reconcile-missing-current-effect", clock.Get(),
		)},
	})
	if err != nil || result.SettlementReview == nil || result.SettlementReview.Evidence.CurrentEffectCount != 1 {
		t.Fatalf("terminal CommitAttempt() = %+v, %v", result, err)
	}
	if err := db.Table(SQLEffectOutboxTable).Where("turn_id = ?", turn.ID).
		Delete(&sqlEffectOutboxRow{}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReconcileTerminal(context.Background(), ReconcileCommand{
		TurnID: turn.ID, Reason: ReclaimReasonAttemptsExhausted,
		ReconcilerID:          "reconciler_missing_current_effect",
		ReconcilerBuildDigest: "sha256:reconciler-missing-current-effect",
	}); !errors.Is(err, ErrStoreIntegrity) {
		t.Fatalf("ReconcileTerminal() after current Effect deletion = %v, want ErrStoreIntegrity", err)
	}
}

func TestSettlementReviewAuthorityBindingRejectsMutationAndTerminalizesNothing(t *testing.T) {
	db, store, _, turns := newSQLClaimNextFixture(t, "review_authority_binding")
	turn := turns[0]
	authority := newTestSettlementReviewAuthority(t, db)
	binding, err := store.BindSettlementReviewAuthority(authority)
	if err != nil || !store.MatchesSettlementAuthorityBinding(binding) {
		t.Fatalf("BindSettlementReviewAuthority() = %p, %v", binding, err)
	}
	if second, err := store.BindSettlementReviewAuthority(authority); second != nil ||
		!errors.Is(err, ErrSettlementBindingInvalid) {
		t.Fatalf("second binding = %p, %v", second, err)
	}
	if store.MatchesSettlementAuthorityBinding(&SettlementAuthorityBinding{}) {
		t.Fatal("Store accepted a forged Settlement Authority binding")
	}

	claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(
		turn.ID, "attempt_review_authority_binding"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_review_authority_binding_partial",
		Event: &EventDraft{Type: "writer.document.delta", Data: json.RawMessage(`{"partial":true}`)},
	}); err != nil {
		t.Fatal(err)
	}
	var before executionTestTurnState
	executionTakeTurnState(t, db, turn.ID, &before)

	store.WithSettlementAuthority(nil)
	if store.MatchesSettlementAuthorityBinding(binding) {
		t.Fatal("post-bind compatibility mutation left binding production-ready")
	}
	_, err = store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_review_authority_binding_terminal",
		TerminalStatus: agentv1.TurnStatusTimeout,
	})
	if !errors.Is(err, ErrSettlementUsageUnknown) {
		t.Fatalf("terminal commit after binding mutation = %v, want usage unknown", err)
	}
	var after executionTestTurnState
	executionTakeTurnState(t, db, turn.ID, &after)
	assertSettlementGuardTurnUnchanged(t, before, after)
	if len(authority.held()) != 0 || executionTableCount(t, db, SQLSettlementReviewTable, "turn_id = ?", turn.ID) != 0 {
		t.Fatal("invalid binding opened a Settlement Review")
	}
}

func TestSettlementReviewAndEffectClaimSerializeAcrossStores(t *testing.T) {
	for iteration := 0; iteration < 12; iteration++ {
		t.Run(fmt.Sprintf("iteration_%02d", iteration), func(t *testing.T) {
			db, store, clock, turns := newSQLClaimNextFixture(t, fmt.Sprintf("review_claim_race_%02d", iteration))
			turn := turns[0]
			authority := newTestSettlementReviewAuthority(t, db)
			store.WithSettlementAuthority(authority)
			dispatchStore, err := NewSQLStore(db)
			if err != nil {
				t.Fatal(err)
			}
			dispatchStore.executionClock = clock.Now
			claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(
				turn.ID, fmt.Sprintf("attempt_review_claim_race_%02d", iteration)))
			if err != nil {
				t.Fatal(err)
			}
			outboxID := fmt.Sprintf("outbox_review_claim_race_%02d", iteration)
			if _, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
				Fence: claimed.Attempt.Fence(), OperationID: fmt.Sprintf("operation_review_claim_race_partial_%02d", iteration),
				Event: &EventDraft{Type: "writer.document.delta", Data: json.RawMessage(`{"partial":true}`)},
				Effects: []EffectOutboxDraft{
					executionTestEffect(outboxID, "writer.document.publish", fmt.Sprintf("review-claim-race-%02d", iteration), clock.Get()),
				},
			}); err != nil {
				t.Fatal(err)
			}

			start := make(chan struct{})
			reviewErr := make(chan error, 1)
			claimErr := make(chan error, 1)
			go func() {
				<-start
				_, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
					Fence: claimed.Attempt.Fence(), OperationID: fmt.Sprintf("operation_review_claim_race_terminal_%02d", iteration),
					TerminalStatus: agentv1.TurnStatusTimeout,
				})
				reviewErr <- err
			}()
			go func() {
				<-start
				_, err := dispatchStore.ClaimEffects(context.Background(), ClaimEffectsCommand{
					LeaseOwnerID: fmt.Sprintf("dispatcher_review_claim_race_%02d", iteration),
				})
				claimErr <- err
			}()
			close(start)
			if err := <-reviewErr; err != nil {
				t.Fatalf("review commit = %v", err)
			}
			if err := <-claimErr; err != nil && !errors.Is(err, ErrNoClaimableEffects) {
				t.Fatalf("effect claim = %v", err)
			}
			assertEffectReviewHeld(t, db, outboxID)
		})
	}
}

func TestSettlementReviewPreservesEffectCompletionThatWonBeforeHold(t *testing.T) {
	db, store, clock, turns := newSQLClaimNextFixture(t, "review_completed_effect")
	turn := turns[0]
	authority := newTestSettlementReviewAuthority(t, db)
	store.WithSettlementAuthority(authority)
	claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_review_completed_effect"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_review_completed_effect_partial",
		Event: &EventDraft{Type: "writer.document.delta", Data: json.RawMessage(`{"partial":true}`)},
		Effects: []EffectOutboxDraft{
			executionTestEffect("outbox_review_completed_effect", "writer.document.publish", "review-completed-effect", clock.Get()),
		},
	}); err != nil {
		t.Fatal(err)
	}
	deliveries, err := store.ClaimEffects(context.Background(), ClaimEffectsCommand{LeaseOwnerID: "dispatcher_review_completed_effect"})
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("ClaimEffects() = %+v, %v", deliveries, err)
	}
	if _, err := store.CompleteEffect(context.Background(), CompleteEffectCommand{
		Fence: deliveries[0].Fence, Report: DeliveryReport{Outcome: DeliveryOutcomeDelivered},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_review_completed_effect_terminal",
		TerminalStatus: agentv1.TurnStatusFailed,
	}); err != nil {
		t.Fatal(err)
	}
	var effect sqlEffectOutboxRow
	if err := db.Table(SQLEffectOutboxTable).Where("outbox_id = ?", "outbox_review_completed_effect").Take(&effect).Error; err != nil {
		t.Fatal(err)
	}
	if effect.Status != string(EffectStatusDelivered) || effect.DeliveredAt == nil || !validEffectOutboxState(effect) {
		t.Fatalf("completion that preceded review = %+v", effect)
	}
}

func TestSettlementReviewHoldKeepsEffectUpdatedAtMonotonic(t *testing.T) {
	db, store, clock, turns := newSQLClaimNextFixture(t, "review_effect_time")
	turn := turns[0]
	authority := newTestSettlementReviewAuthority(t, db)
	store.WithSettlementAuthority(authority)
	claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_review_effect_time"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_review_effect_time_partial",
		Event: &EventDraft{Type: "writer.document.delta", Data: json.RawMessage(`{"partial":true}`)},
		Effects: []EffectOutboxDraft{
			executionTestEffect("outbox_review_effect_time", "writer.document.publish", "review-effect-time", clock.Get()),
		},
	}); err != nil {
		t.Fatal(err)
	}
	future := clock.Get().Add(2 * time.Hour).UTC()
	if err := db.Table(SQLEffectOutboxTable).Where("outbox_id = ?", "outbox_review_effect_time").
		UpdateColumn("updated_at", future).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_review_effect_time_terminal",
		TerminalStatus: agentv1.TurnStatusTimeout,
	}); err != nil {
		t.Fatal(err)
	}
	var effect sqlEffectOutboxRow
	if err := db.Table(SQLEffectOutboxTable).Where("outbox_id = ?", "outbox_review_effect_time").Take(&effect).Error; err != nil {
		t.Fatal(err)
	}
	if effect.UpdatedAt.Before(future) || effect.Status != string(EffectStatusReviewHold) {
		t.Fatalf("held Effect time = %s status = %s, want >= %s and review_hold", effect.UpdatedAt, effect.Status, future)
	}
}

func TestSettlementReviewReplayRejectsMissingOrTamperedReview(t *testing.T) {
	for name, corrupt := range map[string]func(*testing.T, *gorm.DB, agentv1.TurnID){
		"missing": func(t *testing.T, db *gorm.DB, turnID agentv1.TurnID) {
			if err := db.Table(SQLSettlementReviewTable).Where("turn_id = ?", turnID).
				Delete(&sqlSettlementReviewRow{}).Error; err != nil {
				t.Fatal(err)
			}
		},
		"tampered digest": func(t *testing.T, db *gorm.DB, turnID agentv1.TurnID) {
			if err := db.Table(SQLSettlementReviewTable).Where("turn_id = ?", turnID).
				UpdateColumn("request_digest", "sha256:"+strings.Repeat("0", 64)).Error; err != nil {
				t.Fatal(err)
			}
		},
		"escaped effect": func(t *testing.T, db *gorm.DB, turnID agentv1.TurnID) {
			if err := db.Table(SQLEffectOutboxTable).Where("turn_id = ?", turnID).
				UpdateColumn("status", string(EffectStatusPending)).Error; err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			db, store, terminal, turnID := createReviewedTerminalForTest(t, "review_tamper_"+strings.ReplaceAll(name, " ", "_"))
			corrupt(t, db, turnID)
			if _, err := store.CommitAttempt(context.Background(), terminal); !errors.Is(err, ErrStoreIntegrity) {
				t.Fatalf("tampered review replay = %v, want ErrStoreIntegrity", err)
			}
		})
	}
}

func TestSettlementReviewLedgerBlocksEffectWhoseHoldStateWasTampered(t *testing.T) {
	db, store, terminal, turnID := createReviewedTerminalForTest(t, "review_effect_escape")
	if err := db.Table(SQLEffectOutboxTable).Where("turn_id = ?", turnID).
		UpdateColumn("status", string(EffectStatusPending)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitAttempt(context.Background(), terminal); !errors.Is(err, ErrStoreIntegrity) {
		t.Fatalf("terminal replay with escaped Effect = %v, want ErrStoreIntegrity", err)
	}
	if deliveries, err := store.ClaimEffects(context.Background(), ClaimEffectsCommand{
		LeaseOwnerID: "dispatcher_review_effect_escape",
	}); len(deliveries) != 0 || !errors.Is(err, ErrStoreIntegrity) {
		t.Fatalf("ClaimEffects() with escaped Review Effect = %+v, %v", deliveries, err)
	}
}

func createReviewedTerminalForTest(
	t *testing.T,
	suffix string,
) (*gorm.DB, *SQLStore, CommitAttemptCommand, agentv1.TurnID) {
	t.Helper()
	db, store, clock, turns := newSQLClaimNextFixture(t, suffix)
	turn := turns[0]
	authority := newTestSettlementReviewAuthority(t, db)
	store.WithSettlementAuthority(authority)
	claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_"+suffix))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_partial_" + suffix,
		Event: &EventDraft{Type: "writer.document.delta", Data: json.RawMessage(`{"partial":true}`)},
		Effects: []EffectOutboxDraft{executionTestEffect(
			"outbox_partial_"+suffix, "writer.document.publish", "partial-"+suffix, clock.Get(),
		)},
	}); err != nil {
		t.Fatal(err)
	}
	terminal := CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_terminal_" + suffix,
		TerminalStatus: agentv1.TurnStatusTimeout,
	}
	if _, err := store.CommitAttempt(context.Background(), terminal); err != nil {
		t.Fatal(err)
	}
	return db, store, terminal, turn.ID
}

func assertExecutorSettlementReview(
	t *testing.T,
	result CommitAttemptResult,
	turnID agentv1.TurnID,
	operationID string,
	priorOperations, priorEffects int64,
	currentEffects int,
) {
	t.Helper()
	if result.SettlementReview == nil {
		t.Fatal("terminal result has no Settlement Review")
	}
	review := result.SettlementReview
	if err := review.Validate(); err != nil {
		t.Fatalf("Settlement Review invalid: %v", err)
	}
	if review.TurnID != turnID || review.Source != SettlementReviewSourceExecutor ||
		review.OperationID != operationID || review.AttemptID != result.Attempt.ID ||
		review.FencingToken != result.Attempt.FencingToken || review.TerminalStatus != result.TurnStatus ||
		review.Evidence != (SettlementUsageEvidence{
			PriorOperationCount: priorOperations,
			PriorEffectCount:    priorEffects,
			CurrentEffectCount:  currentEffects,
		}) {
		t.Fatalf("Settlement Review = %+v", review)
	}
}

func assertEffectReviewHeld(t *testing.T, db *gorm.DB, outboxID string) {
	t.Helper()
	var row sqlEffectOutboxRow
	if err := db.Table(SQLEffectOutboxTable).Where("outbox_id = ?", outboxID).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != string(EffectStatusReviewHold) || row.LeaseOwnerID != nil || row.LeaseExpiresAt != nil ||
		row.DeliveredAt != nil || row.DeadLetteredAt != nil || !validEffectOutboxState(row) {
		t.Fatalf("held Effect = %+v", row)
	}
}
