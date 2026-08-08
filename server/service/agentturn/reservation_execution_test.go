package agentturn

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	agentv1 "server/contracts/agent/v1"
)

type reservationExecutionTestAuthority struct {
	calls atomic.Int64
	mu    sync.RWMutex
	err   error
	check func(*gorm.DB, Turn) error
}

func (*reservationExecutionTestAuthority) Settle(*gorm.DB, SettlementCommand) error { return nil }

func (authority *reservationExecutionTestAuthority) AuthorizeTurnExecution(tx *gorm.DB, turn Turn) error {
	authority.calls.Add(1)
	authority.mu.RLock()
	err := authority.err
	check := authority.check
	authority.mu.RUnlock()
	if tx == nil {
		return errors.New("missing claim transaction")
	}
	if check != nil {
		if checkErr := check(tx, turn); checkErr != nil {
			return checkErr
		}
	}
	return err
}

func (authority *reservationExecutionTestAuthority) setFailure(err error) {
	authority.mu.Lock()
	authority.err = err
	authority.mu.Unlock()
}

func (authority *reservationExecutionTestAuthority) setCheck(check func(*gorm.DB, Turn) error) {
	authority.mu.Lock()
	authority.check = check
	authority.mu.Unlock()
}

type reservationExecutionLegacySettlementAuthority struct{}

func (reservationExecutionLegacySettlementAuthority) Settle(*gorm.DB, SettlementCommand) error {
	return nil
}

func TestClaimAttemptFreshExecutionAuthorizationSucceeds(t *testing.T) {
	_, store, turn, _ := newSQLExecutionFixture(t, "reservation_authorized")
	authority := &reservationExecutionTestAuthority{}
	store.WithSettlementAuthority(authority)

	claimed, err := store.ClaimAttempt(
		context.Background(), executionClaimCommand(turn.ID, "attempt_reservation_authorized"),
	)
	if err != nil || !claimed.Claimed || claimed.Replay || claimed.Attempt.FencingToken != 1 {
		t.Fatalf("authorized ClaimAttempt() = %+v, %v", claimed, err)
	}
	if authority.calls.Load() != 1 {
		t.Fatalf("execution authorization calls = %d, want 1", authority.calls.Load())
	}
}

func TestClaimAttemptExecutionAuthorizationRejectionLeavesFreshTurnUnchanged(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "missing_binding", err: errors.New("private-missing-binding")},
		{name: "expired_reservation", err: errors.New("private-expired-reservation")},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, store, turn, _ := newSQLExecutionFixture(t, "reservation_reject_"+test.name)
			authority := &reservationExecutionTestAuthority{err: test.err}
			authority.setCheck(func(tx *gorm.DB, locked Turn) error {
				if locked.ID != turn.ID || locked.Status != agentv1.TurnStatusQueued {
					return errors.New("authority did not observe the locked queued Turn")
				}
				var attempts int64
				if err := tx.Table(SQLTurnAttemptTable).Where("turn_id = ?", string(turn.ID)).Count(&attempts).Error; err != nil {
					return err
				}
				if attempts != 0 {
					return errors.New("fresh Attempt existed before execution authorization")
				}
				return nil
			})
			store.WithSettlementAuthority(authority)

			_, err := store.ClaimAttempt(
				context.Background(), executionClaimCommand(turn.ID, "attempt_reservation_rejected"),
			)
			if !errors.Is(err, ErrTurnReservationExecutionUnauthorized) || strings.Contains(err.Error(), test.err.Error()) {
				t.Fatalf("rejected ClaimAttempt() error = %v, want opaque execution sentinel", err)
			}
			if authority.calls.Load() != 1 {
				t.Fatalf("execution authorization calls = %d, want 1", authority.calls.Load())
			}
			var state executionTestTurnState
			executionTakeTurnState(t, db, turn.ID, &state)
			if state.Status != string(agentv1.TurnStatusQueued) || state.ActiveAttemptID != nil ||
				state.FencingToken != 0 || state.LastEventSequence != 1 {
				t.Fatalf("rejected fresh claim mutated Turn: %+v", state)
			}
			if got := executionTableCount(t, db, SQLTurnAttemptTable, "turn_id = ?", turn.ID); got != 0 {
				t.Fatalf("rejected fresh claim persisted %d Attempts", got)
			}
			if got := executionTableCount(t, db, SQLTurnEventTable, "turn_id = ?", turn.ID); got != 1 {
				t.Fatalf("rejected fresh claim changed Event count to %d", got)
			}
		})
	}
}

