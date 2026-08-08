package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"

	"server/config"
	agentv1 "server/contracts/agent/v1"
	"server/service/agentturn"
)

const (
	testWriterPluginDigest   = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testWorkbookPluginDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testParityDigest         = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	testArtifactDigest       = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
)

type productionFactoryTripwire struct {
	calls int
}

func (tripwire *productionFactoryTripwire) called(t *testing.T) {
	t.Helper()
	tripwire.calls++
	t.Fatal("a production dependency factory ran during static validation")
}

func validProductionSnapshotForTest() workerStartupSnapshot {
	return workerStartupSnapshot{
		source: workerConfigSourceLocal,
		digest: sha256.Sum256([]byte("synthetic P0-038 config fixture")),
		mysql: config.GormMysql{
			GeneralDB: config.GeneralDB{
				Path: "db.internal", Port: "3306", Dbname: "workmax_contract",
				Username: "worker", Password: "synthetic-secret",
			},
		},
		rollout: workerOnRollout(),
	}
}

func validBuildIdentityForTest() workerBuildIdentity {
	return workerBuildIdentity{
		identity: ProcessIdentity{
			WorkerID:    "agent-worker-test-01",
			BuildDigest: testArtifactDigest,
		},
		provenance: workerBuildIdentityFromArtifact,
	}
}

func promotionForTest(snapshot agentv1.EventPluginRef) workerPluginPromotionEvidence {
	return workerPluginPromotionEvidence{
		marker: 1, snapshot: snapshot, ledgerDigest: testParityDigest,
	}
}

func validProductionPlanForTest() workerDependencyPlan {
	writer := agentv1.EventPluginRef{
		ID: "workmax.writer", Version: "1.0.0", ReleaseDigest: testWriterPluginDigest,
	}
	workbook := agentv1.EventPluginRef{
		ID: "workmax.workbook", Version: "2.0.0", ReleaseDigest: testWorkbookPluginDigest,
	}
	return workerDependencyPlan{
		// Deliberately reverse lexical order. The validated snapshot normalizes it.
		Plugins: []workerPluginRequirement{
			{
				Snapshot: workbook, EffectTopics: []string{"workbook.export", "artifact.index"},
				ExecutionTimeout: 30 * time.Minute, ProgressTimeout: 2 * time.Minute,
				Promotion: promotionForTest(workbook),
			},
			{
				Snapshot: writer, EffectTopics: []string{"writer.export", "artifact.index"},
				ExecutionTimeout: 20 * time.Minute, ProgressTimeout: time.Minute,
				Promotion: promotionForTest(writer),
			},
		},
		ProviderUsage: workerProviderUsageJournalRegistryV1,
		Settlement:    workerSettlementCreditsV1,
	}
}

