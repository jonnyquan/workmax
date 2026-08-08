package migrations

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"gorm.io/gorm"

	"server/utils/testutil"
)

const agentTurnReservationSettlementLedgerMigrationFile = "20260812_create_agent_turn_reservation_settlement_ledger.sql"

func TestAgentTurnReservationSettlementLedgerMigrationGuardsBeforeDDL(t *testing.T) {
	raw, err := os.ReadFile(agentTurnReservationSettlementLedgerMigrationFile)
	if err != nil {
		t.Fatalf("read %s: %v", agentTurnReservationSettlementLedgerMigrationFile, err)
	}
	normalized := normalizeSQL(string(raw))
	firstPersistentDDL := strings.Index(normalized, "alter table `w_agent_turn`")
	if firstPersistentDDL < 0 {
		t.Fatal("migration is missing the first parent-key ALTER")
	}

	for _, want := range []string{
		"oracle mysql 8.0.19+ only",
		"apply 20260665 through 20260671 and 20260807 first",
		"stop agent start",
		"stop the reservation expiry/refund sweeper",
		"one physical mysql session",
		"stop on the first error",
		"empty agent turn graph",
		"do not manufacture a binding or outcome",
		"create temporary table `_w_agent_settlement_version_guard`",
		"regexp_substr(version(), '^[0-9]+[.][0-9]+[.][0-9]+')",
		"locate('mariadb', lower(version())) = 0",
		"@w_agent_settlement_version_patch >= 19",
		"create temporary table `_w_agent_settlement_baseline_guard`",
		"@@session.foreign_key_checks = 1",
		"@@session.check_constraint_checks = 1",
		"@@session.unique_checks = 1",
		"@@session.time_zone = '+00:00'",
		"upper(@@session.transaction_isolation) in ('read-committed', 'repeatable-read')",
		"timestampdiff(second, utc_timestamp(6), current_timestamp(6)) = 0",
		"@@innodb_page_size >= 16384",
		"'w_agent_turn', 'w_agent_turn_operation', 'w_agent_turn_settlement_review', 'w_credit_reservation'",
		"'w_credit_reservation_allocation'",
		"upper(coalesce(`row_format`, '')) = 'dynamic'",
		"and `index_name` <> 'primary' group by `table_name` having count(distinct `index_name`) > case `table_name` when 'w_agent_turn' then 62 else 63 end",
		"`partition_name` is not null",
		"lower(coalesce(`extra`, '')) not like '%generated%'",
		"('w_agent_turn', 'uk_w_agent_turn_turn_id')",
		"('w_agent_turn_operation', 'uk_w_agent_turn_operation_binding')",
		"'uk_w_agent_turn_settlement_review_resolution_binding'",
		"('w_credit_reservation', 'idx_reservation_uid_key')",
		"'uk_w_credit_reservation_allocation_pair'",
		"sum(upper(`is_visible`) <> 'yes') as `invisible_columns`",
		"as `required_primary_fingerprints`",
		"and `ordered_columns` = 'id'",
		"inner join `information_schema`.`check_constraints` as `cc`",
		"and upper(`tc`.`enforced`) = 'yes'",
		"'chk_w_credit_reservation_status'",
		"'chk_w_credit_reservation_amounts'",
		"'chk_w_credit_reservation_refund_tuple'",
		"'chk_w_credit_reservation_allocation_credits'",
		"'fk_w_credit_reservation_allocation_reservation'",
		"group_concat(`kcu`.`column_name` order by `kcu`.`ordinal_position` separator ',') as `ordered_columns`",
		"and `column_count` = 1",
		"as `required_reservation_fk_fingerprint`",
		"create temporary table `_w_agent_settlement_history_guard`",
		"not exists (select 1 from `w_agent_turn` limit 1)",
		"binary `status` = 'review_hold'",
		"binary `request_digest` regexp '[^0-9a-f]'",
		"create temporary table `_w_agent_settlement_target_guard`",
		"'w_agent_turn_reservation_binding', 'w_agent_turn_settlement_outcome'",
		"'uk_w_agent_turn_reservation_identity'",
		"'uk_w_agent_turn_operation_settlement_binding'",
		"'uk_w_agent_turn_settlement_review_outcome_binding'",
		"'uk_w_credit_reservation_agent_binding'",
		"'chk_w_agent_turn_reservation_binding_identity'",
		"'chk_w_agent_turn_reservation_binding_amounts'",
		"'chk_w_agent_turn_reservation_binding_digests'",
		"'chk_w_agent_turn_settlement_outcome_identity'",
		"'chk_w_agent_turn_settlement_outcome_digests'",
		"'chk_w_agent_turn_settlement_outcome_authorization'",
		"'chk_w_agent_turn_settlement_outcome_terminal'",
		"'chk_w_agent_turn_settlement_outcome_amounts'",
		"'chk_w_agent_turn_settlement_outcome_review_tuple'",
		"'chk_w_agent_turn_settlement_outcome_resolution_tuple'",
		"'chk_w_agent_turn_settlement_outcome_state_tuple'",
		"'chk_w_agent_turn_settlement_outcome_updated_time'",
	} {
		position := strings.Index(normalized, want)
		if position < 0 {
			t.Errorf("migration safety guard missing %q", want)
		} else if position >= firstPersistentDDL {
			t.Errorf("migration safety guard %q must precede persistent DDL", want)
		}
	}
	for _, guard := range []string{
		"_w_agent_settlement_version_guard",
		"_w_agent_settlement_baseline_guard",
		"_w_agent_settlement_history_guard",
		"_w_agent_settlement_target_guard",
	} {
		if !strings.Contains(normalized, "insert into `"+guard+"` (`guard_key`) values (0)") ||
			!strings.Contains(normalized, "drop temporary table `"+guard+"`") {
			t.Errorf("guard %s must install and remove its duplicate-key sentinel", guard)
		}
	}

	if count := regexp.MustCompile(`(?mi)^ALTER TABLE `+"`").FindAll(raw, -1); len(count) != 4 {
		t.Fatalf("persistent parent ALTER count = %d, want 4", len(count))
	}
	if strings.Count(normalized, "create table `w_agent_turn_reservation_binding`") != 1 ||
		strings.Count(normalized, "create table `w_agent_turn_settlement_outcome`") != 1 {
		t.Fatal("migration must create exactly one binding and one outcome table")
	}
	if strings.Contains(normalized, "create table if not exists `w_agent_turn_") {
		t.Fatal("target CREATE must not silently accept an existing drifted table")
	}
	for _, target := range []string{
		"w_agent_turn_reservation_binding", "w_agent_turn_settlement_outcome",
	} {
		for _, pattern := range []*regexp.Regexp{
			regexp.MustCompile(`(?i)\bupdate\s+` + "`?" + target),
			regexp.MustCompile(`(?i)\bdelete\s+from\s+` + "`?" + target),
			regexp.MustCompile(`(?i)\binsert\s+into\s+` + "`?" + target + "(?:`|\\s)"),
		} {
			if pattern.Match(raw) {
				t.Fatalf("migration must not synthesize or rewrite %s rows", target)
			}
		}
	}
	for _, note := range []string{
		"individually atomic, but not atomic as one six-statement migration",
		"do not rerun the whole file",
		"forward-resume only the first missing reviewed statement",
		"never infer a reservation from a principal",
		"bound agent reservations must never be expired directly",
	} {
		if !strings.Contains(normalized, note) {
			t.Errorf("migration recovery contract missing %q", note)
		}
	}

	identifierPattern := regexp.MustCompile("(?i)(?:constraint|(?:unique )?key) `([^`]+)`")
	for _, match := range identifierPattern.FindAllStringSubmatch(string(raw), -1) {
		if len(match[1]) > 64 {
			t.Fatalf("MySQL identifier %q is %d bytes, want <= 64", match[1], len(match[1]))
		}
	}
}

