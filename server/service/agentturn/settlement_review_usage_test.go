package agentturn

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	agentv1 "server/contracts/agent/v1"
)

type settlementReviewProviderUsageTestScope struct {
	journal  *ProviderUsageJournal
	recorder *ProviderUsageRecorder
	release  UsageMeterReleaseRecord
}

func newSettlementReviewProviderUsageTestScope(
	t *testing.T,
	db *gorm.DB,
	store *SQLStore,
	clock *sqlExecutionTestClock,
	plugin agentv1.EventPluginRef,
	suffix string,
) settlementReviewProviderUsageTestScope {
	t.Helper()
	registration, err := NewProviderUsageSourceRegistration(ProviderUsageSourceRegistrationSpec{
		ProviderKey: "provider.test", ProviderAccountDigest: providerUsageTestDigest("review-account", suffix),
		SourceKey: "provider.test.result", SourceVersion: "1",
		SourceBuildDigest: providerUsageTestDigest("review-source-build", suffix),
		UsageSchemaKey:    "provider.test.usage", UsageSchemaVersion: "1",
		SourceSchemaDigest:      providerUsageTestDigest("review-source-schema", suffix),
		VerificationKind:        "adapter_attestation",
		VerificationKeyDigest:   providerUsageTestDigest("review-verification-key", suffix),
		VerificationBuildDigest: providerUsageTestDigest("review-verification-build", suffix),
	})
	if err != nil {
		t.Fatal(err)
	}
	release, err := NewUsageMeterReleaseRecord(UsageMeterReleaseSpec{
		Plugin: plugin, BillingPolicyKey: "writer.turn.v1",
		PricingSnapshotJSON: []byte(`{"creditsPerUSD":200}`),
		MeterKey:            "workmax.test.meter", MeterVersion: "1.0.0",
		MeterBuildDigest: providerUsageTestDigest("review-meter-build", suffix),
		Sources:          []ProviderUsageSourceRegistration{registration},
	}, clock.Get())
	if err != nil {
		t.Fatal(err)
	}
	releaseRow, err := usageMeterReleaseToSQLRow(release)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&releaseRow).Error; err != nil {
		t.Fatal(err)
	}
	journal, err := NewProviderUsageJournal(store)
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := journal.ScopeRecorder(context.Background(), plugin, release.ReleaseID, registration)
	if err != nil {
		t.Fatal(err)
	}
	return settlementReviewProviderUsageTestScope{journal: journal, recorder: recorder, release: release}
}

