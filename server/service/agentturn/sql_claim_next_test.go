package agentturn

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	agentv1 "server/contracts/agent/v1"
	"server/utils/testutil"
)

// newSQLClaimNextFixture admits several Turns into one store so discovery has
// something to order and contend over. Admission order is the FIFO order the
// scan is expected to reproduce.
func newSQLClaimNextFixture(t *testing.T, suffixes ...string) (*gorm.DB, *SQLStore, *sqlExecutionTestClock, []Turn) {
	t.Helper()
	db := testutil.NewTestDB(t)
	store := mustSQLStore(t, db)
	clock := &sqlExecutionTestClock{now: sqlStoreTestTime.Add(time.Minute).UTC()}
	store.executionClock = clock.Now
	store.attemptLeaseTTL = DefaultAttemptLeaseTTL

	turns := make([]Turn, 0, len(suffixes))
	for _, suffix := range suffixes {
		turn := sqlStoreTestTurn("next_" + suffix)
		if _, err := store.Admit(context.Background(), turn, sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"status":"queued"}`)); err != nil {
			t.Fatalf("Admit(%s): %v", suffix, err)
		}
		// Distinct created_at values make the FIFO assertion meaningful rather
		// than an accident of insertion id ordering.
		clock.Set(clock.Get().Add(time.Second))
		turns = append(turns, turn)
	}
	return db, store, clock, turns
}

func claimNextCommand(attemptID string) ClaimNextCommand {
	return ClaimNextCommand{
		AttemptID:         attemptID,
		WorkerID:          "worker_claim_next_test",
		WorkerBuildDigest: "sha256:worker-claim-next-test",
	}
}

func TestSQLClaimNextTakesOldestQueuedTurnFirst(t *testing.T) {
	_, store, _, turns := newSQLClaimNextFixture(t, "first", "second", "third")

	for index, want := range turns {
		got, err := store.ClaimNext(context.Background(), claimNextCommand(fmt.Sprintf("attempt_fifo_%d", index)))
		if err != nil {
			t.Fatalf("ClaimNext() %d: %v", index, err)
		}
		if !got.Claimed || got.Replay || got.Reclaimed {
			t.Fatalf("ClaimNext() %d = %+v, want a fresh claim", index, got)
		}
		if got.Turn.ID != want.ID {
			t.Fatalf("ClaimNext() %d returned %q, want oldest unclaimed %q", index, got.Turn.ID, want.ID)
		}
		if got.Turn.Status != agentv1.TurnStatusRunning || got.Attempt.Status != AttemptStatusRunning {
			t.Fatalf("ClaimNext() %d state = %+v", index, got)
		}
	}

	if _, err := store.ClaimNext(context.Background(), claimNextCommand("attempt_drained")); !errors.Is(err, ErrNoClaimableTurn) {
		t.Fatalf("drained ClaimNext() error = %v, want ErrNoClaimableTurn", err)
	}
}

func TestSQLClaimNextSkipsTurnsThatNeedReconciliation(t *testing.T) {
	db, store, clock, turns := newSQLClaimNextFixture(t, "cancelled", "terminal", "exhausted", "healthy")
	cancelled, terminal, exhausted, healthy := turns[0], turns[1], turns[2], turns[3]

	if _, err := store.RequestCancel(context.Background(), cancelled.PrincipalID, cancelled.ThreadID, cancelled.ID,
		clock.Get(), sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"cancellationRequested":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(context.Background(), terminal.ID, agentv1.TurnStatusFailed, clock.Get(),
		sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"status":"failed"}`)); err != nil {
		t.Fatal(err)
	}
	if err := db.Table(SQLTurnTable).Where("turn_id = ?", exhausted.ID).
		UpdateColumn("fencing_token", int64(MaxDurableSequence)).Error; err != nil {
		t.Fatal(err)
	}

	got, err := store.ClaimNext(context.Background(), claimNextCommand("attempt_skip"))
	if err != nil {
		t.Fatalf("ClaimNext(): %v", err)
	}
	if got.Turn.ID != healthy.ID {
		t.Fatalf("ClaimNext() returned %q, want the only healthy Turn %q", got.Turn.ID, healthy.ID)
	}
	if _, err := store.ClaimNext(context.Background(), claimNextCommand("attempt_skip_again")); !errors.Is(err, ErrNoClaimableTurn) {
		t.Fatalf("second ClaimNext() error = %v, want ErrNoClaimableTurn", err)
	}
	// Refusing them must not have written speculative Attempts.
	for _, refused := range []Turn{cancelled, terminal, exhausted} {
		if count := executionTableCount(t, db, SQLTurnAttemptTable, "turn_id = ?", refused.ID); count != 0 {
			t.Fatalf("refused Turn %q persisted %d attempts", refused.ID, count)
		}
	}
}

