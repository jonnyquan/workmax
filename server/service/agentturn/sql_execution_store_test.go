package agentturn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	agentv1 "server/contracts/agent/v1"
	"server/utils/testutil"
)

func TestSQLExecutionStoreConcurrentClaimHasSingleWinner(t *testing.T) {
	db, store, turn, _ := newSQLExecutionFixture(t, "claim_race")

	const candidates = 32
	type outcome struct {
		result ClaimAttemptResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, candidates)
	var wait sync.WaitGroup
	for index := 0; index < candidates; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			result, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, fmt.Sprintf("attempt_race_%02d", index)))
			outcomes <- outcome{result: result, err: err}
		}(index)
	}
	close(start)
	wait.Wait()
	close(outcomes)

	var winners []ClaimAttemptResult
	busy := 0
	for outcome := range outcomes {
		switch {
		case outcome.err == nil:
			winners = append(winners, outcome.result)
		case errors.Is(outcome.err, ErrAttemptBusy):
			busy++
		default:
			t.Errorf("ClaimAttempt() error = %v, want ErrAttemptBusy", outcome.err)
		}
	}
	if len(winners) != 1 || busy != candidates-1 {
		t.Fatalf("claim outcomes: winners=%d busy=%d, want 1 and %d", len(winners), busy, candidates-1)
	}
	winner := winners[0]
	if winner.Replay || winner.Turn.Status != agentv1.TurnStatusRunning || winner.Attempt.Status != AttemptStatusRunning || winner.Attempt.FencingToken != 1 {
		t.Fatalf("winning claim = %+v", winner)
	}

	var state executionTestTurnState
	executionTakeTurnState(t, db, turn.ID, &state)
	if state.Status != string(agentv1.TurnStatusRunning) || state.ActiveAttemptID == nil || *state.ActiveAttemptID != winner.Attempt.ID || state.FencingToken != 1 || state.LastEventSequence != 2 {
		t.Fatalf("persisted turn state = %+v", state)
	}
	if got := executionTableCount(t, db, "w_agent_turn_attempt", "turn_id = ?", turn.ID); got != 1 {
		t.Fatalf("attempt count = %d, want 1", got)
	}
	if got := executionTableCount(t, db, SQLTurnEventTable, "turn_id = ?", turn.ID); got != 2 {
		t.Fatalf("event count = %d, want queued + running", got)
	}
}

func TestSQLExecutionStoreExpiredAttemptIsReclaimedAndOldFenceRejected(t *testing.T) {
	db, store, turn, clock := newSQLExecutionFixture(t, "reclaim")
	first, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_reclaim_old"))
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(first.Attempt.LeaseExpiresAt)

	second, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_reclaim_new"))
	if err != nil {
		t.Fatalf("reclaim ClaimAttempt(): %v", err)
	}
	if second.Replay || second.Attempt.FencingToken != 2 || second.Attempt.ID != "attempt_reclaim_new" || second.Attempt.Status != AttemptStatusRunning {
		t.Fatalf("reclaimed attempt = %+v", second)
	}

	var expired executionTestAttemptState
	if err := db.Table("w_agent_turn_attempt").
		Select("attempt_id", "status", "fencing_token", "finished_at", "lease_expires_at").
		Where("attempt_id = ?", first.Attempt.ID).Take(&expired).Error; err != nil {
		t.Fatal(err)
	}
	if expired.Status != string(AttemptStatusExpired) || expired.FencingToken != 1 || expired.FinishedAt == nil {
		t.Fatalf("expired attempt row = %+v", expired)
	}
	var state executionTestTurnState
	executionTakeTurnState(t, db, turn.ID, &state)
	if state.ActiveAttemptID == nil || *state.ActiveAttemptID != second.Attempt.ID || state.FencingToken != 2 || state.LastEventSequence != 2 {
		t.Fatalf("reclaimed turn state = %+v", state)
	}

	if _, err := store.HeartbeatAttempt(context.Background(), HeartbeatAttemptCommand{Fence: first.Attempt.Fence()}); !errors.Is(err, ErrAttemptFenced) {
		t.Fatalf("old-fence HeartbeatAttempt() error = %v, want ErrAttemptFenced", err)
	}
	_, err = store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence:       first.Attempt.Fence(),
		OperationID: "operation_reclaim_old",
		Event:       &EventDraft{Type: "writer.reclaim.stale", Data: json.RawMessage(`{"stale":true}`)},
	})
	if !errors.Is(err, ErrAttemptFenced) {
		t.Fatalf("old-fence CommitAttempt() error = %v, want ErrAttemptFenced", err)
	}
	if got := executionTableCount(t, db, "w_agent_turn_operation", "turn_id = ?", turn.ID); got != 0 {
		t.Fatalf("stale commit persisted %d operations", got)
	}
}

