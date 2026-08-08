package agentturn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"gorm.io/gorm"

	agentv1 "server/contracts/agent/v1"
)

type recordedSettlement struct {
	command SettlementCommand
}

type testSettlementAuthority struct {
	mu       sync.Mutex
	calls    []recordedSettlement
	fail     error
	seenKeys map[string]int
}

func newTestSettlementAuthority() *testSettlementAuthority {
	return &testSettlementAuthority{seenKeys: map[string]int{}}
}

func (authority *testSettlementAuthority) Settle(tx *gorm.DB, command SettlementCommand) error {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if tx == nil {
		return errors.New("settlement was called without the caller transaction")
	}
	authority.calls = append(authority.calls, recordedSettlement{command: command})
	authority.seenKeys[command.SettlementKey]++
	return authority.fail
}

func (authority *testSettlementAuthority) committed() []SettlementCommand {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	commands := make([]SettlementCommand, 0, len(authority.calls))
	for _, call := range authority.calls {
		commands = append(commands, call.command)
	}
	return commands
}

func (authority *testSettlementAuthority) keyCount(key string) int {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	return authority.seenKeys[key]
}

func TestSettlementRidesTheTerminalCommitExactlyOnce(t *testing.T) {
	db, store, _, turns := newSQLClaimNextFixture(t, "settle_once")
	turn := turns[0]
	authority := newTestSettlementAuthority()
	store.WithSettlementAuthority(authority)

	claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_settle"))
	if err != nil {
		t.Fatal(err)
	}
	// A non-terminal Operation is not a commercial event.
	if _, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_mid",
		Event: &EventDraft{Type: "writer.document.delta", Data: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatal(err)
	}
	if len(authority.committed()) != 0 {
		t.Fatalf("a non-terminal Operation settled: %+v", authority.committed())
	}

	terminal := CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_terminal",
		TerminalStatus: agentv1.TurnStatusCompleted,
		Settlement:     &SettlementRequest{UsedUnits: 7},
	}
	if _, err := store.CommitAttempt(context.Background(), terminal); err != nil {
		t.Fatal(err)
	}
	commands := authority.committed()
	if len(commands) != 1 {
		t.Fatalf("settlement calls = %d, want 1: %+v", len(commands), commands)
	}
	settled := commands[0]
	wantKey := settlementKey(turn.ID, "operation_terminal")
	if settled.SettlementKey != wantKey || settled.Intent != SettlementIntentFinalize ||
		settled.UsedUnits != 7 || settled.TerminalStatus != agentv1.TurnStatusCompleted ||
		settled.TurnID != turn.ID || settled.PrincipalID != turn.PrincipalID {
		t.Fatalf("settlement command = %+v", settled)
	}

	// A retried terminal commit resolves from the immutable Operation receipt
	// and must never reach settlement a second time.
	replay, err := store.CommitAttempt(context.Background(), terminal)
	if err != nil || !replay.Replay {
		t.Fatalf("retried terminal commit = %+v, %v", replay, err)
	}
	if got := authority.keyCount(wantKey); got != 1 {
		t.Fatalf("settlement key was presented %d times, want exactly 1", got)
	}
	conflict := terminal
	conflict.Settlement = &SettlementRequest{UsedUnits: 8}
	if _, err := store.CommitAttempt(context.Background(), conflict); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("changed settlement replay error = %v, want ErrOperationConflict", err)
	}
	conflict.Settlement = &SettlementRequest{Intent: SettlementIntentRelease}
	if _, err := store.CommitAttempt(context.Background(), conflict); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("changed settlement intent replay error = %v, want ErrOperationConflict", err)
	}
	if got := authority.keyCount(wantKey); got != 1 {
		t.Fatalf("conflicting settlement reached authority %d times, want 1", got)
	}
	var state executionTestTurnState
	executionTakeTurnState(t, db, turn.ID, &state)
	if state.Status != string(agentv1.TurnStatusCompleted) {
		t.Fatalf("turn state = %+v", state)
	}
}

