package agentbilling

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	agentv1 "server/contracts/agent/v1"
	"server/model"
	"server/service/account"
	"server/service/agentturn"
	"server/utils/testutil"
)

type pendingReconcileHarness struct {
	db           *gorm.DB
	store        *agentturn.SQLStore
	authority    *CreditSettlementAuthority
	reservations *account.CreditReservationService
	userID       int
	packID       uint
}

type pendingReconcileItem struct {
	turn          agentturn.Turn
	attempt       agentturn.TurnAttempt
	reservationID uint
	operationID   string
}

func assertPendingReconcilePassCounts(
	t *testing.T,
	got PendingReconcilePassResult,
	discovered, attempted, converged, stillPending, failed int,
) {
	t.Helper()
	if got.Discovered != discovered || got.Attempted != attempted || got.Converged != converged ||
		got.StillPending != stillPending || got.Failed != failed {
		t.Fatalf("pass result = %+v, want counts discovered=%d attempted=%d converged=%d stillPending=%d failed=%d",
			got, discovered, attempted, converged, stillPending, failed)
	}
	detailCount := 0
	omitted := 0
	if got.FailureDetails != nil {
		detailCount = len(got.FailureDetails.Items)
		omitted = got.FailureDetails.Omitted
	}
	if detailCount+omitted != failed {
		t.Fatalf("failure details = %+v, failed = %d", got.FailureDetails, failed)
	}
}

func newPendingReconcileHarness(t *testing.T, suffix string) *pendingReconcileHarness {
	t.Helper()
	return newPendingReconcileHarnessWithDB(t, suffix, testutil.NewTestDB(t))
}

func newPersistentPendingReconcileHarness(t *testing.T, suffix string) *pendingReconcileHarness {
	t.Helper()
	return newPendingReconcileHarnessWithDB(t, suffix, testutil.NewPersistentTestDB(t))
}

