package migrations

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"server/utils/testutil"
)

const creditReservationHardeningMigrationFile = "20260807_harden_credit_reservation.sql"

const gormRegistrationFile = "../initialize/gorm.go"

func TestCreditReservationHardeningMigrationPinsFailClosedStateMachine(t *testing.T) {
	sql := readCreditReservationHardeningMigration(t)
	normalized := normalizeSQL(sql)

	for _, want := range []string{
		"create temporary table `_w_credit_reservation_refund_history_guard`",
		"constraint `chk_w_credit_reservation_refund_history_guard` check (`incompatible_rows` = 0)",
		"binary `status` = 'expired' and lower(coalesce(`remark`, '')) not like '%p0-046-refund-reconciled%'",
		"create temporary table `_w_credit_reservation_integrity_guard`",
		"constraint `chk_w_credit_reservation_integrity_guard` check (`incompatible_rows` = 0)",
		"where `reserved` < 0 or `used` < 0 or `used` > `reserved` or (binary `status` <> 'finalized' and `used` <> 0)",
		"where `r`.`project_id` <> 0 and (`p`.`id` is null or `p`.`uid` <> `r`.`uid`)",
		"where `p`.`uid` <> `r`.`uid`",
		"having sum(`a`.`credits`) > `p`.`credits_used`",
		"having sum(`r`.`reserved`) > `p`.`budget_credits_used`",
		"having count(*) > 1",
		"where coalesce(`a`.`allocated`, 0) <> `r`.`reserved`",
		"alter table `w_credit_reservation` add column `request_digest` varchar(64) character set ascii collate ascii_bin default null",
		"add column `hold_review_id` varchar(256) character set ascii collate ascii_bin default null",
		"add column `hold_settlement_key` varchar(256) character set ascii collate ascii_bin default null",
		"add column `hold_request_digest` varchar(128) character set ascii collate ascii_bin default null",
		"add column `review_held_at` datetime(6) default null",
		"add column `refund_target_status` varchar(16) character set ascii collate ascii_bin default null",
		"add column `refund_target_used` int unsigned default null",
		"add column `refund_due` int unsigned not null default 0",
		"add column `refund_attempts` bigint unsigned not null default 0",
		"add column `next_refund_at` datetime(6) default null",
		"add column `last_refund_error_code` varchar(64) character set ascii collate ascii_bin default null",
		"add column `state_changed_at` datetime(6) default null",
		"add column `state_version` bigint unsigned not null default 0",
		"add unique key `uk_w_credit_reservation_hold_settlement` (`hold_settlement_key`)",
		"add key `idx_w_credit_reservation_sweep` (`status`, `expires_at`, `id`)",
		"add key `idx_w_credit_reservation_refund` (`status`, `next_refund_at`, `id`)",
		"check (binary `status` in ( 'reserved', 'review_hold', 'refund_pending', 'finalized', 'released', 'expired' ))",
		"`used` <= `reserved` and (binary `status` = 'finalized' or `used` = 0) and `refund_due` between 0 and `reserved`",
		"`refund_due` = `reserved` - `refund_target_used`",
		"`refund_target_status` is not null and binary `refund_target_status` in ('finalized', 'released', 'expired')",
		"`refund_due` > 0",
		"binary `refund_target_status` = 'finalized' or `refund_target_used` = 0",
		"constraint `chk_w_credit_reservation_refund_error_code`",
		"alter table `w_credit_reservation_allocation` add unique key `uk_w_credit_reservation_allocation_pair` (`reservation_id`, `pack_id`)",
		"constraint `chk_w_credit_reservation_allocation_credits` check (`credits` > 0)",
		"constraint `fk_w_credit_reservation_allocation_reservation` foreign key (`reservation_id`) references `w_credit_reservation` (`id`) on delete restrict on update restrict",
	} {
		assertSQLContains(t, normalized, want)
	}

	if strings.Contains(normalized, "metered_hold") {
		t.Fatal("P0-046 Reservation status must not invent metered_hold")
	}
	if strings.Contains(normalized, "references `w_credits_pack`") ||
		strings.Contains(normalized, "references `w_agent_") {
		t.Fatal("P0-046 must not add Pack or Agent foreign keys")
	}
	if regexp.MustCompile(`(?i)\bupdate\s+`+"`?w_credit_reservation").MatchString(sql) ||
		regexp.MustCompile(`(?i)\binsert\s+into\s+`+"`?w_credit_reservation(?:`|\\s)").MatchString(sql) {
		t.Fatal("P0-046 must not fabricate Reservation state or refund backfills")
	}

	namePattern := regexp.MustCompile("(?i)(?:constraint|(?:unique )?key) `([^`]+)`")
	for _, match := range namePattern.FindAllStringSubmatch(sql, -1) {
		if len(match[1]) > 64 {
			t.Fatalf("MySQL identifier %q is %d bytes, want <= 64", match[1], len(match[1]))
		}
	}
}

