package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"server/config"
	agentv1 "server/contracts/agent/v1"
	"server/service/agentturn"
	"server/utils/testutil"
)

type fakeExecutor struct {
	run func(ctx context.Context, session agentturn.ExecutionSession) (agentv1.TurnStatus, error)
}

func (executor fakeExecutor) Execute(ctx context.Context, session agentturn.ExecutionSession) (agentv1.TurnStatus, error) {
	return executor.run(ctx, session)
}

type fakeDeliverer struct {
	mu      sync.Mutex
	seen    []agentturn.EffectDelivery
	respond func(agentturn.EffectDelivery) (agentturn.DeliveryReport, error)
}

func (deliverer *fakeDeliverer) Deliver(_ context.Context, delivery agentturn.EffectDelivery) (agentturn.DeliveryReport, error) {
	deliverer.mu.Lock()
	deliverer.seen = append(deliverer.seen, delivery)
	deliverer.mu.Unlock()
	if deliverer.respond != nil {
		return deliverer.respond(delivery)
	}
	return agentturn.DeliveryReport{Outcome: agentturn.DeliveryOutcomeDelivered}, nil
}

func (deliverer *fakeDeliverer) delivered() []agentturn.EffectDelivery {
	deliverer.mu.Lock()
	defer deliverer.mu.Unlock()
	return append([]agentturn.EffectDelivery(nil), deliverer.seen...)
}

type fakeSettlement struct {
	mu      sync.Mutex
	calls   []agentturn.SettlementCommand
	reviews []agentturn.SettlementReviewHoldCommand
}

var _ agentturn.SettlementReviewUsageAuthority = (*fakeSettlement)(nil)
var _ agentturn.SettlementReviewProviderUsageAuthority = (*fakeSettlement)(nil)

type fakeJournalBackedSettlement struct {
	agentturn.SettlementReviewProviderUsageAuthority
	providerUsage *workerProviderUsageBinding
}

var _ workerJournalBackedSettlementAuthority = (*fakeJournalBackedSettlement)(nil)

func (settlement *fakeJournalBackedSettlement) matchesWorkerProviderUsageBinding(
	binding *workerProviderUsageBinding,
) bool {
	return settlement != nil && settlement.providerUsage != nil &&
		settlement.providerUsage == binding
}

func providerUsageAndSettlementForTest(
	t *testing.T,
	database *gorm.DB,
	store *agentturn.SQLStore,
	plugins []workerPluginRequirement,
	settlement agentturn.SettlementReviewProviderUsageAuthority,
) (*workerProviderUsageBinding, workerJournalBackedSettlementAuthority) {
	t.Helper()
	binding := newRealProviderUsageBindingForTest(t, database, store, plugins)
	return binding, &fakeJournalBackedSettlement{
		SettlementReviewProviderUsageAuthority: settlement,
		providerUsage:                          binding,
	}
}

type healthyRuntimeProbe struct{}

func (healthyRuntimeProbe) Startup(context.Context) error { return nil }
func (healthyRuntimeProbe) Check(context.Context) error   { return nil }

type runtimeProbeFuncs struct {
	startup func(context.Context) error
	check   func(context.Context) error
}

func (probe runtimeProbeFuncs) Startup(ctx context.Context) error {
	if probe.startup == nil {
		return nil
	}
	return probe.startup(ctx)
}

func (probe runtimeProbeFuncs) Check(ctx context.Context) error {
	if probe.check == nil {
		return nil
	}
	return probe.check(ctx)
}

func (settlement *fakeSettlement) Settle(tx *gorm.DB, command agentturn.SettlementCommand) error {
	settlement.mu.Lock()
	defer settlement.mu.Unlock()
	if tx == nil {
		return errors.New("settlement ran outside the caller transaction")
	}
	settlement.calls = append(settlement.calls, command)
	return nil
}

func (settlement *fakeSettlement) HoldForReview(tx *gorm.DB, command agentturn.SettlementReviewHoldCommand) error {
	settlement.mu.Lock()
	defer settlement.mu.Unlock()
	if tx == nil {
		return errors.New("review hold ran outside the caller transaction")
	}
	if err := command.Validate(); err != nil {
		return err
	}
	settlement.reviews = append(settlement.reviews, command)
	return nil
}

