package migrations

import (
	"os"
	"strings"
	"testing"

	"server/utils/testutil"
)

const agentTurnExecutionMigrationFile = "20260666_create_agent_turn_execution.sql"

func TestAgentTurnExecutionMigrationPinsFenceAndAttempt(t *testing.T) {
	sql := readAgentTurnExecutionMigration(t)
	normalized := normalizeSQL(sql)

	for _, table := range []string{
		"w_agent_turn_attempt",
		"w_agent_turn_operation",
		"w_agent_effect_outbox",
	} {
		needle := "create table if not exists `" + table + "`"
		if count := strings.Count(normalized, needle); count != 1 {
			t.Fatalf("%s: got %d idempotent CREATE statements, want 1", table, count)
		}
	}

	assertSQLContains(t, normalized, "alter table `w_agent_turn` add column `active_attempt_id` varchar(64) character set ascii collate ascii_bin default null")
	assertSQLContains(t, normalized, "add column `fencing_token` bigint unsigned not null default 0")
	assertSQLContains(t, normalized, "(`active_attempt_id` is null and `fencing_token` between 0 and 9223372036854775807)")
	assertSQLContains(t, normalized, "(`active_attempt_id` is not null and `fencing_token` between 1 and 9223372036854775807)")
	assertSQLContains(t, normalized, "foreign key (`turn_id`, `active_attempt_id`, `fencing_token`) references `w_agent_turn_attempt` (`turn_id`, `attempt_id`, `fencing_token`) on delete restrict on update restrict")

	attemptDDL := createTableDDL(t, sql, "w_agent_turn_attempt")
	assertSQLContains(t, attemptDDL, "unique key `uk_w_agent_turn_attempt_id` (`attempt_id`)")
	assertSQLContains(t, attemptDDL, "unique key `uk_w_agent_turn_attempt_fence` (`turn_id`, `fencing_token`)")
	assertSQLContains(t, attemptDDL, "unique key `uk_w_agent_turn_attempt_binding` (`turn_id`, `attempt_id`, `fencing_token`)")
	assertSQLContains(t, attemptDDL, "key `idx_w_agent_turn_attempt_claim` (`status`, `lease_expires_at`, `id`)")
	assertSQLContains(t, attemptDDL, "check (`fencing_token` between 1 and 9223372036854775807)")
	assertSQLContains(t, attemptDDL, "`status` in ('running', 'completed', 'stopped', 'failed', 'timeout', 'expired')")
	assertSQLContains(t, attemptDDL, "`finished_at` is null or `finished_at` >= `last_heartbeat_at`")
	assertSQLContains(t, attemptDDL, "foreign key (`turn_id`) references `w_agent_turn` (`turn_id`) on delete restrict on update restrict")
	if strings.Contains(attemptDDL, "on update current_timestamp") {
		t.Fatal("Attempt lifecycle timestamps must be written explicitly")
	}
}

func TestAgentTurnExecutionMigrationPinsOperationAndOutbox(t *testing.T) {
	sql := readAgentTurnExecutionMigration(t)
	operationDDL := createTableDDL(t, sql, "w_agent_turn_operation")
	outboxDDL := createTableDDL(t, sql, "w_agent_effect_outbox")

	assertSQLContains(t, operationDDL, "unique key `uk_w_agent_turn_operation_identity` (`turn_id`, `operation_id`)")
	assertSQLContains(t, operationDDL, "`effect_count` smallint unsigned not null default 0")
	assertSQLContains(t, operationDDL, "check (`effect_count` between 0 and 64)")
	assertSQLContains(t, operationDDL, "foreign key (`turn_id`, `attempt_id`, `fencing_token`) references `w_agent_turn_attempt` (`turn_id`, `attempt_id`, `fencing_token`) on delete restrict on update restrict")
	assertSQLContains(t, operationDDL, "foreign key (`turn_id`, `event_sequence`) references `w_agent_turn_event` (`turn_id`, `sequence`) on delete restrict on update restrict")

	assertSQLContains(t, outboxDDL, "unique key `uk_w_agent_effect_outbox_id` (`outbox_id`)")
	assertSQLContains(t, outboxDDL, "unique key `uk_w_agent_effect_outbox_dedupe` (`topic`, `dedupe_key`)")
	assertSQLContains(t, outboxDDL, "unique key `uk_w_agent_effect_outbox_operation_ordinal` (`turn_id`, `operation_id`, `ordinal`)")
	assertSQLContains(t, outboxDDL, "key `idx_w_agent_effect_outbox_pending` (`status`, `available_at`, `id`)")
	assertSQLContains(t, outboxDDL, "key `idx_w_agent_effect_outbox_expired` (`status`, `lease_expires_at`, `id`)")
	assertSQLContains(t, outboxDDL, "check (octet_length(`payload_json`) between 1 and 1048576)")
	assertSQLContains(t, outboxDDL, "check (`ordinal` between 0 and 63)")
	assertSQLContains(t, outboxDDL, "`status` in ('pending', 'delivering', 'delivered', 'dead_letter')")
	assertSQLContains(t, outboxDDL, "foreign key (`turn_id`, `attempt_id`, `turn_fencing_token`) references `w_agent_turn_attempt` (`turn_id`, `attempt_id`, `fencing_token`) on delete restrict on update restrict")
	assertSQLContains(t, outboxDDL, "foreign key (`turn_id`, `operation_id`, `attempt_id`, `turn_fencing_token`) references `w_agent_turn_operation` (`turn_id`, `operation_id`, `attempt_id`, `fencing_token`) on delete restrict on update restrict")
	if strings.Contains(outboxDDL, "on update current_timestamp") {
		t.Fatal("Outbox lifecycle timestamps must be written explicitly")
	}
}