func TestSQLExecutionStoreHeartbeatRejectsLeaseExpiryEquality(t *testing.T) {
	db, store, turn, clock := newSQLExecutionFixture(t, "heartbeat_expiry")
	claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_heartbeat_expiry"))
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(claimed.Attempt.LeaseExpiresAt)

	if _, err := store.HeartbeatAttempt(context.Background(), HeartbeatAttemptCommand{Fence: claimed.Attempt.Fence()}); !errors.Is(err, ErrAttemptLeaseExpired) {
		t.Fatalf("HeartbeatAttempt() at lease equality error = %v, want ErrAttemptLeaseExpired", err)
	}
	var attempt executionTestAttemptState
	if err := db.Table("w_agent_turn_attempt").
		Select("attempt_id", "status", "fencing_token", "last_heartbeat_at", "lease_expires_at").
		Where("attempt_id = ?", claimed.Attempt.ID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.Status != string(AttemptStatusRunning) || !attempt.LastHeartbeatAt.Equal(claimed.Attempt.LastHeartbeatAt) || !attempt.LeaseExpiresAt.Equal(claimed.Attempt.LeaseExpiresAt) {
		t.Fatalf("expired heartbeat mutated attempt: got %+v, claimed %+v", attempt, claimed.Attempt)
	}
}

func TestSQLExecutionStoreOperationReplayIsCompleteAndDigestBound(t *testing.T) {
	db, store, turn, clock := newSQLExecutionFixture(t, "operation_replay")
	claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_operation_replay"))
	if err != nil {
		t.Fatal(err)
	}
	command := CommitAttemptCommand{
		Fence:       claimed.Attempt.Fence(),
		OperationID: "operation_replay",
		Event: &EventDraft{
			Type:         "writer.document.delta",
			ResourceRefs: []string{"wm:writer:document:doc_replay@2"},
			Data:         json.RawMessage(`{"patch":"first"}`),
		},
		Effects: []EffectOutboxDraft{
			executionTestEffect("outbox_replay_0", "writer.document.index", "dedupe_replay_0", clock.Get().Add(time.Minute)),
			executionTestEffect("outbox_replay_1", "writer.document.audit", "dedupe_replay_1", clock.Get().Add(2*time.Minute)),
		},
	}

	first, err := store.CommitAttempt(context.Background(), command)
	if err != nil {
		t.Fatalf("first CommitAttempt(): %v", err)
	}
	if first.Replay || first.OperationDigest == "" || first.Event.Sequence != 3 || first.TurnStatus != agentv1.TurnStatusRunning || len(first.Effects) != len(command.Effects) {
		t.Fatalf("first commit = %+v", first)
	}

	// Receipt lookup precedes lease validation: a caller resolving an unknown
	// outcome must still recover the committed result after its lease expires.
	clock.Set(claimed.Attempt.LeaseExpiresAt)
	replay, err := store.CommitAttempt(context.Background(), command)
	if err != nil {
		t.Fatalf("replay CommitAttempt() after lease expiry: %v", err)
	}
	if !replay.Replay || replay.OperationDigest != first.OperationDigest || replay.Event.EventID != first.Event.EventID || len(replay.Effects) != len(first.Effects) {
		t.Fatalf("operation replay = %+v, first = %+v", replay, first)
	}
	for index := range replay.Effects {
		if replay.Effects[index].OutboxID != first.Effects[index].OutboxID || replay.Effects[index].Ordinal != index {
			t.Fatalf("replayed effects[%d] = %+v, first = %+v", index, replay.Effects[index], first.Effects[index])
		}
	}

	conflict := command
	conflict.Effects = append([]EffectOutboxDraft(nil), command.Effects...)
	conflict.Effects[0].AvailableAt = conflict.Effects[0].AvailableAt.Add(time.Microsecond)
	if _, err := store.CommitAttempt(context.Background(), conflict); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("different-digest CommitAttempt() error = %v, want ErrOperationConflict", err)
	}
	if got := executionTableCount(t, db, "w_agent_turn_operation", "turn_id = ?", turn.ID); got != 1 {
		t.Fatalf("operation count = %d, want 1", got)
	}
	if got := executionTableCount(t, db, "w_agent_effect_outbox", "turn_id = ?", turn.ID); got != int64(len(command.Effects)) {
		t.Fatalf("outbox count = %d, want %d", got, len(command.Effects))
	}
	if got := executionTableCount(t, db, SQLTurnEventTable, "turn_id = ?", turn.ID); got != 3 {
		t.Fatalf("event count = %d, want 3", got)
	}
	if err := db.Exec("DELETE FROM w_agent_effect_outbox WHERE outbox_id = ?", command.Effects[0].OutboxID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitAttempt(context.Background(), command); !errors.Is(err, ErrStoreIntegrity) {
		t.Fatalf("incomplete operation receipt error = %v, want ErrStoreIntegrity", err)
	}
}

func TestSQLExecutionStoreOperationReplayRejectsMutatedContent(t *testing.T) {
	db, store, turn, clock := newSQLExecutionFixture(t, "operation_content")
	claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_operation_content"))
	if err != nil {
		t.Fatal(err)
	}
	command := CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_content",
		Event: &EventDraft{
			Type:         "writer.document.delta",
			ResourceRefs: []string{"wm:writer:document:doc_content@2"},
			Data:         json.RawMessage(`{"n":1,"patch":"first"}`),
		},
		Effects: []EffectOutboxDraft{
			executionTestEffect("outbox_content", "writer.document.index", "dedupe_content", clock.Get()),
		},
	}
	committed, err := store.CommitAttempt(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}

	var eventRow sqlTurnEventRow
	if err := db.Where("turn_id = ? AND sequence = ?", turn.ID, committed.Event.Sequence).Take(&eventRow).Error; err != nil {
		t.Fatal(err)
	}
	var stored agentv1.EventEnvelope
	if err := json.Unmarshal(eventRow.EventJSON, &stored); err != nil {
		t.Fatal(err)
	}
	writeEvent := func(data json.RawMessage) {
		t.Helper()
		stored.Data = data
		raw, err := json.Marshal(stored)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Table(SQLTurnEventTable).Where("turn_id = ? AND sequence = ?", turn.ID, committed.Event.Sequence).
			UpdateColumn("event_json", raw).Error; err != nil {
			t.Fatal(err)
		}
	}

	// JSON formatting and object key order are storage details, not a receipt
	// conflict. A semantic change is an integrity violation.
	writeEvent(json.RawMessage(`{ "patch" : "first", "n" : 1 }`))
	if replay, err := store.CommitAttempt(context.Background(), command); err != nil || !replay.Replay {
		t.Fatalf("semantic Event replay = %+v, %v", replay, err)
	}
	writeEvent(json.RawMessage(`{"n":1,"patch":"mutated"}`))
	if _, err := store.CommitAttempt(context.Background(), command); !errors.Is(err, ErrStoreIntegrity) {
		t.Fatalf("mutated Event replay error = %v, want ErrStoreIntegrity", err)
	}
	writeEvent(command.Event.Data)

	if err := db.Table(SQLEffectOutboxTable).Where("outbox_id = ?", command.Effects[0].OutboxID).
		UpdateColumn("payload_json", []byte(`{ "test" : true }`)).Error; err != nil {
		t.Fatal(err)
	}
	if replay, err := store.CommitAttempt(context.Background(), command); err != nil || !replay.Replay {
		t.Fatalf("semantic Effect replay = %+v, %v", replay, err)
	}
	// Dispatcher lifecycle and its retry schedule are mutable after the
	// Operation commits. A real claim/retry must not invalidate the immutable
	// receipt content proof even though it advances available_at.
	deliveries, err := store.ClaimEffects(context.Background(), ClaimEffectsCommand{
		LeaseOwnerID: "dispatcher_content_retry", Limit: 1,
	})
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("ClaimEffects() = %+v, %v", deliveries, err)
	}
	retried, err := store.CompleteEffect(context.Background(), CompleteEffectCommand{
		Fence: deliveries[0].Fence,
		Report: DeliveryReport{
			Outcome: DeliveryOutcomeRetry, ErrorCode: "provider_retry",
		},
	})
	if err != nil || retried.Status != EffectStatusPending || !retried.AvailableAt.After(command.Effects[0].AvailableAt) {
		t.Fatalf("retry completion = %+v, %v", retried, err)
	}
	if replay, err := store.CommitAttempt(context.Background(), command); err != nil || !replay.Replay ||
		len(replay.Effects) != 1 || !replay.Effects[0].AvailableAt.Equal(retried.AvailableAt) {
		t.Fatalf("replay after Effect retry = %+v, %v", replay, err)
	}

	clock.Set(retried.AvailableAt)
	deliveries, err = store.ClaimEffects(context.Background(), ClaimEffectsCommand{
		LeaseOwnerID: "dispatcher_content_deliver", Limit: 1,
	})
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("second ClaimEffects() = %+v, %v", deliveries, err)
	}
	if _, err := store.CompleteEffect(context.Background(), CompleteEffectCommand{
		Fence: deliveries[0].Fence, Report: DeliveryReport{Outcome: DeliveryOutcomeDelivered},
	}); err != nil {
		t.Fatalf("delivered completion: %v", err)
	}
	if replay, err := store.CommitAttempt(context.Background(), command); err != nil || !replay.Replay {
		t.Fatalf("replay after Effect delivery = %+v, %v", replay, err)
	}
	if err := db.Table(SQLEffectOutboxTable).Where("outbox_id = ?", command.Effects[0].OutboxID).
		UpdateColumn("payload_json", []byte(`{"test":false}`)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitAttempt(context.Background(), command); !errors.Is(err, ErrStoreIntegrity) {
		t.Fatalf("mutated Effect payload replay error = %v, want ErrStoreIntegrity", err)
	}
}

