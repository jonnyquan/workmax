package agentturn

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	agentv1 "server/contracts/agent/v1"
)

type settlementReviewResolutionFixture struct {
	db        *gorm.DB
	store     *SQLStore
	clock     *sqlExecutionTestClock
	authority *testSettlementReviewAuthority
	terminal  CommitAttemptCommand
	review    SettlementReviewRecord
	evidence  SettlementReviewUsageEvidenceRecord
	outboxID  string
}

func newSettlementReviewResolutionFixture(
	t *testing.T,
	suffix string,
	sealed bool,
) settlementReviewResolutionFixture {
	t.Helper()
	db, store, clock, turns := newSQLClaimNextFixture(t, suffix)
	authority := newTestSettlementReviewAuthority(t, db)
	var provider settlementReviewProviderUsageTestScope
	if sealed {
		provider = newSettlementReviewProviderUsageTestScope(t, db, store, clock, turns[0].Plugin, suffix)
		if binding, err := store.BindSettlementReviewProviderUsageAuthority(
			provider.journal, authority,
		); err != nil || binding == nil {
			t.Fatalf("BindSettlementReviewProviderUsageAuthority() = %p, %v", binding, err)
		}
	} else {
		store.WithSettlementAuthority(authority)
	}
	terminal, review, outboxID := openSettlementReviewForResolution(t, store, clock, turns[0], suffix)
	var evidence SettlementReviewUsageEvidenceRecord
	if sealed {
		review = ensureProviderAwareSettlementReview(t, db, review)
		appendSettlementReviewProviderUsage(t, provider, clock, terminal, suffix)
		captured, err := store.CaptureSettlementReviewUsageEvidence(
			context.Background(), settlementReviewUsageCommand(review),
		)
		if err != nil {
			t.Fatalf("CaptureSettlementReviewUsageEvidence() = %+v, %v", captured, err)
		}
		review, evidence = captured.Review, captured.Evidence
	}
	return settlementReviewResolutionFixture{
		db: db, store: store, clock: clock, authority: authority,
		terminal: terminal, review: review, evidence: evidence, outboxID: outboxID,
	}
}

func openSettlementReviewForResolution(
	t *testing.T,
	store *SQLStore,
	clock *sqlExecutionTestClock,
	turn Turn,
	suffix string,
) (CommitAttemptCommand, SettlementReviewRecord, string) {
	t.Helper()
	claimed, err := store.ClaimAttempt(
		context.Background(), executionClaimCommand(turn.ID, "attempt_resolution_"+suffix),
	)
	if err != nil {
		t.Fatal(err)
	}
	outboxID := "outbox_resolution_" + suffix
	terminal := CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_resolution_" + suffix,
		TerminalStatus: agentv1.TurnStatusFailed,
		Effects: []EffectOutboxDraft{executionTestEffect(
			outboxID, "writer.document.publish", "resolution-"+suffix, clock.Get(),
		)},
	}
	result, err := store.CommitAttempt(context.Background(), terminal)
	if err != nil || result.SettlementReview == nil {
		t.Fatalf("CommitAttempt() review = %+v, %v", result, err)
	}
	return terminal, *result.SettlementReview, outboxID
}

func settlementReviewResolutionCommand(
	review SettlementReviewRecord,
	evidence SettlementReviewUsageEvidenceRecord,
) ResolveSettlementReviewCommand {
	return ResolveSettlementReviewCommand{
		TurnID: review.TurnID, ReviewID: review.ReviewID,
		ExpectedRequestDigest: review.RequestDigest,
		EvidenceID:            evidence.EvidenceID, ExpectedEvidenceDigest: evidence.EvidenceDigest,
		ActorID: "operator_finance_1",
	}
}

func unresolvedSettlementReviewResolutionCommand(review SettlementReviewRecord) ResolveSettlementReviewCommand {
	return ResolveSettlementReviewCommand{
		TurnID: review.TurnID, ReviewID: review.ReviewID,
		ExpectedRequestDigest:  review.RequestDigest,
		EvidenceID:             settlementReviewUsageEvidenceID(review.ReviewID),
		ExpectedEvidenceDigest: testSettlementReviewDigest("not-captured", review.ReviewID),
		ActorID:                "operator_finance_1",
	}
}

