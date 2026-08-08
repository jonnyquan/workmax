package agentturn

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	agentv1 "server/contracts/agent/v1"
)

type providerUsageJournalFixture struct {
	db           *gorm.DB
	store        *SQLStore
	clock        *sqlExecutionTestClock
	turn         Turn
	attempt      TurnAttempt
	journal      *ProviderUsageJournal
	recorder     *ProviderUsageRecorder
	release      UsageMeterReleaseRecord
	registration ProviderUsageSourceRegistration
}

func newProviderUsageJournalFixture(t *testing.T, suffix string) providerUsageJournalFixture {
	t.Helper()
	db, store, clock, turns := newSQLClaimNextFixture(t, "provider_usage_"+suffix)
	claimed, err := store.ClaimAttempt(
		context.Background(), executionClaimCommand(turns[0].ID, "attempt_provider_usage_"+suffix),
	)
	if err != nil {
		t.Fatalf("claim attempt: %v", err)
	}
	registration, err := NewProviderUsageSourceRegistration(ProviderUsageSourceRegistrationSpec{
		ProviderKey: "anthropic", ProviderAccountDigest: providerUsageTestDigest("account", suffix),
		SourceKey: "anthropic.messages.result", SourceVersion: "1",
		SourceBuildDigest: providerUsageTestDigest("source-build", suffix),
		UsageSchemaKey:    "anthropic.usage", UsageSchemaVersion: "1",
		SourceSchemaDigest: providerUsageTestDigest("source-schema", suffix),
		VerificationKind:   "sdk_tls", VerificationKeyDigest: providerUsageTestDigest("verification-key", suffix),
		VerificationBuildDigest: providerUsageTestDigest("verification-build", suffix),
	})
	if err != nil {
		t.Fatalf("source registration: %v", err)
	}
	release, err := NewUsageMeterReleaseRecord(UsageMeterReleaseSpec{
		Plugin: turns[0].Plugin, BillingPolicyKey: "writer.turn.v1",
		PricingSnapshotJSON: []byte(`{"creditsPerUSD":200}`),
		MeterKey:            "workmax.writer.turn", MeterVersion: "1",
		MeterBuildDigest: providerUsageTestDigest("meter-build", suffix),
		Sources:          []ProviderUsageSourceRegistration{registration},
	}, clock.Get())
	if err != nil {
		t.Fatalf("meter release: %v", err)
	}
	row, err := usageMeterReleaseToSQLRow(release)
	if err != nil {
		t.Fatalf("meter release SQL row: %v", err)
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("insert meter release: %v", err)
	}
	journal, err := NewProviderUsageJournal(store)
	if err != nil {
		t.Fatalf("new journal: %v", err)
	}
	recorder, err := journal.ScopeRecorder(
		context.Background(), turns[0].Plugin, release.ReleaseID, registration,
	)
	if err != nil {
		t.Fatalf("scope recorder: %v", err)
	}
	if !recorder.MatchesScope(journal, turns[0].Plugin, release.ReleaseID, registration) {
		t.Fatal("scoped Provider recorder does not retain exact provenance")
	}
	return providerUsageJournalFixture{
		db: db, store: store, clock: clock, turn: turns[0], attempt: claimed.Attempt,
		journal: journal, recorder: recorder, release: release, registration: registration,
	}
}

func providerUsageTestDigest(parts ...string) string {
	return providerUsageDigest("provider-usage-test-v1", parts...)
}

func providerUsageAppendCommand(
	fixture providerUsageJournalFixture,
	event string,
) AppendAttestedProviderUsageCommand {
	return AppendAttestedProviderUsageCommand{
		Fence: fixture.attempt.Fence(), ProviderRequestDigest: providerUsageTestDigest("request", event),
		ProviderEventDigest:   providerUsageTestDigest("event", event),
		CanonicalUsageJSON:    []byte(`{"inputTokens":3,"outputTokens":5}`),
		ProviderReceiptDigest: providerUsageTestDigest("receipt", event),
		AttestationDigest:     providerUsageTestDigest("attestation", event),
		ProviderReportedAt:    fixture.clock.Get(),
	}
}

