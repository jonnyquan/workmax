package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	agentv1 "server/contracts/agent/v1"
	"server/service/agentturn"
	"server/utils/testutil"
)

func TestWorkerExactClaimStoreBindsTheBaseAndOwnsItsScope(t *testing.T) {
	base, err := agentturn.NewSQLStore(testutil.NewTestDB(t))
	if err != nil {
		t.Fatal(err)
	}
	snapshots := []agentv1.EventPluginRef{
		{ID: "workmax.writer", Version: "1.0.0", ReleaseDigest: testWriterPluginDigest},
		{ID: "workmax.workbook", Version: "2.0.0", ReleaseDigest: testWorkbookPluginDigest},
	}
	exact, err := newWorkerExactClaimStore(base, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	snapshots[0].Version = "mutated"
	if !exact.intact(base, []agentv1.EventPluginRef{
		{ID: "workmax.workbook", Version: "2.0.0", ReleaseDigest: testWorkbookPluginDigest},
		{ID: "workmax.writer", Version: "1.0.0", ReleaseDigest: testWriterPluginDigest},
	}) {
		t.Fatal("exact Claim result retained caller mutation or depended on input order")
	}
	other, err := agentturn.NewSQLStore(testutil.NewTestDB(t))
	if err != nil {
		t.Fatal(err)
	}
	if exact.intact(other, []agentv1.EventPluginRef{{
		ID: "workmax.writer", Version: "1.0.0", ReleaseDigest: testWriterPluginDigest,
	}}) {
		t.Fatal("exact Claim result matched another SQLStore or an incomplete scope")
	}
}

func TestWorkerExactEffectRouterHasNoFallbackAndOwnsTopics(t *testing.T) {
	writer := &fakeDeliverer{}
	workbook := &fakeDeliverer{}
	bindings := []workerEffectBinding{
		{Topic: "writer.export", Deliverer: writer},
		{Topic: "workbook.export", Deliverer: workbook},
	}
	exact, err := newWorkerExactEffectRouter(bindings)
	if err != nil {
		t.Fatal(err)
	}
	bindings[0].Topic = "mutated.topic"
	bindings[0].Deliverer = workbook
	if !exact.intact([]string{"workbook.export", "writer.export"}) {
		t.Fatal("exact Effect router retained caller binding mutation")
	}

	if _, err := exact.deliverer.Deliver(context.Background(), agentturn.EffectDelivery{
		Topic: "writer.export",
	}); err != nil {
		t.Fatalf("authorized delivery: %v", err)
	}
	if len(writer.delivered()) != 1 || len(workbook.delivered()) != 0 {
		t.Fatalf("route calls writer=%d workbook=%d, want 1/0",
			len(writer.delivered()), len(workbook.delivered()))
	}
	if _, err := exact.deliverer.Deliver(context.Background(), agentturn.EffectDelivery{
		Topic: "unknown.export",
	}); !errors.Is(err, errWorkerEffectTopicUnauthorized) {
		t.Fatalf("unknown route error = %v, want unauthorized", err)
	}
	if len(writer.delivered()) != 1 || len(workbook.delivered()) != 0 {
		t.Fatal("unknown topic reached a provider")
	}
}

func TestWorkerExactEffectRouterRejectsInvalidBindings(t *testing.T) {
	var typedNil *fakeDeliverer
	for name, bindings := range map[string][]workerEffectBinding{
		"empty": nil,
		"duplicate topic": {
			{Topic: "writer.export", Deliverer: &fakeDeliverer{}},
			{Topic: "writer.export", Deliverer: &fakeDeliverer{}},
		},
		"invalid topic":       {{Topic: " writer.export", Deliverer: &fakeDeliverer{}}},
		"typed nil deliverer": {{Topic: "writer.export", Deliverer: typedNil}},
	} {
		t.Run(name, func(t *testing.T) {
			if router, err := newWorkerExactEffectRouter(bindings); router.marker != 0 ||
				!errors.Is(err, errWorkerEffectScopeInvalid) {
				t.Fatalf("newWorkerExactEffectRouter() = %+v, %v; want invalid scope", router, err)
			}
		})
	}
}

func TestWorkerExactExecutorChecksFullSnapshotBeforeCallingRuntime(t *testing.T) {
	snapshot := agentv1.EventPluginRef{
		ID: "workmax.writer", Version: "1.0.0", ReleaseDigest: testWriterPluginDigest,
	}
	calls := 0
	registry, err := newWorkerExactExecutorRegistry([]workerPluginExecutorBinding{{
		Snapshot: snapshot, EffectTopics: []string{"writer.export"},
		Executor: fakeExecutor{run: func(context.Context, agentturn.ExecutionSession) (agentv1.TurnStatus, error) {
			calls++
			return agentv1.TurnStatusCompleted, nil
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	session := &recordingExecutionSession{turn: agentturn.Turn{Plugin: snapshot}}
	session.turn.Plugin.Version = "1.0.1"
	if _, err := registry.Execute(context.Background(), session); !errors.Is(err, errWorkerExecutorScopeInvalid) {
		t.Fatalf("version mismatch error = %v, want exact scope rejection", err)
	}
	if calls != 0 {
		t.Fatalf("mismatched snapshot called executor %d times", calls)
	}
}

func TestWorkerExactExecutorRejectsUnauthorizedEmitEvenWhenIgnored(t *testing.T) {
	snapshot := agentv1.EventPluginRef{
		ID: "workmax.writer", Version: "1.0.0", ReleaseDigest: testWriterPluginDigest,
	}
	registry, err := newWorkerExactExecutorRegistry([]workerPluginExecutorBinding{{
		Snapshot: snapshot, EffectTopics: []string{"writer.export"},
		Executor: fakeExecutor{run: func(ctx context.Context, session agentturn.ExecutionSession) (agentv1.TurnStatus, error) {
			_, _ = session.Emit(ctx, testScopedOperation("workbook.export"))
			return agentv1.TurnStatusCompleted, nil
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	underlying := &recordingExecutionSession{turn: agentturn.Turn{Plugin: snapshot}}
	status, executeErr := registry.Execute(context.Background(), underlying)
	if !errors.Is(executeErr, errWorkerEffectTopicUnauthorized) || status != "" {
		t.Fatalf("Execute() = %q, %v; want forced unauthorized failure", status, executeErr)
	}
	if len(underlying.operations) != 0 {
		t.Fatalf("unauthorized Emit reached durable session: %+v", underlying.operations)
	}
}

func TestWorkerExactExecutorAllowsDeclaredTopicAndCopiesOperation(t *testing.T) {
	snapshot := agentv1.EventPluginRef{
		ID: "workmax.writer", Version: "1.0.0", ReleaseDigest: testWriterPluginDigest,
	}
	operation := testScopedOperation("writer.export")
	registry, err := newWorkerExactExecutorRegistry([]workerPluginExecutorBinding{{
		Snapshot: snapshot, EffectTopics: []string{"writer.export"},
		Executor: fakeExecutor{run: func(ctx context.Context, session agentturn.ExecutionSession) (agentv1.TurnStatus, error) {
			if _, emitErr := session.Emit(ctx, operation); emitErr != nil {
				return "", emitErr
			}
			return agentv1.TurnStatusCompleted, nil
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	underlying := &recordingExecutionSession{turn: agentturn.Turn{Plugin: snapshot}}
	status, executeErr := registry.Execute(context.Background(), underlying)
	if executeErr != nil || status != agentv1.TurnStatusCompleted || len(underlying.operations) != 1 {
		t.Fatalf("Execute() = %q, %v, operations=%d", status, executeErr, len(underlying.operations))
	}
	operation.Event.Data[0] = 'x'
	operation.Effects[0].Payload[0] = 'x'
	if !json.Valid(underlying.operations[0].Event.Data) ||
		!json.Valid(underlying.operations[0].Effects[0].Payload) {
		t.Fatal("durable Emit retained caller-owned JSON buffers")
	}
}

func TestWorkerExactScopeRejectsTypedNilExecutorDelivererAndSession(t *testing.T) {
	snapshot := agentv1.EventPluginRef{
		ID: "workmax.writer", Version: "1.0.0", ReleaseDigest: testWriterPluginDigest,
	}
	var typedNilExecutor *fakeExecutor
	if registry, err := newWorkerExactExecutorRegistry([]workerPluginExecutorBinding{{
		Snapshot: snapshot, EffectTopics: []string{"writer.export"}, Executor: typedNilExecutor,
	}}); registry != nil || !errors.Is(err, errWorkerExecutorScopeInvalid) {
		t.Fatalf("typed-nil executor registry = %p, %v; want exact-scope rejection", registry, err)
	}

	var typedNilDeliverer *fakeDeliverer
	if router, err := newWorkerExactEffectRouter([]workerEffectBinding{{
		Topic: "writer.export", Deliverer: typedNilDeliverer,
	}}); router.marker != 0 || !errors.Is(err, errWorkerEffectScopeInvalid) {
		t.Fatalf("typed-nil deliverer router = %+v, %v; want exact-scope rejection", router, err)
	}

	calls := 0
	registry, err := newWorkerExactExecutorRegistry([]workerPluginExecutorBinding{{
		Snapshot: snapshot, EffectTopics: []string{"writer.export"},
		Executor: fakeExecutor{run: func(context.Context, agentturn.ExecutionSession) (agentv1.TurnStatus, error) {
			calls++
			return agentv1.TurnStatusCompleted, nil
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var typedNilSession *recordingExecutionSession
	if status, err := registry.Execute(context.Background(), typedNilSession); status != "" || !errors.Is(err, errWorkerExecutorScopeInvalid) {
		t.Fatalf("typed-nil session Execute() = %q, %v; want exact-scope rejection", status, err)
	}
	if calls != 0 {
		t.Fatalf("typed-nil session reached executor %d times", calls)
	}
}

func TestWorkerOperationSnapshotAuthorizesTheSameCapturedValue(t *testing.T) {
	operation := testScopedOperation("writer.export")
	snapshot := snapshotWorkerOperation(operation)

	// Model a caller changing its input at the former check/copy boundary. The
	// policy decision and durable handoff must both observe the earlier owned
	// snapshot, never this later caller-owned value.
	operation.Effects[0].Topic = "workbook.export"
	operation.Effects[0].Payload[0] = 'x'
	authorized, err := snapshot.authorize(map[string]struct{}{"writer.export": {}})
	if err != nil {
		t.Fatalf("authorize captured snapshot: %v", err)
	}
	if authorized.Effects[0].Topic != "writer.export" ||
		!json.Valid(authorized.Effects[0].Payload) {
		t.Fatalf("authorized operation followed caller mutation: %+v", authorized.Effects[0])
	}
}

func TestWorkerEffectScopedSessionClonePressureNeverPersistsUnauthorizedTopic(t *testing.T) {
	const operations = 96
	snapshot := agentv1.EventPluginRef{
		ID: "workmax.writer", Version: "1.0.0", ReleaseDigest: testWriterPluginDigest,
	}
	registry, err := newWorkerExactExecutorRegistry([]workerPluginExecutorBinding{{
		Snapshot: snapshot, EffectTopics: []string{"writer.export"},
		Executor: fakeExecutor{run: func(ctx context.Context, session agentturn.ExecutionSession) (agentv1.TurnStatus, error) {
			for index := 0; index < operations; index++ {
				operation := testScopedOperation("writer.export")
				operation.OperationID = fmt.Sprintf("operation_scope_pressure_%03d", index)
				operation.Effects[0].OutboxID = fmt.Sprintf("outbox_scope_pressure_%03d", index)
				operation.Effects[0].DedupeKey = fmt.Sprintf("dedupe_scope_pressure_%03d", index)
				if _, emitErr := session.Emit(ctx, operation); emitErr != nil {
					return "", emitErr
				}
				// The executor still owns this input after Emit returns. Rewriting
				// it must not rewrite the already-authorized snapshot retained by
				// the durable session boundary.
				operation.Effects[0].Topic = "workbook.export"
			}
			return agentv1.TurnStatusCompleted, nil
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	retained := &recordingExecutionSession{turn: agentturn.Turn{Plugin: snapshot}}
	status, err := registry.Execute(context.Background(), retained)
	if err != nil || status != agentv1.TurnStatusCompleted || len(retained.operations) != operations {
		t.Fatalf("Execute() = %q, %v, retained=%d; want %d authorized operations",
			status, err, len(retained.operations), operations)
	}

	db := testutil.NewTestDB(t)
	store, err := agentturn.NewSQLStore(db)
	if err != nil {
		t.Fatal(err)
	}
	turn := testTurn("scope_clone_pressure")
	turn.Plugin = snapshot
	admit(t, store, turn)
	claimed, err := store.ClaimAttempt(context.Background(), agentturn.ClaimAttemptCommand{
		TurnID: turn.ID, AttemptID: "attempt_scope_clone_pressure", WorkerID: "scope_test",
		WorkerBuildDigest: "sha256:scope-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	for index := range retained.operations {
		operation := retained.operations[index]
		if _, err := store.CommitAttempt(context.Background(), agentturn.CommitAttemptCommand{
			Fence: claimed.Attempt.Fence(), OperationID: operation.OperationID,
			Event: &operation.Event, Effects: operation.Effects,
		}); err != nil {
			t.Fatalf("persist retained operation %d: %v", index, err)
		}
	}
	var total, unauthorized int64
	if err := db.Table(agentturn.SQLEffectOutboxTable).Where("turn_id = ?", turn.ID).Count(&total).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table(agentturn.SQLEffectOutboxTable).
		Where("turn_id = ? AND topic <> ?", turn.ID, "writer.export").Count(&unauthorized).Error; err != nil {
		t.Fatal(err)
	}
	if total != operations || unauthorized != 0 {
		t.Fatalf("persisted effects total=%d unauthorized=%d; want %d/0", total, unauthorized, operations)
	}
}

func TestComposeExactWorkerInstallsScopedClaimExecutorAndEffectPorts(t *testing.T) {
	plugins, _, ok := normalizeWorkerPluginRequirements(validProductionPlanForTest().Plugins)
	if !ok {
		t.Fatal("test Plugin plan is invalid")
	}
	db := testutil.NewTestDB(t)
	store, err := agentturn.NewSQLStore(db)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := newWorkerExactClaimStore(store, workerRequirementSnapshots(plugins))
	if err != nil {
		t.Fatal(err)
	}
	executorCalls := 0
	executorBindings := make([]workerPluginExecutorBinding, 0, len(plugins))
	for _, plugin := range plugins {
		executorBindings = append(executorBindings, workerPluginExecutorBinding{
			Snapshot: plugin.Snapshot, EffectTopics: plugin.EffectTopics,
			Executor: fakeExecutor{run: func(context.Context, agentturn.ExecutionSession) (agentv1.TurnStatus, error) {
				executorCalls++
				return agentv1.TurnStatusCompleted, nil
			}},
		})
	}
	executors, err := newWorkerExactExecutorRegistry(executorBindings)
	if err != nil {
		t.Fatal(err)
	}
	deliverer := &fakeDeliverer{}
	effectBindings := make([]workerEffectBinding, 0)
	_, topics, _ := normalizeWorkerPluginRequirements(plugins)
	for _, topic := range topics {
		effectBindings = append(effectBindings, workerEffectBinding{Topic: topic, Deliverer: deliverer})
	}
	effects, err := newWorkerExactEffectRouter(effectBindings)
	if err != nil {
		t.Fatal(err)
	}
	resources, err := newWorkerResourceStack(nil)
	if err != nil {
		t.Fatal(err)
	}
	providerUsage, settlement := providerUsageAndSettlementForTest(
		t, db, store, plugins, &fakeSettlement{},
	)
	composition, err := composeExactWorker(workerExactComposeOptions{
		Rollout: workerOnRollout(), Identity: validBuildIdentityForTest().identity,
		Store: store, Claim: claim, Executors: executors, Effects: effects,
		Plugins: plugins, ProviderUsage: providerUsage, Settlement: settlement,
		RuntimeProbe: healthyRuntimeProbe{},
		Resources:    resources,
	})
	if err != nil {
		t.Fatalf("composeExactWorker(): %v", err)
	}
	if !workerCompositionReady(composition) || composition.runtimeScope == nil {
		t.Fatal("exact composition did not pass its runtime-scope seal")
	}
	if !composition.worker.MatchesPluginExecutionLimits(workerRequirementExecutionLimits(plugins)) {
		t.Fatal("exact composition did not install the Plugin Snapshot execution limits")
	}

	foreign := testTurn("exact_foreign")
	foreign.Plugin = agentv1.EventPluginRef{
		ID: plugins[0].Snapshot.ID, Version: plugins[0].Snapshot.Version,
		ReleaseDigest: testParityDigest,
	}
	admit(t, store, foreign)
	supported := testTurn("exact_supported")
	supported.Plugin = plugins[0].Snapshot
	admit(t, store, supported)
	result, err := composition.worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce(): %v", err)
	}
	if result.TurnID != supported.ID || executorCalls != 1 {
		t.Fatalf("RunOnce() = %+v, executor calls=%d; want supported Turn only", result, executorCalls)
	}
	var foreignAttempts int64
	if err := db.Table(agentturn.SQLTurnAttemptTable).Where("turn_id = ?", foreign.ID).
		Count(&foreignAttempts).Error; err != nil {
		t.Fatal(err)
	}
	if got := foreignAttempts; got != 0 {
		t.Fatalf("foreign release persisted %d Attempts", got)
	}

	originalExecutionTimeout := composition.runtimeScope.plugins[0].ExecutionTimeout
	composition.runtimeScope.plugins[0].ExecutionTimeout += time.Nanosecond
	if workerCompositionReady(composition) {
		t.Fatal("composition remained ready after its sealed execution limit was mutated")
	}
	composition.runtimeScope.plugins[0].ExecutionTimeout = originalExecutionTimeout
	if !workerCompositionReady(composition) {
		t.Fatal("composition did not recover after restoring its sealed execution limit")
	}

	composition.runtimeScope.topics[0] = "mutated.topic"
	if workerCompositionReady(composition) {
		t.Fatal("composition remained ready after its sealed Topic scope was mutated")
	}
}

func TestComposeExactWorkerPassesTheSameNonEmptyTopicsToDispatcher(t *testing.T) {
	plugins, topics, ok := normalizeWorkerPluginRequirements(validProductionPlanForTest().Plugins)
	if !ok {
		t.Fatal("test Plugin plan is invalid")
	}
	db := testutil.NewTestDB(t)
	store, err := agentturn.NewSQLStore(db)
	if err != nil {
		t.Fatal(err)
	}
	claim, _ := newWorkerExactClaimStore(store, workerRequirementSnapshots(plugins))
	executorBindings := make([]workerPluginExecutorBinding, 0, len(plugins))
	for _, plugin := range plugins {
		executorBindings = append(executorBindings, workerPluginExecutorBinding{
			Snapshot: plugin.Snapshot, EffectTopics: plugin.EffectTopics,
			Executor: fakeExecutor{run: func(context.Context, agentturn.ExecutionSession) (agentv1.TurnStatus, error) {
				return agentv1.TurnStatusCompleted, nil
			}},
		})
	}
	executors, _ := newWorkerExactExecutorRegistry(executorBindings)
	deliverer := &fakeDeliverer{}
	effectBindings := make([]workerEffectBinding, 0, len(topics))
	for _, topic := range topics {
		effectBindings = append(effectBindings, workerEffectBinding{Topic: topic, Deliverer: deliverer})
	}
	effects, _ := newWorkerExactEffectRouter(effectBindings)
	resources, _ := newWorkerResourceStack(nil)
	providerUsage, settlement := providerUsageAndSettlementForTest(
		t, db, store, plugins, &fakeSettlement{},
	)
	composition, err := composeExactWorker(workerExactComposeOptions{
		Rollout: workerOnRollout(), Identity: validBuildIdentityForTest().identity,
		Store: store, Claim: claim, Executors: executors, Effects: effects, Plugins: plugins,
		ProviderUsage: providerUsage, Settlement: settlement,
		RuntimeProbe: healthyRuntimeProbe{}, Resources: resources,
	})
	if err != nil {
		t.Fatal(err)
	}

	foreign := testTurn("foreign_effect")
	foreign.Plugin = plugins[0].Snapshot
	admit(t, store, foreign)
	claimed, err := store.ClaimAttempt(context.Background(), agentturn.ClaimAttemptCommand{
		TurnID: foreign.ID, AttemptID: "attempt_foreign_effect", WorkerID: "fixture",
		WorkerBuildDigest: "sha256:fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitAttempt(context.Background(), agentturn.CommitAttemptCommand{
		Fence: claimed.Attempt.Fence(), OperationID: "operation_foreign_effect",
		Event: &agentturn.EventDraft{Type: "writer.progress", Data: json.RawMessage(`{"ok":true}`)},
		Effects: []agentturn.EffectOutboxDraft{{
			OutboxID: "outbox_foreign_effect", Topic: "foreign.effect", DedupeKey: "foreign_effect",
			Payload: json.RawMessage(`{"ok":true}`), AvailableAt: time.Now().UTC().Add(-time.Minute),
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := composition.dispatcher.DispatchOnce(context.Background()); !errors.Is(err, agentturn.ErrNoClaimableEffects) {
		t.Fatalf("DispatchOnce() error = %v, want no in-scope effects", err)
	}
	if len(deliverer.delivered()) != 0 {
		t.Fatal("out-of-scope Effect reached the exact router")
	}
}

func TestComposeExactWorkerRejectsScopeMismatchAndClosesTransferredOwner(t *testing.T) {
	plugins, topics, ok := normalizeWorkerPluginRequirements(validProductionPlanForTest().Plugins)
	if !ok {
		t.Fatal("test Plugin plan is invalid")
	}
	db := testutil.NewTestDB(t)
	store, _ := agentturn.NewSQLStore(db)
	claim, _ := newWorkerExactClaimStore(store, workerRequirementSnapshots(plugins[:1]))
	executorBindings := make([]workerPluginExecutorBinding, 0, len(plugins))
	for _, plugin := range plugins {
		executorBindings = append(executorBindings, workerPluginExecutorBinding{
			Snapshot: plugin.Snapshot, EffectTopics: plugin.EffectTopics,
			Executor: fakeExecutor{run: func(context.Context, agentturn.ExecutionSession) (agentv1.TurnStatus, error) {
				return agentv1.TurnStatusCompleted, nil
			}},
		})
	}
	executors, _ := newWorkerExactExecutorRegistry(executorBindings)
	effectBindings := make([]workerEffectBinding, 0, len(topics))
	for _, topic := range topics {
		effectBindings = append(effectBindings, workerEffectBinding{Topic: topic, Deliverer: &fakeDeliverer{}})
	}
	effects, _ := newWorkerExactEffectRouter(effectBindings)
	closed := make(chan struct{})
	resources, _ := newWorkerResourceStack([]WorkerResourceCloser{
		WorkerResourceCloseFunc(func(context.Context) error { close(closed); return nil }),
	})
	providerUsage, settlement := providerUsageAndSettlementForTest(
		t, db, store, plugins, &fakeSettlement{},
	)
	composition, composeErr := composeExactWorker(workerExactComposeOptions{
		Rollout: workerOnRollout(), Identity: validBuildIdentityForTest().identity,
		Store: store, Claim: claim, Executors: executors, Effects: effects, Plugins: plugins,
		ProviderUsage: providerUsage, Settlement: settlement,
		RuntimeProbe: healthyRuntimeProbe{}, Resources: resources,
	})
	if composition != nil || !errors.Is(composeErr, errWorkerDependencyPlanInvalid) {
		t.Fatalf("composeExactWorker() = %p, %v; want scope rejection", composition, composeErr)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("scope rejection did not close the transferred resource owner")
	}
}

type recordingExecutionSession struct {
	turn       agentturn.Turn
	attempt    agentturn.TurnAttempt
	operations []agentturn.OperationDraft
}

func (session *recordingExecutionSession) Turn() agentturn.Turn { return session.turn }
func (session *recordingExecutionSession) Attempt() agentturn.TurnAttempt {
	return session.attempt
}
func (session *recordingExecutionSession) CancellationRequested() bool { return false }
func (session *recordingExecutionSession) Emit(
	_ context.Context,
	operation agentturn.OperationDraft,
) (agentturn.CommitAttemptResult, error) {
	session.operations = append(session.operations, operation)
	return agentturn.CommitAttemptResult{}, nil
}

func testScopedOperation(topic string) agentturn.OperationDraft {
	return agentturn.OperationDraft{
		OperationID: "operation_scope",
		Event: agentturn.EventDraft{
			Type: "writer.progress", ResourceRefs: []string{"artifact:one"}, Data: json.RawMessage(`{"ok":true}`),
		},
		Effects: []agentturn.EffectOutboxDraft{{
			OutboxID: "outbox_scope", Topic: topic, DedupeKey: "dedupe_scope",
			Payload: json.RawMessage(`{"ok":true}`), AvailableAt: time.Now().UTC(),
		}},
	}
}
