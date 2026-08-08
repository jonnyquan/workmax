package agentturn

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"gorm.io/gorm"

	agentv1 "server/contracts/agent/v1"
)

type completedUsageTestFixture struct {
	db        *gorm.DB
	store     *SQLStore
	clock     *sqlExecutionTestClock
	turn      Turn
	authority *testSettlementReviewAuthority
	fence     AttemptFence
}

func newCompletedUsageTestFixture(t *testing.T, suffix string) completedUsageTestFixture {
	t.Helper()
	db, store, clock, turns := newSQLClaimNextFixture(t, "completed_usage_"+suffix)
	authority := newTestSettlementReviewAuthority(t, db)
	if binding, err := store.BindSettlementReviewUsageAuthority(authority); err != nil || binding == nil {
		t.Fatalf("BindSettlementReviewUsageAuthority() = %p, %v", binding, err)
	}
	claimed, err := store.ClaimAttempt(
		context.Background(), executionClaimCommand(turns[0].ID, "attempt_completed_usage_"+suffix),
	)
	if err != nil {
		t.Fatal(err)
	}
	return completedUsageTestFixture{
		db: db, store: store, clock: clock, turn: turns[0], authority: authority,
		fence: claimed.Attempt.Fence(),
	}
}

func TestCompletedUsageZeroAssertionsOpenTrustedReviewAndResolve(t *testing.T) {
	tests := []struct {
		name       string
		settlement *SettlementRequest
		withEffect bool
	}{
		{name: "nil"},
		{name: "default", settlement: &SettlementRequest{}},
		{name: "finalize_zero_with_effect", settlement: &SettlementRequest{Intent: SettlementIntentFinalize}, withEffect: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCompletedUsageTestFixture(t, test.name)
			command := CommitAttemptCommand{
				Fence: fixture.fence, OperationID: "operation_completed_usage_" + test.name,
				TerminalStatus: agentv1.TurnStatusCompleted, Settlement: test.settlement,
			}
			if test.withEffect {
				command.Effects = []EffectOutboxDraft{executionTestEffect(
					"outbox_completed_usage_"+test.name,
					"writer.document.publish", "completed-usage-"+test.name, fixture.clock.Get(),
				)}
			}

			result, err := fixture.store.CommitAttempt(context.Background(), command)
			if err != nil || result.Replay || result.SettlementReview == nil {
				t.Fatalf("CommitAttempt() = %+v, %v", result, err)
			}
			review := *result.SettlementReview
			if review.Source != SettlementReviewSourceExecutorCompletion ||
				review.Reason != SettlementReviewReasonCompletedUsageUnmeasured ||
				review.TerminalStatus != agentv1.TurnStatusCompleted ||
				review.Status != SettlementReviewStatusPending ||
				review.Evidence.PriorOperationCount != 0 || review.Evidence.PriorEffectCount != 0 ||
				review.Evidence.CurrentEffectCount != len(command.Effects) {
				t.Fatalf("completed Review = %+v", review)
			}
			if err := review.Validate(); err != nil {
				t.Fatalf("completed Review invalid: %v", err)
			}
			if calls := fixture.authority.committed(); len(calls) != 0 {
				t.Fatalf("completed Review called Settle: %+v", calls)
			}
			if holds := fixture.authority.held(); len(holds) != 1 || holds[0].Review != review {
				t.Fatalf("completed Review holds = %+v", holds)
			}
			for _, effect := range result.Effects {
				if effect.Status != string(EffectStatusReviewHold) {
					t.Fatalf("completed Effect status = %q", effect.Status)
				}
				assertEffectReviewHeld(t, fixture.db, effect.OutboxID)
			}
			var marker struct {
				SettlementReviewID     string `json:"settlementReviewId"`
				SettlementReviewDigest string `json:"settlementReviewDigest"`
			}
			if err := json.Unmarshal(result.Event.Data, &marker); err != nil ||
				marker.SettlementReviewID != review.ReviewID || marker.SettlementReviewDigest != review.RequestDigest {
				t.Fatalf("terminal Review marker = %+v, %v", marker, err)
			}
			_, legacyDigest, err := normalizeCommitCommand(command)
			if err != nil || result.OperationDigest == legacyDigest {
				t.Fatalf("completed digest = %q, legacy = %q, err = %v", result.OperationDigest, legacyDigest, err)
			}

			replay, err := fixture.store.CommitAttempt(context.Background(), command)
			if err != nil || !replay.Replay || replay.OperationDigest != result.OperationDigest ||
				replay.SettlementReview == nil || *replay.SettlementReview != review {
				t.Fatalf("completed exact replay = %+v, %v", replay, err)
			}
			if len(fixture.authority.held()) != 1 || len(fixture.authority.committed()) != 0 {
				t.Fatal("completed exact replay repeated a commercial call")
			}

			asserted := command
			asserted.Settlement = &SettlementRequest{Intent: SettlementIntentFinalize, UsedUnits: 1}
			if _, err := fixture.store.CommitAttempt(context.Background(), asserted); !errors.Is(err, ErrSettlementCompletedUsageUntrusted) {
				t.Fatalf("positive assertion against v3 receipt = %v", err)
			}

			if test.name == "nil" {
				_, err := fixture.store.CaptureSettlementReviewUsageEvidence(
					context.Background(), settlementReviewUsageCommand(review),
				)
				if !errors.Is(err, ErrSettlementReviewUsageUnavailable) {
					t.Fatalf("historical v3 CaptureSettlementReviewUsageEvidence() = %v", err)
				}
			}
		})
	}
}

