package migrations

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"gorm.io/gorm"

	"server/utils/testutil"
)

const agentProviderUsageJournalMigrationFile = "20260671_create_agent_provider_usage_journal.sql"

func TestAgentProviderUsageJournalMigrationPinsNoFabricationGuard(t *testing.T) {
	sql := readAgentProviderUsageJournalMigration(t)
	normalized := normalizeSQL(sql)

	guard := strings.Index(normalized, "create temporary table `_w_agent_provider_usage_empty_guard`")
	firstMutation := strings.Index(normalized, "create table if not exists `w_agent_usage_meter_release`")
	if guard < 0 || firstMutation < 0 || guard >= firstMutation {
		t.Fatal("Provider journal no-fabrication guard must precede every persistent schema mutation")
	}
	for _, want := range []string{
		"check (`incompatible_rows` = 0)",
		"select count(*) from `w_agent_turn_settlement_usage_evidence`",
		"select count(*) from `w_agent_turn_settlement_review_resolution`",
		"drop temporary table `_w_agent_provider_usage_empty_guard`",
	} {
		assertSQLContains(t, normalized, want)
	}
	if strings.Contains(normalized, "update `w_agent_turn_settlement_usage_evidence`") ||
		strings.Contains(normalized, "update `w_agent_turn_settlement_review_resolution`") ||
		strings.Contains(normalized, "insert into `w_agent_provider_usage_journal` select") ||
		strings.Contains(normalized, "insert into `w_agent_turn_settlement_usage_evidence_source` select") {
		t.Fatal("P0-045 migration must not fabricate Provider or Evidence provenance")
	}
}

