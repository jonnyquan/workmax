package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	agentv1 "server/contracts/agent/v1"
	"server/service/agentturn"
	"server/utils/testutil"
)

type workerBuilderFixture struct {
	catalog           workerDependencyCatalog
	sequence          []string
	sequenceMu        sync.Mutex
	resourceClose     atomic.Int32
	deliverers        map[string]*fakeDeliverer
	databaseRegistrar workerResourceRegistrar
	providerUsage     *workerProviderUsageBinding
	pluginUsage       map[string]workerPluginProviderUsage
}

// settleOnlyAuthority models a pre-P0-041 provider. The public candidate
// composition may still accept this compatibility shape, but the exact
// production Builder requires the complete usage Review capability.
type settleOnlyAuthority struct{}

func (settleOnlyAuthority) Settle(*gorm.DB, agentturn.SettlementCommand) error {
	return nil
}

type reviewHoldOnlyAuthority struct{ settleOnlyAuthority }

func (reviewHoldOnlyAuthority) HoldForReview(*gorm.DB, agentturn.SettlementReviewHoldCommand) error {
	return nil
}

type reviewResolutionOnlyAuthority struct{ reviewHoldOnlyAuthority }

func (reviewResolutionOnlyAuthority) ResolveReview(
	*gorm.DB,
	agentturn.SettlementReviewResolutionAuthorityCommand,
) (agentturn.SettlementReviewResolutionAuthorityReceipt, error) {
	return agentturn.SettlementReviewResolutionAuthorityReceipt{}, nil
}

// legacyUsageAuthority models the strongest pre-P0-045 settlement shape. It
// can measure synthetic Review usage but cannot consume sealed Provider
// Journal receipts, so exact production acquisition must reject it.
type legacyUsageAuthority struct{ reviewResolutionOnlyAuthority }

func (legacyUsageAuthority) MeasureReview(
	*gorm.DB,
	agentturn.MeasureSettlementReviewUsageCommand,
) (agentturn.SettlementReviewUsageAuthorityReceipt, error) {
	return agentturn.SettlementReviewUsageAuthorityReceipt{}, nil
}

var _ agentturn.SettlementReviewUsageAuthority = legacyUsageAuthority{}

func newWorkerBuilderFixture(t *testing.T, requested workerDependencyPlan) *workerBuilderFixture {
	t.Helper()
	fixture := &workerBuilderFixture{
		deliverers:  make(map[string]*fakeDeliverer),
		pluginUsage: make(map[string]workerPluginProviderUsage),
	}
	record := func(value string) {
		fixture.sequenceMu.Lock()
		fixture.sequence = append(fixture.sequence, value)
		fixture.sequenceMu.Unlock()
	}
	fixture.catalog.Database = func(
		_ context.Context,
		_ workerValidatedDatabaseConfig,
		registrar workerResourceRegistrar,
	) (*gorm.DB, RuntimeProbe, workerFactoryOwnership, error) {
		record("database")
		fixture.databaseRegistrar = registrar
		if err := registrar.Own(WorkerResourceCloseFunc(func(context.Context) error {
			fixture.resourceClose.Add(1)
			return nil
		})); err != nil {
			return nil, nil, workerFactoryRegisteredResources, err
		}
		return testutil.NewTestDB(t), healthyRuntimeProbe{}, workerFactoryRegisteredResources, nil
	}
	fixture.catalog.ProviderUsage = func(
		_ context.Context,
		database *gorm.DB,
		store *agentturn.SQLStore,
		plugins []agentv1.EventPluginRef,
		_ workerResourceRegistrar,
	) (*workerProviderUsageBinding, RuntimeProbe, workerFactoryOwnership, error) {
		record("provider-usage")
		requirements := make([]workerPluginRequirement, len(plugins))
		for index, plugin := range plugins {
			requirements[index].Snapshot = plugin
		}
		binding := newRealProviderUsageBindingForTest(t, database, store, requirements)
		fixture.providerUsage = binding
		return binding, healthyRuntimeProbe{}, workerFactoryBorrowedOnly, nil
	}
	fixture.catalog.Settlement = func(
		_ context.Context,
		_ *gorm.DB,
		binding *workerProviderUsageBinding,
		_ workerResourceRegistrar,
	) (agentturn.SettlementAuthority, RuntimeProbe, workerFactoryOwnership, error) {
		record("settlement")
		return &fakeJournalBackedSettlement{
			SettlementReviewProviderUsageAuthority: &fakeSettlement{}, providerUsage: binding,
		}, healthyRuntimeProbe{}, workerFactoryBorrowedOnly, nil
	}
	pluginFactory := workerExecutorFactory(func(
		_ context.Context,
		_ *gorm.DB,
		requirement workerPluginRequirement,
		usage workerPluginProviderUsage,
		_ workerResourceRegistrar,
	) (agentturn.TurnExecutor, RuntimeProbe, workerFactoryOwnership, error) {
		record("plugin:" + requirement.Snapshot.ID)
		if !usage.intact(fixture.providerUsage, requirement.Snapshot) {
			return nil, nil, workerFactoryBorrowedOnly, errWorkerPluginProviderUsageInvalid
		}
		fixture.pluginUsage[requirement.Snapshot.ID] = usage
		return fakeExecutor{run: func(context.Context, agentturn.ExecutionSession) (agentv1.TurnStatus, error) {
			return agentv1.TurnStatusCompleted, nil
		}}, healthyRuntimeProbe{}, workerFactoryBorrowedOnly, nil
	})
	effectFactory := workerEffectFactory(func(
		_ context.Context,
		_ *gorm.DB,
		topic string,
		_ workerResourceRegistrar,
	) (agentturn.Deliverer, RuntimeProbe, workerFactoryOwnership, error) {
		record("effect:" + topic)
		deliverer := &fakeDeliverer{}
		fixture.sequenceMu.Lock()
		fixture.deliverers[topic] = deliverer
		fixture.sequenceMu.Unlock()
		return deliverer, healthyRuntimeProbe{}, workerFactoryBorrowedOnly, nil
	})
	topics := make(map[string]struct{})
	for _, requirement := range requested.Plugins {
		fixture.catalog.Plugins = append(fixture.catalog.Plugins, workerPluginRegistration{
			Snapshot: requirement.Snapshot, EffectTopics: append([]string(nil), requirement.EffectTopics...),
			Factory: pluginFactory,
		})
		for _, topic := range requirement.EffectTopics {
			topics[topic] = struct{}{}
		}
	}
	for topic := range topics {
		fixture.catalog.Effects = append(fixture.catalog.Effects, workerEffectRegistration{
			Topic: topic, Factory: effectFactory,
		})
	}
	return fixture
}