func TestProviderUsageJournalAppendAttestedPersistsAndReplays(t *testing.T) {
	fixture := newProviderUsageJournalFixture(t, "append_replay")
	command := providerUsageAppendCommand(fixture, "one")

	first, err := fixture.recorder.AppendAttested(context.Background(), command)
	if err != nil {
		t.Fatalf("AppendAttested() = %+v, %v", first, err)
	}
	if first.Replay || first.Record.Validate() != nil || first.Record.Plugin != fixture.turn.Plugin ||
		first.Record.sourceRegistration() != fixture.registration || first.Record.MeterReleaseID != fixture.release.ReleaseID {
		t.Fatalf("first journal receipt = %+v", first)
	}
	second, err := fixture.recorder.AppendAttested(context.Background(), command)
	if err != nil || !second.Replay || second.Record.JournalRecordDigest != first.Record.JournalRecordDigest {
		t.Fatalf("exact replay = %+v, %v", second, err)
	}
	if got := executionTableCount(t, fixture.db, SQLProviderUsageJournalTable,
		"provider_event_digest = ?", command.ProviderEventDigest); got != 1 {
		t.Fatalf("journal row count = %d, want 1", got)
	}
}

func TestProviderUsageJournalExactReplaySurvivesRunningLeaseExpiry(t *testing.T) {
	fixture := newProviderUsageJournalFixture(t, "replay_expired_running_lease")
	command := providerUsageAppendCommand(fixture, "replay-expired-running-lease")
	first, err := fixture.recorder.AppendAttested(context.Background(), command)
	if err != nil || first.Replay {
		t.Fatalf("first append = %+v, %v", first, err)
	}
	fixture.clock.Set(fixture.attempt.LeaseExpiresAt.Add(time.Second))
	replay, err := fixture.recorder.AppendAttested(context.Background(), command)
	if err != nil || !replay.Replay || replay.Record.JournalRecordDigest != first.Record.JournalRecordDigest {
		t.Fatalf("expired running-lease replay = %+v, %v", replay, err)
	}
}