func TestAgentProviderUsageJournalMigrationPinsRegistryJournalAndSources(t *testing.T) {
	sql := readAgentProviderUsageJournalMigration(t)
	if count := len(regexp.MustCompile("(?i)unique key `[^`]+`").FindAllString(sql, -1)); count != 9 {
		t.Fatalf("P0-045 unique-index additions = %d, want 9 (runtime total 31)", count)
	}
	if count := len(regexp.MustCompile("(?i)foreign key ").FindAllString(sql, -1)); count != 5 {
		t.Fatalf("P0-045 RESTRICT foreign-key additions = %d, want 5 (runtime total 17)", count)
	}
	for _, table := range []string{
		"w_agent_usage_meter_release",
		"w_agent_provider_usage_journal",
		"w_agent_turn_settlement_usage_evidence_source",
	} {
		if count := strings.Count(normalizeSQL(sql), "create table if not exists `"+table+"`"); count != 1 {
			t.Fatalf("%s: got %d idempotent CREATE statements, want 1", table, count)
		}
	}

	releaseDDL := createTableDDL(t, sql, "w_agent_usage_meter_release")
	for _, want := range []string{
		"`release_id` varchar(64) character set ascii collate ascii_bin not null",
		"`plugin_id` varchar(512) character set utf8mb4 collate utf8mb4_bin not null",
		"`plugin_snapshot_digest` varchar(128) character set ascii collate ascii_bin not null",
		"`pricing_snapshot_json` mediumblob not null",
		"`pricing_snapshot_digest` varchar(128) character set ascii collate ascii_bin not null",
		"`source_registry_json` mediumblob not null",
		"`source_registry_digest` varchar(128) character set ascii collate ascii_bin not null",
		"unique key `uk_w_agent_usage_meter_release_release_id` (`release_id`)",
		"unique key `uk_w_agent_usage_meter_release_plugin_snapshot` (`plugin_snapshot_digest`)",
		"unique key `uk_w_agent_usage_meter_release_digest` (`release_digest`)",
		"octet_length(`pricing_snapshot_json`) between 1 and 65536",
		"json_valid(convert(`pricing_snapshot_json` using utf8mb4))",
		"convert(convert(`pricing_snapshot_json` using utf8mb4) using binary) = `pricing_snapshot_json`",
		"octet_length(`source_registry_json`) between 1 and 65536",
		"json_valid(convert(`source_registry_json` using utf8mb4))",
		"convert(convert(`source_registry_json` using utf8mb4) using binary) = `source_registry_json`",
		") engine=innodb",
	} {
		assertSQLContains(t, releaseDDL, want)
	}

	journalDDL := createTableDDL(t, sql, "w_agent_provider_usage_journal")
	for _, want := range []string{
		"`receipt_id` varchar(64) character set ascii collate ascii_bin not null",
		"`turn_id` varchar(256) character set utf8mb4 collate utf8mb4_bin not null",
		"`plugin_id` varchar(512) character set utf8mb4 collate utf8mb4_bin not null",
		"`provider_request_digest` varchar(128) character set ascii collate ascii_bin not null",
		"`source_key` varchar(256) character set ascii collate ascii_bin not null",
		"`usage_schema_key` varchar(256) character set ascii collate ascii_bin not null",
		"`canonical_usage_digest` varchar(128) character set ascii collate ascii_bin not null",
		"`verification_kind` varchar(32) character set ascii collate ascii_bin not null",
		"`attestation_digest` varchar(128) character set ascii collate ascii_bin not null",
		"`journal_record_digest` varchar(128) character set ascii collate ascii_bin not null",
		"`provider_usage_json` mediumblob not null",
		"unique key `uk_w_agent_provider_usage_journal_receipt_id` (`receipt_id`)",
		"unique key `uk_w_agent_provider_usage_journal_provider_event` (`provider_key`, `provider_account_digest`, `provider_event_digest`)",
		"unique key `uk_w_agent_provider_usage_journal_source_binding` (`receipt_id`, `turn_id`, `meter_release_id`, `canonical_usage_digest`, `provider_receipt_digest`, `journal_record_digest`)",
		"foreign key (`turn_id`, `attempt_id`, `fencing_token`) references `w_agent_turn_attempt` (`turn_id`, `attempt_id`, `fencing_token`) on delete restrict on update restrict",
		"foreign key (`meter_release_id`) references `w_agent_usage_meter_release` (`release_id`) on delete restrict on update restrict",
		"octet_length(`provider_usage_json`) between 1 and 65536",
		"json_valid(convert(`provider_usage_json` using utf8mb4))",
		"convert(convert(`provider_usage_json` using utf8mb4) using binary) = `provider_usage_json`",
	} {
		assertSQLContains(t, journalDDL, want)
	}

	sourceDDL := createTableDDL(t, sql, "w_agent_turn_settlement_usage_evidence_source")
	for _, want := range []string{
		"unique key `uk_w_agent_turn_settlement_usage_evidence_source_ordinal` (`evidence_id`, `ordinal`)",
		"unique key `uk_w_agent_turn_settlement_usage_evidence_source_receipt` (`receipt_id`)",
		"`ordinal` < `source_receipt_count`",
		"foreign key (`evidence_id`, `review_id`, `turn_id`, `settlement_key`, `review_request_digest`, `meter_release_id`, `usage_source_digest`, `evidence_digest`, `source_receipt_count`) references `w_agent_turn_settlement_usage_evidence` (`evidence_id`, `review_id`, `turn_id`, `settlement_key`, `review_request_digest`, `meter_release_id`, `usage_source_digest`, `evidence_digest`, `source_receipt_count`) on delete restrict on update restrict",
		"foreign key (`receipt_id`, `turn_id`, `meter_release_id`, `canonical_usage_digest`, `provider_receipt_digest`, `journal_record_digest`) references `w_agent_provider_usage_journal` (`receipt_id`, `turn_id`, `meter_release_id`, `canonical_usage_digest`, `provider_receipt_digest`, `journal_record_digest`) on delete restrict on update restrict",
	} {
		assertSQLContains(t, sourceDDL, want)
	}

	namePattern := regexp.MustCompile("(?i)(?:constraint|(?:unique )?key) `([^`]+)`")
	for _, match := range namePattern.FindAllStringSubmatch(sql, -1) {
		if len(match[1]) > 64 {
			t.Fatalf("MySQL identifier %q is %d bytes, want <= 64", match[1], len(match[1]))
		}
	}
}

