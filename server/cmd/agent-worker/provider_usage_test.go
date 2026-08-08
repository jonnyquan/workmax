package main

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"gorm.io/gorm"

	agentv1 "server/contracts/agent/v1"
	"server/service/agentturn"
	"server/utils/testutil"
)

func providerUsageSourceForTest(
	t *testing.T,
	provider string,
	accountDigest string,
) agentturn.ProviderUsageSourceRegistration {
	t.Helper()
	source, err := agentturn.NewProviderUsageSourceRegistration(
		agentturn.ProviderUsageSourceRegistrationSpec{
			ProviderKey: provider, ProviderAccountDigest: accountDigest,
			SourceKey: "verified.receipt", SourceVersion: "1",
			SourceBuildDigest: testArtifactDigest, UsageSchemaKey: "usage.tokens",
			UsageSchemaVersion: "1", SourceSchemaDigest: testParityDigest,
			VerificationKind: "signed-receipt", VerificationKeyDigest: testWriterPluginDigest,
			VerificationBuildDigest: testWorkbookPluginDigest,
		},
	)
	if err != nil {
		t.Fatalf("NewProviderUsageSourceRegistration(): %v", err)
	}
	return source
}

func insertUsageMeterReleaseForTest(
	t *testing.T,
	database *gorm.DB,
	release agentturn.UsageMeterReleaseRecord,
) {
	t.Helper()
	if err := database.Table(agentturn.SQLUsageMeterReleaseTable).Create(map[string]any{
		"release_id":              release.ReleaseID,
		"plugin_id":               release.Plugin.ID,
		"plugin_version":          release.Plugin.Version,
		"plugin_release_digest":   release.Plugin.ReleaseDigest,
		"plugin_snapshot_digest":  release.PluginSnapshotDigest,
		"billing_policy_key":      release.BillingPolicyKey,
		"pricing_snapshot_json":   []byte(release.PricingSnapshotJSON),
		"pricing_snapshot_digest": release.PricingSnapshotDigest,
		"meter_key":               release.MeterKey,
		"meter_version":           release.MeterVersion,
		"meter_build_digest":      release.MeterBuildDigest,
		"source_registry_json":    []byte(release.SourceRegistryJSON),
		"source_registry_digest":  release.SourceRegistryDigest,
		"release_digest":          release.ReleaseDigest,
		"created_at":              release.CreatedAt,
	}).Error; err != nil {
		t.Fatalf("insert UsageMeterRelease: %v", err)
	}
}

func newRealProviderUsageBindingForTest(
	t *testing.T,
	database *gorm.DB,
	store *agentturn.SQLStore,
	plugins []workerPluginRequirement,
) *workerProviderUsageBinding {
	t.Helper()
	journal, err := agentturn.NewProviderUsageJournal(store)
	if err != nil {
		t.Fatalf("NewProviderUsageJournal(): %v", err)
	}
	registry := make(workerProviderUsageRecorderRegistry, len(plugins))
	createdAt := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	for _, plugin := range plugins {
		sources := []agentturn.ProviderUsageSourceRegistration{
			providerUsageSourceForTest(t, "provider.primary."+plugin.Snapshot.ID, testArtifactDigest),
			providerUsageSourceForTest(t, "provider.backup."+plugin.Snapshot.ID, testParityDigest),
		}
		release, releaseErr := agentturn.NewUsageMeterReleaseRecord(
			agentturn.UsageMeterReleaseSpec{
				Plugin: plugin.Snapshot, BillingPolicyKey: "credits.v1",
				PricingSnapshotJSON: json.RawMessage(`{"currency":"credits","unitPrice":1}`),
				MeterKey:            "provider.usage", MeterVersion: "1",
				MeterBuildDigest: testArtifactDigest, Sources: sources,
			},
			createdAt,
		)
		if releaseErr != nil {
			t.Fatalf("NewUsageMeterReleaseRecord(%q): %v", plugin.Snapshot.ID, releaseErr)
		}
		insertUsageMeterReleaseForTest(t, database, release)
		registrations := make([]workerProviderUsageRecorderRegistration, 0, len(sources))
		for _, source := range sources {
			recorder, recorderErr := journal.ScopeRecorder(
				context.Background(), plugin.Snapshot, release.ReleaseID, source,
			)
			if recorderErr != nil {
				t.Fatalf("ScopeRecorder(%q): %v", plugin.Snapshot.ID, recorderErr)
			}
			registrations = append(registrations, workerProviderUsageRecorderRegistration{
				MeterReleaseID: release.ReleaseID, Source: source, Recorder: recorder,
			})
		}
		registry[workerPluginSnapshotKey(plugin.Snapshot)] = registrations
	}
	binding, err := newWorkerProviderUsageBinding(
		database, store, journal, workerRequirementSnapshots(plugins), registry,
	)
	if err != nil {
		t.Fatalf("newWorkerProviderUsageBinding(): %v", err)
	}
	return binding
}