func TestSettlementFailureRollsBackTheWholeTerminalCommit(t *testing.T) {
	db, store, _, turns := newSQLClaimNextFixture(t, "settle_rollback")
	turn := turns[0]
	authority := newTestSettlementAuthority()
	authority.fail = errors.New("ledger refused with SECRET_PROVIDER_DSN")
	store.WithSettlementAuthority(authority)

	claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_rollback"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_unbillable",
		TerminalStatus: agentv1.TurnStatusCompleted,
		Settlement:     &SettlementRequest{UsedUnits: 3},
	})
	if !errors.Is(err, ErrSettlementFailed) {
		t.Fatalf("CommitAttempt() error = %v, want ErrSettlementFailed", err)
	}
	if strings.Contains(err.Error(), "SECRET_PROVIDER_DSN") || strings.Contains(err.Error(), "ledger refused") {
		t.Fatalf("CommitAttempt() exposed raw Settlement Authority error: %q", err)
	}
	// A Turn that could not be billed must not be recorded as finished, and
	// must leave no receipt claiming it was.
	var state executionTestTurnState
	executionTakeTurnState(t, db, turn.ID, &state)
	if state.Status != string(agentv1.TurnStatusRunning) || state.ActiveAttemptID == nil {
		t.Fatalf("failed settlement left the turn at %+v, want it still running", state)
	}
	if got := executionTableCount(t, db, SQLTurnOperationTable, "turn_id = ?", turn.ID); got != 0 {
		t.Fatalf("failed settlement left %d operation receipts", got)
	}

	// Once the ledger recovers the same commit succeeds.
	authority.mu.Lock()
	authority.fail = nil
	authority.mu.Unlock()
	if _, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_unbillable",
		TerminalStatus: agentv1.TurnStatusCompleted,
		Settlement:     &SettlementRequest{UsedUnits: 3},
	}); err != nil {
		t.Fatalf("retry after ledger recovery: %v", err)
	}
	executionTakeTurnState(t, db, turn.ID, &state)
	if state.Status != string(agentv1.TurnStatusCompleted) {
		t.Fatalf("recovered turn state = %+v", state)
	}
}

func TestSettlementDefaultsChargeOnlyACompletedTurn(t *testing.T) {
	for name, tc := range map[string]struct {
		terminal agentv1.TurnStatus
		want     SettlementIntent
	}{
		"completed charges":  {agentv1.TurnStatusCompleted, SettlementIntentFinalize},
		"failed releases":    {agentv1.TurnStatusFailed, SettlementIntentRelease},
		"timeout releases":   {agentv1.TurnStatusTimeout, SettlementIntentRelease},
		"cancelled releases": {agentv1.TurnStatusStopped, SettlementIntentRelease},
	} {
		if got := DefaultSettlementIntent(tc.terminal); got != tc.want {
			t.Fatalf("%s: DefaultSettlementIntent(%q) = %q, want %q", name, tc.terminal, got, tc.want)
		}
	}

	// A failed Turn releases even when the executor reported used units, and
	// the released command carries no units at all.
	_, store, _, turns := newSQLClaimNextFixture(t, "settle_failed")
	turn := turns[0]
	authority := newTestSettlementAuthority()
	store.WithSettlementAuthority(authority)
	claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_failed"))
	if err != nil {
		t.Fatal(err)
	}
	command := CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_failed",
		TerminalStatus: agentv1.TurnStatusFailed,
		Settlement:     &SettlementRequest{UsedUnits: 99},
	}
	if _, err := store.CommitAttempt(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	commands := authority.committed()
	if len(commands) != 1 || commands[0].Intent != SettlementIntentRelease || commands[0].UsedUnits != 0 {
		t.Fatalf("failed-turn settlement = %+v, want a release carrying no units", commands)
	}
	// UsedUnits is not part of release semantics. A retry that only changes the
	// ignored value resolves the same normalized Operation receipt.
	command.Settlement = &SettlementRequest{UsedUnits: 1}
	if replay, err := store.CommitAttempt(context.Background(), command); err != nil || !replay.Replay {
		t.Fatalf("normalized release replay = %+v, %v", replay, err)
	}
	if got := len(authority.committed()); got != 1 {
		t.Fatalf("normalized release reached authority %d times, want 1", got)
	}
}

