package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"sort"
	"sync/atomic"

	agentv1 "server/contracts/agent/v1"
	"server/service/agentturn"
)

var (
	errWorkerClaimScopeInvalid       = errors.New("agent-worker exact claim scope is invalid")
	errWorkerExecutorScopeInvalid    = errors.New("agent-worker exact executor scope is invalid")
	errWorkerEffectScopeInvalid      = errors.New("agent-worker exact effect scope is invalid")
	errWorkerEffectTopicUnauthorized = errors.New("agent-worker executor emitted an unauthorized effect topic")
)

// newWorkerExactClaimStore is the only constructor for a production Claim
// result. It binds the concrete scoped view to the same SQLStore used by the
// Reconciler and Effect dispatcher.
func newWorkerExactClaimStore(
	base *agentturn.SQLStore,
	snapshots []agentv1.EventPluginRef,
) (workerExactClaimStore, error) {
	normalized, ok := normalizeWorkerPluginSnapshots(snapshots)
	if base == nil || !ok {
		return workerExactClaimStore{}, errWorkerClaimScopeInvalid
	}
	execution, err := agentturn.NewPluginScopedExecutionStore(base, normalized)
	if err != nil {
		return workerExactClaimStore{}, errWorkerClaimScopeInvalid
	}
	return workerExactClaimStore{
		marker: 1, execution: execution, scopeDigest: workerPluginScopeDigest(normalized),
	}, nil
}

func (scope workerExactClaimStore) intact(
	base *agentturn.SQLStore,
	expected []agentv1.EventPluginRef,
) bool {
	normalized, ok := normalizeWorkerPluginSnapshots(expected)
	return ok && scope.marker == 1 && !dependencyMissing(scope.execution) &&
		scope.scopeDigest == workerPluginScopeDigest(normalized) &&
		scope.execution.Matches(base, normalized)
}

func normalizeWorkerPluginSnapshots(input []agentv1.EventPluginRef) ([]agentv1.EventPluginRef, bool) {
	if len(input) == 0 || len(input) > maxWorkerProductionPlugins {
		return nil, false
	}
	seen := make(map[string]struct{}, len(input))
	output := append([]agentv1.EventPluginRef(nil), input...)
	for _, snapshot := range output {
		if !validWorkerPluginSnapshot(snapshot) {
			return nil, false
		}
		key := workerPluginSnapshotKey(snapshot)
		if _, duplicate := seen[key]; duplicate {
			return nil, false
		}
		seen[key] = struct{}{}
	}
	sort.Slice(output, func(left, right int) bool {
		return workerPluginSnapshotKey(output[left]) < workerPluginSnapshotKey(output[right])
	})
	return output, true
}

func workerPluginScopeDigest(snapshots []agentv1.EventPluginRef) [sha256.Size]byte {
	payload := make([]byte, 0, len(snapshots)*128)
	payload = appendWorkerIntegrityUint64(payload, uint64(len(snapshots)))
	for _, snapshot := range snapshots {
		payload = appendWorkerIntegrityString(payload, snapshot.ID)
		payload = appendWorkerIntegrityString(payload, snapshot.Version)
		payload = appendWorkerIntegrityString(payload, snapshot.ReleaseDigest)
	}
	digest := sha256.Sum256(payload)
	clear(payload)
	return digest
}

func workerRequirementSnapshots(requirements []workerPluginRequirement) []agentv1.EventPluginRef {
	snapshots := make([]agentv1.EventPluginRef, len(requirements))
	for index, requirement := range requirements {
		snapshots[index] = requirement.Snapshot
	}
	return snapshots
}

// workerRequirementExecutionLimits carries the same immutable Plugin release
// identity used by Claim and Executor routing into the execution watchdog.
// There is deliberately no ID-only projection or default policy fallback.
func workerRequirementExecutionLimits(requirements []workerPluginRequirement) []agentturn.PluginExecutionLimits {
	limits := make([]agentturn.PluginExecutionLimits, len(requirements))
	for index, requirement := range requirements {
		limits[index] = agentturn.PluginExecutionLimits{
			Plugin:           requirement.Snapshot,
			ExecutionTimeout: requirement.ExecutionTimeout,
			ProgressTimeout:  requirement.ProgressTimeout,
		}
	}
	return limits
}

func copyWorkerPluginRequirements(input []workerPluginRequirement) []workerPluginRequirement {
	output := make([]workerPluginRequirement, len(input))
	for index, requirement := range input {
		output[index] = requirement
		output[index].EffectTopics = append([]string(nil), requirement.EffectTopics...)
	}
	return output
}

// workerTopicRouter has no default route. Even a forged delivery passed
// directly to Deliver must name one of the exact installed topics.
type workerTopicRouter struct {
	routes map[string]agentturn.Deliverer
}

