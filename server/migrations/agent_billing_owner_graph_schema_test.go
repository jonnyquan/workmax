package migrations

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"server/utils/testutil"
)

const agentBillingOwnerGraphMigrationFile = "20260813_harden_agent_billing_owner_graph.sql"

func TestAgentBillingOwnerGraphMigrationPinsGuardedForwardResume(t *testing.T) {
	raw, err := os.ReadFile(agentBillingOwnerGraphMigrationFile)
	if err != nil {
		t.Fatalf("read %s: %v", agentBillingOwnerGraphMigrationFile, err)
	}
	normalized := normalizeSQL(string(raw))
	executable := normalizeSQL(agentBillingOwnerGraphExecutableSQL(string(raw)))

	for _, want := range []string{
		"oracle mysql 8.0.19+ only",
		"three-alter migration is not a",
		"successful ddl auto-commits and remains durable",
		"one physical mysql session",
		"stop on the",
		"first error",
		"external maintenance fence",
		"fresh read-only session",
		"never retry blindly",
		"absent, exact or",
		"drift, and only then forward-resume",
		"forward-resume",
		"create temporary table `_w_agent_billing_owner_version_guard`",
		"regexp_substr(version(), '^[0-9]+[.][0-9]+[.][0-9]+')",
		"locate('mariadb', lower(version())) = 0",
		"locate('mariadb', lower(@@version_comment)) = 0",
		"@w_agent_billing_owner_version_patch >= 19",
		"get_lock(@w_agent_billing_owner_lock_name, 0)",
		"@@session.foreign_key_checks = 1",
		"@@session.check_constraint_checks = 1",
		"@@session.unique_checks = 1",
		"@@session.time_zone = '+00:00'",
		"upper(@@session.transaction_isolation) in ('read-committed', 'repeatable-read')",
		"timestampdiff(second, utc_timestamp(6), current_timestamp(6)) = 0",
		"@@innodb_page_size >= 16384",
		"`table_name` in ( 'w_global_project', 'w_credit_reservation', 'w_credit_reservation_allocation', 'w_credits_pack', 'w_order' )",
		"`partition_name` is not null",
		"`table_name` = 'w_order' and upper(`row_format`) = 'dynamic'",
		"`column_name` = 'budget_credits_cap' and `data_type` = 'int' and `column_type` = 'int' and `is_nullable` = 'yes'",
		"`column_name` = 'budget_credits_used' and `data_type` = 'int' and `column_type` = 'int' and `is_nullable` = 'no'",
		"`table_name` = 'w_credit_reservation_allocation' and `column_name` in ('reservation_id', 'pack_id') and `data_type` = 'bigint' and `column_type` = 'bigint unsigned'",
		"`table_name` = 'w_credits_pack' and `column_name` = 'id' and `data_type` = 'bigint' and `column_type` = 'bigint unsigned'",
		"`index_name` = 'idx_credit_reservation_allocation_pack'",
		"`ordered_columns` = 'pack_id'",
		"`column_name` in ('order_type', 'status') and `data_type` in ('char', 'varchar')",
		"`column_name` = 'pay_time' and `data_type` in ('datetime', 'timestamp')",
		"`column_name` = 'id' and `data_type` in ('int', 'bigint') and `column_type` in ('int', 'int unsigned', 'bigint', 'bigint unsigned') and `is_nullable` = 'no'",
		"sum(`character_octet_length`), 3073",
		") <= 3000",
		"where (`budget_credits_cap` is not null and `budget_credits_cap` < 0) or `budget_credits_used` < 0",
		"left join `w_credit_reservation` as `r` on `r`.`id` = `a`.`reservation_id`",
		"left join `w_credits_pack` as `p` on `p`.`id` = `a`.`pack_id`",
		"or `r`.`uid` <> `p`.`uid`",
		"@w_agent_billing_budget_state = case",
		"@w_agent_billing_budget_clause_mysql = '((budget_credits_capisnull)or(budget_credits_cap>=0))and(budget_credits_used>=0)'",
		"@w_agent_billing_pack_fk_state = case",
		"@w_agent_billing_order_index_state = case",
		"then 'absent'",
		"then 'exact'",
		"else 'drift'",
		"`tc`.`enforced` = 'yes'",
		"inner join `information_schema`.`check_constraints` as `cc`",
		"@w_agent_billing_budget_shape_count",
		"@w_agent_billing_budget_touch_count",
		"locate('budget_credits_cap', lower(replace(`cc`.`check_clause`, '`', ''))) > 0",
		"locate('budget_credits_used', lower(replace(`cc`.`check_clause`, '`', ''))) > 0",
		"@w_agent_billing_pack_fk_touch_count",
		"count(distinct `kcu`.`constraint_name`)",
		"`kcu`.`column_name` = 'pack_id'",
		"`rc`.`unique_constraint_name` = 'primary'",
		"`key_parts` = 1 and `child_columns` = 'pack_id'",
		"`parent_tables` = 'w_credits_pack'",
		"`parent_columns` = 'id'",
		"`parent_positions` = '1'",
		"`rc`.`update_rule` = 'restrict'",
		"`rc`.`delete_rule` = 'restrict'",
		"@w_agent_billing_order_index_shape_count",
		"`ordered_columns` = 'uid,order_type,status,pay_time,id'",
		"`non_unique` = 1",
		"upper(`index_type`) = 'btree'",
		"`prefix_parts` = 0",
		"`expression_parts` = 0",
		"`invisible_parts` = 0",
		"`nonascending_parts` = 0",
		"@w_agent_billing_budget_state in ('absent', 'exact')",
		"@w_agent_billing_pack_fk_state in ('absent', 'exact')",
		"@w_agent_billing_order_index_state in ('absent', 'exact')",
		"@w_agent_billing_order_index_state = 'exact' or ( select count(distinct `index_name`) from `information_schema`.`statistics` where `table_schema` = database() and `table_name` = 'w_order' and `index_name` <> 'primary' ) <= 63",
		"alter table `w_global_project` add constraint `chk_w_global_project_budget_credits` check ((`budget_credits_cap` is null or `budget_credits_cap` >= 0) and `budget_credits_used` >= 0) enforced",
		"alter table `w_credit_reservation_allocation` add constraint `fk_w_credit_reservation_allocation_pack` foreign key (`pack_id`) references `w_credits_pack` (`id`) on delete restrict on update restrict",
		"alter table `w_order` add index `idx_w_order_membership_resolution` (`uid`, `order_type`, `status`, `pay_time`, `id`) visible",
		"release_lock(@w_agent_billing_owner_lock_name)",
	} {
		if !strings.Contains(normalized, want) {
			t.Errorf("migration safety contract missing %q", want)
		}
	}

	firstPersistentExecution := strings.Index(normalized, "execute `w_agent_billing_project_stmt`")
	if firstPersistentExecution < 0 {
		t.Fatal("migration is missing the first conditional DDL execution")
	}
	for _, guard := range []string{
		"create temporary table `_w_agent_billing_owner_version_guard`",
		"create temporary table `_w_agent_billing_owner_session_guard`",
		"create temporary table `_w_agent_billing_owner_schema_guard`",
		"create temporary table `_w_agent_billing_owner_data_guard`",
		"set @w_agent_billing_budget_state = case",
		"set @w_agent_billing_pack_fk_state = case",
		"set @w_agent_billing_order_index_state = case",
		"create temporary table `_w_agent_billing_owner_target_guard`",
	} {
		position := strings.Index(normalized, guard)
		if position < 0 || position >= firstPersistentExecution {
			t.Errorf("preflight %q must finish before the first conditional DDL", guard)
		}
	}

	orderedStages := []string{
		"execute `w_agent_billing_project_stmt`",
		"create temporary table `_w_agent_billing_project_post_guard`",
		"execute `w_agent_billing_pack_fk_stmt`",
		"create temporary table `_w_agent_billing_pack_fk_post_guard`",
		"execute `w_agent_billing_order_index_stmt`",
		"create temporary table `_w_agent_billing_order_index_post_guard`",
		"release_lock(@w_agent_billing_owner_lock_name)",
	}
	previous := -1
	for _, stage := range orderedStages {
		position := strings.Index(normalized, stage)
		if position <= previous {
			t.Fatalf("migration stage %q is missing or out of order", stage)
		}
		previous = position
	}

	for _, table := range []string{
		"w_global_project",
		"w_credit_reservation_allocation",
		"w_order",
	} {
		if count := strings.Count(executable, "alter table `"+table+"`"); count != 1 {
			t.Errorf("persistent ALTER count for %s = %d, want 1", table, count)
		}
	}
	if count := strings.Count(executable, "alter table `"); count != 3 {
		t.Fatalf("business ALTER count = %d, want exactly 3", count)
	}
	for _, statement := range []string{
		"w_agent_billing_project_stmt",
		"w_agent_billing_pack_fk_stmt",
		"w_agent_billing_order_index_stmt",
	} {
		if count := strings.Count(executable, "prepare `"+statement+"` from"); count != 1 {
			t.Errorf("PREPARE count for %s = %d, want 1", statement, count)
		}
		if count := strings.Count(executable, "deallocate prepare `"+statement+"`"); count != 1 {
			t.Errorf("DEALLOCATE count for %s = %d, want 1", statement, count)
		}
	}

	for _, forbidden := range []string{
		"if not exists",
		"foreign_key_checks = 0",
		"check_constraint_checks = 0",
		"unique_checks = 0",
		"modify column",
		"drop constraint",
		"drop foreign key",
		"drop index",
		"start transaction",
	} {
		if strings.Contains(executable, forbidden) {
			t.Errorf("migration executable SQL contains forbidden operation %q", forbidden)
		}
	}
	transactionControl := regexp.MustCompile(`(?i)(?:^|;)\s*(?:commit|rollback)\s*;`)
	if transactionControl.MatchString(agentBillingOwnerGraphExecutableSQL(string(raw))) {
		t.Fatal("migration must not imply transactional rollback around auto-commit DDL")
	}
	businessDML := regexp.MustCompile("(?i)\\b(?:update|delete\\s+from|insert\\s+into)\\s+`?(?:w_global_project|w_credit_reservation|w_credit_reservation_allocation|w_credits_pack|w_order)`?\\b")
	if businessDML.MatchString(agentBillingOwnerGraphExecutableSQL(string(raw))) {
		t.Fatal("migration must not mutate, delete or synthesize business rows")
	}

	identifierPattern := regexp.MustCompile("(?i)(?:constraint|(?:unique )?(?:key|index)) `([^`]+)`")
	for _, match := range identifierPattern.FindAllStringSubmatch(string(raw), -1) {
		if len(match[1]) > 64 {
			t.Fatalf("MySQL identifier %q is %d bytes, want <= 64", match[1], len(match[1]))
		}
	}
}