func TestAgentTurnReservationSettlementLedgerMigrationPinsExactSchema(t *testing.T) {
	raw, err := os.ReadFile(agentTurnReservationSettlementLedgerMigrationFile)
	if err != nil {
		t.Fatalf("read %s: %v", agentTurnReservationSettlementLedgerMigrationFile, err)
	}
	normalized := normalizeSQL(string(raw))
	bindingStart := strings.Index(normalized, "create table `w_agent_turn_reservation_binding`")
	outcomeStart := strings.Index(normalized, "create table `w_agent_turn_settlement_outcome`")
	if bindingStart < 0 || outcomeStart <= bindingStart {
		t.Fatal("binding must be created before its outcome child")
	}
	parentDDL := normalized[:bindingStart]
	bindingDDL := normalized[bindingStart:outcomeStart]
	outcomeDDL := normalized[outcomeStart:]

	for _, want := range []string{
		"add unique key `uk_w_agent_turn_reservation_identity` (`turn_id`, `principal_id`, `command_digest`)",
		"add unique key `uk_w_agent_turn_settlement_fence` (`turn_id`, `fencing_token`, `status`)",
		"add unique key `uk_w_agent_turn_operation_settlement_binding` (`turn_id`, `operation_id`, `attempt_id`, `fencing_token`, `turn_status`)",
		"add unique key `uk_w_agent_turn_settlement_review_outcome_binding` (`review_id`, `turn_id`, `settlement_key`, `request_digest`, `terminal_status`)",
		"modify column `tool` varchar(64) character set utf8mb4 collate utf8mb4_bin not null",
		"add unique key `uk_w_credit_reservation_agent_binding` (`id`, `uid`, `request_digest`, `tool`, `reserved`, `project_id`)",
		"drop constraint `chk_w_credit_reservation_digests`",
		"binary `request_digest` not regexp '[^0-9a-f]'",
		"binary substring(`hold_request_digest`, 8) not regexp '[^0-9a-f]'",
	} {
		assertSQLContains(t, parentDDL, want)
	}

	for _, want := range []string{
		"`binding_id` char(64) character set ascii collate ascii_bin not null",
		"`turn_id` varchar(256) character set utf8mb4 collate utf8mb4_bin not null",
		"`principal_id` varchar(128) character set utf8mb4 collate utf8mb4_bin not null",
		"`turn_command_digest` varchar(128) character set ascii collate ascii_bin not null",
		"`reservation_id` bigint unsigned not null",
		"`reservation_uid` int not null",
		"`reservation_request_digest` varchar(64) character set ascii collate ascii_bin not null",
		"`reservation_tool` varchar(64) character set utf8mb4 collate utf8mb4_bin not null",
		"`reserved_units` int not null",
		"`project_id` int not null",
		"`pricing_snapshot_digest` char(71) character set ascii collate ascii_bin not null",
		"`binding_digest` char(71) character set ascii collate ascii_bin not null",
		"`created_at` datetime(6) not null default current_timestamp(6)",
		"unique key `uk_w_agent_turn_reservation_binding_binding_id` (`binding_id`)",
		"unique key `uk_w_agent_turn_reservation_binding_turn_id` (`turn_id`)",
		"unique key `uk_w_agent_turn_reservation_binding_reservation_id` (`reservation_id`)",
		"unique key `uk_w_agent_turn_reservation_binding_exact` (`binding_id`, `turn_id`, `reservation_id`, `binding_digest`, `reserved_units`)",
		"foreign key (`turn_id`, `principal_id`, `turn_command_digest`) references `w_agent_turn` (`turn_id`, `principal_id`, `command_digest`) on delete restrict on update restrict",
		"foreign key (`reservation_id`, `reservation_uid`, `reservation_request_digest`, `reservation_tool`, `reserved_units`, `project_id`) references `w_credit_reservation` (`id`, `uid`, `request_digest`, `tool`, `reserved`, `project_id`) on delete restrict on update restrict",
		"octet_length(`binding_id`) = 64",
		"binary substring(`pricing_snapshot_digest`, 8) not regexp '[^0-9a-f]'",
		") engine=innodb row_format=dynamic",
	} {
		assertSQLContains(t, bindingDDL, want)
	}

	for _, want := range []string{
		"`outcome_id` char(64) character set ascii collate ascii_bin not null",
		"`binding_id` char(64) character set ascii collate ascii_bin not null",
		"`settlement_key` varchar(256) character set ascii collate ascii_bin not null",
		"`ledger_request_digest` char(71) character set ascii collate ascii_bin not null",
		"`authorization_kind` varchar(16) character set ascii collate ascii_bin not null",
		"`attempt_id` varchar(64) character set ascii collate ascii_bin default null",
		"`fencing_token` bigint unsigned not null",
		"`operation_id` varchar(128) character set ascii collate ascii_bin default null",
		"`requested_intent` varchar(16) character set ascii collate ascii_bin not null",
		"`used_units` int default null",
		"`status` varchar(16) character set ascii collate ascii_bin not null",
		"`refund_target` varchar(16) character set ascii collate ascii_bin default null",
		"`reservation_state_version` bigint unsigned not null",
		"`review_id` varchar(64) character set ascii collate ascii_bin default null",
		"`review_request_digest` varchar(128) character set ascii collate ascii_bin default null",
		"`resolution_id` varchar(64) character set ascii collate ascii_bin default null",
		"`resolution_request_digest` varchar(128) character set ascii collate ascii_bin default null",
		"`outcome_digest` char(71) character set ascii collate ascii_bin not null",
		"unique key `uk_w_agent_turn_settlement_outcome_settlement_key` (`settlement_key`)",
		"unique key `uk_w_agent_turn_settlement_outcome_turn_id` (`turn_id`)",
		"unique key `uk_w_agent_turn_settlement_outcome_reservation_id` (`reservation_id`)",
		"unique key `uk_w_agent_turn_settlement_outcome_review_id` (`review_id`)",
		"key `idx_w_agent_turn_settlement_outcome_recovery` (`status`, `updated_at`, `id`)",
		"`status` = 'review_held' and `requested_intent` = 'review'",
		"`status` = 'refund_pending' and `used_units` is not null",
		"`refund_due` = `reserved_units` - `used_units`",
		"`status` = 'finalized' and `requested_intent` in ('finalize', 'review')",
		"`status` = 'released' and `requested_intent` = 'release' and `used_units` = 0",
		"foreign key (`binding_id`, `turn_id`, `reservation_id`, `binding_digest`, `reserved_units`) references `w_agent_turn_reservation_binding` (`binding_id`, `turn_id`, `reservation_id`, `binding_digest`, `reserved_units`) on delete restrict on update restrict",
		"foreign key (`turn_id`, `fencing_token`, `terminal_status`) references `w_agent_turn` (`turn_id`, `fencing_token`, `status`) on delete restrict on update restrict",
		"foreign key (`turn_id`, `operation_id`, `attempt_id`, `fencing_token`, `terminal_status`) references `w_agent_turn_operation` (`turn_id`, `operation_id`, `attempt_id`, `fencing_token`, `turn_status`) on delete restrict on update restrict",
		"foreign key (`review_id`, `turn_id`, `settlement_key`, `review_request_digest`, `terminal_status`) references `w_agent_turn_settlement_review` (`review_id`, `turn_id`, `settlement_key`, `request_digest`, `terminal_status`) on delete restrict on update restrict",
		"no on update clause",
		") engine=innodb row_format=dynamic",
	} {
		assertSQLContains(t, outcomeDDL, want)
	}
	if strings.Contains(outcomeDDL, "references `w_agent_turn_settlement_review_resolution`") {
		t.Fatal("Outcome must not FK a Resolution row that is inserted only after Authority returns")
	}
	if strings.Contains(outcomeDDL, "on update current_timestamp") {
		t.Fatal("Outcome updated_at must be written explicitly by settlement CAS")
	}
}

