package agentturn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	agentv1 "server/contracts/agent/v1"
)

func newTestReconciler(t *testing.T, store *SQLStore) *Reconciler {
	t.Helper()
	reconciler, err := NewReconciler(store, store, ReconcilerOptions{
		ReconcilerID:          "reconciler_test",
		ReconcilerBuildDigest: "sha256:reconciler-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return reconciler
}

type reservationExpiryTestAuthority struct {
	*testSettlementAuthority
	verifyErr   error
	verifyCalls int
}

func newReservationExpiryTestAuthority() *reservationExpiryTestAuthority {
	return &reservationExpiryTestAuthority{testSettlementAuthority: newTestSettlementAuthority()}
}

func (*reservationExpiryTestAuthority) AuthorizeTurnExecution(*gorm.DB, Turn) error { return nil }

func (authority *reservationExpiryTestAuthority) VerifyExpiredTurnReservation(*gorm.DB, Turn) error {
	authority.verifyCalls++
	return authority.verifyErr
}

// exhaustTurnAttempts claims and expires the Turn until its attempt budget is
// spent, returning the final Attempt so callers can assert against the last
// dead epoch.
func exhaustTurnAttempts(t *testing.T, store *SQLStore, clock *sqlExecutionTestClock, turnID agentv1.TurnID, prefix string) ClaimAttemptResult {
	t.Helper()
	var last ClaimAttemptResult
	for attempt := 0; attempt < DefaultMaxTurnAttempts; attempt++ {
		claimed, err := store.ClaimAttempt(context.Background(),
			executionClaimCommand(turnID, prefix+string(rune('a'+attempt))))
		if err != nil {
			t.Fatalf("claim %d of %d: %v", attempt+1, DefaultMaxTurnAttempts, err)
		}
		clock.Set(claimed.Attempt.LeaseExpiresAt)
		last = claimed
	}
	return last
}

func TestSQLAttemptBudgetStopsRetryLoopAndBecomesActionable(t *testing.T) {
	db, store, clock, turns := newSQLClaimNextFixture(t, "budget")
	turn := turns[0]

	last := exhaustTurnAttempts(t, store, clock, turn.ID, "attempt_budget_")
	if last.Attempt.FencingToken != agentv1.Sequence(DefaultMaxTurnAttempts) {
		t.Fatalf("final fence = %d, want %d", last.Attempt.FencingToken, DefaultMaxTurnAttempts)
	}

	// The budget, not the schema bound, is what stops the loop.
	if _, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_over_budget")); !errors.Is(err, ErrAttemptBudgetExhausted) {
		t.Fatalf("over-budget ClaimAttempt() error = %v, want ErrAttemptBudgetExhausted", err)
	}
	if _, err := store.ClaimNext(context.Background(), claimNextCommand("attempt_over_budget_next")); !errors.Is(err, ErrNoClaimableTurn) {
		t.Fatalf("over-budget ClaimNext() error = %v, want ErrNoClaimableTurn", err)
	}
	if got := executionTableCount(t, db, SQLTurnAttemptTable, "turn_id = ?", turn.ID); got != int64(DefaultMaxTurnAttempts) {
		t.Fatalf("turn holds %d attempts, want exactly the budget %d", got, DefaultMaxTurnAttempts)
	}

	reclaimable, err := store.ListReclaimableTurns(context.Background(), ReclaimQuery{ActionableOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(reclaimable) != 1 || reclaimable[0].TurnID != turn.ID || reclaimable[0].Reason != ReclaimReasonAttemptsExhausted {
		t.Fatalf("actionable reclaimable = %+v, want %q attempts_exhausted", reclaimable, turn.ID)
	}
	if !reclaimable[0].Reason.Actionable() {
		t.Fatal("attempts_exhausted must be actionable")
	}
}

func TestSQLReconcileTerminalRetiresExhaustedTurnAsTimeout(t *testing.T) {
	db, store, clock, turns := newSQLClaimNextFixture(t, "retire")
	turn := turns[0]
	last := exhaustTurnAttempts(t, store, clock, turn.ID, "attempt_retire_")

	result, err := store.ReconcileTerminal(context.Background(), ReconcileCommand{
		TurnID: turn.ID, Reason: ReclaimReasonAttemptsExhausted,
		ReconcilerID: "reconciler_test", ReconcilerBuildDigest: "sha256:reconciler-test",
	})
	if err != nil {
		t.Fatalf("ReconcileTerminal(): %v", err)
	}
	if !result.Changed || result.TerminalStatus != agentv1.TurnStatusTimeout {
		t.Fatalf("ReconcileTerminal() = %+v, want a changed timeout", result)
	}
	if result.FencedAttemptID != last.Attempt.ID {
		t.Fatalf("fenced attempt = %q, want the last dead epoch %q", result.FencedAttemptID, last.Attempt.ID)
	}

	var payload struct {
		Status     string `json:"status"`
		Reconciled bool   `json:"reconciled"`
		Reason     string `json:"reason"`
	}
	if err := json.Unmarshal(result.Event.Data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Status != string(agentv1.TurnStatusTimeout) || !payload.Reconciled ||
		payload.Reason != string(ReclaimReasonAttemptsExhausted) {
		t.Fatalf("terminal event payload = %+v, want a reconciled timeout", payload)
	}

	var state executionTestTurnState
	executionTakeTurnState(t, db, turn.ID, &state)
	if state.Status != string(agentv1.TurnStatusTimeout) || state.ActiveAttemptID != nil {
		t.Fatalf("retired turn state = %+v", state)
	}
	// The fence advanced past the retired epoch so a partitioned executor that
	// wakes up cannot commit against it.
	if state.FencingToken != int64(last.Attempt.FencingToken)+1 {
		t.Fatalf("fence = %d, want %d", state.FencingToken, int64(last.Attempt.FencingToken)+1)
	}
	if _, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence: last.Attempt.Fence(), OperationID: "operation_late_executor",
		Event: &EventDraft{Type: "writer.late", Data: json.RawMessage(`{"late":true}`)},
	}); err == nil {
		t.Fatal("a retired epoch was still able to commit")
	}
	if got := executionTableCount(t, db, SQLTurnOperationTable, "turn_id = ?", turn.ID); got != 0 {
		t.Fatalf("reconciliation or late commit wrote %d operation receipts, want 0", got)
	}

	// Reconciliation is idempotent: a second pass appends no second terminal.
	repeat, err := store.ReconcileTerminal(context.Background(), ReconcileCommand{
		TurnID: turn.ID, Reason: ReclaimReasonAttemptsExhausted,
		ReconcilerID: "reconciler_test", ReconcilerBuildDigest: "sha256:reconciler-test",
	})
	if err != nil || repeat.Changed || repeat.TerminalStatus != agentv1.TurnStatusTimeout {
		t.Fatalf("repeat ReconcileTerminal() = %+v, %v", repeat, err)
	}
	if got := executionTableCount(t, db, SQLTurnEventTable, "turn_id = ? AND event_type = ?",
		turn.ID, string(agentv1.EventCoreTurnStatus)); got != int64(state.LastEventSequence) {
		t.Fatalf("status event count = %d, want %d", got, state.LastEventSequence)
	}
}

func TestSQLReconcileTerminalStopsObserverlessCancellation(t *testing.T) {
	db, store, clock, turns := newSQLClaimNextFixture(t, "cancel")
	turn := turns[0]
	if _, err := store.RequestCancel(context.Background(), turn.PrincipalID, turn.ThreadID, turn.ID,
		clock.Get(), sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"cancellationRequested":true}`)); err != nil {
		t.Fatal(err)
	}

	result, err := store.ReconcileTerminal(context.Background(), ReconcileCommand{
		TurnID: turn.ID, Reason: ReclaimReasonCancellationPending,
		ReconcilerID: "reconciler_test", ReconcilerBuildDigest: "sha256:reconciler-test",
	})
	if err != nil {
		t.Fatalf("ReconcileTerminal(): %v", err)
	}
	if !result.Changed || result.TerminalStatus != agentv1.TurnStatusStopped || result.FencedAttemptID != "" {
		t.Fatalf("ReconcileTerminal() = %+v, want a changed stopped with no fenced attempt", result)
	}
	var state executionTestTurnState
	executionTakeTurnState(t, db, turn.ID, &state)
	if state.Status != string(agentv1.TurnStatusStopped) || state.ActiveAttemptID != nil {
		t.Fatalf("cancelled turn state = %+v", state)
	}
	// No epoch ran, but the fence still advances so a stale claim cannot land.
	if state.FencingToken != 1 {
		t.Fatalf("fence = %d, want 1", state.FencingToken)
	}
	if got := executionTableCount(t, db, SQLTurnAttemptTable, "turn_id = ?", turn.ID); got != 0 {
		t.Fatalf("reconciliation invented %d attempts", got)
	}
}

func TestSQLReconcileExpiredReservationRetiresOnlyWithoutLiveExecutor(t *testing.T) {
	t.Run("queued never-started Turn", func(t *testing.T) {
		db, store, _, turns := newSQLClaimNextFixture(t, "reservation_expired_queued")
		authority := newReservationExpiryTestAuthority()
		store.WithSettlementAuthority(authority)

		result, err := store.ReconcileTerminal(context.Background(), ReconcileCommand{
			TurnID: turns[0].ID, Reason: ReclaimReasonReservationExpired,
			ReconcilerID: "reservation_expiry_test", ReconcilerBuildDigest: "sha256:reservation-expiry-test",
		})
		if err != nil || !result.Changed || result.TerminalStatus != agentv1.TurnStatusTimeout ||
			result.FencedAttemptID != "" || authority.verifyCalls != 1 {
			t.Fatalf("queued expiry reconcile = %+v, %v, verifyCalls=%d", result, err, authority.verifyCalls)
		}
		var state executionTestTurnState
		executionTakeTurnState(t, db, turns[0].ID, &state)
		if state.Status != string(agentv1.TurnStatusTimeout) || state.ActiveAttemptID != nil || state.FencingToken != 1 {
			t.Fatalf("queued expiry state = %+v", state)
		}
		if authority.keyCount(reconcileSettlementKey(turns[0].ID, 1)) != 1 {
			t.Fatal("queued expiry did not use the exact reconcile settlement key")
		}
	})

	t.Run("live Attempt survives reservation TTL", func(t *testing.T) {
		db, store, _, turns := newSQLClaimNextFixture(t, "reservation_expired_live")
		authority := newReservationExpiryTestAuthority()
		store.WithSettlementAuthority(authority)
		claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turns[0].ID, "attempt_expired_live"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.ReconcileTerminal(context.Background(), ReconcileCommand{
			TurnID: turns[0].ID, Reason: ReclaimReasonReservationExpired,
			ReconcilerID: "reservation_expiry_test", ReconcilerBuildDigest: "sha256:reservation-expiry-test",
		}); !errors.Is(err, ErrReconcilePrecondition) {
			t.Fatalf("live expiry reconcile error = %v, want ErrReconcilePrecondition", err)
		}
		if authority.verifyCalls != 0 {
			t.Fatalf("live Attempt reached commercial expiry verification %d times", authority.verifyCalls)
		}
		var state executionTestTurnState
		executionTakeTurnState(t, db, turns[0].ID, &state)
		if state.Status != string(agentv1.TurnStatusRunning) || state.ActiveAttemptID == nil ||
			*state.ActiveAttemptID != claimed.Attempt.ID {
			t.Fatalf("live Attempt was disturbed: %+v", state)
		}
	})

	t.Run("lease-expired running Turn", func(t *testing.T) {
		db, store, clock, turns := newSQLClaimNextFixture(t, "reservation_expired_running")
		authority := newReservationExpiryTestAuthority()
		store.WithSettlementAuthority(authority)
		claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turns[0].ID, "attempt_expired_dead"))
		if err != nil {
			t.Fatal(err)
		}
		clock.Set(claimed.Attempt.LeaseExpiresAt)

		result, err := store.ReconcileTerminal(context.Background(), ReconcileCommand{
			TurnID: turns[0].ID, Reason: ReclaimReasonReservationExpired,
			ReconcilerID: "reservation_expiry_test", ReconcilerBuildDigest: "sha256:reservation-expiry-test",
		})
		if err != nil || !result.Changed || result.TerminalStatus != agentv1.TurnStatusTimeout ||
			result.FencedAttemptID != claimed.Attempt.ID || authority.verifyCalls != 1 {
			t.Fatalf("dead expiry reconcile = %+v, %v, verifyCalls=%d", result, err, authority.verifyCalls)
		}
		var attempt executionTestAttemptState
		if err := db.Table(SQLTurnAttemptTable).
			Select("attempt_id", "status", "fencing_token", "last_heartbeat_at", "lease_expires_at", "finished_at").
			Where("attempt_id = ?", claimed.Attempt.ID).Take(&attempt).Error; err != nil {
			t.Fatal(err)
		}
		if attempt.Status != string(AttemptStatusExpired) || attempt.FinishedAt == nil {
			t.Fatalf("dead Attempt state = %+v", attempt)
		}
	})
}

func TestSQLReconcileTerminalFailsClosedAgainstRecoveredWork(t *testing.T) {
	_, store, clock, turns := newSQLClaimNextFixture(t, "live", "wrong_reason", "healthy")
	live, wrongReason, healthy := turns[0], turns[1], turns[2]

	// A live lease means an executor still owns the Turn.
	if _, err := store.ClaimAttempt(context.Background(), executionClaimCommand(live.ID, "attempt_live_owner")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReconcileTerminal(context.Background(), ReconcileCommand{
		TurnID: live.ID, Reason: ReclaimReasonAttemptsExhausted,
		ReconcilerID: "reconciler_test", ReconcilerBuildDigest: "sha256:reconciler-test",
	}); !errors.Is(err, ErrReconcilePrecondition) {
		t.Fatalf("live-lease ReconcileTerminal() error = %v, want ErrReconcilePrecondition", err)
	}

	// Cancellation is the more specific outcome, so an exhausted-budget
	// command against a cancelled Turn must not land it on `timeout`.
	exhaustTurnAttempts(t, store, clock, wrongReason.ID, "attempt_wrong_")
	if _, err := store.RequestCancel(context.Background(), wrongReason.PrincipalID, wrongReason.ThreadID, wrongReason.ID,
		clock.Get(), sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"cancellationRequested":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReconcileTerminal(context.Background(), ReconcileCommand{
		TurnID: wrongReason.ID, Reason: ReclaimReasonAttemptsExhausted,
		ReconcilerID: "reconciler_test", ReconcilerBuildDigest: "sha256:reconciler-test",
	}); !errors.Is(err, ErrReconcilePrecondition) {
		t.Fatalf("cancelled Turn accepted an exhausted-budget reason: %v", err)
	}
	stopped, err := store.ReconcileTerminal(context.Background(), ReconcileCommand{
		TurnID: wrongReason.ID, Reason: ReclaimReasonCancellationPending,
		ReconcilerID: "reconciler_test", ReconcilerBuildDigest: "sha256:reconciler-test",
	})
	if err != nil || stopped.TerminalStatus != agentv1.TurnStatusStopped {
		t.Fatalf("re-issued cancellation ReconcileTerminal() = %+v, %v", stopped, err)
	}

	// A Turn that never went stuck is not reconcilable at all.
	if _, err := store.ReconcileTerminal(context.Background(), ReconcileCommand{
		TurnID: healthy.ID, Reason: ReclaimReasonCancellationPending,
		ReconcilerID: "reconciler_test", ReconcilerBuildDigest: "sha256:reconciler-test",
	}); !errors.Is(err, ErrReconcilePrecondition) {
		t.Fatalf("healthy Turn ReconcileTerminal() error = %v, want ErrReconcilePrecondition", err)
	}
	// A non-actionable reason is rejected before any database work.
	if _, err := store.ReconcileTerminal(context.Background(), ReconcileCommand{
		TurnID: healthy.ID, Reason: ReclaimReasonLeaseExpired,
		ReconcilerID: "reconciler_test", ReconcilerBuildDigest: "sha256:reconciler-test",
	}); err == nil {
		t.Fatal("lease_expired was accepted as a reconcile reason")
	}
}

func TestReconcilerPassRetiresOnlyActionableWorkAndLeavesRetriesAlone(t *testing.T) {
	db, store, clock, turns := newSQLClaimNextFixture(t, "exhausted", "cancelled", "retryable", "fresh")
	exhausted, cancelled, retryable, fresh := turns[0], turns[1], turns[2], turns[3]

	exhaustTurnAttempts(t, store, clock, exhausted.ID, "attempt_pass_ex_")
	if _, err := store.RequestCancel(context.Background(), cancelled.PrincipalID, cancelled.ThreadID, cancelled.ID,
		clock.Get(), sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"cancellationRequested":true}`)); err != nil {
		t.Fatal(err)
	}
	// One dead epoch, budget remaining: this is retry traffic, not stuck work.
	retryClaim, err := store.ClaimAttempt(context.Background(), executionClaimCommand(retryable.ID, "attempt_pass_retry"))
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(maxTime(clock.Get(), retryClaim.Attempt.LeaseExpiresAt))

	report, err := newTestReconciler(t, store).ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("ReconcileOnce(): %v", err)
	}
	if report.Scanned != 2 || len(report.Retired) != 2 || report.Skipped != 0 || len(report.Failures) != 0 {
		t.Fatalf("report = %+v, want exactly the two actionable Turns retired", report)
	}
	retired := make(map[agentv1.TurnID]agentv1.TurnStatus, len(report.Retired))
	for _, result := range report.Retired {
		retired[result.Turn.ID] = result.TerminalStatus
	}
	if retired[exhausted.ID] != agentv1.TurnStatusTimeout || retired[cancelled.ID] != agentv1.TurnStatusStopped {
		t.Fatalf("retired = %+v, want %q timeout and %q stopped", retired, exhausted.ID, cancelled.ID)
	}

	// The retryable Turn is untouched and still claimable; the never-claimed
	// Turn was never scanned as stuck.
	var retryState executionTestTurnState
	executionTakeTurnState(t, db, retryable.ID, &retryState)
	if retryState.Status != string(agentv1.TurnStatusRunning) || retryState.FencingToken != 1 {
		t.Fatalf("retryable turn was mutated: %+v", retryState)
	}
	claimed, err := store.ClaimNext(context.Background(), claimNextCommand("attempt_after_pass"))
	if err != nil {
		t.Fatalf("ClaimNext() after reconciliation: %v", err)
	}
	if claimed.Turn.ID != retryable.ID && claimed.Turn.ID != fresh.ID {
		t.Fatalf("ClaimNext() returned %q, want a still-claimable Turn", claimed.Turn.ID)
	}

	// A second pass finds nothing left to do.
	second, err := newTestReconciler(t, store).ReconcileOnce(context.Background())
	if err != nil || second.Scanned != 0 || len(second.Retired) != 0 {
		t.Fatalf("second ReconcileOnce() = %+v, %v", second, err)
	}
}