func TestSQLExecutionStoreOutboxFailureRollsBackWholeCommit(t *testing.T) {
	db, store, turn, clock := newSQLExecutionFixture(t, "outbox_rollback")
	claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_outbox_rollback"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER fail_agent_effect_outbox BEFORE INSERT ON w_agent_effect_outbox
		WHEN NEW.outbox_id = 'outbox_forced_failure' BEGIN SELECT RAISE(ABORT, 'forced-execution-outbox'); END`).Error; err != nil {
		t.Fatal(err)
	}

	_, err = store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence:          claimed.Attempt.Fence(),
		OperationID:    "operation_outbox_rollback",
		TerminalStatus: agentv1.TurnStatusCompleted,
		Effects: []EffectOutboxDraft{
			executionTestEffect("outbox_forced_failure", "writer.effect.dispatch", "dedupe_forced_failure", clock.Get().Add(time.Minute)),
		},
	})
	if !errors.Is(err, ErrStoreUnavailable) || strings.Contains(err.Error(), "forced-execution-outbox") {
		t.Fatalf("CommitAttempt() error = %v, want sanitized ErrStoreUnavailable", err)
	}

	if got := executionTableCount(t, db, "w_agent_turn_operation", "turn_id = ?", turn.ID); got != 0 {
		t.Fatalf("failed commit persisted %d operations", got)
	}
	if got := executionTableCount(t, db, "w_agent_effect_outbox", "turn_id = ?", turn.ID); got != 0 {
		t.Fatalf("failed commit persisted %d outbox rows", got)
	}
	if got := executionTableCount(t, db, SQLTurnEventTable, "turn_id = ?", turn.ID); got != 2 {
		t.Fatalf("failed commit left %d events, want admission + running only", got)
	}
	var state executionTestTurnState
	executionTakeTurnState(t, db, turn.ID, &state)
	if state.Status != string(agentv1.TurnStatusRunning) || state.ActiveAttemptID == nil || *state.ActiveAttemptID != claimed.Attempt.ID || state.FencingToken != 1 || state.LastEventSequence != 2 {
		t.Fatalf("failed commit mutated turn state = %+v", state)
	}
	var attempt executionTestAttemptState
	if err := db.Table("w_agent_turn_attempt").
		Select("attempt_id", "status", "fencing_token", "finished_at").
		Where("attempt_id = ?", claimed.Attempt.ID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.Status != string(AttemptStatusRunning) || attempt.FinishedAt != nil {
		t.Fatalf("failed commit mutated attempt = %+v", attempt)
	}
}

func TestSQLExecutionStoreTerminalCommitClearsActiveAttempt(t *testing.T) {
	db, store, turn, _ := newSQLExecutionFixture(t, "terminal")
	claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_terminal"))
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence:          claimed.Attempt.Fence(),
		OperationID:    "operation_terminal",
		TerminalStatus: agentv1.TurnStatusCompleted,
	})
	if err != nil {
		t.Fatalf("terminal CommitAttempt(): %v", err)
	}
	if terminal.Replay || terminal.TurnStatus != agentv1.TurnStatusCompleted || terminal.Event.Type != agentv1.EventCoreTurnStatus || terminal.Event.Sequence != 3 || len(terminal.Effects) != 0 {
		t.Fatalf("terminal commit = %+v", terminal)
	}

	var state executionTestTurnState
	executionTakeTurnState(t, db, turn.ID, &state)
	if state.Status != string(agentv1.TurnStatusCompleted) || state.ActiveAttemptID != nil || state.FencingToken != 1 || state.LastEventSequence != 3 || state.FinishedAt == nil {
		t.Fatalf("terminal turn state = %+v", state)
	}
	var attempt executionTestAttemptState
	if err := db.Table("w_agent_turn_attempt").
		Select("attempt_id", "status", "fencing_token", "finished_at").
		Where("attempt_id = ?", claimed.Attempt.ID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.Status != string(AttemptStatusCompleted) || attempt.FinishedAt == nil {
		t.Fatalf("terminal attempt state = %+v", attempt)
	}
	if got := executionTableCount(t, db, "w_agent_turn_operation", "turn_id = ?", turn.ID); got != 1 {
		t.Fatalf("terminal operation count = %d, want 1", got)
	}
	if _, err := store.HeartbeatAttempt(context.Background(), HeartbeatAttemptCommand{Fence: claimed.Attempt.Fence()}); !errors.Is(err, ErrAttemptFenced) {
		t.Fatalf("terminal HeartbeatAttempt() error = %v, want ErrAttemptFenced", err)
	}
}

func TestSQLExecutionStoreClaimReplayConflictAndUnfencedPortsClose(t *testing.T) {
	db, store, turn, clock := newSQLExecutionFixture(t, "claim_replay")
	command := executionClaimCommand(turn.ID, "attempt_claim_replay")
	claimed, err := store.ClaimAttempt(context.Background(), command)
	if err != nil || !claimed.Claimed || claimed.Replay {
		t.Fatalf("first ClaimAttempt() = %+v, %v", claimed, err)
	}
	replayed, err := store.ClaimAttempt(context.Background(), command)
	if err != nil || !replayed.Replay || replayed.Claimed || replayed.Attempt.FencingToken != claimed.Attempt.FencingToken {
		t.Fatalf("replayed ClaimAttempt() = %+v, %v", replayed, err)
	}
	conflict := command
	conflict.WorkerID = "worker_conflicting_identity"
	if _, err := store.ClaimAttempt(context.Background(), conflict); !errors.Is(err, ErrAttemptConflict) {
		t.Fatalf("conflicting ClaimAttempt() error = %v, want ErrAttemptConflict", err)
	}
	if _, err := store.AppendEvent(context.Background(), turn.ID, sqlStoreTestDraft("writer.unfenced", `{}`)); !errors.Is(err, ErrExecutionFenceRequired) {
		t.Fatalf("unfenced AppendEvent() error = %v, want ErrExecutionFenceRequired", err)
	}
	if _, err := store.Transition(context.Background(), turn.ID, agentv1.TurnStatusCompleted, clock.Get(),
		sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"status":"completed"}`)); !errors.Is(err, ErrExecutionFenceRequired) {
		t.Fatalf("unfenced Transition() error = %v, want ErrExecutionFenceRequired", err)
	}
	if got := executionTableCount(t, db, SQLTurnEventTable, "turn_id = ?", turn.ID); got != 2 {
		t.Fatalf("replay/conflict/unfenced calls changed event count to %d", got)
	}
}