func (settlement *fakeSettlement) MeasureReview(
	tx *gorm.DB,
	command agentturn.MeasureSettlementReviewUsageCommand,
) (agentturn.SettlementReviewUsageAuthorityReceipt, error) {
	if tx == nil {
		return agentturn.SettlementReviewUsageAuthorityReceipt{}, errors.New("usage measurement ran outside the caller transaction")
	}
	if err := command.Validate(); err != nil {
		return agentturn.SettlementReviewUsageAuthorityReceipt{}, err
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	receipt := agentturn.SettlementReviewUsageAuthorityReceipt{
		EvidenceID: command.EvidenceID, ReviewID: command.Review.ReviewID,
		TurnID: command.Review.TurnID, SettlementKey: command.Review.SettlementKey,
		ReviewRequestDigest: command.Review.RequestDigest, Plugin: command.Plugin,
		BillingPolicyKey: "test_policy", PricingSnapshotDigest: digest,
		MeterKey: "test_meter", MeterVersion: "1", MeterBuildDigest: digest,
		UsageSourceDigest: digest, MeasurementDigest: digest, UsedUnits: 1,
		MeterReceiptDigest: digest,
	}
	if err := receipt.Validate(command); err != nil {
		return agentturn.SettlementReviewUsageAuthorityReceipt{}, err
	}
	return receipt, nil
}

func (settlement *fakeSettlement) MeasureProviderUsage(
	tx *gorm.DB,
	command agentturn.MeasureSettlementReviewProviderUsageCommand,
) (agentturn.SettlementReviewProviderUsageAuthorityReceipt, error) {
	if tx == nil {
		return agentturn.SettlementReviewProviderUsageAuthorityReceipt{},
			errors.New("provider usage measurement ran outside the caller transaction")
	}
	if err := command.Validate(); err != nil {
		return agentturn.SettlementReviewProviderUsageAuthorityReceipt{}, err
	}
	receipt := agentturn.SettlementReviewProviderUsageAuthorityReceipt{
		MeasurementDigest: "sha256:" + strings.Repeat("a", 64), UsedUnits: 1,
	}
	if err := receipt.Validate(command); err != nil {
		return agentturn.SettlementReviewProviderUsageAuthorityReceipt{}, err
	}
	return receipt, nil
}

func (settlement *fakeSettlement) ResolveReview(
	tx *gorm.DB,
	command agentturn.SettlementReviewResolutionAuthorityCommand,
) (agentturn.SettlementReviewResolutionAuthorityReceipt, error) {
	if tx == nil {
		return agentturn.SettlementReviewResolutionAuthorityReceipt{}, errors.New("review resolution ran outside the caller transaction")
	}
	if err := command.Validate(); err != nil {
		return agentturn.SettlementReviewResolutionAuthorityReceipt{}, err
	}
	receipt := agentturn.SettlementReviewResolutionAuthorityReceipt{
		ResolutionID: command.ResolutionID, DecisionDigest: command.DecisionDigest,
		EvidenceID: command.Evidence.EvidenceID, EvidenceDigest: command.Evidence.EvidenceDigest,
		PricingSnapshotDigest: command.Evidence.PricingSnapshotDigest,
		UsedUnits:             command.UsedUnits, ReservedUnits: command.UsedUnits,
		ReceiptDigest: "sha256:" + strings.Repeat("b", 64),
	}
	if err := receipt.Validate(command); err != nil {
		return agentturn.SettlementReviewResolutionAuthorityReceipt{}, err
	}
	return receipt, nil
}

func (settlement *fakeSettlement) settled() []agentturn.SettlementCommand {
	settlement.mu.Lock()
	defer settlement.mu.Unlock()
	return append([]agentturn.SettlementCommand(nil), settlement.calls...)
}

func (settlement *fakeSettlement) reviewHolds() []agentturn.SettlementReviewHoldCommand {
	settlement.mu.Lock()
	defer settlement.mu.Unlock()
	return append([]agentturn.SettlementReviewHoldCommand(nil), settlement.reviews...)
}

const testPluginID = "workmax.writer"

func workerOnRollout() config.AgentPlatformRollout {
	return config.EffectiveAgentPlatformRollout(&config.AgentPlatformRollout{
		Durable: config.DurableTurnRollout{Worker: config.DurableWorkerOn},
		Readiness: config.AgentPlatformReadiness{
			SQLStore:              true,
			WorkerLeaseFencing:    true,
			TransactionalOutbox:   true,
			ExactlyOnceSettlement: true,
		},
	})
}

func testTurn(suffix string) agentturn.Turn {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return agentturn.Turn{
		ID:             agentv1.TurnID("turn_" + suffix),
		PrincipalID:    agentturn.PrincipalID("principal_" + suffix),
		ThreadID:       agentv1.ThreadID("thread_" + suffix),
		IdempotencyKey: agentv1.IdempotencyKey("idem_" + suffix),
		CommandDigest:  "sha256:command-" + suffix,
		Plugin: agentv1.EventPluginRef{
			ID: testPluginID, Version: "1.0.0", ReleaseDigest: testWriterPluginDigest,
		},
		Status: agentv1.TurnStatusQueued, CreatedAt: now, UpdatedAt: now,
	}
}

func admit(t *testing.T, store *agentturn.SQLStore, turn agentturn.Turn) {
	t.Helper()
	if _, err := store.Admit(context.Background(), turn, agentturn.EventDraft{
		Type: agentv1.EventCoreTurnStatus, Data: json.RawMessage(`{"status":"queued"}`),
	}); err != nil {
		t.Fatalf("Admit(%s): %v", turn.ID, err)
	}
}

func composeForTest(t *testing.T, executor agentturn.TurnExecutor, deliverer agentturn.Deliverer, settlement agentturn.SettlementReviewProviderUsageAuthority) (*gorm.DB, *WorkerComposition) {
	return composeForTestWithRollout(t, workerOnRollout(), executor, deliverer, settlement)
}

func composeForTestWithRollout(t *testing.T, rollout config.AgentPlatformRollout, executor agentturn.TurnExecutor, deliverer agentturn.Deliverer, settlement agentturn.SettlementReviewProviderUsageAuthority) (*gorm.DB, *WorkerComposition) {
	return composeForTestWithProbe(t, rollout, executor, deliverer, settlement, healthyRuntimeProbe{})
}

func composeForTestWithProbe(t *testing.T, rollout config.AgentPlatformRollout, executor agentturn.TurnExecutor, deliverer agentturn.Deliverer, settlement agentturn.SettlementReviewProviderUsageAuthority, probe RuntimeProbe) (*gorm.DB, *WorkerComposition) {
	return composeForTestWithProbeAndResources(
		t, rollout, executor, deliverer, settlement, probe,
	)
}

func composeForTestWithProbeAndResources(
	t *testing.T,
	rollout config.AgentPlatformRollout,
	executor agentturn.TurnExecutor,
	deliverer agentturn.Deliverer,
	settlement agentturn.SettlementReviewProviderUsageAuthority,
	probe RuntimeProbe,
	ownedResources ...WorkerResourceCloser,
) (*gorm.DB, *WorkerComposition) {
	t.Helper()
	db, candidate, guard := composeExactCandidateWithGuardForTest(
		t, context.Background(), rollout, executor, deliverer, settlement, probe, ownedResources...,
	)
	composition, err := guard.commit(candidate)
	if err != nil {
		t.Fatalf("acquisition Guard commit(): %v", err)
	}
	return db, composition
}

// composeExactCandidateWithGuardForTest mirrors the production ownership
// protocol without opening an external database: a root dependency registers
// a test closer, the SQL store explicitly borrows that lifetime, and the exact
// composition receives only the Guard's sealed owner. The caller chooses
// whether to commit or exercise a rejected pre-commit candidate.
func composeExactCandidateWithGuardForTest(
	t *testing.T,
	guardCtx context.Context,
	rollout config.AgentPlatformRollout,
	executor agentturn.TurnExecutor,
	deliverer agentturn.Deliverer,
	settlement agentturn.SettlementReviewProviderUsageAuthority,
	probe RuntimeProbe,
	ownedResources ...WorkerResourceCloser,
) (*gorm.DB, *WorkerComposition, *workerAcquisitionGuard) {
	t.Helper()
	guard, err := newWorkerAcquisitionGuard(guardCtx, time.Second)
	if err != nil {
		t.Fatalf("newWorkerAcquisitionGuard(): %v", err)
	}
	t.Cleanup(guard.abort)

	db := testutil.NewTestDB(t)
	if len(ownedResources) == 0 {
		ownedResources = []WorkerResourceCloser{WorkerResourceCloseFunc(func(context.Context) error {
			return nil
		})}
	} else {
		ownedResources = append([]WorkerResourceCloser(nil), ownedResources...)
	}
	database, err := acquireWorkerDependency(
		guard,
		workerOwnsResource(),
		func(registrar workerResourceRegistrar) (*gorm.DB, error) {
			for _, resource := range ownedResources {
				if ownErr := registrar.Own(resource); ownErr != nil {
					return nil, ownErr
				}
			}
			return db, nil
		},
		func(value *gorm.DB) bool { return value == db },
	)
	if err != nil {
		t.Fatalf("acquire test database: %v", err)
	}
	storeDependency, err := acquireWorkerDependency(
		guard,
		workerBorrowsFrom(database.ownership),
		func(workerResourceRegistrar) (*agentturn.SQLStore, error) {
			return agentturn.NewSQLStore(database.value)
		},
		func(value *agentturn.SQLStore) bool { return value != nil },
	)
	if err != nil {
		t.Fatalf("acquire test SQL store: %v", err)
	}
	store := storeDependency.value
	snapshot := agentv1.EventPluginRef{
		ID: testPluginID, Version: "1.0.0", ReleaseDigest: testWriterPluginDigest,
	}
	plugins, topics, ok := normalizeWorkerPluginRequirements([]workerPluginRequirement{{
		Snapshot: snapshot, EffectTopics: []string{"writer.document.index"},
		ExecutionTimeout: 20 * time.Minute, ProgressTimeout: time.Minute,
		Promotion: promotionForTest(snapshot),
	}})
	if !ok {
		t.Fatal("exact test Plugin requirement is invalid")
	}
	claim, err := newWorkerExactClaimStore(store, workerRequirementSnapshots(plugins))
	if err != nil {
		t.Fatalf("newWorkerExactClaimStore(): %v", err)
	}
	executors, err := newWorkerExactExecutorRegistry([]workerPluginExecutorBinding{{
		Snapshot: snapshot, EffectTopics: append([]string(nil), topics...), Executor: executor,
	}})
	if err != nil {
		t.Fatalf("newWorkerExactExecutorRegistry(): %v", err)
	}
	effects, err := newWorkerExactEffectRouter([]workerEffectBinding{{
		Topic: topics[0], Deliverer: deliverer,
	}})
	if err != nil {
		t.Fatalf("newWorkerExactEffectRouter(): %v", err)
	}
	providerUsage, journalBackedSettlement := providerUsageAndSettlementForTest(
		t, db, store, plugins, settlement,
	)
	probeChildren := make([]RuntimeProbe, workerCoreProbeCount+len(plugins)+len(topics))
	for index := range probeChildren {
		probeChildren[index] = probe
	}
	compositeProbe, err := newWorkerCompositeRuntimeProbe(probeChildren)
	if err != nil {
		t.Fatalf("newWorkerCompositeRuntimeProbe(): %v", err)
	}
	resources, err := guard.seal()
	if err != nil {
		t.Fatalf("acquisition Guard seal(): %v", err)
	}
	composition, err := composeExactWorker(workerExactComposeOptions{
		Rollout: rollout, Identity: ProcessIdentity{
			WorkerID: "worker_e2e", BuildDigest: testArtifactDigest,
		},
		Store: store, Claim: claim, Executors: executors, Effects: effects,
		Plugins: plugins, ProviderUsage: providerUsage, Settlement: journalBackedSettlement,
		RuntimeProbe: compositeProbe, Resources: resources,
	})
	if err != nil {
		t.Fatalf("composeExactWorker(): %v", err)
	}
	return db, composition, guard
}

// TestEndToEndTurnLifecycle is the first evidence that the candidates actually
// compose. Every prior test exercised one boundary against a hand-built
// fixture; this one drives a Turn through the real composition.
func TestEndToEndTurnLifecycle(t *testing.T) {
	deliverer := &fakeDeliverer{}
	settlement := &fakeSettlement{}
	executor := fakeExecutor{
		run: func(_ context.Context, session agentturn.ExecutionSession) (agentv1.TurnStatus, error) {
			// One durable mid-Turn Operation carrying an external effect.
			if _, err := session.Emit(context.Background(), agentturn.OperationDraft{
				OperationID: "operation_progress",
				Event: agentturn.EventDraft{
					Type: "writer.document.delta",
					Data: json.RawMessage(`{"patch":"hello"}`),
				},
				Effects: []agentturn.EffectOutboxDraft{{
					OutboxID:  "outbox_e2e_0",
					Topic:     "writer.document.index",
					DedupeKey: "dedupe_e2e_0",
					Payload:   json.RawMessage(`{"indexed":true}`),
					// Keep the E2E independent of database/process clock skew at
					// the exact availability boundary.
					AvailableAt: time.Now().UTC().Add(-time.Minute),
				}},
			}); err != nil {
				return "", err
			}
			return agentv1.TurnStatusCompleted, nil
		},
	}

	db, composition := composeForTest(t, executor, deliverer, settlement)
	turn := testTurn("e2e")
	admit(t, composition.store, turn)

	// 1. Discovery, execution and terminal commit.
	result, err := composition.worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce(): %v", err)
	}
	if result.TurnID != turn.ID || !result.Committed || result.TerminalStatus != agentv1.TurnStatusCompleted {
		t.Fatalf("worker result = %+v", result)
	}

	// 2. A sealed exact composition never treats completed Finalize(0) as
	//    trusted usage. The terminal transaction opens one completion Review
	//    and invokes the Authority hold without settling.
	settled := settlement.settled()
	if len(settled) != 0 {
		t.Fatalf("settlement = %+v, want no direct settlement for %q", settled, turn.ID)
	}
	review := result.Commit.SettlementReview
	if review == nil || review.Validate() != nil || review.TurnID != turn.ID ||
		review.Source != agentturn.SettlementReviewSourceExecutorCompletion ||
		review.Reason != agentturn.SettlementReviewReasonCompletedUsageUnmeasured ||
		review.TerminalStatus != agentv1.TurnStatusCompleted ||
		review.Status != agentturn.SettlementReviewStatusPending {
		t.Fatalf("completion Review = %+v", review)
	}
	holds := settlement.reviewHolds()
	if len(holds) != 1 || holds[0].PrincipalID != turn.PrincipalID ||
		holds[0].Review.ReviewID != review.ReviewID ||
		holds[0].Review.RequestDigest != review.RequestDigest {
		t.Fatalf("review holds = %+v, want exact completion Review", holds)
	}
	reviews, err := composition.store.ListSettlementReviews(
		context.Background(), agentturn.ListSettlementReviewsQuery{},
	)
	if err != nil {
		t.Fatalf("ListSettlementReviews(): %v", err)
	}
	if len(reviews) != 1 || reviews[0].ReviewID != review.ReviewID ||
		reviews[0].RequestDigest != review.RequestDigest {
		t.Fatalf("durable Reviews = %+v, want completion Review %q", reviews, review.ReviewID)
	}

	// 3. The earlier Operation's Effect remains durably review-held. It cannot
	//    be claimed or delivered before evidence-backed adjudication.
	if _, err := composition.dispatcher.DispatchOnce(context.Background()); !errors.Is(err, agentturn.ErrNoClaimableEffects) {
		t.Fatalf("DispatchOnce() error = %v, want ErrNoClaimableEffects", err)
	}
	if deliveries := deliverer.delivered(); len(deliveries) != 0 {
		t.Fatalf("review-held Effect reached deliverer: %+v", deliveries)
	}
	var effect struct {
		TurnID    string `gorm:"column:turn_id"`
		DedupeKey string `gorm:"column:dedupe_key"`
		Status    string `gorm:"column:status"`
	}
	if err := db.Table(agentturn.SQLEffectOutboxTable).Where("outbox_id = ?", "outbox_e2e_0").
		Take(&effect).Error; err != nil {
		t.Fatal(err)
	}
	if effect.TurnID != string(turn.ID) || effect.DedupeKey != "dedupe_e2e_0" ||
		effect.Status != string(agentturn.EffectStatusReviewHold) {
		t.Fatalf("durable Effect = %+v, want review_hold for %q", effect, turn.ID)
	}

	// 4. Nothing is left claimable or deliverable.
	if _, err := composition.worker.RunOnce(context.Background()); !errors.Is(err, agentturn.ErrNoClaimableTurn) {
		t.Fatalf("second RunOnce() error = %v, want ErrNoClaimableTurn", err)
	}
	if _, err := composition.dispatcher.DispatchOnce(context.Background()); !errors.Is(err, agentturn.ErrNoClaimableEffects) {
		t.Fatalf("second DispatchOnce() error = %v, want ErrNoClaimableEffects", err)
	}

	var status string
	if err := db.Table("w_agent_turn").Where("turn_id = ?", turn.ID).
		Select("status").Scan(&status).Error; err != nil {
		t.Fatal(err)
	}
	if status != string(agentv1.TurnStatusCompleted) {
		t.Fatalf("persisted turn status = %q", status)
	}
}