func ensureProviderAwareSettlementReview(
	t *testing.T,
	db *gorm.DB,
	review SettlementReviewRecord,
) SettlementReviewRecord {
	t.Helper()
	if settlementReviewProviderUsageAware(review) {
		return review
	}
	if review.Source != SettlementReviewSourceExecutor {
		t.Fatalf("legacy Review source = %q, want executor release", review.Source)
	}
	review.Source = SettlementReviewSourceExecutorTerminal
	review.Reason = SettlementReviewReasonTerminalUsageUnmeasured
	review.RequestDigest = settlementReviewRequestDigestV2(review)
	if err := review.Validate(); err != nil {
		t.Fatalf("provider-aware Review invalid: %v", err)
	}
	if err := db.Table(SQLSettlementReviewTable).Where("review_id = ?", review.ReviewID).
		UpdateColumns(map[string]any{
			"source": review.Source, "reason": review.Reason,
			"request_digest":             review.RequestDigest,
			"prior_provider_usage_count": review.Evidence.PriorProviderUsageCount,
		}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table("test_agent_settlement_review_hold").Where("review_id = ?", review.ReviewID).
		Update("request_digest", review.RequestDigest).Error; err != nil {
		t.Fatal(err)
	}
	return review
}

func appendSettlementReviewProviderUsage(
	t *testing.T,
	scope settlementReviewProviderUsageTestScope,
	clock *sqlExecutionTestClock,
	terminal CommitAttemptCommand,
	suffix string,
) ProviderUsageJournalRecord {
	t.Helper()
	result, err := scope.recorder.AppendAttested(context.Background(), AppendAttestedProviderUsageCommand{
		Fence:                 terminal.Fence,
		ProviderRequestDigest: providerUsageTestDigest("review-request", suffix),
		ProviderEventDigest:   providerUsageTestDigest("review-event", suffix),
		CanonicalUsageJSON:    []byte(`{"inputTokens":3,"outputTokens":5}`),
		ProviderReceiptDigest: providerUsageTestDigest("review-receipt", suffix),
		AttestationDigest:     providerUsageTestDigest("review-attestation", suffix),
		ProviderReportedAt:    clock.Get(),
	})
	if err != nil || result.Replay {
		t.Fatalf("AppendAttested() = %+v, %v", result, err)
	}
	return result.Record
}

func settlementReviewUsageCommand(
	review SettlementReviewRecord,
) CaptureSettlementReviewUsageEvidenceCommand {
	return CaptureSettlementReviewUsageEvidenceCommand{
		TurnID: review.TurnID, ReviewID: review.ReviewID,
		ExpectedRequestDigest: review.RequestDigest,
	}
}

type settlementReviewUsageFixture struct {
	db        *gorm.DB
	store     *SQLStore
	clock     *sqlExecutionTestClock
	authority *testSettlementReviewAuthority
	provider  settlementReviewProviderUsageTestScope
	terminal  CommitAttemptCommand
	review    SettlementReviewRecord
	outboxID  string
}

func newSettlementReviewUsageFixture(
	t *testing.T,
	suffix string,
	sealed bool,
) settlementReviewUsageFixture {
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
	if sealed {
		review = ensureProviderAwareSettlementReview(t, db, review)
		appendSettlementReviewProviderUsage(t, provider, clock, terminal, suffix)
	}
	return settlementReviewUsageFixture{
		db: db, store: store, clock: clock, authority: authority, provider: provider,
		terminal: terminal, review: review, outboxID: outboxID,
	}
}

func TestCaptureSettlementReviewUsageEvidencePersistsTrustedMeasurementAndReplays(t *testing.T) {
	fixture := newSettlementReviewUsageFixture(t, "usage_success", true)
	fixture.clock.Set(fixture.clock.Get().Add(time.Second))
	command := settlementReviewUsageCommand(fixture.review)

	result, err := fixture.store.CaptureSettlementReviewUsageEvidence(context.Background(), command)
	if err != nil {
		t.Fatalf("CaptureSettlementReviewUsageEvidence() = %+v, %v", result, err)
	}
	if result.Replay || result.Review.Status != SettlementReviewStatusMeteredHeld ||
		!result.Review.UpdatedAt.Equal(result.Evidence.CreatedAt) || result.Evidence.UsedUnits != 7 ||
		result.Evidence.ReviewID != fixture.review.ReviewID ||
		result.Evidence.TurnID != fixture.review.TurnID ||
		result.Evidence.SettlementKey != fixture.review.SettlementKey ||
		result.Evidence.ReviewRequestDigest != fixture.review.RequestDigest ||
		result.Evidence.MeterReleaseID != fixture.provider.release.ReleaseID ||
		result.Evidence.SourceReceiptCount != 1 ||
		result.Evidence.MeterReceiptDigest != settlementReviewProviderMeterReceiptDigest(
			result.Evidence, fixture.provider.release,
		) {
		t.Fatalf("captured usage evidence = %+v", result)
	}
	if err := result.Evidence.Validate(); err != nil {
		t.Fatalf("evidence invalid: %v", err)
	}
	measured := fixture.authority.providerMeasured()
	if len(measured) != 1 || measured[0].Review != fixture.review ||
		measured[0].Plugin != result.Evidence.Plugin ||
		measured[0].EvidenceID != result.Evidence.EvidenceID {
		t.Fatalf("authority measurements = %+v", measured)
	}
	if executionTableCount(t, fixture.db, SQLSettlementReviewUsageEvidenceTable,
		"review_id = ?", fixture.review.ReviewID) != 1 ||
		executionTableCount(t, fixture.db, SQLSettlementReviewUsageEvidenceSourceTable,
			"evidence_id = ?", result.Evidence.EvidenceID) != 1 ||
		executionTableCount(t, fixture.db, "test_agent_settlement_review_usage",
			"review_id = ?", fixture.review.ReviewID) != 1 {
		t.Fatal("capture did not atomically persist both evidence receipts")
	}
	listed, err := fixture.store.ListSettlementReviewUsageEvidence(
		context.Background(), ListSettlementReviewUsageEvidenceQuery{},
	)
	if err != nil || len(listed) != 1 || listed[0] != result.Evidence {
		t.Fatalf("ListSettlementReviewUsageEvidence() = %+v, %v", listed, err)
	}
	open, err := fixture.store.ListSettlementReviews(context.Background(), ListSettlementReviewsQuery{})
	if err != nil || len(open) != 1 || open[0] != result.Review {
		t.Fatalf("ListSettlementReviews() = %+v, %v", open, err)
	}
	assertEffectReviewHeld(t, fixture.db, fixture.outboxID)

	replay, err := fixture.store.CaptureSettlementReviewUsageEvidence(context.Background(), command)
	if err != nil || !replay.Replay || replay.Review != result.Review || replay.Evidence != result.Evidence {
		t.Fatalf("exact capture replay = %+v, %v", replay, err)
	}
	if len(fixture.authority.providerMeasured()) != 1 {
		t.Fatal("exact replay called the meter again")
	}

	conflict := command
	conflict.ExpectedRequestDigest = "sha256:" + strings.Repeat("0", 64)
	if _, err := fixture.store.CaptureSettlementReviewUsageEvidence(
		context.Background(), conflict,
	); !errors.Is(err, ErrSettlementReviewUsageConflict) {
		t.Fatalf("conflicting replay = %v", err)
	}
	if len(fixture.authority.providerMeasured()) != 1 {
		t.Fatal("conflicting replay called the meter again")
	}
}

func TestCaptureSettlementReviewUsageEvidenceKeepsZeroSourceReviewPending(t *testing.T) {
	db, store, clock, turns := newSQLClaimNextFixture(t, "usage_zero_source_pending")
	authority := newTestSettlementReviewAuthority(t, db)
	provider := newSettlementReviewProviderUsageTestScope(
		t, db, store, clock, turns[0].Plugin, "usage_zero_source_pending",
	)
	if _, err := store.BindSettlementReviewProviderUsageAuthority(provider.journal, authority); err != nil {
		t.Fatal(err)
	}
	_, review, _ := openSettlementReviewForResolution(
		t, store, clock, turns[0], "usage_zero_source_pending",
	)
	review = ensureProviderAwareSettlementReview(t, db, review)
	if _, err := store.CaptureSettlementReviewUsageEvidence(
		context.Background(), settlementReviewUsageCommand(review),
	); !errors.Is(err, ErrSettlementReviewUsagePending) {
		t.Fatalf("zero-source capture = %v", err)
	}
	if executionTableCount(t, db, SQLSettlementReviewUsageEvidenceTable,
		"review_id = ?", review.ReviewID) != 0 || len(authority.providerMeasured()) != 0 {
		t.Fatal("zero-source capture called Meter or wrote Evidence")
	}
	listed, err := store.ListSettlementReviews(context.Background(), ListSettlementReviewsQuery{})
	if err != nil || len(listed) != 1 || listed[0].Status != SettlementReviewStatusPending {
		t.Fatalf("pending Review = %+v, %v", listed, err)
	}
}

func TestCaptureSettlementReviewUsageEvidenceKeepsOverflowReviewPending(t *testing.T) {
	db, store, clock, turns := newSQLClaimNextFixture(t, "usage_source_overflow")
	authority := newTestSettlementReviewAuthority(t, db)
	provider := newSettlementReviewProviderUsageTestScope(
		t, db, store, clock, turns[0].Plugin, "usage_source_overflow",
	)
	if _, err := store.BindSettlementReviewProviderUsageAuthority(provider.journal, authority); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimAttempt(
		context.Background(), executionClaimCommand(turns[0].ID, "attempt_usage_source_overflow"),
	)
	if err != nil {
		t.Fatal(err)
	}
	var firstAppend AppendAttestedProviderUsageCommand
	for index := 0; index <= MaxProviderUsageSources; index++ {
		appendCommand := AppendAttestedProviderUsageCommand{
			Fence:                 claimed.Attempt.Fence(),
			ProviderRequestDigest: providerUsageTestDigest("overflow-request", fmt.Sprintf("%02d", index)),
			ProviderEventDigest:   providerUsageTestDigest("overflow-event", fmt.Sprintf("%02d", index)),
			CanonicalUsageJSON:    []byte(`{"inputTokens":1}`),
			ProviderReceiptDigest: providerUsageTestDigest("overflow-receipt", fmt.Sprintf("%02d", index)),
			AttestationDigest:     providerUsageTestDigest("overflow-attestation", fmt.Sprintf("%02d", index)),
			ProviderReportedAt:    clock.Get(),
		}
		if index == 0 {
			firstAppend = appendCommand
		}
		result, appendErr := provider.recorder.AppendAttested(context.Background(), appendCommand)
		if appendErr != nil || result.Replay {
			t.Fatalf("append overflow source %d = %+v, %v", index, result, appendErr)
		}
	}
	terminal := CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_usage_source_overflow",
		TerminalStatus: agentv1.TurnStatusFailed,
	}
	committed, err := store.CommitAttempt(context.Background(), terminal)
	if err != nil || committed.SettlementReview == nil {
		t.Fatalf("overflow terminal Review = %+v, %v", committed, err)
	}
	commitReplay, err := store.CommitAttempt(context.Background(), terminal)
	if err != nil || !commitReplay.Replay || commitReplay.SettlementReview == nil ||
		commitReplay.SettlementReview.ReviewID != committed.SettlementReview.ReviewID {
		t.Fatalf("overflow terminal replay = %+v, %v", commitReplay, err)
	}
	journalReplay, err := provider.recorder.AppendAttested(context.Background(), firstAppend)
	if err != nil || !journalReplay.Replay {
		t.Fatalf("overflow journal replay = %+v, %v", journalReplay, err)
	}
	if _, err := store.CaptureSettlementReviewUsageEvidence(
		context.Background(), settlementReviewUsageCommand(*committed.SettlementReview),
	); !errors.Is(err, ErrSettlementReviewUsageOverflow) {
		t.Fatalf("overflow capture = %v", err)
	}
	if executionTableCount(t, db, SQLSettlementReviewUsageEvidenceTable,
		"review_id = ?", committed.SettlementReview.ReviewID) != 0 ||
		len(authority.providerMeasured()) != 0 {
		t.Fatal("overflow capture called Meter or wrote partial Evidence")
	}
	open, err := store.ListSettlementReviews(context.Background(), ListSettlementReviewsQuery{})
	if err != nil || len(open) != 1 || open[0].Status != SettlementReviewStatusPending {
		t.Fatalf("overflow pending Review = %+v, %v", open, err)
	}
}