func TestCreditReservationSchemaIsMigrationOwnedNotAutoMigrated(t *testing.T) {
	body, err := os.ReadFile(gormRegistrationFile)
	if err != nil {
		t.Fatalf("read %s: %v", gormRegistrationFile, err)
	}
	if strings.Contains(string(body), "model.CreditReservation{}") {
		t.Fatal("CreditReservation must remain migration-owned; AutoMigrate bypasses P0-046 guards and CHECKs")
	}
}

func TestCreditReservationHardeningSQLiteMirrorEnforcesStateAndAllocation(t *testing.T) {
	db := testutil.NewTestDB(t)
	future := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	digest := strings.Repeat("d", 64)
	holdDigest := "sha256:" + strings.Repeat("d", 64)

	if err := db.Exec(`INSERT INTO w_credit_reservation
		(uid, tool, idempotency_key, reserved, used, status, expires_at)
		VALUES (1, 'legacy', 'legacy-row', 10, 0, 'reserved', ?)`, future).Error; err != nil {
		t.Fatalf("insert compatible legacy Reservation: %v", err)
	}
	var legacyID uint
	if err := db.Raw(`SELECT id FROM w_credit_reservation WHERE idempotency_key = 'legacy-row'`).Scan(&legacyID).Error; err != nil || legacyID == 0 {
		t.Fatalf("read legacy Reservation id: id=%d err=%v", legacyID, err)
	}

	if err := db.Exec(`INSERT INTO w_credit_reservation
		(uid, tool, idempotency_key, reserved, used, status, expires_at)
		VALUES (1, 'invalid', 'bad-amount', 4, 5, 'reserved', ?)`, future).Error; err == nil {
		t.Fatal("used greater than reserved must fail")
	}
	if err := db.Exec(`INSERT INTO w_credit_reservation
		(uid, tool, idempotency_key, reserved, used, status, expires_at)
		VALUES (1, 'invalid', 'bad-status', 1, 0, 'metered_hold', ?)`, future).Error; err == nil {
		t.Fatal("unfrozen metered_hold status must fail")
	}
	if err := db.Exec(`INSERT INTO w_credit_reservation
		(uid, tool, idempotency_key, reserved, used, status, expires_at)
		VALUES (1, 'invalid', 'used-before-finalized', 4, 1, 'reserved', ?)`, future).Error; err == nil {
		t.Fatal("non-finalized state with used credits must fail")
	}
	if err := db.Exec(`INSERT INTO w_credit_reservation
		(uid, tool, idempotency_key, reserved, used, status, expires_at,
		 hold_review_id, hold_settlement_key)
		VALUES (1, 'invalid', 'partial-hold', 3, 0, 'review_hold', ?, 'review-partial', 'settlement-partial')`, future).Error; err == nil {
		t.Fatal("partial review hold tuple must fail")
	}

	if err := db.Exec(`INSERT INTO w_credit_reservation
		(uid, tool, idempotency_key, request_digest, reserved, used, status, expires_at,
		 hold_review_id, hold_settlement_key, hold_request_digest, review_held_at,
		 state_changed_at, state_version)
		VALUES (1, 'hold', 'valid-hold', ?, 6, 0, 'review_hold', ?,
		 'review-valid', 'settlement-unique', ?, ?, ?, 1)`,
		digest, future, holdDigest, future, future).Error; err != nil {
		t.Fatalf("insert valid review hold: %v", err)
	}
	if err := db.Exec(`INSERT INTO w_credit_reservation
		(uid, tool, idempotency_key, reserved, used, status, expires_at,
		 hold_review_id, hold_settlement_key, hold_request_digest, review_held_at)
		VALUES (1, 'hold', 'duplicate-hold', 6, 0, 'review_hold', ?,
		 'review-other', 'settlement-unique', ?, ?)`, future, holdDigest, future).Error; err == nil {
		t.Fatal("one settlement key must not bind two Reservations")
	}
	if err := db.Exec(`INSERT INTO w_credit_reservation
		(uid, tool, idempotency_key, reserved, used, status, expires_at,
		 hold_review_id, hold_settlement_key, hold_request_digest, review_held_at)
		VALUES (1, 'invalid', 'reserved-with-hold', 2, 0, 'reserved', ?,
		 'review-stale', 'settlement-stale', ?, ?)`, future, holdDigest, future).Error; err == nil {
		t.Fatal("ordinary reserved state must not carry a hold tuple")
	}

	if err := db.Exec(`INSERT INTO w_credit_reservation
		(uid, tool, idempotency_key, reserved, used, status, expires_at,
		 refund_target_status, refund_target_used, refund_due, next_refund_at,
		 state_changed_at, state_version)
		VALUES (1, 'refund', 'valid-refund', 10, 0, 'refund_pending', ?,
		 'finalized', 4, 6, ?, ?, 1)`, future, future, future).Error; err != nil {
		t.Fatalf("insert valid pending partial-finalize refund: %v", err)
	}
	if err := db.Exec(`INSERT INTO w_credit_reservation
		(uid, tool, idempotency_key, reserved, used, status, expires_at,
		 refund_target_status, refund_target_used, refund_due, next_refund_at)
		VALUES (1, 'refund', 'bad-release-target', 10, 0, 'refund_pending', ?,
		 'released', 1, 9, ?)`, future, future).Error; err == nil {
		t.Fatal("released refund target with non-zero used units must fail")
	}
	if err := db.Exec(`INSERT INTO w_credit_reservation
		(uid, tool, idempotency_key, reserved, used, status, expires_at,
		 refund_target_status, refund_target_used, refund_due, next_refund_at,
		 last_refund_error_code)
		VALUES (1, 'refund', 'bad-refund-code', 10, 0, 'refund_pending', ?,
		 'released', 0, 10, ?, 'raw driver error')`, future, future).Error; err == nil {
		t.Fatal("refund error code outside the closed set must fail")
	}
	if err := db.Exec(`INSERT INTO w_credit_reservation
		(uid, tool, idempotency_key, reserved, used, status, expires_at,
		 refund_target_status, refund_target_used, refund_due, next_refund_at)
		VALUES (1, 'refund', 'intent-outside-pending', 10, 0, 'reserved', ?,
		 'released', 0, 10, ?)`, future, future).Error; err == nil {
		t.Fatal("non-pending state must not carry executable refund intent")
	}
	if err := db.Exec(`INSERT INTO w_credit_reservation
		(uid, tool, idempotency_key, reserved, used, status, expires_at,
		 refund_target_used, refund_due, next_refund_at)
		VALUES (1, 'refund', 'missing-refund-target', 10, 0, 'refund_pending', ?,
		 0, 10, ?)`, future, future).Error; err == nil {
		t.Fatal("pending refund with NULL target status must fail closed")
	}
	if err := db.Exec(`INSERT INTO w_credit_reservation
		(uid, tool, idempotency_key, reserved, used, status, expires_at,
		 refund_target_status, refund_target_used, refund_due, next_refund_at)
		VALUES (1, 'refund', 'zero-due-refund', 10, 0, 'refund_pending', ?,
		 'finalized', 10, 0, ?)`, future, future).Error; err == nil {
		t.Fatal("pending refund with zero due must fail closed")
	}

	if err := db.Exec(`INSERT INTO w_credits_pack
		(id, uid, source_type, source_id, credits_total, credits_used)
		VALUES (9001, 1, 'migration-test', 'pack-9001', 10, 10)`).Error; err != nil {
		t.Fatalf("insert allocation owner Pack: %v", err)
	}
	if err := db.Exec(`INSERT INTO w_credits_pack
		(id, uid, source_type, source_id, credits_total, credits_used)
		VALUES (9002, 1, 'migration-test', 'pack-9002', 10, 0)`).Error; err != nil {
		t.Fatalf("insert zero-credit test Pack: %v", err)
	}
	if err := db.Exec(`INSERT INTO w_credit_reservation_allocation
		(reservation_id, pack_id, credits) VALUES (?, 9001, 10)`, legacyID).Error; err != nil {
		t.Fatalf("insert valid Reservation allocation: %v", err)
	}
	if err := db.Exec(`INSERT INTO w_credit_reservation_allocation
		(reservation_id, pack_id, credits) VALUES (?, 9001, 1)`, legacyID).Error; err == nil {
		t.Fatal("duplicate reservation/pack allocation must fail")
	}
	if err := db.Exec(`INSERT INTO w_credit_reservation_allocation
		(reservation_id, pack_id, credits) VALUES (?, 9002, 0)`, legacyID).Error; err == nil {
		t.Fatal("zero-credit allocation must fail")
	}
	if err := db.Exec(`INSERT INTO w_credit_reservation_allocation
		(reservation_id, pack_id, credits) VALUES (?, 9003, 1)`, legacyID).Error; err == nil {
		t.Fatal("orphan Pack allocation must fail")
	}
	if err := db.Exec(`INSERT INTO w_credit_reservation_allocation
		(reservation_id, pack_id, credits) VALUES (999999, 9001, 1)`).Error; err == nil {
		t.Fatal("orphan Reservation allocation must fail")
	}
	if err := db.Exec(`DELETE FROM w_credits_pack WHERE id = 9001`).Error; err == nil {
		t.Fatal("deleting a Pack with allocations must be RESTRICTed")
	}
	if err := db.Exec(`DELETE FROM w_credit_reservation WHERE id = ?`, legacyID).Error; err == nil {
		t.Fatal("deleting a Reservation with allocations must be RESTRICTed")
	}

	for _, index := range []string{
		"uk_w_credit_reservation_hold_settlement",
		"idx_w_credit_reservation_sweep",
		"idx_w_credit_reservation_refund",
	} {
		var count int64
		if err := db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, index).
			Scan(&count).Error; err != nil || count != 1 {
			t.Fatalf("SQLite mirror index %s count=%d err=%v, want 1", index, count, err)
		}
	}
}

func readCreditReservationHardeningMigration(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(creditReservationHardeningMigrationFile)
	if err != nil {
		t.Fatalf("read %s: %v", creditReservationHardeningMigrationFile, err)
	}
	return string(body)
}