func validProductionCatalogForTest(t *testing.T, plan workerDependencyPlan, tripwire *productionFactoryTripwire) workerDependencyCatalog {
	t.Helper()
	database := workerDatabaseFactory(func(context.Context, workerValidatedDatabaseConfig, workerResourceRegistrar) (*gorm.DB, RuntimeProbe, workerFactoryOwnership, error) {
		tripwire.called(t)
		return nil, nil, workerFactoryRegisteredResources, nil
	})
	providerUsage := workerProviderUsageFactory(func(context.Context, *gorm.DB, *agentturn.SQLStore, []agentv1.EventPluginRef, workerResourceRegistrar) (*workerProviderUsageBinding, RuntimeProbe, workerFactoryOwnership, error) {
		tripwire.called(t)
		return nil, nil, workerFactoryBorrowedOnly, nil
	})
	settlement := workerSettlementFactory(func(context.Context, *gorm.DB, *workerProviderUsageBinding, workerResourceRegistrar) (agentturn.SettlementAuthority, RuntimeProbe, workerFactoryOwnership, error) {
		tripwire.called(t)
		return nil, nil, workerFactoryBorrowedOnly, nil
	})
	pluginFactory := workerExecutorFactory(func(context.Context, *gorm.DB, workerPluginRequirement, workerPluginProviderUsage, workerResourceRegistrar) (agentturn.TurnExecutor, RuntimeProbe, workerFactoryOwnership, error) {
		tripwire.called(t)
		return nil, nil, workerFactoryBorrowedOnly, nil
	})
	effectFactory := workerEffectFactory(func(context.Context, *gorm.DB, string, workerResourceRegistrar) (agentturn.Deliverer, RuntimeProbe, workerFactoryOwnership, error) {
		tripwire.called(t)
		return nil, nil, workerFactoryBorrowedOnly, nil
	})
	plugins := make([]workerPluginRegistration, 0, len(plan.Plugins))
	topics := make(map[string]struct{})
	for _, requirement := range plan.Plugins {
		plugins = append(plugins, workerPluginRegistration{
			Snapshot: requirement.Snapshot, EffectTopics: append([]string(nil), requirement.EffectTopics...),
			Factory: pluginFactory,
		})
		for _, topic := range requirement.EffectTopics {
			topics[topic] = struct{}{}
		}
	}
	effects := make([]workerEffectRegistration, 0, len(topics))
	for topic := range topics {
		effects = append(effects, workerEffectRegistration{Topic: topic, Factory: effectFactory})
	}
	return workerDependencyCatalog{
		Database: database, ProviderUsage: providerUsage, Settlement: settlement,
		Plugins: plugins, Effects: effects,
	}
}

func TestValidateWorkerDependencyPlanCopiesExactCoverageWithoutCallingFactories(t *testing.T) {
	snapshot := validProductionSnapshotForTest()
	build := validBuildIdentityForTest()
	plan := validProductionPlanForTest()
	tripwire := &productionFactoryTripwire{}
	catalog := validProductionCatalogForTest(t, plan, tripwire)

	validated, err := validateWorkerDependencyPlan(snapshot, build, plan, catalog)
	if err != nil {
		t.Fatalf("validateWorkerDependencyPlan() error = %v", err)
	}
	if !validated.intact() {
		t.Fatal("validated production dependency plan did not remain intact")
	}
	if tripwire.calls != 0 {
		t.Fatalf("factory calls = %d, want 0", tripwire.calls)
	}
	if validated.identity != build.identity || validated.configDigest != snapshot.digest {
		t.Fatalf("validated identity/config digest = %+v/%x", validated.identity, validated.configDigest)
	}
	if validated.providerUsage != workerProviderUsageJournalRegistryV1 ||
		validated.catalog.ProviderUsage == nil {
		t.Fatal("validated plan omitted the explicit ProviderUsage dependency")
	}
	snapshot.mysql.Path = "mutated.invalid"
	if strings.Contains(validated.database.settings.address, "mutated") {
		t.Fatal("validated database input retained caller-controlled MySQL state")
	}
	if got := []string{validated.plugins[0].Snapshot.ID, validated.plugins[1].Snapshot.ID}; got[0] != "workmax.workbook" || got[1] != "workmax.writer" {
		t.Fatalf("validated Plugin order = %v", got)
	}
	wantTopics := []string{"artifact.index", "workbook.export", "writer.export"}
	if !equalWorkerStrings(validated.effectTopics, wantTopics) {
		t.Fatalf("validated topics = %v, want %v", validated.effectTopics, wantTopics)
	}

	// The validated snapshot owns deep copies of caller-controlled slices.
	plan.Plugins[0].Snapshot.ID = "mutated"
	plan.Plugins[0].EffectTopics[0] = "mutated"
	catalog.Plugins[0].Snapshot.ID = "mutated"
	catalog.Plugins[0].EffectTopics[0] = "mutated"
	catalog.Effects[0].Topic = "mutated"
	if validated.plugins[0].Snapshot.ID != "workmax.workbook" ||
		validated.plugins[0].EffectTopics[0] != "artifact.index" ||
		validated.catalog.Plugins[0].Snapshot.ID != "workmax.workbook" ||
		validated.catalog.Plugins[0].EffectTopics[0] != "artifact.index" ||
		validated.catalog.Effects[0].Topic != "artifact.index" {
		t.Fatalf("validated plan retained mutable caller slices: %+v", validated)
	}
}

