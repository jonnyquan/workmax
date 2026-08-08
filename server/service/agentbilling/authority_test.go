package agentbilling

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"gorm.io/gorm"

	agentv1 "server/contracts/agent/v1"
	"server/model"
	"server/service/account"
	"server/service/agentturn"
	"server/utils/testutil"
)

type billingFixture struct {
	db            *gorm.DB
	store         *agentturn.SQLStore
	authority     *CreditSettlementAuthority
	turn          agentturn.Turn
	attempt       agentturn.TurnAttempt
	reservationID uint
	packID        uint
}

func newBillingFixture(t *testing.T, suffix string, reserved int) billingFixture {
	t.Helper()
	db := testutil.NewTestDB(t)
	user := model.User{Member: 0, Nickname: "billing-test", Email: suffix + "@example.com"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	pack := model.CreditsPack{
		UID: int(user.Id), SourceType: model.CreditsSourcePurchase,
		SourceID: "agentbilling-" + suffix, CreditsTotal: 100,
	}
	if err := db.Create(&pack).Error; err != nil {
		t.Fatalf("seed pack: %v", err)
	}
	reservationService := account.NewCreditReservationService()
	authority, err := NewCreditSettlementAuthority(db, reservationService)
	if err != nil {
		t.Fatal(err)
	}
	store, err := agentturn.NewSQLStore(db)
	if err != nil {
		t.Fatal(err)
	}
	turn := billingTestTurn(suffix)
	admission, err := authority.Admission(ReservationAdmission{
		PrincipalID: turn.PrincipalID,
		Reservation: account.ReservationRequest{
			UID: int(user.Id), Tool: "workagent", IdempotencyKey: "reservation-" + suffix,
			Reserved: reserved, TTL: time.Hour, Remark: "agent billing test",
		},
		PricingSnapshotDigest: digest("agentbilling-test-pricing-v1", suffix),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.AdmitWithReservationAuthority(
		context.Background(), turn,
		agentturn.EventDraft{Type: agentv1.EventCoreTurnStatus, Data: json.RawMessage(`{"status":"queued"}`)},
		admission,
	)
	if err != nil || !result.Created {
		t.Fatalf("bound admission = %+v, %v", result, err)
	}
	var binding bindingRow
	if err := db.Table(BindingTable).Where("turn_id = ?", string(turn.ID)).Take(&binding).Error; err != nil {
		t.Fatalf("load binding: %v", err)
	}
	store.WithSettlementAuthority(authority)
	claimed, err := store.ClaimAttempt(context.Background(), agentturn.ClaimAttemptCommand{
		TurnID: turn.ID, AttemptID: "attempt_" + suffix, WorkerID: "worker_agentbilling",
		WorkerBuildDigest: "sha256:worker-agentbilling",
	})
	if err != nil {
		t.Fatalf("claim bound turn: %v", err)
	}
	return billingFixture{
		db: db, store: store, authority: authority, turn: result.Turn, attempt: claimed.Attempt,
		reservationID: uint(binding.ReservationID), packID: pack.Id,
	}
}

func billingTestTurn(suffix string) agentturn.Turn {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	return agentturn.Turn{
		ID:             agentv1.TurnID("turn_billing_" + suffix),
		PrincipalID:    agentturn.PrincipalID("principal_billing_" + suffix),
		ThreadID:       agentv1.ThreadID("thread_billing_" + suffix),
		IdempotencyKey: agentv1.IdempotencyKey("idem_billing_" + suffix),
		CommandDigest:  "sha256:command-billing-" + suffix,
		Plugin: agentv1.EventPluginRef{
			ID: "workmax.writer", Version: "1.0.0", ReleaseDigest: "sha256:release-billing",
		},
		Status: agentv1.TurnStatusQueued, CreatedAt: now, UpdatedAt: now,
	}
}

func TestCreditSettlementAuthorityAdmissionReplayRequiresExactReservationDigest(t *testing.T) {
	db := testutil.NewTestDB(t)
	user := model.User{Member: 0, Nickname: "billing-admission", Email: "billing-admission@example.com"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	pack := model.CreditsPack{
		UID: int(user.Id), SourceType: model.CreditsSourcePurchase,
		SourceID: "agentbilling-admission", CreditsTotal: 100,
	}
	if err := db.Create(&pack).Error; err != nil {
		t.Fatalf("seed pack: %v", err)
	}
	authority, err := NewCreditSettlementAuthority(db, account.NewCreditReservationService())
	if err != nil {
		t.Fatal(err)
	}
	store, err := agentturn.NewSQLStore(db)
	if err != nil {
		t.Fatal(err)
	}
	turn := billingTestTurn("admission_digest")
	spec := ReservationAdmission{
		PrincipalID: turn.PrincipalID,
		Reservation: account.ReservationRequest{
			UID: int(user.Id), Tool: "workagent", IdempotencyKey: "reservation-admission-digest",
			QuoteID: "quote-admission-v1", Reserved: 10, TTL: time.Hour, Remark: "first admission",
		},
		PricingSnapshotDigest: digest("agentbilling-test-pricing-v1", "admission-digest"),
	}
	admission, err := authority.Admission(spec)
	if err != nil {
		t.Fatal(err)
	}
	initial := agentturn.EventDraft{
		Type: agentv1.EventCoreTurnStatus, Data: json.RawMessage(`{"status":"queued"}`),
	}
	first, err := store.AdmitWithReservationAuthority(context.Background(), turn, initial, admission)
	if err != nil || !first.Created || first.Turn.ID != turn.ID {
		t.Fatalf("first admission = %+v, %v", first, err)
	}

	retry := turn
	retry.ID = "turn_billing_admission_digest_retry"
	exactAdmission, err := authority.Admission(spec)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := store.AdmitWithReservationAuthority(context.Background(), retry, initial, exactAdmission)
	if err != nil || replayed.Created || replayed.Turn.ID != turn.ID {
		t.Fatalf("exact admission replay = %+v, %v", replayed, err)
	}

	conflicts := map[string]func(*ReservationAdmission){
		"idempotency key": func(value *ReservationAdmission) {
			value.Reservation.IdempotencyKey = "reservation-admission-other"
		},
		"quote": func(value *ReservationAdmission) {
			value.Reservation.QuoteID = "quote-admission-v2"
		},
	}
	for name, mutate := range conflicts {
		t.Run(name, func(t *testing.T) {
			conflict := spec
			mutate(&conflict)
			conflictingAdmission, err := authority.Admission(conflict)
			if err != nil {
				t.Fatalf("build conflicting admission: %v", err)
			}
			if _, err := store.AdmitWithReservationAuthority(
				context.Background(), retry, initial, conflictingAdmission,
			); !errors.Is(err, agentturn.ErrTurnReservationBindingInvalid) {
				t.Fatalf("conflicting replay error = %v, want ErrTurnReservationBindingInvalid", err)
			}
		})
	}

	expectedDigest, err := account.CanonicalReservationRequestDigest(spec.Reservation)
	if err != nil {
		t.Fatal(err)
	}
	var binding bindingRow
	if err := db.Table(BindingTable).Where("turn_id = ?", string(turn.ID)).Take(&binding).Error; err != nil {
		t.Fatal(err)
	}
	if binding.ReservationRequestDigest != expectedDigest {
		t.Fatalf("binding request digest = %q, want %q", binding.ReservationRequestDigest, expectedDigest)
	}
	checks := []struct {
		table     string
		predicate string
		args      []any
	}{
		{model.CreditReservation{}.TableName(), "uid = ?", []any{int(user.Id)}},
		{model.CreditReservationAllocation{}.TableName(), "reservation_id = ?", []any{binding.ReservationID}},
		{BindingTable, "turn_id = ?", []any{string(turn.ID)}},
		{agentturn.SQLTurnTable, "principal_id = ?", []any{string(turn.PrincipalID)}},
		{agentturn.SQLTurnEventTable, "turn_id = ?", []any{string(turn.ID)}},
	}
	for _, check := range checks {
		var count int64
		if err := db.Table(check.table).Where(check.predicate, check.args...).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("%s rows = %d, %v", check.table, count, err)
		}
	}
	if err := db.First(&pack, pack.Id).Error; err != nil {
		t.Fatal(err)
	}
	if pack.CreditsUsed != spec.Reservation.Reserved {
		t.Fatalf("pack used = %d, want one debit of %d", pack.CreditsUsed, spec.Reservation.Reserved)
	}
}

func TestCreditSettlementAuthorityFinalizesAndReplaysOneOutcome(t *testing.T) {
	fixture := newBillingFixture(t, "finalize", 10)
	command := agentturn.CommitAttemptCommand{
		Fence: fixture.attempt.Fence(), OperationID: "operation_finalize",
		TerminalStatus: agentv1.TurnStatusCompleted,
		Settlement: &agentturn.SettlementRequest{
			Intent: agentturn.SettlementIntentFinalize, UsedUnits: 4,
		},
	}
	first, err := fixture.store.CommitAttempt(context.Background(), command)
	if err != nil || first.Replay || first.TurnStatus != agentv1.TurnStatusCompleted {
		t.Fatalf("first terminal commit = %+v, %v", first, err)
	}
	outcome, err := fixture.authority.GetOutcome(context.Background(), fixture.turn.PrincipalID, fixture.turn.ID)
	if err != nil {
		t.Fatalf("GetOutcome: %v", err)
	}
	if outcome.Status != OutcomeStatusFinalized || outcome.RequestedIntent != RequestedIntentFinalize ||
		outcome.UsedUnits == nil || *outcome.UsedUnits != 4 ||
		outcome.AuthorizationKind != AuthorizationKindOperation || outcome.AttemptID == nil ||
		*outcome.AttemptID != fixture.attempt.ID || outcome.OperationID == nil ||
		*outcome.OperationID != command.OperationID || outcome.FencingToken != fixture.attempt.FencingToken {
		t.Fatalf("outcome = %+v", outcome)
	}
	assertBillingFinancialState(t, fixture, model.CreditReservationStatusFinalized, 4, 4)

	replay, err := fixture.store.CommitAttempt(context.Background(), command)
	if err != nil || !replay.Replay {
		t.Fatalf("terminal replay = %+v, %v", replay, err)
	}
	replayed, err := fixture.authority.GetOutcome(context.Background(), fixture.turn.PrincipalID, fixture.turn.ID)
	if err != nil || replayed.OutcomeDigest != outcome.OutcomeDigest ||
		replayed.ReservationStateVersion != outcome.ReservationStateVersion {
		t.Fatalf("replayed outcome = %+v, %v", replayed, err)
	}
	assertBillingFinancialState(t, fixture, model.CreditReservationStatusFinalized, 4, 4)
}

func TestCreditSettlementAuthorityNormalizesMissingTurnOwnership(t *testing.T) {
	fixture := newBillingFixture(t, "missing_turn_owner", 10)
	missingTurnID := agentv1.TurnID("turn_billing_missing_owner")
	for name, call := range map[string]func() error{
		"get": func() error {
			_, err := fixture.authority.GetOutcome(context.Background(), fixture.turn.PrincipalID, missingTurnID)
			return err
		},
		"reconcile": func() error {
			_, err := fixture.authority.ReconcilePending(context.Background(), fixture.turn.PrincipalID, missingTurnID)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, ErrBindingNotFound) {
				t.Fatalf("missing Turn ownership error = %v, want ErrBindingNotFound", err)
			}
		})
	}
}

func TestCreditSettlementAuthorityRejectsReservationTransitionTimeDrift(t *testing.T) {
	t.Run("terminal state_changed_at", func(t *testing.T) {
		fixture := newBillingFixture(t, "terminal_time_drift", 10)
		if _, err := fixture.store.CommitAttempt(context.Background(), agentturn.CommitAttemptCommand{
			Fence: fixture.attempt.Fence(), OperationID: "operation_terminal_time_drift",
			TerminalStatus: agentv1.TurnStatusCompleted,
			Settlement:     &agentturn.SettlementRequest{Intent: agentturn.SettlementIntentFinalize, UsedUnits: 4},
		}); err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.Table(model.CreditReservation{}.TableName()).
			Where("id = ?", fixture.reservationID).
			Update("state_changed_at", time.Now().UTC().Add(24*time.Hour).Truncate(time.Microsecond)).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.authority.GetOutcome(
			context.Background(), fixture.turn.PrincipalID, fixture.turn.ID,
		); !errors.Is(err, ErrReservationStateDrift) {
			t.Fatalf("terminal transition time drift error = %v", err)
		}
	})

	t.Run("review held_at", func(t *testing.T) {
		fixture := newBillingFixture(t, "review_time_drift", 10)
		if _, err := fixture.store.CommitAttempt(context.Background(), agentturn.CommitAttemptCommand{
			Fence: fixture.attempt.Fence(), OperationID: "operation_review_time_drift_effect",
			Event: &agentturn.EventDraft{Type: "writer.document.delta", Data: json.RawMessage(`{"partial":true}`)},
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.store.CommitAttempt(context.Background(), agentturn.CommitAttemptCommand{
			Fence: fixture.attempt.Fence(), OperationID: "operation_review_time_drift_terminal",
			TerminalStatus: agentv1.TurnStatusFailed,
			Settlement:     &agentturn.SettlementRequest{Intent: agentturn.SettlementIntentRelease},
		}); err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.Table(model.CreditReservation{}.TableName()).
			Where("id = ?", fixture.reservationID).
			Update("review_held_at", time.Now().UTC().Add(24*time.Hour).Truncate(time.Microsecond)).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.authority.GetOutcome(
			context.Background(), fixture.turn.PrincipalID, fixture.turn.ID,
		); !errors.Is(err, ErrReservationStateDrift) {
			t.Fatalf("Review transition time drift error = %v", err)
		}
	})
}

func TestCreditSettlementAuthorityFinalizesAttemptAuthorizedBeforeReservationExpiry(t *testing.T) {
	fixture := newBillingFixture(t, "finalize_after_ttl", 10)
	expiredAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	if err := fixture.db.Model(&model.CreditReservation{}).
		Where("id = ?", fixture.reservationID).
		Update("expires_at", expiredAt).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Transaction(func(tx *gorm.DB) error {
		return fixture.authority.reservations.Finalize(tx, fixture.reservationID, 4)
	}); !errors.Is(err, account.ErrReservationTTLExpired) {
		t.Fatalf("generic expired Finalize error = %v", err)
	}

	result, err := fixture.store.CommitAttempt(context.Background(), agentturn.CommitAttemptCommand{
		Fence: fixture.attempt.Fence(), OperationID: "operation_finalize_after_ttl",
		TerminalStatus: agentv1.TurnStatusCompleted,
		Settlement: &agentturn.SettlementRequest{
			Intent: agentturn.SettlementIntentFinalize, UsedUnits: 4,
		},
	})
	if err != nil || result.Replay || result.TurnStatus != agentv1.TurnStatusCompleted {
		t.Fatalf("authorized terminal settlement after TTL = %+v, %v", result, err)
	}
	outcome, err := fixture.authority.GetOutcome(
		context.Background(), fixture.turn.PrincipalID, fixture.turn.ID,
	)
	if err != nil || outcome.Status != OutcomeStatusFinalized || outcome.UsedUnits == nil || *outcome.UsedUnits != 4 {
		t.Fatalf("authorized outcome after TTL = %+v, %v", outcome, err)
	}
	assertBillingFinancialState(t, fixture, model.CreditReservationStatusFinalized, 4, 4)
}

func TestCreditSettlementAuthorityRefundPendingRecoversExactOutcome(t *testing.T) {
	tests := []struct {
		name              string
		suffix            string
		operationID       string
		terminalStatus    agentv1.TurnStatus
		settlement        agentturn.SettlementRequest
		requestedIntent   RequestedIntent
		refundTarget      string
		used              int64
		finalOutcome      OutcomeStatus
		reservationStatus string
	}{
		{
			name: "finalize", suffix: "refund_pending_finalize", operationID: "operation_refund_pending_finalize",
			terminalStatus: agentv1.TurnStatusCompleted,
			settlement: agentturn.SettlementRequest{
				Intent: agentturn.SettlementIntentFinalize, UsedUnits: 4,
			},
			requestedIntent: RequestedIntentFinalize, refundTarget: model.CreditReservationStatusFinalized,
			used: 4, finalOutcome: OutcomeStatusFinalized, reservationStatus: model.CreditReservationStatusFinalized,
		},
		{
			name: "release", suffix: "refund_pending_release", operationID: "operation_refund_pending_release",
			terminalStatus: agentv1.TurnStatusFailed,
			settlement: agentturn.SettlementRequest{
				Intent: agentturn.SettlementIntentRelease,
			},
			requestedIntent: RequestedIntentRelease, refundTarget: model.CreditReservationStatusReleased,
			used: 0, finalOutcome: OutcomeStatusReleased, reservationStatus: model.CreditReservationStatusReleased,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const reserved = 10
			fixture := newBillingFixture(t, test.suffix, reserved)
			before := loadBillingReservation(t, fixture)

			// Break the immutable allocation evidence without touching the Pack
			// debit. Credits must commit only a bounded refund intent, never a
			// false terminal result or a partial financial mutation.
			if err := fixture.db.Where("reservation_id = ?", fixture.reservationID).
				Delete(&model.CreditReservationAllocation{}).Error; err != nil {
				t.Fatalf("drop allocation evidence: %v", err)
			}
			command := agentturn.CommitAttemptCommand{
				Fence: fixture.attempt.Fence(), OperationID: test.operationID,
				TerminalStatus: test.terminalStatus, Settlement: &test.settlement,
			}
			committed, err := fixture.store.CommitAttempt(context.Background(), command)
			if err != nil || committed.Replay || committed.TurnStatus != test.terminalStatus {
				t.Fatalf("terminal refund-pending commit = %+v, %v", committed, err)
			}

			pending, err := fixture.authority.GetOutcome(
				context.Background(), fixture.turn.PrincipalID, fixture.turn.ID,
			)
			if err != nil {
				t.Fatalf("GetOutcome pending: %v", err)
			}
			reservation := loadBillingReservation(t, fixture)
			if pending.Status != OutcomeStatusRefundPending ||
				pending.RequestedIntent != test.requestedIntent ||
				pending.UsedUnits == nil || *pending.UsedUnits != test.used ||
				pending.RefundTarget == nil || *pending.RefundTarget != test.refundTarget ||
				pending.RefundDue != reserved-int64(test.used) ||
				pending.ReservationStateVersion != reservation.StateVersion {
				t.Fatalf("pending outcome = %+v, reservation version=%d", pending, reservation.StateVersion)
			}
			if pending.SettlementKey != billingSettlementKey(fixture.turn.ID, test.operationID) ||
				pending.AuthorizationKind != AuthorizationKindOperation || pending.AttemptID == nil ||
				*pending.AttemptID != fixture.attempt.ID || pending.OperationID == nil ||
				*pending.OperationID != test.operationID || pending.FencingToken != fixture.attempt.FencingToken {
				t.Fatalf("pending outcome authority tuple = %+v", pending)
			}
			if reservation.Status != model.CreditReservationStatusRefundPending || reservation.Used != 0 ||
				reservation.RefundTargetStatus != test.refundTarget || reservation.RefundTargetUsed == nil ||
				int64(*reservation.RefundTargetUsed) != test.used ||
				reservation.RefundDue != reserved-int(test.used) || reservation.RefundAttempts != 1 ||
				reservation.LastRefundErrorCode != "allocation_incomplete" ||
				reservation.StateVersion != before.StateVersion+2 {
				t.Fatalf("pending reservation = %+v, initial version=%d", reservation, before.StateVersion)
			}
			assertBillingFinancialState(t, fixture, model.CreditReservationStatusRefundPending, 0, reserved)

			// Kernel replay and a wrong owner cannot mutate either Credits or the
			// one-row outcome projection while recovery is still pending.
			replay, err := fixture.store.CommitAttempt(context.Background(), command)
			if err != nil || !replay.Replay || replay.TurnStatus != test.terminalStatus {
				t.Fatalf("terminal pending replay = %+v, %v", replay, err)
			}
			wrongPrincipal := agentturn.PrincipalID("principal_wrong_" + test.suffix)
			if _, err := fixture.authority.GetOutcome(context.Background(), wrongPrincipal, fixture.turn.ID); !errors.Is(err, ErrBindingNotFound) {
				t.Fatalf("wrong-principal GetOutcome error = %v", err)
			}
			if _, err := fixture.authority.ReconcilePending(context.Background(), wrongPrincipal, fixture.turn.ID); !errors.Is(err, ErrBindingNotFound) {
				t.Fatalf("wrong-principal ReconcilePending error = %v", err)
			}
			early, err := fixture.authority.ReconcilePending(
				context.Background(), fixture.turn.PrincipalID, fixture.turn.ID,
			)
			if err != nil || early.OutcomeDigest != pending.OutcomeDigest ||
				early.ReservationStateVersion != pending.ReservationStateVersion {
				t.Fatalf("backoff-window reconcile = %+v, %v", early, err)
			}
			backoffReservation := loadBillingReservation(t, fixture)
			if backoffReservation.StateVersion != reservation.StateVersion ||
				backoffReservation.RefundAttempts != reservation.RefundAttempts {
				t.Fatalf("backoff-window reconcile mutated reservation: before=%+v after=%+v", reservation, backoffReservation)
			}
			assertBillingFinancialState(t, fixture, model.CreditReservationStatusRefundPending, 0, reserved)

			if err := fixture.db.Create(&model.CreditReservationAllocation{
				ReservationID: fixture.reservationID, PackID: fixture.packID, Credits: reserved,
			}).Error; err != nil {
				t.Fatalf("repair allocation evidence: %v", err)
			}
			due := time.Now().Add(-time.Minute)
			if err := fixture.db.Model(&model.CreditReservation{}).
				Where("id = ?", fixture.reservationID).
				Update("next_refund_at", due).Error; err != nil {
				t.Fatalf("make refund retry due: %v", err)
			}

			recovered, err := fixture.authority.ReconcilePending(
				context.Background(), fixture.turn.PrincipalID, fixture.turn.ID,
			)
			if err != nil {
				t.Fatalf("ReconcilePending: %v", err)
			}
			finalReservation := loadBillingReservation(t, fixture)
			if recovered.Status != test.finalOutcome || recovered.RequestedIntent != test.requestedIntent ||
				recovered.UsedUnits == nil || *recovered.UsedUnits != test.used ||
				recovered.RefundTarget != nil || recovered.RefundDue != 0 ||
				recovered.ReservationStateVersion != finalReservation.StateVersion ||
				finalReservation.StateVersion != reservation.StateVersion+1 {
				t.Fatalf("recovered outcome = %+v, reservation = %+v", recovered, finalReservation)
			}
			if recovered.OutcomeID != pending.OutcomeID || recovered.BindingID != pending.BindingID ||
				recovered.SettlementKey != pending.SettlementKey ||
				recovered.LedgerRequestDigest != pending.LedgerRequestDigest ||
				recovered.CreatedAt != pending.CreatedAt || recovered.OutcomeDigest == pending.OutcomeDigest {
				t.Fatalf("recovery did not monotonically update one outcome: pending=%+v recovered=%+v", pending, recovered)
			}
			assertBillingFinancialState(t, fixture, test.reservationStatus, int(test.used), int(test.used))

			// Reconciliation and recovery reads are exact replays after terminal
			// convergence: no second refund, no new ledger row and no version bump.
			reconciledReplay, err := fixture.authority.ReconcilePending(
				context.Background(), fixture.turn.PrincipalID, fixture.turn.ID,
			)
			if err != nil || reconciledReplay.OutcomeDigest != recovered.OutcomeDigest ||
				reconciledReplay.ReservationStateVersion != recovered.ReservationStateVersion {
				t.Fatalf("reconcile replay = %+v, %v", reconciledReplay, err)
			}
			exact, err := fixture.authority.GetOutcome(
				context.Background(), fixture.turn.PrincipalID, fixture.turn.ID,
			)
			if err != nil || exact.OutcomeDigest != recovered.OutcomeDigest ||
				exact.ReservationStateVersion != recovered.ReservationStateVersion {
				t.Fatalf("GetOutcome recovered = %+v, %v", exact, err)
			}
			assertBillingFinancialState(t, fixture, test.reservationStatus, int(test.used), int(test.used))

			var count int64
			if err := fixture.db.Table(OutcomeTable).Where("turn_id = ?", string(fixture.turn.ID)).Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("outcome rows = %d, %v", count, err)
			}
		})
	}
}

func TestCreditSettlementAuthorityRejectsSettlementKeyOrDecisionConflict(t *testing.T) {
	fixture := newBillingFixture(t, "conflict", 10)
	operationID := "operation_conflict"
	if _, err := fixture.store.CommitAttempt(context.Background(), agentturn.CommitAttemptCommand{
		Fence: fixture.attempt.Fence(), OperationID: operationID,
		TerminalStatus: agentv1.TurnStatusCompleted,
		Settlement:     &agentturn.SettlementRequest{Intent: agentturn.SettlementIntentFinalize, UsedUnits: 4},
	}); err != nil {
		t.Fatal(err)
	}
	outcome, err := fixture.authority.GetOutcome(context.Background(), fixture.turn.PrincipalID, fixture.turn.ID)
	if err != nil {
		t.Fatal(err)
	}

	err = fixture.db.Transaction(func(tx *gorm.DB) error {
		return fixture.authority.Settle(tx, agentturn.SettlementCommand{
			TurnID: fixture.turn.ID, PrincipalID: fixture.turn.PrincipalID,
			SettlementKey:     outcome.SettlementKey,
			AuthorizationKind: agentturn.SettlementAuthorizationOperation,
			AttemptID:         fixture.attempt.ID, FencingToken: fixture.attempt.FencingToken,
			OperationID: operationID, Intent: agentturn.SettlementIntentFinalize,
			TerminalStatus: agentv1.TurnStatusCompleted, UsedUnits: 5,
		})
	})
	if !errors.Is(err, ErrOutcomeConflict) {
		t.Fatalf("same-key different decision error = %v", err)
	}
	err = fixture.db.Transaction(func(tx *gorm.DB) error {
		otherOperation := "operation_other"
		return fixture.authority.Settle(tx, agentturn.SettlementCommand{
			TurnID: fixture.turn.ID, PrincipalID: fixture.turn.PrincipalID,
			SettlementKey:     billingSettlementKey(fixture.turn.ID, otherOperation),
			AuthorizationKind: agentturn.SettlementAuthorizationOperation,
			AttemptID:         fixture.attempt.ID, FencingToken: fixture.attempt.FencingToken,
			OperationID: otherOperation, Intent: agentturn.SettlementIntentFinalize,
			TerminalStatus: agentv1.TurnStatusCompleted, UsedUnits: 4,
		})
	})
	if !errors.Is(err, ErrOutcomeConflict) {
		t.Fatalf("different-key same-turn error = %v", err)
	}
	assertBillingFinancialState(t, fixture, model.CreditReservationStatusFinalized, 4, 4)
}

func TestOutcomeRecordRejectsAuthorizationTupleWithRecomputedDigest(t *testing.T) {
	fixture := newBillingFixture(t, "authorization_integrity", 10)
	if _, err := fixture.store.CommitAttempt(context.Background(), agentturn.CommitAttemptCommand{
		Fence: fixture.attempt.Fence(), OperationID: "operation_authorization_integrity",
		TerminalStatus: agentv1.TurnStatusCompleted,
		Settlement:     &agentturn.SettlementRequest{Intent: agentturn.SettlementIntentFinalize, UsedUnits: 4},
	}); err != nil {
		t.Fatal(err)
	}
	outcome, err := fixture.authority.GetOutcome(context.Background(), fixture.turn.PrincipalID, fixture.turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	forgedOperation := "operation_forged"
	outcome.OperationID = &forgedOperation
	outcome.OutcomeDigest = outcomeRecordDigest(outcome)
	if err := outcome.Validate(); !errors.Is(err, ErrOutcomeConflict) {
		t.Fatalf("forged authorization tuple validation = %v", err)
	}
}

func TestCreditSettlementAuthorityReleasesProvablyEmptyTurn(t *testing.T) {
	fixture := newBillingFixture(t, "release", 10)
	result, err := fixture.store.CommitAttempt(context.Background(), agentturn.CommitAttemptCommand{
		Fence: fixture.attempt.Fence(), OperationID: "operation_release",
		TerminalStatus: agentv1.TurnStatusFailed,
		Settlement:     &agentturn.SettlementRequest{Intent: agentturn.SettlementIntentRelease},
	})
	if err != nil || result.SettlementReview != nil {
		t.Fatalf("release commit = %+v, %v", result, err)
	}
	outcome, err := fixture.authority.GetOutcome(context.Background(), fixture.turn.PrincipalID, fixture.turn.ID)
	if err != nil || outcome.Status != OutcomeStatusReleased || outcome.RequestedIntent != RequestedIntentRelease ||
		outcome.UsedUnits == nil || *outcome.UsedUnits != 0 {
		t.Fatalf("release outcome = %+v, %v", outcome, err)
	}
	assertBillingFinancialState(t, fixture, model.CreditReservationStatusReleased, 0, 0)
}

func TestCreditSettlementAuthorityHoldsAmbiguousTurnForReview(t *testing.T) {
	fixture := newBillingFixture(t, "review", 10)
	if _, err := fixture.store.CommitAttempt(context.Background(), agentturn.CommitAttemptCommand{
		Fence: fixture.attempt.Fence(), OperationID: "operation_partial",
		Event: &agentturn.EventDraft{Type: "writer.document.delta", Data: json.RawMessage(`{"partial":true}`)},
	}); err != nil {
		t.Fatalf("partial commit: %v", err)
	}
	result, err := fixture.store.CommitAttempt(context.Background(), agentturn.CommitAttemptCommand{
		Fence: fixture.attempt.Fence(), OperationID: "operation_review_terminal",
		TerminalStatus: agentv1.TurnStatusFailed,
		Settlement:     &agentturn.SettlementRequest{Intent: agentturn.SettlementIntentRelease},
	})
	if err != nil || result.SettlementReview == nil {
		t.Fatalf("review terminal commit = %+v, %v", result, err)
	}
	outcome, err := fixture.authority.GetOutcome(context.Background(), fixture.turn.PrincipalID, fixture.turn.ID)
	if err != nil || outcome.Status != OutcomeStatusReviewHeld || outcome.RequestedIntent != RequestedIntentReview ||
		outcome.ReviewID == nil || *outcome.ReviewID != result.SettlementReview.ReviewID ||
		outcome.ReviewRequestDigest == nil || *outcome.ReviewRequestDigest != result.SettlementReview.RequestDigest {
		t.Fatalf("held outcome = %+v, %v", outcome, err)
	}
	assertBillingFinancialState(t, fixture, model.CreditReservationStatusReviewHold, 0, 10)
}

func TestCreditSettlementAuthorityFailureRollsBackTerminalAndCredits(t *testing.T) {
	fixture := newBillingFixture(t, "rollback", 10)
	if err := fixture.db.Exec(`CREATE TRIGGER fail_agent_billing_outcome BEFORE INSERT ON w_agent_turn_settlement_outcome
		BEGIN SELECT RAISE(FAIL, 'forced outcome failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	_, err := fixture.store.CommitAttempt(context.Background(), agentturn.CommitAttemptCommand{
		Fence: fixture.attempt.Fence(), OperationID: "operation_rollback",
		TerminalStatus: agentv1.TurnStatusCompleted,
		Settlement:     &agentturn.SettlementRequest{Intent: agentturn.SettlementIntentFinalize, UsedUnits: 4},
	})
	if !errors.Is(err, agentturn.ErrSettlementFailed) {
		t.Fatalf("terminal failure = %v", err)
	}
	var turn struct {
		Status string `gorm:"column:status"`
	}
	if err := fixture.db.Table(agentturn.SQLTurnTable).Where("turn_id = ?", string(fixture.turn.ID)).Take(&turn).Error; err != nil {
		t.Fatal(err)
	}
	if turn.Status != string(agentv1.TurnStatusRunning) {
		t.Fatalf("turn status = %q, want running", turn.Status)
	}
	var count int64
	if err := fixture.db.Table(OutcomeTable).Where("turn_id = ?", string(fixture.turn.ID)).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("outcome count = %d, %v", count, err)
	}
	assertBillingFinancialState(t, fixture, model.CreditReservationStatusReserved, 0, 10)
}

func TestCreditSettlementAuthorityBlocksUnboundFreshClaim(t *testing.T) {
	db := testutil.NewTestDB(t)
	store, err := agentturn.NewSQLStore(db)
	if err != nil {
		t.Fatal(err)
	}
	turn := billingTestTurn("unbound")
	if _, err := store.Admit(context.Background(), turn,
		agentturn.EventDraft{Type: agentv1.EventCoreTurnStatus, Data: json.RawMessage(`{"status":"queued"}`)}); err != nil {
		t.Fatal(err)
	}
	authority, err := NewCreditSettlementAuthority(db, account.NewCreditReservationService())
	if err != nil {
		t.Fatal(err)
	}
	store.WithSettlementAuthority(authority)
	_, err = store.ClaimAttempt(context.Background(), agentturn.ClaimAttemptCommand{
		TurnID: turn.ID, AttemptID: "attempt_unbound", WorkerID: "worker_unbound",
		WorkerBuildDigest: "sha256:worker-unbound",
	})
	if !errors.Is(err, agentturn.ErrTurnReservationExecutionUnauthorized) {
		t.Fatalf("unbound claim error = %v", err)
	}
	var attempts int64
	if err := db.Table(agentturn.SQLTurnAttemptTable).Where("turn_id = ?", string(turn.ID)).Count(&attempts).Error; err != nil || attempts != 0 {
		t.Fatalf("attempts = %d, %v", attempts, err)
	}
}

func assertBillingFinancialState(
	t *testing.T,
	fixture billingFixture,
	status string,
	used int,
	packUsed int,
) {
	t.Helper()
	var reservation model.CreditReservation
	if err := fixture.db.First(&reservation, fixture.reservationID).Error; err != nil {
		t.Fatal(err)
	}
	if reservation.Status != status || reservation.Used != used {
		t.Fatalf("reservation = status:%s used:%d, want %s/%d", reservation.Status, reservation.Used, status, used)
	}
	var pack model.CreditsPack
	if err := fixture.db.First(&pack, fixture.packID).Error; err != nil {
		t.Fatal(err)
	}
	if pack.CreditsUsed != packUsed {
		t.Fatalf("pack used = %d, want %d", pack.CreditsUsed, packUsed)
	}
}

func loadBillingReservation(t *testing.T, fixture billingFixture) model.CreditReservation {
	t.Helper()
	var reservation model.CreditReservation
	if err := fixture.db.First(&reservation, fixture.reservationID).Error; err != nil {
		t.Fatal(err)
	}
	return reservation
}

func billingSettlementKey(turnID agentv1.TurnID, operationID string) string {
	hash := sha256.New()
	for _, part := range []string{"operation", string(turnID), operationID} {
		fmt.Fprintf(hash, "%d:%s", len(part), part)
	}
	return "wm:turn-settlement:v1:" + hex.EncodeToString(hash.Sum(nil))
}