func TestReconcilerRejectsInvalidConstruction(t *testing.T) {
	_, store, _, _ := newSQLClaimNextFixture(t, "construct")
	valid := ReconcilerOptions{ReconcilerID: "r", ReconcilerBuildDigest: "sha256:r"}
	if _, err := NewReconciler(nil, store, valid); err == nil {
		t.Fatal("nil scanner accepted")
	}
	if _, err := NewReconciler(store, nil, valid); err == nil {
		t.Fatal("nil store accepted")
	}
	for name, options := range map[string]ReconcilerOptions{
		"missing id":      {ReconcilerBuildDigest: "sha256:r"},
		"missing digest":  {ReconcilerID: "r"},
		"oversized batch": {ReconcilerID: "r", ReconcilerBuildDigest: "sha256:r", BatchLimit: MaxReclaimScanLimit + 1},
		"negative batch":  {ReconcilerID: "r", ReconcilerBuildDigest: "sha256:r", BatchLimit: -1},
	} {
		if _, err := NewReconciler(store, store, options); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func TestReconcilerRunDrainsBacklogThenIntervalsAndStops(t *testing.T) {
	// A backlog larger than one page must drain at scan speed, not one page
	// per interval.
	const stuck = 5
	suffixes := make([]string, 0, stuck)
	for index := 0; index < stuck; index++ {
		suffixes = append(suffixes, fmt.Sprintf("run_%02d", index))
	}
	_, store, clock, turns := newSQLClaimNextFixture(t, suffixes...)
	for _, turn := range turns {
		if _, err := store.RequestCancel(context.Background(), turn.PrincipalID, turn.ThreadID, turn.ID,
			clock.Get(), sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"cancellationRequested":true}`)); err != nil {
			t.Fatal(err)
		}
	}

	reconciler, err := NewReconciler(store, store, ReconcilerOptions{
		ReconcilerID: "reconciler_run", ReconcilerBuildDigest: "sha256:reconciler-run",
		BatchLimit: 2, Interval: time.Millisecond, JitterFraction: 0.5,
		Rand: func() float64 { return 0.5 },
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	var mu sync.Mutex
	retired := map[agentv1.TurnID]bool{}
	drained := make(chan struct{})
	runErr := make(chan error, 1)
	go func() {
		runErr <- reconciler.Run(ctx, func(report ReconcileReport, err error) {
			if err != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, result := range report.Retired {
				retired[result.Turn.ID] = true
			}
			if len(retired) == stuck {
				select {
				case <-drained:
				default:
					close(drained)
				}
			}
		})
	}()

	select {
	case <-drained:
	case <-time.After(20 * time.Second):
		t.Fatal("reconciler did not drain the backlog")
	}
	stop()
	if err := <-runErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, turn := range turns {
		if !retired[turn.ID] {
			t.Fatalf("turn %q was never retired", turn.ID)
		}
	}
}

func TestReconcilerJitterStaysInsideItsBand(t *testing.T) {
	interval := time.Second
	for name, tc := range map[string]struct {
		fraction float64
		random   float64
		want     time.Duration
	}{
		"lower bound": {fraction: 0.2, random: 0, want: 800 * time.Millisecond},
		"midpoint":    {fraction: 0.2, random: 0.5, want: interval},
		"upper bound": {fraction: 0.2, random: 1, want: 1200 * time.Millisecond},
		"disabled":    {fraction: 0, random: 0, want: interval},
	} {
		options := ReconcilerOptions{
			ReconcilerID: "r", ReconcilerBuildDigest: "sha256:r",
			Interval: interval, JitterFraction: tc.fraction,
			Rand: func() float64 { return tc.random },
		}
		// A zero fraction must mean "no jitter", not "fall back to the default".
		if tc.fraction == 0 {
			options.JitterFraction = 0
			options.applyDefaults()
			options.JitterFraction = 0
		}
		reconciler := &Reconciler{options: options}
		if got := reconciler.nextDelay(); got != tc.want {
			t.Fatalf("%s: nextDelay() = %s, want %s", name, got, tc.want)
		}
	}
	if _, err := NewReconciler(nil, nil, ReconcilerOptions{}); err == nil {
		t.Fatal("nil dependencies accepted")
	}
}