func TestFreezeWorkerMySQLSettingsRejectsNonCanonicalDriverState(t *testing.T) {
	parsed, err := newWorkerMySQLSettings(validProductionSnapshotForTest().mysql)
	if err != nil {
		t.Fatalf("newWorkerMySQLSettings() error = %v", err)
	}
	for name, mutate := range map[string]func(*workerMySQLSettings){
		"file access": func(settings *workerMySQLSettings) {
			settings.driver.AllowAllFiles = true
		},
		"cleartext authentication": func(settings *workerMySQLSettings) {
			settings.driver.AllowCleartextPasswords = true
		},
		"plaintext fallback": func(settings *workerMySQLSettings) {
			settings.driver.AllowFallbackToPlaintext = true
		},
		"multi statements": func(settings *workerMySQLSettings) {
			settings.driver.MultiStatements = true
		},
		"interpolated parameters": func(settings *workerMySQLSettings) {
			settings.driver.InterpolateParams = true
		},
		"named TLS config": func(settings *workerMySQLSettings) {
			settings.driver.TLSConfig = "unsafe"
		},
		"missing safe logger": func(settings *workerMySQLSettings) {
			settings.driver.Logger = nil
		},
		"insecure TLS": func(settings *workerMySQLSettings) {
			settings.driver.TLS.InsecureSkipVerify = true
		},
		"extra session parameter": func(settings *workerMySQLSettings) {
			settings.driver.Params["sql_mode"] = "ANSI"
		},
		"before-connect callback": func(settings *workerMySQLSettings) {
			_ = settings.driver.Apply(drivermysql.BeforeConnect(
				func(context.Context, *drivermysql.Config) error { return nil },
			))
		},
		"time truncation": func(settings *workerMySQLSettings) {
			_ = settings.driver.Apply(drivermysql.TimeTruncate(time.Second))
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := parsed
			candidate.driver = parsed.driver.Clone()
			mutate(&candidate)
			if _, ok := freezeWorkerMySQLSettings(candidate); ok {
				t.Fatal("non-canonical mutable driver state passed the freeze boundary")
			}
		})
	}
}

func TestValidatedWorkerMySQLSettingsCreateIndependentSafeDrivers(t *testing.T) {
	parsed, err := newWorkerMySQLSettings(validProductionSnapshotForTest().mysql)
	if err != nil {
		t.Fatalf("newWorkerMySQLSettings() error = %v", err)
	}
	frozen, ok := freezeWorkerMySQLSettings(parsed)
	if !ok || !frozen.intact() {
		t.Fatal("canonical MySQL settings did not freeze")
	}
	first, ok := frozen.freshDriverConfig()
	if !ok {
		t.Fatal("first freshDriverConfig() rejected intact settings")
	}
	second, ok := frozen.freshDriverConfig()
	if !ok {
		t.Fatal("second freshDriverConfig() rejected intact settings")
	}
	if first == second || first.TLS == second.TLS {
		t.Fatal("fresh driver configurations retained mutable pointer state")
	}

	first.AllowAllFiles = true
	first.AllowFallbackToPlaintext = true
	first.MultiStatements = true
	first.Params["sql_mode"] = "ANSI"
	first.TLS.InsecureSkipVerify = true
	third, ok := frozen.freshDriverConfig()
	if !ok || !frozen.intact() {
		t.Fatal("mutating a returned driver changed frozen settings")
	}
	if third.AllowAllFiles || third.AllowFallbackToPlaintext || third.MultiStatements ||
		third.TLS == nil || third.TLS.InsecureSkipVerify || len(third.Params) != 6 ||
		third.Params["unique_checks"] != "1" ||
		third.Params["transaction_isolation"] != "'READ-COMMITTED'" {
		t.Fatal("fresh driver did not restore the canonical safe policy")
	}
}

