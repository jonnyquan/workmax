package migrations

import (
	"os"
	"strings"
	"testing"

	"gorm.io/gorm"

	"server/utils/testutil"
)

const agentTurnSettlementReviewResolutionMigrationFile = "20260668_resolve_agent_turn_settlement_review.sql"

func TestAgentTurnSettlementReviewResolutionMigrationPinsPositiveFinalize(t *testing.T) {
	sql := readAgentTurnSettlementReviewResolutionMigration(t)
	normalized := normalizeSQL(sql)
	if count := strings.Count(normalized, "create table if not exists `w_agent_turn_settlement_review_resolution`"); count != 1 {
		t.Fatalf("Settlement Review Resolution table: got %d idempotent CREATE statements, want 1", count)
	}

	assertSQLContains(t, normalized, "alter table `w_agent_turn_settlement_review` drop constraint `chk_w_agent_turn_settlement_review_status`")
	assertSQLContains(t, normalized, "unique key `uk_w_agent_turn_settlement_review_resolution_binding` (`review_id`, `turn_id`, `settlement_key`, `request_digest`)")
	assertSQLContains(t, normalized, "check (`status` in ('pending', 'finalized_held'))")
	if strings.Contains(normalized, "alter table `w_agent_effect_outbox`") {
		t.Fatal("financial resolution migration must not release or otherwise alter held Effects")
	}

	resolutionDDL := createTableDDL(t, sql, "w_agent_turn_settlement_review_resolution")
	for _, want := range []string{
		"`id` bigint unsigned not null auto_increment",
		"`resolution_id` varchar(64) character set ascii collate ascii_bin not null",
		"`review_id` varchar(64) character set ascii collate ascii_bin not null",
		"`turn_id` varchar(256) character set utf8mb4 collate utf8mb4_bin not null",
		"`settlement_key` varchar(256) character set ascii collate ascii_bin not null",
		"`review_request_digest` varchar(128) character set ascii collate ascii_bin not null",
		"`decision_digest` varchar(128) character set ascii collate ascii_bin not null",
		"`resolution_digest` varchar(128) character set ascii collate ascii_bin not null",
		"`intent` varchar(16) character set ascii collate ascii_bin not null",
		"`used_units` bigint unsigned not null",
		"`reserved_units` bigint unsigned not null",
		"`actor_id` varchar(256) character set utf8mb4 collate utf8mb4_bin not null",
		"`reason` varchar(32) character set ascii collate ascii_bin not null",
		"`evidence_digest` varchar(128) character set ascii collate ascii_bin not null",
		"`authority_receipt_digest` varchar(128) character set ascii collate ascii_bin not null",
		"`created_at` datetime(6) not null",
		"unique key `uk_w_agent_turn_settlement_review_resolution_resolution_id` (`resolution_id`)",
		"unique key `uk_w_agent_turn_settlement_review_resolution_review_id` (`review_id`)",
		"key `idx_w_agent_turn_settlement_review_resolution_binding` (`review_id`, `turn_id`, `settlement_key`, `review_request_digest`)",
		"check (`intent` = 'finalize')",
		"`used_units` between 1 and 9223372036854775807",
		"`reserved_units` between `used_units` and 9223372036854775807",
		"check (`reason` = 'metered_usage_confirmed')",
		"foreign key (`review_id`, `turn_id`, `settlement_key`, `review_request_digest`) references `w_agent_turn_settlement_review` (`review_id`, `turn_id`, `settlement_key`, `request_digest`) on delete restrict on update restrict",
		") engine=innodb",
	} {
		assertSQLContains(t, resolutionDDL, want)
	}
	for _, column := range []string{
		"resolution_id", "review_id", "turn_id", "settlement_key", "review_request_digest",
		"decision_digest", "resolution_digest", "actor_id", "evidence_digest", "authority_receipt_digest",
	} {
		assertSQLContains(t, resolutionDDL, "octet_length(`"+column+"`) between 1 and")
	}
	if strings.Contains(resolutionDDL, "on update current_timestamp") {
		t.Fatal("Settlement Review Resolution receipt must remain append-only")
	}
}