// TestEndToEndReconcilerRetiresAnObserverlessCancellation proves the second
// half of the lifecycle: work that no executor will ever finish still reaches
// a terminal state, but its reservation remains held for Provider usage.
func TestEndToEndReconcilerRetiresAnObserverlessCancellation(t *testing.T) {
	settlement := &fakeSettlement{}
	executor := fakeExecutor{
		run: func(context.Context, agentturn.ExecutionSession) (agentv1.TurnStatus, error) {
			t.Error("a cancelled turn must never be handed to an executor")
			return agentv1.TurnStatusCompleted, nil
		},
	}
	_, composition := composeForTest(t, executor, &fakeDeliverer{}, settlement)

	turn := testTurn("cancelled")
	admit(t, composition.store, turn)
	if _, err := composition.store.RequestCancel(context.Background(),
		turn.PrincipalID, turn.ThreadID, turn.ID, time.Now().UTC(),
		agentturn.EventDraft{Type: agentv1.EventCoreTurnStatus, Data: json.RawMessage(`{"cancellationRequested":true}`)},
	); err != nil {
		t.Fatal(err)
	}

	// The worker must refuse it: a cancelled Turn is not new work.
	if _, err := composition.worker.RunOnce(context.Background()); !errors.Is(err, agentturn.ErrNoClaimableTurn) {
		t.Fatalf("worker claimed a cancelled turn: %v", err)
	}
	// Only the reconciler can retire it.
	report, err := composition.reconciler.ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("ReconcileOnce(): %v", err)
	}
	if len(report.Retired) != 1 || report.Retired[0].TerminalStatus != agentv1.TurnStatusStopped {
		t.Fatalf("reconcile report = %+v, want one stopped turn", report)
	}
	if settled := settlement.settled(); len(settled) != 0 {
		t.Fatalf("settlement = %+v, want no direct release", settled)
	}
	holds := settlement.reviewHolds()
	if len(holds) != 1 ||
		holds[0].Review.Source != agentturn.SettlementReviewSourceReconcileTerminal ||
		holds[0].Review.Reason != agentturn.SettlementReviewReasonTerminalUsageUnmeasured ||
		holds[0].Review.TerminalStatus != agentv1.TurnStatusStopped ||
		holds[0].Review.Status != agentturn.SettlementReviewStatusPending ||
		holds[0].Review.Evidence.PriorProviderUsageCount != 0 {
		t.Fatalf("review holds = %+v, want one pending provider-usage hold", holds)
	}
}

