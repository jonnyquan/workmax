package agentbilling

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	agentv1 "server/contracts/agent/v1"
	"server/model"
	"server/service/account"
	"server/service/agentturn"
	"server/utils/testutil"
)

type testProviderUsageMeter struct {
	mu      sync.Mutex
	used    int64
	failure error
	calls   []agentturn.MeasureSettlementReviewProviderUsageCommand
}

func (meter *testProviderUsageMeter) MeasureProviderUsage(
	tx *gorm.DB,
	command agentturn.MeasureSettlementReviewProviderUsageCommand,
) (agentturn.SettlementReviewProviderUsageAuthorityReceipt, error) {
	if meter == nil || tx == nil {
		return agentturn.SettlementReviewProviderUsageAuthorityReceipt{}, ErrProviderUsageMeterUnavailable
	}
	if err := command.Validate(); err != nil {
		return agentturn.SettlementReviewProviderUsageAuthorityReceipt{}, err
	}
	meter.mu.Lock()
	defer meter.mu.Unlock()
	meter.calls = append(meter.calls, command)
	if meter.failure != nil {
		return agentturn.SettlementReviewProviderUsageAuthorityReceipt{}, meter.failure
	}
	return agentturn.SettlementReviewProviderUsageAuthorityReceipt{
		MeasurementDigest: digest("agentbilling-provider-meter-v1", command.UsageSourceDigest),
		UsedUnits:         meter.used,
	}, nil
}

func (meter *testProviderUsageMeter) callCount() int {
	meter.mu.Lock()
	defer meter.mu.Unlock()
	return len(meter.calls)
}

type providerBillingFixture struct {
	db            *gorm.DB
	store         *agentturn.SQLStore
	credits       *CreditSettlementAuthority
	composite     *ProviderUsageCreditAuthority
	meter         *testProviderUsageMeter
	turn          agentturn.Turn
	attempt       agentturn.TurnAttempt
	recorder      *agentturn.ProviderUsageRecorder
	release       agentturn.UsageMeterReleaseRecord
	reservationID uint
	packID        uint
}