func TestAgentTurnSettlementReviewResolutionSQLiteMirrorEnforcesBindingAndRestrict(t *testing.T) {
	db := testutil.NewTestDB(t)
	for _, index := range []string{
		"uk_w_agent_turn_settlement_review_resolution_binding",
		"uk_w_agent_turn_settlement_review_resolution_resolution_id",
		"uk_w_agent_turn_settlement_review_resolution_review_id",
	} {
		var count int64
		if err := db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, index).
			Scan(&count).Error; err != nil || count != 1 {
			t.Fatalf("SQLite mirror index %s count = %d, err = %v, want 1", index, count, err)
		}
	}
	insertResolutionReview(t, db, "review_resolution_1", "turn_resolution_1", "settlement_resolution_1", "sha256:request_1")
	insertResolutionReview(t, db, "review_resolution_2", "turn_resolution_2", "settlement_resolution_2", "sha256:request_2")
	insertResolutionReview(t, db, "review_resolution_3", "turn_resolution_3", "settlement_resolution_3", "sha256:request_3")

	valid := validSettlementReviewResolutionFixture(
		"resolution_1", "review_resolution_1", "turn_resolution_1", "settlement_resolution_1", "sha256:request_1")
	insertValidSettlementUsageEvidenceForResolution(t, db, valid)
	if err := insertSettlementReviewResolution(db, valid); err != nil {
		t.Fatalf("insert Settlement Review Resolution: %v", err)
	}
	if err := db.Exec(`UPDATE w_agent_turn_settlement_review SET status = 'finalized_held'
		WHERE review_id = 'review_resolution_1'`).Error; err != nil {
		t.Fatalf("mark resolved Review finalized_held: %v", err)
	}
	if err := db.Exec(`UPDATE w_agent_turn_settlement_review SET status = 'released'
		WHERE review_id = 'review_resolution_1'`).Error; err == nil {
		t.Fatal("Review status outside pending|metered_held|finalized_held must fail")
	}

	duplicateResolutionID := valid
	duplicateResolutionID.reviewID = "review_resolution_2"
	duplicateResolutionID.turnID = "turn_resolution_2"
	duplicateResolutionID.settlementKey = "settlement_resolution_2"
	duplicateResolutionID.reviewRequestDigest = "sha256:request_2"
	duplicateResolutionID.evidenceID = "evidence_resolution_2"
	duplicateResolutionID.evidenceDigest = "sha256:evidence_2"
	insertValidSettlementUsageEvidenceForResolution(t, db, duplicateResolutionID)
	if err := insertSettlementReviewResolution(db, duplicateResolutionID); err == nil {
		t.Fatal("duplicate resolution_id must fail")
	}
	duplicateReviewID := valid
	duplicateReviewID.resolutionID = "resolution_2"
	if err := insertSettlementReviewResolution(db, duplicateReviewID); err == nil {
		t.Fatal("a second Resolution for one review_id must fail")
	}

	mismatchedBinding := valid
	mismatchedBinding.resolutionID = "resolution_3"
	mismatchedBinding.reviewID = "review_resolution_3"
	mismatchedBinding.turnID = "turn_resolution_3"
	mismatchedBinding.settlementKey = "settlement_resolution_3"
	mismatchedBinding.reviewRequestDigest = "sha256:different_request"
	if err := insertSettlementReviewResolution(db, mismatchedBinding); err == nil {
		t.Fatal("Resolution that does not match the immutable Review binding must fail")
	}

	if err := db.Exec(`DELETE FROM w_agent_turn_settlement_review
		WHERE review_id = 'review_resolution_1'`).Error; err == nil {
		t.Fatal("deleting a resolved Review must be RESTRICTed")
	}
	if err := db.Exec(`UPDATE w_agent_turn_settlement_review SET request_digest = 'sha256:changed'
		WHERE review_id = 'review_resolution_1'`).Error; err == nil {
		t.Fatal("updating resolved Review evidence must be RESTRICTed")
	}
}

