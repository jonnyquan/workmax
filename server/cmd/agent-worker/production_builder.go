package main

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	agentv1 "server/contracts/agent/v1"
	"server/service/agentturn"
)

type workerDatabaseDependency struct {
	db    *gorm.DB
	probe RuntimeProbe
}

type workerProviderUsageDependency struct {
	binding *workerProviderUsageBinding
	probe   RuntimeProbe
}

type workerSettlementDependency struct {
	authority workerJournalBackedSettlementAuthority
	probe     RuntimeProbe
}

type workerExecutorDependency struct {
	executor agentturn.TurnExecutor
	probe    RuntimeProbe
}

type workerEffectDependency struct {
	deliverer agentturn.Deliverer
	probe     RuntimeProbe
}

var (
	errWorkerCompositeProbeInvalid = errors.New("agent-worker composite runtime probe is invalid")
	errWorkerCompositeProbeFailed  = errors.New("agent-worker composite runtime probe failed")
)

const (
	workerCoreProbeCount     = 3
	maxWorkerCompositeProbes = workerCoreProbeCount + maxWorkerProductionPlugins + maxWorkerEffectTopics
)

// workerCompositeRuntimeProbe is an internal, fail-closed view over every
// probe returned by an acquired production dependency. Its private
// constructor owns a copy of the fixed-order slice, so no external factory can
// omit, replace or reorder child health evidence before readiness consumes it.
type workerCompositeRuntimeProbe struct {
	marker        byte
	expectedCount int
	probes        []RuntimeProbe
}

func newWorkerCompositeRuntimeProbe(input []RuntimeProbe) (*workerCompositeRuntimeProbe, error) {
	if len(input) == 0 || len(input) > maxWorkerCompositeProbes {
		return nil, errWorkerCompositeProbeInvalid
	}
	probes := append([]RuntimeProbe(nil), input...)
	for _, probe := range probes {
		if dependencyMissing(probe) {
			return nil, errWorkerCompositeProbeInvalid
		}
	}
	return &workerCompositeRuntimeProbe{
		marker: 1, expectedCount: len(probes), probes: probes,
	}, nil
}

func (probe *workerCompositeRuntimeProbe) intact(expected int) bool {
	if probe == nil || probe.marker != 1 || expected <= 0 ||
		probe.expectedCount != expected || len(probe.probes) != probe.expectedCount ||
		len(probe.probes) > maxWorkerCompositeProbes {
		return false
	}
	for _, child := range probe.probes {
		if dependencyMissing(child) {
			return false
		}
	}
	return true
}

func (probe *workerCompositeRuntimeProbe) Startup(ctx context.Context) error {
	return probe.run(ctx, true)
}

func (probe *workerCompositeRuntimeProbe) Check(ctx context.Context) error {
	return probe.run(ctx, false)
}

func (probe *workerCompositeRuntimeProbe) run(ctx context.Context, startup bool) error {
	if probe == nil || ctx == nil || !probe.intact(probe.expectedCount) {
		return errWorkerCompositeProbeInvalid
	}
	for _, child := range probe.probes {
		if err := ctx.Err(); err != nil {
			return err
		}
		var err error
		if startup {
			err = child.Startup(ctx)
		} else {
			err = child.Check(ctx)
		}
		if err != nil {
			return errWorkerCompositeProbeFailed
		}
	}
	return nil
}

