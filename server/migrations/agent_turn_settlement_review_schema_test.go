package migrations

import (
	"os"
	"strings"
	"testing"

	"gorm.io/gorm"

	"server/utils/testutil"
)

const agentTurnSettlementReviewMigrationFile = "20260667_create_agent_turn_settlement_review.sql"

func TestAgentTurnSettlementReviewMigrationPinsReviewAndEffectHold(t *testing.T) {
	sql := readAgentTurnSettlementReviewMigration(t)
	normalized := normalizeSQL(sql)
	if count := strings.Count(normalized, "create table if not exists `w_agent_turn_settlement_review`"); count != 1 {
		t.Fatalf("Settlement Review table: got %d idempotent CREATE statements, want 1", count)
	}

	assertSQLContains(t, normalized, "alter table `w_agent_effect_outbox` drop constraint `chk_w_agent_effect_outbox_status`, drop constraint `chk_w_agent_effect_outbox_state_tuple`")
	assertSQLContains(t, normalized, "check (`status` in ('pending', 'delivering', 'delivered', 'dead_letter', 'review_hold'))")
	assertSQLContains(t, normalized, "`status` = 'review_hold' and `lease_owner_id` is null and `lease_expires_at` is null and `delivered_at` is null and `dead_lettered_at` is null")

	reviewDDL := createTableDDL(t, sql, "w_agent_turn_settlement_review")
	assertSQLContains(t, reviewDDL, "`review_id` varchar(64) character set ascii collate ascii_bin not null")
	assertSQLContains(t, reviewDDL, "`turn_id` varchar(256) character set utf8mb4 collate utf8mb4_bin not null")
	assertSQLContains(t, reviewDDL, "`settlement_key` varchar(256) character set ascii collate ascii_bin not null")
	assertSQLContains(t, reviewDDL, "`request_digest` varchar(128) character set ascii collate ascii_bin not null")
	assertSQLContains(t, reviewDDL, "unique key `uk_w_agent_turn_settlement_review_review_id` (`review_id`)")
	assertSQLContains(t, reviewDDL, "unique key `uk_w_agent_turn_settlement_review_turn_id` (`turn_id`)")
	assertSQLContains(t, reviewDDL, "unique key `uk_w_agent_turn_settlement_review_settlement_key` (`settlement_key`)")
	assertSQLContains(t, reviewDDL, "key `idx_w_agent_turn_settlement_review_pending` (`status`, `created_at`, `id`)")
	assertSQLContains(t, reviewDDL, "check (`reason` = 'usage_unknown')")
	assertSQLContains(t, reviewDDL, "check (`source` in ('executor_release', 'reconcile_release'))")
	assertSQLContains(t, reviewDDL, "check (`terminal_status` in ('completed', 'stopped', 'failed', 'timeout'))")
	assertSQLContains(t, reviewDDL, "check (`fencing_token` between 1 and 9223372036854775807)")
	assertSQLContains(t, reviewDDL, "`prior_operation_count` between 0 and 9223372036854775807")
	assertSQLContains(t, reviewDDL, "`prior_effect_count` between 0 and 9223372036854775807")
	assertSQLContains(t, reviewDDL, "`current_effect_count` between 0 and 64")
	assertSQLContains(t, reviewDDL, "(`prior_operation_count` > 0 or `prior_effect_count` > 0 or `current_effect_count` > 0)")
	assertSQLContains(t, reviewDDL, "`source` = 'executor_release' and `attempt_id` is not null and `operation_id` is not null")
	assertSQLContains(t, reviewDDL, "`source` = 'reconcile_release' and `attempt_id` is null and `operation_id` is null and `current_effect_count` = 0")
	assertSQLContains(t, reviewDDL, "check (`status` = 'pending')")
	assertSQLContains(t, reviewDDL, "check (`updated_at` >= `created_at`)")
	assertSQLContains(t, reviewDDL, "foreign key (`turn_id`) references `w_agent_turn` (`turn_id`) on delete restrict on update restrict")
	assertSQLContains(t, reviewDDL, "foreign key (`turn_id`, `operation_id`, `attempt_id`, `fencing_token`) references `w_agent_turn_operation` (`turn_id`, `operation_id`, `attempt_id`, `fencing_token`) on delete restrict on update restrict")
	if strings.Contains(reviewDDL, "on update current_timestamp") {
		t.Fatal("Settlement Review timestamps must be written explicitly")
	}
}

