package migrations

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"gorm.io/gorm"

	"server/utils/testutil"
)

const agentTurnSettlementUsageEvidenceMigrationFile = "20260669_create_agent_turn_settlement_usage_evidence.sql"

func TestAgentTurnSettlementUsageEvidenceMigrationPinsFailClosedBindings(t *testing.T) {
	sql := readAgentTurnSettlementUsageEvidenceMigration(t)
	normalized := normalizeSQL(sql)
	if count := strings.Count(normalized, "create table if not exists `w_agent_turn_settlement_usage_evidence`"); count != 1 {
		t.Fatalf("Settlement Usage Evidence table: got %d idempotent CREATE statements, want 1", count)
	}

	for _, want := range []string{
		"create temporary table `_w_agent_turn_resolution_empty_guard`",
		"check (`incompatible_rows` = 0)",
		"select count(*) from `w_agent_turn_settlement_review_resolution`",
		"select count(*) from `w_agent_turn_settlement_review` where `status` = 'finalized_held'",
		"drop temporary table `_w_agent_turn_resolution_empty_guard`",
		"alter table `w_agent_turn_settlement_review` drop constraint `chk_w_agent_turn_settlement_review_status`",
		"check (`status` in ('pending', 'metered_held', 'finalized_held'))",
		"alter table `w_agent_turn_settlement_review_resolution` add column `evidence_id` varchar(64) character set ascii collate ascii_bin not null",
		"add column `pricing_snapshot_digest` varchar(128) character set ascii collate ascii_bin not null",
		"key `idx_w_agent_turn_settlement_review_resolution_evidence` (`review_id`, `turn_id`, `settlement_key`, `review_request_digest`, `evidence_id`, `pricing_snapshot_digest`, `evidence_digest`, `used_units`)",
		"foreign key (`review_id`, `turn_id`, `settlement_key`, `review_request_digest`, `evidence_id`, `pricing_snapshot_digest`, `evidence_digest`, `used_units`) references `w_agent_turn_settlement_usage_evidence` (`review_id`, `turn_id`, `settlement_key`, `review_request_digest`, `evidence_id`, `pricing_snapshot_digest`, `evidence_digest`, `used_units`) on delete restrict on update restrict",
	} {
		assertSQLContains(t, normalized, want)
	}
	if strings.Contains(normalized, "update `w_agent_turn_settlement_review_resolution`") ||
		strings.Contains(normalized, "insert into `w_agent_turn_settlement_usage_evidence` select") {
		t.Fatal("P0-043 migration must not fabricate a usage-evidence backfill")
	}

	evidenceDDL := createTableDDL(t, sql, "w_agent_turn_settlement_usage_evidence")
	for _, want := range []string{
		"`evidence_id` varchar(64) character set ascii collate ascii_bin not null",
		"`review_id` varchar(64) character set ascii collate ascii_bin not null",
		"`turn_id` varchar(256) character set utf8mb4 collate utf8mb4_bin not null",
		"`settlement_key` varchar(256) character set ascii collate ascii_bin not null",
		"`plugin_id` varchar(512) character set utf8mb4 collate utf8mb4_bin not null",
		"`plugin_version` varchar(512) character set utf8mb4 collate utf8mb4_bin not null",
		"`plugin_release_digest` varchar(512) character set utf8mb4 collate utf8mb4_bin not null",
		"`billing_policy_key` varchar(256) character set ascii collate ascii_bin not null",
		"`meter_key` varchar(256) character set ascii collate ascii_bin not null",
		"`meter_version` varchar(256) character set ascii collate ascii_bin not null",
		"`used_units` bigint unsigned not null",
		"unique key `uk_w_agent_turn_settlement_usage_evidence_evidence_id` (`evidence_id`)",
		"unique key `uk_w_agent_turn_settlement_usage_evidence_review_id` (`review_id`)",
		"unique key `uk_w_agent_turn_settlement_usage_evidence_meter_source` (`meter_key`, `meter_version`, `usage_source_digest`)",
		"unique key `uk_w_agent_turn_settlement_usage_evidence_resolution_binding` (`review_id`, `turn_id`, `settlement_key`, `review_request_digest`, `evidence_id`, `pricing_snapshot_digest`, `evidence_digest`, `used_units`)",
		"check (`used_units` between 1 and 9223372036854775807)",
		"foreign key (`review_id`, `turn_id`, `settlement_key`, `review_request_digest`) references `w_agent_turn_settlement_review` (`review_id`, `turn_id`, `settlement_key`, `request_digest`) on delete restrict on update restrict",
		") engine=innodb",
	} {
		assertSQLContains(t, evidenceDDL, want)
	}
	for _, column := range []string{
		"evidence_id", "review_id", "turn_id", "settlement_key", "review_request_digest",
		"plugin_id", "plugin_version", "plugin_release_digest", "billing_policy_key",
		"pricing_snapshot_digest", "meter_key", "meter_version", "meter_build_digest",
		"usage_source_digest", "measurement_digest", "meter_receipt_digest", "evidence_digest",
	} {
		assertSQLContains(t, evidenceDDL, "octet_length(`"+column+"`) between 1 and")
	}
	if strings.Contains(evidenceDDL, "on update current_timestamp") {
		t.Fatal("Settlement Usage Evidence must remain append-only")
	}

	namePattern := regexp.MustCompile("(?i)(?:constraint|(?:unique )?key) `([^`]+)`")
	for _, match := range namePattern.FindAllStringSubmatch(sql, -1) {
		if len(match[1]) > 64 {
			t.Fatalf("MySQL identifier %q is %d bytes, want <= 64", match[1], len(match[1]))
		}
	}
}