func TestResolveSettlementReviewFinalizesFinanciallyAndKeepsEffectsHeld(t *testing.T) {
	fixture := newSettlementReviewResolutionFixture(t, "success", true)
	fixture.clock.Set(fixture.clock.Get().Add(time.Second))
	command := settlementReviewResolutionCommand(fixture.review, fixture.evidence)

	result, err := fixture.store.ResolveSettlementReview(context.Background(), command)
	if err != nil {
		t.Fatalf("ResolveSettlementReview(): %v", err)
	}
	if result.Replay || result.Review.Status != SettlementReviewStatusFinalizedHeld ||
		result.Resolution.ReviewID != fixture.review.ReviewID ||
		result.Resolution.UsedUnits != 7 || result.Resolution.ReservedUnits != 100 ||
		result.Resolution.EvidenceID != fixture.evidence.EvidenceID ||
		result.Resolution.EvidenceDigest != fixture.evidence.EvidenceDigest ||
		result.Resolution.PricingSnapshotDigest != fixture.evidence.PricingSnapshotDigest ||
		!result.Review.UpdatedAt.Equal(result.Resolution.CreatedAt) {
		t.Fatalf("resolution result = %+v", result)
	}
	if err := result.Resolution.Validate(); err != nil {
		t.Fatalf("resolution receipt invalid: %v", err)
	}
	if calls := fixture.authority.resolved(); len(calls) != 1 ||
		calls[0].DecisionDigest != result.Resolution.DecisionDigest {
		t.Fatalf("authority resolution calls = %+v", calls)
	}
	if count := executionTableCount(t, fixture.db, "test_agent_settlement_review_resolution", "resolution_id = ?", result.Resolution.ResolutionID); count != 1 {
		t.Fatalf("commercial resolution markers = %d, want 1", count)
	}
	pending, err := fixture.store.ListSettlementReviews(context.Background(), ListSettlementReviewsQuery{})
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending reviews after resolution = %+v, %v", pending, err)
	}
	resolutions, err := fixture.store.ListSettlementReviewResolutions(
		context.Background(), ListSettlementReviewResolutionsQuery{},
	)
	if err != nil || len(resolutions) != 1 || resolutions[0] != result.Resolution {
		t.Fatalf("resolution audit list = %+v, %v", resolutions, err)
	}
	evidence, err := fixture.store.ListSettlementReviewUsageEvidence(
		context.Background(), ListSettlementReviewUsageEvidenceQuery{},
	)
	if err != nil || len(evidence) != 1 || evidence[0] != fixture.evidence {
		t.Fatalf("finalized evidence audit list = %+v, %v", evidence, err)
	}
	assertEffectReviewHeld(t, fixture.db, fixture.outboxID)
	if _, err := fixture.store.ClaimEffects(context.Background(), ClaimEffectsCommand{
		LeaseOwnerID: "dispatcher_after_financial_resolution",
	}); !errors.Is(err, ErrNoClaimableEffects) {
		t.Fatalf("ClaimEffects() after financial resolution = %v", err)
	}

	if len(fixture.authority.resolved()) != 1 {
		t.Fatal("resolution authority was called more than once")
	}
}