// buildValidatedWorkerComposition is the P0-039 production Builder candidate.
// It performs no discovery: all external factories and exact static coverage
// must already be present in a validated plan. Claim scope, Effect routing and
// probe aggregation are constructed internally from acquired results. The
// shipped runtime still does not call this function because no real
// artifact/parity evidence producer or domain, Effect and Credits catalog is
// registered.
func buildValidatedWorkerComposition(
	ctx context.Context,
	input validatedWorkerDependencyPlan,
	closeTimeout time.Duration,
) (composition *WorkerComposition, resultErr error) {
	if ctx == nil || !input.intact() {
		return nil, errWorkerDependencyPlanInvalid
	}
	plan := copyValidatedWorkerDependencyPlan(input)
	if !plan.intact() {
		return nil, errWorkerDependencyPlanInvalid
	}
	guard, err := newWorkerAcquisitionGuard(ctx, closeTimeout)
	if err != nil {
		return nil, errWorkerAcquisitionInvalid
	}
	defer func() {
		panicValue := recover()
		if resultErr != nil || panicValue != nil {
			guard.abort()
			composition = nil
			resultErr = errWorkerAcquisitionFailed
		}
	}()

	database, err := acquireWorkerFactoryDependency(
		guard, nil,
		func(registrar workerResourceRegistrar) (workerDatabaseDependency, workerFactoryOwnership, error) {
			db, probe, ownership, factoryErr := plan.catalog.Database(ctx, plan.database, registrar)
			return workerDatabaseDependency{db: db, probe: probe}, ownership, factoryErr
		},
		func(value workerDatabaseDependency) bool {
			return value.db != nil && !dependencyMissing(value.probe)
		},
	)
	if err != nil {
		return nil, err
	}

	store, err := acquireWorkerDependency(
		guard, workerBorrowsFrom(database.ownership),
		func(workerResourceRegistrar) (*agentturn.SQLStore, error) {
			return agentturn.NewSQLStore(database.value.db)
		},
		func(value *agentturn.SQLStore) bool { return value != nil },
	)
	if err != nil {
		return nil, err
	}

	snapshots := workerRequirementSnapshots(plan.plugins)
	claim, err := acquireWorkerDependency(
		guard, workerBorrowsFrom(store.ownership),
		func(workerResourceRegistrar) (workerExactClaimStore, error) {
			return newWorkerExactClaimStore(store.value,
				append([]agentv1.EventPluginRef(nil), snapshots...))
		},
		func(value workerExactClaimStore) bool {
			return value.intact(store.value, snapshots)
		},
	)
	if err != nil {
		return nil, err
	}

	providerUsage, err := acquireWorkerFactoryDependency(
		guard, []workerOwnershipReceipt{store.ownership},
		func(registrar workerResourceRegistrar) (workerProviderUsageDependency, workerFactoryOwnership, error) {
			binding, probe, ownership, factoryErr := plan.catalog.ProviderUsage(
				ctx,
				database.value.db,
				store.value,
				append([]agentv1.EventPluginRef(nil), snapshots...),
				registrar,
			)
			return workerProviderUsageDependency{binding: binding, probe: probe}, ownership, factoryErr
		},
		func(value workerProviderUsageDependency) bool {
			return value.binding.intact(database.value.db, store.value, snapshots) &&
				!dependencyMissing(value.probe)
		},
	)
	if err != nil {
		return nil, err
	}

	settlement, err := acquireWorkerFactoryDependency(
		guard, []workerOwnershipReceipt{store.ownership, providerUsage.ownership},
		func(registrar workerResourceRegistrar) (workerSettlementDependency, workerFactoryOwnership, error) {
			authority, probe, ownership, factoryErr := plan.catalog.Settlement(
				ctx, database.value.db, providerUsage.value.binding, registrar,
			)
			journalBacked, _ := authority.(workerJournalBackedSettlementAuthority)
			return workerSettlementDependency{authority: journalBacked, probe: probe}, ownership, factoryErr
		},
		func(value workerSettlementDependency) bool {
			return !dependencyMissing(value.authority) && !dependencyMissing(value.probe) &&
				value.authority.matchesWorkerProviderUsageBinding(providerUsage.value.binding)
		},
	)
	if err != nil {
		return nil, err
	}

	pluginFactories := make(map[string]workerExecutorFactory, len(plan.catalog.Plugins))
	for _, registration := range plan.catalog.Plugins {
		pluginFactories[workerPluginSnapshotKey(registration.Snapshot)] = registration.Factory
	}
	executorBindings := make([]workerPluginExecutorBinding, 0, len(plan.plugins))
	executorReceipts := make([]workerOwnershipReceipt, 0, len(plan.plugins))
	probes := []RuntimeProbe{
		database.value.probe, providerUsage.value.probe, settlement.value.probe,
	}
	probeReceipts := []workerOwnershipReceipt{
		database.ownership, providerUsage.ownership, settlement.ownership,
	}
	for _, requirement := range plan.plugins {
		factory := pluginFactories[workerPluginSnapshotKey(requirement.Snapshot)]
		factoryRequirement := requirement
		factoryRequirement.EffectTopics = append([]string(nil), requirement.EffectTopics...)
		pluginProviderUsage, scopeErr := newWorkerPluginProviderUsage(
			providerUsage.value.binding, requirement.Snapshot,
		)
		if scopeErr != nil {
			return nil, errWorkerAcquisitionFailed
		}
		acquired, acquireErr := acquireWorkerFactoryDependency(
			guard, []workerOwnershipReceipt{store.ownership, providerUsage.ownership},
			func(registrar workerResourceRegistrar) (workerExecutorDependency, workerFactoryOwnership, error) {
				executor, probe, ownership, factoryErr := factory(
					ctx, database.value.db, factoryRequirement, pluginProviderUsage, registrar,
				)
				return workerExecutorDependency{executor: executor, probe: probe}, ownership, factoryErr
			},
			func(value workerExecutorDependency) bool {
				return !dependencyMissing(value.executor) && !dependencyMissing(value.probe) &&
					pluginProviderUsage.intact(providerUsage.value.binding, requirement.Snapshot)
			},
		)
		if acquireErr != nil {
			return nil, acquireErr
		}
		executorBindings = append(executorBindings, workerPluginExecutorBinding{
			Snapshot: requirement.Snapshot, EffectTopics: append([]string(nil), requirement.EffectTopics...),
			Executor: acquired.value.executor,
		})
		executorReceipts = append(executorReceipts, acquired.ownership)
		probes = append(probes, acquired.value.probe)
		probeReceipts = append(probeReceipts, acquired.ownership)
	}

	executors, err := acquireWorkerDependency(
		guard, workerBorrowsFrom(executorReceipts...),
		func(workerResourceRegistrar) (*workerExactExecutorRegistry, error) {
			return newWorkerExactExecutorRegistry(executorBindings)
		},
		func(value *workerExactExecutorRegistry) bool { return value.intact(plan.plugins) },
	)
	if err != nil {
		return nil, err
	}

	effectFactories := make(map[string]workerEffectFactory, len(plan.catalog.Effects))
	for _, registration := range plan.catalog.Effects {
		effectFactories[registration.Topic] = registration.Factory
	}
	effectBindings := make([]workerEffectBinding, 0, len(plan.effectTopics))
	effectReceipts := make([]workerOwnershipReceipt, 0, len(plan.effectTopics))
	for _, topic := range plan.effectTopics {
		factory := effectFactories[topic]
		factoryTopic := topic
		acquired, acquireErr := acquireWorkerFactoryDependency(
			guard, []workerOwnershipReceipt{store.ownership},
			func(registrar workerResourceRegistrar) (workerEffectDependency, workerFactoryOwnership, error) {
				deliverer, probe, ownership, factoryErr := factory(ctx, database.value.db, factoryTopic, registrar)
				return workerEffectDependency{deliverer: deliverer, probe: probe}, ownership, factoryErr
			},
			func(value workerEffectDependency) bool {
				return !dependencyMissing(value.deliverer) && !dependencyMissing(value.probe)
			},
		)
		if acquireErr != nil {
			return nil, acquireErr
		}
		effectBindings = append(effectBindings, workerEffectBinding{
			Topic: topic, Deliverer: acquired.value.deliverer,
		})
		effectReceipts = append(effectReceipts, acquired.ownership)
		probes = append(probes, acquired.value.probe)
		probeReceipts = append(probeReceipts, acquired.ownership)
	}

	effects, err := acquireWorkerDependency(
		guard, workerBorrowsFrom(effectReceipts...),
		func(workerResourceRegistrar) (workerExactEffectRouter, error) {
			// Bind the exact Topic returned to each Effect factory directly to
			// that factory's acquired Deliverer. Keeping this construction inside
			// the Builder removes the former external scope factory's ability to
			// exchange otherwise valid Topic-to-Deliverer pairs.
			bindings := append([]workerEffectBinding(nil), effectBindings...)
			return newWorkerExactEffectRouter(bindings)
		},
		func(value workerExactEffectRouter) bool { return value.intact(plan.effectTopics) },
	)
	if err != nil {
		return nil, err
	}

	probe, err := acquireWorkerDependency(
		guard, workerBorrowsFrom(probeReceipts...),
		func(workerResourceRegistrar) (*workerCompositeRuntimeProbe, error) {
			return newWorkerCompositeRuntimeProbe(append([]RuntimeProbe(nil), probes...))
		},
		func(value *workerCompositeRuntimeProbe) bool { return value.intact(len(probes)) },
	)
	if err != nil {
		return nil, err
	}
	if !plan.intact() || !claim.value.intact(store.value, snapshots) ||
		!providerUsage.value.binding.intact(database.value.db, store.value, snapshots) ||
		!settlement.value.authority.matchesWorkerProviderUsageBinding(providerUsage.value.binding) ||
		!executors.value.intact(plan.plugins) || !effects.value.intact(plan.effectTopics) {
		return nil, errWorkerDependencyPlanInvalid
	}

	owner, err := guard.seal()
	if err != nil {
		return nil, err
	}
	composition, err = composeExactWorker(workerExactComposeOptions{
		Rollout: plan.rollout, Identity: plan.identity,
		Store: store.value, Claim: claim.value, Executors: executors.value, Effects: effects.value,
		Plugins: copyWorkerPluginRequirements(plan.plugins), ProviderUsage: providerUsage.value.binding,
		Settlement:   settlement.value.authority,
		RuntimeProbe: probe.value, Resources: owner, CloseTimeout: guard.closeTimeout,
	})
	if err != nil {
		return nil, err
	}
	// Ownership transfer is intentionally the final Builder operation.
	return guard.commit(composition)
}

func copyValidatedWorkerDependencyPlan(input validatedWorkerDependencyPlan) validatedWorkerDependencyPlan {
	output := input
	output.plugins = copyWorkerPluginRequirements(input.plugins)
	output.effectTopics = append([]string(nil), input.effectTopics...)
	output.catalog = copyWorkerDependencyCatalog(input.catalog)
	return output
}