func TestAgentTurnSettlementUsageEvidenceSQLiteMirrorEnforcesUniquenessAndBinding(t *testing.T) {
	db := testutil.NewTestDB(t)
	for _, index := range []string{
		"uk_w_agent_turn_settlement_usage_evidence_evidence_id",
		"uk_w_agent_turn_settlement_usage_evidence_review_id",
		"uk_w_agent_turn_settlement_usage_evidence_meter_source",
		"uk_w_agent_turn_settlement_usage_evidence_resolution_binding",
	} {
		var count int64
		if err := db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, index).
			Scan(&count).Error; err != nil || count != 1 {
			t.Fatalf("SQLite mirror index %s count = %d, err = %v, want 1", index, count, err)
		}
	}

	insertResolutionReview(t, db, "review_usage_1", "turn_usage_1", "settlement_usage_1", "sha256:request_1")
	insertResolutionReview(t, db, "review_usage_2", "turn_usage_2", "settlement_usage_2", "sha256:request_2")
	valid := validSettlementUsageEvidenceFixture(
		"evidence_usage_1", "review_usage_1", "turn_usage_1", "settlement_usage_1", "sha256:request_1")
	if err := insertSettlementUsageEvidence(db, valid); err != nil {
		t.Fatalf("insert Settlement Usage Evidence: %v", err)
	}
	if err := db.Exec(`UPDATE w_agent_turn_settlement_review SET status = 'metered_held'
		WHERE review_id = ?`, valid.reviewID).Error; err != nil {
		t.Fatalf("mark Review metered_held: %v", err)
	}

	duplicateEvidenceID := valid
	duplicateEvidenceID.reviewID = "review_usage_2"
	duplicateEvidenceID.turnID = "turn_usage_2"
	duplicateEvidenceID.settlementKey = "settlement_usage_2"
	duplicateEvidenceID.reviewRequestDigest = "sha256:request_2"
	duplicateEvidenceID.meterKey = "meter_other"
	duplicateEvidenceID.usageSourceDigest = "sha256:source_other"
	if err := insertSettlementUsageEvidence(db, duplicateEvidenceID); err == nil {
		t.Fatal("duplicate evidence_id must fail")
	}

	duplicateReview := valid
	duplicateReview.evidenceID = "evidence_usage_2"
	duplicateReview.meterKey = "meter_other"
	duplicateReview.usageSourceDigest = "sha256:source_other"
	if err := insertSettlementUsageEvidence(db, duplicateReview); err == nil {
		t.Fatal("a second Evidence row for one Review must fail")
	}

	duplicateMeterSource := valid
	duplicateMeterSource.evidenceID = "evidence_usage_2"
	duplicateMeterSource.reviewID = "review_usage_2"
	duplicateMeterSource.turnID = "turn_usage_2"
	duplicateMeterSource.settlementKey = "settlement_usage_2"
	duplicateMeterSource.reviewRequestDigest = "sha256:request_2"
	if err := insertSettlementUsageEvidence(db, duplicateMeterSource); err == nil {
		t.Fatal("duplicate meter/version/usage-source identity must fail")
	}

	mismatchedParent := duplicateMeterSource
	mismatchedParent.meterKey = "meter_distinct"
	mismatchedParent.usageSourceDigest = "sha256:source_distinct"
	mismatchedParent.reviewRequestDigest = "sha256:wrong_parent"
	if err := insertSettlementUsageEvidence(db, mismatchedParent); err == nil {
		t.Fatal("Evidence outside the immutable Review binding must fail")
	}
	if err := db.Exec(`DELETE FROM w_agent_turn_settlement_review WHERE review_id = ?`, valid.reviewID).Error; err == nil {
		t.Fatal("deleting a measured Review must be RESTRICTed")
	}
}