func TestResolveSettlementReviewExactReplayAndConflict(t *testing.T) {
	fixture := newSettlementReviewResolutionFixture(t, "replay", true)
	command := settlementReviewResolutionCommand(fixture.review, fixture.evidence)
	first, err := fixture.store.ResolveSettlementReview(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := fixture.store.ResolveSettlementReview(context.Background(), command)
	if err != nil || !replay.Replay || replay.Resolution != first.Resolution || replay.Review != first.Review {
		t.Fatalf("exact replay = %+v, %v", replay, err)
	}
	conflicting := command
	conflicting.ActorID = "operator_finance_2"
	if _, err := fixture.store.ResolveSettlementReview(context.Background(), conflicting); !errors.Is(err, ErrSettlementReviewResolutionConflict) {
		t.Fatalf("conflicting replay = %v", err)
	}
	if len(fixture.authority.resolved()) != 1 ||
		executionTableCount(t, fixture.db, SQLSettlementReviewResolutionTable, "review_id = ?", fixture.review.ReviewID) != 1 {
		t.Fatal("replay/conflict created another financial resolution")
	}
}

func TestResolveSettlementReviewRejectsUnsupportedOrStaleCommandsWithoutMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ResolveSettlementReviewCommand)
	}{
		{name: "missing evidence id", mutate: func(command *ResolveSettlementReviewCommand) {
			command.EvidenceID = ""
		}},
		{name: "foreign evidence id", mutate: func(command *ResolveSettlementReviewCommand) {
			command.EvidenceID = strings.Repeat("0", 64)
		}},
		{name: "invalid evidence digest", mutate: func(command *ResolveSettlementReviewCommand) {
			command.ExpectedEvidenceDigest = "sha256:not-a-digest"
		}},
		{name: "foreign evidence digest", mutate: func(command *ResolveSettlementReviewCommand) {
			command.ExpectedEvidenceDigest = "sha256:" + strings.Repeat("0", 64)
		}},
		{name: "stale review digest", mutate: func(command *ResolveSettlementReviewCommand) {
			command.ExpectedRequestDigest = "sha256:" + strings.Repeat("0", 64)
		}},
		{name: "missing actor", mutate: func(command *ResolveSettlementReviewCommand) {
			command.ActorID = ""
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSettlementReviewResolutionFixture(t, "invalid_"+strings.ReplaceAll(test.name, " ", "_"), true)
			command := settlementReviewResolutionCommand(fixture.review, fixture.evidence)
			test.mutate(&command)
			if _, err := fixture.store.ResolveSettlementReview(context.Background(), command); err == nil {
				t.Fatal("invalid resolution command succeeded")
			}
			if len(fixture.authority.resolved()) != 0 ||
				executionTableCount(t, fixture.db, SQLSettlementReviewResolutionTable, "review_id = ?", fixture.review.ReviewID) != 0 {
				t.Fatal("invalid resolution command mutated commercial state")
			}
			open, err := fixture.store.ListSettlementReviews(context.Background(), ListSettlementReviewsQuery{})
			if err != nil || len(open) != 1 || open[0].Status != SettlementReviewStatusMeteredHeld {
				t.Fatalf("metered review after rejection = %+v, %v", open, err)
			}
			assertEffectReviewHeld(t, fixture.db, fixture.outboxID)
		})
	}
}

type testHoldOnlySettlementReviewAuthority struct {
	target *testSettlementReviewAuthority
}

func (authority testHoldOnlySettlementReviewAuthority) Settle(tx *gorm.DB, command SettlementCommand) error {
	return authority.target.Settle(tx, command)
}

func (authority testHoldOnlySettlementReviewAuthority) HoldForReview(tx *gorm.DB, command SettlementReviewHoldCommand) error {
	return authority.target.HoldForReview(tx, command)
}