func TestValidatedWorkerDependencyPlanDetectsInternalMutation(t *testing.T) {
	for name, mutate := range map[string]func(*validatedWorkerDependencyPlan){
		"marker": func(plan *validatedWorkerDependencyPlan) { plan.marker = 0 },
		"identity": func(plan *validatedWorkerDependencyPlan) {
			plan.identity.WorkerID = "mutated"
		},
		"rollout": func(plan *validatedWorkerDependencyPlan) {
			plan.rollout.Readiness.TransactionalOutbox = false
		},
		"database settings": func(plan *validatedWorkerDependencyPlan) {
			plan.database.settings.address = "mutated.invalid:3306"
		},
		"oversized database secret": func(plan *validatedWorkerDependencyPlan) {
			plan.database.settings.password = strings.Repeat("x", int(maxWorkerConfigBytes)+1)
		},
		"Plugin snapshot": func(plan *validatedWorkerDependencyPlan) {
			plan.plugins[0].Snapshot.Version = "mutated"
		},
		"Plugin topic": func(plan *validatedWorkerDependencyPlan) {
			plan.plugins[0].EffectTopics[0] = "mutated.topic"
		},
		"effect topic": func(plan *validatedWorkerDependencyPlan) {
			plan.effectTopics[0] = "mutated.topic"
		},
		"provider usage kind": func(plan *validatedWorkerDependencyPlan) {
			plan.providerUsage = "mutated"
		},
		"provider usage factory": func(plan *validatedWorkerDependencyPlan) {
			plan.catalog.ProviderUsage = nil
		},
		"Plugin factory": func(plan *validatedWorkerDependencyPlan) {
			plan.catalog.Plugins[0].Factory = nil
		},
		"Effect factory": func(plan *validatedWorkerDependencyPlan) {
			plan.catalog.Effects[0].Factory = nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			requested := validProductionPlanForTest()
			tripwire := &productionFactoryTripwire{}
			validated, err := validateWorkerDependencyPlan(
				validProductionSnapshotForTest(), validBuildIdentityForTest(), requested,
				validProductionCatalogForTest(t, requested, tripwire),
			)
			if err != nil || !validated.intact() {
				t.Fatalf("valid static plan = %+v, %v", validated, err)
			}
			mutate(&validated)
			if validated.intact() {
				t.Fatal("mutated static plan remained intact")
			}
			if tripwire.calls != 0 {
				t.Fatalf("factory calls = %d, want 0", tripwire.calls)
			}
		})
	}
}

func TestValidateWorkerDependencyPlanRejectsUnsafeSnapshotBeforeFactories(t *testing.T) {
	base := validProductionSnapshotForTest()
	for name, mutate := range map[string]func(*workerStartupSnapshot){
		"zero config digest": func(snapshot *workerStartupSnapshot) { snapshot.digest = [sha256.Size]byte{} },
		"unknown source":     func(snapshot *workerStartupSnapshot) { snapshot.source = "unknown" },
		"Worker off": func(snapshot *workerStartupSnapshot) {
			snapshot.rollout.Durable.Worker = config.DurableWorkerOff
		},
		"database check": func(snapshot *workerStartupSnapshot) { snapshot.checkDatabase = true },
		"plaintext exception": func(snapshot *workerStartupSnapshot) {
			snapshot.allowDatabasePlaintext = true
		},
		"invalid MySQL": func(snapshot *workerStartupSnapshot) { snapshot.mysql.Password = "" },
		"invalid MySQL port": func(snapshot *workerStartupSnapshot) {
			snapshot.mysql.Port = "not-a-port"
		},
		"unsafe MySQL option": func(snapshot *workerStartupSnapshot) {
			snapshot.mysql.Config = "multiStatements=true"
		},
		"overclaimed readiness": func(snapshot *workerStartupSnapshot) {
			snapshot.rollout.Readiness.SQLStore = false
		},
	} {
		t.Run(name, func(t *testing.T) {
			snapshot := base
			mutate(&snapshot)
			plan := validProductionPlanForTest()
			tripwire := &productionFactoryTripwire{}
			_, err := validateWorkerDependencyPlan(
				snapshot, validBuildIdentityForTest(), plan, validProductionCatalogForTest(t, plan, tripwire),
			)
			if !errors.Is(err, errWorkerDependencyPlanInvalid) {
				t.Fatalf("error = %v, want plan-invalid classification", err)
			}
			if tripwire.calls != 0 {
				t.Fatalf("factory calls = %d, want 0", tripwire.calls)
			}
		})
	}
}