func TestAgentTurnSettlementUsageEvidenceSQLiteMirrorRejectsInvalidRows(t *testing.T) {
	valid := validSettlementUsageEvidenceFixture(
		"evidence_invalid", "review_usage_invalid", "turn_usage_invalid",
		"settlement_usage_invalid", "sha256:request_invalid")
	tests := []struct {
		name   string
		mutate func(*settlementUsageEvidenceFixture)
	}{
		{name: "empty evidence id", mutate: func(row *settlementUsageEvidenceFixture) { row.evidenceID = "" }},
		{name: "oversized evidence id", mutate: func(row *settlementUsageEvidenceFixture) { row.evidenceID = strings.Repeat("e", 65) }},
		{name: "oversized multibyte plugin", mutate: func(row *settlementUsageEvidenceFixture) { row.pluginID = strings.Repeat("界", 171) }},
		{name: "empty policy", mutate: func(row *settlementUsageEvidenceFixture) { row.billingPolicyKey = "" }},
		{name: "oversized meter key", mutate: func(row *settlementUsageEvidenceFixture) { row.meterKey = strings.Repeat("m", 257) }},
		{name: "empty pricing digest", mutate: func(row *settlementUsageEvidenceFixture) { row.pricingSnapshotDigest = "" }},
		{name: "oversized measurement digest", mutate: func(row *settlementUsageEvidenceFixture) { row.measurementDigest = strings.Repeat("d", 129) }},
		{name: "zero used units", mutate: func(row *settlementUsageEvidenceFixture) { row.usedUnits = 0 }},
		{name: "negative used units", mutate: func(row *settlementUsageEvidenceFixture) { row.usedUnits = -1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			insertResolutionReview(t, db, valid.reviewID, valid.turnID, valid.settlementKey, valid.reviewRequestDigest)
			row := valid
			test.mutate(&row)
			if err := insertSettlementUsageEvidence(db, row); err == nil {
				t.Fatal("invalid Settlement Usage Evidence row must fail")
			}
		})
	}
}

func TestAgentTurnSettlementUsageEvidenceSQLiteMirrorRestrictsResolutionToExactEvidence(t *testing.T) {
	db := testutil.NewTestDB(t)
	insertResolutionReview(t, db, "review_usage_resolution", "turn_usage_resolution",
		"settlement_usage_resolution", "sha256:request_resolution")
	resolution := validSettlementReviewResolutionFixture(
		"resolution_usage", "review_usage_resolution", "turn_usage_resolution",
		"settlement_usage_resolution", "sha256:request_resolution")
	insertValidSettlementUsageEvidenceForResolution(t, db, resolution)
	if err := insertSettlementReviewResolution(db, resolution); err != nil {
		t.Fatalf("insert evidence-bound Resolution: %v", err)
	}

	if err := db.Exec(`DELETE FROM w_agent_turn_settlement_usage_evidence WHERE evidence_id = ?`, resolution.evidenceID).Error; err == nil {
		t.Fatal("deleting Evidence used by a Resolution must be RESTRICTed")
	}
	if err := db.Exec(`UPDATE w_agent_turn_settlement_usage_evidence SET used_units = used_units + 1
		WHERE evidence_id = ?`, resolution.evidenceID).Error; err == nil {
		t.Fatal("changing Evidence units used by a Resolution must be RESTRICTed")
	}

	otherDB := testutil.NewTestDB(t)
	insertResolutionReview(t, otherDB, "review_usage_mismatch", "turn_usage_mismatch",
		"settlement_usage_mismatch", "sha256:request_mismatch")
	other := validSettlementReviewResolutionFixture(
		"resolution_mismatch", "review_usage_mismatch", "turn_usage_mismatch",
		"settlement_usage_mismatch", "sha256:request_mismatch")
	insertValidSettlementUsageEvidenceForResolution(t, otherDB, other)
	other.pricingSnapshotDigest = "sha256:different_pricing"
	if err := insertSettlementReviewResolution(otherDB, other); err == nil {
		t.Fatal("Resolution with a different pricing snapshot must fail")
	}
}

type settlementUsageEvidenceFixture struct {
	evidenceID            string
	reviewID              string
	turnID                string
	settlementKey         string
	reviewRequestDigest   string
	pluginID              string
	pluginVersion         string
	pluginReleaseDigest   string
	billingPolicyKey      string
	pricingSnapshotDigest string
	meterKey              string
	meterVersion          string
	meterBuildDigest      string
	meterReleaseID        string
	usageSourceDigest     string
	sourceReceiptCount    int64
	measurementDigest     string
	usedUnits             int64
	meterReceiptDigest    string
	evidenceDigest        string
	createdAt             string
}