func TestSQLExecutionStoreHeartbeatRenewsAndReturnsCancellation(t *testing.T) {
	db, store, turn, clock := newSQLExecutionFixture(t, "heartbeat_cancel")
	claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_heartbeat_cancel"))
	if err != nil {
		t.Fatal(err)
	}
	heartbeatAt := claimed.Attempt.LastHeartbeatAt.Add(5 * time.Second)
	clock.Set(heartbeatAt)
	cancelled, err := store.RequestCancel(context.Background(), turn.PrincipalID, turn.ThreadID, turn.ID, heartbeatAt,
		sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"cancellationRequested":true}`))
	if err != nil || !cancelled.NewlyRequested {
		t.Fatalf("RequestCancel() = %+v, %v", cancelled, err)
	}
	heartbeat, err := store.HeartbeatAttempt(context.Background(), HeartbeatAttemptCommand{Fence: claimed.Attempt.Fence()})
	if err != nil {
		t.Fatalf("HeartbeatAttempt(): %v", err)
	}
	if heartbeat.CancelRequestedAt == nil || !heartbeat.CancelRequestedAt.Equal(heartbeatAt) ||
		!heartbeat.Attempt.LastHeartbeatAt.Equal(heartbeatAt) ||
		!heartbeat.Attempt.LeaseExpiresAt.Equal(heartbeatAt.Add(DefaultAttemptLeaseTTL)) {
		t.Fatalf("heartbeat result = %+v", heartbeat)
	}
	if got := executionTableCount(t, db, SQLTurnEventTable, "turn_id = ?", turn.ID); got != 3 {
		t.Fatalf("heartbeat appended an event; count = %d, want admission + running + cancellation", got)
	}
}

func TestSQLExecutionStoreStoppedCommitRequiresCancellation(t *testing.T) {
	db, store, turn, clock := newSQLExecutionFixture(t, "stopped")
	claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_stopped"))
	if err != nil {
		t.Fatal(err)
	}
	stop := CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_stopped",
		TerminalStatus: agentv1.TurnStatusStopped,
	}
	if _, err := store.CommitAttempt(context.Background(), stop); !errors.Is(err, ErrCancellationNotRequested) {
		t.Fatalf("CommitAttempt(stopped) error = %v, want ErrCancellationNotRequested", err)
	}
	clock.Set(clock.Get().Add(time.Second))
	if _, err := store.RequestCancel(context.Background(), turn.PrincipalID, turn.ThreadID, turn.ID, clock.Get(),
		sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"cancellationRequested":true}`)); err != nil {
		t.Fatal(err)
	}
	clock.Set(clock.Get().Add(time.Second))
	stopped, err := store.CommitAttempt(context.Background(), stop)
	if err != nil || stopped.TurnStatus != agentv1.TurnStatusStopped || stopped.Attempt.Status != AttemptStatusStopped {
		t.Fatalf("CommitAttempt(stopped) = %+v, %v", stopped, err)
	}
	var state executionTestTurnState
	executionTakeTurnState(t, db, turn.ID, &state)
	if state.Status != string(agentv1.TurnStatusStopped) || state.ActiveAttemptID != nil || state.FencingToken != 1 || state.LastEventSequence != 4 {
		t.Fatalf("stopped state = %+v", state)
	}
}