func TestAgentTurnReservationSettlementLedgerSQLiteMirrorEnforcesOwnershipAndState(t *testing.T) {
	db := testutil.NewTestDB(t)
	for _, index := range []string{
		"uk_w_agent_turn_reservation_identity",
		"uk_w_agent_turn_settlement_fence",
		"uk_w_agent_turn_operation_settlement_binding",
		"uk_w_agent_turn_settlement_review_outcome_binding",
		"uk_w_credit_reservation_agent_binding",
		"uk_w_agent_turn_reservation_binding_binding_id",
		"uk_w_agent_turn_reservation_binding_turn_id",
		"uk_w_agent_turn_reservation_binding_reservation_id",
		"uk_w_agent_turn_reservation_binding_exact",
		"uk_w_agent_turn_settlement_outcome_outcome_id",
		"uk_w_agent_turn_settlement_outcome_settlement_key",
		"uk_w_agent_turn_settlement_outcome_turn_id",
		"uk_w_agent_turn_settlement_outcome_reservation_id",
		"uk_w_agent_turn_settlement_outcome_review_id",
		"idx_w_agent_turn_settlement_outcome_recovery",
	} {
		var count int64
		if err := db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, index).
			Scan(&count).Error; err != nil || count != 1 {
			t.Fatalf("SQLite mirror index %s count=%d err=%v, want 1", index, count, err)
		}
	}
	if err := db.Exec(`INSERT INTO w_credit_reservation
		(uid, tool, idempotency_key, request_digest, reserved, used, status, expires_at)
		VALUES (1, 'agent', 'uppercase-reservation-digest', ?, 1, 0, 'reserved',
		        '2026-08-12 01:00:00')`, strings.Repeat("A", 64)).Error; err == nil {
		t.Fatal("new Reservation request digests must be canonical lowercase hex")
	}

	ordinary := seedSettlementLedgerFixture(t, db, "ordinary", "a", false)
	if err := insertOrdinaryFinalizedOutcome(db, ordinary); err != nil {
		t.Fatalf("insert exact ordinary finalized outcome: %v", err)
	}
	if err := db.Exec(`UPDATE w_agent_turn_settlement_outcome
		SET terminal_status = 'failed' WHERE turn_id = ?`, ordinary.turnID).Error; err == nil {
		t.Fatal("outcome terminal status must match both the Turn fence and Operation receipt")
	}
	if err := db.Exec(`INSERT INTO w_agent_turn_settlement_outcome
		(outcome_id, binding_id, turn_id, reservation_id, binding_digest,
		 settlement_key, ledger_request_digest, authorization_kind,
		 attempt_id, fencing_token, operation_id, terminal_status,
		 requested_intent, used_units, reserved_units, status,
		 refund_due, reservation_state_version, outcome_digest)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'operation', ?, 1, ?, 'completed',
		 'finalize', 10, 10, 'finalized', 0, 2, ?)`,
		strings.Repeat("2", 64), ordinary.bindingID, ordinary.turnID, ordinary.reservationID,
		ordinary.bindingDigest, settlementLedgerKey("2"), settlementLedgerSHA("2"),
		ordinary.attemptID, ordinary.operationID, settlementLedgerSHA("3"),
	).Error; err == nil {
		t.Fatal("a Turn/Reservation must not publish a second SettlementKey outcome")
	}
	if err := db.Exec(`UPDATE w_agent_turn_settlement_outcome
		SET status = 'released', used_units = 0,
		    outcome_digest = ? WHERE turn_id = ?`, settlementLedgerSHA("4"), ordinary.turnID).Error; err == nil {
		t.Fatal("a finalized outcome must not publish release under its frozen finalize intent")
	}
	if err := db.Exec(`UPDATE w_credit_reservation SET request_digest = ? WHERE id = ?`,
		strings.Repeat("9", 64), ordinary.reservationID).Error; err == nil {
		t.Fatal("binding FK must freeze the Reservation request digest")
	}
	if err := db.Exec(`UPDATE w_agent_turn_reservation_binding
		SET binding_digest = ? WHERE turn_id = ?`, settlementLedgerSHA("7"), ordinary.turnID).Error; err == nil {
		t.Fatal("outcome FK must freeze the exact admission binding digest")
	}
	if err := db.Exec(`DELETE FROM w_agent_turn WHERE turn_id = ?`, ordinary.turnID).Error; err == nil {
		t.Fatal("binding/outcome FKs must RESTRICT deletion of the Turn owner")
	}
	if err := db.Exec(`DELETE FROM w_credit_reservation WHERE id = ?`, ordinary.reservationID).Error; err == nil {
		t.Fatal("binding FK must RESTRICT deletion of the Reservation owner")
	}

	reviewed := seedSettlementLedgerFixture(t, db, "reviewed", "b", true)
	if err := insertReviewHeldOutcome(db, reviewed); err != nil {
		t.Fatalf("insert exact review-held outcome: %v", err)
	}
	if err := db.Exec(`UPDATE w_agent_turn_settlement_outcome
		SET status = 'refund_pending', used_units = 4,
		    refund_target = 'finalized', refund_due = 5,
		    resolution_id = ?, resolution_request_digest = ?,
		    reservation_state_version = 3, outcome_digest = ?
		WHERE turn_id = ?`, strings.Repeat("c", 64), settlementLedgerSHA("c"),
		settlementLedgerSHA("d"), reviewed.turnID).Error; err == nil {
		t.Fatal("refund_pending must freeze exact reserved-used refund_due")
	}
	if err := db.Exec(`UPDATE w_agent_turn_settlement_outcome
		SET status = 'refund_pending', used_units = 4,
		    refund_target = 'finalized', refund_due = 6,
		    resolution_id = ?, resolution_request_digest = ?,
		    reservation_state_version = 3, outcome_digest = ?
		WHERE turn_id = ?`, strings.Repeat("c", 64), settlementLedgerSHA("c"),
		settlementLedgerSHA("d"), reviewed.turnID).Error; err != nil {
		t.Fatalf("advance exact review outcome to refund_pending: %v", err)
	}
	if err := db.Exec(`UPDATE w_agent_turn_settlement_outcome
		SET status = 'finalized', refund_target = NULL, refund_due = 0,
		    reservation_state_version = 4, outcome_digest = ?
		WHERE turn_id = ?`, settlementLedgerSHA("e"), reviewed.turnID).Error; err != nil {
		t.Fatalf("advance exact review outcome to finalized: %v", err)
	}
	if err := db.Exec(`UPDATE w_agent_turn_settlement_outcome
		SET status = 'released', requested_intent = 'release', used_units = 0,
		    resolution_id = NULL, resolution_request_digest = NULL,
		    outcome_digest = ? WHERE turn_id = ?`, settlementLedgerSHA("f"), reviewed.turnID).Error; err == nil {
		t.Fatal("a review-owned outcome must not be rewritten as ordinary release")
	}

	conflict := seedSettlementLedgerFixture(t, db, "conflict", "d", true)
	if err := db.Exec(`INSERT INTO w_agent_turn_settlement_outcome
		(outcome_id, binding_id, turn_id, reservation_id, binding_digest,
		 settlement_key, ledger_request_digest, authorization_kind,
		 attempt_id, fencing_token, operation_id, terminal_status,
		 requested_intent, reserved_units, status, refund_due,
		 reservation_state_version, review_id, review_request_digest, outcome_digest)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'operation', ?, 1, ?, 'completed',
		 'review', 10, 'review_held', 0, 2, ?, ?, ?)`,
		strings.Repeat("e", 64), conflict.bindingID, conflict.turnID, conflict.reservationID,
		conflict.bindingDigest, conflict.settlementKey, settlementLedgerSHA("e"),
		conflict.attemptID, conflict.operationID, conflict.reviewID,
		settlementLedgerSHA("0"), settlementLedgerSHA("f"),
	).Error; err == nil {
		t.Fatal("review outcome must FK the exact Review request digest")
	}
	if err := db.Exec(`UPDATE w_agent_turn_reservation_binding
		SET principal_id = 'principal-mismatch' WHERE turn_id = ?`, conflict.turnID).Error; err == nil {
		t.Fatal("binding must FK the exact Turn principal and Reservation tuple")
	}
}