func TestClaimNextSkipsOnlyExactReservationExpiry(t *testing.T) {
	t.Run("exact expiry lets a healthy candidate proceed", func(t *testing.T) {
		db, store, _, turns := newSQLClaimNextFixture(t, "expired_first", "healthy_second")
		authority := &reservationExecutionTestAuthority{}
		authority.setCheck(func(_ *gorm.DB, turn Turn) error {
			if turn.ID == turns[0].ID {
				return ErrTurnReservationExecutionExpired
			}
			return nil
		})
		store.WithSettlementAuthority(authority)

		claimed, err := store.ClaimNext(context.Background(), claimNextCommand("attempt_after_expired_hold"))
		if err != nil || claimed.Turn.ID != turns[1].ID || !claimed.Claimed {
			t.Fatalf("ClaimNext() = %+v, %v, want healthy second Turn", claimed, err)
		}
		var first executionTestTurnState
		executionTakeTurnState(t, db, turns[0].ID, &first)
		if first.Status != string(agentv1.TurnStatusQueued) || first.ActiveAttemptID != nil || first.FencingToken != 0 {
			t.Fatalf("skipped expired Turn mutated: %+v", first)
		}
	})

	t.Run("generic denial stops discovery", func(t *testing.T) {
		db, store, _, turns := newSQLClaimNextFixture(t, "denied_first", "hidden_healthy")
		authority := &reservationExecutionTestAuthority{}
		authority.setCheck(func(_ *gorm.DB, turn Turn) error {
			if turn.ID == turns[0].ID {
				return errors.New("private owner tuple failure")
			}
			return nil
		})
		store.WithSettlementAuthority(authority)

		if _, err := store.ClaimNext(context.Background(), claimNextCommand("attempt_must_not_skip_denial")); !errors.Is(err, ErrTurnReservationExecutionUnauthorized) ||
			errors.Is(err, ErrTurnReservationExecutionExpired) {
			t.Fatalf("ClaimNext() error = %v, want opaque Unauthorized only", err)
		}
		if got := executionTableCount(t, db, SQLTurnAttemptTable, "turn_id = ?", turns[1].ID); got != 0 {
			t.Fatalf("generic denial was skipped and healthy Turn received %d Attempts", got)
		}
	})
}