func TestSQLExecutionStoreRejectsCancelledClaimAndFenceExhaustion(t *testing.T) {
	db, store, turn, clock := newSQLExecutionFixture(t, "claim_cancelled")
	if _, err := store.RequestCancel(context.Background(), turn.PrincipalID, turn.ThreadID, turn.ID, clock.Get(),
		sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"cancellationRequested":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_cancelled")); !errors.Is(err, ErrAttemptCancelled) {
		t.Fatalf("cancelled ClaimAttempt() error = %v, want ErrAttemptCancelled", err)
	}
	if got := executionTableCount(t, db, SQLTurnAttemptTable, "turn_id = ?", turn.ID); got != 0 {
		t.Fatalf("cancelled claim persisted %d attempts", got)
	}

	db2, store2, turn2, _ := newSQLExecutionFixture(t, "fence_exhausted")
	if err := db2.Table(SQLTurnTable).Where("turn_id = ?", turn2.ID).
		UpdateColumn("fencing_token", int64(MaxDurableSequence)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store2.ClaimAttempt(context.Background(), executionClaimCommand(turn2.ID, "attempt_exhausted")); !errors.Is(err, ErrAttemptFenceExhausted) {
		t.Fatalf("exhausted ClaimAttempt() error = %v, want ErrAttemptFenceExhausted", err)
	}
	if got := executionTableCount(t, db2, SQLTurnAttemptTable, "turn_id = ?", turn2.ID); got != 0 {
		t.Fatalf("exhausted claim persisted %d attempts", got)
	}
}

// A cancellation intent must stop new execution epochs without stranding the
// Attempt that is already live: its owner still has to reach the `stopped`
// terminal. This pins the guard's position after the idempotent replay branch.
func TestSQLExecutionStoreCancelledTurnReplaysLiveAttemptButRefusesReclaim(t *testing.T) {
	db, store, turn, clock := newSQLExecutionFixture(t, "cancel_live_attempt")
	claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_live"))
	if err != nil || !claimed.Claimed {
		t.Fatalf("ClaimAttempt() = %+v, %v", claimed, err)
	}
	clock.Set(clock.Get().Add(time.Second))
	if _, err := store.RequestCancel(context.Background(), turn.PrincipalID, turn.ThreadID, turn.ID, clock.Get(),
		sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"cancellationRequested":true}`)); err != nil {
		t.Fatal(err)
	}

	replayed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_live"))
	if err != nil || !replayed.Replay || replayed.Claimed {
		t.Fatalf("same-Attempt ClaimAttempt() after cancel = %+v, %v", replayed, err)
	}
	if replayed.Attempt.Fence() != claimed.Attempt.Fence() {
		t.Fatalf("replay fence = %+v, want %+v", replayed.Attempt.Fence(), claimed.Attempt.Fence())
	}
	if _, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_other")); !errors.Is(err, ErrAttemptCancelled) {
		t.Fatalf("different-Attempt ClaimAttempt() after cancel error = %v, want ErrAttemptCancelled", err)
	}

	if got := executionTableCount(t, db, SQLTurnAttemptTable, "turn_id = ?", turn.ID); got != 1 {
		t.Fatalf("cancelled Turn holds %d attempts, want only the original live Attempt", got)
	}

	// The live Attempt can still commit the terminal the cancellation implies.
	stopped, err := store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_cancel_live",
		TerminalStatus: agentv1.TurnStatusStopped,
	})
	if err != nil || stopped.TurnStatus != agentv1.TurnStatusStopped {
		t.Fatalf("CommitAttempt(stopped) = %+v, %v", stopped, err)
	}

	// A lapsed lease must not reopen a cancelled Turn to a fresh worker: the
	// reconciler drives it to `stopped` rather than starting a new epoch.
	db2, store2, turn2, clock2 := newSQLExecutionFixture(t, "cancel_expired_lease")
	expiring, err := store2.ClaimAttempt(context.Background(), executionClaimCommand(turn2.ID, "attempt_expiring"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store2.RequestCancel(context.Background(), turn2.PrincipalID, turn2.ThreadID, turn2.ID, clock2.Get(),
		sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"cancellationRequested":true}`)); err != nil {
		t.Fatal(err)
	}
	clock2.Set(expiring.Attempt.LeaseExpiresAt)
	if _, err := store2.ClaimAttempt(context.Background(), executionClaimCommand(turn2.ID, "attempt_reclaim")); !errors.Is(err, ErrAttemptCancelled) {
		t.Fatalf("expired-lease reclaim after cancel error = %v, want ErrAttemptCancelled", err)
	}
	if got := executionTableCount(t, db2, SQLTurnAttemptTable, "turn_id = ?", turn2.ID); got != 1 {
		t.Fatalf("cancelled Turn holds %d attempts after expiry, want only the original", got)
	}
}

