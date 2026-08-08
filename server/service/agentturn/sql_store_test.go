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

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	agentv1 "server/contracts/agent/v1"
	"server/utils/testutil"
)

var sqlStoreTestTime = time.Date(2026, 8, 1, 12, 0, 0, 123456000, time.FixedZone("UTC+8", 8*60*60))

func TestNewSQLStoreDoesNotAutoMigrateOrFallback(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewSQLStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if db.Migrator().HasTable(SQLTurnTable) || db.Migrator().HasTable(SQLTurnEventTable) {
		t.Fatal("NewSQLStore unexpectedly created durable Agent tables")
	}
	_, err = store.GetOwned(context.Background(), "principal", "thread", "turn")
	if !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("missing-schema read error = %v, want ErrStoreUnavailable", err)
	}
}

func TestSQLStoreMigrationMirrorAdmissionLifecycleAndOwnership(t *testing.T) {
	db := testutil.NewTestDB(t)
	store := mustSQLStore(t, db)
	turn := sqlStoreTestTurn("core")
	initial := sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"status":"queued"}`)

	admission, err := store.Admit(context.Background(), turn, initial)
	if err != nil || !admission.Created {
		t.Fatalf("Admit() = %+v, %v", admission, err)
	}
	zero := agentv1.Sequence(0)
	replay, err := store.Replay(context.Background(), turn.PrincipalID, turn.ThreadID, turn.ID, ReplayQuery{
		Cursor: agentv1.ReplayCursor{AfterSequence: &zero}, Limit: 10,
	})
	if err != nil || len(replay.Events) != 1 || replay.Events[0].Sequence != 1 {
		t.Fatalf("fresh Replay() = %+v, %v", replay, err)
	}

	retry := turn
	retry.ID = "turn_core_retry"
	replayed, err := store.Admit(context.Background(), retry, initial)
	if err != nil || replayed.Created || replayed.Turn.ID != turn.ID {
		t.Fatalf("same-digest Admit() = %+v, %v", replayed, err)
	}
	conflict := retry
	conflict.ID = "turn_core_conflict"
	conflict.CommandDigest = "sha256:different"
	if _, err := store.Admit(context.Background(), conflict, initial); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("different-digest Admit() error = %v, want ErrIdempotencyConflict", err)
	}
	collision := turn
	collision.IdempotencyKey = "idem_core_collision"
	if _, err := store.Admit(context.Background(), collision, initial); !errors.Is(err, ErrTurnIDConflict) {
		t.Fatalf("turn collision error = %v, want ErrTurnIDConflict", err)
	}

	if _, err := store.GetOwned(context.Background(), "other", turn.ThreadID, turn.ID); !errors.Is(err, ErrTurnNotFound) {
		t.Fatalf("cross-owner GetOwned() error = %v", err)
	}
	if _, err := store.Replay(context.Background(), turn.PrincipalID, "other", turn.ID, ReplayQuery{Limit: 10}); !errors.Is(err, ErrTurnNotFound) {
		t.Fatalf("cross-thread Replay() error = %v", err)
	}
	owned, err := store.GetOwned(context.Background(), turn.PrincipalID, turn.ThreadID, turn.ID)
	if err != nil || owned.CreatedAt.Location() != time.UTC || owned.UpdatedAt.Location() != time.UTC {
		t.Fatalf("owned UTC Turn = %+v, %v", owned, err)
	}
	// The store, not the caller, owns durable lifecycle time. `turn.CreatedAt`
	// is a fixed non-UTC test instant, so reusing it would be observable here.
	if owned.CreatedAt.Equal(turn.CreatedAt) {
		t.Fatalf("admission reused caller CreatedAt %s as database authority", turn.CreatedAt)
	}
	if !owned.CreatedAt.Equal(owned.UpdatedAt) {
		t.Fatalf("admission CreatedAt %s and UpdatedAt %s disagree", owned.CreatedAt, owned.UpdatedAt)
	}

	// Caller intent times are deliberately stale and in the wrong order. A
	// store that honored them would write updatedAt before createdAt.
	staleAt := turn.CreatedAt.Add(time.Second)
	running, err := store.Transition(context.Background(), turn.ID, agentv1.TurnStatusRunning, staleAt,
		sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"status":"running"}`))
	if err != nil || !running.Changed || running.Event == nil || running.Event.Sequence != 2 {
		t.Fatalf("Transition(running) = %+v, %v", running, err)
	}
	if running.Turn.StartedAt == nil || running.Turn.StartedAt.Before(owned.CreatedAt) || running.Turn.StartedAt.Equal(staleAt) {
		t.Fatalf("Transition(running) StartedAt = %v, want store clock at or after %s", running.Turn.StartedAt, owned.CreatedAt)
	}
	cancelled, err := store.RequestCancel(context.Background(), turn.PrincipalID, turn.ThreadID, turn.ID,
		staleAt.Add(time.Second), sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"cancellationRequested":true}`))
	if err != nil || !cancelled.NewlyRequested || cancelled.Event == nil || cancelled.Event.Sequence != 3 {
		t.Fatalf("RequestCancel() = %+v, %v", cancelled, err)
	}
	if cancelled.Turn.CancelRequestedAt == nil || cancelled.Turn.CancelRequestedAt.Before(owned.CreatedAt) {
		t.Fatalf("RequestCancel() CancelRequestedAt = %v, want store clock at or after %s", cancelled.Turn.CancelRequestedAt, owned.CreatedAt)
	}
	stopped, err := store.Transition(context.Background(), turn.ID, agentv1.TurnStatusStopped, staleAt.Add(2*time.Second),
		sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"status":"stopped"}`))
	if err != nil || !stopped.Changed || stopped.Event == nil || stopped.Event.Sequence != 4 {
		t.Fatalf("Transition(stopped) = %+v, %v", stopped, err)
	}
	if stopped.Turn.FinishedAt == nil || stopped.Turn.FinishedAt.Before(*running.Turn.StartedAt) {
		t.Fatalf("Transition(stopped) FinishedAt = %v, want at or after StartedAt %v", stopped.Turn.FinishedAt, running.Turn.StartedAt)
	}
	if _, err := store.AppendEvent(context.Background(), turn.ID, sqlStoreTestDraft("writer.after-terminal", `{}`)); !errors.Is(err, ErrTurnTerminal) {
		t.Fatalf("terminal AppendEvent() error = %v, want ErrTurnTerminal", err)
	}
}