func TestSettlementHonoursAnExplicitIntentOverride(t *testing.T) {
	// A cancelled Turn that already produced billable output is a different
	// commercial event from a crash, so the domain may override the default.
	_, store, clock, turns := newSQLClaimNextFixture(t, "settle_override")
	turn := turns[0]
	authority := newTestSettlementAuthority()
	store.WithSettlementAuthority(authority)

	claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_override"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RequestCancel(context.Background(), turn.PrincipalID, turn.ThreadID, turn.ID,
		clock.Get(), sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"cancellationRequested":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_partial",
		TerminalStatus: agentv1.TurnStatusStopped,
		Settlement:     &SettlementRequest{Intent: SettlementIntentFinalize, UsedUnits: 4},
	}); err != nil {
		t.Fatal(err)
	}
	commands := authority.committed()
	if len(commands) != 1 || commands[0].Intent != SettlementIntentFinalize || commands[0].UsedUnits != 4 {
		t.Fatalf("override settlement = %+v, want a finalize of 4 units", commands)
	}
}

func TestSettlementReleaseFailsClosedAfterDurableWork(t *testing.T) {
	for name, tc := range map[string]struct {
		explicitRelease bool
		withEffect      bool
	}{
		"default release after operation": {},
		"explicit release after effect":   {explicitRelease: true, withEffect: true},
	} {
		t.Run(name, func(t *testing.T) {
			db, store, clock, turns := newSQLClaimNextFixture(t, "release_guard_"+map[bool]string{false: "operation", true: "effect"}[tc.withEffect])
			turn := turns[0]
			authority := newTestSettlementAuthority()
			store.WithSettlementAuthority(authority)

			claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_release_guard"))
			if err != nil {
				t.Fatal(err)
			}
			operation := CommitAttemptCommand{
				Fence: claimed.Attempt.Fence(), OperationID: "operation_partial_output",
				Event: &EventDraft{Type: "writer.document.delta", Data: json.RawMessage(`{"partial":true}`)},
			}
			if tc.withEffect {
				operation.Effects = []EffectOutboxDraft{
					executionTestEffect("outbox_partial_output", "writer.document.publish", "partial-output", clock.Get()),
				}
			}
			if _, err := store.CommitAttempt(context.Background(), operation); err != nil {
				t.Fatal(err)
			}

			var before executionTestTurnState
			executionTakeTurnState(t, db, turn.ID, &before)
			beforeEvents := executionTableCount(t, db, SQLTurnEventTable, "turn_id = ?", turn.ID)
			terminal := CommitAttemptCommand{
				Fence: claimed.Attempt.Fence(), OperationID: "operation_unsafe_release",
				TerminalStatus: agentv1.TurnStatusTimeout,
			}
			if tc.explicitRelease {
				terminal.Settlement = &SettlementRequest{Intent: SettlementIntentRelease}
			}
			if _, err := store.CommitAttempt(context.Background(), terminal); !errors.Is(err, ErrSettlementUsageUnknown) {
				t.Fatalf("CommitAttempt() error = %v, want ErrSettlementUsageUnknown", err)
			}

			var after executionTestTurnState
			executionTakeTurnState(t, db, turn.ID, &after)
			assertSettlementGuardTurnUnchanged(t, before, after)
			if got := executionTableCount(t, db, SQLTurnEventTable, "turn_id = ?", turn.ID); got != beforeEvents {
				t.Fatalf("event count = %d, want unchanged %d", got, beforeEvents)
			}
			if got := executionTableCount(t, db, SQLTurnOperationTable, "turn_id = ?", turn.ID); got != 1 {
				t.Fatalf("operation count = %d, want only the prior receipt", got)
			}
			wantEffects := int64(0)
			if tc.withEffect {
				wantEffects = 1
			}
			if got := executionTableCount(t, db, SQLEffectOutboxTable, "turn_id = ?", turn.ID); got != wantEffects {
				t.Fatalf("effect count = %d, want %d", got, wantEffects)
			}
			if calls := authority.committed(); len(calls) != 0 {
				t.Fatalf("ambiguous release reached settlement authority: %+v", calls)
			}
		})
	}
}