func (fixture *workerBuilderFixture) recorded() []string {
	fixture.sequenceMu.Lock()
	defer fixture.sequenceMu.Unlock()
	return append([]string(nil), fixture.sequence...)
}

func validatedWorkerBuilderPlan(t *testing.T, fixture *workerBuilderFixture) validatedWorkerDependencyPlan {
	t.Helper()
	requested := validProductionPlanForTest()
	validated, err := validateWorkerDependencyPlan(
		validProductionSnapshotForTest(), validBuildIdentityForTest(), requested, fixture.catalog,
	)
	if err != nil {
		t.Fatalf("validateWorkerDependencyPlan(): %v", err)
	}
	return validated
}

func TestBuildValidatedWorkerCompositionAcquiresInCanonicalOrderAndTransfersOneOwner(t *testing.T) {
	requested := validProductionPlanForTest()
	fixture := newWorkerBuilderFixture(t, requested)
	plan := validatedWorkerBuilderPlan(t, fixture)
	composition, err := buildValidatedWorkerComposition(context.Background(), plan, time.Second)
	if err != nil {
		t.Fatalf("buildValidatedWorkerComposition(): %v", err)
	}
	if !workerProductionCompositionReady(composition) || composition.runtimeScope == nil {
		t.Fatal("Builder returned a production-unready or unsealed exact composition")
	}
	admission := composition.runtimeScope.admissionGate()
	if admission == nil || !admission.Open() ||
		!composition.worker.MatchesAdmissionGate(admission) ||
		!composition.reconciler.MatchesAdmissionGate(admission) ||
		!composition.dispatcher.MatchesAdmissionGate(admission) {
		t.Fatal("Builder did not bind one open AdmissionGate to all runtime components")
	}
	composite, ok := composition.probe.delegate.(*workerCompositeRuntimeProbe)
	wantProbeCount := workerCoreProbeCount + len(plan.plugins) + len(plan.effectTopics)
	if !ok || !composite.intact(wantProbeCount) {
		t.Fatalf("Builder composite probe = %T/%v, want all %d acquired probes",
			composition.probe.delegate, ok, wantProbeCount)
	}
	want := []string{
		"database", "provider-usage", "settlement",
		"plugin:workmax.workbook", "plugin:workmax.writer",
		"effect:artifact.index", "effect:workbook.export", "effect:writer.export",
	}
	if got := fixture.recorded(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("Factory order = %v, want %v", got, want)
	}
	if !composition.runtimeScope.providerUsage.intact(
		fixture.providerUsage.database,
		composition.store,
		workerRequirementSnapshots(plan.plugins),
	) || composition.runtimeScope.providerUsage != fixture.providerUsage ||
		!composition.runtimeScope.settlementAuthority.matchesWorkerProviderUsageBinding(fixture.providerUsage) {
		t.Fatal("Builder did not preserve one exact ProviderUsage binding through Settlement and runtime scope")
	}
	if len(fixture.pluginUsage) != len(plan.plugins) {
		t.Fatalf("Plugin ProviderUsage facades = %d, want %d", len(fixture.pluginUsage), len(plan.plugins))
	}
	for _, plugin := range plan.plugins {
		if !fixture.pluginUsage[plugin.Snapshot.ID].intact(fixture.providerUsage, plugin.Snapshot) {
			t.Fatalf("Plugin %q did not receive its exact ProviderUsage facade", plugin.Snapshot.ID)
		}
	}
	if fixture.resourceClose.Load() != 0 {
		t.Fatal("successful ownership transfer closed the database resource early")
	}
	var lateClose atomic.Int32
	if err := fixture.databaseRegistrar.Own(WorkerResourceCloseFunc(func(context.Context) error {
		lateClose.Add(1)
		return nil
	})); !errors.Is(err, errWorkerAcquisitionClosed) {
		t.Fatalf("post-transfer Own error = %v, want closed", err)
	}
	waitForAtomicValue(t, &lateClose, 1)
	if fixture.resourceClose.Load() != 0 {
		t.Fatal("post-transfer Own changed or closed the Composition owner")
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := composition.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	if admission.Open() {
		t.Fatal("Composition.Close left its AdmissionGate open")
	}
	if fixture.resourceClose.Load() != 1 {
		t.Fatalf("resource close calls = %d, want exactly one", fixture.resourceClose.Load())
	}
}

func TestProductionCompositionRejectsPostBindSettlementMutation(t *testing.T) {
	for name, mutate := range map[string]func(*agentturn.SQLStore){
		"nil": func(store *agentturn.SQLStore) {
			store.WithSettlementAuthority(nil)
		},
		"settle only": func(store *agentturn.SQLStore) {
			store.WithSettlementAuthority(settleOnlyAuthority{})
		},
		"different review authority": func(store *agentturn.SQLStore) {
			store.WithSettlementAuthority(&fakeSettlement{})
		},
	} {
		t.Run(name, func(t *testing.T) {
			requested := validProductionPlanForTest()
			fixture := newWorkerBuilderFixture(t, requested)
			plan := validatedWorkerBuilderPlan(t, fixture)
			composition, err := buildValidatedWorkerComposition(context.Background(), plan, time.Second)
			if err != nil || !workerProductionCompositionReady(composition) {
				t.Fatalf("initial Builder result = %p, %v", composition, err)
			}
			mutate(composition.store)
			if workerProductionCompositionReady(composition) ||
				composition.store.MatchesSettlementAuthorityBinding(composition.runtimeScope.settlement) {
				t.Fatal("post-bind Settlement mutation remained production-ready")
			}
			if err := Serve(context.Background(), composition); !errors.Is(err, errWorkerDependenciesUnavailable) {
				t.Fatalf("Serve() after Settlement mutation = %v", err)
			}
			if fixture.resourceClose.Load() != 1 {
				t.Fatalf("rejected Composition resource closes = %d, want 1", fixture.resourceClose.Load())
			}
		})
	}
}

func TestProductionRuntimeScopeRejectsAdmissionGateReplacementAndClosure(t *testing.T) {
	requested := validProductionPlanForTest()
	fixture := newWorkerBuilderFixture(t, requested)
	plan := validatedWorkerBuilderPlan(t, fixture)
	composition, err := buildValidatedWorkerComposition(context.Background(), plan, time.Second)
	if err != nil {
		t.Fatalf("buildValidatedWorkerComposition(): %v", err)
	}
	original := composition.runtimeScope.admissionGate()
	replacement := agentturn.NewAdmissionGate()
	composition.runtimeScope.admission = replacement
	if workerProductionCompositionReady(composition) {
		t.Fatal("production readiness accepted a replacement AdmissionGate")
	}
	composition.runtimeScope.admission = original
	if !workerProductionCompositionReady(composition) {
		t.Fatal("restoring the exact AdmissionGate did not restore readiness")
	}
	original.Close()
	if workerProductionCompositionReady(composition) || original.Open() {
		t.Fatal("closed AdmissionGate remained production-ready or reopened")
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if closeErr := composition.Close(closeCtx); closeErr != nil {
		t.Fatalf("close composition: %v", closeErr)
	}
}

func TestProductionRuntimeScopeRejectsProviderUsageAndMatcherTampering(t *testing.T) {
	requested := validProductionPlanForTest()
	fixture := newWorkerBuilderFixture(t, requested)
	plan := validatedWorkerBuilderPlan(t, fixture)
	composition, err := buildValidatedWorkerComposition(context.Background(), plan, time.Second)
	if err != nil || !workerProductionCompositionReady(composition) {
		t.Fatalf("initial Builder result = %p, %v", composition, err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = composition.Close(closeCtx)
	})

	originalBinding := composition.runtimeScope.providerUsage
	originalAuthority := composition.runtimeScope.settlementAuthority
	originalJournal := composition.runtimeScope.settlementJournal
	foreign, err := newWorkerProviderUsageBinding(
		originalBinding.database, composition.store, originalBinding.journal,
		workerRequirementSnapshots(plan.plugins), originalBinding.recorders,
	)
	if err != nil {
		t.Fatal(err)
	}
	composition.runtimeScope.providerUsage = foreign
	if workerProductionCompositionReady(composition) {
		t.Fatal("runtime scope accepted a replacement ProviderUsage binding")
	}
	composition.runtimeScope.providerUsage = originalBinding
	if !workerProductionCompositionReady(composition) {
		t.Fatal("restoring the exact ProviderUsage binding did not restore readiness")
	}

	composition.runtimeScope.settlementAuthority = &fakeJournalBackedSettlement{
		SettlementReviewProviderUsageAuthority: &fakeSettlement{}, providerUsage: foreign,
	}
	if workerProductionCompositionReady(composition) {
		t.Fatal("runtime scope accepted a Settlement matcher bound to foreign ProviderUsage")
	}
	composition.runtimeScope.settlementAuthority = originalAuthority
	if !workerProductionCompositionReady(composition) {
		t.Fatal("restoring the exact Settlement matcher did not restore readiness")
	}

	foreignJournal, err := agentturn.NewProviderUsageJournal(composition.store)
	if err != nil {
		t.Fatal(err)
	}
	composition.runtimeScope.settlementJournal = foreignJournal
	if workerProductionCompositionReady(composition) {
		t.Fatal("runtime scope accepted a Settlement binding for another ProviderUsage Journal")
	}
	composition.runtimeScope.settlementJournal = originalJournal
	if !workerProductionCompositionReady(composition) {
		t.Fatal("restoring the exact Settlement ProviderUsage Journal did not restore readiness")
	}

	originalBinding.identity.marker = 0
	if workerProductionCompositionReady(composition) {
		t.Fatal("runtime scope accepted a tampered ProviderUsage binding seal")
	}
	originalBinding.identity.marker = 1
	if !workerProductionCompositionReady(composition) {
		t.Fatal("restoring the ProviderUsage binding seal did not restore readiness")
	}
}

func TestBuildValidatedWorkerCompositionBindsEachEffectFactoryResultInternally(t *testing.T) {
	requested := validProductionPlanForTest()
	fixture := newWorkerBuilderFixture(t, requested)
	plan := validatedWorkerBuilderPlan(t, fixture)
	composition, err := buildValidatedWorkerComposition(context.Background(), plan, time.Second)
	if err != nil {
		t.Fatalf("buildValidatedWorkerComposition(): %v", err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = composition.Close(closeCtx)
	})

	router := composition.runtimeScope.effects.deliverer
	for _, topic := range plan.effectTopics {
		if _, err := router.Deliver(context.Background(), agentturn.EffectDelivery{Topic: topic}); err != nil {
			t.Fatalf("Deliver(%q): %v", topic, err)
		}
		for boundTopic, deliverer := range fixture.deliverers {
			want := 0
			if boundTopic == topic {
				want = 1
			}
			if got := len(deliverer.delivered()); got != want {
				t.Fatalf("after %q, factory result %q calls = %d, want %d", topic, boundTopic, got, want)
			}
		}
		// Reset observations so each route is proved independently.
		for _, deliverer := range fixture.deliverers {
			deliverer.mu.Lock()
			deliverer.seen = nil
			deliverer.mu.Unlock()
		}
	}
	if _, err := router.Deliver(context.Background(), agentturn.EffectDelivery{
		Topic: "unexpected.topic",
	}); !errors.Is(err, errWorkerEffectTopicUnauthorized) {
		t.Fatalf("unknown Topic error = %v, want unauthorized", err)
	}
}

func TestBuildValidatedWorkerCompositionRejectsUnownedDatabaseBeforeLaterFactories(t *testing.T) {
	requested := validProductionPlanForTest()
	fixture := newWorkerBuilderFixture(t, requested)
	fixture.catalog.Database = func(
		context.Context,
		workerValidatedDatabaseConfig,
		workerResourceRegistrar,
	) (*gorm.DB, RuntimeProbe, workerFactoryOwnership, error) {
		fixture.sequenceMu.Lock()
		fixture.sequence = append(fixture.sequence, "database-unowned")
		fixture.sequenceMu.Unlock()
		return testutil.NewTestDB(t), healthyRuntimeProbe{}, workerFactoryRegisteredResources, nil
	}
	plan := validatedWorkerBuilderPlan(t, fixture)
	composition, err := buildValidatedWorkerComposition(context.Background(), plan, time.Second)
	if composition != nil || !errors.Is(err, errWorkerAcquisitionFailed) {
		t.Fatalf("Builder = %p, %v; want stable acquisition failure", composition, err)
	}
	if got := fixture.recorded(); len(got) != 1 || got[0] != "database-unowned" {
		t.Fatalf("Factory sequence after unowned database = %v", got)
	}
}

func TestBuildValidatedWorkerCompositionRejectsInvalidProviderUsageBeforeSettlement(t *testing.T) {
	var typedNilBinding *workerProviderUsageBinding
	var typedNilProbe *runtimeProbeFuncs
	for name, result := range map[string]func(
		*gorm.DB,
		*agentturn.SQLStore,
		[]agentv1.EventPluginRef,
	) (*workerProviderUsageBinding, RuntimeProbe){
		"nil binding": func(*gorm.DB, *agentturn.SQLStore, []agentv1.EventPluginRef) (*workerProviderUsageBinding, RuntimeProbe) {
			return nil, healthyRuntimeProbe{}
		},
		"typed nil binding": func(*gorm.DB, *agentturn.SQLStore, []agentv1.EventPluginRef) (*workerProviderUsageBinding, RuntimeProbe) {
			return typedNilBinding, healthyRuntimeProbe{}
		},
		"typed nil probe": func(database *gorm.DB, store *agentturn.SQLStore, plugins []agentv1.EventPluginRef) (*workerProviderUsageBinding, RuntimeProbe) {
			binding := newRealProviderUsageBindingForTest(
				t, database, store, providerUsageRequirementsForSnapshotsForTest(plugins),
			)
			return binding, typedNilProbe
		},
		"different database": func(database *gorm.DB, store *agentturn.SQLStore, plugins []agentv1.EventPluginRef) (*workerProviderUsageBinding, RuntimeProbe) {
			valid := newRealProviderUsageBindingForTest(
				t, database, store, providerUsageRequirementsForSnapshotsForTest(plugins),
			)
			binding, _ := newWorkerProviderUsageBinding(
				testutil.NewTestDB(t), store, valid.journal, plugins, valid.recorders,
			)
			return binding, healthyRuntimeProbe{}
		},
		"different store": func(database *gorm.DB, store *agentturn.SQLStore, plugins []agentv1.EventPluginRef) (*workerProviderUsageBinding, RuntimeProbe) {
			valid := newRealProviderUsageBindingForTest(
				t, database, store, providerUsageRequirementsForSnapshotsForTest(plugins),
			)
			other, _ := agentturn.NewSQLStore(database)
			journal, _ := agentturn.NewProviderUsageJournal(other)
			recorders := rebindProviderUsageRecorderRegistryForTest(
				t, journal, plugins, valid.recorders,
			)
			binding, _ := newWorkerProviderUsageBinding(
				database, other, journal, plugins, recorders,
			)
			return binding, healthyRuntimeProbe{}
		},
		"partial Plugin coverage": func(database *gorm.DB, store *agentturn.SQLStore, plugins []agentv1.EventPluginRef) (*workerProviderUsageBinding, RuntimeProbe) {
			valid := newRealProviderUsageBindingForTest(
				t, database, store, providerUsageRequirementsForSnapshotsForTest(plugins),
			)
			partial := workerProviderUsageRecorderRegistry{
				workerPluginSnapshotKey(plugins[0]): valid.recorders[workerPluginSnapshotKey(plugins[0])],
			}
			binding, _ := newWorkerProviderUsageBinding(
				database, store, valid.journal, plugins[:1], partial,
			)
			return binding, healthyRuntimeProbe{}
		},
	} {
		t.Run(name, func(t *testing.T) {
			requested := validProductionPlanForTest()
			fixture := newWorkerBuilderFixture(t, requested)
			fixture.catalog.ProviderUsage = func(
				_ context.Context,
				database *gorm.DB,
				store *agentturn.SQLStore,
				plugins []agentv1.EventPluginRef,
				_ workerResourceRegistrar,
			) (*workerProviderUsageBinding, RuntimeProbe, workerFactoryOwnership, error) {
				fixture.sequenceMu.Lock()
				fixture.sequence = append(fixture.sequence, "provider-usage-invalid")
				fixture.sequenceMu.Unlock()
				binding, probe := result(database, store, plugins)
				return binding, probe, workerFactoryBorrowedOnly, nil
			}
			plan := validatedWorkerBuilderPlan(t, fixture)
			composition, err := buildValidatedWorkerComposition(context.Background(), plan, time.Second)
			if composition != nil || !errors.Is(err, errWorkerAcquisitionFailed) {
				t.Fatalf("Builder = %p, %v; want stable acquisition failure", composition, err)
			}
			waitForAtomicValue(t, &fixture.resourceClose, 1)
			if got := strings.Join(fixture.recorded(), "|"); got != "database|provider-usage-invalid" {
				t.Fatalf("later Factory ran after invalid ProviderUsage: %s", got)
			}
		})
	}
}

func TestBuildValidatedWorkerCompositionReapsProviderUsagePartialResources(t *testing.T) {
	requested := validProductionPlanForTest()
	fixture := newWorkerBuilderFixture(t, requested)
	var providerClose atomic.Int32
	fixture.catalog.ProviderUsage = func(
		_ context.Context,
		_ *gorm.DB,
		_ *agentturn.SQLStore,
		_ []agentv1.EventPluginRef,
		registrar workerResourceRegistrar,
	) (*workerProviderUsageBinding, RuntimeProbe, workerFactoryOwnership, error) {
		fixture.sequenceMu.Lock()
		fixture.sequence = append(fixture.sequence, "provider-usage-failed")
		fixture.sequenceMu.Unlock()
		if err := registrar.Own(WorkerResourceCloseFunc(func(context.Context) error {
			providerClose.Add(1)
			return nil
		})); err != nil {
			return nil, nil, workerFactoryRegisteredResources, err
		}
		return nil, nil, workerFactoryRegisteredResources, errors.New("SECRET_PROVIDER_USAGE_DSN")
	}
	plan := validatedWorkerBuilderPlan(t, fixture)
	composition, err := buildValidatedWorkerComposition(context.Background(), plan, time.Second)
	if composition != nil || !errors.Is(err, errWorkerAcquisitionFailed) {
		t.Fatalf("Builder = %p, %v; want stable acquisition failure", composition, err)
	}
	if strings.Contains(err.Error(), "SECRET_") {
		t.Fatalf("Builder exposed raw ProviderUsage Factory error: %q", err)
	}
	waitForAtomicValue(t, &providerClose, 1)
	waitForAtomicValue(t, &fixture.resourceClose, 1)
	if got := strings.Join(fixture.recorded(), "|"); got != "database|provider-usage-failed" {
		t.Fatalf("later Factory ran after ProviderUsage failure: %s", got)
	}
}

func TestBuildValidatedWorkerCompositionRejectsSettlementBoundToDifferentProviderUsage(t *testing.T) {
	requested := validProductionPlanForTest()
	fixture := newWorkerBuilderFixture(t, requested)
	fixture.catalog.Settlement = func(
		_ context.Context,
		_ *gorm.DB,
		binding *workerProviderUsageBinding,
		_ workerResourceRegistrar,
	) (agentturn.SettlementAuthority, RuntimeProbe, workerFactoryOwnership, error) {
		fixture.sequenceMu.Lock()
		fixture.sequence = append(fixture.sequence, "settlement-foreign-provider-usage")
		fixture.sequenceMu.Unlock()
		foreign, err := newWorkerProviderUsageBinding(
			binding.database, binding.store, binding.journal, binding.plugins, binding.recorders,
		)
		if err != nil {
			return nil, nil, workerFactoryBorrowedOnly, err
		}
		return &fakeJournalBackedSettlement{
			SettlementReviewProviderUsageAuthority: &fakeSettlement{}, providerUsage: foreign,
		}, healthyRuntimeProbe{}, workerFactoryBorrowedOnly, nil
	}
	plan := validatedWorkerBuilderPlan(t, fixture)
	composition, err := buildValidatedWorkerComposition(context.Background(), plan, time.Second)
	if composition != nil || !errors.Is(err, errWorkerAcquisitionFailed) {
		t.Fatalf("Builder = %p, %v; want stable acquisition failure", composition, err)
	}
	waitForAtomicValue(t, &fixture.resourceClose, 1)
	if got := strings.Join(fixture.recorded(), "|"); got != "database|provider-usage|settlement-foreign-provider-usage" {
		t.Fatalf("Plugin Factory ran after foreign Settlement binding: %s", got)
	}
}

func TestBuildValidatedWorkerCompositionReapsPartialResourcesAndSanitizesError(t *testing.T) {
	requested := validProductionPlanForTest()
	fixture := newWorkerBuilderFixture(t, requested)
	fixture.catalog.Settlement = func(
		context.Context,
		*gorm.DB,
		*workerProviderUsageBinding,
		workerResourceRegistrar,
	) (agentturn.SettlementAuthority, RuntimeProbe, workerFactoryOwnership, error) {
		fixture.sequenceMu.Lock()
		fixture.sequence = append(fixture.sequence, "settlement-failed")
		fixture.sequenceMu.Unlock()
		return &fakeSettlement{}, healthyRuntimeProbe{}, workerFactoryBorrowedOnly, errors.New("SECRET_SETTLEMENT_DSN")
	}
	plan := validatedWorkerBuilderPlan(t, fixture)
	composition, err := buildValidatedWorkerComposition(context.Background(), plan, time.Second)
	if composition != nil || !errors.Is(err, errWorkerAcquisitionFailed) {
		t.Fatalf("Builder = %p, %v; want stable acquisition failure", composition, err)
	}
	if strings.Contains(err.Error(), "SECRET_") {
		t.Fatalf("Builder exposed raw Factory error: %q", err)
	}
	waitForAtomicValue(t, &fixture.resourceClose, 1)
	if got := fixture.recorded(); strings.Join(got, "|") != "database|provider-usage|settlement-failed" {
		t.Fatalf("later Factory ran after failure: %v", got)
	}
}

func TestBuildValidatedWorkerCompositionRejectsSettlementWithoutProviderUsageCapability(t *testing.T) {
	var typedNilJournalBacked *fakeJournalBackedSettlement
	for _, test := range []struct {
		name      string
		authority agentturn.SettlementAuthority
	}{
		{name: "settle only", authority: settleOnlyAuthority{}},
		{name: "review hold only", authority: reviewHoldOnlyAuthority{}},
		{name: "review resolution only", authority: reviewResolutionOnlyAuthority{}},
		{name: "legacy usage authority", authority: legacyUsageAuthority{}},
		{name: "provider usage authority without exact binding matcher", authority: &fakeSettlement{}},
		{name: "typed nil Journal-backed authority", authority: typedNilJournalBacked},
	} {
		t.Run(test.name, func(t *testing.T) {
			requested := validProductionPlanForTest()
			fixture := newWorkerBuilderFixture(t, requested)
			fixture.catalog.Settlement = func(
				context.Context,
				*gorm.DB,
				*workerProviderUsageBinding,
				workerResourceRegistrar,
			) (agentturn.SettlementAuthority, RuntimeProbe, workerFactoryOwnership, error) {
				fixture.sequenceMu.Lock()
				fixture.sequence = append(fixture.sequence, "settlement-without-provider-usage")
				fixture.sequenceMu.Unlock()
				return test.authority, healthyRuntimeProbe{}, workerFactoryBorrowedOnly, nil
			}
			plan := validatedWorkerBuilderPlan(t, fixture)
			composition, err := buildValidatedWorkerComposition(context.Background(), plan, time.Second)
			if composition != nil || !errors.Is(err, errWorkerAcquisitionFailed) {
				t.Fatalf("Builder = %p, %v; want stable acquisition failure", composition, err)
			}
			waitForAtomicValue(t, &fixture.resourceClose, 1)
			if got := fixture.recorded(); strings.Join(got, "|") != "database|provider-usage|settlement-without-provider-usage" {
				t.Fatalf("later Factory ran after incompatible settlement: %v", got)
			}
		})
	}
}

func TestBuildValidatedWorkerCompositionConstructsExactClaimScopeInternally(t *testing.T) {
	requested := validProductionPlanForTest()
	fixture := newWorkerBuilderFixture(t, requested)
	plan := validatedWorkerBuilderPlan(t, fixture)
	composition, err := buildValidatedWorkerComposition(context.Background(), plan, time.Second)
	if err != nil {
		t.Fatalf("buildValidatedWorkerComposition(): %v", err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = composition.Close(closeCtx)
	})
	if !composition.runtimeScope.claim.intact(composition.store, workerRequirementSnapshots(plan.plugins)) {
		t.Fatal("Builder did not bind its exact Claim scope to the installed SQLStore and Plugin snapshots")
	}
	if !composition.runtimeScope.intact(
		composition.store, composition.worker, composition.reconciler, composition.dispatcher,
	) ||
		!composition.worker.MatchesPluginExecutionLimits(workerRequirementExecutionLimits(plan.plugins)) {
		t.Fatal("Builder did not bind exact Plugin execution limits into the runtime-scope seal")
	}
}

func TestWorkerCompositeRuntimeProbeOwnsInputAndCallsEveryChildInFixedOrder(t *testing.T) {
	calls := make([]string, 0, 6)
	makeProbe := func(name string) RuntimeProbe {
		return runtimeProbeFuncs{
			startup: func(context.Context) error {
				calls = append(calls, "startup:"+name)
				return nil
			},
			check: func(context.Context) error {
				calls = append(calls, "check:"+name)
				return nil
			},
		}
	}
	input := []RuntimeProbe{makeProbe("database"), makeProbe("settlement"), makeProbe("plugin")}
	composite, err := newWorkerCompositeRuntimeProbe(input)
	if err != nil {
		t.Fatal(err)
	}
	if composite.intact(len(input) - 1) {
		t.Fatal("composite accepted a Builder expected-count mismatch")
	}
	input[0] = runtimeProbeFuncs{
		startup: func(context.Context) error { return errors.New("caller mutation ran") },
		check:   func(context.Context) error { return errors.New("caller mutation ran") },
	}
	if err := composite.Startup(context.Background()); err != nil {
		t.Fatalf("Startup(): %v", err)
	}
	if err := composite.Check(context.Background()); err != nil {
		t.Fatalf("Check(): %v", err)
	}
	want := []string{
		"startup:database", "startup:settlement", "startup:plugin",
		"check:database", "check:settlement", "check:plugin",
	}
	if strings.Join(calls, "|") != strings.Join(want, "|") {
		t.Fatalf("probe order = %v, want %v", calls, want)
	}
}

func TestWorkerCompositeRuntimeProbeFailsClosedAtFirstFailedChild(t *testing.T) {
	for _, startup := range []bool{true, false} {
		name := "check"
		if startup {
			name = "startup"
		}
		t.Run(name, func(t *testing.T) {
			calls := make([]string, 0, 3)
			makeProbe := func(child string, fail bool) RuntimeProbe {
				invoke := func(context.Context) error {
					calls = append(calls, child)
					if fail {
						return errors.New("SECRET_PROBE_PROVIDER_RESPONSE")
					}
					return nil
				}
				probe := runtimeProbeFuncs{}
				if startup {
					probe.startup = invoke
					probe.check = func(context.Context) error { return nil }
				} else {
					probe.startup = func(context.Context) error { return nil }
					probe.check = invoke
				}
				return probe
			}
			composite, err := newWorkerCompositeRuntimeProbe([]RuntimeProbe{
				makeProbe("database", false), makeProbe("settlement", true), makeProbe("plugin", false),
			})
			if err != nil {
				t.Fatal(err)
			}
			if startup {
				err = composite.Startup(context.Background())
			} else {
				err = composite.Check(context.Background())
			}
			if !errors.Is(err, errWorkerCompositeProbeFailed) {
				t.Fatalf("probe error = %v, want stable composite failure", err)
			}
			if strings.Contains(err.Error(), "SECRET_") {
				t.Fatalf("composite exposed child probe error: %q", err)
			}
			if strings.Join(calls, "|") != "database|settlement" {
				t.Fatalf("calls after failed child = %v", calls)
			}
		})
	}
}

func TestProviderUsageProbeFailureFailsClosedThroughRuntimeHealth(t *testing.T) {
	requested := validProductionPlanForTest()
	fixture := newWorkerBuilderFixture(t, requested)
	providerFactory := fixture.catalog.ProviderUsage
	var providerProbeCalls atomic.Int32
	fixture.catalog.ProviderUsage = func(
		ctx context.Context,
		database *gorm.DB,
		store *agentturn.SQLStore,
		plugins []agentv1.EventPluginRef,
		registrar workerResourceRegistrar,
	) (*workerProviderUsageBinding, RuntimeProbe, workerFactoryOwnership, error) {
		binding, _, ownership, err := providerFactory(ctx, database, store, plugins, registrar)
		probe := runtimeProbeFuncs{
			startup: func(context.Context) error {
				providerProbeCalls.Add(1)
				return errors.New("SECRET_PROVIDER_USAGE_HEALTH")
			},
			check: func(context.Context) error {
				providerProbeCalls.Add(1)
				return errors.New("SECRET_PROVIDER_USAGE_HEALTH")
			},
		}
		return binding, probe, ownership, err
	}
	plan := validatedWorkerBuilderPlan(t, fixture)
	composition, err := buildValidatedWorkerComposition(context.Background(), plan, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = composition.Close(closeCtx)
	})
	composite, ok := composition.probe.delegate.(*workerCompositeRuntimeProbe)
	wantCount := workerCoreProbeCount + len(plan.plugins) + len(plan.effectTopics)
	if !ok || !composite.intact(wantCount) {
		t.Fatalf("composite ProviderUsage probe coverage = %T/%v", composition.probe.delegate, ok)
	}
	health := newWorkerRuntimeHealth(time.Now, time.Minute, time.Minute)
	if !health.markCompositionReady() {
		t.Fatal("failed to mark composition ready")
	}
	if outcome := runWorkerStartupProbe(
		context.Background(), composition.probe, health, time.Second,
	); outcome == workerProbeSucceeded {
		t.Fatal("ProviderUsage probe failure was accepted")
	}
	if providerProbeCalls.Load() != 1 {
		t.Fatalf("ProviderUsage probe calls = %d, want 1", providerProbeCalls.Load())
	}
	snapshot := health.Snapshot()
	if snapshot.Live || snapshot.Ready || snapshot.Phase != string(workerPhaseFailed) ||
		!containsReason(snapshot, reasonStartupProbeFailed) {
		t.Fatalf("runtime health after ProviderUsage probe failure = %+v", snapshot)
	}
	if strings.Contains(strings.Join(snapshot.Reasons, "|"), "SECRET_") {
		t.Fatalf("runtime health leaked ProviderUsage probe error: %+v", snapshot)
	}
}

func TestWorkerCompositeRuntimeProbeRejectsMissingOrOversizedChildren(t *testing.T) {
	if composite, err := newWorkerCompositeRuntimeProbe(nil); composite != nil ||
		!errors.Is(err, errWorkerCompositeProbeInvalid) {
		t.Fatalf("empty composite = %p, %v", composite, err)
	}
	var typedNil *runtimeProbeFuncs
	if composite, err := newWorkerCompositeRuntimeProbe([]RuntimeProbe{typedNil}); composite != nil || !errors.Is(err, errWorkerCompositeProbeInvalid) {
		t.Fatalf("typed-nil composite = %p, %v", composite, err)
	}
	tooMany := make([]RuntimeProbe, maxWorkerCompositeProbes+1)
	for index := range tooMany {
		tooMany[index] = healthyRuntimeProbe{}
	}
	if composite, err := newWorkerCompositeRuntimeProbe(tooMany); composite != nil || !errors.Is(err, errWorkerCompositeProbeInvalid) {
		t.Fatalf("oversized composite = %p, %v", composite, err)
	}
	valid, err := newWorkerCompositeRuntimeProbe([]RuntimeProbe{healthyRuntimeProbe{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := valid.Startup(nil); !errors.Is(err, errWorkerCompositeProbeInvalid) {
		t.Fatalf("Startup(nil) = %v, want invalid composite", err)
	}
	if err := valid.Check(nil); !errors.Is(err, errWorkerCompositeProbeInvalid) {
		t.Fatalf("Check(nil) = %v, want invalid composite", err)
	}
}

func TestWorkerCompositeRuntimeProbeStopsBeforeNextChildAfterCancellation(t *testing.T) {
	for _, startup := range []bool{true, false} {
		name := "check"
		if startup {
			name = "startup"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			secondCalls := 0
			canceling := runtimeProbeFuncs{
				startup: func(context.Context) error { cancel(); return nil },
				check:   func(context.Context) error { cancel(); return nil },
			}
			second := runtimeProbeFuncs{
				startup: func(context.Context) error { secondCalls++; return nil },
				check:   func(context.Context) error { secondCalls++; return nil },
			}
			composite, err := newWorkerCompositeRuntimeProbe([]RuntimeProbe{canceling, second})
			if err != nil {
				t.Fatal(err)
			}
			if startup {
				err = composite.Startup(ctx)
			} else {
				err = composite.Check(ctx)
			}
			if !errors.Is(err, context.Canceled) || secondCalls != 0 {
				t.Fatalf("composite after cancellation = %v, second calls=%d", err, secondCalls)
			}
		})
	}
}

func TestBuildValidatedWorkerCompositionInitiallyCanceledInvokesNoFactory(t *testing.T) {
	requested := validProductionPlanForTest()
	fixture := newWorkerBuilderFixture(t, requested)
	plan := validatedWorkerBuilderPlan(t, fixture)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	composition, err := buildValidatedWorkerComposition(ctx, plan, time.Second)
	if composition != nil || !errors.Is(err, errWorkerAcquisitionInvalid) {
		t.Fatalf("Builder = %p, %v; want canceled pre-acquisition rejection", composition, err)
	}
	if got := fixture.recorded(); len(got) != 0 {
		t.Fatalf("canceled Builder invoked Factories: %v", got)
	}
}
