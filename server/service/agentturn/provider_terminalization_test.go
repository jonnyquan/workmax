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

type providerTerminalizationFixture struct {
	db        *gorm.DB
	store     *SQLStore
	clock     *sqlExecutionTestClock
	turn      Turn
	authority *testSettlementReviewAuthority
	provider  settlementReviewProviderUsageTestScope
	attempt   TurnAttempt
}

func newProviderTerminalizationFixture(t *testing.T, suffix string) providerTerminalizationFixture {
	t.Helper()
	db, store, clock, turns := newSQLClaimNextFixture(t, "provider_terminal_"+suffix)
	authority := newTestSettlementReviewAuthority(t, db)
	provider := newSettlementReviewProviderUsageTestScope(
		t, db, store, clock, turns[0].Plugin, "provider_terminal_"+suffix,
	)
	if binding, err := store.BindSettlementReviewProviderUsageAuthority(
		provider.journal, authority,
	); err != nil || binding == nil {
		t.Fatalf("BindSettlementReviewProviderUsageAuthority() = %p, %v", binding, err)
	}
	claimed, err := store.ClaimAttempt(
		context.Background(), executionClaimCommand(turns[0].ID, "attempt_provider_terminal_"+suffix),
	)
	if err != nil {
		t.Fatal(err)
	}
	return providerTerminalizationFixture{
		db: db, store: store, clock: clock, turn: turns[0], authority: authority,
		provider: provider, attempt: claimed.Attempt,
	}
}