func TestProviderUsageJournalConflictingProviderEventFailsClosed(t *testing.T) {
	fixture := newProviderUsageJournalFixture(t, "event_conflict")
	command := providerUsageAppendCommand(fixture, "same-event")
	if _, err := fixture.recorder.AppendAttested(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	conflict := command
	conflict.ProviderRequestDigest = providerUsageTestDigest("different-request")
	conflict.CanonicalUsageJSON = []byte(`{"inputTokens":4,"outputTokens":5}`)
	if _, err := fixture.recorder.AppendAttested(
		context.Background(), conflict,
	); !errors.Is(err, ErrProviderUsageConflict) {
		t.Fatalf("conflicting provider event = %v", err)
	}
	if got := executionTableCount(t, fixture.db, SQLProviderUsageJournalTable, "1 = 1"); got != 1 {
		t.Fatalf("conflict changed journal row count to %d", got)
	}
}

func TestProviderUsageJournalRejectsWrongFencePluginAndExpiredLease(t *testing.T) {
	t.Run("wrong fence", func(t *testing.T) {
		fixture := newProviderUsageJournalFixture(t, "wrong_fence")
		command := providerUsageAppendCommand(fixture, "wrong-fence")
		command.Fence.FencingToken++
		if _, err := fixture.recorder.AppendAttested(
			context.Background(), command,
		); !errors.Is(err, ErrProviderUsageForbidden) {
			t.Fatalf("wrong fence = %v", err)
		}
	})

	t.Run("wrong plugin", func(t *testing.T) {
		fixture := newProviderUsageJournalFixture(t, "wrong_plugin")
		fixture.recorder.plugin = agentv1.EventPluginRef{
			ID: fixture.turn.Plugin.ID, Version: "forged", ReleaseDigest: fixture.turn.Plugin.ReleaseDigest,
		}
		if _, err := fixture.recorder.AppendAttested(
			context.Background(), providerUsageAppendCommand(fixture, "wrong-plugin"),
		); !errors.Is(err, ErrProviderUsageForbidden) {
			t.Fatalf("wrong Plugin = %v", err)
		}
	})

	t.Run("expired lease", func(t *testing.T) {
		fixture := newProviderUsageJournalFixture(t, "expired_lease")
		fixture.clock.Set(fixture.attempt.LeaseExpiresAt.Add(time.Microsecond))
		command := providerUsageAppendCommand(fixture, "expired")
		command.ProviderReportedAt = fixture.attempt.LastHeartbeatAt
		if _, err := fixture.recorder.AppendAttested(
			context.Background(), command,
		); !errors.Is(err, ErrProviderUsageForbidden) {
			t.Fatalf("expired lease = %v", err)
		}
	})
}

func TestProviderUsageJournalTerminalReviewAllowsLateAppendAndOnlyExactPostMeterReplay(t *testing.T) {
	fixture := newProviderUsageJournalFixture(t, "terminal_review")
	authority := newTestSettlementReviewAuthority(t, fixture.db)
	if _, err := fixture.store.BindSettlementReviewProviderUsageAuthority(
		fixture.journal, authority,
	); err != nil {
		t.Fatal(err)
	}
	terminal := CommitAttemptCommand{
		Fence: fixture.attempt.Fence(), OperationID: "operation_provider_usage_terminal",
		TerminalStatus: agentv1.TurnStatusFailed,
		Effects: []EffectOutboxDraft{executionTestEffect(
			"outbox_provider_usage_terminal", "writer.document.publish", "provider-usage-terminal", fixture.clock.Get(),
		)},
	}
	committed, err := fixture.store.CommitAttempt(context.Background(), terminal)
	if err != nil || committed.SettlementReview == nil || committed.SettlementReview.Status != SettlementReviewStatusPending {
		t.Fatalf("terminal Review = %+v, %v", committed, err)
	}
	late := providerUsageAppendCommand(fixture, "late")
	late.ProviderReportedAt = fixture.attempt.LastHeartbeatAt
	first, err := fixture.recorder.AppendAttested(context.Background(), late)
	if err != nil || first.Replay {
		t.Fatalf("pending Review late append = %+v, %v", first, err)
	}
	fixture.clock.Set(fixture.clock.Get().Add(time.Second))
	measured, err := fixture.store.CaptureSettlementReviewUsageEvidence(
		context.Background(), settlementReviewUsageCommand(*committed.SettlementReview),
	)
	if err != nil || measured.Review.Status != SettlementReviewStatusMeteredHeld {
		t.Fatalf("capture terminal Review = %+v, %v", measured, err)
	}
	replay, err := fixture.recorder.AppendAttested(context.Background(), late)
	if err != nil || !replay.Replay {
		t.Fatalf("post-meter exact replay = %+v, %v", replay, err)
	}
	newReceipt := providerUsageAppendCommand(fixture, "post-meter-new")
	newReceipt.ProviderReportedAt = fixture.attempt.LastHeartbeatAt
	if _, err := fixture.recorder.AppendAttested(
		context.Background(), newReceipt,
	); !errors.Is(err, ErrProviderUsageForbidden) {
		t.Fatalf("post-meter new receipt = %v", err)
	}
}

func TestProviderUsageJournalExactReplayRejectsTerminalWithoutProviderReview(t *testing.T) {
	fixture := newProviderUsageJournalFixture(t, "terminal_replay_no_review")
	command := providerUsageAppendCommand(fixture, "terminal-replay-no-review")
	if _, err := fixture.recorder.AppendAttested(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	terminal := CommitAttemptCommand{
		Fence: fixture.attempt.Fence(), OperationID: "operation_provider_usage_replay_no_review",
		TerminalStatus: agentv1.TurnStatusFailed,
	}
	if committed, err := fixture.store.CommitAttempt(context.Background(), terminal); err != nil ||
		committed.SettlementReview != nil {
		t.Fatalf("terminal without provider Review = %+v, %v", committed, err)
	}
	if _, err := fixture.recorder.AppendAttested(
		context.Background(), command,
	); !errors.Is(err, ErrProviderUsageForbidden) {
		t.Fatalf("exact replay without provider Review = %v", err)
	}
}

func TestProviderUsageJournalRejectsTerminalWithoutReview(t *testing.T) {
	fixture := newProviderUsageJournalFixture(t, "terminal_no_review")
	fixture.store.WithSettlementAuthority(newTestSettlementAuthority())
	terminal := CommitAttemptCommand{
		Fence: fixture.attempt.Fence(), OperationID: "operation_provider_usage_no_review",
		TerminalStatus: agentv1.TurnStatusFailed,
	}
	if committed, err := fixture.store.CommitAttempt(context.Background(), terminal); err != nil || committed.SettlementReview != nil {
		t.Fatalf("terminal without Review = %+v, %v", committed, err)
	}
	command := providerUsageAppendCommand(fixture, "terminal-no-review")
	command.ProviderReportedAt = fixture.attempt.LastHeartbeatAt
	if _, err := fixture.recorder.AppendAttested(
		context.Background(), command,
	); !errors.Is(err, ErrProviderUsageForbidden) {
		t.Fatalf("terminal without Review append = %v", err)
	}
}

func TestProviderUsageJournalRejectsHistoricalV1PendingReviewLateAppend(t *testing.T) {
	fixture := newProviderUsageJournalFixture(t, "legacy_v1_review")
	fixture.store.WithSettlementAuthority(newTestSettlementReviewAuthority(t, fixture.db))
	terminal := CommitAttemptCommand{
		Fence: fixture.attempt.Fence(), OperationID: "operation_provider_usage_legacy_v1",
		TerminalStatus: agentv1.TurnStatusFailed,
		Effects: []EffectOutboxDraft{executionTestEffect(
			"outbox_provider_usage_legacy_v1", "writer.document.publish", "legacy-v1", fixture.clock.Get(),
		)},
	}
	committed, err := fixture.store.CommitAttempt(context.Background(), terminal)
	if err != nil || committed.SettlementReview == nil ||
		committed.SettlementReview.RequestDigest != settlementReviewRequestDigestV1(*committed.SettlementReview) {
		t.Fatalf("legacy Review = %+v, %v", committed, err)
	}
	command := providerUsageAppendCommand(fixture, "legacy-v1-late")
	command.ProviderReportedAt = fixture.attempt.LastHeartbeatAt
	if _, err := fixture.recorder.AppendAttested(
		context.Background(), command,
	); !errors.Is(err, ErrProviderUsageForbidden) {
		t.Fatalf("legacy v1 late append = %v", err)
	}
}

func TestProviderUsageJournalDetectsRegistryAndReceiptTamper(t *testing.T) {
	t.Run("registry", func(t *testing.T) {
		fixture := newProviderUsageJournalFixture(t, "registry_tamper")
		if err := fixture.db.Table(SQLUsageMeterReleaseTable).
			Where("release_id = ?", fixture.release.ReleaseID).
			Update("meter_version", "forged").Error; err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.recorder.AppendAttested(
			context.Background(), providerUsageAppendCommand(fixture, "registry-tamper"),
		); !errors.Is(err, ErrStoreIntegrity) {
			t.Fatalf("tampered registry = %v", err)
		}
	})

	t.Run("journal receipt", func(t *testing.T) {
		fixture := newProviderUsageJournalFixture(t, "receipt_tamper")
		command := providerUsageAppendCommand(fixture, "receipt-tamper")
		result, err := fixture.recorder.AppendAttested(context.Background(), command)
		if err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.Table(SQLProviderUsageJournalTable).
			Where("receipt_id = ?", result.Record.ReceiptID).
			Update("journal_record_digest", providerUsageTestDigest("forged-record")).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.recorder.AppendAttested(
			context.Background(), command,
		); !errors.Is(err, ErrStoreIntegrity) {
			t.Fatalf("tampered journal receipt = %v", err)
		}
	})

	t.Run("exact replay cannot mask parent release tamper", func(t *testing.T) {
		fixture := newProviderUsageJournalFixture(t, "replay_parent_tamper")
		command := providerUsageAppendCommand(fixture, "replay-parent-tamper")
		if _, err := fixture.recorder.AppendAttested(context.Background(), command); err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.Table(SQLUsageMeterReleaseTable).
			Where("release_id = ?", fixture.release.ReleaseID).
			Update("meter_version", "forged").Error; err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.recorder.AppendAttested(
			context.Background(), command,
		); !errors.Is(err, ErrStoreIntegrity) {
			t.Fatalf("exact replay with tampered parent release = %v", err)
		}
	})
}

func TestProviderUsageJournalMySQLCanonicalBytesRoundTrip(t *testing.T) {
	settings := mysqlContractSettingsForTest(t)
	database := openMySQLContractDatabase(t, settings)
	mysqlContractPreflight(t, database)
	store := mustSQLStore(t, database)
	now, err := databaseExecutionClock(context.Background(), database)
	if err != nil {
		t.Fatal("read MySQL Provider usage contract clock failed")
	}
	suffix := mysqlContractSuffix(t, "mxusage")
	turn := sqlStoreTestTurn(suffix)
	turn.Plugin = agentv1.EventPluginRef{
		ID: "workmax.contract." + suffix, Version: "1.0.0",
		ReleaseDigest: providerUsageTestDigest("plugin-release", suffix),
	}
	turn.CreatedAt = now.Add(-time.Minute)
	turn.UpdatedAt = turn.CreatedAt
	mysqlContractAssertNamespaceEmpty(t, database, turn)
	admission, err := store.Admit(context.Background(), turn,
		sqlStoreTestDraft(agentv1.EventCoreTurnStatus, `{"status":"queued"}`))
	mysqlContractAssertCreated(t, admission, err)
	claimed, err := store.ClaimAttempt(context.Background(), executionClaimCommand(
		turn.ID, "attempt_"+suffix,
	))
	if err != nil {
		t.Fatal("claim MySQL Provider usage contract Attempt failed")
	}
	cleanupTurn := mysqlContractOwnedCleanup(t, database, turn)
	var ownedReleaseID, ownedReleaseDigest string
	t.Cleanup(func() {
		cleanupTurn()
		if ownedReleaseID == "" {
			return
		}
		deleted := database.Table(SQLUsageMeterReleaseTable).
			Where("release_id = ? AND release_digest = ?", ownedReleaseID, ownedReleaseDigest).
			Delete(&sqlUsageMeterReleaseRow{})
		if deleted.Error != nil || deleted.RowsAffected > 1 {
			t.Error("delete owned MySQL Provider usage MeterRelease failed")
		}
	})

	registration, err := NewProviderUsageSourceRegistration(ProviderUsageSourceRegistrationSpec{
		ProviderKey: "contract.provider", ProviderAccountDigest: providerUsageTestDigest("account", suffix),
		SourceKey: "contract.provider.result", SourceVersion: "1",
		SourceBuildDigest: providerUsageTestDigest("source-build", suffix),
		UsageSchemaKey:    "contract.provider.usage", UsageSchemaVersion: "1",
		SourceSchemaDigest: providerUsageTestDigest("source-schema", suffix),
		VerificationKind:   "signed_receipt", VerificationKeyDigest: providerUsageTestDigest("verification-key", suffix),
		VerificationBuildDigest: providerUsageTestDigest("verification-build", suffix),
	})
	if err != nil {
		t.Fatal("build MySQL Provider usage SourceRegistration failed")
	}
	pricing := []byte(`{"currency":"USD","nested":{"label":"你好","rates":[1,1.25,3e4]}}`)
	release, err := NewUsageMeterReleaseRecord(UsageMeterReleaseSpec{
		Plugin: turn.Plugin, BillingPolicyKey: "contract.provider.v1",
		PricingSnapshotJSON: pricing, MeterKey: "contract.provider.usage", MeterVersion: "1",
		MeterBuildDigest: providerUsageTestDigest("meter-build", suffix),
		Sources:          []ProviderUsageSourceRegistration{registration},
	}, now)
	if err != nil {
		t.Fatal("build MySQL Provider usage MeterRelease failed")
	}
	releaseRow, err := usageMeterReleaseToSQLRow(release)
	ownedReleaseID, ownedReleaseDigest = release.ReleaseID, release.ReleaseDigest
	if err != nil || database.Create(&releaseRow).Error != nil {
		t.Fatal("insert MySQL Provider usage MeterRelease failed")
	}

	var storedPricing, storedRegistry []byte
	if err := database.Raw(`SELECT pricing_snapshot_json, source_registry_json
		FROM w_agent_usage_meter_release WHERE release_id = ?`, release.ReleaseID).
		Row().Scan(&storedPricing, &storedRegistry); err != nil {
		t.Fatal("read MySQL MeterRelease canonical bytes failed")
	}
	if !bytes.Equal(storedPricing, release.PricingSnapshotJSON) ||
		!bytes.Equal(storedRegistry, release.SourceRegistryJSON) {
		t.Fatal("MySQL did not preserve exact MeterRelease canonical bytes")
	}

	journal, err := NewProviderUsageJournal(store)
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := journal.ScopeRecorder(context.Background(), turn.Plugin, release.ReleaseID, registration)
	if err != nil {
		t.Fatal("scope MySQL Provider usage Recorder failed")
	}
	usage := []byte(`{"inputTokens":3,"nested":{"label":"模型","ratios":[1,1.5,2e3]},"outputTokens":5}`)
	command := AppendAttestedProviderUsageCommand{
		Fence: claimed.Attempt.Fence(), ProviderRequestDigest: providerUsageTestDigest("request", suffix),
		ProviderEventDigest: providerUsageTestDigest("event", suffix), CanonicalUsageJSON: usage,
		ProviderReceiptDigest: providerUsageTestDigest("receipt", suffix),
		AttestationDigest:     providerUsageTestDigest("attestation", suffix), ProviderReportedAt: now,
	}
	first, err := recorder.AppendAttested(context.Background(), command)
	if err != nil || first.Replay {
		t.Fatalf("append MySQL Provider usage = replay:%v err:%v", first.Replay, err)
	}
	var storedUsage []byte
	if err := database.Raw(`SELECT provider_usage_json FROM w_agent_provider_usage_journal
		WHERE receipt_id = ?`, first.Record.ReceiptID).Row().Scan(&storedUsage); err != nil {
		t.Fatal("read MySQL Provider usage canonical bytes failed")
	}
	if !bytes.Equal(storedUsage, usage) {
		t.Fatal("MySQL did not preserve exact Provider usage canonical bytes")
	}

	restarted := mustSQLStore(t, database)
	restartedJournal, err := NewProviderUsageJournal(restarted)
	if err != nil {
		t.Fatal(err)
	}
	restartedRecorder, err := restartedJournal.ScopeRecorder(
		context.Background(), turn.Plugin, release.ReleaseID, registration,
	)
	if err != nil {
		t.Fatal("rebuild MySQL Provider usage Recorder failed")
	}
	replay, err := restartedRecorder.AppendAttested(context.Background(), command)
	if err != nil || !replay.Replay || replay.Record.JournalRecordDigest != first.Record.JournalRecordDigest {
		t.Fatalf("MySQL Provider usage restart replay = replay:%v err:%v", replay.Replay, err)
	}
}