func TestAgentTurnSettlementReviewSQLiteMirrorEnforcesIdentityAndEvidence(t *testing.T) {
	db := testutil.NewTestDB(t)
	insertSettlementReviewTurn(t, db, "turn_review_1", "idem_review_1")
	insertSettlementReviewTurn(t, db, "turn_review_2", "idem_review_2")
	insertSettlementReviewExecutorBinding(t, db, "turn_review_1", "attempt_1", "operation_1")

	executor := settlementReviewFixture{
		reviewID: "review_1", turnID: "turn_review_1", settlementKey: "settlement_1",
		source: "executor_release", terminalStatus: "failed", attemptID: "attempt_1",
		fencingToken: 1, operationID: "operation_1", priorOperationCount: 1,
	}
	if err := insertSettlementReview(db, executor); err != nil {
		t.Fatalf("insert executor Settlement Review: %v", err)
	}

	duplicate := executor
	duplicate.turnID = "turn_review_2"
	duplicate.settlementKey = "settlement_2"
	duplicate.source = "reconcile_release"
	duplicate.terminalStatus = "timeout"
	duplicate.attemptID = nil
	duplicate.operationID = nil
	if err := insertSettlementReview(db, duplicate); err == nil {
		t.Fatal("duplicate review_id must fail")
	}
	duplicate.reviewID = "review_2"
	duplicate.settlementKey = executor.settlementKey
	if err := insertSettlementReview(db, duplicate); err == nil {
		t.Fatal("duplicate settlement_key must fail")
	}
	duplicate.settlementKey = "settlement_2"
	duplicate.turnID = executor.turnID
	if err := insertSettlementReview(db, duplicate); err == nil {
		t.Fatal("duplicate reviewed Turn must fail")
	}

	reconcile := settlementReviewFixture{
		reviewID: "review_2", turnID: "turn_review_2", settlementKey: "settlement_2",
		source: "reconcile_release", terminalStatus: "timeout", fencingToken: 3,
		priorEffectCount: 1,
	}
	if err := insertSettlementReview(db, reconcile); err != nil {
		t.Fatalf("insert reconcile Settlement Review: %v", err)
	}
}

func TestAgentTurnSettlementReviewSQLiteMirrorRejectsInvalidRows(t *testing.T) {
	valid := settlementReviewFixture{
		reviewID: "review_invalid", turnID: "turn_review_invalid", settlementKey: "settlement_invalid",
		source: "executor_release", terminalStatus: "stopped", attemptID: "attempt_invalid",
		fencingToken: 1, operationID: "operation_invalid", priorOperationCount: 1,
	}
	cases := []struct {
		name   string
		mutate func(*settlementReviewFixture)
	}{
		{name: "orphan turn", mutate: func(review *settlementReviewFixture) { review.turnID = "missing_turn" }},
		{name: "unknown reason", mutate: func(review *settlementReviewFixture) { review.reason = "free_release" }},
		{name: "unknown source", mutate: func(review *settlementReviewFixture) { review.source = "worker" }},
		{name: "nonterminal status", mutate: func(review *settlementReviewFixture) { review.terminalStatus = "running" }},
		{name: "zero fence", mutate: func(review *settlementReviewFixture) { review.fencingToken = 0 }},
		{name: "no evidence", mutate: func(review *settlementReviewFixture) { review.priorOperationCount = 0 }},
		{name: "too many current effects", mutate: func(review *settlementReviewFixture) { review.currentEffectCount = 65 }},
		{name: "executor missing attempt", mutate: func(review *settlementReviewFixture) { review.attemptID = nil }},
		{name: "executor missing operation", mutate: func(review *settlementReviewFixture) { review.operationID = nil }},
		{name: "executor unknown attempt", mutate: func(review *settlementReviewFixture) { review.attemptID = "attempt_missing" }},
		{name: "executor unknown operation", mutate: func(review *settlementReviewFixture) { review.operationID = "operation_missing" }},
		{name: "reconcile with attempt", mutate: func(review *settlementReviewFixture) {
			review.source, review.operationID = "reconcile_release", nil
		}},
		{name: "reconcile with operation", mutate: func(review *settlementReviewFixture) {
			review.source, review.attemptID = "reconcile_release", nil
		}},
		{name: "reconcile with current effects", mutate: func(review *settlementReviewFixture) {
			review.source, review.attemptID, review.operationID = "reconcile_release", nil, nil
			review.currentEffectCount = 1
		}},
		{name: "nonpending review", mutate: func(review *settlementReviewFixture) { review.status = "resolved" }},
		{name: "oversized review identity", mutate: func(review *settlementReviewFixture) { review.reviewID = strings.Repeat("r", 65) }},
		{name: "updated before created", mutate: func(review *settlementReviewFixture) { review.updatedAt = "2026-08-03 23:59:59.000000" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			if test.name != "orphan turn" {
				insertSettlementReviewTurn(t, db, valid.turnID, "idem_review_invalid")
				insertSettlementReviewExecutorBinding(t, db, valid.turnID, valid.attemptID.(string), valid.operationID.(string))
			}
			review := valid
			test.mutate(&review)
			if err := insertSettlementReview(db, review); err == nil {
				t.Fatal("invalid Settlement Review row must fail")
			}
		})
	}
}