func TestComposeRefusesARolloutItCannotServe(t *testing.T) {
	executor := fakeExecutor{run: func(context.Context, agentturn.ExecutionSession) (agentv1.TurnStatus, error) {
		return agentv1.TurnStatusCompleted, nil
	}}
	var typedNilExecutor *fakeExecutor
	var typedNilDeliverer *fakeDeliverer
	var typedNilSettlement *fakeSettlement
	var typedNilProbe *runtimeProbeFuncs
	for name, tc := range map[string]struct {
		mutate   func(*ComposeOptions)
		fragment string
	}{
		"no settlement authority": {
			mutate:   func(o *ComposeOptions) { o.Settlement = nil },
			fragment: "settlement authority",
		},
		"no plugin executor": {
			mutate:   func(o *ComposeOptions) { o.Executors = ExecutorRegistry{} },
			fragment: "no plugin executor is registered",
		},
		"nil plugin executor": {
			mutate: func(o *ComposeOptions) {
				o.Executors = ExecutorRegistry{testPluginID: nil}
			},
			fragment: "executor",
		},
		"typed nil plugin executor": {
			mutate: func(o *ComposeOptions) {
				o.Executors = ExecutorRegistry{testPluginID: typedNilExecutor}
			},
			fragment: "executor",
		},
		"empty plugin id": {
			mutate: func(o *ComposeOptions) {
				o.Executors = ExecutorRegistry{" ": executor}
			},
			fragment: "empty plugin id",
		},
		"plugin id with surrounding whitespace": {
			mutate: func(o *ComposeOptions) {
				o.Executors = ExecutorRegistry{" workmax.writer ": executor}
			},
			fragment: "surrounding whitespace",
		},
		"no effect deliverer": {
			mutate:   func(o *ComposeOptions) { o.Deliverer = nil },
			fragment: "no effect deliverer",
		},
		"typed nil effect deliverer": {
			mutate:   func(o *ComposeOptions) { o.Deliverer = typedNilDeliverer },
			fragment: "effect deliverer",
		},
		"no database": {
			mutate:   func(o *ComposeOptions) { o.DB = nil },
			fragment: "requires a database",
		},
		"no identity": {
			mutate:   func(o *ComposeOptions) { o.Identity = ProcessIdentity{} },
			fragment: "worker id and build digest",
		},
		"no runtime probe": {
			mutate:   func(o *ComposeOptions) { o.RuntimeProbe = nil },
			fragment: "runtime probe",
		},
		"typed nil runtime probe": {
			mutate:   func(o *ComposeOptions) { o.RuntimeProbe = typedNilProbe },
			fragment: "runtime probe",
		},
		"typed nil settlement authority": {
			mutate:   func(o *ComposeOptions) { o.Settlement = typedNilSettlement },
			fragment: "settlement authority",
		},
	} {
		options := ComposeOptions{
			DB:           testutil.NewTestDB(t),
			Rollout:      workerOnRollout(),
			Identity:     ProcessIdentity{WorkerID: "w", BuildDigest: "sha256:w"},
			Executors:    ExecutorRegistry{testPluginID: executor},
			Deliverer:    &fakeDeliverer{},
			Settlement:   &fakeSettlement{},
			RuntimeProbe: healthyRuntimeProbe{},
		}
		tc.mutate(&options)
		_, err := Compose(options)
		if err == nil {
			t.Fatalf("%s: Compose() succeeded", name)
		}
		if !strings.Contains(err.Error(), tc.fragment) {
			t.Fatalf("%s: error = %v, want it to mention %q", name, err, tc.fragment)
		}
	}
}