func TestSQLClaimNextReclaimsExpiredLeaseAndReplaysBoundAttempt(t *testing.T) {
	_, store, clock, turns := newSQLClaimNextFixture(t, "lease")
	turn := turns[0]

	first, err := store.ClaimNext(context.Background(), claimNextCommand("attempt_lease_first"))
	if err != nil || first.Turn.ID != turn.ID {
		t.Fatalf("first ClaimNext() = %+v, %v", first, err)
	}

	// The same command re-issued after an unknown outcome must recover the
	// bound epoch instead of stranding this Turn and claiming another.
	replay, err := store.ClaimNext(context.Background(), claimNextCommand("attempt_lease_first"))
	if err != nil {
		t.Fatalf("replay ClaimNext(): %v", err)
	}
	if !replay.Replay || replay.Turn.ID != turn.ID || replay.Attempt.FencingToken != first.Attempt.FencingToken {
		t.Fatalf("replay ClaimNext() = %+v, want the bound Attempt of %q", replay, turn.ID)
	}

	// A live lease leaves no claimable work for a different worker.
	if _, err := store.ClaimNext(context.Background(), claimNextCommand("attempt_lease_busy")); !errors.Is(err, ErrNoClaimableTurn) {
		t.Fatalf("busy ClaimNext() error = %v, want ErrNoClaimableTurn", err)
	}

	clock.Set(first.Attempt.LeaseExpiresAt)
	reclaimed, err := store.ClaimNext(context.Background(), claimNextCommand("attempt_lease_second"))
	if err != nil {
		t.Fatalf("reclaim ClaimNext(): %v", err)
	}
	if !reclaimed.Claimed || !reclaimed.Reclaimed || reclaimed.Turn.ID != turn.ID {
		t.Fatalf("reclaim ClaimNext() = %+v", reclaimed)
	}
	if reclaimed.Attempt.FencingToken != first.Attempt.FencingToken+1 {
		t.Fatalf("reclaim fence = %d, want %d", reclaimed.Attempt.FencingToken, first.Attempt.FencingToken+1)
	}
	if _, err := store.HeartbeatAttempt(context.Background(), HeartbeatAttemptCommand{Fence: first.Attempt.Fence()}); !errors.Is(err, ErrAttemptFenced) {
		t.Fatalf("superseded HeartbeatAttempt() error = %v, want ErrAttemptFenced", err)
	}
}

func TestSQLClaimNextConcurrentWorkersNeverShareATurn(t *testing.T) {
	const turnCount = 6
	const workerCount = 24
	suffixes := make([]string, 0, turnCount)
	for index := 0; index < turnCount; index++ {
		suffixes = append(suffixes, fmt.Sprintf("race_%02d", index))
	}
	db, store, _, turns := newSQLClaimNextFixture(t, suffixes...)

	type outcome struct {
		result ClaimAttemptResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, workerCount)
	var wait sync.WaitGroup
	for index := 0; index < workerCount; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			result, err := store.ClaimNext(context.Background(), claimNextCommand(fmt.Sprintf("attempt_race_%02d", index)))
			outcomes <- outcome{result: result, err: err}
		}(index)
	}
	close(start)
	wait.Wait()
	close(outcomes)

	claimedTurns := make(map[agentv1.TurnID]int)
	empty := 0
	for got := range outcomes {
		switch {
		case got.err == nil:
			if !got.result.Claimed || got.result.Replay {
				t.Errorf("winning ClaimNext() = %+v, want a fresh claim", got.result)
			}
			claimedTurns[got.result.Turn.ID]++
		case errors.Is(got.err, ErrNoClaimableTurn):
			empty++
		default:
			t.Errorf("ClaimNext() error = %v, want nil or ErrNoClaimableTurn", got.err)
		}
	}
	if len(claimedTurns) != turnCount || empty != workerCount-turnCount {
		t.Fatalf("claimed %d distinct turns with %d empty results, want %d and %d",
			len(claimedTurns), empty, turnCount, workerCount-turnCount)
	}
	for turnID, count := range claimedTurns {
		if count != 1 {
			t.Fatalf("turn %q was claimed %d times", turnID, count)
		}
	}
	for _, turn := range turns {
		if got := executionTableCount(t, db, SQLTurnAttemptTable, "turn_id = ?", turn.ID); got != 1 {
			t.Fatalf("turn %q holds %d attempts, want exactly one live epoch", turn.ID, got)
		}
	}
}