func TestCaptureSettlementReviewUsageEvidenceCallerCannotAssertCommercialValues(t *testing.T) {
	typeOfCommand := reflect.TypeOf(CaptureSettlementReviewUsageEvidenceCommand{})
	if typeOfCommand.NumField() != 3 {
		t.Fatalf("capture command has %d fields, want only immutable identity", typeOfCommand.NumField())
	}
	for _, forbidden := range []string{
		"UsedUnits", "Price", "PricingSnapshotDigest", "BillingPolicyKey",
		"UsageSourceDigest", "MeasurementDigest", "EvidenceDigest",
	} {
		if _, found := typeOfCommand.FieldByName(forbidden); found {
			t.Fatalf("capture command exposes caller-controlled %s", forbidden)
		}
	}
	resolveType := reflect.TypeOf(ResolveSettlementReviewCommand{})
	for _, forbidden := range []string{"Intent", "UsedUnits", "Reason", "EvidenceDigest"} {
		if _, found := resolveType.FieldByName(forbidden); found {
			t.Fatalf("resolve command exposes caller-controlled %s", forbidden)
		}
	}
}

type testResolutionOnlySettlementReviewAuthority struct {
	target *testSettlementReviewAuthority
}

func (authority testResolutionOnlySettlementReviewAuthority) Settle(
	tx *gorm.DB,
	command SettlementCommand,
) error {
	return authority.target.Settle(tx, command)
}