func TestCompletedUsageCallerAssertionsFailBeforeMutation(t *testing.T) {
	fixture := newCompletedUsageTestFixture(t, "caller_assertions")
	var before executionTestTurnState
	executionTakeTurnState(t, fixture.db, fixture.turn.ID, &before)
	beforeEvents := executionTableCount(t, fixture.db, SQLTurnEventTable, "turn_id = ?", fixture.turn.ID)

	tests := []struct {
		name       string
		settlement SettlementRequest
	}{
		{name: "release", settlement: SettlementRequest{Intent: SettlementIntentRelease}},
		{name: "default_positive", settlement: SettlementRequest{UsedUnits: 1}},
		{name: "finalize_positive", settlement: SettlementRequest{Intent: SettlementIntentFinalize, UsedUnits: 9}},
	}
	for _, test := range tests {
		_, err := fixture.store.CommitAttempt(context.Background(), CommitAttemptCommand{
			Fence: fixture.fence, OperationID: "operation_untrusted_" + test.name,
			TerminalStatus: agentv1.TurnStatusCompleted, Settlement: &test.settlement,
		})
		if !errors.Is(err, ErrSettlementCompletedUsageUntrusted) {
			t.Fatalf("%s assertion = %v", test.name, err)
		}
	}

	var after executionTestTurnState
	executionTakeTurnState(t, fixture.db, fixture.turn.ID, &after)
	assertSettlementGuardTurnUnchanged(t, before, after)
	if got := executionTableCount(t, fixture.db, SQLTurnEventTable, "turn_id = ?", fixture.turn.ID); got != beforeEvents {
		t.Fatalf("event count = %d, want %d", got, beforeEvents)
	}
	for _, table := range []string{SQLTurnOperationTable, SQLEffectOutboxTable, SQLSettlementReviewTable} {
		if count := executionTableCount(t, fixture.db, table, "turn_id = ?", fixture.turn.ID); count != 0 {
			t.Fatalf("%s count = %d after rejected assertions", table, count)
		}
	}
	if len(fixture.authority.held()) != 0 || len(fixture.authority.committed()) != 0 {
		t.Fatal("rejected caller assertion reached the commercial authority")
	}
}