func (fixture providerTerminalizationFixture) requestCancellation(t *testing.T) {
	t.Helper()
	fixture.clock.Set(fixture.clock.Get().Add(time.Second))
	if _, err := fixture.store.RequestCancel(
		context.Background(), fixture.turn.PrincipalID, fixture.turn.ThreadID, fixture.turn.ID,
		fixture.clock.Get(), sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"cancellationRequested":true}`),
	); err != nil {
		t.Fatal(err)
	}
	fixture.clock.Set(fixture.clock.Get().Add(time.Second))
}

func TestProviderUsageV4EveryTerminalUsesOneZeroMeasurementIdentity(t *testing.T) {
	for _, terminal := range []agentv1.TurnStatus{
		agentv1.TurnStatusCompleted,
		agentv1.TurnStatusStopped,
		agentv1.TurnStatusFailed,
		agentv1.TurnStatusTimeout,
	} {
		t.Run(string(terminal), func(t *testing.T) {
			fixture := newProviderTerminalizationFixture(t, string(terminal))
			if terminal == agentv1.TurnStatusStopped {
				fixture.requestCancellation(t)
			}
			command := CommitAttemptCommand{
				Fence: fixture.attempt.Fence(), OperationID: "operation_provider_v4_" + string(terminal),
				TerminalStatus: terminal,
			}
			first, err := fixture.store.CommitAttempt(context.Background(), command)
			if err != nil || first.Replay || first.SettlementReview == nil {
				t.Fatalf("first v4 terminal = %+v, %v", first, err)
			}
			review := *first.SettlementReview
			wantSource := SettlementReviewSourceExecutorTerminal
			wantReason := SettlementReviewReasonTerminalUsageUnmeasured
			if terminal == agentv1.TurnStatusCompleted {
				wantSource = SettlementReviewSourceExecutorCompletion
				wantReason = SettlementReviewReasonCompletedUsageUnmeasured
			}
			if review.Source != wantSource || review.Reason != wantReason ||
				review.TerminalStatus != terminal || review.Status != SettlementReviewStatusPending ||
				review.RequestDigest != settlementReviewRequestDigestV2(review) ||
				review.Evidence != (SettlementUsageEvidence{}) || !settlementReviewProviderUsageAware(review) {
				t.Fatalf("v4 Review = %+v", review)
			}
			if settled := fixture.authority.committed(); len(settled) != 0 {
				t.Fatalf("v4 terminal called Settle: %+v", settled)
			}
			if holds := fixture.authority.held(); len(holds) != 1 || holds[0].Review != review {
				t.Fatalf("v4 holds = %+v", holds)
			}

			terminalization, err := newProviderUsageTerminalization(terminal)
			if err != nil || terminalization.Mode != providerUsageTerminalizationMode {
				t.Fatalf("provider terminalization = %+v, %v", terminalization, err)
			}
			_, wantDigest, err := normalizeProviderUsageCommitCommand(command, terminalization)
			_, v2Digest, v2Err := normalizeCommitCommand(command)
			if err != nil || v2Err != nil || first.OperationDigest != wantDigest || wantDigest == v2Digest {
				t.Fatalf("v4 digest = %q, want %q, v2 %q, errors %v/%v", first.OperationDigest, wantDigest, v2Digest, err, v2Err)
			}

			var marker reconcileEventData
			if err := json.Unmarshal(first.Event.Data, &marker); err != nil || marker.Reconciled ||
				marker.Status != terminal || marker.SettlementReviewID != review.ReviewID ||
				marker.SettlementReviewDigest != review.RequestDigest {
				t.Fatalf("v4 event marker = %+v, %v", marker, err)
			}

			// Nil, default and explicit Finalize(0) are one v4 command even for
			// failed/stopped/timeout, whose historical v2 default meant Release.
			for _, settlement := range []*SettlementRequest{
				{}, {Intent: SettlementIntentFinalize},
			} {
				retry := command
				retry.Settlement = settlement
				replay, err := fixture.store.CommitAttempt(context.Background(), retry)
				if err != nil || !replay.Replay || replay.OperationDigest != first.OperationDigest ||
					replay.SettlementReview == nil || *replay.SettlementReview != review {
					t.Fatalf("zero measurement replay (%+v) = %+v, %v", settlement, replay, err)
				}
			}
			if len(fixture.authority.held()) != 1 || len(fixture.authority.committed()) != 0 {
				t.Fatal("v4 exact replay repeated a commercial mutation")
			}
		})
	}
}

func TestOperationDigestVersionsRejectCrossModeTerminalization(t *testing.T) {
	command := CommitAttemptCommand{
		Fence: AttemptFence{
			TurnID: "turn_version_domain", AttemptID: "attempt_version_domain", FencingToken: 1,
		},
		OperationID: "operation_version_domain", TerminalStatus: agentv1.TurnStatusCompleted,
	}
	completed := newCompletedUsageTerminalization()
	provider, err := newProviderUsageTerminalization(command.TerminalStatus)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name            string
		version         string
		terminalization *commitDigestTerminalization
	}{
		{name: "v2_with_completed", version: operationDigestVersionV2, terminalization: &completed},
		{name: "v2_with_provider", version: operationDigestVersionV2, terminalization: &provider},
		{name: "v3_without_mode", version: operationDigestVersionV3},
		{name: "v3_with_provider", version: operationDigestVersionV3, terminalization: &provider},
		{name: "v4_without_mode", version: operationDigestVersionV4},
		{name: "v4_with_completed", version: operationDigestVersionV4, terminalization: &completed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, digest, err := normalizeCommitCommandWithVersion(command, test.terminalization, test.version)
			if !errors.Is(err, ErrStoreIntegrity) || digest != "" {
				t.Fatalf("mixed version/mode digest = %q, err %v", digest, err)
			}
		})
	}
	failed := command
	failed.TerminalStatus = agentv1.TurnStatusFailed
	if _, digest, err := normalizeCommitCommandWithVersion(
		failed, &completed, operationDigestVersionV3,
	); !errors.Is(err, ErrStoreIntegrity) || digest != "" {
		t.Fatalf("non-completed v3 digest = %q, err %v", digest, err)
	}
}

func TestProviderUsageV4RejectsCallerCommercialAssertionsBeforeMutation(t *testing.T) {
	for _, terminal := range []agentv1.TurnStatus{
		agentv1.TurnStatusCompleted, agentv1.TurnStatusStopped,
		agentv1.TurnStatusFailed, agentv1.TurnStatusTimeout,
	} {
		t.Run(string(terminal), func(t *testing.T) {
			fixture := newProviderTerminalizationFixture(t, "untrusted_"+string(terminal))
			if terminal == agentv1.TurnStatusStopped {
				fixture.requestCancellation(t)
			}
			assertions := []SettlementRequest{
				{Intent: SettlementIntentRelease},
				{UsedUnits: 1},
				{Intent: SettlementIntentFinalize, UsedUnits: 7},
			}
			for index, assertion := range assertions {
				_, err := fixture.store.CommitAttempt(context.Background(), CommitAttemptCommand{
					Fence: fixture.attempt.Fence(), OperationID: "operation_provider_untrusted_" + string(rune('a'+index)),
					TerminalStatus: terminal, Settlement: &assertion,
				})
				if !errors.Is(err, ErrSettlementCompletedUsageUntrusted) {
					t.Fatalf("assertion %+v = %v", assertion, err)
				}
			}
			for _, table := range []string{SQLTurnOperationTable, SQLSettlementReviewTable} {
				if count := executionTableCount(t, fixture.db, table, "turn_id = ?", fixture.turn.ID); count != 0 {
					t.Fatalf("%s count after rejected assertions = %d", table, count)
				}
			}
			if len(fixture.authority.held()) != 0 || len(fixture.authority.committed()) != 0 {
				t.Fatal("rejected v4 assertion reached the commercial authority")
			}
		})
	}
}

func TestProviderUsageV4FreezesJournalCountAndDetectsTamper(t *testing.T) {
	fixture := newProviderTerminalizationFixture(t, "journal_count")
	receipt, err := fixture.provider.recorder.AppendAttested(
		context.Background(), AppendAttestedProviderUsageCommand{
			Fence: fixture.attempt.Fence(), ProviderRequestDigest: providerUsageTestDigest("v4-request"),
			ProviderEventDigest:   providerUsageTestDigest("v4-event"),
			CanonicalUsageJSON:    []byte(`{"inputTokens":3,"outputTokens":5}`),
			ProviderReceiptDigest: providerUsageTestDigest("v4-receipt"),
			AttestationDigest:     providerUsageTestDigest("v4-attestation"),
			ProviderReportedAt:    fixture.clock.Get(),
		},
	)
	if err != nil || receipt.Replay {
		t.Fatalf("AppendAttested() = %+v, %v", receipt, err)
	}
	command := CommitAttemptCommand{
		Fence: fixture.attempt.Fence(), OperationID: "operation_provider_v4_journal_count",
		TerminalStatus: agentv1.TurnStatusFailed,
	}
	committed, err := fixture.store.CommitAttempt(context.Background(), command)
	if err != nil || committed.SettlementReview == nil ||
		committed.SettlementReview.Evidence.PriorProviderUsageCount != 1 {
		t.Fatalf("v4 provider count Review = %+v, %v", committed, err)
	}
	legacyCommand, legacyDigest, err := normalizeCommitCommand(command)
	if err != nil {
		t.Fatal(err)
	}
	terminalization, err := newProviderUsageTerminalization(command.TerminalStatus)
	if err != nil {
		t.Fatal(err)
	}
	v4Command, v4Digest, err := normalizeProviderUsageCommitCommand(command, terminalization)
	if err != nil {
		t.Fatal(err)
	}
	recovered, found, err := fixture.store.resolveOperation(context.Background(), []commitOperationCandidate{
		{Command: v4Command, Digest: v4Digest, Mode: operationTerminalizationProviderUsageReview},
		{Command: legacyCommand, Digest: legacyDigest, Mode: operationTerminalizationLegacy},
	}, false)
	if err != nil || !found || !recovered.Replay || recovered.OperationDigest != committed.OperationDigest ||
		recovered.SettlementReview == nil || *recovered.SettlementReview != *committed.SettlementReview {
		t.Fatalf("v4 unknown-commit recovery = found:%v result:%+v err:%v", found, recovered, err)
	}
	if replay, err := fixture.store.CommitAttempt(context.Background(), command); err != nil || !replay.Replay {
		t.Fatalf("v4 provider count replay = %+v, %v", replay, err)
	}
	if err := fixture.db.Table(SQLProviderUsageJournalTable).
		Where("receipt_id = ?", receipt.Record.ReceiptID).
		Delete(&sqlProviderUsageJournalRow{}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.CommitAttempt(context.Background(), command); !errors.Is(err, ErrStoreIntegrity) {
		t.Fatalf("replay after Provider receipt deletion = %v", err)
	}
}

func TestProviderUsageV4OverflowReviewStillReplaysExactTerminal(t *testing.T) {
	fixture := newProviderTerminalizationFixture(t, "overflow_replay")
	for index := 0; index <= MaxProviderUsageSources; index++ {
		event := fmt.Sprintf("overflow-%03d", index)
		result, err := fixture.provider.recorder.AppendAttested(
			context.Background(), AppendAttestedProviderUsageCommand{
				Fence: fixture.attempt.Fence(), ProviderRequestDigest: providerUsageTestDigest("overflow-request", event),
				ProviderEventDigest:   providerUsageTestDigest("overflow-event", event),
				CanonicalUsageJSON:    []byte(`{"inputTokens":1,"outputTokens":1}`),
				ProviderReceiptDigest: providerUsageTestDigest("overflow-receipt", event),
				AttestationDigest:     providerUsageTestDigest("overflow-attestation", event),
				ProviderReportedAt:    fixture.clock.Get(),
			},
		)
		if err != nil || result.Replay {
			t.Fatalf("append overflow receipt %d = %+v, %v", index, result, err)
		}
	}
	command := CommitAttemptCommand{
		Fence: fixture.attempt.Fence(), OperationID: "operation_provider_v4_overflow_replay",
		TerminalStatus: agentv1.TurnStatusFailed,
	}
	committed, err := fixture.store.CommitAttempt(context.Background(), command)
	if err != nil || committed.SettlementReview == nil ||
		committed.SettlementReview.Evidence.PriorProviderUsageCount != int64(MaxProviderUsageSources+1) {
		t.Fatalf("overflow terminal Review = %+v, %v", committed, err)
	}
	replay, err := fixture.store.CommitAttempt(context.Background(), command)
	if err != nil || !replay.Replay || replay.SettlementReview == nil ||
		*replay.SettlementReview != *committed.SettlementReview {
		t.Fatalf("overflow terminal exact replay = %+v, %v", replay, err)
	}
}

func TestProviderUsageV4ReplaysHistoricalV3ThenV2ByOriginalMode(t *testing.T) {
	t.Run("completed v3", func(t *testing.T) {
		db, store, clock, turns := newSQLClaimNextFixture(t, "provider_v4_v3_fallback")
		authority := newTestSettlementReviewAuthority(t, db)
		if _, err := store.BindSettlementReviewUsageAuthority(authority); err != nil {
			t.Fatal(err)
		}
		claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turns[0].ID, "attempt_provider_v3_fallback"))
		if err != nil {
			t.Fatal(err)
		}
		command := CommitAttemptCommand{
			Fence: claimed.Attempt.Fence(), OperationID: "operation_provider_v3_fallback",
			TerminalStatus: agentv1.TurnStatusCompleted,
		}
		committed, err := store.CommitAttempt(context.Background(), command)
		if err != nil || committed.SettlementReview == nil ||
			committed.SettlementReview.RequestDigest != settlementReviewRequestDigestV1(*committed.SettlementReview) {
			t.Fatalf("historical v3 commit = %+v, %v", committed, err)
		}
		restarted := mustSQLStore(t, db)
		restarted.executionClock = clock.Now
		journal, err := NewProviderUsageJournal(restarted)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := restarted.BindSettlementReviewProviderUsageAuthority(journal, authority); err != nil {
			t.Fatal(err)
		}
		replay, err := restarted.CommitAttempt(context.Background(), command)
		if err != nil || !replay.Replay || replay.OperationDigest != committed.OperationDigest ||
			replay.SettlementReview == nil || replay.SettlementReview.RequestDigest != settlementReviewRequestDigestV1(*replay.SettlementReview) {
			t.Fatalf("provider binding v3 fallback = %+v, %v", replay, err)
		}
	})

	for _, terminal := range []agentv1.TurnStatus{agentv1.TurnStatusCompleted, agentv1.TurnStatusFailed} {
		t.Run(string(terminal)+" v2", func(t *testing.T) {
			db, store, clock, turns := newSQLClaimNextFixture(t, "provider_v4_v2_fallback_"+string(terminal))
			claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(
				turns[0].ID, "attempt_provider_v2_fallback_"+string(terminal),
			))
			if err != nil {
				t.Fatal(err)
			}
			command := CommitAttemptCommand{
				Fence: claimed.Attempt.Fence(), OperationID: "operation_provider_v2_fallback_" + string(terminal),
				TerminalStatus: terminal,
			}
			committed, err := store.CommitAttempt(context.Background(), command)
			if err != nil || committed.SettlementReview != nil {
				t.Fatalf("historical v2 commit = %+v, %v", committed, err)
			}
			restarted := mustSQLStore(t, db)
			restarted.executionClock = clock.Now
			journal, err := NewProviderUsageJournal(restarted)
			if err != nil {
				t.Fatal(err)
			}
			authority := newTestSettlementReviewAuthority(t, db)
			if _, err := restarted.BindSettlementReviewProviderUsageAuthority(journal, authority); err != nil {
				t.Fatal(err)
			}
			replay, err := restarted.CommitAttempt(context.Background(), command)
			if err != nil || !replay.Replay || replay.OperationDigest != committed.OperationDigest || replay.SettlementReview != nil {
				t.Fatalf("provider binding v2 fallback = %+v, %v", replay, err)
			}
		})
	}
}

func TestProviderUsageReconcileAlwaysOpensTerminalReview(t *testing.T) {
	tests := []struct {
		name          string
		reason        ReclaimReason
		providerUsage bool
	}{
		{name: "stopped_empty", reason: ReclaimReasonCancellationPending},
		{name: "timeout_empty", reason: ReclaimReasonAttemptsExhausted},
		{name: "timeout_provider_receipt", reason: ReclaimReasonAttemptsExhausted, providerUsage: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, store, clock, turns := newSQLClaimNextFixture(t, "provider_reconcile_"+test.name)
			turn := turns[0]
			authority := newTestSettlementReviewAuthority(t, db)
			provider := newSettlementReviewProviderUsageTestScope(t, db, store, clock, turn.Plugin, "provider_reconcile_"+test.name)
			if _, err := store.BindSettlementReviewProviderUsageAuthority(provider.journal, authority); err != nil {
				t.Fatal(err)
			}
			if test.reason == ReclaimReasonCancellationPending {
				if _, err := store.RequestCancel(context.Background(), turn.PrincipalID, turn.ThreadID, turn.ID,
					clock.Get(), sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"cancellationRequested":true}`)); err != nil {
					t.Fatal(err)
				}
			} else if test.providerUsage {
				first, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turn.ID, "attempt_provider_reconcile_first"))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := provider.recorder.AppendAttested(context.Background(), AppendAttestedProviderUsageCommand{
					Fence: first.Attempt.Fence(), ProviderRequestDigest: providerUsageTestDigest("reconcile-request"),
					ProviderEventDigest:   providerUsageTestDigest("reconcile-event"),
					CanonicalUsageJSON:    []byte(`{"inputTokens":1,"outputTokens":2}`),
					ProviderReceiptDigest: providerUsageTestDigest("reconcile-receipt"),
					AttestationDigest:     providerUsageTestDigest("reconcile-attestation"), ProviderReportedAt: clock.Get(),
				}); err != nil {
					t.Fatal(err)
				}
				clock.Set(first.Attempt.LeaseExpiresAt)
				for attempt := 1; attempt < DefaultMaxTurnAttempts; attempt++ {
					claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(
						turn.ID, "attempt_provider_reconcile_"+string(rune('a'+attempt)),
					))
					if err != nil {
						t.Fatal(err)
					}
					clock.Set(claimed.Attempt.LeaseExpiresAt)
				}
			} else {
				exhaustTurnAttempts(t, store, clock, turn.ID, "attempt_provider_reconcile_empty_")
			}

			command := ReconcileCommand{
				TurnID: turn.ID, Reason: test.reason, ReconcilerID: "reconciler_provider_usage",
				ReconcilerBuildDigest: "sha256:reconciler-provider-usage",
			}
			result, err := store.ReconcileTerminal(context.Background(), command)
			if err != nil || !result.Changed || result.SettlementReview == nil {
				t.Fatalf("provider ReconcileTerminal() = %+v, %v", result, err)
			}
			review := *result.SettlementReview
			wantStatus, _ := test.reason.TerminalStatus()
			wantProviderCount := int64(0)
			if test.providerUsage {
				wantProviderCount = 1
			}
			if review.Source != SettlementReviewSourceReconcileTerminal ||
				review.Reason != SettlementReviewReasonTerminalUsageUnmeasured ||
				review.TerminalStatus != wantStatus || review.RequestDigest != settlementReviewRequestDigestV2(review) ||
				review.Evidence.PriorOperationCount != 0 || review.Evidence.PriorEffectCount != 0 ||
				review.Evidence.PriorProviderUsageCount != wantProviderCount || review.Evidence.CurrentEffectCount != 0 {
				t.Fatalf("provider reconcile Review = %+v", review)
			}
			if len(authority.committed()) != 0 || len(authority.held()) != 1 {
				t.Fatalf("provider reconcile commercial calls = settle:%+v hold:%+v", authority.committed(), authority.held())
			}
			replay, err := store.ReconcileTerminal(context.Background(), command)
			if err != nil || replay.Changed || replay.SettlementReview == nil || *replay.SettlementReview != review {
				t.Fatalf("provider reconcile replay = %+v, %v", replay, err)
			}
		})
	}
}