func (authority testResolutionOnlySettlementReviewAuthority) HoldForReview(
	tx *gorm.DB,
	command SettlementReviewHoldCommand,
) error {
	return authority.target.HoldForReview(tx, command)
}

func (authority testResolutionOnlySettlementReviewAuthority) ResolveReview(
	tx *gorm.DB,
	command SettlementReviewResolutionAuthorityCommand,
) (SettlementReviewResolutionAuthorityReceipt, error) {
	return authority.target.ResolveReview(tx, command)
}

func TestCaptureSettlementReviewUsageEvidenceRequiresSealedUsageCapability(t *testing.T) {
	t.Run("unsealed compatibility authority", func(t *testing.T) {
		fixture := newSettlementReviewUsageFixture(t, "usage_unsealed", false)
		if _, err := fixture.store.CaptureSettlementReviewUsageEvidence(
			context.Background(), settlementReviewUsageCommand(fixture.review),
		); !errors.Is(err, ErrSettlementReviewUsageUnavailable) {
			t.Fatalf("unsealed capture = %v", err)
		}
	})

	t.Run("sealed resolution-only authority", func(t *testing.T) {
		db, store, clock, turns := newSQLClaimNextFixture(t, "usage_resolution_only")
		target := newTestSettlementReviewAuthority(t, db)
		if _, err := store.BindSettlementReviewAuthority(
			testResolutionOnlySettlementReviewAuthority{target: target},
		); err != nil {
			t.Fatal(err)
		}
		_, review, _ := openSettlementReviewForResolution(
			t, store, clock, turns[0], "usage_resolution_only",
		)
		if _, err := store.CaptureSettlementReviewUsageEvidence(
			context.Background(), settlementReviewUsageCommand(review),
		); !errors.Is(err, ErrSettlementReviewUsageUnavailable) {
			t.Fatalf("resolution-only capture = %v", err)
		}
		if _, err := store.ResolveSettlementReview(
			context.Background(), unresolvedSettlementReviewResolutionCommand(review),
		); !errors.Is(err, ErrSettlementReviewResolutionUnavailable) {
			t.Fatalf("resolution-only direct resolve bypass = %v", err)
		}
	})

	t.Run("post-bind mutation", func(t *testing.T) {
		fixture := newSettlementReviewUsageFixture(t, "usage_violated", true)
		fixture.store.WithSettlementAuthority(fixture.authority)
		if _, err := fixture.store.CaptureSettlementReviewUsageEvidence(
			context.Background(), settlementReviewUsageCommand(fixture.review),
		); !errors.Is(err, ErrSettlementBindingInvalid) {
			t.Fatalf("capture after binding mutation = %v", err)
		}
	})
}

