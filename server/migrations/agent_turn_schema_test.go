package migrations

import (
	"os"
	"strings"
	"testing"

	agentv1 "server/contracts/agent/v1"
	"server/utils/testutil"
)

const agentTurnMigrationFile = "20260665_create_agent_turn.sql"

func TestAgentTurnMigrationUsesIdempotentKernelTables(t *testing.T) {
	sql := readAgentTurnMigration(t)
	normalized := normalizeSQL(sql)

	for _, table := range []string{"w_agent_turn", "w_agent_turn_event"} {
		needle := "create table if not exists `" + table + "`"
		if count := strings.Count(normalized, needle); count != 1 {
			t.Fatalf("%s: got %d idempotent CREATE statements, want 1", table, count)
		}
	}
	if strings.Contains(normalized, "create table `w_agent_") {
		t.Fatal("Agent Kernel tables must use CREATE TABLE IF NOT EXISTS")
	}
}

func TestAgentTurnMigrationPinsAdmissionIdentityAndSnapshot(t *testing.T) {
	turnDDL := createTableDDL(t, readAgentTurnMigration(t), "w_agent_turn")

	for _, column := range []struct {
		name   string
		length string
	}{
		{name: "principal_id", length: "128"},
		{name: "thread_id", length: "256"},
		{name: "idempotency_key", length: "128"},
	} {
		want := "`" + column.name + "` varchar(" + column.length + ") character set utf8mb4 collate utf8mb4_bin not null"
		assertSQLContains(t, turnDDL, want)
	}

	assertSQLContains(t, turnDDL, "`turn_id` varchar(256) character set utf8mb4 collate utf8mb4_bin not null")
	assertSQLContains(t, turnDDL, "unique key `uk_w_agent_turn_turn_id` (`turn_id`)")
	assertSQLContains(t, turnDDL, "unique key `uk_w_agent_turn_admission` (`principal_id`, `thread_id`, `idempotency_key`)")
	assertSQLContains(t, turnDDL, "`command_digest` varchar(128)")
	assertSQLContains(t, turnDDL, "`plugin_snapshot_json` json not null")
	assertSQLContains(t, turnDDL, "`last_event_sequence` bigint unsigned not null default 1")
	assertSQLContains(t, turnDDL, "check (`last_event_sequence` between 1 and 9223372036854775807)")
	assertSQLContains(t, turnDDL, "check (octet_length(`turn_id`) between 1 and 256)")
	assertSQLContains(t, turnDDL, "check (octet_length(`principal_id`) between 1 and 128)")
	assertSQLContains(t, turnDDL, "check (octet_length(`thread_id`) between 1 and 256)")
	assertSQLContains(t, turnDDL, "check (octet_length(`idempotency_key`) between 1 and 128)")
	assertSQLContains(t, turnDDL, "check (octet_length(`command_digest`) between 1 and 128)")
	assertSQLContains(t, turnDDL, "`updated_at` datetime(6) not null")
	if strings.Contains(turnDDL, "on update current_timestamp") {
		t.Fatal("event sequence allocation must not advance the Turn lifecycle updated_at")
	}

	for _, timestamp := range []string{"cancel_requested_at", "started_at", "finished_at"} {
		assertSQLContains(t, turnDDL, "`"+timestamp+"` datetime(6) default null")
	}

	for _, status := range []agentv1.TurnStatus{
		agentv1.TurnStatusQueued,
		agentv1.TurnStatusRunning,
		agentv1.TurnStatusCompleted,
		agentv1.TurnStatusStopped,
		agentv1.TurnStatusFailed,
		agentv1.TurnStatusTimeout,
	} {
		assertSQLContains(t, turnDDL, "'"+string(status)+"'")
	}
}