func TestAgentProviderUsageJournalMigrationPinsReviewAndEvidenceProvenance(t *testing.T) {
	normalized := normalizeSQL(readAgentProviderUsageJournalMigration(t))
	for _, want := range []string{
		"alter table `w_agent_turn_settlement_review` add column `prior_provider_usage_count` bigint unsigned not null default 0",
		"check (`reason` in ( 'usage_unknown', 'completed_usage_unmeasured', 'terminal_usage_unmeasured' ))",
		"check (`source` in ( 'executor_release', 'reconcile_release', 'executor_completion', 'executor_terminal', 'reconcile_terminal' ))",
		"`source` in ('executor_completion', 'executor_terminal', 'reconcile_terminal')",
		"`prior_provider_usage_count` = 0",
		"`source` = 'executor_terminal' and `reason` = 'terminal_usage_unmeasured' and `terminal_status` in ('stopped', 'failed', 'timeout')",
		"`source` = 'reconcile_terminal' and `reason` = 'terminal_usage_unmeasured' and `terminal_status` in ('stopped', 'failed', 'timeout')",
		"alter table `w_agent_turn_settlement_usage_evidence` add column `meter_release_id` varchar(64) character set ascii collate ascii_bin not null",
		"add column `source_receipt_count` smallint unsigned not null",
		"add unique key `uk_w_agent_turn_settlement_usage_evidence_provenance` (`evidence_id`, `review_id`, `turn_id`, `settlement_key`, `review_request_digest`, `meter_release_id`, `usage_source_digest`, `evidence_digest`, `source_receipt_count`)",
		"foreign key (`meter_release_id`) references `w_agent_usage_meter_release` (`release_id`) on delete restrict on update restrict",
	} {
		assertSQLContains(t, normalized, want)
	}
}