func TestSQLExecutionStoreClaimFailureRollsBackTurnAndEvent(t *testing.T) {
	db, store, turn, _ := newSQLExecutionFixture(t, "claim_rollback")
	if err := db.Exec(`CREATE TRIGGER fail_agent_turn_attempt BEFORE INSERT ON w_agent_turn_attempt
		WHEN NEW.attempt_id = 'attempt_forced_failure' BEGIN SELECT RAISE(ABORT, 'forced-execution-attempt'); END`).Error; err != nil {
		t.Fatal(err)
	}
	_, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_forced_failure"))
	if !errors.Is(err, ErrStoreUnavailable) || strings.Contains(err.Error(), "forced-execution-attempt") {
		t.Fatalf("ClaimAttempt() error = %v, want sanitized ErrStoreUnavailable", err)
	}
	var state executionTestTurnState
	executionTakeTurnState(t, db, turn.ID, &state)
	if state.Status != string(agentv1.TurnStatusQueued) || state.ActiveAttemptID != nil || state.FencingToken != 0 || state.LastEventSequence != 1 {
		t.Fatalf("failed claim mutated turn state = %+v", state)
	}
	if got := executionTableCount(t, db, SQLTurnAttemptTable, "turn_id = ?", turn.ID); got != 0 {
		t.Fatalf("failed claim persisted %d attempts", got)
	}
}

func TestSQLExecutionStoreEffectIdentityConflictIsTypedAndAtomic(t *testing.T) {
	db, store, turn, clock := newSQLExecutionFixture(t, "effect_conflict")
	claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_effect_conflict"))
	if err != nil {
		t.Fatal(err)
	}
	first := CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_effect_first",
		Event: &EventDraft{Type: "writer.effect.first", Data: json.RawMessage(`{}`)},
		Effects: []EffectOutboxDraft{
			executionTestEffect("outbox_effect_first", "writer.effect.send", "dedupe_effect", clock.Get().Add(time.Minute)),
		},
	}
	if _, err := store.CommitAttempt(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_effect_second",
		Event: &EventDraft{Type: "writer.effect.second", Data: json.RawMessage(`{}`)},
		Effects: []EffectOutboxDraft{
			executionTestEffect("outbox_effect_second", "writer.effect.send", "dedupe_effect", clock.Get().Add(2*time.Minute)),
		},
	}
	if _, err := store.CommitAttempt(context.Background(), second); !errors.Is(err, ErrEffectConflict) {
		t.Fatalf("effect conflict error = %v, want ErrEffectConflict", err)
	}
	if got := executionTableCount(t, db, SQLTurnOperationTable, "turn_id = ?", turn.ID); got != 1 {
		t.Fatalf("effect conflict left %d operations, want 1", got)
	}
	if got := executionTableCount(t, db, SQLEffectOutboxTable, "turn_id = ?", turn.ID); got != 1 {
		t.Fatalf("effect conflict left %d outbox rows, want 1", got)
	}
	if got := executionTableCount(t, db, SQLTurnEventTable, "turn_id = ?", turn.ID); got != 3 {
		t.Fatalf("effect conflict left %d events, want 3", got)
	}
	stale := second
	stale.OperationID = "operation_effect_stale"
	stale.Fence.AttemptID = "attempt_effect_stale"
	if _, err := store.CommitAttempt(context.Background(), stale); !errors.Is(err, ErrAttemptFenced) {
		t.Fatalf("stale fence with colliding Effect error = %v, want ErrAttemptFenced", err)
	}
}