func TestValidateWorkerDependencyPlanRequiresArtifactIdentity(t *testing.T) {
	snapshot := validProductionSnapshotForTest()
	configDigest := "sha256:" + hex.EncodeToString(snapshot.digest[:])
	for name, mutate := range map[string]func(*workerBuildIdentity){
		"missing provenance": func(build *workerBuildIdentity) { build.provenance = 0 },
		"missing Worker ID":  func(build *workerBuildIdentity) { build.identity.WorkerID = "" },
		"spaced Worker ID":   func(build *workerBuildIdentity) { build.identity.WorkerID = " worker " },
		"control Worker ID":  func(build *workerBuildIdentity) { build.identity.WorkerID = "worker\nsecret" },
		"oversized Worker ID": func(build *workerBuildIdentity) {
			build.identity.WorkerID = strings.Repeat("w", agentturn.MaxWorkerIDBytes+1)
		},
		"missing digest": func(build *workerBuildIdentity) { build.identity.BuildDigest = "" },
		"noncanonical digest": func(build *workerBuildIdentity) {
			build.identity.BuildDigest = "sha256:not-an-artifact-digest"
		},
		"uppercase digest": func(build *workerBuildIdentity) {
			build.identity.BuildDigest = "sha256:" + strings.Repeat("A", sha256.Size*2)
		},
		"zero digest": func(build *workerBuildIdentity) {
			build.identity.BuildDigest = "sha256:" + strings.Repeat("0", sha256.Size*2)
		},
		"config digest substitution": func(build *workerBuildIdentity) {
			build.identity.BuildDigest = configDigest
		},
	} {
		t.Run(name, func(t *testing.T) {
			build := validBuildIdentityForTest()
			mutate(&build)
			plan := validProductionPlanForTest()
			tripwire := &productionFactoryTripwire{}
			_, err := validateWorkerDependencyPlan(
				snapshot, build, plan, validProductionCatalogForTest(t, plan, tripwire),
			)
			if !errors.Is(err, errWorkerDependencyIdentityUnavailable) {
				t.Fatalf("error = %v, want identity-unavailable classification", err)
			}
			if tripwire.calls != 0 {
				t.Fatalf("factory calls = %d, want 0", tripwire.calls)
			}
		})
	}
}