type providerTerminalHoldAuthority struct {
	target      *testSettlementReviewAuthority
	entered     chan struct{}
	release     chan struct{}
	panicOnHold bool
	once        sync.Once
}

func (authority *providerTerminalHoldAuthority) Settle(tx *gorm.DB, command SettlementCommand) error {
	return authority.target.Settle(tx, command)
}

func (authority *providerTerminalHoldAuthority) HoldForReview(tx *gorm.DB, command SettlementReviewHoldCommand) error {
	if authority.panicOnHold {
		panic("provider terminal hold panic")
	}
	if err := authority.target.HoldForReview(tx, command); err != nil {
		return err
	}
	authority.once.Do(func() { close(authority.entered) })
	<-authority.release
	return nil
}

func (authority *providerTerminalHoldAuthority) MeasureProviderUsage(
	tx *gorm.DB,
	command MeasureSettlementReviewProviderUsageCommand,
) (SettlementReviewProviderUsageAuthorityReceipt, error) {
	return authority.target.MeasureProviderUsage(tx, command)
}

func (authority *providerTerminalHoldAuthority) ResolveReview(
	tx *gorm.DB,
	command SettlementReviewResolutionAuthorityCommand,
) (SettlementReviewResolutionAuthorityReceipt, error) {
	return authority.target.ResolveReview(tx, command)
}