func TestAgentTurnEventMigrationPinsAppendOnlyEnvelopeIdentity(t *testing.T) {
	eventDDL := createTableDDL(t, readAgentTurnMigration(t), "w_agent_turn_event")

	assertSQLContains(t, eventDDL, "`turn_id` varchar(256) character set utf8mb4 collate utf8mb4_bin not null")
	assertSQLContains(t, eventDDL, "`sequence` bigint unsigned not null")
	assertSQLContains(t, eventDDL, "`event_json` json not null")
	assertSQLContains(t, eventDDL, "immutable complete eventenvelope json; insert-only for the application role")
	assertSQLContains(t, eventDDL, "unique key `uk_w_agent_turn_event_sequence` (`turn_id`, `sequence`)")
	assertSQLContains(t, eventDDL, "unique key `uk_w_agent_turn_event_id` (`turn_id`, `event_id`)")
	assertSQLContains(t, eventDDL, "check (`sequence` between 1 and 9223372036854775807)")
	assertSQLContains(t, eventDDL, "check (octet_length(`turn_id`) between 1 and 256)")
	assertSQLContains(t, eventDDL, "check (octet_length(`event_id`) between 1 and 320)")
	assertSQLContains(t, eventDDL, "check (octet_length(`event_type`) between 1 and 255)")
	assertSQLContains(t, eventDDL, "foreign key (`turn_id`) references `w_agent_turn` (`turn_id`) on delete restrict on update restrict")

	if strings.Contains(eventDDL, "`updated_at`") || strings.Contains(eventDDL, "on update current_timestamp") {
		t.Fatal("append-only event rows must not expose an update timestamp or ON UPDATE mutation")
	}
}