func newProviderBillingFixture(
	t *testing.T,
	suffix string,
	meter *testProviderUsageMeter,
) providerBillingFixture {
	t.Helper()
	db := testutil.NewTestDB(t)
	user := model.User{Member: 0, Nickname: "provider-billing-test", Email: suffix + "@example.com"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	pack := model.CreditsPack{
		UID: int(user.Id), SourceType: model.CreditsSourcePurchase,
		SourceID: "provider-agentbilling-" + suffix, CreditsTotal: 100,
	}
	if err := db.Create(&pack).Error; err != nil {
		t.Fatalf("seed pack: %v", err)
	}
	store, err := agentturn.NewSQLStore(db)
	if err != nil {
		t.Fatal(err)
	}
	credits, err := NewCreditSettlementAuthority(db, account.NewCreditReservationService())
	if err != nil {
		t.Fatal(err)
	}
	turn := billingTestTurn("provider_" + suffix)
	source, err := agentturn.NewProviderUsageSourceRegistration(agentturn.ProviderUsageSourceRegistrationSpec{
		ProviderKey:             "provider.test",
		ProviderAccountDigest:   digest("agentbilling-provider-account-v1", suffix),
		SourceKey:               "provider.test.result",
		SourceVersion:           "1",
		SourceBuildDigest:       digest("agentbilling-provider-source-build-v1", suffix),
		UsageSchemaKey:          "provider.test.usage",
		UsageSchemaVersion:      "1",
		SourceSchemaDigest:      digest("agentbilling-provider-source-schema-v1", suffix),
		VerificationKind:        "adapter_attestation",
		VerificationKeyDigest:   digest("agentbilling-provider-verification-key-v1", suffix),
		VerificationBuildDigest: digest("agentbilling-provider-verification-build-v1", suffix),
	})
	if err != nil {
		t.Fatalf("source registration: %v", err)
	}
	release, err := agentturn.NewUsageMeterReleaseRecord(agentturn.UsageMeterReleaseSpec{
		Plugin: turn.Plugin, BillingPolicyKey: "writer.turn.v1",
		PricingSnapshotJSON: json.RawMessage(`{"creditsPerUSD":200}`),
		MeterKey:            "workmax.writer.turn",
		MeterVersion:        "1.0.0",
		MeterBuildDigest:    digest("agentbilling-provider-meter-build-v1", suffix),
		Sources:             []agentturn.ProviderUsageSourceRegistration{source},
	}, time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond))
	if err != nil {
		t.Fatalf("meter release: %v", err)
	}
	insertProviderMeterRelease(t, db, release)
	journal, err := agentturn.NewProviderUsageJournal(store)
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := journal.ScopeRecorder(context.Background(), turn.Plugin, release.ReleaseID, source)
	if err != nil {
		t.Fatalf("scope recorder: %v", err)
	}
	admission, err := credits.Admission(ReservationAdmission{
		PrincipalID: turn.PrincipalID,
		Reservation: account.ReservationRequest{
			UID: int(user.Id), Tool: "workagent", IdempotencyKey: "provider-reservation-" + suffix,
			Reserved: 10, TTL: time.Hour, Remark: "provider agent billing test",
		},
		PricingSnapshotDigest: release.PricingSnapshotDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := store.AdmitWithReservationAuthority(
		context.Background(), turn,
		agentturn.EventDraft{Type: agentv1.EventCoreTurnStatus, Data: json.RawMessage(`{"status":"queued"}`)},
		admission,
	)
	if err != nil || !admitted.Created {
		t.Fatalf("bound admission = %+v, %v", admitted, err)
	}
	composite, err := NewProviderUsageCreditAuthority(credits, meter)
	if err != nil {
		t.Fatal(err)
	}
	if binding, bindErr := store.BindSettlementReviewProviderUsageAuthority(journal, composite); bindErr != nil || binding == nil || !store.MatchesSettlementAuthorityBinding(binding) {
		t.Fatalf("provider-aware binding = %p, %v", binding, bindErr)
	}
	claimed, err := store.ClaimAttempt(context.Background(), agentturn.ClaimAttemptCommand{
		TurnID: turn.ID, AttemptID: "attempt_provider_billing_" + suffix,
		WorkerID: "worker_provider_billing", WorkerBuildDigest: "sha256:worker-provider-billing",
	})
	if err != nil {
		t.Fatalf("claim with composite execution gate: %v", err)
	}
	if _, err := recorder.AppendAttested(context.Background(), agentturn.AppendAttestedProviderUsageCommand{
		Fence:                 claimed.Attempt.Fence(),
		ProviderRequestDigest: digest("agentbilling-provider-request-v1", suffix),
		ProviderEventDigest:   digest("agentbilling-provider-event-v1", suffix),
		CanonicalUsageJSON:    json.RawMessage(`{"inputTokens":3,"outputTokens":5}`),
		ProviderReceiptDigest: digest("agentbilling-provider-receipt-v1", suffix),
		AttestationDigest:     digest("agentbilling-provider-attestation-v1", suffix),
		ProviderReportedAt:    claimed.Attempt.ClaimedAt,
	}); err != nil {
		t.Fatalf("append provider usage: %v", err)
	}
	var binding bindingRow
	if err := db.Table(BindingTable).Where("turn_id = ?", string(turn.ID)).Take(&binding).Error; err != nil {
		t.Fatalf("load binding: %v", err)
	}
	return providerBillingFixture{
		db: db, store: store, credits: credits, composite: composite, meter: meter,
		turn: admitted.Turn, attempt: claimed.Attempt, recorder: recorder, release: release,
		reservationID: uint(binding.ReservationID), packID: pack.Id,
	}
}

func insertProviderMeterRelease(t *testing.T, db *gorm.DB, release agentturn.UsageMeterReleaseRecord) {
	t.Helper()
	row := map[string]any{
		"release_id": release.ReleaseID, "plugin_id": release.Plugin.ID,
		"plugin_version": release.Plugin.Version, "plugin_release_digest": release.Plugin.ReleaseDigest,
		"plugin_snapshot_digest": release.PluginSnapshotDigest, "billing_policy_key": release.BillingPolicyKey,
		"pricing_snapshot_json":   []byte(release.PricingSnapshotJSON),
		"pricing_snapshot_digest": release.PricingSnapshotDigest,
		"meter_key":               release.MeterKey, "meter_version": release.MeterVersion,
		"meter_build_digest":     release.MeterBuildDigest,
		"source_registry_json":   []byte(release.SourceRegistryJSON),
		"source_registry_digest": release.SourceRegistryDigest,
		"release_digest":         release.ReleaseDigest, "created_at": release.CreatedAt,
	}
	if err := db.Table(agentturn.SQLUsageMeterReleaseTable).Create(row).Error; err != nil {
		t.Fatalf("insert meter release: %v", err)
	}
}

func (fixture providerBillingFixture) terminalReview(t *testing.T) agentturn.SettlementReviewRecord {
	t.Helper()
	result, err := fixture.store.CommitAttempt(context.Background(), agentturn.CommitAttemptCommand{
		Fence: fixture.attempt.Fence(), OperationID: "operation_provider_billing_" + string(fixture.turn.ID),
		TerminalStatus: agentv1.TurnStatusCompleted,
	})
	if err != nil || result.SettlementReview == nil {
		t.Fatalf("provider-aware terminal Review = %+v, %v", result, err)
	}
	return *result.SettlementReview
}

func TestProviderUsageCreditAuthorityRejectsNilDependencies(t *testing.T) {
	db := testutil.NewTestDB(t)
	store, err := agentturn.NewSQLStore(db)
	if err != nil {
		t.Fatal(err)
	}
	credits, err := NewCreditSettlementAuthority(db, account.NewCreditReservationService())
	if err != nil {
		t.Fatal(err)
	}
	var typedNilMeter *testProviderUsageMeter
	if _, err := NewProviderUsageCreditAuthority(nil, &testProviderUsageMeter{used: 1}); !errors.Is(err, ErrLedgerUnavailable) {
		t.Fatalf("nil Credits error = %v", err)
	}
	if _, err := NewProviderUsageCreditAuthority(&CreditSettlementAuthority{}, &testProviderUsageMeter{used: 1}); !errors.Is(err, ErrLedgerUnavailable) {
		t.Fatalf("zero Credits error = %v", err)
	}
	if _, err := NewProviderUsageCreditAuthority(credits, nil); !errors.Is(err, ErrProviderUsageMeterUnavailable) {
		t.Fatalf("nil meter error = %v", err)
	}
	if _, err := NewProviderUsageCreditAuthority(credits, typedNilMeter); !errors.Is(err, ErrProviderUsageMeterUnavailable) {
		t.Fatalf("typed-nil meter error = %v", err)
	}

	var nilComposite *ProviderUsageCreditAuthority
	journal, err := agentturn.NewProviderUsageJournal(store)
	if err != nil {
		t.Fatal(err)
	}
	if binding, err := store.BindSettlementReviewProviderUsageAuthority(journal, nilComposite); binding != nil ||
		!errors.Is(err, agentturn.ErrSettlementBindingInvalid) {
		t.Fatalf("typed-nil composite binding = %p, %v", binding, err)
	}
	if err := nilComposite.Settle(nil, agentturn.SettlementCommand{}); !errors.Is(err, ErrLedgerUnavailable) {
		t.Fatalf("nil composite Settle error = %v", err)
	}
	if err := nilComposite.HoldForReview(nil, agentturn.SettlementReviewHoldCommand{}); !errors.Is(err, ErrLedgerUnavailable) {
		t.Fatalf("nil composite HoldForReview error = %v", err)
	}
	if _, err := nilComposite.ResolveReview(nil, agentturn.SettlementReviewResolutionAuthorityCommand{}); !errors.Is(err, ErrLedgerUnavailable) {
		t.Fatalf("nil composite ResolveReview error = %v", err)
	}
	if err := nilComposite.AuthorizeTurnExecution(nil, agentturn.Turn{}); !errors.Is(err, ErrLedgerUnavailable) {
		t.Fatalf("nil composite execution error = %v", err)
	}
	if _, err := nilComposite.MeasureProviderUsage(nil, agentturn.MeasureSettlementReviewProviderUsageCommand{}); !errors.Is(err, ErrProviderUsageMeterUnavailable) {
		t.Fatalf("nil composite meter error = %v", err)
	}
}

func TestProviderUsageCreditAuthorityDelegatesOrdinarySettlement(t *testing.T) {
	fixture := newBillingFixture(t, "composite_ordinary", 10)
	composite, err := NewProviderUsageCreditAuthority(
		fixture.authority,
		&testProviderUsageMeter{used: 3},
	)
	if err != nil {
		t.Fatal(err)
	}
	fixture.store.WithSettlementAuthority(composite)
	result, err := fixture.store.CommitAttempt(context.Background(), agentturn.CommitAttemptCommand{
		Fence: fixture.attempt.Fence(), OperationID: "operation_composite_ordinary",
		TerminalStatus: agentv1.TurnStatusCompleted,
		Settlement: &agentturn.SettlementRequest{
			Intent: agentturn.SettlementIntentFinalize, UsedUnits: 3,
		},
	})
	if err != nil || result.Replay || result.SettlementReview != nil {
		t.Fatalf("ordinary settlement through composite = %+v, %v", result, err)
	}
	outcome, err := fixture.authority.GetOutcome(context.Background(), fixture.turn.PrincipalID, fixture.turn.ID)
	if err != nil || outcome.Status != OutcomeStatusFinalized || outcome.UsedUnits == nil || *outcome.UsedUnits != 3 {
		t.Fatalf("ordinary composite outcome = %+v, %v", outcome, err)
	}
	assertBillingFinancialState(t, fixture, model.CreditReservationStatusFinalized, 3, 3)
}

func TestProviderUsageCreditAuthorityHoldsReviewForAttemptAuthorizedBeforeReservationExpiry(t *testing.T) {
	fixture := newProviderBillingFixture(t, "review_after_ttl", &testProviderUsageMeter{used: 4})
	expiredAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	if err := fixture.db.Model(&model.CreditReservation{}).
		Where("id = ?", fixture.reservationID).
		Update("expires_at", expiredAt).Error; err != nil {
		t.Fatal(err)
	}
	review := fixture.terminalReview(t)
	if review.Status != agentturn.SettlementReviewStatusPending {
		t.Fatalf("review status after TTL = %q", review.Status)
	}
	outcome, err := fixture.credits.GetOutcome(
		context.Background(), fixture.turn.PrincipalID, fixture.turn.ID,
	)
	if err != nil || outcome.Status != OutcomeStatusReviewHeld || outcome.ReviewID == nil ||
		*outcome.ReviewID != review.ReviewID {
		t.Fatalf("held outcome after TTL = %+v, %v", outcome, err)
	}
	assertProviderBillingFinancialState(t, fixture, model.CreditReservationStatusReviewHold, 0, 10)
}

func TestProviderUsageCreditAuthorityMeterFailureDoesNotMoveCredits(t *testing.T) {
	meterFailure := errors.New("test provider meter unavailable")
	fixture := newProviderBillingFixture(t, "meter_failure", &testProviderUsageMeter{used: 4, failure: meterFailure})
	review := fixture.terminalReview(t)
	beforeReservation := loadProviderBillingReservation(t, fixture)
	beforeOutcome, err := fixture.credits.GetOutcome(context.Background(), fixture.turn.PrincipalID, fixture.turn.ID)
	if err != nil || beforeOutcome.Status != OutcomeStatusReviewHeld {
		t.Fatalf("held outcome before capture = %+v, %v", beforeOutcome, err)
	}
	var beforePack model.CreditsPack
	if err := fixture.db.First(&beforePack, fixture.packID).Error; err != nil {
		t.Fatal(err)
	}

	_, err = fixture.store.CaptureSettlementReviewUsageEvidence(context.Background(),
		agentturn.CaptureSettlementReviewUsageEvidenceCommand{
			TurnID: fixture.turn.ID, ReviewID: review.ReviewID,
			ExpectedRequestDigest: review.RequestDigest,
		})
	if !errors.Is(err, agentturn.ErrSettlementReviewUsageFailed) {
		t.Fatalf("meter failure error = %v", err)
	}
	if fixture.meter.callCount() != 1 {
		t.Fatalf("meter calls = %d, want 1", fixture.meter.callCount())
	}
	afterReservation := loadProviderBillingReservation(t, fixture)
	afterOutcome, outcomeErr := fixture.credits.GetOutcome(context.Background(), fixture.turn.PrincipalID, fixture.turn.ID)
	if outcomeErr != nil || !reflect.DeepEqual(afterOutcome, beforeOutcome) ||
		!reflect.DeepEqual(afterReservation, beforeReservation) {
		t.Fatalf("meter failure moved Credits: reservation before=%+v after=%+v outcome before=%+v after=%+v err=%v",
			beforeReservation, afterReservation, beforeOutcome, afterOutcome, outcomeErr)
	}
	var afterPack model.CreditsPack
	if err := fixture.db.First(&afterPack, fixture.packID).Error; err != nil {
		t.Fatal(err)
	}
	if afterPack.CreditsUsed != beforePack.CreditsUsed {
		t.Fatalf("meter failure moved Pack used from %d to %d", beforePack.CreditsUsed, afterPack.CreditsUsed)
	}
}

func TestProviderUsageCreditAuthorityCaptureResolveFinalizesCredits(t *testing.T) {
	fixture := newProviderBillingFixture(t, "capture_resolve", &testProviderUsageMeter{used: 4})
	review := fixture.terminalReview(t)
	captured, err := fixture.store.CaptureSettlementReviewUsageEvidence(context.Background(),
		agentturn.CaptureSettlementReviewUsageEvidenceCommand{
			TurnID: fixture.turn.ID, ReviewID: review.ReviewID,
			ExpectedRequestDigest: review.RequestDigest,
		})
	if err != nil || captured.Replay || captured.Evidence.UsedUnits != 4 ||
		captured.Evidence.PricingSnapshotDigest != fixture.release.PricingSnapshotDigest ||
		captured.Review.Status != agentturn.SettlementReviewStatusMeteredHeld {
		t.Fatalf("captured provider usage = %+v, %v", captured, err)
	}
	resolved, err := fixture.store.ResolveSettlementReview(context.Background(), agentturn.ResolveSettlementReviewCommand{
		TurnID: fixture.turn.ID, ReviewID: captured.Review.ReviewID,
		ExpectedRequestDigest:  captured.Review.RequestDigest,
		EvidenceID:             captured.Evidence.EvidenceID,
		ExpectedEvidenceDigest: captured.Evidence.EvidenceDigest,
		ActorID:                "operator_finance_provider_billing",
	})
	if err != nil || resolved.Replay || resolved.Review.Status != agentturn.SettlementReviewStatusFinalizedHeld ||
		resolved.Resolution.UsedUnits != 4 || resolved.Resolution.ReservedUnits != 10 {
		t.Fatalf("resolved provider Review = %+v, %v", resolved, err)
	}
	outcome, err := fixture.credits.GetOutcome(context.Background(), fixture.turn.PrincipalID, fixture.turn.ID)
	if err != nil || outcome.Status != OutcomeStatusFinalized || outcome.RequestedIntent != RequestedIntentReview ||
		outcome.UsedUnits == nil || *outcome.UsedUnits != 4 || outcome.ResolutionID == nil ||
		*outcome.ResolutionID != resolved.Resolution.ResolutionID || outcome.ResolutionRequestDigest == nil ||
		*outcome.ResolutionRequestDigest != resolved.Resolution.DecisionDigest {
		t.Fatalf("final provider billing outcome = %+v, %v", outcome, err)
	}
	reservation := loadProviderBillingReservation(t, fixture)
	if reservation.Status != model.CreditReservationStatusFinalized || reservation.Used != 4 {
		t.Fatalf("final reservation = %+v", reservation)
	}
	var pack model.CreditsPack
	if err := fixture.db.First(&pack, fixture.packID).Error; err != nil {
		t.Fatal(err)
	}
	if pack.CreditsUsed != 4 || fixture.meter.callCount() != 1 {
		t.Fatalf("final Pack used=%d meter calls=%d", pack.CreditsUsed, fixture.meter.callCount())
	}
}

func TestCreditSettlementOutcomeRejectsTamperedResolutionOwner(t *testing.T) {
	fixture := newProviderBillingFixture(t, "resolution_owner_tamper", &testProviderUsageMeter{used: 4})
	review := fixture.terminalReview(t)
	captured, err := fixture.store.CaptureSettlementReviewUsageEvidence(
		context.Background(),
		agentturn.CaptureSettlementReviewUsageEvidenceCommand{
			TurnID: fixture.turn.ID, ReviewID: review.ReviewID,
			ExpectedRequestDigest: review.RequestDigest,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := fixture.store.ResolveSettlementReview(
		context.Background(),
		agentturn.ResolveSettlementReviewCommand{
			TurnID: fixture.turn.ID, ReviewID: captured.Review.ReviewID,
			ExpectedRequestDigest:  captured.Review.RequestDigest,
			EvidenceID:             captured.Evidence.EvidenceID,
			ExpectedEvidenceDigest: captured.Evidence.EvidenceDigest,
			ActorID:                "operator_resolution_owner_tamper",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Table(agentturn.SQLSettlementReviewResolutionTable).
		Where("resolution_id = ?", resolved.Resolution.ResolutionID).
		Update("decision_digest", digest("agentbilling-forged-resolution-decision-v1", resolved.Resolution.ResolutionID)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.credits.GetOutcome(
		context.Background(), fixture.turn.PrincipalID, fixture.turn.ID,
	); !errors.Is(err, ErrOutcomeConflict) {
		t.Fatalf("tampered Resolution owner error = %v", err)
	}
	if err := fixture.db.Table(agentturn.SQLSettlementReviewResolutionTable).
		Where("resolution_id = ?", resolved.Resolution.ResolutionID).
		Updates(map[string]any{
			"decision_digest": resolved.Resolution.DecisionDigest,
			"actor_id":        "operator_resolution_owner_forged",
		}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.credits.GetOutcome(
		context.Background(), fixture.turn.PrincipalID, fixture.turn.ID,
	); !errors.Is(err, ErrOutcomeConflict) {
		t.Fatalf("tampered Resolution receipt error = %v", err)
	}
	if err := fixture.db.Exec(
		"DELETE FROM "+agentturn.SQLSettlementReviewResolutionTable+" WHERE resolution_id = ?",
		resolved.Resolution.ResolutionID,
	).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.credits.GetOutcome(
		context.Background(), fixture.turn.PrincipalID, fixture.turn.ID,
	); !errors.Is(err, ErrOutcomeConflict) {
		t.Fatalf("missing Resolution owner error = %v", err)
	}
	assertProviderBillingFinancialState(t, fixture, model.CreditReservationStatusFinalized, 4, 4)
}

func TestCreditSettlementOutcomeVerifiesResolutionProjectionBothWays(t *testing.T) {
	fixture := newProviderBillingFixture(t, "resolution_projection", &testProviderUsageMeter{used: 4})
	review := fixture.terminalReview(t)
	captured, err := fixture.store.CaptureSettlementReviewUsageEvidence(
		context.Background(),
		agentturn.CaptureSettlementReviewUsageEvidenceCommand{
			TurnID: fixture.turn.ID, ReviewID: review.ReviewID,
			ExpectedRequestDigest: review.RequestDigest,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := fixture.store.ResolveSettlementReview(
		context.Background(),
		agentturn.ResolveSettlementReviewCommand{
			TurnID: fixture.turn.ID, ReviewID: captured.Review.ReviewID,
			ExpectedRequestDigest:  captured.Review.RequestDigest,
			EvidenceID:             captured.Evidence.EvidenceID,
			ExpectedEvidenceDigest: captured.Evidence.EvidenceDigest,
			ActorID:                "operator_resolution_projection",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := fixture.credits.GetOutcome(context.Background(), fixture.turn.PrincipalID, fixture.turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	var bindingSQL bindingRow
	if err := fixture.db.Table(BindingTable).Where("turn_id = ?", string(fixture.turn.ID)).Take(&bindingSQL).Error; err != nil {
		t.Fatal(err)
	}
	binding, err := bindingSQL.record()
	if err != nil {
		t.Fatal(err)
	}
	var resolutionSQL resolutionOwnerRow
	if err := fixture.db.Table(agentturn.SQLSettlementReviewResolutionTable).
		Where("resolution_id = ?", resolved.Resolution.ResolutionID).Take(&resolutionSQL).Error; err != nil {
		t.Fatal(err)
	}
	resolution, err := resolutionSQL.record()
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyResolutionProjection(binding, outcome, resolution); err != nil {
		t.Fatalf("exact Resolution projection: %v", err)
	}
	forgedBinding := binding
	forgedBinding.PricingSnapshotDigest = digest("agentbilling-forged-binding-pricing-v1", "resolution_projection")
	if err := verifyResolutionProjection(forgedBinding, outcome, resolution); !errors.Is(err, ErrOutcomeConflict) {
		t.Fatalf("pricing linkage error = %v", err)
	}
	forgedReceipt := resolution
	forgedReceipt.AuthorityReceiptDigest = digest("agentbilling-forged-authority-receipt-v1", "resolution_projection")
	if err := verifyResolutionProjection(binding, outcome, forgedReceipt); !errors.Is(err, ErrOutcomeConflict) {
		t.Fatalf("authority receipt linkage error = %v", err)
	}

	withoutProjection := outcome
	withoutProjection.ResolutionID = nil
	withoutProjection.ResolutionRequestDigest = nil
	if err := fixture.db.Transaction(func(tx *gorm.DB) error {
		return verifyOutcomeResolutionOwner(tx, binding, resolved.Review, true, withoutProjection)
	}); !errors.Is(err, ErrOutcomeConflict) {
		t.Fatalf("unprojected child Resolution error = %v", err)
	}

	// Move only the parent projection time. Both rows remain independently
	// valid, so recovery must reject the cross-row equality drift rather than
	// merely failing Resolution's own digest validation first.
	if err := fixture.db.Table(agentturn.SQLSettlementReviewTable).
		Where("review_id = ?", resolved.Review.ReviewID).
		Update("updated_at", resolved.Review.UpdatedAt.Add(time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.credits.GetOutcome(
		context.Background(), fixture.turn.PrincipalID, fixture.turn.ID,
	); !errors.Is(err, ErrOutcomeConflict) {
		t.Fatalf("Resolution/Review time projection error = %v", err)
	}
}

func TestCreditSettlementOutcomeRejectsTamperedReviewOwner(t *testing.T) {
	fixture := newProviderBillingFixture(t, "review_owner_tamper", &testProviderUsageMeter{used: 4})
	review := fixture.terminalReview(t)
	if err := fixture.db.Table(agentturn.SQLSettlementReviewTable).
		Where("review_id = ?", review.ReviewID).
		Update("prior_provider_usage_count", review.Evidence.PriorProviderUsageCount+1).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.credits.GetOutcome(
		context.Background(), fixture.turn.PrincipalID, fixture.turn.ID,
	); !errors.Is(err, ErrOutcomeConflict) {
		t.Fatalf("tampered Review owner error = %v", err)
	}
	assertProviderBillingFinancialState(t, fixture, model.CreditReservationStatusReviewHold, 0, 10)
}

func loadProviderBillingReservation(t *testing.T, fixture providerBillingFixture) model.CreditReservation {
	t.Helper()
	var reservation model.CreditReservation
	if err := fixture.db.First(&reservation, fixture.reservationID).Error; err != nil {
		t.Fatal(err)
	}
	return reservation
}

func assertProviderBillingFinancialState(
	t *testing.T,
	fixture providerBillingFixture,
	status string,
	used int,
	packUsed int,
) {
	t.Helper()
	reservation := loadProviderBillingReservation(t, fixture)
	if reservation.Status != status || reservation.Used != used {
		t.Fatalf("reservation = status:%s used:%d, want %s/%d", reservation.Status, reservation.Used, status, used)
	}
	var pack model.CreditsPack
	if err := fixture.db.First(&pack, fixture.packID).Error; err != nil {
		t.Fatal(err)
	}
	if pack.CreditsUsed != packUsed {
		t.Fatalf("pack used = %d, want %d", pack.CreditsUsed, packUsed)
	}
}