type settlementLedgerFixture struct {
	turnID, attemptID, operationID string
	bindingID, bindingDigest       string
	settlementKey                  string
	reviewID, reviewRequestDigest  string
	reservationID                  int64
}

func seedSettlementLedgerFixture(
	t *testing.T,
	db *gorm.DB,
	suffix string,
	hex string,
	withReview bool,
) settlementLedgerFixture {
	t.Helper()
	fixture := settlementLedgerFixture{
		turnID:              "turn-" + suffix,
		attemptID:           "attempt-" + suffix,
		operationID:         "operation-" + suffix,
		bindingID:           strings.Repeat(hex, 64),
		bindingDigest:       settlementLedgerSHA(hex),
		settlementKey:       settlementLedgerKey(hex),
		reviewID:            strings.Repeat(hex, 64),
		reviewRequestDigest: settlementLedgerSHA(hex),
	}
	commandDigest := "cmd:" + suffix
	if err := db.Exec(`INSERT INTO w_agent_turn
		(turn_id, principal_id, thread_id, idempotency_key, command_digest,
		 plugin_snapshot_json, status, last_event_sequence, fencing_token,
		 finished_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, '{}', 'completed', 1, 1,
		 '2026-08-12 00:00:03', '2026-08-12 00:00:00', '2026-08-12 00:00:03')`,
		fixture.turnID, "principal-"+suffix, "thread-"+suffix, "key-"+suffix, commandDigest,
	).Error; err != nil {
		t.Fatalf("seed Turn %s: %v", suffix, err)
	}
	if err := db.Exec(`INSERT INTO w_agent_turn_event
		(turn_id, sequence, event_id, schema_version, event_type, event_json, created_at)
		VALUES (?, 1, ?, 1, 'core.completed', '{}', '2026-08-12 00:00:03')`,
		fixture.turnID, "event-"+suffix,
	).Error; err != nil {
		t.Fatalf("seed Event %s: %v", suffix, err)
	}
	if err := db.Exec(`INSERT INTO w_agent_turn_attempt
		(attempt_id, turn_id, fencing_token, status, worker_id, worker_build_digest,
		 claimed_at, last_heartbeat_at, lease_expires_at, finished_at, created_at, updated_at)
		VALUES (?, ?, 1, 'completed', 'worker', 'build',
		 '2026-08-12 00:00:00', '2026-08-12 00:00:01', '2026-08-12 00:01:00',
		 '2026-08-12 00:00:03', '2026-08-12 00:00:00', '2026-08-12 00:00:03')`,
		fixture.attemptID, fixture.turnID,
	).Error; err != nil {
		t.Fatalf("seed Attempt %s: %v", suffix, err)
	}
	if err := db.Exec(`INSERT INTO w_agent_turn_operation
		(turn_id, operation_id, operation_digest, attempt_id, fencing_token,
		 event_sequence, turn_status, effect_count, created_at)
		VALUES (?, ?, ?, ?, 1, 1, 'completed', 0, '2026-08-12 00:00:03')`,
		fixture.turnID, fixture.operationID, "operation-digest:"+suffix, fixture.attemptID,
	).Error; err != nil {
		t.Fatalf("seed Operation %s: %v", suffix, err)
	}

	status := "reserved"
	holdReviewID, holdSettlementKey, holdRequestDigest := any(nil), any(nil), any(nil)
	reviewHeldAt := any(nil)
	if withReview {
		status = "review_hold"
		holdReviewID = fixture.reviewID
		holdSettlementKey = fixture.settlementKey
		holdRequestDigest = fixture.reviewRequestDigest
		reviewHeldAt = "2026-08-12 00:00:03"
	}
	requestDigest := strings.Repeat(hex, 64)
	if err := db.Exec(`INSERT INTO w_credit_reservation
		(uid, tool, idempotency_key, request_digest, reserved, used, status,
		 expires_at, hold_review_id, hold_settlement_key, hold_request_digest,
		 review_held_at, state_changed_at, state_version,
		 created_at, updated_at)
		VALUES (1, 'agent', ?, ?, 10, 0, ?, '2026-08-12 01:00:00',
		 ?, ?, ?, ?, '2026-08-12 00:00:03', 1,
		 '2026-08-12 00:00:00', '2026-08-12 00:00:03')`,
		"reservation-"+suffix, requestDigest, status,
		holdReviewID, holdSettlementKey, holdRequestDigest, reviewHeldAt,
	).Error; err != nil {
		t.Fatalf("seed Reservation %s: %v", suffix, err)
	}
	if err := db.Raw(`SELECT id FROM w_credit_reservation WHERE idempotency_key = ?`, "reservation-"+suffix).
		Scan(&fixture.reservationID).Error; err != nil || fixture.reservationID == 0 {
		t.Fatalf("read Reservation %s: id=%d err=%v", suffix, fixture.reservationID, err)
	}
	if err := db.Exec(`INSERT INTO w_agent_turn_reservation_binding
		(binding_id, turn_id, principal_id, turn_command_digest,
		 reservation_id, reservation_uid, reservation_request_digest,
		 reservation_tool, reserved_units, project_id,
		 pricing_snapshot_digest, binding_digest, created_at)
		VALUES (?, ?, ?, ?, ?, 1, ?, 'agent', 10, 0, ?, ?, '2026-08-12 00:00:00')`,
		fixture.bindingID, fixture.turnID, "principal-"+suffix, commandDigest,
		fixture.reservationID, requestDigest, settlementLedgerSHA("1"), fixture.bindingDigest,
	).Error; err != nil {
		t.Fatalf("seed Binding %s: %v", suffix, err)
	}
	if withReview {
		if err := db.Exec(`INSERT INTO w_agent_turn_settlement_review
			(review_id, turn_id, settlement_key, request_digest, reason, source,
			 terminal_status, attempt_id, fencing_token, operation_id,
			 prior_operation_count, prior_effect_count, prior_provider_usage_count,
			 current_effect_count, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'completed_usage_unmeasured', 'executor_completion',
			 'completed', ?, 1, ?, 0, 0, 0, 0, 'pending',
			 '2026-08-12 00:00:03', '2026-08-12 00:00:03')`,
			fixture.reviewID, fixture.turnID, fixture.settlementKey,
			fixture.reviewRequestDigest, fixture.attemptID, fixture.operationID,
		).Error; err != nil {
			t.Fatalf("seed Review %s: %v", suffix, err)
		}
	}
	return fixture
}