func TestAgentTurnSQLiteMirrorEnforcesExactIdentityUniques(t *testing.T) {
	db := testutil.NewTestDB(t)
	if err := db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
		t.Fatalf("enable SQLite foreign keys: %v", err)
	}
	insertTurn := func(turnID, principalID, threadID, idempotencyKey string) error {
		return db.Exec(`INSERT INTO w_agent_turn
			(turn_id, principal_id, thread_id, idempotency_key, command_digest, plugin_snapshot_json, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			turnID, principalID, threadID, idempotencyKey, "sha256:command", `{"id":"workmax.writer"}`, "2026-08-01 00:00:00",
		).Error
	}

	if err := insertTurn("turn_1", "principal_1", "thread_1", "Idem_1"); err != nil {
		t.Fatalf("insert first Turn: %v", err)
	}
	var lastEventSequence int64
	if err := db.Raw(`SELECT last_event_sequence FROM w_agent_turn WHERE turn_id = ?`, "turn_1").Scan(&lastEventSequence).Error; err != nil {
		t.Fatalf("read default last_event_sequence: %v", err)
	}
	if lastEventSequence != 1 {
		t.Fatalf("default last_event_sequence: got %d, want 1", lastEventSequence)
	}
	if err := insertTurn("turn_2", "principal_1", "thread_1", "Idem_1"); err == nil {
		t.Fatal("duplicate principal/thread/idempotency tuple must fail")
	}
	if err := insertTurn("turn_1", "principal_1", "thread_1", "Idem_2"); err == nil {
		t.Fatal("duplicate public turn_id must fail")
	}
	if err := insertTurn("turn_3", "principal_1", "thread_1", "idem_1"); err != nil {
		t.Fatalf("binary idempotency identity must remain case-sensitive: %v", err)
	}
	if err := insertTurn("turn_4", strings.Repeat("p", 129), "thread_1", "Idem_4"); err == nil {
		t.Fatal("principal_id beyond the 128-byte SQL bound must fail")
	}
	if err := insertTurn("turn_5", strings.Repeat("界", 43), "thread_1", "Idem_5"); err == nil {
		t.Fatal("principal_id beyond the 128-byte SQL bound must fail even below 128 characters")
	}
	if err := insertTurn("turn_6", "principal_1", strings.Repeat("界", 86), "Idem_6"); err == nil {
		t.Fatal("thread_id beyond the 256-byte SQL bound must fail even below 256 characters")
	}
	if err := insertTurn("turn_7", "principal_1", "thread_1", strings.Repeat("界", 43)); err == nil {
		t.Fatal("idempotency_key beyond the 128-byte SQL bound must fail even below 128 characters")
	}
	if err := insertTurn(strings.Repeat("界", 86), "principal_1", "thread_1", "Idem_8"); err == nil {
		t.Fatal("turn_id beyond the 256-byte SQL bound must fail even below 256 characters")
	}
	if err := db.Exec(`INSERT INTO w_agent_turn
		(turn_id, principal_id, thread_id, idempotency_key, command_digest, plugin_snapshot_json, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"turn_bad_digest", "principal_1", "thread_1", "Idem_9", strings.Repeat("d", 129), `{"id":"workmax.writer"}`, "2026-08-01 00:00:00",
	).Error; err == nil {
		t.Fatal("command_digest beyond the 128-byte SQL bound must fail")
	}
	if err := db.Exec(`INSERT INTO w_agent_turn
		(turn_id, principal_id, thread_id, idempotency_key, command_digest, plugin_snapshot_json, last_event_sequence, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"turn_zero_sequence", "principal_1", "thread_1", "Idem_zero", "sha256:command", `{"id":"workmax.writer"}`, 0, "2026-08-01 00:00:00",
	).Error; err == nil {
		t.Fatal("last_event_sequence below 1 must fail")
	}

	insertEvent := func(turnID string, sequence int, eventID string) error {
		return db.Exec(`INSERT INTO w_agent_turn_event
			(turn_id, sequence, event_id, schema_version, event_type, event_json)
			VALUES (?, ?, ?, ?, ?, ?)`,
			turnID, sequence, eventID, agentv1.EventEnvelopeSchemaVersion, agentv1.EventCoreTurnStatus, `{"schemaVersion":1}`,
		).Error
	}
	if err := insertEvent("missing_turn", 1, "missing_turn:1"); err == nil {
		t.Fatal("orphan Event must fail the Turn foreign key")
	}
	if err := insertEvent("turn_1", 0, "turn_1:0"); err == nil {
		t.Fatal("Event sequence below 1 must fail")
	}
	if err := insertEvent("turn_1", 1, "turn_1:1"); err != nil {
		t.Fatalf("insert first Event: %v", err)
	}
	if err := insertEvent("turn_1", 1, "turn_1:duplicate-sequence"); err == nil {
		t.Fatal("duplicate turn_id/sequence must fail")
	}
	if err := insertEvent("turn_1", 2, "turn_1:1"); err == nil {
		t.Fatal("duplicate turn_id/event_id must fail")
	}
	if err := insertEvent("turn_1", 2, strings.Repeat("界", 107)); err == nil {
		t.Fatal("event_id beyond the 320-byte SQL bound must fail even below 320 characters")
	}
	if err := db.Exec(`INSERT INTO w_agent_turn_event
		(turn_id, sequence, event_id, schema_version, event_type, event_json)
		VALUES (?, ?, ?, ?, ?, ?)`,
		"turn_1", 2, "turn_1:2", agentv1.EventEnvelopeSchemaVersion, strings.Repeat("界", 86), `{"schemaVersion":1}`,
	).Error; err == nil {
		t.Fatal("event_type beyond the 255-byte SQL bound must fail even below 255 characters")
	}
}

func readAgentTurnMigration(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(agentTurnMigrationFile)
	if err != nil {
		t.Fatalf("read %s: %v", agentTurnMigrationFile, err)
	}
	return string(body)
}

func createTableDDL(t *testing.T, sql, table string) string {
	t.Helper()
	normalized := normalizeSQL(sql)
	marker := "create table if not exists `" + table + "`"
	start := strings.Index(normalized, marker)
	if start < 0 {
		t.Fatalf("missing %s", marker)
	}
	afterMarker := start + len(marker)
	if next := strings.Index(normalized[afterMarker:], "create table if not exists `"); next >= 0 {
		return normalized[start : afterMarker+next]
	}
	return normalized[start:]
}

func assertSQLContains(t *testing.T, sql, want string) {
	t.Helper()
	want = normalizeSQL(want)
	if !strings.Contains(sql, want) {
		t.Errorf("migration DDL missing %q", want)
	}
}

func normalizeSQL(sql string) string {
	return strings.ToLower(strings.Join(strings.Fields(sql), " "))
}