func TestClaimAttemptExecutionAuthorizationRejectsReclaimBeforeExpiringOldAttempt(t *testing.T) {
	db, store, turn, clock := newSQLExecutionFixture(t, "reservation_reclaim_reject")
	authority := &reservationExecutionTestAuthority{}
	store.WithSettlementAuthority(authority)
	first, err := store.ClaimAttempt(
		context.Background(), executionClaimCommand(turn.ID, "attempt_reservation_reclaim_old"),
	)
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(first.Attempt.LeaseExpiresAt)

	var statusAtAuthorization string
	var finishedAtAuthorization bool
	authority.setCheck(func(tx *gorm.DB, _ Turn) error {
		var row struct {
			Status     string     `gorm:"column:status"`
			FinishedAt *time.Time `gorm:"column:finished_at"`
		}
		if err := tx.Table(SQLTurnAttemptTable).
			Select("status", "finished_at").
			Where("attempt_id = ?", first.Attempt.ID).Take(&row).Error; err != nil {
			return err
		}
		statusAtAuthorization = row.Status
		finishedAtAuthorization = row.FinishedAt != nil
		return nil
	})
	authority.setFailure(errors.New("private-expired-reservation"))

	_, err = store.ClaimAttempt(
		context.Background(), executionClaimCommand(turn.ID, "attempt_reservation_reclaim_new"),
	)
	if !errors.Is(err, ErrTurnReservationExecutionUnauthorized) {
		t.Fatalf("rejected reclaim error = %v, want ErrTurnReservationExecutionUnauthorized", err)
	}
	if statusAtAuthorization != string(AttemptStatusRunning) || finishedAtAuthorization {
		t.Fatalf("old Attempt at authorization = status %q finished=%v, want running/false",
			statusAtAuthorization, finishedAtAuthorization)
	}
	if authority.calls.Load() != 2 {
		t.Fatalf("execution authorization calls = %d, want initial + reclaim", authority.calls.Load())
	}

	var old executionTestAttemptState
	if err := db.Table(SQLTurnAttemptTable).
		Select("attempt_id", "status", "fencing_token", "last_heartbeat_at", "lease_expires_at", "finished_at").
		Where("attempt_id = ?", first.Attempt.ID).Take(&old).Error; err != nil {
		t.Fatal(err)
	}
	if old.Status != string(AttemptStatusRunning) || old.FinishedAt != nil || old.FencingToken != 1 {
		t.Fatalf("rejected reclaim mutated old Attempt: %+v", old)
	}
	var state executionTestTurnState
	executionTakeTurnState(t, db, turn.ID, &state)
	if state.Status != string(agentv1.TurnStatusRunning) || state.ActiveAttemptID == nil ||
		*state.ActiveAttemptID != first.Attempt.ID || state.FencingToken != 1 || state.LastEventSequence != 2 {
		t.Fatalf("rejected reclaim mutated Turn: %+v", state)
	}
	if got := executionTableCount(t, db, SQLTurnAttemptTable, "turn_id = ?", turn.ID); got != 1 {
		t.Fatalf("rejected reclaim left %d Attempts, want only old Attempt", got)
	}
}

func TestClaimAttemptExactReplayDoesNotReauthorizeExecution(t *testing.T) {
	_, store, turn, _ := newSQLExecutionFixture(t, "reservation_claim_replay")
	authority := &reservationExecutionTestAuthority{}
	store.WithSettlementAuthority(authority)
	command := executionClaimCommand(turn.ID, "attempt_reservation_claim_replay")
	first, err := store.ClaimAttempt(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	authority.setFailure(errors.New("must-not-run-on-replay"))

	replayed, err := store.ClaimAttempt(context.Background(), command)
	if err != nil || !replayed.Replay || replayed.Claimed || replayed.Attempt.ID != first.Attempt.ID {
		t.Fatalf("exact ClaimAttempt replay = %+v, %v", replayed, err)
	}
	if authority.calls.Load() != 1 {
		t.Fatalf("exact replay reauthorized execution; calls = %d, want 1", authority.calls.Load())
	}
}

func TestClaimAttemptLegacyOrNilSettlementAuthorityKeepsCompatibility(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		_, store, turn, _ := newSQLExecutionFixture(t, "reservation_compat_nil")
		if _, err := store.ClaimAttempt(
			context.Background(), executionClaimCommand(turn.ID, "attempt_reservation_compat_nil"),
		); err != nil {
			t.Fatalf("nil-authority ClaimAttempt(): %v", err)
		}
	})
	t.Run("legacy", func(t *testing.T) {
		_, store, turn, _ := newSQLExecutionFixture(t, "reservation_compat_legacy")
		store.WithSettlementAuthority(reservationExecutionLegacySettlementAuthority{})
		if _, err := store.ClaimAttempt(
			context.Background(), executionClaimCommand(turn.ID, "attempt_reservation_compat_legacy"),
		); err != nil {
			t.Fatalf("legacy-authority ClaimAttempt(): %v", err)
		}
	})
}