func TestSQLStoreAdmissionRollsBackWhenInitialEventFails(t *testing.T) {
	db := testutil.NewTestDB(t)
	if err := db.Exec(`CREATE TRIGGER fail_agent_turn_event BEFORE INSERT ON w_agent_turn_event
		WHEN NEW.turn_id = 'turn_rollback' BEGIN SELECT RAISE(ABORT, 'forced-store-test'); END`).Error; err != nil {
		t.Fatal(err)
	}
	store := mustSQLStore(t, db)
	turn := sqlStoreTestTurn("rollback")
	_, err := store.Admit(context.Background(), turn, sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"status":"queued"}`))
	if !errors.Is(err, ErrStoreUnavailable) || strings.Contains(err.Error(), "forced-store-test") {
		t.Fatalf("rollback error = %v, want sanitized ErrStoreUnavailable", err)
	}
	var turns, events int64
	if err := db.Table(SQLTurnTable).Where("turn_id = ?", turn.ID).Count(&turns).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table(SQLTurnEventTable).Where("turn_id = ?", turn.ID).Count(&events).Error; err != nil {
		t.Fatal(err)
	}
	if turns != 0 || events != 0 {
		t.Fatalf("failed admission persisted turns=%d events=%d", turns, events)
	}
}

func TestSQLStoreRejectsCorruptFullEnvelopeAndIndexedProjection(t *testing.T) {
	db := testutil.NewTestDB(t)
	store := mustSQLStore(t, db)
	turn := sqlStoreTestTurn("corrupt")
	if _, err := store.Admit(context.Background(), turn, sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"status":"queued"}`)); err != nil {
		t.Fatal(err)
	}
	var row sqlTurnEventRow
	if err := db.Where("turn_id = ? AND sequence = 1", turn.ID).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	original := append([]byte(nil), row.EventJSON...)
	var object map[string]any
	if err := json.Unmarshal(original, &object); err != nil {
		t.Fatal(err)
	}
	object["unexpectedField"] = true
	corrupt, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Table(SQLTurnEventTable).Where("turn_id = ? AND sequence = 1", turn.ID).UpdateColumn("event_json", corrupt).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.Replay(context.Background(), "other", turn.ThreadID, turn.ID, ReplayQuery{Limit: 10}); !errors.Is(err, ErrTurnNotFound) {
		t.Fatalf("ownership was not checked before corrupt event: %v", err)
	}
	_, err = store.Replay(context.Background(), turn.PrincipalID, turn.ThreadID, turn.ID, ReplayQuery{Limit: 10})
	if !errors.Is(err, ErrStoreIntegrity) || strings.Contains(err.Error(), "unexpectedField") {
		t.Fatalf("corrupt JSON error = %v, want sanitized ErrStoreIntegrity", err)
	}
	if err := db.Table(SQLTurnEventTable).Where("turn_id = ? AND sequence = 1", turn.ID).Updates(map[string]any{
		"event_json": original,
		"event_type": "writer.index.mismatch",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.Replay(context.Background(), turn.PrincipalID, turn.ThreadID, turn.ID, ReplayQuery{Limit: 10}); !errors.Is(err, ErrStoreIntegrity) {
		t.Fatalf("indexed projection mismatch error = %v, want ErrStoreIntegrity", err)
	}
}

func TestSQLStoreConcurrentAppendUsesPersistentSequence(t *testing.T) {
	db := testutil.NewTestDB(t)
	store := mustSQLStore(t, db)
	turn := sqlStoreTestTurn("concurrent")
	if _, err := store.Admit(context.Background(), turn, sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"status":"queued"}`)); err != nil {
		t.Fatal(err)
	}
	const writers = 96
	start := make(chan struct{})
	errs := make(chan error, writers)
	sequences := make(chan agentv1.Sequence, writers)
	var wait sync.WaitGroup
	for i := 0; i < writers; i++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			event, err := store.AppendEvent(context.Background(), turn.ID,
				sqlStoreTestDraft(agentv1.EventType(fmt.Sprintf("writer.delta.%d", index)), fmt.Sprintf(`{"index":%d}`, index)))
			if err != nil {
				errs <- err
				return
			}
			sequences <- event.Sequence
		}(i)
	}
	close(start)
	wait.Wait()
	close(errs)
	close(sequences)
	for err := range errs {
		t.Errorf("AppendEvent() error = %v", err)
	}
	seen := make(map[agentv1.Sequence]struct{}, writers)
	for sequence := range sequences {
		seen[sequence] = struct{}{}
	}
	for want := agentv1.Sequence(2); want <= writers+1; want++ {
		if _, ok := seen[want]; !ok {
			t.Errorf("missing committed sequence %d", want)
		}
	}
	restarted := mustSQLStore(t, db)
	event, err := restarted.AppendEvent(context.Background(), turn.ID, sqlStoreTestDraft("writer.after.restart", `{}`))
	if err != nil || event.Sequence != writers+2 {
		t.Fatalf("new SQLStore AppendEvent() = %+v, %v; want sequence %d", event, err, writers+2)
	}
}

func TestSQLStoreReplayMergesFurthestCursorAtRetentionBoundary(t *testing.T) {
	db := testutil.NewTestDB(t)
	store := mustSQLStore(t, db)
	turn := sqlStoreTestTurn("cursor")
	if _, err := store.Admit(context.Background(), turn, sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"status":"queued"}`)); err != nil {
		t.Fatal(err)
	}
	for sequence := 2; sequence <= 4; sequence++ {
		if _, err := store.AppendEvent(context.Background(), turn.ID,
			sqlStoreTestDraft(agentv1.EventType(fmt.Sprintf("writer.cursor.%d", sequence)), `{}`)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Where("turn_id = ? AND sequence < ?", turn.ID, 3).Delete(&sqlTurnEventRow{}).Error; err != nil {
		t.Fatal(err)
	}

	boundary := agentv1.Sequence(2)
	replay, err := store.Replay(context.Background(), turn.PrincipalID, turn.ThreadID, turn.ID, ReplayQuery{
		Cursor: agentv1.ReplayCursor{AfterSequence: &boundary}, Limit: 10,
	})
	if err != nil || len(replay.Events) != 2 || replay.Events[0].Sequence != 3 || replay.Events[1].Sequence != 4 {
		t.Fatalf("boundary Replay() = %+v, %v", replay, err)
	}

	stale := agentv1.Sequence(1)
	if _, err := store.Replay(context.Background(), turn.PrincipalID, turn.ThreadID, turn.ID, ReplayQuery{
		Cursor: agentv1.ReplayCursor{AfterSequence: &stale}, Limit: 10,
	}); !errors.Is(err, ErrReplayGap) {
		t.Fatalf("stale Replay() error = %v, want ErrReplayGap", err)
	}

	zero := agentv1.Sequence(0)
	replay, err = store.Replay(context.Background(), turn.PrincipalID, turn.ThreadID, turn.ID, ReplayQuery{
		Cursor: agentv1.ReplayCursor{
			AfterSequence: &zero,
			LastEventID:   deterministicEventID(turn.ID, 3),
		},
		Limit: 10,
	})
	if err != nil || replay.AfterSequence != 3 || len(replay.Events) != 1 || replay.Events[0].Sequence != 4 {
		t.Fatalf("furthest cursor Replay() = %+v, %v", replay, err)
	}

	replay, err = store.Replay(context.Background(), turn.PrincipalID, turn.ThreadID, turn.ID, ReplayQuery{
		Cursor: agentv1.ReplayCursor{LastEventID: deterministicEventID(turn.ID, 2)}, Limit: 10,
	})
	if err != nil || replay.AfterSequence != 2 || len(replay.Events) != 2 {
		t.Fatalf("pruned boundary Event ID Replay() = %+v, %v", replay, err)
	}
}

func TestDurableStoreBoundsMatchSQLContract(t *testing.T) {
	command := testStartCommand("principal_1", "thread_1", "idem_1", "sha256:ascii")
	command.CommandDigest = "sha256:\u4e0d\u5141\u8bb8"
	if err := command.Validate(); err == nil {
		t.Fatal("non-ASCII command digest was accepted")
	}
	if err := (ReplayQuery{
		Cursor: agentv1.ReplayCursor{LastEventID: strings.Repeat("e", MaxEventIDBytes+1)},
		Limit:  10,
	}).Validate(); err == nil {
		t.Fatal("oversized Last-Event-ID was accepted")
	}
	if err := (EventDraft{Type: "writer.large", Data: json.RawMessage(`"` + strings.Repeat("d", MaxEventDataBytes) + `"`)}).Validate(); err == nil {
		t.Fatal("oversized event data was accepted")
	}
	refs := make([]string, MaxEventResourceRefs+1)
	for index := range refs {
		refs[index] = fmt.Sprintf("wm:writer:document:doc_%d@1", index)
	}
	if err := (EventDraft{Type: "writer.refs", ResourceRefs: refs, Data: json.RawMessage(`{}`)}).Validate(); err == nil {
		t.Fatal("too many event resource refs were accepted")
	}
}

func TestSQLStoreRejectsSequenceExhaustionWithoutMutation(t *testing.T) {
	db := testutil.NewTestDB(t)
	store := mustSQLStore(t, db)
	turn := sqlStoreTestTurn("exhausted")
	if _, err := store.Admit(context.Background(), turn, sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"status":"queued"}`)); err != nil {
		t.Fatal(err)
	}
	if err := db.Table(SQLTurnTable).Where("turn_id = ?", turn.ID).
		UpdateColumn("last_event_sequence", int64(MaxDurableSequence)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(context.Background(), turn.ID, sqlStoreTestDraft("writer.exhausted", `{}`)); !errors.Is(err, ErrSequenceExhausted) {
		t.Fatalf("sequence exhaustion error = %v, want ErrSequenceExhausted", err)
	}
	var events int64
	if err := db.Table(SQLTurnEventTable).Where("turn_id = ?", turn.ID).Count(&events).Error; err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("sequence exhaustion appended an event; count = %d", events)
	}
}

func TestSQLStoreMySQLContract(t *testing.T) {
	settings := mysqlContractSettingsForTest(t)
	db := openMySQLContractDatabase(t, settings)
	mysqlContractPreflight(t, db)
	store := mustSQLStore(t, db)
	suffix := mysqlContractSuffix(t, "mysql")
	turn := sqlStoreTestTurn(suffix)
	mysqlContractAssertNamespaceEmpty(t, db, turn)
	admission, err := store.Admit(context.Background(), turn,
		sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"status":"queued"}`))
	mysqlContractAssertCreated(t, admission, err)
	cleanup := mysqlContractOwnedCleanup(t, db, turn)
	t.Cleanup(cleanup)

	const writers = 32
	var wait sync.WaitGroup
	errorsByWriter := make(chan error, writers)
	for writer := 0; writer < writers; writer++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, err := store.AppendEvent(context.Background(), turn.ID,
				sqlStoreTestDraft(agentv1.EventType(fmt.Sprintf("writer.mysql.%d", index)), `{}`))
			if err != nil {
				errorsByWriter <- err
			}
		}(writer)
	}
	wait.Wait()
	close(errorsByWriter)
	for err := range errorsByWriter {
		t.Errorf("MySQL AppendEvent(): %v", err)
	}

	restarted := mustSQLStore(t, db)
	event, err := restarted.AppendEvent(context.Background(), turn.ID, sqlStoreTestDraft("writer.mysql.restart", `{}`))
	if err != nil || event.Sequence != writers+2 {
		t.Fatalf("MySQL restart AppendEvent() = %+v, %v; want sequence %d", event, err, writers+2)
	}
	replay, err := restarted.Replay(context.Background(), turn.PrincipalID, turn.ThreadID, turn.ID, ReplayQuery{Limit: 100})
	if err != nil || len(replay.Events) != writers+2 || replay.Window.LatestSequence != writers+2 {
		t.Fatalf("MySQL Replay() = events:%d window:%+v, %v", len(replay.Events), replay.Window, err)
	}

	// Run once while the test body can verify the result. If this attempt fails,
	// the deferred invocation retries because cleanup is marked complete only
	// after its transaction and zero-residual checks succeed.
	cleanup()
	mysqlContractAssertNoRows(t, db, turn.ID)
}

func mustSQLStore(t *testing.T, db *gorm.DB) *SQLStore {
	t.Helper()
	store, err := NewSQLStore(db)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func sqlStoreTestTurn(suffix string) Turn {
	return Turn{
		ID:             agentv1.TurnID("turn_" + suffix),
		PrincipalID:    PrincipalID("principal_" + suffix),
		ThreadID:       agentv1.ThreadID("thread_" + suffix),
		IdempotencyKey: agentv1.IdempotencyKey("idem_" + suffix),
		CommandDigest:  "sha256:command-" + suffix,
		Plugin: agentv1.EventPluginRef{
			ID: "workmax.writer", Version: "1.0.0", ReleaseDigest: "sha256:release",
		},
		Status: agentv1.TurnStatusQueued, CreatedAt: sqlStoreTestTime, UpdatedAt: sqlStoreTestTime,
	}
}

func sqlStoreTestDraft(eventType agentv1.EventType, data string) EventDraft {
	return EventDraft{Type: eventType, Data: json.RawMessage(data)}
}