func TestSQLExecutionStoreDefaultDatabaseClock(t *testing.T) {
	db := testutil.NewTestDB(t)
	store := mustSQLStore(t, db)
	turn := sqlStoreTestTurn("execution_database_clock")
	turn.CreatedAt = time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	turn.UpdatedAt = turn.CreatedAt
	if _, err := store.Admit(context.Background(), turn, sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"status":"queued"}`)); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_database_clock"))
	if err != nil {
		t.Fatalf("ClaimAttempt() with database clock: %v", err)
	}
	if claimed.Attempt.ClaimedAt.Location() != time.UTC ||
		claimed.Attempt.LeaseExpiresAt.Sub(claimed.Attempt.ClaimedAt) != DefaultAttemptLeaseTTL {
		t.Fatalf("database-clock attempt = %+v", claimed.Attempt)
	}
}

func TestSQLExecutionStoreMySQLContract(t *testing.T) {
	settings := mysqlContractSettingsForTest(t)
	dbOne := openMySQLContractDatabase(t, settings)
	dbTwo := openMySQLContractDatabase(t, settings)
	sqlOne, err := dbOne.DB()
	if err != nil {
		t.Fatal("obtain first MySQL contract pool failed")
	}
	sqlTwo, err := dbTwo.DB()
	if err != nil {
		t.Fatal("obtain second MySQL contract pool failed")
	}
	if sqlOne == sqlTwo {
		t.Fatal("MySQL execution contract requires independent connection pools")
	}
	mysqlContractPreflight(t, dbOne)
	storeOne := mustSQLStore(t, dbOne)
	storeTwo := mustSQLStore(t, dbTwo)
	now, err := databaseExecutionClock(context.Background(), dbOne)
	if err != nil {
		t.Fatal("read MySQL execution clock failed")
	}
	suffix := mysqlContractSuffix(t, "mxexec")
	turn := sqlStoreTestTurn(suffix)
	turn.CreatedAt = now.Add(-time.Minute)
	turn.UpdatedAt = turn.CreatedAt
	mysqlContractAssertNamespaceEmpty(t, dbOne, turn)
	admission, err := storeOne.Admit(context.Background(), turn,
		sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"status":"queued"}`))
	mysqlContractAssertCreated(t, admission, err)
	cleanup := mysqlContractOwnedCleanup(t, dbOne, turn)
	t.Cleanup(cleanup)

	const candidates = 24
	type claimOutcome struct {
		result ClaimAttemptResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan claimOutcome, candidates)
	var wait sync.WaitGroup
	for index := 0; index < candidates; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			store := storeOne
			if index%2 == 1 {
				store = storeTwo
			}
			result, err := store.ClaimAttempt(context.Background(),
				executionClaimCommand(turn.ID, fmt.Sprintf("attempt_mysql_race_%02d_%s", index, suffix)))
			outcomes <- claimOutcome{result: result, err: err}
		}(index)
	}
	close(start)
	wait.Wait()
	close(outcomes)

	var first ClaimAttemptResult
	winners := 0
	busy := 0
	for outcome := range outcomes {
		switch {
		case outcome.err == nil:
			first = outcome.result
			winners++
		case errors.Is(outcome.err, ErrAttemptBusy):
			busy++
		default:
			t.Errorf("MySQL concurrent ClaimAttempt() error = %v, want ErrAttemptBusy", outcome.err)
		}
	}
	if winners != 1 || busy != candidates-1 {
		t.Fatalf("MySQL claim outcomes: winners=%d busy=%d, want 1 and %d", winners, busy, candidates-1)
	}
	if first.Attempt.FencingToken != 1 || first.Attempt.Status != AttemptStatusRunning {
		t.Fatalf("MySQL first claim = %+v", first)
	}
	if got := mysqlContractCount(t, dbTwo, SQLTurnAttemptTable, "turn_id = ?", turn.ID); got != 1 {
		t.Fatalf("MySQL first-claim attempt count = %d, want 1", got)
	}

	// Shorten the first Attempt with a fenced heartbeat, then wait against the
	// database clock. This exercises real DATETIME(6) lease equality without
	// mutating a lease behind the Store's invariants.
	storeOne.attemptLeaseTTL = 500 * time.Millisecond
	storeTwo.attemptLeaseTTL = 500 * time.Millisecond
	shortLease, err := storeTwo.HeartbeatAttempt(context.Background(), HeartbeatAttemptCommand{Fence: first.Attempt.Fence()})
	if err != nil {
		t.Fatalf("MySQL short HeartbeatAttempt(): %v", err)
	}
	waitForMySQLExecutionTime(t, dbOne, shortLease.Attempt.LeaseExpiresAt)
	second, err := storeTwo.ClaimAttempt(context.Background(),
		executionClaimCommand(turn.ID, "attempt_mysql_reclaim_"+suffix))
	if err != nil {
		t.Fatalf("MySQL reclaim ClaimAttempt(): %v", err)
	}
	if !second.Reclaimed || second.Replay || second.Attempt.FencingToken != 2 {
		t.Fatalf("MySQL reclaimed Attempt = %+v", second)
	}
	storeOne.attemptLeaseTTL = DefaultAttemptLeaseTTL
	storeTwo.attemptLeaseTTL = DefaultAttemptLeaseTTL
	if _, err := storeOne.HeartbeatAttempt(context.Background(), HeartbeatAttemptCommand{Fence: second.Attempt.Fence()}); err != nil {
		t.Fatalf("MySQL extend reclaimed Attempt: %v", err)
	}

	if _, err := storeOne.HeartbeatAttempt(context.Background(), HeartbeatAttemptCommand{Fence: first.Attempt.Fence()}); !errors.Is(err, ErrAttemptFenced) {
		t.Fatalf("MySQL old-fence HeartbeatAttempt() error = %v, want ErrAttemptFenced", err)
	}
	staleOperationID := "operation_mysql_stale_" + suffix
	if _, err := storeOne.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence: first.Attempt.Fence(), OperationID: staleOperationID,
		Event: &EventDraft{Type: "writer.mysql.stale", Data: json.RawMessage(`{"stale":true}`)},
	}); !errors.Is(err, ErrAttemptFenced) {
		t.Fatalf("MySQL old-fence CommitAttempt() error = %v, want ErrAttemptFenced", err)
	}
	if got := mysqlContractCount(t, dbTwo, SQLTurnOperationTable,
		"turn_id = ? AND operation_id = ?", turn.ID, staleOperationID); got != 0 {
		t.Fatalf("MySQL stale fence persisted %d operations", got)
	}

	now, err = databaseExecutionClock(context.Background(), dbTwo)
	if err != nil {
		t.Fatal("read second MySQL execution clock failed")
	}
	farFuture := now.AddDate(10, 0, 0)
	operationID := "operation_mysql_effect_" + suffix
	commit := CommitAttemptCommand{
		Fence: second.Attempt.Fence(), OperationID: operationID,
		Event: &EventDraft{
			Type:         "writer.mysql.execution",
			ResourceRefs: []string{"wm:writer:document:doc_mysql@2"},
			Data:         json.RawMessage(`{"patch":"mysql"}`),
		},
		Effects: []EffectOutboxDraft{
			executionTestEffect("outbox_mysql_0_"+suffix, "contract.agentturn.noop.v1", "dedupe_mysql_0_"+suffix, farFuture),
			executionTestEffect("outbox_mysql_1_"+suffix, "contract.agentturn.noop.v1", "dedupe_mysql_1_"+suffix, farFuture.Add(time.Microsecond)),
		},
	}
	committed, err := storeTwo.CommitAttempt(context.Background(), commit)
	if err != nil {
		t.Fatalf("MySQL CommitAttempt(): %v", err)
	}
	if committed.Replay || committed.Event.Sequence != 3 || len(committed.Effects) != 2 || committed.TurnStatus != agentv1.TurnStatusRunning {
		t.Fatalf("MySQL committed Operation = %+v", committed)
	}
	var receipt sqlTurnOperationRow
	if err := dbOne.Where("turn_id = ? AND operation_id = ?", turn.ID, operationID).Take(&receipt).Error; err != nil {
		t.Fatal("read MySQL Operation receipt failed")
	}
	if receipt.AttemptID != second.Attempt.ID || receipt.FencingToken != 2 || receipt.EventSequence != 3 || receipt.EffectCount != 2 {
		t.Fatalf("MySQL Operation receipt = %+v", receipt)
	}
	if got := mysqlContractCount(t, dbOne, SQLEffectOutboxTable,
		"turn_id = ? AND operation_id = ?", turn.ID, operationID); got != 2 {
		t.Fatalf("MySQL committed Outbox count = %d, want 2", got)
	}
	replayed, err := storeOne.CommitAttempt(context.Background(), commit)
	if err != nil {
		t.Fatalf("MySQL replay CommitAttempt(): %v", err)
	}
	if !replayed.Replay || replayed.OperationDigest != committed.OperationDigest ||
		replayed.Event.EventID != committed.Event.EventID || len(replayed.Effects) != len(committed.Effects) {
		t.Fatalf("MySQL replay = %+v, committed = %+v", replayed, committed)
	}
	for index := range replayed.Effects {
		if replayed.Effects[index].OutboxID != committed.Effects[index].OutboxID || replayed.Effects[index].Ordinal != index {
			t.Fatalf("MySQL replay Effect[%d] = %+v, committed = %+v", index, replayed.Effects[index], committed.Effects[index])
		}
	}

	terminalOperationID := "operation_mysql_terminal_" + suffix
	terminal, err := storeOne.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence: second.Attempt.Fence(), OperationID: terminalOperationID,
		TerminalStatus: agentv1.TurnStatusCompleted,
	})
	if err != nil {
		t.Fatalf("MySQL terminal CommitAttempt(): %v", err)
	}
	if terminal.TurnStatus != agentv1.TurnStatusCompleted || terminal.Event.Sequence != 4 || terminal.Attempt.Status != AttemptStatusCompleted {
		t.Fatalf("MySQL terminal result = %+v", terminal)
	}
	var state executionTestTurnState
	if err := dbTwo.Table(SQLTurnTable).
		Select("status", "active_attempt_id", "fencing_token", "last_event_sequence", "finished_at").
		Where("turn_id = ?", turn.ID).Take(&state).Error; err != nil {
		t.Fatal("read MySQL terminal Turn state failed")
	}
	if state.Status != string(agentv1.TurnStatusCompleted) || state.ActiveAttemptID != nil ||
		state.FencingToken != 2 || state.LastEventSequence != 4 || state.FinishedAt == nil {
		t.Fatalf("MySQL terminal Turn state = %+v", state)
	}

	// Exercise the FK-safe cleanup order before the deferred idempotent cleanup
	// and prove that no row from this contract test survives.
	cleanup()
	mysqlContractAssertNoRows(t, dbOne, turn.ID)
}