func TestAgentProviderUsageJournalSQLiteMirrorEnforcesExactProvenance(t *testing.T) {
	db := testutil.NewTestDB(t)
	for _, index := range []string{
		"uk_w_agent_usage_meter_release_release_id",
		"uk_w_agent_usage_meter_release_plugin_snapshot",
		"uk_w_agent_usage_meter_release_digest",
		"uk_w_agent_provider_usage_journal_receipt_id",
		"uk_w_agent_provider_usage_journal_provider_event",
		"uk_w_agent_provider_usage_journal_source_binding",
		"uk_w_agent_turn_settlement_usage_evidence_provenance",
		"uk_w_agent_turn_settlement_usage_evidence_source_ordinal",
		"uk_w_agent_turn_settlement_usage_evidence_source_receipt",
	} {
		var count int64
		if err := db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, index).
			Scan(&count).Error; err != nil || count != 1 {
			t.Fatalf("SQLite mirror index %s count = %d, err = %v, want 1", index, count, err)
		}
	}

	insertSettlementReviewTurn(t, db, "turn_provider_1", "idem_provider_1")
	insertSettlementReviewExecutorBinding(t, db, "turn_provider_1", "attempt_provider_1", "operation_provider_1")
	release := validUsageMeterReleaseFixture("meter_release_provider_1")
	if err := insertUsageMeterRelease(db, release); err != nil {
		t.Fatalf("insert Meter Release: %v", err)
	}
	journal := validProviderUsageJournalFixture(
		"provider_receipt_1", "turn_provider_1", "attempt_provider_1", release.releaseID,
	)
	if err := insertProviderUsageJournal(db, journal); err != nil {
		t.Fatalf("insert Provider usage journal receipt: %v", err)
	}
	if err := insertSettlementReview(db, settlementReviewFixture{
		reviewID: "review_provider_1", turnID: "turn_provider_1",
		settlementKey: "settlement_provider_1", requestDigest: "sha256:request_provider_1",
		source: "reconcile_release", terminalStatus: "failed", fencingToken: 1,
		priorEffectCount: 1,
	}); err != nil {
		t.Fatalf("insert Provider Evidence Review: %v", err)
	}
	evidence := validSettlementUsageEvidenceFixture(
		"evidence_provider_1", "review_provider_1", "turn_provider_1",
		"settlement_provider_1", "sha256:request_provider_1",
	)
	evidence.meterReleaseID = release.releaseID
	evidence.sourceReceiptCount = 2
	if err := insertSettlementUsageEvidence(db, evidence); err != nil {
		t.Fatalf("insert Provider-backed Evidence: %v", err)
	}
	source := providerUsageEvidenceSourceFixture{
		evidenceID: evidence.evidenceID, ordinal: 0,
		reviewID: evidence.reviewID, turnID: evidence.turnID,
		settlementKey: evidence.settlementKey, reviewRequestDigest: evidence.reviewRequestDigest,
		meterReleaseID: release.releaseID, usageSourceDigest: evidence.usageSourceDigest,
		evidenceDigest: evidence.evidenceDigest, sourceReceiptCount: evidence.sourceReceiptCount,
		receiptID: journal.receiptID, sourceRegistrationDigest: journal.sourceRegistrationDigest,
		sourceSchemaDigest: journal.sourceSchemaDigest, canonicalUsageDigest: journal.canonicalUsageDigest,
		providerReceiptDigest: journal.providerReceiptDigest, journalRecordDigest: journal.journalRecordDigest,
		createdAt: "2026-08-04 00:00:03.000000",
	}
	if err := insertProviderUsageEvidenceSource(db, source); err != nil {
		t.Fatalf("insert exact Evidence Source: %v", err)
	}

	duplicateReceipt := source
	duplicateReceipt.ordinal = 1
	if err := insertProviderUsageEvidenceSource(db, duplicateReceipt); err == nil {
		t.Fatal("one Provider receipt consumed by two Evidence Source rows must fail")
	}
	tamperedJournal := source
	tamperedJournal.receiptID = "provider_receipt_unknown"
	if err := insertProviderUsageEvidenceSource(db, tamperedJournal); err == nil {
		t.Fatal("Evidence Source outside the exact Journal binding must fail")
	}
	tamperedEvidence := source
	tamperedEvidence.evidenceDigest = "sha256:tampered_evidence"
	if err := insertProviderUsageEvidenceSource(db, tamperedEvidence); err == nil {
		t.Fatal("Evidence Source outside the exact Evidence provenance must fail")
	}
	if err := db.Exec(`DELETE FROM w_agent_provider_usage_journal WHERE receipt_id = ?`, journal.receiptID).Error; err == nil {
		t.Fatal("deleting a consumed Provider receipt must be RESTRICTed")
	}
	if err := db.Exec(`UPDATE w_agent_turn_settlement_usage_evidence SET source_receipt_count = 1
		WHERE evidence_id = ?`, evidence.evidenceID).Error; err == nil {
		t.Fatal("changing consumed Evidence provenance must be RESTRICTed")
	}
	if err := db.Exec(`DELETE FROM w_agent_usage_meter_release WHERE release_id = ?`, release.releaseID).Error; err == nil {
		t.Fatal("deleting a referenced Meter Release must be RESTRICTed")
	}
}