func TestValidateWorkerDependencyPlanRequiresPromotedExactPluginCoverage(t *testing.T) {
	for name, mutate := range map[string]func(*workerDependencyPlan, *workerDependencyCatalog){
		"no Plugins": func(plan *workerDependencyPlan, _ *workerDependencyCatalog) {
			plan.Plugins = nil
		},
		"duplicate Plugin ID": func(plan *workerDependencyPlan, catalog *workerDependencyCatalog) {
			duplicate := plan.Plugins[0]
			duplicate.Snapshot.Version = "9.0.0"
			duplicate.Snapshot.ReleaseDigest = testWriterPluginDigest
			duplicate.Promotion = promotionForTest(duplicate.Snapshot)
			plan.Plugins = append(plan.Plugins, duplicate)
			catalog.Plugins = append(catalog.Plugins, workerPluginRegistration{
				Snapshot: duplicate.Snapshot, EffectTopics: duplicate.EffectTopics,
				Factory: catalog.Plugins[0].Factory,
			})
		},
		"missing parity evidence": func(plan *workerDependencyPlan, _ *workerDependencyCatalog) {
			plan.Plugins[0].Promotion = workerPluginPromotionEvidence{}
		},
		"parity bound to another release": func(plan *workerDependencyPlan, _ *workerDependencyCatalog) {
			plan.Plugins[0].Promotion.snapshot.Version = "old"
		},
		"invalid parity digest": func(plan *workerDependencyPlan, _ *workerDependencyCatalog) {
			plan.Plugins[0].Promotion.ledgerDigest = "sha256:unreviewed"
		},
		"zero parity digest": func(plan *workerDependencyPlan, _ *workerDependencyCatalog) {
			plan.Plugins[0].Promotion.ledgerDigest = "sha256:" + strings.Repeat("0", sha256.Size*2)
		},
		"noncanonical release digest": func(plan *workerDependencyPlan, catalog *workerDependencyCatalog) {
			plan.Plugins[0].Snapshot.ReleaseDigest = "release-without-digest"
			plan.Plugins[0].Promotion = promotionForTest(plan.Plugins[0].Snapshot)
			catalog.Plugins[0].Snapshot = plan.Plugins[0].Snapshot
		},
		"zero release digest": func(plan *workerDependencyPlan, catalog *workerDependencyCatalog) {
			plan.Plugins[0].Snapshot.ReleaseDigest = "sha256:" + strings.Repeat("0", sha256.Size*2)
			plan.Plugins[0].Promotion = promotionForTest(plan.Plugins[0].Snapshot)
			catalog.Plugins[0].Snapshot = plan.Plugins[0].Snapshot
		},
		"zero execution timeout": func(plan *workerDependencyPlan, _ *workerDependencyCatalog) {
			plan.Plugins[0].ExecutionTimeout = 0
		},
		"progress exceeds execution": func(plan *workerDependencyPlan, _ *workerDependencyCatalog) {
			plan.Plugins[0].ProgressTimeout = plan.Plugins[0].ExecutionTimeout + time.Second
		},
		"duplicate effect topic": func(plan *workerDependencyPlan, catalog *workerDependencyCatalog) {
			plan.Plugins[0].EffectTopics = append(plan.Plugins[0].EffectTopics, plan.Plugins[0].EffectTopics[0])
			catalog.Plugins[0].EffectTopics = append(catalog.Plugins[0].EffectTopics, catalog.Plugins[0].EffectTopics[0])
		},
		"effect topic exceeds kernel bound": func(plan *workerDependencyPlan, catalog *workerDependencyCatalog) {
			oversized := strings.Repeat("t", agentturn.MaxEffectTopicBytes+1)
			plan.Plugins[0].EffectTopics[0] = oversized
			catalog.Plugins[0].EffectTopics[0] = oversized
			catalog.Effects[0].Topic = oversized
		},
		"no effect topics": func(plan *workerDependencyPlan, catalog *workerDependencyCatalog) {
			for index := range plan.Plugins {
				plan.Plugins[index].EffectTopics = nil
				catalog.Plugins[index].EffectTopics = nil
			}
			catalog.Effects = nil
		},
		"catalog missing release": func(_ *workerDependencyPlan, catalog *workerDependencyCatalog) {
			catalog.Plugins = catalog.Plugins[1:]
		},
		"catalog adds release": func(_ *workerDependencyPlan, catalog *workerDependencyCatalog) {
			catalog.Plugins = append(catalog.Plugins, catalog.Plugins[0])
		},
		"catalog release mismatch": func(_ *workerDependencyPlan, catalog *workerDependencyCatalog) {
			catalog.Plugins[0].Snapshot.ReleaseDigest = testArtifactDigest
		},
		"catalog topic mismatch": func(_ *workerDependencyPlan, catalog *workerDependencyCatalog) {
			catalog.Plugins[0].EffectTopics[0] = "other.topic"
		},
		"nil executor factory": func(_ *workerDependencyPlan, catalog *workerDependencyCatalog) {
			catalog.Plugins[0].Factory = nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			plan := validProductionPlanForTest()
			tripwire := &productionFactoryTripwire{}
			catalog := validProductionCatalogForTest(t, plan, tripwire)
			mutate(&plan, &catalog)
			_, err := validateWorkerDependencyPlan(
				validProductionSnapshotForTest(), validBuildIdentityForTest(), plan, catalog,
			)
			if !errors.Is(err, errWorkerDependencyPluginUnavailable) {
				t.Fatalf("error = %v, want Plugin-unavailable classification", err)
			}
			if tripwire.calls != 0 {
				t.Fatalf("factory calls = %d, want 0", tripwire.calls)
			}
		})
	}
}