func newPendingReconcileHarnessWithDB(t *testing.T, suffix string, db *gorm.DB) *pendingReconcileHarness {
	t.Helper()
	user := model.User{Member: 0, Nickname: "pending-reconcile", Email: "pending-" + suffix + "@example.com"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	pack := model.CreditsPack{
		UID: int(user.Id), SourceType: model.CreditsSourcePurchase,
		SourceID: "pending-reconcile-" + suffix, CreditsTotal: 1000,
	}
	if err := db.Create(&pack).Error; err != nil {
		t.Fatalf("seed pack: %v", err)
	}
	reservations := account.NewCreditReservationService()
	authority, err := NewCreditSettlementAuthority(db, reservations)
	if err != nil {
		t.Fatal(err)
	}
	store, err := agentturn.NewSQLStore(db)
	if err != nil {
		t.Fatal(err)
	}
	store.WithSettlementAuthority(authority)
	return &pendingReconcileHarness{
		db: db, store: store, authority: authority, reservations: reservations,
		userID: int(user.Id), packID: pack.Id,
	}
}

func (h *pendingReconcileHarness) admitAndClaim(t *testing.T, suffix string) pendingReconcileItem {
	t.Helper()
	turn := billingTestTurn("batch_" + suffix)
	admission, err := h.authority.Admission(ReservationAdmission{
		PrincipalID: turn.PrincipalID,
		Reservation: account.ReservationRequest{
			UID: h.userID, Tool: "workagent", IdempotencyKey: "reservation-batch-" + suffix,
			Reserved: 10, TTL: time.Hour, Remark: "pending reconcile batch test",
		},
		PricingSnapshotDigest: digest("agentbilling-batch-pricing-v1", suffix),
	})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := h.store.AdmitWithReservationAuthority(
		context.Background(), turn,
		agentturn.EventDraft{Type: agentv1.EventCoreTurnStatus, Data: json.RawMessage(`{"status":"queued"}`)},
		admission,
	)
	if err != nil || !admitted.Created {
		t.Fatalf("admit %s = %+v, %v", suffix, admitted, err)
	}
	var binding bindingRow
	if err := h.db.Table(BindingTable).Where("turn_id = ?", string(turn.ID)).Take(&binding).Error; err != nil {
		t.Fatalf("load binding %s: %v", suffix, err)
	}
	claimed, err := h.store.ClaimAttempt(context.Background(), agentturn.ClaimAttemptCommand{
		TurnID: turn.ID, AttemptID: "attempt_batch_" + suffix,
		WorkerID: "worker_pending_reconcile", WorkerBuildDigest: "sha256:worker-pending-reconcile",
	})
	if err != nil {
		t.Fatalf("claim %s: %v", suffix, err)
	}
	return pendingReconcileItem{
		turn: admitted.Turn, attempt: claimed.Attempt,
		reservationID: uint(binding.ReservationID), operationID: "operation_batch_" + suffix,
	}
}

func (h *pendingReconcileHarness) makePending(
	t *testing.T,
	suffix string,
	due time.Time,
	repairAllocation bool,
) pendingReconcileItem {
	t.Helper()
	item := h.admitAndClaim(t, suffix)
	if err := h.db.Where("reservation_id = ?", item.reservationID).
		Delete(&model.CreditReservationAllocation{}).Error; err != nil {
		t.Fatalf("drop allocation %s: %v", suffix, err)
	}
	result, err := h.store.CommitAttempt(context.Background(), agentturn.CommitAttemptCommand{
		Fence: item.attempt.Fence(), OperationID: item.operationID,
		TerminalStatus: agentv1.TurnStatusCompleted,
		Settlement:     &agentturn.SettlementRequest{Intent: agentturn.SettlementIntentFinalize, UsedUnits: 4},
	})
	if err != nil || result.Replay || result.TurnStatus != agentv1.TurnStatusCompleted {
		t.Fatalf("queue pending %s = %+v, %v", suffix, result, err)
	}
	if repairAllocation {
		if err := h.db.Create(&model.CreditReservationAllocation{
			ReservationID: item.reservationID, PackID: h.packID, Credits: 10,
		}).Error; err != nil {
			t.Fatalf("repair allocation %s: %v", suffix, err)
		}
	}
	if err := h.db.Model(&model.CreditReservation{}).Where("id = ?", item.reservationID).
		Update("next_refund_at", due.UTC()).Error; err != nil {
		t.Fatalf("set refund due %s: %v", suffix, err)
	}
	reservation := h.loadReservation(t, item.reservationID)
	if reservation.Status != model.CreditReservationStatusRefundPending {
		t.Fatalf("pending setup %s status = %q", suffix, reservation.Status)
	}
	return item
}

func (h *pendingReconcileHarness) makeTerminal(t *testing.T, suffix string) pendingReconcileItem {
	t.Helper()
	item := h.admitAndClaim(t, suffix)
	result, err := h.store.CommitAttempt(context.Background(), agentturn.CommitAttemptCommand{
		Fence: item.attempt.Fence(), OperationID: item.operationID,
		TerminalStatus: agentv1.TurnStatusCompleted,
		Settlement:     &agentturn.SettlementRequest{Intent: agentturn.SettlementIntentFinalize, UsedUnits: 10},
	})
	if err != nil || result.TurnStatus != agentv1.TurnStatusCompleted {
		t.Fatalf("terminal setup %s = %+v, %v", suffix, result, err)
	}
	return item
}

func (h *pendingReconcileHarness) makeUnboundPending(t *testing.T, suffix string, due time.Time) uint {
	t.Helper()
	var reservationID uint
	if err := h.db.Transaction(func(tx *gorm.DB) error {
		result, err := h.reservations.Reserve(tx, account.ReservationRequest{
			UID: h.userID, Tool: "ordinary", IdempotencyKey: "unbound-batch-" + suffix,
			Reserved: 10, TTL: time.Hour,
		})
		if err != nil {
			return err
		}
		reservationID = result.Reservation.Id
		return nil
	}); err != nil {
		t.Fatalf("reserve unbound %s: %v", suffix, err)
	}
	if err := h.db.Where("reservation_id = ?", reservationID).
		Delete(&model.CreditReservationAllocation{}).Error; err != nil {
		t.Fatalf("drop unbound allocation %s: %v", suffix, err)
	}
	if err := h.db.Transaction(func(tx *gorm.DB) error {
		_, err := h.reservations.FinalizeWithResult(tx, reservationID, 4)
		return err
	}); err != nil {
		t.Fatalf("queue unbound pending %s: %v", suffix, err)
	}
	if err := h.db.Model(&model.CreditReservation{}).Where("id = ?", reservationID).
		Update("next_refund_at", due.UTC()).Error; err != nil {
		t.Fatalf("set unbound refund due %s: %v", suffix, err)
	}
	return reservationID
}

func (h *pendingReconcileHarness) loadReservation(t *testing.T, reservationID uint) model.CreditReservation {
	t.Helper()
	var reservation model.CreditReservation
	if err := h.db.First(&reservation, reservationID).Error; err != nil {
		t.Fatal(err)
	}
	return reservation
}

func TestPendingReconcileDiscoveryFiltersExactDueRows(t *testing.T) {
	harness := newPendingReconcileHarness(t, "discovery")
	now := time.Now().UTC()
	due := harness.makePending(t, "discovery_due", now.Add(-4*time.Minute), true)
	notDue := harness.makePending(t, "discovery_not_due", now.Add(time.Hour), true)
	terminal := harness.makeTerminal(t, "discovery_terminal")
	unboundID := harness.makeUnboundPending(t, "discovery_unbound", now.Add(-3*time.Minute))

	for _, limit := range []int{0, MaxPendingReconcileBatchSize + 1} {
		if _, err := harness.authority.DiscoverDuePending(context.Background(), limit); !errors.Is(err, ErrPendingReconcileLimit) {
			t.Fatalf("limit %d error = %v", limit, err)
		}
	}
	candidates, err := harness.authority.DiscoverDuePending(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].TurnID != due.turn.ID ||
		candidates[0].PrincipalID != due.turn.PrincipalID ||
		candidates[0].ReservationID != uint64(due.reservationID) ||
		candidates[0].SettlementKey != billingSettlementKey(due.turn.ID, due.operationID) {
		t.Fatalf("due candidates = %+v", candidates)
	}

	result, err := harness.authority.ReconcileDuePendingPass(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	assertPendingReconcilePassCounts(t, result, 1, 1, 1, 0, 0)
	if got := harness.loadReservation(t, due.reservationID).Status; got != model.CreditReservationStatusFinalized {
		t.Fatalf("due status = %q", got)
	}
	if got := harness.loadReservation(t, notDue.reservationID).Status; got != model.CreditReservationStatusRefundPending {
		t.Fatalf("not-due status = %q", got)
	}
	if got := harness.loadReservation(t, terminal.reservationID).Status; got != model.CreditReservationStatusFinalized {
		t.Fatalf("terminal status = %q", got)
	}
	if got := harness.loadReservation(t, unboundID).Status; got != model.CreditReservationStatusRefundPending {
		t.Fatalf("unbound status = %q", got)
	}
}

func TestPendingReconcilePassUsesStableStrictLimit(t *testing.T) {
	harness := newPendingReconcileHarness(t, "limit")
	now := time.Now().UTC()
	oldest := harness.makePending(t, "limit_oldest", now.Add(-5*time.Minute), true)
	sameFirst := harness.makePending(t, "limit_same_first", now.Add(-3*time.Minute), true)
	sameSecond := harness.makePending(t, "limit_same_second", now.Add(-3*time.Minute), true)

	candidates, prepared, exhausted, err := harness.authority.DiscoverDuePendingAfter(
		context.Background(), PendingReconcileCursor{}, 2,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 || candidates[0].TurnID != oldest.turn.ID || candidates[1].TurnID != sameFirst.turn.ID ||
		prepared.CycleHighWatermark == 0 || exhausted {
		t.Fatalf("stable limited page = candidates:%+v cursor:%+v exhausted:%t", candidates, prepared, exhausted)
	}
	if candidates[0].OutcomeRowID == 0 || candidates[0].OutcomeRowID >= candidates[1].OutcomeRowID {
		t.Fatalf("outcome keyset identities = %+v", candidates)
	}
	tailCursor := prepared
	tailCursor.OutcomeRowID = candidates[1].OutcomeRowID
	tail, tailPrepared, exhausted, err := harness.authority.DiscoverDuePendingAfter(
		context.Background(), tailCursor, 2,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(tail) != 1 || tail[0].TurnID != sameSecond.turn.ID ||
		tailPrepared != tailCursor || !exhausted {
		t.Fatalf("finite-cycle tail = candidates:%+v cursor:%+v exhausted:%t", tail, tailPrepared, exhausted)
	}
	result, err := harness.authority.ReconcileDuePendingPass(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	assertPendingReconcilePassCounts(t, result, 2, 2, 2, 0, 0)
	if got := harness.loadReservation(t, oldest.reservationID).Status; got != model.CreditReservationStatusFinalized {
		t.Fatalf("oldest status = %q", got)
	}
	if got := harness.loadReservation(t, sameFirst.reservationID).Status; got != model.CreditReservationStatusFinalized {
		t.Fatalf("same-first status = %q", got)
	}
	if got := harness.loadReservation(t, sameSecond.reservationID).Status; got != model.CreditReservationStatusRefundPending {
		t.Fatalf("strict-limit remainder status = %q", got)
	}
	remaining, err := harness.authority.DiscoverDuePending(context.Background(), 2)
	if err != nil || len(remaining) != 1 || remaining[0].TurnID != sameSecond.turn.ID {
		t.Fatalf("remaining candidates = %+v, %v", remaining, err)
	}
}

func TestPendingReconcileRejectsMalformedCursor(t *testing.T) {
	harness := newPendingReconcileHarness(t, "cursor-guard")
	harness.makePending(t, "cursor_guard_due", time.Now().UTC().Add(-time.Minute), true)
	candidates, prepared, _, err := harness.authority.DiscoverDuePendingAfter(
		context.Background(), PendingReconcileCursor{}, 1,
	)
	if err != nil || len(candidates) != 1 || prepared.CycleHighWatermark == 0 {
		t.Fatalf("prepare pending cursor = %+v/%+v, %v", candidates, prepared, err)
	}
	for name, cursor := range map[string]PendingReconcileCursor{
		"position without generation": {OutcomeRowID: candidates[0].OutcomeRowID},
		"position beyond high": {
			OutcomeRowID: prepared.CycleHighWatermark + 1, CycleHighWatermark: prepared.CycleHighWatermark,
		},
		"forged high": {
			OutcomeRowID: prepared.CycleHighWatermark, CycleHighWatermark: prepared.CycleHighWatermark + 100,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := harness.authority.DiscoverDuePendingAfter(
				context.Background(), cursor, 1,
			); !errors.Is(err, ErrPendingReconcileCursor) {
				t.Fatalf("malformed cursor error = %v, want ErrPendingReconcileCursor", err)
			}
		})
	}
}

func TestPendingReconcileCursorPreventsOldestPoisonStarvation(t *testing.T) {
	harness := newPendingReconcileHarness(t, "cursor-fairness")
	now := time.Now().UTC()
	poison := harness.makePending(t, "cursor_poison", now.Add(-3*time.Minute), true)
	second := harness.makePending(t, "cursor_second", now.Add(-2*time.Minute), true)
	third := harness.makePending(t, "cursor_third", now.Add(-time.Minute), true)

	if err := harness.db.Exec(`CREATE TRIGGER fail_cursor_poison_outcome
		BEFORE UPDATE ON w_agent_turn_settlement_outcome
		WHEN OLD.turn_id = 'turn_billing_batch_cursor_poison'
		BEGIN SELECT RAISE(FAIL, 'driver detail must not escape'); END`).Error; err != nil {
		t.Fatal(err)
	}
	firstPass, err := harness.authority.ReconcileDuePendingPass(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	assertPendingReconcilePassCounts(t, firstPass, 1, 1, 0, 0, 1)
	if firstPass.NextCursor.OutcomeRowID == 0 || firstPass.FailureDetails == nil ||
		len(firstPass.FailureDetails.Items) != 1 ||
		firstPass.FailureDetails.Items[0].Code != PendingReconcileFailureReconcileFailed ||
		firstPass.FailureDetails.Items[0].TurnID != poison.turn.ID {
		t.Fatalf("first poison diagnostic = %+v", firstPass)
	}

	secondPass, err := harness.authority.ReconcileDuePendingPass(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	assertPendingReconcilePassCounts(t, secondPass, 1, 1, 1, 0, 0)
	if got := harness.loadReservation(t, second.reservationID).Status; got != model.CreditReservationStatusFinalized {
		t.Fatalf("second row was starved, status = %q", got)
	}
	if got := harness.loadReservation(t, third.reservationID).Status; got != model.CreditReservationStatusRefundPending {
		t.Fatalf("third row advanced too early, status = %q", got)
	}

	thirdPass, err := harness.authority.ReconcileDuePendingPass(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	assertPendingReconcilePassCounts(t, thirdPass, 1, 1, 1, 0, 0)
	if got := harness.loadReservation(t, third.reservationID).Status; got != model.CreditReservationStatusFinalized {
		t.Fatalf("third row was starved, status = %q", got)
	}

	wrappedPass, err := harness.authority.ReconcileDuePendingPass(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	assertPendingReconcilePassCounts(t, wrappedPass, 1, 1, 0, 0, 1)
	if wrappedPass.NextCursor != (PendingReconcileCursor{}) || wrappedPass.FailureDetails == nil ||
		len(wrappedPass.FailureDetails.Items) != 1 ||
		wrappedPass.FailureDetails.Items[0].TurnID != poison.turn.ID {
		t.Fatalf("cursor did not complete wrapped poison cycle: %+v", wrappedPass)
	}
	if got := harness.loadReservation(t, poison.reservationID).Status; got != model.CreditReservationStatusRefundPending {
		t.Fatalf("poison status = %q", got)
	}
}

func TestPendingReconcileFiniteCycleRevisitsPoisonDuringContinuousGrowth(t *testing.T) {
	harness := newPendingReconcileHarness(t, "continuous-growth")
	now := time.Now().UTC()
	poison := harness.makePending(t, "continuous_poison", now.Add(-4*time.Minute), true)
	firstGood := harness.makePending(t, "continuous_first_good", now.Add(-3*time.Minute), true)
	if err := harness.db.Exec(`CREATE TRIGGER fail_continuous_poison_outcome
		BEFORE UPDATE ON w_agent_turn_settlement_outcome
		WHEN OLD.turn_id = 'turn_billing_batch_continuous_poison'
		BEGIN SELECT RAISE(FAIL, 'continuous poison driver detail'); END`).Error; err != nil {
		t.Fatal(err)
	}

	firstPass, err := harness.authority.ReconcileDuePendingPass(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	assertPendingReconcilePassCounts(t, firstPass, 1, 1, 0, 0, 1)
	if firstPass.NextCursor.CycleHighWatermark == 0 {
		t.Fatalf("finite cycle was not captured: %+v", firstPass.NextCursor)
	}

	// Add at least one full page after every pass. The captured generation must
	// finish instead of chasing the ever-growing tail.
	harness.makePending(t, "continuous_growth_one", now.Add(-2*time.Minute), true)
	secondPass, err := harness.authority.ReconcileDuePendingPass(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	assertPendingReconcilePassCounts(t, secondPass, 1, 1, 1, 0, 0)
	if secondPass.NextCursor != (PendingReconcileCursor{}) {
		t.Fatalf("captured generation did not complete: %+v", secondPass.NextCursor)
	}
	if got := harness.loadReservation(t, firstGood.reservationID).Status; got != model.CreditReservationStatusFinalized {
		t.Fatalf("first generation tail status = %q", got)
	}

	harness.makePending(t, "continuous_growth_two", now.Add(-time.Minute), true)
	thirdPass, err := harness.authority.ReconcileDuePendingPass(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	assertPendingReconcilePassCounts(t, thirdPass, 1, 1, 0, 0, 1)
	if thirdPass.FailureDetails == nil || len(thirdPass.FailureDetails.Items) != 1 ||
		thirdPass.FailureDetails.Items[0].TurnID != poison.turn.ID {
		t.Fatalf("old poison was starved by continuous growth: %+v", thirdPass)
	}
}

func TestPendingReconcileConcurrentPassesShareCursorSafely(t *testing.T) {
	harness := newPendingReconcileHarness(t, "concurrent-cursor")
	items := make([]pendingReconcileItem, 4)
	for index := range items {
		items[index] = harness.makePending(
			t, "concurrent_cursor_"+string(rune('a'+index)),
			time.Now().UTC().Add(-time.Minute), true,
		)
	}

	start := make(chan struct{})
	results := make(chan PendingReconcilePassResult, len(items))
	errorsByPass := make(chan error, len(items))
	var workers sync.WaitGroup
	for range items {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			result, err := harness.authority.ReconcileDuePendingPass(context.Background(), 1)
			results <- result
			errorsByPass <- err
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	close(errorsByPass)

	for err := range errorsByPass {
		if err != nil {
			t.Fatalf("concurrent pass: %v", err)
		}
	}
	converged := 0
	for result := range results {
		assertPendingReconcilePassCounts(t, result, 1, 1, 1, 0, 0)
		converged += result.Converged
	}
	if converged != len(items) {
		t.Fatalf("concurrent converged = %d, want %d", converged, len(items))
	}
	for _, item := range items {
		if got := harness.loadReservation(t, item.reservationID).Status; got != model.CreditReservationStatusFinalized {
			t.Fatalf("concurrent reservation %d status = %q", item.reservationID, got)
		}
	}
}

func TestPendingReconcileDiscoveryReportsOwnerTupleDrift(t *testing.T) {
	harness := newPendingReconcileHarness(t, "tuple-drift")
	item := harness.makePending(t, "tuple_drift_due", time.Now().UTC().Add(-time.Minute), true)
	before := harness.loadReservation(t, item.reservationID)

	if err := harness.db.Exec("PRAGMA foreign_keys = OFF").Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Table(BindingTable).Where("turn_id = ?", string(item.turn.ID)).
		Update("reservation_tool", "drifted-tool").Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
		t.Fatal(err)
	}

	candidates, err := harness.authority.DiscoverDuePending(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].OutcomeRowID == 0 || candidates[0].TurnID != item.turn.ID ||
		candidates[0].ReservationID != uint64(item.reservationID) ||
		candidates[0].FailureCode != PendingReconcileFailureOwnerTupleDrift {
		t.Fatalf("drift candidate = %+v", candidates)
	}

	result, err := harness.authority.ReconcileDuePendingPass(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	assertPendingReconcilePassCounts(t, result, 1, 1, 0, 0, 1)
	if result.FailureDetails == nil || len(result.FailureDetails.Items) != 1 ||
		result.FailureDetails.Items[0].Code != PendingReconcileFailureOwnerTupleDrift ||
		result.FailureDetails.Items[0].TurnID != item.turn.ID {
		t.Fatalf("drift failure details = %+v", result.FailureDetails)
	}
	after := harness.loadReservation(t, item.reservationID)
	if after.StateVersion != before.StateVersion || after.RefundAttempts != before.RefundAttempts ||
		after.Status != model.CreditReservationStatusRefundPending {
		t.Fatalf("drift candidate entered Reservation path: before=%+v after=%+v", before, after)
	}
}

func TestPendingReconcileFailureDetailsAreBounded(t *testing.T) {
	var result PendingReconcilePassResult
	for index := 0; index < MaxPendingReconcileFailureDetails+3; index++ {
		result.addFailure(PendingReconcileCandidate{
			OutcomeRowID: uint64(index + 1), TurnID: agentv1.TurnID("turn_failure_bound"),
			ReservationID: uint64(index + 1), SettlementKey: "wm:turn-settlement:v1:failure-bound",
		}, PendingReconcileFailureReconcileFailed)
	}
	if result.Failed != MaxPendingReconcileFailureDetails+3 || result.FailureDetails == nil ||
		len(result.FailureDetails.Items) != MaxPendingReconcileFailureDetails || result.FailureDetails.Omitted != 3 {
		t.Fatalf("bounded details = %+v", result)
	}
}

func TestPendingReconcilePassIsolatesFailureAndGenericSweeperSkipsBound(t *testing.T) {
	harness := newPendingReconcileHarness(t, "failure")
	now := time.Now().UTC()
	failing := harness.makePending(t, "failure_first", now.Add(-5*time.Minute), true)
	succeeding := harness.makePending(t, "failure_second", now.Add(-3*time.Minute), true)
	failingBefore := harness.loadReservation(t, failing.reservationID)
	succeedingBefore := harness.loadReservation(t, succeeding.reservationID)

	swept, failed, err := harness.reservations.SweepExpiredReservations(harness.db, 20, 0)
	if err != nil || swept != 0 || failed != 0 {
		t.Fatalf("generic sweep = swept:%d failed:%d err:%v", swept, failed, err)
	}
	failingAfterSweep := harness.loadReservation(t, failing.reservationID)
	succeedingAfterSweep := harness.loadReservation(t, succeeding.reservationID)
	if failingAfterSweep.StateVersion != failingBefore.StateVersion ||
		failingAfterSweep.RefundAttempts != failingBefore.RefundAttempts ||
		succeedingAfterSweep.StateVersion != succeedingBefore.StateVersion ||
		succeedingAfterSweep.RefundAttempts != succeedingBefore.RefundAttempts {
		t.Fatalf("generic sweep touched bound rows: before=%+v/%+v after=%+v/%+v",
			failingBefore, succeedingBefore, failingAfterSweep, succeedingAfterSweep)
	}

	if err := harness.db.Exec(`CREATE TRIGGER fail_one_pending_outcome
		BEFORE UPDATE ON w_agent_turn_settlement_outcome
		WHEN OLD.turn_id = 'turn_billing_batch_failure_first'
		BEGIN SELECT RAISE(FAIL, 'forced one-row reconcile failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	result, err := harness.authority.ReconcileDuePendingPass(context.Background(), 20)
	if err != nil {
		t.Fatalf("batch-level error = %v", err)
	}
	assertPendingReconcilePassCounts(t, result, 2, 2, 1, 0, 1)
	if result.FailureDetails == nil || len(result.FailureDetails.Items) != 1 ||
		result.FailureDetails.Items[0].TurnID != failing.turn.ID ||
		result.FailureDetails.Items[0].ReservationID != uint64(failing.reservationID) ||
		result.FailureDetails.Items[0].Code != PendingReconcileFailureReconcileFailed {
		t.Fatalf("stable failure details = %+v", result.FailureDetails)
	}
	if got := harness.loadReservation(t, failing.reservationID).Status; got != model.CreditReservationStatusRefundPending {
		t.Fatalf("failed row status = %q", got)
	}
	if got := harness.loadReservation(t, succeeding.reservationID).Status; got != model.CreditReservationStatusFinalized {
		t.Fatalf("later row status = %q", got)
	}
}

func TestPendingReconcilePassCountsDurableRetryAsStillPending(t *testing.T) {
	harness := newPendingReconcileHarness(t, "still-pending")
	item := harness.makePending(t, "still_pending_due", time.Now().UTC().Add(-time.Minute), false)
	before := harness.loadReservation(t, item.reservationID)

	result, err := harness.authority.ReconcileDuePendingPass(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	assertPendingReconcilePassCounts(t, result, 1, 1, 0, 1, 0)
	after := harness.loadReservation(t, item.reservationID)
	if after.Status != model.CreditReservationStatusRefundPending ||
		after.RefundAttempts != before.RefundAttempts+1 || after.StateVersion != before.StateVersion+1 ||
		after.NextRefundAt == nil || before.NextRefundAt == nil || !after.NextRefundAt.After(*before.NextRefundAt) {
		t.Fatalf("durable retry = before:%+v after:%+v", before, after)
	}
	if candidates, err := harness.authority.DiscoverDuePending(context.Background(), 10); err != nil || len(candidates) != 0 {
		t.Fatalf("backoff candidates = %+v, %v", candidates, err)
	}
}

func TestPendingReconcilePassHonorsCanceledContext(t *testing.T) {
	harness := newPendingReconcileHarness(t, "canceled")
	item := harness.makePending(t, "canceled_due", time.Now().UTC().Add(-time.Minute), true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := harness.authority.DiscoverDuePending(ctx, 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled discovery error = %v", err)
	}
	result, err := harness.authority.ReconcileDuePendingPass(ctx, 10)
	if !errors.Is(err, context.Canceled) || result.Discovered != 0 || result.Attempted != 0 ||
		result.Converged != 0 || result.StillPending != 0 || result.Failed != 0 ||
		result.NextCursor != (PendingReconcileCursor{}) || result.FailureDetails != nil {
		t.Fatalf("canceled pass = %+v, %v", result, err)
	}
	if got := harness.loadReservation(t, item.reservationID).Status; got != model.CreditReservationStatusRefundPending {
		t.Fatalf("canceled row status = %q", got)
	}
}

func TestPendingReconcileCancellationDuringOwnerAttemptDoesNotConsumeCandidate(t *testing.T) {
	harness := newPersistentPendingReconcileHarness(t, "cancel-during-owner")
	item := harness.makePending(t, "cancel_during_owner_due", time.Now().UTC().Add(-time.Minute), true)
	ctx, cancel := context.WithCancel(context.Background())
	callbackName := "agentbilling:cancel-pending-owner-attempt"
	var cancelOnce sync.Once
	if err := harness.db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx != nil && tx.Statement != nil && tx.Statement.Table == agentturn.SQLTurnTable {
			cancelOnce.Do(cancel)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = harness.db.Callback().Query().Remove(callbackName) })

	result, err := harness.authority.ReconcileDuePendingPass(ctx, 1)
	if !errors.Is(err, context.Canceled) || result.Discovered != 1 || result.Attempted != 0 ||
		result.Converged != 0 || result.StillPending != 0 || result.Failed != 0 ||
		result.NextCursor.OutcomeRowID != 0 || result.NextCursor.CycleHighWatermark == 0 ||
		result.FailureDetails != nil {
		t.Fatalf("mid-attempt cancellation consumed candidate: %+v, %v", result, err)
	}
	if got := harness.loadReservation(t, item.reservationID).Status; got != model.CreditReservationStatusRefundPending {
		t.Fatalf("canceled owner attempt status = %q", got)
	}

	if err := harness.db.Callback().Query().Remove(callbackName); err != nil {
		t.Fatal(err)
	}
	retried, err := harness.authority.ReconcileDuePendingPass(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	assertPendingReconcilePassCounts(t, retried, 1, 1, 1, 0, 0)
	if got := harness.loadReservation(t, item.reservationID).Status; got != model.CreditReservationStatusFinalized {
		t.Fatalf("unconsumed candidate was not retried, status = %q", got)
	}
}