func TestComposeCopiesExecutorRegistryAndBindsSealToExactComponents(t *testing.T) {
	db := testutil.NewTestDB(t)
	originalCalls := 0
	replacementCalls := 0
	registry := ExecutorRegistry{testPluginID: fakeExecutor{run: func(context.Context, agentturn.ExecutionSession) (agentv1.TurnStatus, error) {
		originalCalls++
		return agentv1.TurnStatusCompleted, nil
	}}}
	composition, err := Compose(ComposeOptions{
		DB: db, Rollout: workerOnRollout(),
		Identity:     ProcessIdentity{WorkerID: "worker_seal", BuildDigest: "sha256:worker-seal"},
		Executors:    registry,
		Deliverer:    &fakeDeliverer{},
		Settlement:   &fakeSettlement{},
		RuntimeProbe: healthyRuntimeProbe{},
	})
	if err != nil {
		t.Fatalf("Compose(): %v", err)
	}
	registry[testPluginID] = fakeExecutor{run: func(context.Context, agentturn.ExecutionSession) (agentv1.TurnStatus, error) {
		replacementCalls++
		return agentv1.TurnStatusFailed, nil
	}}

	turn := testTurn("registry-copy")
	admit(t, composition.store, turn)
	result, err := composition.worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce(): %v", err)
	}
	if originalCalls != 1 || replacementCalls != 0 || result.TerminalStatus != agentv1.TurnStatusCompleted {
		t.Fatalf("executor calls original=%d replacement=%d result=%+v", originalCalls, replacementCalls, result)
	}

	if !workerCompositionReady(composition) {
		t.Fatal("fresh composition did not pass its seal")
	}
	originalWorker := composition.worker
	composition.worker = &agentturn.Worker{}
	if workerCompositionReady(composition) {
		t.Fatal("composition remained ready after its Worker was replaced")
	}
	composition.worker = originalWorker
	originalProbe := composition.probe
	composition.probe = &sealedRuntimeProbe{delegate: healthyRuntimeProbe{}}
	if workerCompositionReady(composition) {
		t.Fatal("composition remained ready after its runtime probe was replaced")
	}
	composition.probe = originalProbe
	originalResources := composition.resources
	replacementResources, err := newWorkerResourceStack(nil)
	if err != nil {
		t.Fatalf("newWorkerResourceStack(): %v", err)
	}
	composition.resources = replacementResources
	if workerCompositionReady(composition) {
		t.Fatal("composition remained ready after its resource owner was replaced")
	}
	composition.resources = originalResources
	if err := replacementResources.Close(context.Background()); err != nil {
		t.Fatalf("close replacement resource owner: %v", err)
	}
	if !workerCompositionReady(composition) {
		t.Fatal("restoring exact sealed components did not restore static readiness")
	}
}

func TestComposeCopiesAndOwnsResources(t *testing.T) {
	closed := make(chan struct{})
	replacementCalled := make(chan struct{}, 1)
	resource := WorkerResourceCloseFunc(func(context.Context) error {
		close(closed)
		return nil
	})
	resourceInput := []WorkerResourceCloser{resource}
	composition, err := Compose(ComposeOptions{
		DB: testutil.NewTestDB(t), Rollout: workerOnRollout(),
		Identity: ProcessIdentity{WorkerID: "worker_resources", BuildDigest: "sha256:worker-resources"},
		Executors: ExecutorRegistry{testPluginID: fakeExecutor{run: func(context.Context, agentturn.ExecutionSession) (agentv1.TurnStatus, error) {
			return agentv1.TurnStatusCompleted, nil
		}}},
		Deliverer:    &fakeDeliverer{},
		Settlement:   &fakeSettlement{},
		RuntimeProbe: healthyRuntimeProbe{},
		Resources:    resourceInput,
	})
	if err != nil {
		t.Fatalf("Compose(): %v", err)
	}
	resourceInput[0] = WorkerResourceCloseFunc(func(context.Context) error {
		replacementCalled <- struct{}{}
		return nil
	})

	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := composition.Close(closeCtx); err != nil {
		t.Fatalf("composition.Close(): %v", err)
	}
	select {
	case <-closed:
	default:
		t.Fatal("Composition did not close its copied resource")
	}
	select {
	case <-replacementCalled:
		t.Fatal("Composition retained the caller's mutable resource slice")
	default:
	}
}