func (router *workerTopicRouter) Deliver(
	ctx context.Context,
	delivery agentturn.EffectDelivery,
) (agentturn.DeliveryReport, error) {
	if router == nil {
		return agentturn.DeliveryReport{}, errWorkerEffectScopeInvalid
	}
	deliverer, found := router.routes[delivery.Topic]
	if !found || dependencyMissing(deliverer) {
		return agentturn.DeliveryReport{}, errWorkerEffectTopicUnauthorized
	}
	return deliverer.Deliver(ctx, delivery)
}

func (router *workerTopicRouter) matches(topics []string) bool {
	if router == nil || len(router.routes) != len(topics) {
		return false
	}
	for _, topic := range topics {
		if dependencyMissing(router.routes[topic]) {
			return false
		}
	}
	return true
}

func newWorkerExactEffectRouter(bindings []workerEffectBinding) (workerExactEffectRouter, error) {
	if len(bindings) == 0 || len(bindings) > maxWorkerEffectTopics {
		return workerExactEffectRouter{}, errWorkerEffectScopeInvalid
	}
	topics := make([]string, 0, len(bindings))
	routes := make(map[string]agentturn.Deliverer, len(bindings))
	for _, binding := range bindings {
		if dependencyMissing(binding.Deliverer) {
			return workerExactEffectRouter{}, errWorkerEffectScopeInvalid
		}
		topics = append(topics, binding.Topic)
		if _, duplicate := routes[binding.Topic]; duplicate {
			return workerExactEffectRouter{}, errWorkerEffectScopeInvalid
		}
		routes[binding.Topic] = binding.Deliverer
	}
	normalized, ok := normalizeWorkerTopics(topics)
	if !ok || len(normalized) == 0 {
		return workerExactEffectRouter{}, errWorkerEffectScopeInvalid
	}
	router := &workerTopicRouter{routes: routes}
	return workerExactEffectRouter{
		marker: 1, deliverer: router, topics: normalized,
		scopeDigest: workerTopicScopeDigest(normalized),
	}, nil
}

func (scope workerExactEffectRouter) intact(expected []string) bool {
	normalized, ok := normalizeWorkerTopics(expected)
	return ok && len(normalized) > 0 && scope.marker == 1 &&
		!dependencyMissing(scope.deliverer) &&
		equalWorkerStrings(scope.topics, normalized) &&
		scope.scopeDigest == workerTopicScopeDigest(normalized) &&
		scope.deliverer.matches(normalized)
}

func workerTopicScopeDigest(topics []string) [sha256.Size]byte {
	payload := appendWorkerIntegrityStrings(nil, topics)
	digest := sha256.Sum256(payload)
	clear(payload)
	return digest
}

type workerPluginExecutorBinding struct {
	Snapshot     agentv1.EventPluginRef
	EffectTopics []string
	Executor     agentturn.TurnExecutor
}

// workerExactExecutorRegistry dispatches by ID only after proving the complete
// immutable snapshot. Each executor receives a session whose Emit port is
// restricted to that Plugin's declared Topics.
type workerExactExecutorRegistry struct {
	marker          byte
	bindings        map[string]workerPluginExecutorBinding
	integrityDigest [sha256.Size]byte
}

func newWorkerExactExecutorRegistry(
	input []workerPluginExecutorBinding,
) (*workerExactExecutorRegistry, error) {
	if len(input) == 0 || len(input) > maxWorkerProductionPlugins {
		return nil, errWorkerExecutorScopeInvalid
	}
	bindings := make(map[string]workerPluginExecutorBinding, len(input))
	canonical := make([]workerPluginExecutorBinding, 0, len(input))
	for _, binding := range input {
		topics, ok := normalizeWorkerTopics(binding.EffectTopics)
		if !validWorkerPluginSnapshot(binding.Snapshot) || !ok || dependencyMissing(binding.Executor) {
			return nil, errWorkerExecutorScopeInvalid
		}
		if _, duplicate := bindings[binding.Snapshot.ID]; duplicate {
			return nil, errWorkerExecutorScopeInvalid
		}
		binding.EffectTopics = topics
		bindings[binding.Snapshot.ID] = binding
		canonical = append(canonical, binding)
	}
	sort.Slice(canonical, func(left, right int) bool {
		return workerPluginSnapshotKey(canonical[left].Snapshot) <
			workerPluginSnapshotKey(canonical[right].Snapshot)
	})
	return &workerExactExecutorRegistry{
		marker: 1, bindings: bindings, integrityDigest: workerExecutorScopeDigest(canonical),
	}, nil
}

func (registry *workerExactExecutorRegistry) intact(expected []workerPluginRequirement) bool {
	if registry == nil || registry.marker != 1 || len(registry.bindings) != len(expected) {
		return false
	}
	bindings := make([]workerPluginExecutorBinding, 0, len(expected))
	for _, requirement := range expected {
		binding, found := registry.bindings[requirement.Snapshot.ID]
		if !found || binding.Snapshot != requirement.Snapshot ||
			dependencyMissing(binding.Executor) ||
			!equalWorkerStrings(binding.EffectTopics, requirement.EffectTopics) {
			return false
		}
		bindings = append(bindings, binding)
	}
	sort.Slice(bindings, func(left, right int) bool {
		return workerPluginSnapshotKey(bindings[left].Snapshot) <
			workerPluginSnapshotKey(bindings[right].Snapshot)
	})
	return registry.integrityDigest == workerExecutorScopeDigest(bindings)
}

