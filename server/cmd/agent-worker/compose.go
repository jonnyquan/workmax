package main

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"server/config"
	agentv1 "server/contracts/agent/v1"
	"server/service/agentturn"
)

// ExecutorRegistry maps a Plugin ID to the runtime that executes its Turns.
//
// The kernel owns admission, leasing, fencing, event persistence, cancellation
// and settlement; a registered executor owns only what the Turn actually does.
// The registry is keyed by Plugin ID rather than holding one global executor so
// Writer, Workbook and Media can migrate independently, each behind its own
// parity ledger.
type ExecutorRegistry map[string]agentturn.TurnExecutor

// Executor dispatches to the registered runtime for a Turn's Plugin.
//
// An unregistered Plugin fails the Turn rather than falling back to some
// default. A worker that quietly ran the wrong runtime for a Plugin would
// produce output attributed to a Plugin snapshot that never generated it.
type registryExecutor struct {
	registry ExecutorRegistry
}

func dependencyMissing(dependency any) bool {
	if dependency == nil {
		return true
	}
	value := reflect.ValueOf(dependency)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func copyExecutorRegistry(registry ExecutorRegistry) (ExecutorRegistry, error) {
	if len(registry) == 0 {
		return nil, nil
	}
	copy := make(ExecutorRegistry, len(registry))
	for pluginID, executor := range registry {
		normalizedPluginID := strings.TrimSpace(pluginID)
		if normalizedPluginID == "" {
			return nil, fmt.Errorf("agent worker executor has an empty plugin id")
		}
		if normalizedPluginID != pluginID {
			return nil, fmt.Errorf("agent worker executor plugin id must not contain surrounding whitespace")
		}
		if dependencyMissing(executor) {
			return nil, fmt.Errorf("agent worker executor for plugin %q is unavailable", pluginID)
		}
		copy[pluginID] = executor
	}
	return copy, nil
}

func (executor registryExecutor) Execute(ctx context.Context, session agentturn.ExecutionSession) (agentv1.TurnStatus, error) {
	pluginID := session.Turn().Plugin.ID
	runtime, ok := executor.registry[pluginID]
	if !ok {
		return "", fmt.Errorf("no executor is registered for plugin %q", pluginID)
	}
	return runtime.Execute(ctx, session)
}

// WorkerComposition is everything the process runs.
type WorkerComposition struct {
	store             *agentturn.SQLStore
	worker            *agentturn.Worker
	reconciler        *agentturn.Reconciler
	dispatcher        *agentturn.EffectDispatcher
	resources         *workerResourceStack
	readiness         agentturn.ReadinessReport
	probe             *sealedRuntimeProbe
	runtimeScope      *workerRuntimeScopeSeal
	seal              *workerCompositionSeal
	ownershipTransfer *workerOwnershipTransferSeal
}

// A non-zero private token means the composition passed Compose's installed-
// object readiness derivation. Callers cannot promote an arbitrary collection
// of pointers by setting Readiness.Ready themselves.
type sealedRuntimeProbe struct {
	delegate RuntimeProbe
}

func (probe *sealedRuntimeProbe) Startup(ctx context.Context) error {
	return probe.delegate.Startup(ctx)
}

func (probe *sealedRuntimeProbe) Check(ctx context.Context) error {
	return probe.delegate.Check(ctx)
}

type workerCompositionSeal struct {
	marker            byte
	store             *agentturn.SQLStore
	worker            *agentturn.Worker
	reconciler        *agentturn.Reconciler
	dispatcher        *agentturn.EffectDispatcher
	resources         *workerResourceStack
	probe             *sealedRuntimeProbe
	runtimeScope      *workerRuntimeScopeSeal
	ownershipTransfer *workerOwnershipTransferSeal
}

func (seal *workerCompositionSeal) matches(composition *WorkerComposition) bool {
	return seal != nil && seal.marker == 1 && composition != nil &&
		seal.store == composition.store && seal.worker == composition.worker &&
		seal.reconciler == composition.reconciler && seal.dispatcher == composition.dispatcher &&
		seal.resources == composition.resources && seal.probe == composition.probe &&
		seal.runtimeScope == composition.runtimeScope &&
		seal.ownershipTransfer == composition.ownershipTransfer &&
		(seal.runtimeScope == nil || (seal.runtimeScope.intact(
			seal.store, seal.worker, seal.reconciler, seal.dispatcher,
		) && seal.resources.matchesAdmissionGate(seal.runtimeScope.admissionGate())))
}

// workerOwnershipTransferSeal is the private proof that a structurally valid
// exact composition crossed the acquisition Guard's one-owner commit point.
// composeExactWorker deliberately cannot create one: before commit its result
// is a candidate that may be inspected and rejected, but must never Serve.
//
// The seal binds both sides of the handoff (Composition and its structural
// seal) to the Guard, exact runtime scope and immutable resource owner. The
// Guard retains the same token after transfer so readiness cannot be forged by
// copying a pointer from another composition.
type workerOwnershipTransferSeal struct {
	marker              byte
	guard               *workerAcquisitionGuard
	composition         *WorkerComposition
	compositionSeal     *workerCompositionSeal
	runtimeScope        *workerRuntimeScopeSeal
	compositeProbe      *workerCompositeRuntimeProbe
	compositeProbeCount int
	resources           *workerResourceStack
}

func (transfer *workerOwnershipTransferSeal) intact(composition *WorkerComposition) bool {
	if transfer == nil || transfer.marker != 1 || transfer.guard == nil || composition == nil ||
		composition.ownershipTransfer != transfer || composition.seal == nil ||
		composition.seal.ownershipTransfer != transfer || transfer.composition != composition ||
		transfer.compositionSeal != composition.seal || transfer.runtimeScope == nil ||
		transfer.runtimeScope != composition.runtimeScope || transfer.resources == nil ||
		transfer.resources != composition.resources ||
		!transfer.runtimeScope.intact(
			composition.store, composition.worker, composition.reconciler, composition.dispatcher,
		) || !transfer.resources.matchesAdmissionGate(transfer.runtimeScope.admissionGate()) ||
		composition.probe == nil || transfer.compositeProbe == nil ||
		composition.probe.delegate != transfer.compositeProbe {
		return false
	}
	expectedProbeCount := workerCoreProbeCount + len(transfer.runtimeScope.plugins) + len(transfer.runtimeScope.topics)
	if transfer.compositeProbeCount != expectedProbeCount ||
		!transfer.compositeProbe.intact(expectedProbeCount) {
		return false
	}

	transfer.guard.mu.Lock()
	defer transfer.guard.mu.Unlock()
	return transfer.guard.state == workerAcquisitionTransferred &&
		transfer.guard.owner == transfer.resources && transfer.guard.transfer == transfer
}

type ComposeOptions struct {
	DB       *gorm.DB
	Rollout  config.AgentPlatformRollout
	Identity ProcessIdentity
	// Executors must cover every Plugin this worker may claim.
	Executors ExecutorRegistry
	// Deliverer performs external effects. Required when the worker is on,
	// because a worker commits effects that something has to deliver.
	Deliverer agentturn.Deliverer
	// Settlement is the commercial authority. Required for any traffic.
	Settlement agentturn.SettlementAuthority
	// RuntimeProbe proves that installed dependencies are currently working.
	// It is required independently of static readiness: a non-nil Store or
	// adapter is not evidence that its database/provider can be reached.
	RuntimeProbe RuntimeProbe
	// Resources lists process-scoped dependencies in acquisition order. Passing
	// a valid list transfers ownership to Compose immediately; a successful
	// composition closes it in reverse order, while any Compose failure starts
	// the same bounded cleanup before returning.
	Resources []WorkerResourceCloser
}

type ProcessIdentity struct {
	WorkerID    string
	BuildDigest string
}

// Compose builds the runtime and refuses to return one that must not serve.
//
// Readiness is checked against what this function actually installed, not
// against the configuration that asked for it. That is the whole point: a
// deployment cannot talk itself into readiness by editing YAML, because the
// derivation only ever looks at objects this composition holds.
func Compose(options ComposeOptions) (composition *WorkerComposition, resultErr error) {
	resources, err := newWorkerResourceStack(options.Resources)
	if err != nil {
		if resources != nil {
			resources.beginClose(defaultWorkerResourceCloseTimeout)
		}
		return nil, err
	}
	defer func() {
		panicValue := recover()
		if resultErr != nil || panicValue != nil {
			resources.beginClose(defaultWorkerResourceCloseTimeout)
		}
		if panicValue != nil {
			panic(panicValue)
		}
	}()

	if err := (&options.Rollout).ValidateWorkerRole(); err != nil {
		return nil, fmt.Errorf("invalid agent worker rollout: %w", err)
	}
	intent := workerRoleIntent(options.Rollout)
	if !intent.WorkerEnabled {
		return nil, fmt.Errorf("agent worker role is disabled")
	}
	if options.DB == nil {
		return nil, fmt.Errorf("agent worker requires a database")
	}
	if options.Identity.WorkerID == "" || options.Identity.BuildDigest == "" {
		return nil, fmt.Errorf("agent worker requires a worker id and build digest")
	}
	if dependencyMissing(options.Settlement) {
		return nil, fmt.Errorf("agent worker requires a settlement authority")
	}

	store, err := agentturn.NewSQLStore(options.DB)
	if err != nil {
		return nil, err
	}
	store.WithSettlementAuthority(options.Settlement)

	composition = &WorkerComposition{store: store, resources: resources}
	components := agentturn.PlatformComponents{
		Store:      store,
		Execution:  store,
		Reclaim:    store,
		Reconcile:  store,
		Outbox:     store,
		Settlement: options.Settlement,
	}

	registry, err := copyExecutorRegistry(options.Executors)
	if err != nil {
		return nil, err
	}
	if len(registry) == 0 {
		return nil, fmt.Errorf("agent worker is enabled but no plugin executor is registered")
	}
	if dependencyMissing(options.Deliverer) {
		return nil, fmt.Errorf("agent worker is enabled but no effect deliverer is registered")
	}
	if dependencyMissing(options.RuntimeProbe) {
		return nil, fmt.Errorf("agent worker is enabled but no runtime probe is registered")
	}
	worker, err := agentturn.NewWorker(store, registryExecutor{registry: registry},
		agentturn.WorkerOptions{
			WorkerID:          options.Identity.WorkerID,
			WorkerBuildDigest: options.Identity.BuildDigest,
		})
	if err != nil {
		return nil, err
	}
	reconciler, err := agentturn.NewReconciler(store, store, agentturn.ReconcilerOptions{
		ReconcilerID:          options.Identity.WorkerID,
		ReconcilerBuildDigest: options.Identity.BuildDigest,
	})
	if err != nil {
		return nil, err
	}
	dispatcher, err := agentturn.NewEffectDispatcher(store, options.Deliverer,
		agentturn.EffectDispatcherOptions{LeaseOwnerID: options.Identity.WorkerID})
	if err != nil {
		return nil, err
	}
	composition.worker, composition.reconciler, composition.dispatcher = worker, reconciler, dispatcher
	composition.probe = &sealedRuntimeProbe{delegate: options.RuntimeProbe}
	components.Worker, components.Reconciler, components.Dispatcher = worker, reconciler, dispatcher

	composition.readiness = agentturn.DeriveReadiness(intent, declaredWorkerReadiness(options.Rollout), components)
	if !composition.readiness.Ready {
		return nil, fmt.Errorf("agent worker role is not ready:\n  - %s",
			strings.Join(composition.readiness.Blockers, "\n  - "))
	}
	composition.seal = &workerCompositionSeal{
		marker: 1, store: store, worker: worker, reconciler: reconciler,
		dispatcher: dispatcher, resources: resources, probe: composition.probe,
	}
	return composition, nil
}

// workerExactComposeOptions is the private production composition port. The
// public Compose helper above remains useful for candidate/E2E tests, but a
// production Builder must supply sealed Claim, Executor and Effect scope plus
// the exact resource owner produced by workerAcquisitionGuard.
type workerExactComposeOptions struct {
	Rollout       config.AgentPlatformRollout
	Identity      ProcessIdentity
	Store         *agentturn.SQLStore
	Claim         workerExactClaimStore
	Executors     *workerExactExecutorRegistry
	Effects       workerExactEffectRouter
	Plugins       []workerPluginRequirement
	ProviderUsage *workerProviderUsageBinding
	Settlement    workerJournalBackedSettlementAuthority
	RuntimeProbe  RuntimeProbe
	Resources     *workerResourceStack
	// CloseTimeout is the acquisition guard's normalized cleanup budget. An
	// exact composition that rejects the transferred owner must start cleanup
	// with that same budget rather than silently replacing it with the default.
	CloseTimeout time.Duration
}

type workerRuntimeScopeSeal struct {
	marker              byte
	claim               workerExactClaimStore
	executors           *workerExactExecutorRegistry
	effects             workerExactEffectRouter
	providerUsage       *workerProviderUsageBinding
	settlementAuthority workerJournalBackedSettlementAuthority
	settlementJournal   *agentturn.ProviderUsageJournal
	settlement          *agentturn.SettlementAuthorityBinding
	admission           *agentturn.AdmissionGate
	plugins             []workerPluginRequirement
	topics              []string
}

func (scope *workerRuntimeScopeSeal) intact(
	store *agentturn.SQLStore,
	worker *agentturn.Worker,
	reconciler *agentturn.Reconciler,
	dispatcher *agentturn.EffectDispatcher,
) bool {
	if scope == nil || scope.marker != 1 || store == nil || worker == nil ||
		reconciler == nil || dispatcher == nil || scope.admission == nil ||
		!scope.admission.Open() {
		return false
	}
	plugins, topics, ok := normalizeWorkerPluginRequirements(scope.plugins)
	if !ok || !equalWorkerPluginRequirements(plugins, scope.plugins) ||
		!equalWorkerStrings(topics, scope.topics) {
		return false
	}
	return scope.claim.intact(store, workerRequirementSnapshots(plugins)) &&
		scope.executors.intact(plugins) && scope.effects.intact(topics) &&
		scope.providerUsage.matchesStore(store, workerRequirementSnapshots(plugins)) &&
		!dependencyMissing(scope.settlementAuthority) &&
		scope.settlementJournal != nil &&
		scope.settlementJournal == scope.providerUsage.journal &&
		scope.settlementJournal.MatchesStore(store) &&
		scope.settlementAuthority.matchesWorkerProviderUsageBinding(scope.providerUsage) &&
		store.MatchesSettlementAuthorityBinding(scope.settlement) &&
		worker.MatchesPluginExecutionLimits(workerRequirementExecutionLimits(plugins)) &&
		worker.MatchesAdmissionGate(scope.admission) &&
		reconciler.MatchesAdmissionGate(scope.admission) &&
		dispatcher.MatchesAdmissionGate(scope.admission)
}

func (scope *workerRuntimeScopeSeal) admissionGate() *agentturn.AdmissionGate {
	if scope == nil {
		return nil
	}
	return scope.admission
}

func composeExactWorker(
	options workerExactComposeOptions,
) (composition *WorkerComposition, resultErr error) {
	resources := options.Resources
	if resources == nil || !resources.isOpen() {
		return nil, errWorkerResourcesInvalid
	}
	closeTimeout := options.CloseTimeout
	if closeTimeout <= 0 {
		closeTimeout = defaultWorkerResourceCloseTimeout
	}
	var admission *agentturn.AdmissionGate
	defer func() {
		panicValue := recover()
		if resultErr != nil || panicValue != nil {
			admission.Close()
			resources.beginClose(closeTimeout)
		}
		if panicValue != nil {
			panic(panicValue)
		}
	}()

	if err := (&options.Rollout).ValidateWorkerRole(); err != nil {
		return nil, fmt.Errorf("invalid agent worker rollout: %w", err)
	}
	intent := workerRoleIntent(options.Rollout)
	if !intent.WorkerEnabled {
		return nil, fmt.Errorf("agent worker role is disabled")
	}
	if options.Store == nil {
		return nil, fmt.Errorf("agent worker requires a database store")
	}
	if options.Identity.WorkerID == "" || options.Identity.BuildDigest == "" {
		return nil, fmt.Errorf("agent worker requires a worker id and build digest")
	}
	if dependencyMissing(options.Settlement) {
		return nil, fmt.Errorf("agent worker requires a settlement authority")
	}
	if dependencyMissing(options.RuntimeProbe) {
		return nil, fmt.Errorf("agent worker is enabled but no runtime probe is registered")
	}
	plugins, topics, ok := normalizeWorkerPluginRequirements(options.Plugins)
	if !ok || !equalWorkerPluginRequirements(plugins, options.Plugins) ||
		!options.Claim.intact(options.Store, workerRequirementSnapshots(plugins)) ||
		!options.Executors.intact(plugins) || !options.Effects.intact(topics) ||
		!options.ProviderUsage.matchesStore(options.Store, workerRequirementSnapshots(plugins)) ||
		!options.Settlement.matchesWorkerProviderUsageBinding(options.ProviderUsage) {
		return nil, errWorkerDependencyPlanInvalid
	}

	settlementBinding, err := options.Store.BindSettlementReviewProviderUsageAuthority(
		options.ProviderUsage.journal, options.Settlement,
	)
	if err != nil {
		return nil, err
	}
	admission = agentturn.NewAdmissionGate()
	if !resources.bindAdmissionGate(admission) {
		return nil, errWorkerResourcesInvalid
	}
	worker, err := agentturn.NewWorker(options.Claim.execution, options.Executors,
		agentturn.WorkerOptions{
			WorkerID: options.Identity.WorkerID, WorkerBuildDigest: options.Identity.BuildDigest,
			PluginLimits:  workerRequirementExecutionLimits(plugins),
			AdmissionGate: admission,
		})
	if err != nil {
		return nil, err
	}
	reconciler, err := agentturn.NewReconciler(options.Store, options.Store, agentturn.ReconcilerOptions{
		ReconcilerID: options.Identity.WorkerID, ReconcilerBuildDigest: options.Identity.BuildDigest,
		AdmissionGate: admission,
	})
	if err != nil {
		return nil, err
	}
	dispatcher, err := agentturn.NewEffectDispatcher(options.Store, options.Effects.deliverer,
		agentturn.EffectDispatcherOptions{
			LeaseOwnerID:  options.Identity.WorkerID,
			Topics:        append([]string(nil), topics...),
			AdmissionGate: admission,
		})
	if err != nil {
		return nil, err
	}
	probe := &sealedRuntimeProbe{delegate: options.RuntimeProbe}
	scope := &workerRuntimeScopeSeal{
		marker: 1, claim: options.Claim, executors: options.Executors, effects: options.Effects,
		providerUsage: options.ProviderUsage, settlementAuthority: options.Settlement,
		settlementJournal: options.ProviderUsage.journal,
		settlement:        settlementBinding, admission: admission,
		plugins: copyWorkerPluginRequirements(plugins), topics: append([]string(nil), topics...),
	}
	composition = &WorkerComposition{
		store: options.Store, worker: worker, reconciler: reconciler, dispatcher: dispatcher,
		resources: resources, probe: probe, runtimeScope: scope,
	}
	components := agentturn.PlatformComponents{
		Store: options.Store, Execution: options.Claim.execution, Reclaim: options.Store,
		Reconcile: options.Store, Outbox: options.Store, Settlement: options.Settlement,
		Worker: worker, Reconciler: reconciler, Dispatcher: dispatcher,
	}
	composition.readiness = agentturn.DeriveReadiness(
		intent, declaredWorkerReadiness(options.Rollout), components,
	)
	if !composition.readiness.Ready {
		return nil, fmt.Errorf("agent worker role is not ready:\n  - %s",
			strings.Join(composition.readiness.Blockers, "\n  - "))
	}
	composition.seal = &workerCompositionSeal{
		marker: 1, store: options.Store, worker: worker, reconciler: reconciler,
		dispatcher: dispatcher, resources: resources, probe: probe, runtimeScope: scope,
	}
	return composition, nil
}

// workerRoleIntent maps only intent owned by this process. Public API,
// Desktop transport and credential admission belong to separate process
// roles; this binary must neither build nor attest them.
func workerRoleIntent(rollout config.AgentPlatformRollout) agentturn.RolloutIntent {
	return agentturn.RolloutIntent{
		WorkerEnabled: rollout.Durable.Worker == config.DurableWorkerOn,
	}
}

// declaredWorkerReadiness deliberately excludes token/device, public API
// stream and Desktop claims. Those declarations may be meaningful to their
// owning processes, but they cannot make this Worker role ready or unready.
func declaredWorkerReadiness(rollout config.AgentPlatformRollout) agentturn.DeclaredReadiness {
	return agentturn.DeclaredReadiness{
		SQLStore:              rollout.Readiness.SQLStore,
		WorkerLeaseFencing:    rollout.Readiness.WorkerLeaseFencing,
		TransactionalOutbox:   rollout.Readiness.TransactionalOutbox,
		ExactlyOnceSettlement: rollout.Readiness.ExactlyOnceSettlement,
	}
}

// RegisteredPlugins reports the executor coverage for logging and diagnostics.
func (registry ExecutorRegistry) RegisteredPlugins() []string {
	plugins := make([]string, 0, len(registry))
	for pluginID := range registry {
		plugins = append(plugins, pluginID)
	}
	sort.Strings(plugins)
	return plugins
}