type blockingSettlementReviewResolutionAuthority struct {
	target  *testSettlementReviewAuthority
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

type mutatingSettlementReviewResolutionAuthority struct {
	target *testSettlementReviewAuthority
	mutate func(*SettlementReviewResolutionAuthorityReceipt)
}

func (authority mutatingSettlementReviewResolutionAuthority) Settle(
	tx *gorm.DB,
	command SettlementCommand,
) error {
	return authority.target.Settle(tx, command)
}

func (authority mutatingSettlementReviewResolutionAuthority) HoldForReview(
	tx *gorm.DB,
	command SettlementReviewHoldCommand,
) error {
	return authority.target.HoldForReview(tx, command)
}

func (authority mutatingSettlementReviewResolutionAuthority) MeasureReview(
	tx *gorm.DB,
	command MeasureSettlementReviewUsageCommand,
) (SettlementReviewUsageAuthorityReceipt, error) {
	return authority.target.MeasureReview(tx, command)
}

func (authority mutatingSettlementReviewResolutionAuthority) MeasureProviderUsage(
	tx *gorm.DB,
	command MeasureSettlementReviewProviderUsageCommand,
) (SettlementReviewProviderUsageAuthorityReceipt, error) {
	return authority.target.MeasureProviderUsage(tx, command)
}

func (authority mutatingSettlementReviewResolutionAuthority) ResolveReview(
	tx *gorm.DB,
	command SettlementReviewResolutionAuthorityCommand,
) (SettlementReviewResolutionAuthorityReceipt, error) {
	receipt, err := authority.target.ResolveReview(tx, command)
	if err == nil {
		authority.mutate(&receipt)
	}
	return receipt, err
}

func (authority *blockingSettlementReviewResolutionAuthority) Settle(tx *gorm.DB, command SettlementCommand) error {
	return authority.target.Settle(tx, command)
}

func (authority *blockingSettlementReviewResolutionAuthority) HoldForReview(
	tx *gorm.DB,
	command SettlementReviewHoldCommand,
) error {
	return authority.target.HoldForReview(tx, command)
}

func (authority *blockingSettlementReviewResolutionAuthority) MeasureReview(
	tx *gorm.DB,
	command MeasureSettlementReviewUsageCommand,
) (SettlementReviewUsageAuthorityReceipt, error) {
	return authority.target.MeasureReview(tx, command)
}

func (authority *blockingSettlementReviewResolutionAuthority) MeasureProviderUsage(
	tx *gorm.DB,
	command MeasureSettlementReviewProviderUsageCommand,
) (SettlementReviewProviderUsageAuthorityReceipt, error) {
	return authority.target.MeasureProviderUsage(tx, command)
}

func (authority *blockingSettlementReviewResolutionAuthority) ResolveReview(
	tx *gorm.DB,
	command SettlementReviewResolutionAuthorityCommand,
) (SettlementReviewResolutionAuthorityReceipt, error) {
	authority.once.Do(func() { close(authority.entered) })
	<-authority.release
	return authority.target.ResolveReview(tx, command)
}

func TestResolveSettlementReviewRequiresSealedResolutionCapability(t *testing.T) {
	t.Run("unsealed compatibility authority", func(t *testing.T) {
		fixture := newSettlementReviewResolutionFixture(t, "unsealed", false)
		if _, err := fixture.store.ResolveSettlementReview(
			context.Background(), unresolvedSettlementReviewResolutionCommand(fixture.review),
		); !errors.Is(err, ErrSettlementReviewResolutionUnavailable) {
			t.Fatalf("unsealed ResolveSettlementReview() = %v", err)
		}
	})

	t.Run("sealed hold-only authority", func(t *testing.T) {
		db, store, clock, turns := newSQLClaimNextFixture(t, "hold_only")
		target := newTestSettlementReviewAuthority(t, db)
		if _, err := store.BindSettlementReviewAuthority(testHoldOnlySettlementReviewAuthority{target: target}); err != nil {
			t.Fatal(err)
		}
		_, review, _ := openSettlementReviewForResolution(t, store, clock, turns[0], "hold_only")
		if _, err := store.ResolveSettlementReview(
			context.Background(), unresolvedSettlementReviewResolutionCommand(review),
		); !errors.Is(err, ErrSettlementReviewResolutionUnavailable) {
			t.Fatalf("hold-only ResolveSettlementReview() = %v", err)
		}
	})

	t.Run("post-bind mutation", func(t *testing.T) {
		fixture := newSettlementReviewResolutionFixture(t, "violated", true)
		fixture.store.WithSettlementAuthority(fixture.authority)
		if _, err := fixture.store.ResolveSettlementReview(
			context.Background(), settlementReviewResolutionCommand(fixture.review, fixture.evidence),
		); !errors.Is(err, ErrSettlementBindingInvalid) {
			t.Fatalf("violated ResolveSettlementReview() = %v", err)
		}
	})
}

func TestResolveSettlementReviewSerializesPostBindMutationAcrossAuthorityTransaction(t *testing.T) {
	db, store, clock, turns := newSQLClaimNextFixture(t, "binding_linearization")
	target := newTestSettlementReviewAuthority(t, db)
	authority := &blockingSettlementReviewResolutionAuthority{
		target: target, entered: make(chan struct{}), release: make(chan struct{}),
	}
	provider := newSettlementReviewProviderUsageTestScope(
		t, db, store, clock, turns[0].Plugin, "binding_linearization",
	)
	binding, err := store.BindSettlementReviewProviderUsageAuthority(provider.journal, authority)
	if err != nil {
		t.Fatal(err)
	}
	terminal, review, _ := openSettlementReviewForResolution(t, store, clock, turns[0], "binding_linearization")
	review = ensureProviderAwareSettlementReview(t, db, review)
	appendSettlementReviewProviderUsage(t, provider, clock, terminal, "binding_linearization")
	captured, err := store.CaptureSettlementReviewUsageEvidence(
		context.Background(), settlementReviewUsageCommand(review),
	)
	if err != nil {
		t.Fatal(err)
	}
	review, evidence := captured.Review, captured.Evidence

	type resolutionOutcome struct {
		result ResolveSettlementReviewResult
		err    error
	}
	resolved := make(chan resolutionOutcome, 1)
	go func() {
		result, resolveErr := store.ResolveSettlementReview(
			context.Background(), settlementReviewResolutionCommand(review, evidence),
		)
		resolved <- resolutionOutcome{result: result, err: resolveErr}
	}()
	<-authority.entered
	if store.settlementMu.TryLock() {
		store.settlementMu.Unlock()
		t.Fatal("ResolveSettlementReview released the binding lock inside the Authority transaction")
	}

	mutated := make(chan struct{})
	go func() {
		store.WithSettlementAuthority(target)
		close(mutated)
	}()
	select {
	case <-mutated:
		t.Fatal("post-bind mutator completed while the resolution Authority was active")
	case <-time.After(50 * time.Millisecond):
	}
	close(authority.release)
	select {
	case outcome := <-resolved:
		if outcome.err != nil || outcome.result.Replay {
			t.Fatalf("ResolveSettlementReview() = %+v, %v", outcome.result, outcome.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ResolveSettlementReview did not finish after the Authority was released")
	}
	select {
	case <-mutated:
	case <-time.After(2 * time.Second):
		t.Fatal("post-bind mutator did not resume after resolution committed")
	}
	if store.MatchesSettlementAuthorityBinding(binding) {
		t.Fatal("post-bind mutation did not permanently invalidate the sealed binding")
	}
}

func TestResolveSettlementReviewAuthorityFailureAndUnitsOverflowRollBack(t *testing.T) {
	t.Run("provider failure is sanitized", func(t *testing.T) {
		fixture := newSettlementReviewResolutionFixture(t, "provider_failure", true)
		fixture.authority.setResolutionFailure(errors.New("secret reservation 991 provider detail"))
		_, err := fixture.store.ResolveSettlementReview(
			context.Background(), settlementReviewResolutionCommand(fixture.review, fixture.evidence),
		)
		if !errors.Is(err, ErrSettlementReviewResolutionFailed) || strings.Contains(err.Error(), "secret") {
			t.Fatalf("resolution failure = %v", err)
		}
		assertSettlementReviewResolutionRolledBack(t, fixture)
	})

	t.Run("used exceeds reserved", func(t *testing.T) {
		fixture := newSettlementReviewResolutionFixture(t, "exceeds_reserved", true)
		fixture.authority.setReservedUnits(2)
		_, err := fixture.store.ResolveSettlementReview(
			context.Background(), settlementReviewResolutionCommand(fixture.review, fixture.evidence),
		)
		if !errors.Is(err, ErrSettlementReviewUnitsExceedReserved) {
			t.Fatalf("used > reserved error = %v", err)
		}
		assertSettlementReviewResolutionRolledBack(t, fixture)
	})

	t.Run("receipt must echo evidence and pricing", func(t *testing.T) {
		db, store, clock, turns := newSQLClaimNextFixture(t, "resolution_bad_evidence_echo")
		target := newTestSettlementReviewAuthority(t, db)
		authority := mutatingSettlementReviewResolutionAuthority{
			target: target,
			mutate: func(receipt *SettlementReviewResolutionAuthorityReceipt) {
				receipt.PricingSnapshotDigest = testSettlementReviewDigest("forged-pricing")
			},
		}
		provider := newSettlementReviewProviderUsageTestScope(
			t, db, store, clock, turns[0].Plugin, "resolution_bad_evidence_echo",
		)
		if _, err := store.BindSettlementReviewProviderUsageAuthority(provider.journal, authority); err != nil {
			t.Fatal(err)
		}
		terminal, review, _ := openSettlementReviewForResolution(
			t, store, clock, turns[0], "resolution_bad_evidence_echo",
		)
		review = ensureProviderAwareSettlementReview(t, db, review)
		appendSettlementReviewProviderUsage(t, provider, clock, terminal, "resolution_bad_evidence_echo")
		captured, err := store.CaptureSettlementReviewUsageEvidence(
			context.Background(), settlementReviewUsageCommand(review),
		)
		if err != nil {
			t.Fatal(err)
		}
		_, err = store.ResolveSettlementReview(
			context.Background(), settlementReviewResolutionCommand(captured.Review, captured.Evidence),
		)
		if !errors.Is(err, ErrSettlementReviewResolutionFailed) {
			t.Fatalf("forged authority receipt = %v", err)
		}
		if executionTableCount(t, db, SQLSettlementReviewResolutionTable,
			"review_id = ?", review.ReviewID) != 0 ||
			executionTableCount(t, db, "test_agent_settlement_review_resolution",
				"resolution_id <> ?", "") != 0 {
			t.Fatal("forged receipt left a durable resolution")
		}
		open, err := store.ListSettlementReviews(context.Background(), ListSettlementReviewsQuery{})
		if err != nil || len(open) != 1 || open[0].Status != SettlementReviewStatusMeteredHeld {
			t.Fatalf("review after forged receipt = %+v, %v", open, err)
		}
	})
}

func assertSettlementReviewResolutionRolledBack(t *testing.T, fixture settlementReviewResolutionFixture) {
	t.Helper()
	if len(fixture.authority.resolved()) != 0 ||
		executionTableCount(t, fixture.db, SQLSettlementReviewResolutionTable, "review_id = ?", fixture.review.ReviewID) != 0 ||
		executionTableCount(t, fixture.db, "test_agent_settlement_review_resolution", "resolution_id <> ?", "") != 0 {
		t.Fatal("failed financial resolution left a receipt")
	}
	open, err := fixture.store.ListSettlementReviews(context.Background(), ListSettlementReviewsQuery{})
	if err != nil || len(open) != 1 || open[0].Status != SettlementReviewStatusMeteredHeld {
		t.Fatalf("metered review after rollback = %+v, %v", open, err)
	}
	assertEffectReviewHeld(t, fixture.db, fixture.outboxID)
}

func TestSettlementReviewResolutionReplayRejectsMissingOrTamperedReceipt(t *testing.T) {
	t.Run("missing receipt", func(t *testing.T) {
		fixture := newSettlementReviewResolutionFixture(t, "missing_receipt", true)
		command := settlementReviewResolutionCommand(fixture.review, fixture.evidence)
		if _, err := fixture.store.ResolveSettlementReview(context.Background(), command); err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.Table(SQLSettlementReviewResolutionTable).
			Where("review_id = ?", fixture.review.ReviewID).
			Delete(&sqlSettlementReviewResolutionRow{}).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.store.ResolveSettlementReview(context.Background(), command); !errors.Is(err, ErrStoreIntegrity) {
			t.Fatalf("missing receipt replay = %v", err)
		}
		if _, err := fixture.store.CommitAttempt(context.Background(), fixture.terminal); !errors.Is(err, ErrStoreIntegrity) {
			t.Fatalf("terminal replay with missing receipt = %v", err)
		}
	})

	t.Run("tampered receipt", func(t *testing.T) {
		fixture := newSettlementReviewResolutionFixture(t, "tampered_receipt", true)
		command := settlementReviewResolutionCommand(fixture.review, fixture.evidence)
		if _, err := fixture.store.ResolveSettlementReview(context.Background(), command); err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.Table(SQLSettlementReviewResolutionTable).
			Where("review_id = ?", fixture.review.ReviewID).
			UpdateColumn("decision_digest", "sha256:"+strings.Repeat("0", 64)).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.store.ResolveSettlementReview(context.Background(), command); !errors.Is(err, ErrStoreIntegrity) {
			t.Fatalf("tampered receipt replay = %v", err)
		}
	})

	t.Run("coordinated timestamp tamper", func(t *testing.T) {
		fixture := newSettlementReviewResolutionFixture(t, "timestamp_tamper", true)
		command := settlementReviewResolutionCommand(fixture.review, fixture.evidence)
		result, err := fixture.store.ResolveSettlementReview(context.Background(), command)
		if err != nil {
			t.Fatal(err)
		}
		tamperedAt := result.Resolution.CreatedAt.Add(time.Second)
		if err := fixture.db.Table(SQLSettlementReviewResolutionTable).
			Where("review_id = ?", fixture.review.ReviewID).
			UpdateColumn("created_at", tamperedAt).Error; err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.Table(SQLSettlementReviewTable).
			Where("review_id = ?", fixture.review.ReviewID).
			UpdateColumn("updated_at", tamperedAt).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.store.ResolveSettlementReview(context.Background(), command); !errors.Is(err, ErrStoreIntegrity) {
			t.Fatalf("timestamp-tampered receipt replay = %v", err)
		}
	})

	t.Run("receipt under pending parent", func(t *testing.T) {
		fixture := newSettlementReviewResolutionFixture(t, "pending_parent", true)
		if _, err := fixture.store.ResolveSettlementReview(
			context.Background(), settlementReviewResolutionCommand(fixture.review, fixture.evidence),
		); err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.Table(SQLSettlementReviewTable).
			Where("review_id = ?", fixture.review.ReviewID).
			UpdateColumn("status", SettlementReviewStatusPending).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.store.CommitAttempt(context.Background(), fixture.terminal); !errors.Is(err, ErrStoreIntegrity) {
			t.Fatalf("pending parent with receipt = %v", err)
		}
		if _, err := fixture.store.ListSettlementReviews(
			context.Background(), ListSettlementReviewsQuery{},
		); !errors.Is(err, ErrStoreIntegrity) {
			t.Fatalf("pending list with smuggled receipt = %v", err)
		}
	})
}

func TestOpenSettlementReviewListCandidateOmitsConcurrentResolution(t *testing.T) {
	fixture := newSettlementReviewResolutionFixture(t, "list_concurrent_resolution", true)
	var staleRow sqlSettlementReviewRow
	if err := fixture.db.Table(SQLSettlementReviewTable).
		Where("review_id = ? AND status = ?", fixture.review.ReviewID, SettlementReviewStatusMeteredHeld).
		Take(&staleRow).Error; err != nil {
		t.Fatal(err)
	}
	stale, err := staleRow.toRecord()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.ResolveSettlementReview(
		context.Background(), settlementReviewResolutionCommand(fixture.review, fixture.evidence),
	); err != nil {
		t.Fatal(err)
	}
	_, stillOpen, err := fixture.store.validateOpenSettlementReviewListCandidate(context.Background(), stale)
	if err != nil || stillOpen {
		t.Fatalf("stale metered list candidate = %t, %v", stillOpen, err)
	}
	pending, err := fixture.store.ListSettlementReviews(context.Background(), ListSettlementReviewsQuery{})
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending reviews after concurrent resolution = %+v, %v", pending, err)
	}
}

func TestResolveSettlementReviewSerializesConcurrentResolversAcrossStores(t *testing.T) {
	fixture := newSettlementReviewResolutionFixture(t, "concurrent", true)
	second := mustSQLStore(t, fixture.db)
	second.executionClock = fixture.clock.Now
	secondJournal, err := NewProviderUsageJournal(second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.BindSettlementReviewProviderUsageAuthority(secondJournal, fixture.authority); err != nil {
		t.Fatal(err)
	}
	command := settlementReviewResolutionCommand(fixture.review, fixture.evidence)

	type outcome struct {
		result ResolveSettlementReviewResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, store := range []*SQLStore{fixture.store, second} {
		go func(candidate *SQLStore) {
			ready.Done()
			<-start
			result, err := candidate.ResolveSettlementReview(context.Background(), command)
			outcomes <- outcome{result: result, err: err}
		}(store)
	}
	ready.Wait()
	close(start)
	first, secondOutcome := <-outcomes, <-outcomes
	if first.err != nil || secondOutcome.err != nil {
		t.Fatalf("concurrent outcomes = %+v / %+v", first, secondOutcome)
	}
	if first.result.Replay == secondOutcome.result.Replay ||
		first.result.Resolution.ResolutionDigest != secondOutcome.result.Resolution.ResolutionDigest {
		t.Fatalf("concurrent outcomes = %+v / %+v", first.result, secondOutcome.result)
	}
	if len(fixture.authority.resolved()) != 1 ||
		executionTableCount(t, fixture.db, SQLSettlementReviewResolutionTable, "review_id = ?", fixture.review.ReviewID) != 1 {
		t.Fatal("concurrent resolution invoked the Authority or persisted more than once")
	}
}