func TestCompletedUsageReviewHoldFailureRollsBackTerminalCommit(t *testing.T) {
	fixture := newCompletedUsageTestFixture(t, "hold_failure")
	fixture.authority.setHoldFailure(errors.New("secret provider hold failure"))
	var before executionTestTurnState
	executionTakeTurnState(t, fixture.db, fixture.turn.ID, &before)
	beforeEvents := executionTableCount(t, fixture.db, SQLTurnEventTable, "turn_id = ?", fixture.turn.ID)
	operationID := "operation_completed_usage_hold_failure"
	outboxID := "outbox_completed_usage_hold_failure"

	_, err := fixture.store.CommitAttempt(context.Background(), CommitAttemptCommand{
		Fence: fixture.fence, OperationID: operationID, TerminalStatus: agentv1.TurnStatusCompleted,
		Effects: []EffectOutboxDraft{executionTestEffect(
			outboxID, "writer.document.publish", "completed-usage-hold-failure", fixture.clock.Get(),
		)},
	})
	if !errors.Is(err, ErrSettlementReviewFailed) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("failed completed Review hold = %v", err)
	}
	var after executionTestTurnState
	executionTakeTurnState(t, fixture.db, fixture.turn.ID, &after)
	assertSettlementGuardTurnUnchanged(t, before, after)
	if got := executionTableCount(t, fixture.db, SQLTurnEventTable, "turn_id = ?", fixture.turn.ID); got != beforeEvents {
		t.Fatalf("event count = %d, want %d", got, beforeEvents)
	}
	if executionTableCount(t, fixture.db, SQLTurnOperationTable, "operation_id = ?", operationID) != 0 ||
		executionTableCount(t, fixture.db, SQLEffectOutboxTable, "outbox_id = ?", outboxID) != 0 ||
		executionTableCount(t, fixture.db, SQLSettlementReviewTable, "turn_id = ?", fixture.turn.ID) != 0 {
		t.Fatal("failed completed Review hold left terminal rows")
	}
	if len(fixture.authority.committed()) != 0 {
		t.Fatal("failed completed Review hold called Settle")
	}
}

func TestCompletedUsageCompatibilityBindingsRetainV2Settlement(t *testing.T) {
	tests := []struct {
		name string
		bind func(*SQLStore, *testSettlementReviewAuthority) error
	}{
		{name: "with_settlement_authority", bind: func(store *SQLStore, authority *testSettlementReviewAuthority) error {
			store.WithSettlementAuthority(authority)
			return nil
		}},
		{name: "review_only_bind", bind: func(store *SQLStore, authority *testSettlementReviewAuthority) error {
			_, err := store.BindSettlementReviewAuthority(authority)
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, store, _, turns := newSQLClaimNextFixture(t, "completed_usage_compat_"+test.name)
			authority := newTestSettlementReviewAuthority(t, db)
			if err := test.bind(store, authority); err != nil {
				t.Fatal(err)
			}
			claimed, err := store.ClaimAttempt(
				context.Background(), executionClaimCommand(turns[0].ID, "attempt_completed_usage_compat_"+test.name),
			)
			if err != nil {
				t.Fatal(err)
			}
			command := CommitAttemptCommand{
				Fence: claimed.Attempt.Fence(), OperationID: "operation_completed_usage_compat_" + test.name,
				TerminalStatus: agentv1.TurnStatusCompleted,
			}
			result, err := store.CommitAttempt(context.Background(), command)
			if err != nil || result.SettlementReview != nil || len(authority.held()) != 0 {
				t.Fatalf("compatibility completed commit = %+v, %v", result, err)
			}
			settled := authority.committed()
			if len(settled) != 1 || settled[0].Intent != SettlementIntentFinalize || settled[0].UsedUnits != 0 {
				t.Fatalf("compatibility settlement = %+v", settled)
			}
			_, legacyDigest, err := normalizeCommitCommand(command)
			if err != nil || result.OperationDigest != legacyDigest {
				t.Fatalf("compatibility digest = %q, want %q, err %v", result.OperationDigest, legacyDigest, err)
			}
		})
	}
}

func TestCompletedUsageHistoricalV2ReceiptReplaysWithoutReview(t *testing.T) {
	db, legacyStore, clock, turns := newSQLClaimNextFixture(t, "completed_usage_v2_replay")
	authority := newTestSettlementReviewAuthority(t, db)
	legacyStore.WithSettlementAuthority(authority)
	claimed, err := legacyStore.ClaimAttempt(
		context.Background(), executionClaimCommand(turns[0].ID, "attempt_completed_usage_v2_replay"),
	)
	if err != nil {
		t.Fatal(err)
	}
	command := CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_completed_usage_v2_replay",
		TerminalStatus: agentv1.TurnStatusCompleted,
		Settlement:     &SettlementRequest{Intent: SettlementIntentFinalize, UsedUnits: 4},
	}
	legacy, err := legacyStore.CommitAttempt(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	_, legacyDigest, err := normalizeCommitCommand(command)
	if err != nil || legacy.OperationDigest != legacyDigest {
		t.Fatalf("legacy write digest = %q, want %q, err %v", legacy.OperationDigest, legacyDigest, err)
	}

	exactStore := mustSQLStore(t, db)
	exactStore.executionClock = clock.Now
	if _, err := exactStore.BindSettlementReviewUsageAuthority(authority); err != nil {
		t.Fatal(err)
	}
	replay, err := exactStore.CommitAttempt(context.Background(), command)
	if err != nil || !replay.Replay || replay.OperationDigest != legacy.OperationDigest || replay.SettlementReview != nil {
		t.Fatalf("historical v2 replay = %+v, %v", replay, err)
	}
	if len(authority.committed()) != 1 || len(authority.held()) != 0 ||
		executionTableCount(t, db, SQLSettlementReviewTable, "turn_id = ?", turns[0].ID) != 0 {
		t.Fatal("historical v2 replay performed a new commercial mutation")
	}
}