func TestAgentTurnExecutionSQLiteMirrorEnforcesFenceOperationAndOutbox(t *testing.T) {
	db := testutil.NewTestDB(t)
	var foreignKeys int
	if err := db.Raw("PRAGMA foreign_keys").Scan(&foreignKeys).Error; err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 {
		t.Fatalf("SQLite foreign_keys = %d, want 1", foreignKeys)
	}

	const (
		turnID    = "turn_execution_1"
		attemptID = "attempt_1"
		claimedAt = "2026-08-01 00:00:00.000000"
		heartbeat = "2026-08-01 00:00:01.000000"
		leaseAt   = "2026-08-01 00:01:00.000000"
	)
	if err := db.Exec(`INSERT INTO w_agent_turn
		(turn_id, principal_id, thread_id, idempotency_key, command_digest, plugin_snapshot_json, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		turnID, "principal_1", "thread_1", "idem_execution_1", "sha256:command", `{"id":"workmax.writer"}`, claimedAt,
	).Error; err != nil {
		t.Fatalf("insert Turn: %v", err)
	}
	if err := db.Exec(`UPDATE w_agent_turn SET active_attempt_id = ?, fencing_token = 1 WHERE turn_id = ?`, "missing_attempt", turnID).Error; err == nil {
		t.Fatal("Turn active Attempt foreign key accepted a missing Attempt")
	}

	insertAttempt := func(id string, fence int64, status string, finishedAt any) error {
		return db.Exec(`INSERT INTO w_agent_turn_attempt
			(attempt_id, turn_id, fencing_token, status, worker_id, worker_build_digest,
			 claimed_at, last_heartbeat_at, lease_expires_at, finished_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, turnID, fence, status, "worker_1", "sha256:worker-build", claimedAt, heartbeat, leaseAt, finishedAt,
		).Error
	}
	if err := insertAttempt(attemptID, 1, "running", nil); err != nil {
		t.Fatalf("insert Attempt: %v", err)
	}
	if err := db.Exec(`UPDATE w_agent_turn SET active_attempt_id = ?, fencing_token = 1 WHERE turn_id = ?`, attemptID, turnID).Error; err != nil {
		t.Fatalf("activate Attempt: %v", err)
	}
	if err := db.Exec(`UPDATE w_agent_turn SET active_attempt_id = NULL WHERE turn_id = ?`, turnID).Error; err != nil {
		t.Fatalf("clear active Attempt while retaining fence: %v", err)
	}
	if err := db.Exec(`UPDATE w_agent_turn SET active_attempt_id = ? WHERE turn_id = ?`, attemptID, turnID).Error; err != nil {
		t.Fatalf("restore active Attempt: %v", err)
	}
	if err := insertAttempt("attempt_duplicate_fence", 1, "running", nil); err == nil {
		t.Fatal("duplicate Turn fencing token must fail")
	}
	if err := insertAttempt("attempt_zero", 0, "running", nil); err == nil {
		t.Fatal("Attempt fencing token zero must fail")
	}
	if err := insertAttempt("attempt_bad_status", 2, "queued", nil); err == nil {
		t.Fatal("unknown Attempt status must fail")
	}
	if err := insertAttempt("attempt_unfinished_terminal", 3, "completed", nil); err == nil {
		t.Fatal("terminal Attempt without finished_at must fail")
	}
	if err := insertAttempt("attempt_early_finish", 4, "failed", claimedAt); err == nil {
		t.Fatal("Attempt finished before its last heartbeat must fail")
	}
	if err := db.Exec(`UPDATE w_agent_turn SET active_attempt_id = NULL, fencing_token = 9223372036854775808 WHERE turn_id = ?`, turnID).Error; err == nil {
		t.Fatal("Turn fencing token beyond MaxInt64 must fail")
	}

	if err := db.Exec(`INSERT INTO w_agent_turn_event
		(turn_id, sequence, event_id, schema_version, event_type, event_json)
		VALUES (?, 1, ?, 1, 'core.turn.status', '{"schemaVersion":1}')`, turnID, turnID+":1").Error; err != nil {
		t.Fatalf("insert Event: %v", err)
	}
	insertOperation := func(operationID, attempt string, fence, sequence, effectCount int64) error {
		return db.Exec(`INSERT INTO w_agent_turn_operation
			(turn_id, operation_id, operation_digest, attempt_id, fencing_token, event_sequence, turn_status, effect_count)
			VALUES (?, ?, ?, ?, ?, ?, 'running', ?)`,
			turnID, operationID, "sha256:operation", attempt, fence, sequence, effectCount,
		).Error
	}
	if err := insertOperation("operation_1", attemptID, 1, 1, 1); err != nil {
		t.Fatalf("insert Operation: %v", err)
	}
	if err := insertOperation("operation_1", attemptID, 1, 1, 1); err == nil {
		t.Fatal("duplicate Turn/Operation identity must fail")
	}
	if err := insertOperation("operation_missing_attempt", "missing_attempt", 1, 1, 0); err == nil {
		t.Fatal("Operation accepted a missing Attempt binding")
	}
	if err := insertOperation("operation_missing_event", attemptID, 1, 2, 0); err == nil {
		t.Fatal("Operation accepted a missing Event binding")
	}
	if err := insertOperation("operation_too_many_effects", attemptID, 1, 1, 65); err == nil {
		t.Fatal("Operation effect_count beyond 64 must fail")
	}

	insertOutbox := func(outboxID, operationID, topic, dedupe, payload, status string, ordinal int, attempts, dispatchFence int64, owner any, lease any, delivered any, dead any) error {
		return db.Exec(`INSERT INTO w_agent_effect_outbox
			(outbox_id, turn_id, attempt_id, turn_fencing_token, operation_id, ordinal,
			 topic, dedupe_key, payload_json, status, available_at, delivery_attempts,
			 dispatch_fencing_token, lease_owner_id, lease_expires_at, delivered_at, dead_lettered_at)
			VALUES (?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			outboxID, turnID, attemptID, operationID, ordinal, topic, dedupe, payload, status,
			claimedAt, attempts, dispatchFence, owner, lease, delivered, dead,
		).Error
	}
	if err := insertOutbox("outbox_1", "operation_1", "effect.send", "dedupe_1", `{}`, "pending", 0, 0, 0, nil, nil, nil, nil); err != nil {
		t.Fatalf("insert Outbox: %v", err)
	}
	if err := insertOutbox("outbox_duplicate_dedupe", "operation_1", "effect.send", "dedupe_1", `{}`, "pending", 1, 0, 0, nil, nil, nil, nil); err == nil {
		t.Fatal("duplicate topic/dedupe key must fail")
	}
	if err := insertOutbox("outbox_duplicate_ordinal", "operation_1", "effect.other", "dedupe_2", `{}`, "pending", 0, 0, 0, nil, nil, nil, nil); err == nil {
		t.Fatal("duplicate Turn/Operation ordinal must fail")
	}
	if err := insertOutbox("outbox_bad_ordinal", "operation_1", "effect.other", "dedupe_3", `{}`, "pending", 64, 0, 0, nil, nil, nil, nil); err == nil {
		t.Fatal("Outbox ordinal beyond 63 must fail")
	}
	if err := insertOutbox("outbox_bad_json", "operation_1", "effect.other", "dedupe_4", `{`, "pending", 1, 0, 0, nil, nil, nil, nil); err == nil {
		t.Fatal("malformed Outbox payload JSON must fail")
	}
	oversizedPayload := `"` + strings.Repeat("x", 1<<20) + `"`
	if err := insertOutbox("outbox_large", "operation_1", "effect.other", "dedupe_5", oversizedPayload, "pending", 1, 0, 0, nil, nil, nil, nil); err == nil {
		t.Fatal("Outbox payload beyond 1 MiB must fail")
	}
	if err := insertOutbox("outbox_bad_delivering", "operation_1", "effect.other", "dedupe_6", `{}`, "delivering", 1, 1, 1, nil, nil, nil, nil); err == nil {
		t.Fatal("delivering Outbox without a lease tuple must fail")
	}
	if err := insertOutbox("outbox_bad_delivered", "operation_1", "effect.other", "dedupe_7", `{}`, "delivered", 1, 1, 1, nil, nil, nil, nil); err == nil {
		t.Fatal("delivered Outbox without delivered_at must fail")
	}
	if err := db.Exec(`UPDATE w_agent_effect_outbox SET dispatch_fencing_token = 9223372036854775808 WHERE outbox_id = 'outbox_1'`).Error; err == nil {
		t.Fatal("Outbox dispatch fence beyond MaxInt64 must fail")
	}
}

func readAgentTurnExecutionMigration(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(agentTurnExecutionMigrationFile)
	if err != nil {
		t.Fatalf("read %s: %v", agentTurnExecutionMigrationFile, err)
	}
	return string(body)
}