func TestAgentTurnSettlementReviewSQLiteMirrorSupportsUnclaimableEffectHold(t *testing.T) {
	db := testutil.NewTestDB(t)
	const (
		turnID    = "turn_review_hold"
		attemptID = "attempt_review_hold"
		now       = "2026-08-04 00:00:00.000000"
		heartbeat = "2026-08-04 00:00:01.000000"
		lease     = "2026-08-04 00:01:00.000000"
	)
	insertSettlementReviewTurn(t, db, turnID, "idem_review_hold")
	if err := db.Exec(`INSERT INTO w_agent_turn_attempt
		(attempt_id, turn_id, fencing_token, status, worker_id, worker_build_digest,
		 claimed_at, last_heartbeat_at, lease_expires_at)
		VALUES (?, ?, 1, 'running', 'worker_review', 'sha256:worker', ?, ?, ?)`,
		attemptID, turnID, now, heartbeat, lease).Error; err != nil {
		t.Fatalf("insert held Effect Attempt: %v", err)
	}
	if err := db.Exec(`INSERT INTO w_agent_turn_event
		(turn_id, sequence, event_id, schema_version, event_type, event_json)
		VALUES (?, 1, ?, 1, 'core.turn.status', '{"schemaVersion":1}')`, turnID, turnID+":1").Error; err != nil {
		t.Fatalf("insert held Effect Event: %v", err)
	}
	if err := db.Exec(`INSERT INTO w_agent_turn_operation
		(turn_id, operation_id, operation_digest, attempt_id, fencing_token, event_sequence, turn_status, effect_count)
		VALUES (?, 'operation_review_hold', 'sha256:operation', ?, 1, 1, 'running', 2)`, turnID, attemptID).Error; err != nil {
		t.Fatalf("insert held Effect Operation: %v", err)
	}
	if err := db.Exec(`INSERT INTO w_agent_effect_outbox
		(outbox_id, turn_id, attempt_id, turn_fencing_token, operation_id, ordinal,
		 topic, dedupe_key, payload_json, status, available_at, delivery_attempts,
		 dispatch_fencing_token, lease_owner_id, lease_expires_at, created_at, updated_at)
		VALUES ('outbox_review_hold', ?, ?, 1, 'operation_review_hold', 0,
		 'effect.send', 'dedupe_review_hold', '{}', 'delivering', ?, 3, 4, 'dispatcher_1', ?, ?, ?)`,
		turnID, attemptID, now, lease, now, heartbeat).Error; err != nil {
		t.Fatalf("insert delivering Effect: %v", err)
	}
	if err := db.Exec(`UPDATE w_agent_effect_outbox
		SET status = 'review_hold', lease_owner_id = NULL, lease_expires_at = NULL, updated_at = ?
		WHERE outbox_id = 'outbox_review_hold'`, lease).Error; err != nil {
		t.Fatalf("move delivering Effect to review_hold: %v", err)
	}
	var held struct {
		Status               string
		DeliveryAttempts     int64
		DispatchFencingToken int64
		LeaseOwnerID         *string
		LeaseExpiresAt       *string
	}
	if err := db.Raw(`SELECT status, delivery_attempts, dispatch_fencing_token,
		lease_owner_id, lease_expires_at FROM w_agent_effect_outbox WHERE outbox_id = 'outbox_review_hold'`).Scan(&held).Error; err != nil {
		t.Fatalf("read held Effect: %v", err)
	}
	if held.Status != "review_hold" || held.DeliveryAttempts != 3 || held.DispatchFencingToken != 4 ||
		held.LeaseOwnerID != nil || held.LeaseExpiresAt != nil {
		t.Fatalf("held Effect = %+v, want unleased review_hold with preserved counters", held)
	}
	if err := db.Exec(`UPDATE w_agent_effect_outbox SET lease_owner_id = 'dispatcher_2'
		WHERE outbox_id = 'outbox_review_hold'`).Error; err == nil {
		t.Fatal("review_hold Effect with lease owner must fail")
	}
	if err := db.Exec(`UPDATE w_agent_effect_outbox SET delivered_at = ?
		WHERE outbox_id = 'outbox_review_hold'`, lease).Error; err == nil {
		t.Fatal("review_hold Effect with delivered_at must fail")
	}
}