func TestValidateWorkerDependencyPlanRequiresExactEffectCoverage(t *testing.T) {
	for name, mutate := range map[string]func(*workerDependencyCatalog){
		"missing Effect topic": func(catalog *workerDependencyCatalog) {
			catalog.Effects = catalog.Effects[1:]
		},
		"extra Effect topic": func(catalog *workerDependencyCatalog) {
			catalog.Effects = append(catalog.Effects, workerEffectRegistration{
				Topic: "unexpected.topic", Factory: catalog.Effects[0].Factory,
			})
		},
		"duplicate Effect topic": func(catalog *workerDependencyCatalog) {
			catalog.Effects = append(catalog.Effects, catalog.Effects[0])
		},
		"nil Effect factory": func(catalog *workerDependencyCatalog) {
			catalog.Effects[0].Factory = nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			plan := validProductionPlanForTest()
			tripwire := &productionFactoryTripwire{}
			catalog := validProductionCatalogForTest(t, plan, tripwire)
			mutate(&catalog)
			_, err := validateWorkerDependencyPlan(
				validProductionSnapshotForTest(), validBuildIdentityForTest(), plan, catalog,
			)
			if !errors.Is(err, errWorkerDependencyEffectUnavailable) {
				t.Fatalf("error = %v, want %v", err, errWorkerDependencyEffectUnavailable)
			}
			if tripwire.calls != 0 {
				t.Fatalf("factory calls = %d, want 0", tripwire.calls)
			}
		})
	}
}

func TestValidateWorkerDependencyPlanRequiresCompleteFactoryTopology(t *testing.T) {
	for name, mutate := range map[string]struct {
		mutate func(*workerDependencyPlan, *workerDependencyCatalog)
		want   error
	}{
		"database": {
			mutate: func(_ *workerDependencyPlan, catalog *workerDependencyCatalog) { catalog.Database = nil },
			want:   errWorkerDependencyDatabaseUnavailable,
		},
		"provider usage kind": {
			mutate: func(plan *workerDependencyPlan, _ *workerDependencyCatalog) {
				plan.ProviderUsage = "unknown"
			},
			want: errWorkerDependencyProviderUsageUnavailable,
		},
		"provider usage factory": {
			mutate: func(_ *workerDependencyPlan, catalog *workerDependencyCatalog) {
				catalog.ProviderUsage = nil
			},
			want: errWorkerDependencyProviderUsageUnavailable,
		},
		"settlement kind": {
			mutate: func(plan *workerDependencyPlan, _ *workerDependencyCatalog) { plan.Settlement = "unknown" },
			want:   errWorkerDependencySettlementUnavailable,
		},
		"settlement factory": {
			mutate: func(_ *workerDependencyPlan, catalog *workerDependencyCatalog) { catalog.Settlement = nil },
			want:   errWorkerDependencySettlementUnavailable,
		},
	} {
		t.Run(name, func(t *testing.T) {
			plan := validProductionPlanForTest()
			tripwire := &productionFactoryTripwire{}
			catalog := validProductionCatalogForTest(t, plan, tripwire)
			mutate.mutate(&plan, &catalog)
			_, err := validateWorkerDependencyPlan(
				validProductionSnapshotForTest(), validBuildIdentityForTest(), plan, catalog,
			)
			if !errors.Is(err, mutate.want) {
				t.Fatalf("error = %v, want %v", err, mutate.want)
			}
			if tripwire.calls != 0 {
				t.Fatalf("factory calls = %d, want 0", tripwire.calls)
			}
		})
	}
}