func TestCompletedUsageV3ReplayRejectsReceiptTampering(t *testing.T) {
	t.Run("review digest", func(t *testing.T) {
		fixture := newCompletedUsageTestFixture(t, "tamper_review")
		command := CommitAttemptCommand{
			Fence: fixture.fence, OperationID: "operation_completed_usage_tamper_review",
			TerminalStatus: agentv1.TurnStatusCompleted,
		}
		if _, err := fixture.store.CommitAttempt(context.Background(), command); err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.Table(SQLSettlementReviewTable).Where("turn_id = ?", fixture.turn.ID).
			UpdateColumn("request_digest", "sha256:"+strings.Repeat("0", 64)).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.store.CommitAttempt(context.Background(), command); !errors.Is(err, ErrStoreIntegrity) {
			t.Fatalf("Review digest tamper replay = %v", err)
		}
	})

	t.Run("operation downgrade to v2", func(t *testing.T) {
		fixture := newCompletedUsageTestFixture(t, "tamper_v2_downgrade")
		command := CommitAttemptCommand{
			Fence: fixture.fence, OperationID: "operation_completed_usage_tamper_v2_downgrade",
			TerminalStatus: agentv1.TurnStatusCompleted,
		}
		if _, err := fixture.store.CommitAttempt(context.Background(), command); err != nil {
			t.Fatal(err)
		}
		_, legacyDigest, err := normalizeCommitCommand(command)
		if err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.Table(SQLTurnOperationTable).
			Where("turn_id = ? AND operation_id = ?", fixture.turn.ID, command.OperationID).
			UpdateColumn("operation_digest", legacyDigest).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.store.CommitAttempt(context.Background(), command); !errors.Is(err, ErrStoreIntegrity) {
			t.Fatalf("v3-to-v2 downgrade replay = %v", err)
		}
	})
}

func TestCompletedUsageV3RejectsTerminalizationPolicyMutation(t *testing.T) {
	command := CommitAttemptCommand{
		Fence:       AttemptFence{TurnID: "turn_digest", AttemptID: "attempt_digest", FencingToken: 1},
		OperationID: "operation_digest", TerminalStatus: agentv1.TurnStatusCompleted,
	}
	_, legacyDigest, err := normalizeCommitCommand(command)
	if err != nil {
		t.Fatal(err)
	}
	base := newCompletedUsageTerminalization()
	_, baseDigest, err := normalizeCompletedUsageCommitCommand(command, base)
	if err != nil || baseDigest == legacyDigest {
		t.Fatalf("v3 digest = %q, legacy = %q, err = %v", baseDigest, legacyDigest, err)
	}
	mutations := []commitDigestTerminalization{
		func() commitDigestTerminalization { value := base; value.Mode += "_changed"; return value }(),
		func() commitDigestTerminalization {
			value := base
			value.Source = SettlementReviewSourceExecutor
			return value
		}(),
		func() commitDigestTerminalization {
			value := base
			value.Reason = SettlementReviewReasonUsageUnknown
			return value
		}(),
		func() commitDigestTerminalization {
			value := base
			value.PolicyDigest = testSettlementReviewDigest("changed-policy")
			return value
		}(),
	}
	for index, mutation := range mutations {
		_, digest, err := normalizeCompletedUsageCommitCommand(command, mutation)
		if !errors.Is(err, ErrStoreIntegrity) || digest != "" {
			t.Fatalf("terminalization mutation %d digest = %q, base %q, err %v; want exact-policy rejection", index, digest, baseDigest, err)
		}
	}
}