func TestAgentBillingOwnerGraphBudgetCheckFingerprintCoversDDLAndMySQLParenthesization(t *testing.T) {
	sourceClause := "(`budget_credits_cap` IS NULL OR `budget_credits_cap` >= 0) AND `budget_credits_used` >= 0"
	mysqlClause := "((`budget_credits_cap` is null) or (`budget_credits_cap` >= 0)) and (`budget_credits_used` >= 0)"

	if got, want := agentBillingOwnerGraphCanonicalCheckClause(sourceClause),
		"(budget_credits_capisnullorbudget_credits_cap>=0)andbudget_credits_used>=0"; got != want {
		t.Fatalf("source CHECK canonical form = %q, want %q", got, want)
	}
	if got, want := agentBillingOwnerGraphCanonicalCheckClause(mysqlClause),
		"((budget_credits_capisnull)or(budget_credits_cap>=0))and(budget_credits_used>=0)"; got != want {
		t.Fatalf("MySQL-parenthesized CHECK canonical form = %q, want %q", got, want)
	}

	raw, err := os.ReadFile(agentBillingOwnerGraphMigrationFile)
	if err != nil {
		t.Fatalf("read %s: %v", agentBillingOwnerGraphMigrationFile, err)
	}
	for _, fingerprint := range []string{
		agentBillingOwnerGraphCanonicalCheckClause(sourceClause),
		agentBillingOwnerGraphCanonicalCheckClause(mysqlClause),
	} {
		if !strings.Contains(string(raw), "'"+fingerprint+"'") {
			t.Errorf("migration does not accept CHECK fingerprint %q", fingerprint)
		}
	}
}