func insertOrdinaryFinalizedOutcome(db *gorm.DB, fixture settlementLedgerFixture) error {
	return db.Exec(`INSERT INTO w_agent_turn_settlement_outcome
		(outcome_id, binding_id, turn_id, reservation_id, binding_digest,
		 settlement_key, ledger_request_digest, authorization_kind,
		 attempt_id, fencing_token, operation_id, terminal_status,
		 requested_intent, used_units, reserved_units, status,
		 refund_due, reservation_state_version, outcome_digest,
		 created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'operation', ?, 1, ?, 'completed',
		 'finalize', 10, 10, 'finalized', 0, 2, ?,
		 '2026-08-12 00:00:03', '2026-08-12 00:00:03')`,
		strings.Repeat("1", 64), fixture.bindingID, fixture.turnID, fixture.reservationID,
		fixture.bindingDigest, fixture.settlementKey, settlementLedgerSHA("2"),
		fixture.attemptID, fixture.operationID, settlementLedgerSHA("3"),
	).Error
}

func insertReviewHeldOutcome(db *gorm.DB, fixture settlementLedgerFixture) error {
	return db.Exec(`INSERT INTO w_agent_turn_settlement_outcome
		(outcome_id, binding_id, turn_id, reservation_id, binding_digest,
		 settlement_key, ledger_request_digest, authorization_kind,
		 attempt_id, fencing_token, operation_id, terminal_status,
		 requested_intent, reserved_units, status, refund_due,
		 reservation_state_version, review_id, review_request_digest,
		 outcome_digest, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'operation', ?, 1, ?, 'completed',
		 'review', 10, 'review_held', 0, 2, ?, ?, ?,
		 '2026-08-12 00:00:03', '2026-08-12 00:00:03')`,
		strings.Repeat("9", 64), fixture.bindingID, fixture.turnID, fixture.reservationID,
		fixture.bindingDigest, fixture.settlementKey, settlementLedgerSHA("9"),
		fixture.attemptID, fixture.operationID, fixture.reviewID,
		fixture.reviewRequestDigest, settlementLedgerSHA("8"),
	).Error
}

func settlementLedgerSHA(hex string) string {
	return "sha256:" + strings.Repeat(hex, 64)
}

func settlementLedgerKey(hex string) string {
	return "wm:turn-settlement:v1:" + strings.Repeat(hex, 64)
}