func TestComposeFailureClosesTransferredResources(t *testing.T) {
	closed := make(chan struct{})
	_, err := Compose(ComposeOptions{
		Rollout: workerOnRollout(),
		Resources: []WorkerResourceCloser{WorkerResourceCloseFunc(func(context.Context) error {
			close(closed)
			return errors.New("SECRET_RESOURCE_CLOSE_ERROR")
		})},
	})
	if err == nil || !strings.Contains(err.Error(), "database") {
		t.Fatalf("Compose() error = %v, want database validation failure", err)
	}
	if strings.Contains(err.Error(), "SECRET_RESOURCE_CLOSE_ERROR") {
		t.Fatalf("Compose() exposed resource close failure: %q", err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Compose failure did not start resource cleanup")
	}
}

func TestComposeValidatesWorkerReadinessBeforeDependencies(t *testing.T) {
	rollout := workerOnRollout()
	rollout.Readiness.ExactlyOnceSettlement = false
	_, err := Compose(ComposeOptions{Rollout: rollout})
	if err == nil || !strings.Contains(err.Error(), "exactly-once settlement") {
		t.Fatalf("Compose() error = %v, want Worker readiness rejection", err)
	}
	if strings.Contains(err.Error(), "database") {
		t.Fatalf("Compose() reached dependency validation before rollout validation: %v", err)
	}
}

func TestUnregisteredPluginIsNotClaimedOrGuessed(t *testing.T) {
	settlement := &fakeSettlement{}
	executor := fakeExecutor{run: func(context.Context, agentturn.ExecutionSession) (agentv1.TurnStatus, error) {
		t.Error("an out-of-scope Turn must not reach the registered executor")
		return agentv1.TurnStatusCompleted, nil
	}}
	db, composition := composeForTest(t, executor, &fakeDeliverer{}, settlement)

	turn := testTurn("foreign")
	turn.Plugin.ID = "workmax.media"
	admit(t, composition.store, turn)

	if _, err := composition.worker.RunOnce(context.Background()); !errors.Is(err, agentturn.ErrNoClaimableTurn) {
		t.Fatalf("RunOnce() error = %v, want ErrNoClaimableTurn", err)
	}
	var attemptCount int64
	if err := db.Table(agentturn.SQLTurnAttemptTable).Where("turn_id = ?", turn.ID).
		Count(&attemptCount).Error; err != nil {
		t.Fatal(err)
	}
	if attemptCount != 0 {
		t.Fatalf("out-of-scope Turn persisted %d Attempts, want 0", attemptCount)
	}
	if settled := settlement.settled(); len(settled) != 0 {
		t.Fatalf("out-of-scope Turn reached settlement: %+v", settled)
	}
}

func TestProductionServeRejectsLegacyCompose(t *testing.T) {
	composition, err := Compose(ComposeOptions{
		DB: testutil.NewTestDB(t), Rollout: workerOnRollout(),
		Identity: ProcessIdentity{WorkerID: "legacy_candidate", BuildDigest: testArtifactDigest},
		Executors: ExecutorRegistry{testPluginID: fakeExecutor{run: func(
			context.Context, agentturn.ExecutionSession,
		) (agentv1.TurnStatus, error) {
			return agentv1.TurnStatusCompleted, nil
		}}},
		Deliverer: &fakeDeliverer{}, Settlement: settleOnlyAuthority{}, RuntimeProbe: healthyRuntimeProbe{},
	})
	if err != nil {
		t.Fatalf("legacy Compose(): %v", err)
	}
	if !workerCompositionReady(composition) {
		t.Fatal("legacy candidate composition lost structural readiness")
	}
	if workerProductionCompositionReady(composition) {
		t.Fatal("legacy candidate composition passed the production runtime-scope gate")
	}
	if err := Serve(context.Background(), composition); !errors.Is(err, errWorkerDependenciesUnavailable) {
		t.Fatalf("Serve(legacy) error = %v, want dependency rejection", err)
	}
}

func TestProductionServeRejectsUncommittedExactCandidate(t *testing.T) {
	_, candidate, _ := composeExactCandidateWithGuardForTest(
		t,
		context.Background(),
		workerOnRollout(),
		fakeExecutor{run: func(context.Context, agentturn.ExecutionSession) (agentv1.TurnStatus, error) {
			return agentv1.TurnStatusCompleted, nil
		}},
		&fakeDeliverer{},
		&fakeSettlement{},
		healthyRuntimeProbe{},
	)
	if !workerCompositionReady(candidate) || candidate.runtimeScope == nil {
		t.Fatal("direct exact Compose lost structural candidate readiness")
	}
	if candidate.ownershipTransfer != nil || candidate.seal.ownershipTransfer != nil {
		t.Fatal("direct exact Compose minted an ownership-transfer seal")
	}
	if workerProductionCompositionReady(candidate) {
		t.Fatal("uncommitted exact candidate passed production readiness")
	}
	if err := Serve(context.Background(), candidate); !errors.Is(err, errWorkerDependenciesUnavailable) {
		t.Fatalf("Serve(uncommitted exact candidate) error = %v, want dependency rejection", err)
	}
}

func TestAcquisitionCommitPromotesExactCandidateForProduction(t *testing.T) {
	_, candidate, guard := composeExactCandidateWithGuardForTest(
		t,
		context.Background(),
		workerOnRollout(),
		fakeExecutor{run: func(context.Context, agentturn.ExecutionSession) (agentv1.TurnStatus, error) {
			return agentv1.TurnStatusCompleted, nil
		}},
		&fakeDeliverer{},
		&fakeSettlement{},
		healthyRuntimeProbe{},
	)
	composition, err := guard.commit(candidate)
	if err != nil {
		t.Fatalf("commit(exact candidate): %v", err)
	}
	if composition != candidate || composition.ownershipTransfer == nil ||
		composition.seal.ownershipTransfer != composition.ownershipTransfer ||
		!composition.ownershipTransfer.intact(composition) {
		t.Fatal("commit did not bind one intact transfer seal to the candidate and structural seal")
	}
	if !workerProductionCompositionReady(composition) {
		t.Fatal("committed exact composition did not pass production readiness")
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := composition.Close(closeCtx); err != nil {
		t.Fatalf("close committed exact composition: %v", err)
	}
}

func TestAcquisitionCanceledOrFailedCommitNeverMintsTransferSeal(t *testing.T) {
	newCandidate := func(t *testing.T, ctx context.Context) (*WorkerComposition, *workerAcquisitionGuard) {
		t.Helper()
		_, candidate, guard := composeExactCandidateWithGuardForTest(
			t,
			ctx,
			workerOnRollout(),
			fakeExecutor{run: func(context.Context, agentturn.ExecutionSession) (agentv1.TurnStatus, error) {
				return agentv1.TurnStatusCompleted, nil
			}},
			&fakeDeliverer{},
			&fakeSettlement{},
			healthyRuntimeProbe{},
		)
		return candidate, guard
	}

	t.Run("canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		candidate, guard := newCandidate(t, ctx)
		cancel()
		if composition, err := guard.commit(candidate); composition != nil ||
			!errors.Is(err, errWorkerAcquisitionTransfer) {
			t.Fatalf("commit after cancellation = %p, %v; want transfer rejection", composition, err)
		}
		if candidate.ownershipTransfer != nil || candidate.seal.ownershipTransfer != nil ||
			workerProductionCompositionReady(candidate) {
			t.Fatal("canceled commit left a production transfer seal")
		}
	})

	t.Run("cancellation between first check and callback stop", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		candidate, guard := newCandidate(t, ctx)

		guard.mu.Lock()
		originalStop := guard.stopAbort
		guard.mu.Unlock()
		if originalStop == nil || !originalStop() {
			t.Fatal("could not stop the original cancellation callback")
		}
		guard.mu.Lock()
		guard.stopAbort = func() bool {
			cancel()
			return true
		}
		guard.mu.Unlock()

		if composition, err := guard.commit(candidate); composition != nil ||
			!errors.Is(err, errWorkerAcquisitionTransfer) {
			t.Fatalf("commit across cancellation race = %p, %v; want transfer rejection", composition, err)
		}
		if candidate.ownershipTransfer != nil || candidate.seal.ownershipTransfer != nil ||
			workerProductionCompositionReady(candidate) {
			t.Fatal("cancellation race left a production transfer seal")
		}
	})

	t.Run("tampered exact seal", func(t *testing.T) {
		candidate, guard := newCandidate(t, context.Background())
		candidate.runtimeScope = &workerRuntimeScopeSeal{}
		if composition, err := guard.commit(candidate); composition != nil ||
			!errors.Is(err, errWorkerAcquisitionTransfer) {
			t.Fatalf("commit after exact-scope replacement = %p, %v; want transfer rejection", composition, err)
		}
		if candidate.ownershipTransfer != nil || candidate.seal.ownershipTransfer != nil ||
			workerProductionCompositionReady(candidate) {
			t.Fatal("failed exact-scope commit left a production transfer seal")
		}
	})

	t.Run("non-composite runtime probe", func(t *testing.T) {
		candidate, guard := newCandidate(t, context.Background())
		candidate.probe.delegate = healthyRuntimeProbe{}
		if composition, err := guard.commit(candidate); composition != nil ||
			!errors.Is(err, errWorkerAcquisitionTransfer) {
			t.Fatalf("commit with non-composite probe = %p, %v; want transfer rejection", composition, err)
		}
		if candidate.ownershipTransfer != nil || candidate.seal.ownershipTransfer != nil ||
			workerProductionCompositionReady(candidate) {
			t.Fatal("non-composite probe commit left a production transfer seal")
		}
	})

	t.Run("different resource owner", func(t *testing.T) {
		candidate, guard := newCandidate(t, context.Background())
		replacement, err := newWorkerResourceStack(nil)
		if err != nil {
			t.Fatal(err)
		}
		candidate.resources = replacement
		candidate.seal.resources = replacement
		if composition, commitErr := guard.commit(candidate); composition != nil ||
			!errors.Is(commitErr, errWorkerAcquisitionTransfer) {
			t.Fatalf("commit with a different owner = %p, %v; want transfer rejection", composition, commitErr)
		}
		if candidate.ownershipTransfer != nil || candidate.seal.ownershipTransfer != nil ||
			workerProductionCompositionReady(candidate) {
			t.Fatal("wrong-owner commit left a production transfer seal")
		}
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if closeErr := replacement.Close(closeCtx); closeErr != nil {
			t.Fatalf("close replacement owner: %v", closeErr)
		}
	})
}