type mutatingSettlementReviewUsageAuthority struct {
	target *testSettlementReviewAuthority
	mutate func(*SettlementReviewProviderUsageAuthorityReceipt)
}

type blockingSettlementReviewUsageAuthority struct {
	target  *testSettlementReviewAuthority
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (authority *blockingSettlementReviewUsageAuthority) Settle(
	tx *gorm.DB,
	command SettlementCommand,
) error {
	return authority.target.Settle(tx, command)
}

func (authority *blockingSettlementReviewUsageAuthority) HoldForReview(
	tx *gorm.DB,
	command SettlementReviewHoldCommand,
) error {
	return authority.target.HoldForReview(tx, command)
}

func (authority *blockingSettlementReviewUsageAuthority) MeasureProviderUsage(
	tx *gorm.DB,
	command MeasureSettlementReviewProviderUsageCommand,
) (SettlementReviewProviderUsageAuthorityReceipt, error) {
	authority.once.Do(func() { close(authority.entered) })
	<-authority.release
	return authority.target.MeasureProviderUsage(tx, command)
}

func (authority *blockingSettlementReviewUsageAuthority) ResolveReview(
	tx *gorm.DB,
	command SettlementReviewResolutionAuthorityCommand,
) (SettlementReviewResolutionAuthorityReceipt, error) {
	return authority.target.ResolveReview(tx, command)
}

func TestCaptureSettlementReviewUsageEvidenceSerializesPostBindMutation(t *testing.T) {
	db, store, clock, turns := newSQLClaimNextFixture(t, "usage_binding_linearization")
	target := newTestSettlementReviewAuthority(t, db)
	authority := &blockingSettlementReviewUsageAuthority{
		target: target, entered: make(chan struct{}), release: make(chan struct{}),
	}
	provider := newSettlementReviewProviderUsageTestScope(
		t, db, store, clock, turns[0].Plugin, "usage_binding_linearization",
	)
	binding, err := store.BindSettlementReviewProviderUsageAuthority(provider.journal, authority)
	if err != nil {
		t.Fatal(err)
	}
	terminal, review, _ := openSettlementReviewForResolution(
		t, store, clock, turns[0], "usage_binding_linearization",
	)
	review = ensureProviderAwareSettlementReview(t, db, review)
	appendSettlementReviewProviderUsage(t, provider, clock, terminal, "usage_binding_linearization")
	type captureOutcome struct {
		result CaptureSettlementReviewUsageEvidenceResult
		err    error
	}
	captured := make(chan captureOutcome, 1)
	go func() {
		result, captureErr := store.CaptureSettlementReviewUsageEvidence(
			context.Background(), settlementReviewUsageCommand(review),
		)
		captured <- captureOutcome{result: result, err: captureErr}
	}()
	select {
	case <-authority.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("MeasureProviderUsage was not entered")
	}
	if store.settlementMu.TryLock() {
		store.settlementMu.Unlock()
		t.Fatal("capture released the binding lock inside the meter transaction")
	}
	mutated := make(chan struct{})
	go func() {
		store.WithSettlementAuthority(target)
		close(mutated)
	}()
	select {
	case <-mutated:
		t.Fatal("post-bind mutator completed while the meter was active")
	case <-time.After(50 * time.Millisecond):
	}
	close(authority.release)
	select {
	case outcome := <-captured:
		if outcome.err != nil || outcome.result.Replay {
			t.Fatalf("capture = %+v, %v", outcome.result, outcome.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("capture did not finish after the meter was released")
	}
	select {
	case <-mutated:
	case <-time.After(2 * time.Second):
		t.Fatal("post-bind mutator did not resume after capture committed")
	}
	if store.MatchesSettlementAuthorityBinding(binding) {
		t.Fatal("post-bind mutation did not permanently invalidate the sealed binding")
	}
}

func (authority mutatingSettlementReviewUsageAuthority) Settle(
	tx *gorm.DB,
	command SettlementCommand,
) error {
	return authority.target.Settle(tx, command)
}

func (authority mutatingSettlementReviewUsageAuthority) HoldForReview(
	tx *gorm.DB,
	command SettlementReviewHoldCommand,
) error {
	return authority.target.HoldForReview(tx, command)
}

func (authority mutatingSettlementReviewUsageAuthority) MeasureProviderUsage(
	tx *gorm.DB,
	command MeasureSettlementReviewProviderUsageCommand,
) (SettlementReviewProviderUsageAuthorityReceipt, error) {
	receipt, err := authority.target.MeasureProviderUsage(tx, command)
	if err == nil {
		authority.mutate(&receipt)
	}
	return receipt, err
}

func (authority mutatingSettlementReviewUsageAuthority) ResolveReview(
	tx *gorm.DB,
	command SettlementReviewResolutionAuthorityCommand,
) (SettlementReviewResolutionAuthorityReceipt, error) {
	return authority.target.ResolveReview(tx, command)
}

func TestCaptureSettlementReviewUsageEvidenceFailureRollsBackAndSanitizes(t *testing.T) {
	t.Run("meter failure", func(t *testing.T) {
		fixture := newSettlementReviewUsageFixture(t, "usage_meter_failure", true)
		fixture.authority.setMeasurementFailure(errors.New("secret provider usage journal 991"))
		_, err := fixture.store.CaptureSettlementReviewUsageEvidence(
			context.Background(), settlementReviewUsageCommand(fixture.review),
		)
		if !errors.Is(err, ErrSettlementReviewUsageFailed) || strings.Contains(err.Error(), "secret") {
			t.Fatalf("meter failure = %v", err)
		}
		assertSettlementReviewUsageCaptureRolledBack(t, fixture.db, fixture.store, fixture.review)
	})

	malformed := []struct {
		name   string
		mutate func(*SettlementReviewProviderUsageAuthorityReceipt)
	}{
		{name: "zero units", mutate: func(receipt *SettlementReviewProviderUsageAuthorityReceipt) {
			receipt.UsedUnits = 0
		}},
		{name: "bad measurement digest", mutate: func(receipt *SettlementReviewProviderUsageAuthorityReceipt) {
			receipt.MeasurementDigest = "sha256:not-a-digest"
		}},
	}
	for _, test := range malformed {
		t.Run(test.name, func(t *testing.T) {
			suffix := "usage_malformed_" + strings.ReplaceAll(test.name, " ", "_")
			db, store, clock, turns := newSQLClaimNextFixture(t, suffix)
			target := newTestSettlementReviewAuthority(t, db)
			authority := mutatingSettlementReviewUsageAuthority{target: target, mutate: test.mutate}
			provider := newSettlementReviewProviderUsageTestScope(t, db, store, clock, turns[0].Plugin, suffix)
			if _, err := store.BindSettlementReviewProviderUsageAuthority(provider.journal, authority); err != nil {
				t.Fatal(err)
			}
			terminal, review, _ := openSettlementReviewForResolution(t, store, clock, turns[0], suffix)
			review = ensureProviderAwareSettlementReview(t, db, review)
			appendSettlementReviewProviderUsage(t, provider, clock, terminal, suffix)
			_, err := store.CaptureSettlementReviewUsageEvidence(
				context.Background(), settlementReviewUsageCommand(review),
			)
			if !errors.Is(err, ErrSettlementReviewUsageFailed) {
				t.Fatalf("malformed meter receipt = %v", err)
			}
			assertSettlementReviewUsageCaptureRolledBack(t, db, store, review)
		})
	}
}

func assertSettlementReviewUsageCaptureRolledBack(
	t *testing.T,
	db *gorm.DB,
	store *SQLStore,
	review SettlementReviewRecord,
) {
	t.Helper()
	if executionTableCount(t, db, SQLSettlementReviewUsageEvidenceTable,
		"review_id = ?", review.ReviewID) != 0 ||
		executionTableCount(t, db, "test_agent_settlement_review_usage",
			"review_id = ?", review.ReviewID) != 0 {
		t.Fatal("failed capture left a durable usage receipt")
	}
	open, err := store.ListSettlementReviews(context.Background(), ListSettlementReviewsQuery{})
	if err != nil || len(open) != 1 || open[0].Status != SettlementReviewStatusPending {
		t.Fatalf("pending review after failed capture = %+v, %v", open, err)
	}
}

func TestSettlementReviewUsageEvidenceAuditRejectsCoordinatedWrongPlugin(t *testing.T) {
	fixture := newSettlementReviewUsageFixture(t, "usage_wrong_plugin", true)
	result, err := fixture.store.CaptureSettlementReviewUsageEvidence(
		context.Background(), settlementReviewUsageCommand(fixture.review),
	)
	if err != nil {
		t.Fatal(err)
	}
	tampered := result.Evidence
	tampered.Plugin.Version = "9.9.9"
	if err := fixture.db.Table(SQLSettlementReviewUsageEvidenceTable).
		Where("review_id = ?", fixture.review.ReviewID).
		UpdateColumns(map[string]any{
			"plugin_version": tampered.Plugin.Version,
		}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.ListSettlementReviewUsageEvidence(
		context.Background(), ListSettlementReviewUsageEvidenceQuery{},
	); !errors.Is(err, ErrStoreIntegrity) {
		t.Fatalf("wrong-plugin evidence audit = %v", err)
	}
	if _, err := fixture.store.CaptureSettlementReviewUsageEvidence(
		context.Background(), settlementReviewUsageCommand(fixture.review),
	); !errors.Is(err, ErrStoreIntegrity) {
		t.Fatalf("wrong-plugin capture replay = %v", err)
	}
}

func TestOpenSettlementReviewListCandidatePromotesConcurrentMeasurement(t *testing.T) {
	fixture := newSettlementReviewUsageFixture(t, "usage_list_measurement", true)
	stale := fixture.review
	result, err := fixture.store.CaptureSettlementReviewUsageEvidence(
		context.Background(), settlementReviewUsageCommand(fixture.review),
	)
	if err != nil {
		t.Fatal(err)
	}
	current, stillOpen, err := fixture.store.validateOpenSettlementReviewListCandidate(
		context.Background(), stale,
	)
	if err != nil || !stillOpen || current != result.Review {
		t.Fatalf("stale pending candidate = %+v, %t, %v", current, stillOpen, err)
	}
}

func TestCaptureSettlementReviewUsageEvidenceSerializesConcurrentStores(t *testing.T) {
	fixture := newSettlementReviewUsageFixture(t, "usage_concurrent", true)
	second := mustSQLStore(t, fixture.db)
	second.executionClock = fixture.clock.Now
	secondJournal, err := NewProviderUsageJournal(second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.BindSettlementReviewProviderUsageAuthority(secondJournal, fixture.authority); err != nil {
		t.Fatal(err)
	}
	command := settlementReviewUsageCommand(fixture.review)
	type outcome struct {
		result CaptureSettlementReviewUsageEvidenceResult
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
			result, err := candidate.CaptureSettlementReviewUsageEvidence(
				context.Background(), command,
			)
			outcomes <- outcome{result: result, err: err}
		}(store)
	}
	ready.Wait()
	close(start)
	first, secondOutcome := <-outcomes, <-outcomes
	if first.err != nil || secondOutcome.err != nil {
		t.Fatalf("concurrent captures = %+v / %+v", first, secondOutcome)
	}
	if first.result.Replay == secondOutcome.result.Replay ||
		first.result.Evidence != secondOutcome.result.Evidence {
		t.Fatalf("concurrent capture results = %+v / %+v", first.result, secondOutcome.result)
	}
	if len(fixture.authority.providerMeasured()) != 1 ||
		executionTableCount(t, fixture.db, SQLSettlementReviewUsageEvidenceTable,
			"review_id = ?", fixture.review.ReviewID) != 1 {
		t.Fatal("concurrent capture measured or persisted more than once")
	}
}