func waitForMySQLExecutionTime(t *testing.T, db *gorm.DB, threshold time.Time) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		now, err := databaseExecutionClock(context.Background(), db)
		if err != nil {
			t.Fatal("read MySQL execution clock failed")
		}
		if !now.Before(threshold) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("MySQL execution clock did not reach %s (last %s)", threshold, now)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type sqlExecutionTestClock struct {
	mu  sync.RWMutex
	now time.Time
}

func (clock *sqlExecutionTestClock) Now(context.Context, *gorm.DB) (time.Time, error) {
	return clock.Get(), nil
}

func (clock *sqlExecutionTestClock) Get() time.Time {
	clock.mu.RLock()
	defer clock.mu.RUnlock()
	return clock.now
}

func (clock *sqlExecutionTestClock) Set(now time.Time) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = now.UTC()
}

type executionTestTurnState struct {
	Status            string     `gorm:"column:status"`
	ActiveAttemptID   *string    `gorm:"column:active_attempt_id"`
	FencingToken      int64      `gorm:"column:fencing_token"`
	LastEventSequence int64      `gorm:"column:last_event_sequence"`
	FinishedAt        *time.Time `gorm:"column:finished_at"`
}

type executionTestAttemptState struct {
	AttemptID       string     `gorm:"column:attempt_id"`
	Status          string     `gorm:"column:status"`
	FencingToken    int64      `gorm:"column:fencing_token"`
	LastHeartbeatAt time.Time  `gorm:"column:last_heartbeat_at"`
	LeaseExpiresAt  time.Time  `gorm:"column:lease_expires_at"`
	FinishedAt      *time.Time `gorm:"column:finished_at"`
}

func newSQLExecutionFixture(t *testing.T, suffix string) (*gorm.DB, *SQLStore, Turn, *sqlExecutionTestClock) {
	t.Helper()
	db := testutil.NewTestDB(t)
	store := mustSQLStore(t, db)
	turn := sqlStoreTestTurn("execution_" + suffix)
	clock := &sqlExecutionTestClock{now: turn.CreatedAt.Add(time.Minute).UTC()}
	store.executionClock = clock.Now
	store.attemptLeaseTTL = DefaultAttemptLeaseTTL
	if _, err := store.Admit(context.Background(), turn, sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"status":"queued"}`)); err != nil {
		t.Fatalf("Admit(): %v", err)
	}
	return db, store, turn, clock
}

func executionClaimCommand(turnID agentv1.TurnID, attemptID string) ClaimAttemptCommand {
	return ClaimAttemptCommand{
		TurnID:            turnID,
		AttemptID:         attemptID,
		WorkerID:          "worker_execution_test",
		WorkerBuildDigest: "sha256:worker-execution-test",
	}
}

func executionTestEffect(outboxID, topic, dedupeKey string, availableAt time.Time) EffectOutboxDraft {
	return EffectOutboxDraft{
		OutboxID:    outboxID,
		Topic:       topic,
		DedupeKey:   dedupeKey,
		Payload:     json.RawMessage(`{"test":true}`),
		AvailableAt: availableAt.UTC(),
	}
}

func executionTakeTurnState(t *testing.T, db *gorm.DB, turnID agentv1.TurnID, destination *executionTestTurnState) {
	t.Helper()
	if err := db.Table(SQLTurnTable).
		Select("status", "active_attempt_id", "fencing_token", "last_event_sequence", "finished_at").
		Where("turn_id = ?", turnID).Take(destination).Error; err != nil {
		t.Fatal(err)
	}
}

func executionTableCount(t *testing.T, db *gorm.DB, table, predicate string, args ...any) int64 {
	t.Helper()
	var count int64
	query := db.Table(table)
	if predicate != "" {
		query = query.Where(predicate, args...)
	}
	if err := query.Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	return count
}