type settlementReviewFixture struct {
	reviewID            string
	turnID              string
	settlementKey       string
	requestDigest       string
	reason              string
	source              string
	terminalStatus      string
	attemptID           any
	fencingToken        int64
	operationID         any
	priorOperationCount int64
	priorEffectCount    int64
	priorProviderCount  int64
	currentEffectCount  int
	status              string
	createdAt           string
	updatedAt           string
}

func insertSettlementReview(db *gorm.DB, review settlementReviewFixture) error {
	if review.requestDigest == "" {
		review.requestDigest = "sha256:request"
	}
	if review.reason == "" {
		review.reason = "usage_unknown"
	}
	if review.status == "" {
		review.status = "pending"
	}
	if review.createdAt == "" {
		review.createdAt = "2026-08-04 00:00:00.000000"
	}
	if review.updatedAt == "" {
		review.updatedAt = "2026-08-04 00:00:01.000000"
	}
	return db.Exec(`INSERT INTO w_agent_turn_settlement_review
		(review_id, turn_id, settlement_key, request_digest, reason, source, terminal_status,
		 attempt_id, fencing_token, operation_id, prior_operation_count, prior_effect_count,
		 prior_provider_usage_count, current_effect_count, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		review.reviewID, review.turnID, review.settlementKey, review.requestDigest,
		review.reason, review.source, review.terminalStatus, review.attemptID,
		review.fencingToken, review.operationID, review.priorOperationCount,
		review.priorEffectCount, review.priorProviderCount, review.currentEffectCount, review.status,
		review.createdAt, review.updatedAt).Error
}

func insertSettlementReviewTurn(t *testing.T, db *gorm.DB, turnID, idempotencyKey string) {
	t.Helper()
	if err := db.Exec(`INSERT INTO w_agent_turn
		(turn_id, principal_id, thread_id, idempotency_key, command_digest, plugin_snapshot_json, updated_at)
		VALUES (?, 'principal_review', 'thread_review', ?, 'sha256:command', '{"id":"workmax.writer"}', ?)`,
		turnID, idempotencyKey, "2026-08-04 00:00:00.000000").Error; err != nil {
		t.Fatalf("insert Settlement Review Turn: %v", err)
	}
}

func insertSettlementReviewExecutorBinding(t *testing.T, db *gorm.DB, turnID, attemptID, operationID string) {
	t.Helper()
	const (
		claimedAt = "2026-08-04 00:00:00.000000"
		heartbeat = "2026-08-04 00:00:01.000000"
		leaseAt   = "2026-08-04 00:01:00.000000"
	)
	if err := db.Exec(`INSERT INTO w_agent_turn_attempt
		(attempt_id, turn_id, fencing_token, status, worker_id, worker_build_digest,
		 claimed_at, last_heartbeat_at, lease_expires_at)
		VALUES (?, ?, 1, 'running', 'worker_review', 'sha256:worker', ?, ?, ?)`,
		attemptID, turnID, claimedAt, heartbeat, leaseAt).Error; err != nil {
		t.Fatalf("insert Settlement Review Attempt binding: %v", err)
	}
	if err := db.Exec(`INSERT INTO w_agent_turn_event
		(turn_id, sequence, event_id, schema_version, event_type, event_json)
		VALUES (?, 1, ?, 1, 'core.turn.status', '{"schemaVersion":1}')`, turnID, turnID+":1").Error; err != nil {
		t.Fatalf("insert Settlement Review Event binding: %v", err)
	}
	if err := db.Exec(`INSERT INTO w_agent_turn_operation
		(turn_id, operation_id, operation_digest, attempt_id, fencing_token, event_sequence, turn_status, effect_count)
		VALUES (?, ?, 'sha256:operation', ?, 1, 1, 'running', 0)`,
		turnID, operationID, attemptID).Error; err != nil {
		t.Fatalf("insert Settlement Review Operation binding: %v", err)
	}
}

func readAgentTurnSettlementReviewMigration(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(agentTurnSettlementReviewMigrationFile)
	if err != nil {
		t.Fatalf("read %s: %v", agentTurnSettlementReviewMigrationFile, err)
	}
	return string(body)
}