func TestProviderUsageTerminalizationRetainsBindingThroughTransactionAndUnlocksOnPanic(t *testing.T) {
	t.Run("compatibility mutator waits", func(t *testing.T) {
		db, store, clock, turns := newSQLClaimNextFixture(t, "provider_terminal_binding_wait")
		target := newTestSettlementReviewAuthority(t, db)
		authority := &providerTerminalHoldAuthority{
			target: target, entered: make(chan struct{}), release: make(chan struct{}),
		}
		provider := newSettlementReviewProviderUsageTestScope(t, db, store, clock, turns[0].Plugin, "provider_terminal_binding_wait")
		binding, err := store.BindSettlementReviewProviderUsageAuthority(provider.journal, authority)
		if err != nil {
			t.Fatal(err)
		}
		claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turns[0].ID, "attempt_provider_terminal_binding_wait"))
		if err != nil {
			t.Fatal(err)
		}
		type outcome struct {
			result CommitAttemptResult
			err    error
		}
		committed := make(chan outcome, 1)
		go func() {
			result, commitErr := store.CommitAttempt(context.Background(), CommitAttemptCommand{
				Fence: claimed.Attempt.Fence(), OperationID: "operation_provider_terminal_binding_wait",
				TerminalStatus: agentv1.TurnStatusFailed,
			})
			committed <- outcome{result: result, err: commitErr}
		}()
		select {
		case <-authority.entered:
		case <-time.After(2 * time.Second):
			t.Fatal("provider HoldForReview was not entered")
		}
		mutated := make(chan struct{})
		go func() {
			store.WithSettlementAuthority(target)
			close(mutated)
		}()
		select {
		case <-mutated:
			t.Fatal("compatibility mutator crossed the provider terminal transaction")
		case <-time.After(50 * time.Millisecond):
		}
		close(authority.release)
		select {
		case result := <-committed:
			if result.err != nil || result.result.SettlementReview == nil {
				t.Fatalf("provider terminal commit = %+v, %v", result.result, result.err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("provider terminal commit deadlocked with compatibility mutator")
		}
		select {
		case <-mutated:
		case <-time.After(2 * time.Second):
			t.Fatal("compatibility mutator did not resume after terminal commit")
		}
		if store.MatchesSettlementAuthorityBinding(binding) {
			t.Fatal("compatibility mutation did not invalidate the sealed binding")
		}
	})

	t.Run("reconcile compatibility mutator waits", func(t *testing.T) {
		db, store, clock, turns := newSQLClaimNextFixture(t, "provider_reconcile_binding_wait")
		target := newTestSettlementReviewAuthority(t, db)
		authority := &providerTerminalHoldAuthority{
			target: target, entered: make(chan struct{}), release: make(chan struct{}),
		}
		provider := newSettlementReviewProviderUsageTestScope(t, db, store, clock, turns[0].Plugin, "provider_reconcile_binding_wait")
		binding, err := store.BindSettlementReviewProviderUsageAuthority(provider.journal, authority)
		if err != nil {
			t.Fatal(err)
		}
		turn := turns[0]
		if _, err := store.RequestCancel(context.Background(), turn.PrincipalID, turn.ThreadID, turn.ID,
			clock.Get(), sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"cancellationRequested":true}`)); err != nil {
			t.Fatal(err)
		}
		type outcome struct {
			result ReconcileResult
			err    error
		}
		reconciled := make(chan outcome, 1)
		go func() {
			result, reconcileErr := store.ReconcileTerminal(context.Background(), ReconcileCommand{
				TurnID: turn.ID, Reason: ReclaimReasonCancellationPending,
				ReconcilerID: "reconciler_provider_binding_wait", ReconcilerBuildDigest: "sha256:reconciler-provider-binding-wait",
			})
			reconciled <- outcome{result: result, err: reconcileErr}
		}()
		select {
		case <-authority.entered:
		case <-time.After(2 * time.Second):
			t.Fatal("provider Reconcile HoldForReview was not entered")
		}
		mutated := make(chan struct{})
		go func() {
			store.WithSettlementAuthority(target)
			close(mutated)
		}()
		select {
		case <-mutated:
			t.Fatal("compatibility mutator crossed the provider reconcile transaction")
		case <-time.After(50 * time.Millisecond):
		}
		close(authority.release)
		select {
		case result := <-reconciled:
			if result.err != nil || result.result.SettlementReview == nil || !result.result.Changed {
				t.Fatalf("provider reconcile = %+v, %v", result.result, result.err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("provider reconcile deadlocked with compatibility mutator")
		}
		select {
		case <-mutated:
		case <-time.After(2 * time.Second):
			t.Fatal("compatibility mutator did not resume after reconcile commit")
		}
		if store.MatchesSettlementAuthorityBinding(binding) {
			t.Fatal("reconcile compatibility mutation did not invalidate the sealed binding")
		}
	})

	t.Run("panic releases binding", func(t *testing.T) {
		db, store, clock, turns := newSQLClaimNextFixture(t, "provider_terminal_binding_panic")
		target := newTestSettlementReviewAuthority(t, db)
		authority := &providerTerminalHoldAuthority{target: target, panicOnHold: true}
		provider := newSettlementReviewProviderUsageTestScope(t, db, store, clock, turns[0].Plugin, "provider_terminal_binding_panic")
		if _, err := store.BindSettlementReviewProviderUsageAuthority(provider.journal, authority); err != nil {
			t.Fatal(err)
		}
		claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(turns[0].ID, "attempt_provider_terminal_binding_panic"))
		if err != nil {
			t.Fatal(err)
		}
		func() {
			defer func() {
				if recover() == nil {
					t.Fatal("provider HoldForReview panic was swallowed")
				}
			}()
			_, _ = store.CommitAttempt(context.Background(), CommitAttemptCommand{
				Fence: claimed.Attempt.Fence(), OperationID: "operation_provider_terminal_binding_panic",
				TerminalStatus: agentv1.TurnStatusFailed,
			})
		}()
		if !store.settlementMu.TryLock() {
			t.Fatal("provider terminal panic stranded settlement binding read lock")
		}
		store.settlementMu.Unlock()
		if executionTableCount(t, db, SQLTurnOperationTable, "turn_id = ?", turns[0].ID) != 0 ||
			executionTableCount(t, db, SQLSettlementReviewTable, "turn_id = ?", turns[0].ID) != 0 {
			t.Fatal("provider terminal panic committed partial terminalization")
		}
	})
}