func TestSettlementReleaseFailsClosedForTerminalEffects(t *testing.T) {
	db, store, clock, turns := newSQLClaimNextFixture(t, "release_guard_terminal_effect")
	turn := turns[0]
	authority := newTestSettlementAuthority()
	store.WithSettlementAuthority(authority)
	claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_terminal_effect"))
	if err != nil {
		t.Fatal(err)
	}
	var before executionTestTurnState
	executionTakeTurnState(t, db, turn.ID, &before)
	_, err = store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_terminal_effect",
		TerminalStatus: agentv1.TurnStatusFailed,
		Effects: []EffectOutboxDraft{
			executionTestEffect("outbox_terminal_effect", "writer.document.publish", "terminal-effect", clock.Get()),
		},
	})
	if !errors.Is(err, ErrSettlementUsageUnknown) {
		t.Fatalf("CommitAttempt() error = %v, want ErrSettlementUsageUnknown", err)
	}
	var after executionTestTurnState
	executionTakeTurnState(t, db, turn.ID, &after)
	assertSettlementGuardTurnUnchanged(t, before, after)
	if got := executionTableCount(t, db, SQLTurnOperationTable, "turn_id = ?", turn.ID); got != 0 {
		t.Fatalf("terminal release left %d operation receipts", got)
	}
	if got := executionTableCount(t, db, SQLEffectOutboxTable, "turn_id = ?", turn.ID); got != 0 {
		t.Fatalf("terminal release left %d effects", got)
	}
	if calls := authority.committed(); len(calls) != 0 {
		t.Fatalf("terminal effects reached settlement authority: %+v", calls)
	}
}

func TestSettlementReleaseSerializesWithConcurrentOperation(t *testing.T) {
	db, store, _, turns := newSQLClaimNextFixture(t, "release_guard_race")
	turn := turns[0]
	authority := newTestSettlementAuthority()
	store.WithSettlementAuthority(authority)
	claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_release_guard_race"))
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	go func() {
		<-start
		_, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
			Fence: claimed.Attempt.Fence(), OperationID: "operation_racing_output",
			Event: &EventDraft{Type: "writer.document.delta", Data: json.RawMessage(`{"race":true}`)},
		})
		errs <- err
	}()
	go func() {
		<-start
		_, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
			Fence: claimed.Attempt.Fence(), OperationID: "operation_racing_timeout",
			TerminalStatus: agentv1.TurnStatusTimeout,
		})
		errs <- err
	}()
	close(start)
	first, second := <-errs, <-errs

	var state executionTestTurnState
	executionTakeTurnState(t, db, turn.ID, &state)
	calls := authority.committed()
	switch {
	case first == nil || second == nil:
		other := first
		if first == nil {
			other = second
		}
		switch {
		case state.Status == string(agentv1.TurnStatusTimeout):
			if !errors.Is(other, ErrAttemptFenced) || len(calls) != 1 {
				t.Fatalf("terminal-first outcome: errors=(%v, %v), settlement=%+v", first, second, calls)
			}
		case state.Status == string(agentv1.TurnStatusRunning):
			if !errors.Is(other, ErrSettlementUsageUnknown) || len(calls) != 0 {
				t.Fatalf("operation-first outcome: errors=(%v, %v), settlement=%+v", first, second, calls)
			}
		default:
			t.Fatalf("serialized outcome left status %q", state.Status)
		}
	default:
		t.Fatalf("neither serialized operation succeeded: errors=(%v, %v)", first, second)
	}
	if state.Status == string(agentv1.TurnStatusTimeout) &&
		executionTableCount(t, db, SQLTurnOperationTable, "turn_id = ?", turn.ID) != 1 {
		t.Fatal("terminal-first outcome persisted unexpected partial output")
	}
}