func TestAcquisitionAbortRevokesAdmissionBeforeResourceClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var published atomic.Pointer[WorkerComposition]
	gateOpenAtClose := make(chan bool, 1)
	closer := WorkerResourceCloseFunc(func(context.Context) error {
		composition := published.Load()
		if composition == nil {
			gateOpenAtClose <- true
			return nil
		}
		gateOpenAtClose <- workerCompositionAdmissionGate(composition).Open()
		return nil
	})
	_, candidate, _ := composeExactCandidateWithGuardForTest(
		t,
		ctx,
		workerOnRollout(),
		fakeExecutor{run: func(context.Context, agentturn.ExecutionSession) (agentv1.TurnStatus, error) {
			return agentv1.TurnStatusCompleted, nil
		}},
		&fakeDeliverer{},
		&fakeSettlement{},
		healthyRuntimeProbe{},
		closer,
	)
	published.Store(candidate)
	gate := workerCompositionAdmissionGate(candidate)
	if gate == nil || !gate.Open() {
		t.Fatal("candidate did not start with an open AdmissionGate")
	}
	cancel()
	select {
	case wasOpen := <-gateOpenAtClose:
		if wasOpen {
			t.Fatal("acquisition abort began resource Close before revoking admission")
		}
	case <-time.After(time.Second):
		t.Fatal("acquisition abort did not close its resource owner")
	}
	if gate.Open() {
		t.Fatal("acquisition abort left candidate AdmissionGate open")
	}
}

func TestAcquisitionSecondCommitCannotRevokeTransferredOwnership(t *testing.T) {
	_, candidate, guard := composeExactCandidateWithGuardForTest(
		t,
		context.Background(),
		workerOnRollout(),
		fakeExecutor{run: func(context.Context, agentturn.ExecutionSession) (agentv1.TurnStatus, error) {
			return agentv1.TurnStatusCompleted, nil
		}},
		&fakeDeliverer{},
		&fakeSettlement{},
		healthyRuntimeProbe{},
	)
	composition, err := guard.commit(candidate)
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}
	if duplicate, duplicateErr := guard.commit(candidate); duplicate != nil ||
		!errors.Is(duplicateErr, errWorkerAcquisitionTransfer) {
		t.Fatalf("second commit = %p, %v; want transfer rejection", duplicate, duplicateErr)
	}
	if !workerProductionCompositionReady(composition) || !composition.resources.isOpen() {
		t.Fatal("second commit revoked an already transferred owner")
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if closeErr := composition.Close(closeCtx); closeErr != nil {
		t.Fatalf("close committed composition: %v", closeErr)
	}
}