func providerUsageRequirementsForSnapshotsForTest(
	plugins []agentv1.EventPluginRef,
) []workerPluginRequirement {
	requirements := make([]workerPluginRequirement, len(plugins))
	for index, plugin := range plugins {
		requirements[index].Snapshot = plugin
	}
	return requirements
}

func rebindProviderUsageRecorderRegistryForTest(
	t *testing.T,
	journal *agentturn.ProviderUsageJournal,
	plugins []agentv1.EventPluginRef,
	input workerProviderUsageRecorderRegistry,
) workerProviderUsageRecorderRegistry {
	t.Helper()
	output := make(workerProviderUsageRecorderRegistry, len(plugins))
	for _, plugin := range plugins {
		key := workerPluginSnapshotKey(plugin)
		registrations := make([]workerProviderUsageRecorderRegistration, 0, len(input[key]))
		for _, registration := range input[key] {
			recorder, err := journal.ScopeRecorder(
				context.Background(), plugin, registration.MeterReleaseID, registration.Source,
			)
			if err != nil {
				t.Fatalf("rebind ScopeRecorder(%q): %v", plugin.ID, err)
			}
			registration.Recorder = recorder
			registrations = append(registrations, registration)
		}
		output[key] = registrations
	}
	return output
}

func newProviderUsageBindingForTest(
	t *testing.T,
) (*gorm.DB, *agentturn.SQLStore, []workerPluginRequirement, *workerProviderUsageBinding) {
	t.Helper()
	database := testutil.NewTestDB(t)
	store, err := agentturn.NewSQLStore(database)
	if err != nil {
		t.Fatal(err)
	}
	plugins, _, ok := normalizeWorkerPluginRequirements(validProductionPlanForTest().Plugins)
	if !ok {
		t.Fatal("test Plugin requirements are invalid")
	}
	return database, store, plugins,
		newRealProviderUsageBindingForTest(t, database, store, plugins)
}

func TestWorkerProviderUsageBindingCopiesAndSealsExactCoverage(t *testing.T) {
	database, store, plugins, binding := newProviderUsageBindingForTest(t)
	want := workerRequirementSnapshots(plugins)
	if !binding.intact(database, store, want) ||
		!equalWorkerPluginSnapshots(binding.plugins, want) {
		t.Fatal("binding lost exact Plugin or Store coverage")
	}

	seenIdentities := make(map[*workerProviderUsageIdentity]struct{}, len(plugins))
	for _, plugin := range plugins {
		scope, scopeErr := newWorkerPluginProviderUsage(binding, plugin.Snapshot)
		if scopeErr != nil || !scope.intact(binding, plugin.Snapshot) {
			t.Fatalf("Plugin scope for %+v = %+v, %v", plugin.Snapshot, scope, scopeErr)
		}
		if len(scope.recorders) != 2 {
			t.Fatalf("Plugin %q recorders = %d, want two exact sources", plugin.Snapshot.ID, len(scope.recorders))
		}
		seenIdentities[scope.identity] = struct{}{}
		other := plugins[0].Snapshot
		if other == plugin.Snapshot {
			other = plugins[1].Snapshot
		}
		if scope.intact(binding, other) {
			t.Fatal("per-Plugin ProviderUsage facade matched another release")
		}
	}
	if len(seenIdentities) != 1 {
		t.Fatal("Plugin facades were not derived from one exact ProviderUsage binding")
	}
	foreign := plugins[0].Snapshot
	foreign.ReleaseDigest = testArtifactDigest
	if scope, scopeErr := newWorkerPluginProviderUsage(binding, foreign); scopeErr == nil ||
		scope.marker != 0 {
		t.Fatalf("foreign release scope = %+v, %v; want rejection", scope, scopeErr)
	}
}