func validSettlementUsageEvidenceFixture(
	evidenceID, reviewID, turnID, settlementKey, reviewRequestDigest string,
) settlementUsageEvidenceFixture {
	return settlementUsageEvidenceFixture{
		evidenceID: evidenceID, reviewID: reviewID, turnID: turnID,
		settlementKey: settlementKey, reviewRequestDigest: reviewRequestDigest,
		pluginID: "workmax.writer", pluginVersion: "1.0.0", pluginReleaseDigest: "sha256:plugin",
		billingPolicyKey: "writer.standard", pricingSnapshotDigest: "sha256:pricing",
		meterKey: "provider_usage", meterVersion: "v1", meterBuildDigest: "sha256:meter_build",
		meterReleaseID:    "meter_release_" + evidenceID,
		usageSourceDigest: "sha256:usage_source", sourceReceiptCount: 1,
		measurementDigest: "sha256:measurement",
		usedUnits:         1, meterReceiptDigest: "sha256:meter_receipt", evidenceDigest: "sha256:evidence",
		createdAt: "2026-08-04 00:00:01.000000",
	}
}

func insertSettlementUsageEvidence(db *gorm.DB, row settlementUsageEvidenceFixture) error {
	if err := insertSettlementUsageMeterRelease(db, row); err != nil {
		return err
	}
	return db.Exec(`INSERT INTO w_agent_turn_settlement_usage_evidence
		(evidence_id, review_id, turn_id, settlement_key, review_request_digest,
		 plugin_id, plugin_version, plugin_release_digest, billing_policy_key,
		 pricing_snapshot_digest, meter_key, meter_version, meter_build_digest,
		 meter_release_id, usage_source_digest, source_receipt_count, measurement_digest,
		 used_units, meter_receipt_digest, evidence_digest, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.evidenceID, row.reviewID, row.turnID, row.settlementKey, row.reviewRequestDigest,
		row.pluginID, row.pluginVersion, row.pluginReleaseDigest, row.billingPolicyKey,
		row.pricingSnapshotDigest, row.meterKey, row.meterVersion, row.meterBuildDigest,
		row.meterReleaseID, row.usageSourceDigest, row.sourceReceiptCount,
		row.measurementDigest, row.usedUnits, row.meterReceiptDigest,
		row.evidenceDigest, row.createdAt).Error
}

func insertSettlementUsageMeterRelease(db *gorm.DB, row settlementUsageEvidenceFixture) error {
	return db.Exec(`INSERT OR IGNORE INTO w_agent_usage_meter_release
		(release_id, plugin_id, plugin_version, plugin_release_digest, plugin_snapshot_digest,
		 billing_policy_key, pricing_snapshot_json, pricing_snapshot_digest,
		 meter_key, meter_version, meter_build_digest, source_registry_json,
		 source_registry_digest, release_digest, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.meterReleaseID, row.pluginID, row.pluginVersion, row.pluginReleaseDigest,
		"sha256:plugin_snapshot_"+row.meterReleaseID, row.billingPolicyKey, []byte("{}"),
		row.pricingSnapshotDigest, row.meterKey, row.meterVersion, row.meterBuildDigest,
		[]byte("[]"), "sha256:source_registry_"+row.meterReleaseID,
		"sha256:meter_release_"+row.meterReleaseID, row.createdAt).Error
}

func insertValidSettlementUsageEvidenceForResolution(
	t *testing.T,
	db *gorm.DB,
	resolution settlementReviewResolutionFixture,
) {
	t.Helper()
	evidence := validSettlementUsageEvidenceFixture(
		resolution.evidenceID, resolution.reviewID, resolution.turnID,
		resolution.settlementKey, resolution.reviewRequestDigest,
	)
	evidence.pricingSnapshotDigest = resolution.pricingSnapshotDigest
	evidence.evidenceDigest = resolution.evidenceDigest
	evidence.usedUnits = resolution.usedUnits
	evidence.meterKey = "meter_" + resolution.evidenceID
	evidence.usageSourceDigest = "sha256:source_" + resolution.evidenceID
	if err := insertSettlementUsageEvidence(db, evidence); err != nil {
		t.Fatalf("insert Settlement Usage Evidence for Resolution: %v", err)
	}
	if err := db.Exec(`UPDATE w_agent_turn_settlement_review SET status = 'metered_held'
		WHERE review_id = ?`, resolution.reviewID).Error; err != nil {
		t.Fatalf("mark Resolution Review metered_held: %v", err)
	}
}

func readAgentTurnSettlementUsageEvidenceMigration(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(agentTurnSettlementUsageEvidenceMigrationFile)
	if err != nil {
		t.Fatalf("read %s: %v", agentTurnSettlementUsageEvidenceMigrationFile, err)
	}
	return string(body)
}