func TestReconciledTurnAlwaysReleasesItsReservation(t *testing.T) {
	_, store, clock, turns := newSQLClaimNextFixture(t, "settle_reconcile")
	turn := turns[0]
	authority := newTestSettlementAuthority()
	store.WithSettlementAuthority(authority)

	if _, err := store.RequestCancel(context.Background(), turn.PrincipalID, turn.ThreadID, turn.ID,
		clock.Get(), sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"cancellationRequested":true}`)); err != nil {
		t.Fatal(err)
	}
	result, err := store.ReconcileTerminal(context.Background(), ReconcileCommand{
		TurnID: turn.ID, Reason: ReclaimReasonCancellationPending,
		ReconcilerID: "reconciler_settle", ReconcilerBuildDigest: "sha256:reconciler-settle",
	})
	if err != nil || !result.Changed {
		t.Fatalf("ReconcileTerminal() = %+v, %v", result, err)
	}
	commands := authority.committed()
	if len(commands) != 1 || commands[0].Intent != SettlementIntentRelease ||
		commands[0].TerminalStatus != agentv1.TurnStatusStopped || commands[0].UsedUnits != 0 {
		t.Fatalf("reconcile settlement = %+v, want a release", commands)
	}
	// The key is the retiring fence, not an Operation receipt: nothing ran.
	// The Turn had no epoch, so retirement advances the fence 0 -> 1.
	if commands[0].SettlementKey != reconcileSettlementKey(turn.ID, 1) {
		t.Fatalf("reconcile settlement key = %q, want the retiring-fence key", commands[0].SettlementKey)
	}

	// A repeated pass finds the Turn terminal and settles nothing further.
	if _, err := store.ReconcileTerminal(context.Background(), ReconcileCommand{
		TurnID: turn.ID, Reason: ReclaimReasonCancellationPending,
		ReconcilerID: "reconciler_settle", ReconcilerBuildDigest: "sha256:reconciler-settle",
	}); err != nil {
		t.Fatal(err)
	}
	if len(authority.committed()) != 1 {
		t.Fatalf("a repeated retirement settled again: %+v", authority.committed())
	}
}

func TestReconcileReleaseFailsClosedAfterDurableWork(t *testing.T) {
	db, store, clock, turns := newSQLClaimNextFixture(t, "release_guard_reconcile")
	turn := turns[0]
	authority := newTestSettlementAuthority()
	store.WithSettlementAuthority(authority)

	first, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_reconcile_partial_a"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence: first.Attempt.Fence(), OperationID: "operation_reconcile_partial",
		Event: &EventDraft{Type: "writer.document.delta", Data: json.RawMessage(`{"partial":true}`)},
		Effects: []EffectOutboxDraft{
			executionTestEffect("outbox_reconcile_partial", "writer.document.publish", "reconcile-partial", clock.Get()),
		},
	}); err != nil {
		t.Fatal(err)
	}
	clock.Set(first.Attempt.LeaseExpiresAt)
	var last = first
	for index := 1; index < DefaultMaxTurnAttempts; index++ {
		last, err = store.ClaimAttempt(context.Background(), executionClaimCommand(
			turn.ID, fmt.Sprintf("attempt_reconcile_partial_%c", 'a'+index)))
		if err != nil {
			t.Fatal(err)
		}
		clock.Set(last.Attempt.LeaseExpiresAt)
	}

	var before executionTestTurnState
	executionTakeTurnState(t, db, turn.ID, &before)
	beforeEvents := executionTableCount(t, db, SQLTurnEventTable, "turn_id = ?", turn.ID)
	_, err = store.ReconcileTerminal(context.Background(), ReconcileCommand{
		TurnID: turn.ID, Reason: ReclaimReasonAttemptsExhausted,
		ReconcilerID: "reconciler_release_guard", ReconcilerBuildDigest: "sha256:reconciler-release-guard",
	})
	if !errors.Is(err, ErrSettlementUsageUnknown) {
		t.Fatalf("ReconcileTerminal() error = %v, want ErrSettlementUsageUnknown", err)
	}
	var after executionTestTurnState
	executionTakeTurnState(t, db, turn.ID, &after)
	assertSettlementGuardTurnUnchanged(t, before, after)
	if got := executionTableCount(t, db, SQLTurnEventTable, "turn_id = ?", turn.ID); got != beforeEvents {
		t.Fatalf("event count = %d, want unchanged %d", got, beforeEvents)
	}
	var attempt executionTestAttemptState
	if err := db.Table(SQLTurnAttemptTable).
		Select("attempt_id", "status", "fencing_token", "last_heartbeat_at", "lease_expires_at", "finished_at").
		Where("attempt_id = ?", last.Attempt.ID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.Status != string(AttemptStatusRunning) || attempt.FinishedAt != nil {
		t.Fatalf("reconcile guard mutated the expired attempt: %+v", attempt)
	}
	if calls := authority.committed(); len(calls) != 0 {
		t.Fatalf("ambiguous reconcile reached settlement authority: %+v", calls)
	}
}

func assertSettlementGuardTurnUnchanged(t *testing.T, before, after executionTestTurnState) {
	t.Helper()
	beforeAttempt, afterAttempt := "", ""
	if before.ActiveAttemptID != nil {
		beforeAttempt = *before.ActiveAttemptID
	}
	if after.ActiveAttemptID != nil {
		afterAttempt = *after.ActiveAttemptID
	}
	if before.Status != after.Status || beforeAttempt != afterAttempt ||
		before.FencingToken != after.FencingToken || before.LastEventSequence != after.LastEventSequence ||
		(before.FinishedAt == nil) != (after.FinishedAt == nil) {
		t.Fatalf("settlement guard mutated Turn: before=%+v after=%+v", before, after)
	}
}

func TestNoSettlementAuthorityLeavesEveryPathUnchanged(t *testing.T) {
	// The default is nil on purpose: composing the store must not silently
	// start moving money.
	db, store, _, turns := newSQLClaimNextFixture(t, "settle_absent")
	turn := turns[0]
	if store.hasSettlementAuthority() {
		t.Fatal("a freshly constructed store already had a settlement authority")
	}
	claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_absent"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_absent_partial",
		Event: &EventDraft{Type: "writer.document.delta", Data: json.RawMessage(`{"partial":true}`)},
	}); err != nil {
		t.Fatalf("partial commit without an authority: %v", err)
	}
	if _, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_absent",
		TerminalStatus: agentv1.TurnStatusFailed,
		Settlement:     &SettlementRequest{UsedUnits: 5},
	}); err != nil {
		t.Fatalf("terminal commit without an authority: %v", err)
	}
	var state executionTestTurnState
	executionTakeTurnState(t, db, turn.ID, &state)
	if state.Status != string(agentv1.TurnStatusFailed) {
		t.Fatalf("turn state = %+v", state)
	}
}

func TestSettlementCommandValidationIsFailClosed(t *testing.T) {
	base := SettlementCommand{
		TurnID: "turn_x", PrincipalID: "principal_x",
		AuthorizationKind: SettlementAuthorizationOperation, AttemptID: "attempt_x",
		FencingToken: 1, OperationID: "operation_x",
		Intent: SettlementIntentFinalize, TerminalStatus: agentv1.TurnStatusCompleted,
	}
	base.SettlementKey = settlementKey(base.TurnID, base.OperationID)
	if err := base.Validate(); err != nil {
		t.Fatalf("valid command rejected: %v", err)
	}
	for name, mutate := range map[string]func(*SettlementCommand){
		"non-terminal status":   func(c *SettlementCommand) { c.TerminalStatus = agentv1.TurnStatusRunning },
		"unknown intent":        func(c *SettlementCommand) { c.Intent = "invented" },
		"negative units":        func(c *SettlementCommand) { c.UsedUnits = -1 },
		"released with units":   func(c *SettlementCommand) { c.Intent = SettlementIntentRelease; c.UsedUnits = 1 },
		"missing key":           func(c *SettlementCommand) { c.SettlementKey = "" },
		"missing principal":     func(c *SettlementCommand) { c.PrincipalID = "" },
		"missing turn":          func(c *SettlementCommand) { c.TurnID = "" },
		"missing authorization": func(c *SettlementCommand) { c.AuthorizationKind = "" },
		"missing fence":         func(c *SettlementCommand) { c.FencingToken = 0 },
		"wrong operation key":   func(c *SettlementCommand) { c.SettlementKey = settlementKey(c.TurnID, "other") },
	} {
		command := base
		mutate(&command)
		if err := command.Validate(); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	// A settlement request on a non-terminal commit is rejected at the command
	// boundary rather than silently ignored.
	if err := (CommitAttemptCommand{
		Fence:       AttemptFence{TurnID: "t", AttemptID: "a", FencingToken: 1, WorkerID: "w", WorkerBuildDigest: "d"},
		OperationID: "op",
		Event:       &EventDraft{Type: "writer.x", Data: json.RawMessage(`{}`)},
		Settlement:  &SettlementRequest{UsedUnits: 1},
	}).Validate(); err == nil {
		t.Fatal("a settlement on a non-terminal commit was accepted")
	}
}
