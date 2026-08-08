package agentbilling

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	agentv1 "server/contracts/agent/v1"
	"server/model"
	"server/service/account"
	"server/service/agentturn"
	"server/utils/testutil"
)

type expiredReservationHarness struct {
	db           *gorm.DB
	store        *agentturn.SQLStore
	authority    *CreditSettlementAuthority
	reservations *account.CreditReservationService
	reconciler   *ExpiredReservationReconciler
	userID       int
	packID       uint
}

type expiredReservationItem struct {
	turn          agentturn.Turn
	reservationID uint
	bindingRowID  uint64
}

func newExpiredReservationHarness(t *testing.T) *expiredReservationHarness {
	t.Helper()
	db := testutil.NewTestDB(t)
	user := model.User{Member: 0, Nickname: "expired-reconcile", Email: "expired-reconcile@example.com"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	pack := model.CreditsPack{
		UID: int(user.Id), SourceType: model.CreditsSourcePurchase,
		SourceID: "agentbilling-expired-reconcile", CreditsTotal: 1000,
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
	settlementBinding, err := store.BindSettlementReviewAuthority(authority)
	if err != nil {
		t.Fatal(err)
	}
	reconciler, err := NewExpiredReservationReconciler(authority, store, ExpiredReservationReconcilerOptions{
		ReconcilerID: "expired_reservation_test", ReconcilerBuildDigest: "sha256:expired-reservation-test",
		SettlementBinding: settlementBinding,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &expiredReservationHarness{
		db: db, store: store, authority: authority, reservations: reservations,
		reconciler: reconciler, userID: int(user.Id), packID: pack.Id,
	}
}

func (h *expiredReservationHarness) admit(t *testing.T, suffix string) expiredReservationItem {
	return h.admitOnStore(t, h.store, suffix)
}

func (h *expiredReservationHarness) admitOnStore(
	t *testing.T,
	store *agentturn.SQLStore,
	suffix string,
) expiredReservationItem {
	t.Helper()
	if store == nil {
		t.Fatal("admission Store is nil")
	}
	turn := billingTestTurn("expired_" + suffix)
	admission, err := h.authority.Admission(ReservationAdmission{
		PrincipalID: turn.PrincipalID,
		Reservation: account.ReservationRequest{
			UID: h.userID, Tool: "workagent", IdempotencyKey: "expired-reservation-" + suffix,
			Reserved: 10, TTL: time.Hour, Remark: "expired reservation reconcile test",
		},
		PricingSnapshotDigest: digest("agentbilling-expired-pricing-v1", suffix),
	})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := store.AdmitWithReservationAuthority(
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
	return expiredReservationItem{
		turn: admitted.Turn, reservationID: uint(binding.ReservationID), bindingRowID: binding.ID,
	}
}

func (h *expiredReservationHarness) expireReservation(t *testing.T, item expiredReservationItem) {
	t.Helper()
	expires := time.Now().UTC().Add(-2 * time.Minute)
	if err := h.db.Model(&model.CreditReservation{}).Where("id = ?", item.reservationID).
		Update("expires_at", expires).Error; err != nil {
		t.Fatalf("expire reservation %d: %v", item.reservationID, err)
	}
}

func (h *expiredReservationHarness) expireAttemptLease(
	t *testing.T,
	attempt agentturn.TurnAttempt,
) {
	t.Helper()
	now := time.Now().UTC()
	claimedAt := now.Add(-3 * time.Minute)
	heartbeatAt := now.Add(-2 * time.Minute)
	leaseExpiresAt := now.Add(-time.Minute)
	if err := h.db.Table(agentturn.SQLTurnAttemptTable).Where("attempt_id = ?", attempt.ID).Updates(map[string]any{
		"created_at": claimedAt, "claimed_at": claimedAt, "last_heartbeat_at": heartbeatAt,
		"lease_expires_at": leaseExpiresAt, "updated_at": heartbeatAt,
	}).Error; err != nil {
		t.Fatalf("expire Attempt %s lease: %v", attempt.ID, err)
	}
}

func (h *expiredReservationHarness) reservation(t *testing.T, id uint) model.CreditReservation {
	t.Helper()
	var reservation model.CreditReservation
	if err := h.db.First(&reservation, id).Error; err != nil {
		t.Fatal(err)
	}
	return reservation
}

func (h *expiredReservationHarness) turnState(t *testing.T, turnID agentv1.TurnID) struct {
	Status          string  `gorm:"column:status"`
	ActiveAttemptID *string `gorm:"column:active_attempt_id"`
	FencingToken    int64   `gorm:"column:fencing_token"`
} {
	t.Helper()
	var state struct {
		Status          string  `gorm:"column:status"`
		ActiveAttemptID *string `gorm:"column:active_attempt_id"`
		FencingToken    int64   `gorm:"column:fencing_token"`
	}
	if err := h.db.Table(agentturn.SQLTurnTable).Where("turn_id = ?", string(turnID)).Take(&state).Error; err != nil {
		t.Fatal(err)
	}
	return state
}

func TestExpiredReservationQueuedReleaseAndGenericSweeperExclusion(t *testing.T) {
	h := newExpiredReservationHarness(t)
	item := h.admit(t, "queued_release")
	h.expireReservation(t, item)

	before := h.reservation(t, item.reservationID)
	swept, failed, err := h.reservations.SweepExpiredReservations(h.db, 20, 0)
	if err != nil || swept != 0 || failed != 0 {
		t.Fatalf("generic sweep = %d/%d, %v, want bound row excluded", swept, failed, err)
	}
	afterSweep := h.reservation(t, item.reservationID)
	if afterSweep.Status != model.CreditReservationStatusReserved || afterSweep.StateVersion != before.StateVersion {
		t.Fatalf("generic sweeper touched bound Reservation: before=%+v after=%+v", before, afterSweep)
	}

	result, err := h.reconciler.ReconcileExpiredReservationPass(context.Background(), 10)
	if err != nil || result.Discovered != 1 || result.Attempted != 1 || len(result.Retired) != 1 ||
		result.Deferred != 0 || result.Failed != 0 {
		t.Fatalf("expiry pass = %+v, %v", result, err)
	}
	if result.Retired[0].TerminalStatus != agentv1.TurnStatusTimeout || result.Retired[0].FencedAttemptID != "" {
		t.Fatalf("queued retirement = %+v", result.Retired[0])
	}
	reservation := h.reservation(t, item.reservationID)
	if reservation.Status != model.CreditReservationStatusReleased || reservation.Used != 0 || reservation.ReleasedAt == nil {
		t.Fatalf("released Reservation = %+v", reservation)
	}
	state := h.turnState(t, item.turn.ID)
	if state.Status != string(agentv1.TurnStatusTimeout) || state.ActiveAttemptID != nil || state.FencingToken != 1 {
		t.Fatalf("queued expired Turn = %+v", state)
	}
	outcome, err := h.authority.GetOutcome(context.Background(), item.turn.PrincipalID, item.turn.ID)
	if err != nil || outcome.Status != OutcomeStatusReleased ||
		outcome.AuthorizationKind != AuthorizationKindReconcile || outcome.TerminalStatus != agentv1.TurnStatusTimeout {
		t.Fatalf("released Outcome = %+v, %v", outcome, err)
	}
}

func TestExpiredReservationLiveAttemptIsNeverTerminalized(t *testing.T) {
	h := newExpiredReservationHarness(t)
	item := h.admit(t, "live_attempt")
	claimed, err := h.store.ClaimAttempt(context.Background(), agentturn.ClaimAttemptCommand{
		TurnID: item.turn.ID, AttemptID: "attempt_expired_reservation_live",
		WorkerID: "worker_expired_reservation", WorkerBuildDigest: "sha256:worker-expired-reservation",
	})
	if err != nil {
		t.Fatal(err)
	}
	h.expireReservation(t, item)

	if _, err := h.store.ReconcileTerminal(context.Background(), agentturn.ReconcileCommand{
		TurnID: item.turn.ID, Reason: agentturn.ReclaimReasonReservationExpired,
		ReconcilerID: "expired_reservation_test", ReconcilerBuildDigest: "sha256:expired-reservation-test",
	}); !errors.Is(err, agentturn.ErrReconcilePrecondition) {
		t.Fatalf("live Attempt reconcile error = %v, want ErrReconcilePrecondition", err)
	}
	result, err := h.reconciler.ReconcileExpiredReservationPass(context.Background(), 10)
	if err != nil || result.Discovered != 1 || result.Attempted != 0 || result.Deferred != 1 ||
		len(result.Retired) != 0 || result.Failed != 0 || result.Details == nil ||
		result.Details.Items[0].Code != ExpiredReservationFailureLiveAttempt {
		t.Fatalf("live Attempt expiry pass = %+v, %v", result, err)
	}
	state := h.turnState(t, item.turn.ID)
	if state.Status != string(agentv1.TurnStatusRunning) || state.ActiveAttemptID == nil ||
		*state.ActiveAttemptID != claimed.Attempt.ID || state.FencingToken != int64(claimed.Attempt.FencingToken) {
		t.Fatalf("live Turn changed: %+v", state)
	}
	if reservation := h.reservation(t, item.reservationID); reservation.Status != model.CreditReservationStatusReserved {
		t.Fatalf("live Reservation status = %q, want reserved", reservation.Status)
	}
}

func TestExpiredReservationRunningLeaseExpiredIsRecoveredAtomically(t *testing.T) {
	h := newExpiredReservationHarness(t)
	item := h.admit(t, "running_dead")
	claimed, err := h.store.ClaimAttempt(context.Background(), agentturn.ClaimAttemptCommand{
		TurnID: item.turn.ID, AttemptID: "attempt_expired_reservation_dead",
		WorkerID: "worker_expired_reservation", WorkerBuildDigest: "sha256:worker-expired-reservation",
	})
	if err != nil {
		t.Fatal(err)
	}
	h.expireReservation(t, item)
	h.expireAttemptLease(t, claimed.Attempt)

	result, err := h.reconciler.ReconcileExpiredReservationPass(context.Background(), 10)
	if err != nil || result.Attempted != 1 || len(result.Retired) != 1 || result.Failed != 0 {
		t.Fatalf("dead running expiry pass = %+v, %v", result, err)
	}
	retired := result.Retired[0]
	if retired.TerminalStatus != agentv1.TurnStatusTimeout || retired.FencedAttemptID != claimed.Attempt.ID {
		t.Fatalf("dead running retirement = %+v", retired)
	}
	var attempt struct {
		Status     string     `gorm:"column:status"`
		FinishedAt *time.Time `gorm:"column:finished_at"`
	}
	if err := h.db.Table(agentturn.SQLTurnAttemptTable).Where("attempt_id = ?", claimed.Attempt.ID).
		Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.Status != string(agentturn.AttemptStatusExpired) || attempt.FinishedAt == nil {
		t.Fatalf("fenced Attempt = %+v", attempt)
	}
	if reservation := h.reservation(t, item.reservationID); reservation.Status != model.CreditReservationStatusReleased {
		t.Fatalf("dead running Reservation status = %q, want released", reservation.Status)
	}
}

func TestExpiredReservationCursorMovesPastTupleDriftPoison(t *testing.T) {
	h := newExpiredReservationHarness(t)
	poison := h.admit(t, "poison_first")
	healthy := h.admit(t, "healthy_second")
	h.expireReservation(t, poison)
	h.expireReservation(t, healthy)
	if err := h.db.Table(BindingTable).Where("id = ?", poison.bindingRowID).
		Update("binding_digest", digest("agentbilling-expired-corrupt-binding", "poison")).Error; err != nil {
		t.Fatal(err)
	}

	first, err := h.reconciler.ReconcileExpiredReservationPass(context.Background(), 1)
	if err != nil || first.Discovered != 1 || first.Failed != 1 || first.Attempted != 0 ||
		first.Details == nil || len(first.Details.Items) != 1 ||
		first.Details.Items[0].BindingRowID != poison.bindingRowID ||
		first.Details.Items[0].Code != ExpiredReservationFailureOwnerTupleDrift ||
		first.NextCursor.BindingRowID != poison.bindingRowID || first.NextCursor.CycleHighWatermark < healthy.bindingRowID {
		t.Fatalf("poison pass = %+v, %v", first, err)
	}
	second, err := h.reconciler.ReconcileExpiredReservationPass(context.Background(), 1)
	if err != nil || second.Attempted != 1 || len(second.Retired) != 1 ||
		second.Retired[0].Turn.ID != healthy.turn.ID || second.Failed != 0 {
		t.Fatalf("post-poison pass = %+v, %v", second, err)
	}
	if got := h.reservation(t, poison.reservationID).Status; got != model.CreditReservationStatusReserved {
		t.Fatalf("poison Reservation status = %q, want untouched reserved", got)
	}
	if got := h.reservation(t, healthy.reservationID).Status; got != model.CreditReservationStatusReleased {
		t.Fatalf("healthy Reservation status = %q, want released", got)
	}
}

func TestCreditAuthorityExactExpiryLetsClaimNextReachHealthyTurn(t *testing.T) {
	h := newExpiredReservationHarness(t)
	expired := h.admit(t, "claim_expired_first")
	healthy := h.admit(t, "claim_healthy_second")
	h.expireReservation(t, expired)

	claimed, err := h.store.ClaimNext(context.Background(), agentturn.ClaimNextCommand{
		AttemptID: "attempt_claim_after_exact_expiry", WorkerID: "worker_expired_reservation",
		WorkerBuildDigest: "sha256:worker-expired-reservation", ScanLimit: 2,
	})
	if err != nil || claimed.Turn.ID != healthy.turn.ID {
		t.Fatalf("ClaimNext() = %+v, %v, want healthy Turn", claimed, err)
	}
	state := h.turnState(t, expired.turn.ID)
	if state.Status != string(agentv1.TurnStatusQueued) || state.ActiveAttemptID != nil || state.FencingToken != 0 {
		t.Fatalf("exact expired candidate mutated: %+v", state)
	}
}

func TestExpiredReservationReconcilerRejectsStoreAuthorityMismatch(t *testing.T) {
	h := newExpiredReservationHarness(t)
	otherAuthority, err := NewCreditSettlementAuthority(h.db, account.NewCreditReservationService())
	if err != nil {
		t.Fatal(err)
	}
	otherStore, err := agentturn.NewSQLStore(h.db)
	if err != nil {
		t.Fatal(err)
	}
	otherBinding, err := otherStore.BindSettlementReviewAuthority(otherAuthority)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewExpiredReservationReconciler(h.authority, otherStore, ExpiredReservationReconcilerOptions{
		ReconcilerID:          "expired_reservation_mismatch",
		ReconcilerBuildDigest: "sha256:expired-reservation-mismatch",
		SettlementBinding:     otherBinding,
	}); !errors.Is(err, ErrLedgerUnavailable) {
		t.Fatalf("mismatched Store constructor error = %v, want ErrLedgerUnavailable", err)
	}
	foreignDBHarness := newExpiredReservationHarness(t)
	crossStore, err := agentturn.NewSQLStore(foreignDBHarness.db)
	if err != nil {
		t.Fatal(err)
	}
	crossBinding, err := crossStore.BindSettlementReviewAuthority(h.authority)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewExpiredReservationReconciler(h.authority, crossStore, ExpiredReservationReconcilerOptions{
		ReconcilerID:          "expired_reservation_database_mismatch",
		ReconcilerBuildDigest: "sha256:expired-reservation-database-mismatch",
		SettlementBinding:     crossBinding,
	}); !errors.Is(err, ErrLedgerUnavailable) {
		t.Fatalf("different-database Store constructor error = %v, want ErrLedgerUnavailable", err)
	}

	// Even the same Store must reject a caller-supplied wrapper over a different
	// Credits ledger; matching only the interface shape is not an identity proof.
	foreignComposite, err := NewProviderUsageCreditAuthority(otherAuthority, &testProviderUsageMeter{used: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewExpiredReservationReconciler(h.authority, h.store, ExpiredReservationReconcilerOptions{
		ReconcilerID:          "expired_reservation_foreign_wrapper",
		ReconcilerBuildDigest: "sha256:expired-reservation-foreign-wrapper",
		SettlementBinding:     h.reconciler.binding,
		ExpiryAuthority:       foreignComposite,
	}); !errors.Is(err, ErrLedgerUnavailable) {
		t.Fatalf("foreign wrapper constructor error = %v, want ErrLedgerUnavailable", err)
	}
}

func TestExpiredReservationReconcilerAcceptsSameLedgerProviderAuthority(t *testing.T) {
	h := newExpiredReservationHarness(t)
	providerStore, err := agentturn.NewSQLStore(h.db)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := agentturn.NewProviderUsageJournal(providerStore)
	if err != nil {
		t.Fatal(err)
	}
	meter := &testProviderUsageMeter{used: 1}
	composite, err := NewProviderUsageCreditAuthority(h.authority, meter)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := providerStore.BindSettlementReviewProviderUsageAuthority(journal, composite)
	if err != nil {
		t.Fatal(err)
	}
	reconciler, err := NewExpiredReservationReconciler(
		h.authority, providerStore, ExpiredReservationReconcilerOptions{
			ReconcilerID:          "expired_reservation_provider_test",
			ReconcilerBuildDigest: "sha256:expired-reservation-provider-test",
			SettlementBinding:     binding,
			ExpiryAuthority:       composite,
		},
	)
	if err != nil {
		t.Fatalf("same-ledger Provider constructor: %v", err)
	}
	item := h.admitOnStore(t, providerStore, "same_ledger_provider")
	h.expireReservation(t, item)

	result, err := reconciler.ReconcileExpiredReservationPass(context.Background(), 10)
	if err != nil || result.Discovered != 1 || result.Attempted != 1 || len(result.Retired) != 1 ||
		result.Failed != 0 || result.Retired[0].TerminalStatus != agentv1.TurnStatusTimeout ||
		result.Retired[0].SettlementReview == nil {
		t.Fatalf("same-ledger Provider expiry pass = %+v, %v", result, err)
	}
	reservation := h.reservation(t, item.reservationID)
	if reservation.Status != model.CreditReservationStatusReviewHold || reservation.ReviewHeldAt == nil {
		t.Fatalf("Provider expiry Reservation = %+v", reservation)
	}
	outcome, err := h.authority.GetOutcome(context.Background(), item.turn.PrincipalID, item.turn.ID)
	if err != nil || outcome.Status != OutcomeStatusReviewHeld ||
		outcome.RequestedIntent != RequestedIntentReview || outcome.TerminalStatus != agentv1.TurnStatusTimeout {
		t.Fatalf("Provider expiry Outcome = %+v, %v", outcome, err)
	}
	if meter.callCount() != 0 {
		t.Fatalf("Provider meter calls during expiry = %d, want 0", meter.callCount())
	}
}

func TestExpiredReservationReconcilerFailsClosedAfterSealedBindingViolation(t *testing.T) {
	h := newExpiredReservationHarness(t)
	item := h.admit(t, "sealed_violation")
	h.expireReservation(t, item)
	replacement, err := NewCreditSettlementAuthority(h.db, account.NewCreditReservationService())
	if err != nil {
		t.Fatal(err)
	}
	// A post-bind compatibility call cannot replace the sealed authority; it
	// permanently invalidates the opaque binding and every later pass.
	h.store.WithSettlementAuthority(replacement)
	if _, err := h.store.ReconcileTerminal(context.Background(), agentturn.ReconcileCommand{
		TurnID: item.turn.ID, Reason: agentturn.ReclaimReasonReservationExpired,
		ReconcilerID: "expired_reservation_test", ReconcilerBuildDigest: "sha256:expired-reservation-test",
	}); !errors.Is(err, agentturn.ErrSettlementBindingInvalid) {
		t.Fatalf("post-bind violation ReconcileTerminal error = %v, want ErrSettlementBindingInvalid", err)
	}
	if _, _, _, err := h.reconciler.DiscoverExpiredReservations(
		context.Background(), ExpiredReservationCursor{}, 10,
	); !errors.Is(err, ErrLedgerUnavailable) {
		t.Fatalf("post-bind violation discovery error = %v, want ErrLedgerUnavailable", err)
	}
	state := h.turnState(t, item.turn.ID)
	if state.Status != string(agentv1.TurnStatusQueued) || state.ActiveAttemptID != nil {
		t.Fatalf("binding violation mutated Turn: %+v", state)
	}
	if got := h.reservation(t, item.reservationID).Status; got != model.CreditReservationStatusReserved {
		t.Fatalf("binding violation Reservation status = %q, want reserved", got)
	}
}

func TestExpiredReservationReconcilerRejectsMalformedCursor(t *testing.T) {
	h := newExpiredReservationHarness(t)
	item := h.admit(t, "cursor_guard")
	h.expireReservation(t, item)
	for name, cursor := range map[string]ExpiredReservationCursor{
		"position without generation": {BindingRowID: item.bindingRowID},
		"position beyond high": {
			BindingRowID: item.bindingRowID + 1, CycleHighWatermark: item.bindingRowID,
		},
		"forged high": {
			BindingRowID: item.bindingRowID, CycleHighWatermark: item.bindingRowID + 100,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := h.reconciler.DiscoverExpiredReservations(
				context.Background(), cursor, 10,
			); !errors.Is(err, ErrExpiredReservationCursor) {
				t.Fatalf("DiscoverExpiredReservations(%+v) error = %v, want ErrExpiredReservationCursor", cursor, err)
			}
		})
	}
}