func TestAgentTurnSettlementReviewResolutionSQLiteMirrorRejectsInvalidReceipts(t *testing.T) {
	valid := validSettlementReviewResolutionFixture(
		"resolution_invalid", "review_resolution_invalid", "turn_resolution_invalid",
		"settlement_resolution_invalid", "sha256:request_invalid")
	cases := []struct {
		name   string
		mutate func(*settlementReviewResolutionFixture)
	}{
		{name: "empty resolution identity", mutate: func(row *settlementReviewResolutionFixture) { row.resolutionID = "" }},
		{name: "oversized resolution identity", mutate: func(row *settlementReviewResolutionFixture) { row.resolutionID = strings.Repeat("r", 65) }},
		{name: "empty evidence identity", mutate: func(row *settlementReviewResolutionFixture) { row.evidenceID = "" }},
		{name: "wrong evidence identity", mutate: func(row *settlementReviewResolutionFixture) { row.evidenceID = "other_evidence" }},
		{name: "unknown intent", mutate: func(row *settlementReviewResolutionFixture) { row.intent = "release" }},
		{name: "zero used units", mutate: func(row *settlementReviewResolutionFixture) { row.usedUnits = 0 }},
		{name: "reserved below used", mutate: func(row *settlementReviewResolutionFixture) { row.usedUnits, row.reservedUnits = 3, 2 }},
		{name: "empty decision digest", mutate: func(row *settlementReviewResolutionFixture) { row.decisionDigest = "" }},
		{name: "oversized resolution digest", mutate: func(row *settlementReviewResolutionFixture) { row.resolutionDigest = strings.Repeat("d", 129) }},
		{name: "oversized multibyte actor", mutate: func(row *settlementReviewResolutionFixture) { row.actorID = strings.Repeat("界", 86) }},
		{name: "unknown reason", mutate: func(row *settlementReviewResolutionFixture) { row.reason = "manual_override" }},
		{name: "empty evidence digest", mutate: func(row *settlementReviewResolutionFixture) { row.evidenceDigest = "" }},
		{name: "empty pricing digest", mutate: func(row *settlementReviewResolutionFixture) { row.pricingSnapshotDigest = "" }},
		{name: "wrong pricing digest", mutate: func(row *settlementReviewResolutionFixture) { row.pricingSnapshotDigest = "sha256:other_pricing" }},
		{name: "oversized authority receipt", mutate: func(row *settlementReviewResolutionFixture) { row.authorityReceiptDigest = strings.Repeat("a", 129) }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			insertResolutionReview(t, db, valid.reviewID, valid.turnID, valid.settlementKey, valid.reviewRequestDigest)
			insertValidSettlementUsageEvidenceForResolution(t, db, valid)
			row := valid
			test.mutate(&row)
			if err := insertSettlementReviewResolution(db, row); err == nil {
				t.Fatal("invalid Settlement Review Resolution row must fail")
			}
		})
	}
}

type settlementReviewResolutionFixture struct {
	resolutionID           string
	reviewID               string
	turnID                 string
	settlementKey          string
	reviewRequestDigest    string
	evidenceID             string
	decisionDigest         string
	resolutionDigest       string
	intent                 string
	usedUnits              int64
	reservedUnits          int64
	actorID                string
	reason                 string
	evidenceDigest         string
	pricingSnapshotDigest  string
	authorityReceiptDigest string
	createdAt              string
}

func insertSettlementReviewResolution(db *gorm.DB, row settlementReviewResolutionFixture) error {
	return db.Exec(`INSERT INTO w_agent_turn_settlement_review_resolution
		(resolution_id, review_id, turn_id, settlement_key, review_request_digest, evidence_id,
		 decision_digest, resolution_digest, intent, used_units, reserved_units,
		 actor_id, reason, evidence_digest, pricing_snapshot_digest, authority_receipt_digest, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.resolutionID, row.reviewID, row.turnID, row.settlementKey, row.reviewRequestDigest,
		row.evidenceID, row.decisionDigest, row.resolutionDigest, row.intent, row.usedUnits, row.reservedUnits,
		row.actorID, row.reason, row.evidenceDigest, row.pricingSnapshotDigest,
		row.authorityReceiptDigest, row.createdAt).Error
}

func validSettlementReviewResolutionFixture(
	resolutionID, reviewID, turnID, settlementKey, reviewRequestDigest string,
) settlementReviewResolutionFixture {
	return settlementReviewResolutionFixture{
		resolutionID: resolutionID, reviewID: reviewID, turnID: turnID,
		settlementKey: settlementKey, reviewRequestDigest: reviewRequestDigest,
		evidenceID:     "evidence_" + resolutionID,
		decisionDigest: "sha256:decision", resolutionDigest: "sha256:resolution",
		intent: "finalize", usedUnits: 1, reservedUnits: 1, actorID: "metering_authority",
		reason: "metered_usage_confirmed", evidenceDigest: "sha256:evidence",
		pricingSnapshotDigest:  "sha256:pricing",
		authorityReceiptDigest: "sha256:authority_receipt", createdAt: "2026-08-04 00:00:02.000000",
	}
}

func insertResolutionReview(t *testing.T, db *gorm.DB, reviewID, turnID, settlementKey, requestDigest string) {
	t.Helper()
	insertSettlementReviewTurn(t, db, turnID, "idem_"+turnID)
	if err := insertSettlementReview(db, settlementReviewFixture{
		reviewID: reviewID, turnID: turnID, settlementKey: settlementKey, requestDigest: requestDigest,
		source: "reconcile_release", terminalStatus: "failed", fencingToken: 1, priorEffectCount: 1,
	}); err != nil {
		t.Fatalf("insert Review for Settlement Review Resolution: %v", err)
	}
}

func readAgentTurnSettlementReviewResolutionMigration(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(agentTurnSettlementReviewResolutionMigrationFile)
	if err != nil {
		t.Fatalf("read %s: %v", agentTurnSettlementReviewResolutionMigrationFile, err)
	}
	return string(body)
}