func TestAgentProviderUsageJournalSQLiteMirrorRejectsReplayAndTampering(t *testing.T) {
	db := testutil.NewTestDB(t)
	insertSettlementReviewTurn(t, db, "turn_journal_replay", "idem_journal_replay")
	insertSettlementReviewExecutorBinding(t, db, "turn_journal_replay", "attempt_journal_replay", "operation_journal_replay")
	release := validUsageMeterReleaseFixture("meter_release_replay")
	if err := insertUsageMeterRelease(db, release); err != nil {
		t.Fatal(err)
	}
	valid := validProviderUsageJournalFixture(
		"provider_receipt_replay", "turn_journal_replay", "attempt_journal_replay", release.releaseID,
	)
	if err := insertProviderUsageJournal(db, valid); err != nil {
		t.Fatal(err)
	}

	duplicateReceipt := valid
	duplicateReceipt.providerEventDigest = "sha256:provider_event_other"
	if err := insertProviderUsageJournal(db, duplicateReceipt); err == nil {
		t.Fatal("duplicate server receipt_id must fail")
	}
	duplicateProviderEvent := valid
	duplicateProviderEvent.receiptID = "provider_receipt_other"
	duplicateProviderEvent.journalRecordDigest = "sha256:journal_other"
	if err := insertProviderUsageJournal(db, duplicateProviderEvent); err == nil {
		t.Fatal("duplicate Provider account/event identity must fail")
	}
	wrongAttempt := valid
	wrongAttempt.receiptID = "provider_receipt_wrong_attempt"
	wrongAttempt.providerEventDigest = "sha256:provider_event_wrong_attempt"
	wrongAttempt.attemptID = "attempt_unknown"
	wrongAttempt.journalRecordDigest = "sha256:journal_wrong_attempt"
	if err := insertProviderUsageJournal(db, wrongAttempt); err == nil {
		t.Fatal("Journal receipt outside the exact Attempt fence must fail")
	}
	wrongRelease := valid
	wrongRelease.receiptID = "provider_receipt_wrong_release"
	wrongRelease.providerEventDigest = "sha256:provider_event_wrong_release"
	wrongRelease.meterReleaseID = "meter_release_unknown"
	wrongRelease.journalRecordDigest = "sha256:journal_wrong_release"
	if err := insertProviderUsageJournal(db, wrongRelease); err == nil {
		t.Fatal("Journal receipt outside the immutable Meter Release must fail")
	}
	oversized := valid
	oversized.receiptID = "provider_receipt_oversized"
	oversized.providerEventDigest = "sha256:provider_event_oversized"
	oversized.journalRecordDigest = "sha256:journal_oversized"
	oversized.providerUsageJSON = `{"usage":"` + strings.Repeat("u", 65536) + `"}`
	if err := insertProviderUsageJournal(db, oversized); err == nil {
		t.Fatal("Provider canonical usage payload above 64 KiB must fail")
	}
}