func TestAgentBillingOwnerGraphSQLiteMirrorEnforcesAddedObjects(t *testing.T) {
	db := testutil.NewTestDB(t)

	if err := db.Exec(`INSERT INTO w_global_project
		(budget_credits_cap, budget_credits_used) VALUES (NULL, 0)`).Error; err != nil {
		t.Fatalf("insert uncapped project: %v", err)
	}
	if err := db.Exec(`INSERT INTO w_global_project
		(budget_credits_cap, budget_credits_used) VALUES (1, 2)`).Error; err != nil {
		t.Fatalf("historical usage above cap must remain valid: %v", err)
	}
	if err := db.Exec(`INSERT INTO w_global_project
		(budget_credits_cap, budget_credits_used) VALUES (-1, 0)`).Error; err == nil {
		t.Fatal("negative project budget cap bypassed CHECK")
	}
	if err := db.Exec(`INSERT INTO w_global_project
		(budget_credits_cap, budget_credits_used) VALUES (NULL, -1)`).Error; err == nil {
		t.Fatal("negative project budget usage bypassed CHECK")
	}

	for _, pack := range []struct {
		uid      int
		sourceID string
	}{
		{uid: 41, sourceID: "owner-pack"},
		{uid: 42, sourceID: "other-owner-pack"},
	} {
		if err := db.Exec(`INSERT INTO w_credits_pack
			(uid, source_type, source_id, credits_total, credits_used)
			VALUES (?, 'purchase', ?, 20, 0)`, pack.uid, pack.sourceID).Error; err != nil {
			t.Fatalf("insert %s: %v", pack.sourceID, err)
		}
	}
	var ownerPackID, otherPackID int64
	if err := db.Raw(`SELECT id FROM w_credits_pack WHERE source_id = 'owner-pack'`).Scan(&ownerPackID).Error; err != nil || ownerPackID == 0 {
		t.Fatalf("read owner pack: id=%d err=%v", ownerPackID, err)
	}
	if err := db.Raw(`SELECT id FROM w_credits_pack WHERE source_id = 'other-owner-pack'`).Scan(&otherPackID).Error; err != nil || otherPackID == 0 {
		t.Fatalf("read other-owner pack: id=%d err=%v", otherPackID, err)
	}

	future := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	if err := db.Exec(`INSERT INTO w_credit_reservation
		(uid, tool, idempotency_key, reserved, used, status, expires_at)
		VALUES (41, 'owner-graph-test', 'owner-reservation', 10, 0, 'reserved', ?)`, future).Error; err != nil {
		t.Fatalf("insert owner Reservation: %v", err)
	}
	var reservationID int64
	if err := db.Raw(`SELECT id FROM w_credit_reservation
		WHERE idempotency_key = 'owner-reservation'`).Scan(&reservationID).Error; err != nil || reservationID == 0 {
		t.Fatalf("read owner Reservation: id=%d err=%v", reservationID, err)
	}

	if err := db.Exec(`INSERT INTO w_credit_reservation_allocation
		(reservation_id, pack_id, credits) VALUES (?, ?, 5)`, reservationID, ownerPackID).Error; err != nil {
		t.Fatalf("insert owner-matched Allocation: %v", err)
	}
	if err := db.Exec(`INSERT INTO w_credit_reservation_allocation
		(reservation_id, pack_id, credits) VALUES (?, 999999999, 1)`, reservationID).Error; err == nil {
		t.Fatal("orphan pack Allocation bypassed Pack foreign key")
	}
	if err := db.Exec(`DELETE FROM w_credits_pack WHERE id = ?`, ownerPackID).Error; err == nil {
		t.Fatal("deleting an allocated Pack bypassed ON DELETE RESTRICT")
	}
	if err := db.Exec(`UPDATE w_credits_pack SET id = id + 100000 WHERE id = ?`, ownerPackID).Error; err == nil {
		t.Fatal("changing an allocated Pack identity bypassed ON UPDATE RESTRICT")
	}

	// A single-column Pack FK proves Pack existence, not UID equality. The
	// migration's no-backfill data guard must therefore reject cross-owner
	// history before DDL; application transactions remain responsible for future
	// owner matching.
	if err := db.Exec(`INSERT INTO w_credit_reservation_allocation
		(reservation_id, pack_id, credits) VALUES (?, ?, 1)`, reservationID, otherPackID).Error; err != nil {
		t.Fatalf("create cross-owner preflight fixture: %v", err)
	}
	var incompatible int64
	if err := db.Raw(`SELECT COUNT(*)
		FROM w_credit_reservation_allocation AS a
		LEFT JOIN w_credit_reservation AS r ON r.id = a.reservation_id
		LEFT JOIN w_credits_pack AS p ON p.id = a.pack_id
		WHERE r.id IS NULL OR p.id IS NULL OR r.uid <> p.uid`).Scan(&incompatible).Error; err != nil {
		t.Fatalf("evaluate Allocation owner guard mirror: %v", err)
	}
	if incompatible != 1 {
		t.Fatalf("Allocation owner guard incompatible rows = %d, want 1", incompatible)
	}

	type sqliteIndexListRow struct {
		Seq     int    `gorm:"column:seq"`
		Name    string `gorm:"column:name"`
		Unique  int    `gorm:"column:unique"`
		Origin  string `gorm:"column:origin"`
		Partial int    `gorm:"column:partial"`
	}
	var indexes []sqliteIndexListRow
	if err := db.Raw(`PRAGMA index_list('w_order')`).Scan(&indexes).Error; err != nil {
		t.Fatalf("list w_order indexes: %v", err)
	}
	found := false
	for _, index := range indexes {
		if index.Name == "idx_w_order_membership_resolution" {
			found = true
			if index.Unique != 0 || index.Partial != 0 {
				t.Fatalf("membership index unique=%d partial=%d, want ordinary full index", index.Unique, index.Partial)
			}
		}
	}
	if !found {
		t.Fatal("SQLite mirror is missing idx_w_order_membership_resolution")
	}

	type sqliteIndexColumn struct {
		Seqno int    `gorm:"column:seqno"`
		Cid   int    `gorm:"column:cid"`
		Name  string `gorm:"column:name"`
	}
	var columns []sqliteIndexColumn
	if err := db.Raw(`PRAGMA index_info('idx_w_order_membership_resolution')`).Scan(&columns).Error; err != nil {
		t.Fatalf("read membership index columns: %v", err)
	}
	wantColumns := []string{"uid", "order_type", "status", "pay_time", "id"}
	if len(columns) != len(wantColumns) {
		t.Fatalf("membership index column count = %d, want %d", len(columns), len(wantColumns))
	}
	for position, want := range wantColumns {
		if columns[position].Seqno != position || columns[position].Name != want {
			t.Fatalf("membership index part %d = seqno:%d name:%q, want seqno:%d name:%q",
				position, columns[position].Seqno, columns[position].Name, position, want)
		}
	}
}

func agentBillingOwnerGraphExecutableSQL(sql string) string {
	lines := strings.Split(sql, "\n")
	for index, line := range lines {
		if comment := strings.Index(line, "--"); comment >= 0 {
			lines[index] = line[:comment]
		}
	}
	return strings.Join(lines, "\n")
}

func agentBillingOwnerGraphCanonicalCheckClause(clause string) string {
	clause = strings.ToLower(strings.ReplaceAll(clause, "`", ""))
	return strings.Join(strings.Fields(clause), "")
}