func (registry *workerExactExecutorRegistry) Execute(
	ctx context.Context,
	session agentturn.ExecutionSession,
) (agentv1.TurnStatus, error) {
	if registry == nil || registry.marker != 1 || dependencyMissing(session) {
		return "", errWorkerExecutorScopeInvalid
	}
	turn := session.Turn()
	binding, found := registry.bindings[turn.Plugin.ID]
	if !found || binding.Snapshot != turn.Plugin || dependencyMissing(binding.Executor) {
		return "", errWorkerExecutorScopeInvalid
	}
	scoped := &workerEffectScopedSession{
		delegate: session,
		allowed:  make(map[string]struct{}, len(binding.EffectTopics)),
	}
	for _, topic := range binding.EffectTopics {
		scoped.allowed[topic] = struct{}{}
	}
	status, err := binding.Executor.Execute(ctx, scoped)
	if scoped.violation.Load() {
		return "", errWorkerEffectTopicUnauthorized
	}
	return status, err
}

func workerExecutorScopeDigest(bindings []workerPluginExecutorBinding) [sha256.Size]byte {
	payload := make([]byte, 0, len(bindings)*192)
	payload = appendWorkerIntegrityUint64(payload, uint64(len(bindings)))
	for _, binding := range bindings {
		payload = appendWorkerIntegrityString(payload, binding.Snapshot.ID)
		payload = appendWorkerIntegrityString(payload, binding.Snapshot.Version)
		payload = appendWorkerIntegrityString(payload, binding.Snapshot.ReleaseDigest)
		payload = appendWorkerIntegrityStrings(payload, binding.EffectTopics)
		payload = appendWorkerIntegrityFactory(payload, !dependencyMissing(binding.Executor))
	}
	digest := sha256.Sum256(payload)
	clear(payload)
	return digest
}

type workerEffectScopedSession struct {
	delegate  agentturn.ExecutionSession
	allowed   map[string]struct{}
	violation atomic.Bool
}

func (session *workerEffectScopedSession) Turn() agentturn.Turn {
	return session.delegate.Turn()
}

func (session *workerEffectScopedSession) Attempt() agentturn.TurnAttempt {
	return session.delegate.Attempt()
}

func (session *workerEffectScopedSession) CancellationRequested() bool {
	return session.delegate.CancellationRequested()
}

func (session *workerEffectScopedSession) Emit(
	ctx context.Context,
	operation agentturn.OperationDraft,
) (agentturn.CommitAttemptResult, error) {
	// Copy first, then authorize and submit that same immutable snapshot. An
	// executor must not be able to race a caller-owned Topic between policy
	// evaluation and the durable CommitAttempt call.
	snapshot := snapshotWorkerOperation(operation)
	cloned, err := snapshot.authorize(session.allowed)
	if err != nil {
		session.violation.Store(true)
		return agentturn.CommitAttemptResult{}, err
	}
	return session.delegate.Emit(ctx, cloned)
}

// workerOperationSnapshot makes the policy linearization point explicit: the
// only authorizable value is the owned copy captured before any Topic lookup.
// Keeping the copy opaque prevents a future caller from accidentally checking
// one Operation and handing another to the durable session.
type workerOperationSnapshot struct {
	operation agentturn.OperationDraft
}

func snapshotWorkerOperation(operation agentturn.OperationDraft) workerOperationSnapshot {
	return workerOperationSnapshot{operation: cloneWorkerOperationDraft(operation)}
}

func (snapshot workerOperationSnapshot) authorize(
	allowed map[string]struct{},
) (agentturn.OperationDraft, error) {
	for _, effect := range snapshot.operation.Effects {
		if _, topicAllowed := allowed[effect.Topic]; !topicAllowed {
			return agentturn.OperationDraft{}, errWorkerEffectTopicUnauthorized
		}
	}
	return snapshot.operation, nil
}

func cloneWorkerOperationDraft(operation agentturn.OperationDraft) agentturn.OperationDraft {
	cloned := operation
	cloned.Event.ResourceRefs = append([]string(nil), operation.Event.ResourceRefs...)
	cloned.Event.Data = append([]byte(nil), operation.Event.Data...)
	cloned.Effects = append([]agentturn.EffectOutboxDraft(nil), operation.Effects...)
	for index := range cloned.Effects {
		cloned.Effects[index].Payload = append([]byte(nil), operation.Effects[index].Payload...)
	}
	return cloned
}

var (
	_ agentturn.Deliverer        = (*workerTopicRouter)(nil)
	_ agentturn.TurnExecutor     = (*workerExactExecutorRegistry)(nil)
	_ agentturn.ExecutionSession = (*workerEffectScopedSession)(nil)
)