func TestAgentProviderUsageJournalSQLiteMirrorPreservesCanonicalPayloadBytes(t *testing.T) {
	db := testutil.NewTestDB(t)
	exact := `{"nested":{"label":"你好","values":[1,1.25,3e4]},"ordered":true}`
	release := validUsageMeterReleaseFixture("meter_release_blob_roundtrip")
	release.pricingSnapshotJSON = exact
	if err := insertUsageMeterRelease(db, release); err != nil {
		t.Fatalf("insert canonical BLOB release: %v", err)
	}
	var storageType string
	var stored []byte
	if err := db.Raw(`SELECT typeof(pricing_snapshot_json), pricing_snapshot_json
		FROM w_agent_usage_meter_release WHERE release_id = ?`, release.releaseID).
		Row().Scan(&storageType, &stored); err != nil {
		t.Fatalf("read canonical BLOB release: %v", err)
	}
	if storageType != "blob" || string(stored) != exact {
		t.Fatalf("canonical payload storage = %s/%q, want exact blob/%q", storageType, stored, exact)
	}

	atLimit := validUsageMeterReleaseFixture("meter_release_blob_65536")
	atLimit.pricingSnapshotJSON = `{"x":"` + strings.Repeat("a", 65528) + `"}`
	if len(atLimit.pricingSnapshotJSON) != 65536 {
		t.Fatalf("64 KiB fixture length = %d", len(atLimit.pricingSnapshotJSON))
	}
	if err := insertUsageMeterRelease(db, atLimit); err != nil {
		t.Fatalf("canonical payload at 64 KiB must pass: %v", err)
	}

	aboveLimit := validUsageMeterReleaseFixture("meter_release_blob_65537")
	aboveLimit.pricingSnapshotJSON = `{"x":"` + strings.Repeat("a", 65529) + `"}`
	if len(aboveLimit.pricingSnapshotJSON) != 65537 {
		t.Fatalf("above-limit fixture length = %d", len(aboveLimit.pricingSnapshotJSON))
	}
	if err := insertUsageMeterRelease(db, aboveLimit); err == nil {
		t.Fatal("canonical payload above 64 KiB must fail")
	}

	textStorage := validUsageMeterReleaseFixture("meter_release_text_storage")
	if err := db.Exec(`INSERT INTO w_agent_usage_meter_release
		(release_id, plugin_id, plugin_version, plugin_release_digest, plugin_snapshot_digest,
		 billing_policy_key, pricing_snapshot_json, pricing_snapshot_digest, meter_key,
		 meter_version, meter_build_digest, source_registry_json, source_registry_digest,
		 release_digest, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		textStorage.releaseID, textStorage.pluginID, textStorage.pluginVersion,
		textStorage.pluginReleaseDigest, textStorage.pluginSnapshotDigest,
		textStorage.billingPolicyKey, textStorage.pricingSnapshotJSON,
		textStorage.pricingSnapshotDigest, textStorage.meterKey, textStorage.meterVersion,
		textStorage.meterBuildDigest, []byte(textStorage.sourceRegistryJSON),
		textStorage.sourceRegistryDigest, textStorage.releaseDigest,
		"2026-08-04 00:00:00.000000").Error; err == nil {
		t.Fatal("canonical payload stored with SQLite TEXT affinity must fail")
	}
}

func TestAgentProviderUsageJournalSQLiteMirrorPinsTerminalReviewSources(t *testing.T) {
	db := testutil.NewTestDB(t)
	insertSettlementReviewTurn(t, db, "turn_executor_terminal", "idem_executor_terminal")
	insertSettlementReviewExecutorBinding(t, db, "turn_executor_terminal", "attempt_executor_terminal", "operation_executor_terminal")
	if err := insertSettlementReview(db, settlementReviewFixture{
		reviewID: "review_executor_terminal", turnID: "turn_executor_terminal",
		settlementKey: "settlement_executor_terminal", source: "executor_terminal",
		reason: "terminal_usage_unmeasured", terminalStatus: "failed",
		attemptID: "attempt_executor_terminal", operationID: "operation_executor_terminal", fencingToken: 1,
	}); err != nil {
		t.Fatalf("zero-count executor terminal Review: %v", err)
	}

	insertSettlementReviewTurn(t, db, "turn_reconcile_terminal", "idem_reconcile_terminal")
	if err := insertSettlementReview(db, settlementReviewFixture{
		reviewID: "review_reconcile_terminal", turnID: "turn_reconcile_terminal",
		settlementKey: "settlement_reconcile_terminal", source: "reconcile_terminal",
		reason: "terminal_usage_unmeasured", terminalStatus: "timeout", fencingToken: 1,
	}); err != nil {
		t.Fatalf("zero-count reconcile terminal Review: %v", err)
	}

	insertSettlementReviewTurn(t, db, "turn_legacy_provider", "idem_legacy_provider")
	insertSettlementReviewExecutorBinding(t, db, "turn_legacy_provider", "attempt_legacy_provider", "operation_legacy_provider")
	if err := insertSettlementReview(db, settlementReviewFixture{
		reviewID: "review_legacy_provider", turnID: "turn_legacy_provider",
		settlementKey: "settlement_legacy_provider", source: "executor_release",
		terminalStatus: "failed", attemptID: "attempt_legacy_provider",
		operationID: "operation_legacy_provider", fencingToken: 1,
		priorOperationCount: 1, priorProviderCount: 1,
	}); err == nil {
		t.Fatal("historical release source must reject Provider-journal evidence")
	}

	insertSettlementReviewTurn(t, db, "turn_terminal_completed", "idem_terminal_completed")
	insertSettlementReviewExecutorBinding(t, db, "turn_terminal_completed", "attempt_terminal_completed", "operation_terminal_completed")
	if err := insertSettlementReview(db, settlementReviewFixture{
		reviewID: "review_terminal_completed", turnID: "turn_terminal_completed",
		settlementKey: "settlement_terminal_completed", source: "executor_terminal",
		reason: "terminal_usage_unmeasured", terminalStatus: "completed",
		attemptID: "attempt_terminal_completed", operationID: "operation_terminal_completed", fencingToken: 1,
	}); err == nil {
		t.Fatal("terminal_usage_unmeasured must reject completed Turns")
	}
}

type usageMeterReleaseFixture struct {
	releaseID             string
	pluginSnapshotDigest  string
	releaseDigest         string
	pricingSnapshotJSON   string
	sourceRegistryJSON    string
	pricingSnapshotDigest string
	sourceRegistryDigest  string
	pluginID              string
	pluginVersion         string
	pluginReleaseDigest   string
	billingPolicyKey      string
	meterKey              string
	meterVersion          string
	meterBuildDigest      string
}

func validUsageMeterReleaseFixture(releaseID string) usageMeterReleaseFixture {
	return usageMeterReleaseFixture{
		releaseID: releaseID, pluginSnapshotDigest: "sha256:plugin_snapshot_" + releaseID,
		releaseDigest:       "sha256:release_" + releaseID,
		pricingSnapshotJSON: `{"unitPrice":1}`, sourceRegistryJSON: `[{"source":"provider.api"}]`,
		pricingSnapshotDigest: "sha256:pricing", sourceRegistryDigest: "sha256:source_registry",
		pluginID: "workmax.writer", pluginVersion: "1.0.0", pluginReleaseDigest: "sha256:plugin",
		billingPolicyKey: "writer.standard", meterKey: "provider_usage", meterVersion: "v1",
		meterBuildDigest: "sha256:meter_build",
	}
}

func insertUsageMeterRelease(db *gorm.DB, row usageMeterReleaseFixture) error {
	return db.Exec(`INSERT INTO w_agent_usage_meter_release
		(release_id, plugin_id, plugin_version, plugin_release_digest, plugin_snapshot_digest,
		 billing_policy_key, pricing_snapshot_json, pricing_snapshot_digest, meter_key,
		 meter_version, meter_build_digest, source_registry_json, source_registry_digest,
		 release_digest, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.releaseID, row.pluginID, row.pluginVersion, row.pluginReleaseDigest,
		row.pluginSnapshotDigest, row.billingPolicyKey, []byte(row.pricingSnapshotJSON),
		row.pricingSnapshotDigest, row.meterKey, row.meterVersion, row.meterBuildDigest,
		[]byte(row.sourceRegistryJSON), row.sourceRegistryDigest, row.releaseDigest,
		"2026-08-04 00:00:00.000000").Error
}

type providerUsageJournalFixture struct {
	receiptID, turnID, attemptID, meterReleaseID  string
	providerEventDigest, sourceRegistrationDigest string
	sourceSchemaDigest, canonicalUsageDigest      string
	providerReceiptDigest, journalRecordDigest    string
	providerUsageJSON                             string
}

func validProviderUsageJournalFixture(receiptID, turnID, attemptID, meterReleaseID string) providerUsageJournalFixture {
	return providerUsageJournalFixture{
		receiptID: receiptID, turnID: turnID, attemptID: attemptID, meterReleaseID: meterReleaseID,
		providerEventDigest:      "sha256:event_" + receiptID,
		sourceRegistrationDigest: "sha256:source_registration",
		sourceSchemaDigest:       "sha256:usage_schema", canonicalUsageDigest: "sha256:canonical_usage_" + receiptID,
		providerReceiptDigest: "sha256:provider_receipt_" + receiptID,
		journalRecordDigest:   "sha256:journal_" + receiptID, providerUsageJSON: `{"inputTokens":10}`,
	}
}

func insertProviderUsageJournal(db *gorm.DB, row providerUsageJournalFixture) error {
	return db.Exec(`INSERT INTO w_agent_provider_usage_journal
		(receipt_id, turn_id, attempt_id, fencing_token, meter_release_id,
		 plugin_id, plugin_version, plugin_release_digest, plugin_snapshot_digest,
		 provider_key, provider_account_digest, provider_request_digest, provider_event_digest,
		 source_key, source_version, source_build_digest, source_registration_digest,
		 usage_schema_key, usage_schema_version, source_schema_digest, canonical_usage_digest,
		 provider_receipt_digest, verification_kind, verification_key_digest,
		 verification_build_digest, attestation_digest, journal_record_digest,
		 provider_usage_json, provider_reported_at, created_at)
		VALUES (?, ?, ?, 1, ?, 'workmax.writer', '1.0.0', 'sha256:plugin',
		 'sha256:plugin_snapshot', 'provider.api', 'sha256:account', 'sha256:request', ?,
		 'provider.api.usage', 'v1', 'sha256:source_build', ?, 'provider.usage', 'v1', ?, ?,
		 ?, 'signed_receipt', 'sha256:verification_key', 'sha256:verification_build',
		 'sha256:attestation', ?, ?, ?, ?)`,
		row.receiptID, row.turnID, row.attemptID, row.meterReleaseID, row.providerEventDigest,
		row.sourceRegistrationDigest, row.sourceSchemaDigest, row.canonicalUsageDigest,
		row.providerReceiptDigest, row.journalRecordDigest, []byte(row.providerUsageJSON),
		"2026-08-04 00:00:01.000000", "2026-08-04 00:00:02.000000").Error
}

type providerUsageEvidenceSourceFixture struct {
	evidenceID               string
	ordinal                  int64
	reviewID                 string
	turnID                   string
	settlementKey            string
	reviewRequestDigest      string
	meterReleaseID           string
	usageSourceDigest        string
	evidenceDigest           string
	sourceReceiptCount       int64
	receiptID                string
	sourceRegistrationDigest string
	sourceSchemaDigest       string
	canonicalUsageDigest     string
	providerReceiptDigest    string
	journalRecordDigest      string
	createdAt                string
}

func insertProviderUsageEvidenceSource(db *gorm.DB, row providerUsageEvidenceSourceFixture) error {
	return db.Exec(`INSERT INTO w_agent_turn_settlement_usage_evidence_source
		(evidence_id, ordinal, review_id, turn_id, settlement_key, review_request_digest,
		 meter_release_id, usage_source_digest, evidence_digest, source_receipt_count,
		 receipt_id, source_registration_digest, source_schema_digest, canonical_usage_digest,
		 provider_receipt_digest, journal_record_digest, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.evidenceID, row.ordinal, row.reviewID, row.turnID, row.settlementKey,
		row.reviewRequestDigest, row.meterReleaseID, row.usageSourceDigest,
		row.evidenceDigest, row.sourceReceiptCount, row.receiptID,
		row.sourceRegistrationDigest, row.sourceSchemaDigest, row.canonicalUsageDigest,
		row.providerReceiptDigest, row.journalRecordDigest, row.createdAt).Error
}

func readAgentProviderUsageJournalMigration(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(agentProviderUsageJournalMigrationFile)
	if err != nil {
		t.Fatalf("read %s: %v", agentProviderUsageJournalMigrationFile, err)
	}
	return string(body)
}