func TestSQLListReclaimableTurnsReportsOnlyStuckWork(t *testing.T) {
	_, store, clock, turns := newSQLClaimNextFixture(t, "expired", "cancel_pending", "cancel_live", "healthy", "done")
	expired, cancelPending, cancelLive, _, done := turns[0], turns[1], turns[2], turns[3], turns[4]

	// 1. An Attempt whose lease has lapsed.
	expiredClaim, err := store.ClaimAttempt(context.Background(), executionClaimCommand(expired.ID, "attempt_expired"))
	if err != nil {
		t.Fatal(err)
	}
	// 2. A cancellation with no Attempt to observe it.
	if _, err := store.RequestCancel(context.Background(), cancelPending.PrincipalID, cancelPending.ThreadID, cancelPending.ID,
		clock.Get(), sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"cancellationRequested":true}`)); err != nil {
		t.Fatal(err)
	}
	// 3. A cancellation whose live Attempt is expected to retire it itself.
	// It is claimed mid-way through the first lease so that its own lease
	// outlives the expiry checked below; otherwise both would lapse together
	// and this case would silently become another lease_expired row.
	clock.Set(clock.Get().Add(DefaultAttemptLeaseTTL / 2))
	if _, err := store.ClaimAttempt(context.Background(), executionClaimCommand(cancelLive.ID, "attempt_cancel_live")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RequestCancel(context.Background(), cancelLive.PrincipalID, cancelLive.ThreadID, cancelLive.ID,
		clock.Get(), sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"cancellationRequested":true}`)); err != nil {
		t.Fatal(err)
	}
	// 4. A terminal Turn is never reclaimable.
	if _, err := store.Transition(context.Background(), done.ID, agentv1.TurnStatusFailed, clock.Get(),
		sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"status":"failed"}`)); err != nil {
		t.Fatal(err)
	}

	// Before expiry only the observer-less cancellation is stuck.
	before, err := store.ListReclaimableTurns(context.Background(), ReclaimQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 || before[0].TurnID != cancelPending.ID || before[0].Reason != ReclaimReasonCancellationPending {
		t.Fatalf("pre-expiry reclaimable = %+v, want only %q as cancellation_pending", before, cancelPending.ID)
	}
	if before[0].CancelRequestedAt == nil || before[0].AttemptID != "" {
		t.Fatalf("cancellation_pending row carries attempt state: %+v", before[0])
	}

	clock.Set(expiredClaim.Attempt.LeaseExpiresAt)
	after, err := store.ListReclaimableTurns(context.Background(), ReclaimQuery{})
	if err != nil {
		t.Fatal(err)
	}
	reasons := make(map[agentv1.TurnID]ReclaimReason, len(after))
	for _, entry := range after {
		reasons[entry.TurnID] = entry.Reason
	}
	if len(after) != 2 ||
		reasons[expired.ID] != ReclaimReasonLeaseExpired ||
		reasons[cancelPending.ID] != ReclaimReasonCancellationPending {
		t.Fatalf("post-expiry reclaimable = %+v, want %q lease_expired and %q cancellation_pending",
			after, expired.ID, cancelPending.ID)
	}
	for _, entry := range after {
		if entry.Reason != ReclaimReasonLeaseExpired {
			continue
		}
		if entry.AttemptID != expiredClaim.Attempt.ID || entry.WorkerID != expiredClaim.Attempt.WorkerID ||
			entry.LeaseExpiresAt == nil || entry.FencingToken != expiredClaim.Attempt.FencingToken {
			t.Fatalf("lease_expired row = %+v, want the expired Attempt of %q", entry, expired.ID)
		}
	}

	// Discovery grants no rights: listing must not mutate anything.
	repeat, err := store.ListReclaimableTurns(context.Background(), ReclaimQuery{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(repeat) != 1 {
		t.Fatalf("limited reclaimable page = %d rows, want 1", len(repeat))
	}
	again, err := store.ListReclaimableTurns(context.Background(), ReclaimQuery{})
	if err != nil || len(again) != len(after) {
		t.Fatalf("repeat ListReclaimableTurns() = %d rows, %v, want %d unchanged", len(again), err, len(after))
	}
}

func TestSQLClaimNextRejectsInvalidCommands(t *testing.T) {
	_, store, _, _ := newSQLClaimNextFixture(t, "invalid")
	for name, command := range map[string]ClaimNextCommand{
		"missing attempt id": {WorkerID: "worker", WorkerBuildDigest: "sha256:build"},
		"missing worker id":  {AttemptID: "attempt", WorkerBuildDigest: "sha256:build"},
		"missing digest":     {AttemptID: "attempt", WorkerID: "worker"},
		"negative scan":      {AttemptID: "attempt", WorkerID: "worker", WorkerBuildDigest: "sha256:build", ScanLimit: -1},
		"oversized scan":     {AttemptID: "attempt", WorkerID: "worker", WorkerBuildDigest: "sha256:build", ScanLimit: MaxClaimNextScanLimit + 1},
	} {
		if _, err := store.ClaimNext(context.Background(), command); err == nil {
			t.Fatalf("%s: ClaimNext() error = nil, want validation failure", name)
		}
	}
	if err := (ReclaimQuery{Limit: MaxReclaimScanLimit + 1}).Validate(); err == nil {
		t.Fatal("oversized ReclaimQuery accepted")
	}
}