func TestWorkerProviderUsageBindingRejectsMissingTypedNilAndWrongCoverage(t *testing.T) {
	database, store, plugins, valid := newProviderUsageBindingForTest(t)
	otherStore, err := agentturn.NewSQLStore(database)
	if err != nil {
		t.Fatal(err)
	}
	otherJournal, err := agentturn.NewProviderUsageJournal(otherStore)
	if err != nil {
		t.Fatal(err)
	}
	var typedNilJournal *agentturn.ProviderUsageJournal
	var typedNilRecorder *agentturn.ProviderUsageRecorder
	partial := copyWorkerProviderUsageRecorderRegistry(valid.recorders)
	delete(partial, workerPluginSnapshotKey(plugins[0].Snapshot))
	typedNilRegistry := copyWorkerProviderUsageRecorderRegistry(valid.recorders)
	typedNilRegistry[workerPluginSnapshotKey(plugins[0].Snapshot)][0].Recorder = typedNilRecorder
	for name, candidate := range map[string]struct {
		database *gorm.DB
		store    *agentturn.SQLStore
		journal  *agentturn.ProviderUsageJournal
		plugins  []agentv1.EventPluginRef
		registry workerProviderUsageRecorderRegistry
	}{
		"nil database": {store: store, journal: valid.journal, plugins: valid.plugins, registry: valid.recorders},
		"nil store":    {database: database, journal: valid.journal, plugins: valid.plugins, registry: valid.recorders},
		"nil Journal":  {database: database, store: store, plugins: valid.plugins, registry: valid.recorders},
		"typed nil Journal": {
			database: database, store: store, journal: typedNilJournal,
			plugins: valid.plugins, registry: valid.recorders,
		},
		"Journal bound to another Store": {
			database: database, store: store, journal: otherJournal,
			plugins: valid.plugins, registry: valid.recorders,
		},
		"partial Plugin coverage": {
			database: database, store: store, journal: valid.journal,
			plugins: valid.plugins, registry: partial,
		},
		"typed nil scoped Recorder": {
			database: database, store: store, journal: valid.journal,
			plugins: valid.plugins, registry: typedNilRegistry,
		},
		"empty coverage": {database: database, store: store, journal: valid.journal},
	} {
		t.Run(name, func(t *testing.T) {
			binding, bindErr := newWorkerProviderUsageBinding(
				candidate.database, candidate.store, candidate.journal,
				candidate.plugins, candidate.registry,
			)
			if binding != nil || bindErr == nil {
				t.Fatalf("binding = %p, %v; want rejection", binding, bindErr)
			}
		})
	}
}

func TestWorkerProviderUsageBindingAndPluginFacadeDetectTampering(t *testing.T) {
	for name, mutate := range map[string]func(*workerProviderUsageBinding){
		"marker": func(binding *workerProviderUsageBinding) { binding.marker = 0 },
		"database": func(binding *workerProviderUsageBinding) {
			binding.database = testutil.NewTestDB(t)
		},
		"store": func(binding *workerProviderUsageBinding) {
			binding.store, _ = agentturn.NewSQLStore(testutil.NewTestDB(t))
		},
		"Journal": func(binding *workerProviderUsageBinding) {
			binding.journal, _ = agentturn.NewProviderUsageJournal(binding.store)
		},
		"Plugin coverage": func(binding *workerProviderUsageBinding) {
			binding.plugins[0].Version = "mutated"
		},
		"Recorder": func(binding *workerProviderUsageBinding) {
			binding.recorders[workerPluginSnapshotKey(binding.plugins[0])][0].Recorder = nil
		},
		"source registration": func(binding *workerProviderUsageBinding) {
			binding.recorders[workerPluginSnapshotKey(binding.plugins[0])][0].Source.ProviderKey = "mutated"
		},
		"scope digest": func(binding *workerProviderUsageBinding) { binding.scopeDigest[0]++ },
		"identity":     func(binding *workerProviderUsageBinding) { binding.identity.marker = 0 },
		"seal":         func(binding *workerProviderUsageBinding) { binding.seal = nil },
		"sealed Recorder registry": func(binding *workerProviderUsageBinding) {
			binding.seal.recorders[workerPluginSnapshotKey(binding.plugins[0])][0].Recorder = nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			database, store, plugins, binding := newProviderUsageBindingForTest(t)
			scope, err := newWorkerPluginProviderUsage(binding, plugins[0].Snapshot)
			if err != nil {
				t.Fatal(err)
			}
			mutate(binding)
			if binding.intact(database, store, workerRequirementSnapshots(plugins)) ||
				scope.intact(binding, plugins[0].Snapshot) {
				t.Fatal("tampered ProviderUsage binding or derived facade remained intact")
			}
		})
	}
}

func TestWorkerPluginProviderUsageShapeContainsNoRawWholeJournal(t *testing.T) {
	scopeType := reflect.TypeOf(workerPluginProviderUsage{})
	for index := 0; index < scopeType.NumField(); index++ {
		fieldType := scopeType.Field(index).Type
		if fieldType == reflect.TypeOf((*workerProviderUsageBinding)(nil)) ||
			fieldType == reflect.TypeOf((*gorm.DB)(nil)) ||
			fieldType == reflect.TypeOf((*agentturn.SQLStore)(nil)) ||
			fieldType == reflect.TypeOf((*agentturn.ProviderUsageJournal)(nil)) {
			t.Fatalf("Plugin ProviderUsage facade field %q exposes raw whole-Journal state", scopeType.Field(index).Name)
		}
	}
}