func TestAcquisitionTransferSealRejectsCompositeProbeTampering(t *testing.T) {
	_, candidate, guard := composeExactCandidateWithGuardForTest(
		t,
		context.Background(),
		workerOnRollout(),
		fakeExecutor{run: func(context.Context, agentturn.ExecutionSession) (agentv1.TurnStatus, error) {
			return agentv1.TurnStatusCompleted, nil
		}},
		&fakeDeliverer{},
		&fakeSettlement{},
		healthyRuntimeProbe{},
	)
	composition, err := guard.commit(candidate)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if !workerProductionCompositionReady(composition) {
		t.Fatal("committed composition is not production-ready")
	}
	original := composition.probe.delegate
	composition.probe.delegate = healthyRuntimeProbe{}
	if workerProductionCompositionReady(composition) {
		t.Fatal("composition remained production-ready after replacing its composite probe")
	}
	composition.probe.delegate = original
	if !workerProductionCompositionReady(composition) {
		t.Fatal("restoring the exact composite probe did not restore structural readiness")
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if closeErr := composition.Close(closeCtx); closeErr != nil {
		t.Fatalf("close committed composition: %v", closeErr)
	}
}

func TestComposeExactFailureUsesTheSuppliedCloseBudget(t *testing.T) {
	const closeBudget = 25 * time.Millisecond
	deadlineRemaining := make(chan time.Duration, 1)
	resources, err := newWorkerResourceStack([]WorkerResourceCloser{
		WorkerResourceCloseFunc(func(ctx context.Context) error {
			deadline, ok := ctx.Deadline()
			if !ok {
				deadlineRemaining <- 0
				return nil
			}
			deadlineRemaining <- time.Until(deadline)
			<-ctx.Done()
			return ctx.Err()
		}),
	})
	if err != nil {
		t.Fatalf("newWorkerResourceStack(): %v", err)
	}
	composition, composeErr := composeExactWorker(workerExactComposeOptions{
		Rollout: workerOnRollout(), Resources: resources, CloseTimeout: closeBudget,
	})
	if composition != nil || composeErr == nil {
		t.Fatalf("composeExactWorker() = %p, %v; want dependency rejection", composition, composeErr)
	}
	select {
	case remaining := <-deadlineRemaining:
		if remaining <= 0 || remaining > 4*closeBudget {
			t.Fatalf("resource close deadline remaining = %s, want supplied budget %s", remaining, closeBudget)
		}
	case <-time.After(time.Second):
		t.Fatal("exact Compose failure did not start resource cleanup")
	}
}

func TestWorkerRoleIgnoresOtherProcessConfigurationAndReadiness(t *testing.T) {
	rollout := workerOnRollout()
	rollout.Credential.DesktopResource = config.CredentialRolloutMode("invalid-other-role")
	rollout.Credential.AgentResource = config.CredentialRolloutMode("invalid-other-role")
	rollout.Durable.PublicAPI = config.DurablePublicAPIMode("invalid-other-role")
	rollout.Desktop.AgentTransport = config.DesktopAgentTransport("invalid-other-role")
	rollout.Readiness.TokenRolloverComplete = true
	rollout.Readiness.ActiveDeviceSessions = true
	rollout.Readiness.AtomicLiveEventStream = true

	intent := workerRoleIntent(rollout)
	if !intent.WorkerEnabled || intent.PublicAPIEnabled || intent.DesktopDurable || intent.CredentialEnforcement {
		t.Fatalf("worker intent crossed process boundaries: %+v", intent)
	}
	declared := declaredWorkerReadiness(rollout)
	if declared.TokenRolloverComplete || declared.ActiveDeviceSessions || declared.AtomicLiveEventStream {
		t.Fatalf("worker readiness crossed process boundaries: %+v", declared)
	}

	executor := fakeExecutor{run: func(context.Context, agentturn.ExecutionSession) (agentv1.TurnStatus, error) {
		return agentv1.TurnStatusCompleted, nil
	}}
	_, composition := composeForTestWithRollout(t, rollout, executor, &fakeDeliverer{}, &fakeSettlement{})
	if composition.readiness.Derived.AtomicLiveEventStream {
		t.Fatal("Worker composition claimed an API event stream")
	}
	if len(composition.readiness.Overclaimed) != 0 {
		t.Fatalf("other-role declarations affected Worker readiness: %v", composition.readiness.Overclaimed)
	}
}

func TestDefaultRolloutRunsNothing(t *testing.T) {
	// The shipped default must never start Worker traffic.
	intent := workerRoleIntent(config.EffectiveAgentPlatformRollout(nil))
	if intent.WorkerEnabled || intent.PublicAPIEnabled || intent.DesktopDurable {
		t.Fatalf("default rollout enables traffic: %+v", intent)
	}
}

func TestPreparedServeDrainsEveryLoopOnCancellation(t *testing.T) {
	_, composition := composeForTest(t,
		fakeExecutor{run: func(context.Context, agentturn.ExecutionSession) (agentv1.TurnStatus, error) {
			return agentv1.TurnStatusCompleted, nil
		}}, &fakeDeliverer{}, &fakeSettlement{})
	policy := defaultWorkerProbePolicy()
	health := newWorkerRuntimeHealth(time.Now, policy.Freshness, policy.LoopFreshness)
	if !health.bindAdmissionGate(workerCompositionAdmissionGate(composition)) ||
		!health.markCompositionReady() ||
		runWorkerStartupProbe(context.Background(), composition.probe, health, policy.Timeout) != workerProbeSucceeded {
		t.Fatal("failed to prepare test runtime health")
	}

	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- servePreparedWorker(ctx, composition, health, policy) }()
	readyDeadline := time.NewTimer(5 * time.Second)
	readyPoll := time.NewTicker(time.Millisecond)
	defer readyDeadline.Stop()
	defer readyPoll.Stop()
	for !health.Snapshot().Ready {
		select {
		case <-readyPoll.C:
		case <-readyDeadline.C:
			t.Fatal("Worker loops did not become ready")
		}
	}
	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve() returned %v, want a clean shutdown", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Serve() did not stop after cancellation")
	}
	if snapshot := health.Snapshot(); snapshot.Phase != string(workerPhaseDraining) || !snapshot.Live || snapshot.Ready {
		t.Fatalf("health after loop drain = %+v, want draining until resource cleanup", snapshot)
	}
	if err := Serve(context.Background(), nil); err == nil {
		t.Fatal("Serve(nil) was accepted")
	}
}

func TestPreparedServeImmediateRecurringFailureCannotBeatLoopStartBarrier(t *testing.T) {
	probe := runtimeProbeFuncs{check: func(context.Context) error {
		return errors.New("SECRET_IMMEDIATE_RUNTIME_PROBE_FAILURE")
	}}
	_, composition := composeForTestWithProbe(t, workerOnRollout(),
		fakeExecutor{run: func(context.Context, agentturn.ExecutionSession) (agentv1.TurnStatus, error) {
			return agentv1.TurnStatusCompleted, nil
		}}, &fakeDeliverer{}, &fakeSettlement{}, probe)
	policy := workerProbePolicy{
		Interval: time.Nanosecond, Timeout: time.Second,
		Freshness: time.Second, LoopFreshness: time.Second,
		ShutdownTimeout: time.Second,
	}.normalized()
	health := newWorkerRuntimeHealth(time.Now, policy.Freshness, policy.LoopFreshness)
	if !health.bindAdmissionGate(workerCompositionAdmissionGate(composition)) ||
		!health.markCompositionReady() ||
		runWorkerStartupProbe(context.Background(), composition.probe, health, policy.Timeout) != workerProbeSucceeded {
		t.Fatal("failed to prepare immediate-failure runtime")
	}

	done := make(chan error, 1)
	go func() {
		done <- servePreparedWorker(context.Background(), composition, health, policy)
	}()
	select {
	case err := <-done:
		if !errors.Is(err, errWorkerReadinessLost) {
			t.Fatalf("Serve error = %v, want stable readiness-loss result", err)
		}
		if strings.Contains(err.Error(), "SECRET_IMMEDIATE_RUNTIME_PROBE_FAILURE") {
			t.Fatalf("Serve error leaked probe detail in %q", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("immediate recurring failure deadlocked loop/probe supervision")
	}
	snapshot := health.Snapshot()
	if snapshot.Phase != string(workerPhaseDraining) || !snapshot.Live || snapshot.Ready ||
		!containsReason(snapshot, reasonDependencyProbeFailed) {
		t.Fatalf("immediate recurring failure health = %+v", snapshot)
	}
}