func TestValidateWorkerDependencyPlanHasStablePreconditionPriority(t *testing.T) {
	validPlan := validProductionPlanForTest()
	tripwire := &productionFactoryTripwire{}
	validCatalog := validProductionCatalogForTest(t, validPlan, tripwire)

	unsafeSnapshot := validProductionSnapshotForTest()
	unsafeSnapshot.checkDatabase = true
	badIdentity := validBuildIdentityForTest()
	badIdentity.identity.BuildDigest = "SECRET_INVALID_DIGEST"
	badPlan := validProductionPlanForTest()
	badPlan.Plugins[0].Promotion = workerPluginPromotionEvidence{}
	badCatalog := workerDependencyCatalog{}

	for _, test := range []struct {
		name     string
		snapshot workerStartupSnapshot
		identity workerBuildIdentity
		plan     workerDependencyPlan
		catalog  workerDependencyCatalog
		want     error
	}{
		{
			name: "snapshot before every other defect", snapshot: unsafeSnapshot,
			identity: badIdentity, plan: badPlan, catalog: badCatalog,
			want: errWorkerDependencyPlanInvalid,
		},
		{
			name: "identity before plan and topology", snapshot: validProductionSnapshotForTest(),
			identity: badIdentity, plan: badPlan, catalog: badCatalog,
			want: errWorkerDependencyIdentityUnavailable,
		},
		{
			name: "Plugin plan before topology", snapshot: validProductionSnapshotForTest(),
			identity: validBuildIdentityForTest(), plan: badPlan, catalog: badCatalog,
			want: errWorkerDependencyPluginUnavailable,
		},
		{
			name: "database before later topology", snapshot: validProductionSnapshotForTest(),
			identity: validBuildIdentityForTest(), plan: validPlan,
			catalog: func() workerDependencyCatalog {
				copy := validCatalog
				copy.Database, copy.Settlement = nil, nil
				return copy
			}(),
			want: errWorkerDependencyDatabaseUnavailable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := validateWorkerDependencyPlan(test.snapshot, test.identity, test.plan, test.catalog)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			assertErrorOmits(t, err, "SECRET_INVALID_DIGEST")
		})
	}
	if tripwire.calls != 0 {
		t.Fatalf("factory calls = %d, want 0", tripwire.calls)
	}
}

func TestProductionDependencyContractErrorsAreClosedAndSecretFree(t *testing.T) {
	const secret = "SECRET_PROVIDER_RESPONSE db.internal/password"
	plan := validProductionPlanForTest()
	plan.Plugins[0].Snapshot.ID = secret
	tripwire := &productionFactoryTripwire{}
	_, err := validateWorkerDependencyPlan(
		validProductionSnapshotForTest(), validBuildIdentityForTest(), plan,
		validProductionCatalogForTest(t, plan, tripwire),
	)
	if !errors.Is(err, errWorkerDependencyPluginUnavailable) {
		t.Fatalf("error = %v, want Plugin-unavailable classification", err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "db.internal") {
		t.Fatalf("static contract error leaked input: %q", err)
	}
	if tripwire.calls != 0 {
		t.Fatalf("factory calls = %d, want 0", tripwire.calls)
	}
}

func TestUnwiredWorkerCompositionStillFailsClosedAfterStaticContract(t *testing.T) {
	composition, err := unwiredWorkerComposition(context.Background(), validProductionSnapshotForTest())
	if composition != nil || !errors.Is(err, errWorkerDependenciesUnavailable) {
		t.Fatalf("unwiredWorkerComposition() = %p, %v; want nil, unavailable", composition, err)
	}
}
