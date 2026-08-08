package migrations

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"gorm.io/gorm"

	"server/utils/testutil"
)

const commerceProviderEventMigrationFile = "20260811_create_commerce_provider_event_inbox_outbox.sql"

func TestCommerceProviderEventMigrationPinsGuardsBeforePersistentDDL(t *testing.T) {
	raw, err := os.ReadFile(commerceProviderEventMigrationFile)
	if err != nil {
		t.Fatalf("read %s: %v", commerceProviderEventMigrationFile, err)
	}
	normalized := normalizeSQL(string(raw))
	firstDDL := strings.Index(normalized, "create table `w_commerce_provider_event`")
	if firstDDL < 0 {
		t.Fatal("migration is missing the Commerce Provider Event table")
	}
	for _, want := range []string{
		"apply 20260807 through 20260810 first",
		"create temporary table `_w_commerce_version_guard`",
		"regexp_substr(version(), '^[0-9]+[.][0-9]+[.][0-9]+')",
		"locate('mariadb', lower(version())) = 0",
		"@w_commerce_version_patch >= 19",
		"create temporary table `_w_commerce_baseline_guard`",
		"@@innodb_page_size >= 8192",
		"'w_order', 'w_user', 'w_credits_pack', 'w_credit_reservation', 'w_credit_reservation_allocation'",
		"`partition_name` is not null",
		"`index_name` = 'uk_w_order_invoice_idempotency'",
		"`index_name` = 'uk_w_order_checkout_session_identity'",
		"`index_name` = 'idx_w_credits_pack_uid_id'",
		"`index_name` = 'uk_w_credits_pack_source_identity'",
		"`index_name` = 'uk_w_credit_reservation_hold_settlement'",
		"`index_name` = 'idx_w_credit_reservation_sweep'",
		"`index_name` = 'idx_w_credit_reservation_refund'",
		"`index_name` = 'uk_w_credit_reservation_allocation_pair'",
		"`ordered_columns` = 'invoice_idempotency_key'",
		"`ordered_columns` = 'checkout_session_idempotency_key'",
		"`ordered_columns` = 'uid,id'",
		"`ordered_columns` = 'uid,source_type,source_id'",
		"`ordered_columns` = 'hold_settlement_key'",
		"`ordered_columns` = 'status,expires_at,id'",
		"`ordered_columns` = 'status,next_refund_at,id'",
		"`ordered_columns` = 'reservation_id,pack_id'",
		"`constraint_name` = 'chk_w_credit_reservation_allocation_credits'",
		"`constraint_name` = 'fk_w_credit_reservation_allocation_reservation'",
		"`rc`.`update_rule` = 'restrict'",
		"`rc`.`delete_rule` = 'restrict'",
		"create temporary table `_w_commerce_target_guard`",
		"'w_commerce_provider_event', 'w_commerce_outbox'",
		"`constraint_name` = 'fk_w_commerce_outbox_provider_event'",
	} {
		pos := strings.Index(normalized, want)
		if pos < 0 {
			t.Errorf("migration guard missing %q", want)
		} else if pos >= firstDDL {
			t.Errorf("migration guard %q must precede persistent DDL", want)
		}
	}
	for _, guard := range []string{
		"_w_commerce_version_guard",
		"_w_commerce_baseline_guard",
		"_w_commerce_target_guard",
	} {
		if !strings.Contains(normalized, "insert into `"+guard+"` (`guard_key`) values (0)") ||
			!strings.Contains(normalized, "drop temporary table `"+guard+"`") {
			t.Errorf("guard %s must install and remove its duplicate-key sentinel", guard)
		}
	}
	if strings.Count(normalized, "create table `w_commerce_") != 2 {
		t.Fatal("migration must publish exactly the Provider Event and Outbox tables")
	}
	if strings.Contains(normalized, "create table if not exists `w_commerce_") {
		t.Fatal("target CREATE must not silently accept an existing drifted table")
	}
	for _, target := range []string{"w_commerce_provider_event", "w_commerce_outbox"} {
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
		"oracle mysql 8.0.19+ only",
		"does not fabricate historical provider events",
		"stop webhook/order/credits writers",
		"one physical mysql session",
		"stop on the first error",
		"individually atomic, but not atomic as a pair",
		"do not rerun the whole file",
	} {
		if !strings.Contains(normalized, note) {
			t.Errorf("migration safety contract missing %q", note)
		}
	}

	identifierPattern := regexp.MustCompile("(?i)(?:constraint|(?:unique )?key) `([^`]+)`")
	for _, match := range identifierPattern.FindAllStringSubmatch(string(raw), -1) {
		if len(match[1]) > 64 {
			t.Fatalf("MySQL identifier %q is %d bytes, want <= 64", match[1], len(match[1]))
		}
	}
}

func TestCommerceProviderEventMigrationPinsInboxAndOutboxContracts(t *testing.T) {
	raw, err := os.ReadFile(commerceProviderEventMigrationFile)
	if err != nil {
		t.Fatalf("read %s: %v", commerceProviderEventMigrationFile, err)
	}
	normalized := normalizeSQL(string(raw))
	inboxStart := strings.Index(normalized, "create table `w_commerce_provider_event`")
	outboxStart := strings.Index(normalized, "create table `w_commerce_outbox`")
	if inboxStart < 0 || outboxStart <= inboxStart {
		t.Fatal("Commerce Inbox must be created before its Outbox child")
	}
	inboxDDL := normalized[inboxStart:outboxStart]
	outboxDDL := normalized[outboxStart:]

	for _, want := range []string{
		"`id` bigint unsigned not null auto_increment",
		"`provider` varchar(32) character set ascii collate ascii_bin not null",
		"`provider_account_id` varchar(255) character set ascii collate ascii_bin not null",
		"`provider_api_version` varchar(32) character set ascii collate ascii_bin not null default ''",
		"`event_id` varchar(255) character set ascii collate ascii_bin not null",
		"`event_type` varchar(128) character set ascii collate ascii_bin not null",
		"`object_id` varchar(255) character set ascii collate ascii_bin not null",
		"`live_mode` tinyint unsigned not null default 0",
		"`provider_created_at` datetime(6) default null",
		"`verification_key_digest` char(71) character set ascii collate ascii_bin not null",
		"`payload_digest` char(64) character set ascii collate ascii_bin not null",
		"`payload_json` mediumblob not null",
		"`status` varchar(32) character set ascii collate ascii_bin not null default 'received'",
		"`attempt_count` int unsigned not null default 0",
		"`processing_version` bigint unsigned not null default 0",
		"`lease_owner_id` varchar(128) character set ascii collate ascii_bin not null default ''",
		"`lease_expires_at` datetime(6) default null",
		"`next_attempt_at` datetime(6) default null",
		"`processed_at` datetime(6) default null",
		"`outcome_kind` varchar(64) character set ascii collate ascii_bin not null default ''",
		"`result_digest` char(64) character set ascii collate ascii_bin not null default ''",
		"`last_error_code` varchar(64) character set ascii collate ascii_bin not null default ''",
		"`created_at` datetime(6) not null default current_timestamp(6)",
		"`updated_at` datetime(6) not null default current_timestamp(6)",
		"unique key `uk_w_commerce_provider_event_identity` (`provider`, `provider_account_id`, `live_mode`, `event_id`)",
		"key `idx_w_commerce_provider_event_received` (`status`, `id`)",
		"key `idx_w_commerce_provider_event_retry` (`status`, `next_attempt_at`, `id`)",
		"key `idx_w_commerce_provider_event_expired` (`status`, `lease_expires_at`, `id`)",
		"key `idx_w_commerce_provider_event_object` (`provider`, `provider_account_id`, `live_mode`, `object_id`, `provider_created_at`, `id`)",
		"`provider` not regexp '[^!-~]'",
		"`provider_api_version` not regexp '[^!-~]'",
		"`lease_owner_id` not regexp '[^!-~]'",
		"check (`status` in ( 'received', 'processing', 'retry_wait', 'processed', 'ignored', 'manual_review' ))",
		"`status` = 'received' and `attempt_count` = 0 and `processing_version` = 0",
		"`status` = 'processing' and `attempt_count` >= 1 and `processing_version` >= 1",
		"`status` = 'retry_wait' and `attempt_count` >= 1 and `processing_version` >= 1",
		"`status` in ('processed', 'ignored')",
		"`status` = 'manual_review'",
		"octet_length(`payload_json`) between 1 and 65536",
		"json_valid(convert(`payload_json` using utf8mb4))",
		"no on update clause",
		") engine=innodb row_format=dynamic",
	} {
		assertSQLContains(t, inboxDDL, want)
	}
	if strings.Contains(inboxDDL, "on update current_timestamp") {
		t.Fatal("Inbox updated_at must be written explicitly")
	}

	for _, want := range []string{
		"`id` bigint unsigned not null auto_increment",
		"`provider_event_id` bigint unsigned not null",
		"`ordinal` int unsigned not null",
		"`topic` varchar(128) character set ascii collate ascii_bin not null",
		"`dedupe_key` char(64) character set ascii collate ascii_bin not null",
		"`payload_digest` char(64) character set ascii collate ascii_bin not null",
		"`payload_json` mediumblob not null",
		"`status` varchar(32) character set ascii collate ascii_bin not null default 'pending'",
		"`available_at` datetime(6) not null",
		"`delivery_attempts` bigint unsigned not null default 0",
		"`dispatch_version` bigint unsigned not null default 0",
		"`lease_owner_id` varchar(128) character set ascii collate ascii_bin not null default ''",
		"`lease_expires_at` datetime(6) default null",
		"`delivered_at` datetime(6) default null",
		"`last_error_code` varchar(64) character set ascii collate ascii_bin not null default ''",
		"`created_at` datetime(6) not null default current_timestamp(6)",
		"`updated_at` datetime(6) not null default current_timestamp(6)",
		"unique key `uk_w_commerce_outbox_event_ordinal` (`provider_event_id`, `ordinal`)",
		"unique key `uk_w_commerce_outbox_dedupe` (`topic`, `dedupe_key`)",
		"key `idx_w_commerce_outbox_pending` (`status`, `available_at`, `id`)",
		"key `idx_w_commerce_outbox_expired` (`status`, `lease_expires_at`, `id`)",
		"`topic` not regexp '[^!-~]'",
		"check (`status` in ('pending', 'delivering', 'delivered', 'dead_letter'))",
		"`status` = 'delivering' and `delivery_attempts` >= 1 and `dispatch_version` >= 1",
		"`status` = 'dead_letter'",
		"foreign key (`provider_event_id`) references `w_commerce_provider_event` (`id`) on delete restrict on update restrict",
		"octet_length(`payload_json`) between 1 and 65536",
		") engine=innodb row_format=dynamic",
	} {
		assertSQLContains(t, outboxDDL, want)
	}
	if strings.Contains(outboxDDL, "on update current_timestamp") {
		t.Fatal("Outbox updated_at must be written explicitly")
	}
}

func TestCommerceProviderEventSQLiteMirrorEnforcesIdentityAndState(t *testing.T) {
	db := testutil.NewTestDB(t)
	for index, wantDDL := range map[string]string{
		"uk_w_commerce_provider_event_identity":  "create unique index uk_w_commerce_provider_event_identity on w_commerce_provider_event(provider, provider_account_id, live_mode, event_id)",
		"idx_w_commerce_provider_event_received": "create index idx_w_commerce_provider_event_received on w_commerce_provider_event(status, id)",
		"idx_w_commerce_provider_event_retry":    "create index idx_w_commerce_provider_event_retry on w_commerce_provider_event(status, next_attempt_at, id)",
		"idx_w_commerce_provider_event_expired":  "create index idx_w_commerce_provider_event_expired on w_commerce_provider_event(status, lease_expires_at, id)",
		"idx_w_commerce_provider_event_object":   "create index idx_w_commerce_provider_event_object on w_commerce_provider_event(provider, provider_account_id, live_mode, object_id, provider_created_at, id)",
		"uk_w_commerce_outbox_event_ordinal":     "create unique index uk_w_commerce_outbox_event_ordinal on w_commerce_outbox(provider_event_id, ordinal)",
		"uk_w_commerce_outbox_dedupe":            "create unique index uk_w_commerce_outbox_dedupe on w_commerce_outbox(topic, dedupe_key)",
		"idx_w_commerce_outbox_pending":          "create index idx_w_commerce_outbox_pending on w_commerce_outbox(status, available_at, id)",
		"idx_w_commerce_outbox_expired":          "create index idx_w_commerce_outbox_expired on w_commerce_outbox(status, lease_expires_at, id)",
	} {
		var gotDDL string
		if err := db.Raw(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?`, index).
			Scan(&gotDDL).Error; err != nil {
			t.Fatalf("read SQLite mirror index %s: %v", index, err)
		}
		if got := normalizeSQL(gotDDL); got != wantDDL {
			t.Fatalf("SQLite mirror index %s = %q, want %q", index, got, wantDDL)
		}
	}

	if err := insertSQLiteCommerceEvent(db, "stripe", "acct_1", false, "evt_same", "2026-08-01", "checkout.session.completed"); err != nil {
		t.Fatalf("insert initial Provider Event: %v", err)
	}
	if err := insertSQLiteCommerceEvent(db, "stripe", "acct_1", false, "evt_same", "2026-08-01", "checkout.session.completed"); err == nil {
		t.Fatal("same provider/account/mode/event identity must be unique")
	}
	if err := insertSQLiteCommerceEvent(db, "stripe", "acct_1", true, "evt_same", "2026-08-01", "checkout.session.completed"); err != nil {
		t.Fatalf("test/live Provider Event identities must remain separate: %v", err)
	}
	if err := insertSQLiteCommerceEvent(db, "stripe", "acct_1", false, "EVT_same", "", "checkout.session.completed"); err != nil {
		t.Fatalf("binary-distinct Provider Event identity should remain distinct: %v", err)
	}
	for _, testCase := range []struct {
		name, provider, account, eventID, apiVersion, eventType string
	}{
		{"unicode provider", "strípe", "acct_ascii", "evt_unicode_provider", "", "event.valid"},
		{"control account", "stripe", "acct_\tbad", "evt_control_account", "", "event.valid"},
		{"space in API version", "stripe", "acct_ascii", "evt_space_api", "2026 08", "event.valid"},
		{"provider too long", strings.Repeat("p", 33), "acct_ascii", "evt_long_provider", "", "event.valid"},
		{"API version too long", "stripe", "acct_ascii", "evt_long_api", strings.Repeat("v", 33), "event.valid"},
		{"event type too long", "stripe", "acct_ascii", "evt_long_type", "", strings.Repeat("t", 129)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if err := insertSQLiteCommerceEvent(
				db, testCase.provider, testCase.account, false, testCase.eventID,
				testCase.apiVersion, testCase.eventType,
			); err == nil {
				t.Fatal("non-canonical or over-bound Provider Event identity must fail")
			}
		})
	}

	if err := db.Exec(`INSERT INTO w_commerce_provider_event
		(provider, provider_account_id, event_id, event_type, object_id,
		 verification_key_digest, payload_digest, payload_json)
		VALUES ('stripe', 'acct_invalid', 'evt_invalid', 'event.invalid', 'obj_invalid', ?, ?, ?)`,
		"sha256:"+strings.Repeat("A", 64), strings.Repeat("b", 64), []byte(`{}`),
	).Error; err == nil {
		t.Fatal("uppercase verification digest must fail")
	}
	if err := db.Exec(`INSERT INTO w_commerce_provider_event
		(provider, provider_account_id, event_id, event_type, object_id,
		 verification_key_digest, payload_digest, payload_json)
		VALUES ('stripe', 'acct_invalid', 'evt_bad_json', 'event.invalid', 'obj_invalid', ?, ?, ?)`,
		"sha256:"+strings.Repeat("a", 64), strings.Repeat("b", 64), []byte(`{`),
	).Error; err == nil {
		t.Fatal("malformed signed payload JSON must fail")
	}
	if err := db.Exec(`INSERT INTO w_commerce_provider_event
		(provider, provider_account_id, event_id, event_type, object_id,
		 verification_key_digest, payload_digest, payload_json,
		 status, attempt_count, processing_version)
		VALUES ('stripe', 'acct_invalid', 'evt_bad_state', 'event.invalid', 'obj_invalid', ?, ?, ?,
		 'processing', 1, 1)`,
		"sha256:"+strings.Repeat("a", 64), strings.Repeat("b", 64), []byte(`{}`),
	).Error; err == nil {
		t.Fatal("processing state without an exact lease tuple must fail")
	}

	if err := insertSQLiteCommerceEvent(db, "stripe", "acct_state", false, "evt_state_contract", "", "event.valid"); err != nil {
		t.Fatalf("insert state-contract Provider Event: %v", err)
	}
	var stateEventID int64
	if err := db.Raw(`SELECT id FROM w_commerce_provider_event WHERE event_id = 'evt_state_contract'`).
		Scan(&stateEventID).Error; err != nil || stateEventID == 0 {
		t.Fatalf("read state-contract Provider Event: id=%d err=%v", stateEventID, err)
	}
	for _, testCase := range []struct {
		name  string
		query string
		args  []any
	}{
		{
			name:  "received with nonzero fence",
			query: `UPDATE w_commerce_provider_event SET processing_version = 1 WHERE id = ?`,
			args:  []any{stateEventID},
		},
		{
			name: "attempt above schema maximum",
			query: `UPDATE w_commerce_provider_event
				SET status = 'processing', attempt_count = 65, processing_version = 65,
					lease_owner_id = 'worker', lease_expires_at = '2099-01-01 00:00:00'
				WHERE id = ?`,
			args: []any{stateEventID},
		},
		{
			name: "retry without error code",
			query: `UPDATE w_commerce_provider_event
				SET status = 'retry_wait', attempt_count = 1, processing_version = 1,
					next_attempt_at = '2099-01-01 00:00:00'
				WHERE id = ?`,
			args: []any{stateEventID},
		},
		{
			name: "terminal without outcome",
			query: `UPDATE w_commerce_provider_event
				SET status = 'processed', attempt_count = 1, processing_version = 1,
					processed_at = '2099-01-01 00:00:00', result_digest = ?
				WHERE id = ?`,
			args: []any{strings.Repeat("d", 64), stateEventID},
		},
		{
			name: "manual review with processed timestamp",
			query: `UPDATE w_commerce_provider_event
				SET status = 'manual_review', attempt_count = 1, processing_version = 1,
					processed_at = '2099-01-01 00:00:00', last_error_code = 'manual_check'
				WHERE id = ?`,
			args: []any{stateEventID},
		},
		{
			name: "lease not after updated time",
			query: `UPDATE w_commerce_provider_event
				SET status = 'processing', attempt_count = 1, processing_version = 1,
					lease_owner_id = 'worker', lease_expires_at = '2000-01-01 00:00:00'
				WHERE id = ?`,
			args: []any{stateEventID},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if err := db.Exec(testCase.query, testCase.args...).Error; err == nil {
				t.Fatal("invalid Provider Event state tuple must fail")
			}
		})
	}
}

func TestCommerceOutboxSQLiteMirrorEnforcesSourceAndDeliveryState(t *testing.T) {
	db := testutil.NewTestDB(t)
	if err := insertSQLiteCommerceEvent(db, "stripe", "acct_outbox", false, "evt_outbox", "", "invoice.payment_succeeded"); err != nil {
		t.Fatalf("insert Provider Event owner: %v", err)
	}
	var eventID int64
	if err := db.Raw(`SELECT id FROM w_commerce_provider_event WHERE event_id = 'evt_outbox'`).Scan(&eventID).Error; err != nil || eventID == 0 {
		t.Fatalf("read Provider Event owner: id=%d err=%v", eventID, err)
	}
	insertOutbox := func(providerEventID int64, ordinal int, topic, dedupe, status string, attempts, fence int64, owner any, lease any, delivered any, errorCode string) error {
		return db.Exec(`INSERT INTO w_commerce_outbox
			(provider_event_id, ordinal, topic, dedupe_key, payload_digest, payload_json,
			 status, available_at, delivery_attempts, dispatch_version,
			 lease_owner_id, lease_expires_at, delivered_at, last_error_code)
			VALUES (?, ?, ?, ?, ?, ?, ?, '2026-08-07 00:00:00.000000', ?, ?, ?, ?, ?, ?)`,
			providerEventID, ordinal, topic, dedupe, strings.Repeat("c", 64), []byte(`{"kind":"receipt"}`),
			status, attempts, fence, owner, lease, delivered, errorCode,
		).Error
	}
	if err := insertOutbox(eventID, 0, "commerce.email.receipt", strings.Repeat("d", 64), "pending", 0, 0, "", nil, nil, ""); err != nil {
		t.Fatalf("insert pending Commerce Outbox row: %v", err)
	}
	if err := insertOutbox(eventID, 0, "commerce.email.other", strings.Repeat("e", 64), "pending", 0, 0, "", nil, nil, ""); err == nil {
		t.Fatal("duplicate Provider Event ordinal must fail")
	}
	if err := insertOutbox(eventID, 1, "commerce.email.receipt", strings.Repeat("d", 64), "pending", 0, 0, "", nil, nil, ""); err == nil {
		t.Fatal("duplicate Topic/DedupeKey must fail")
	}
	if err := insertOutbox(999999, 0, "commerce.email.missing", strings.Repeat("f", 64), "pending", 0, 0, "", nil, nil, ""); err == nil {
		t.Fatal("Outbox without a Provider Event owner must fail")
	}
	if err := insertOutbox(eventID, 1, "commerce.email.invalid", strings.Repeat("1", 64), "delivering", 1, 1, "", nil, nil, ""); err == nil {
		t.Fatal("delivering Outbox without a lease tuple must fail")
	}
	if err := insertOutbox(eventID, 1, "commerce.邮件", strings.Repeat("2", 64), "pending", 0, 0, "", nil, nil, ""); err == nil {
		t.Fatal("non-ASCII Outbox topic must fail")
	}
	if err := insertOutbox(eventID, 1, strings.Repeat("t", 129), strings.Repeat("3", 64), "pending", 0, 0, "", nil, nil, ""); err == nil {
		t.Fatal("over-bound Outbox topic must fail")
	}
	if err := insertOutbox(eventID, 1, "commerce.dead", strings.Repeat("4", 64), "dead_letter", 1, 1, "", nil, nil, ""); err == nil {
		t.Fatal("dead-letter Outbox without an error code must fail")
	}
	if err := insertOutbox(eventID, 1, "commerce.delivered", strings.Repeat("5", 64), "delivered", 1, 1, "", nil, nil, ""); err == nil {
		t.Fatal("delivered Outbox without a delivery timestamp must fail")
	}
	if err := insertOutbox(
		eventID, 1, "commerce.expired", strings.Repeat("6", 64), "delivering", 1, 1,
		"worker", "2000-01-01 00:00:00", nil, "",
	); err == nil {
		t.Fatal("Outbox lease not after updated_at must fail")
	}
	if err := db.Exec(`DELETE FROM w_commerce_provider_event WHERE id = ?`, eventID).Error; err == nil {
		t.Fatal("deleting a Provider Event with committed Outbox rows must be RESTRICTed")
	}
}

func insertSQLiteCommerceEvent(
	db *gorm.DB,
	provider string,
	account string,
	live bool,
	eventID string,
	apiVersion string,
	eventType string,
) error {
	return db.Exec(`INSERT INTO w_commerce_provider_event
		(provider, provider_account_id, provider_api_version, event_id, event_type,
		 object_id, live_mode, verification_key_digest, payload_digest, payload_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		provider, account, apiVersion, eventID, eventType, "obj_"+eventID, live,
		"sha256:"+strings.Repeat("a", 64), strings.Repeat("b", 64), []byte(`{}`),
	).Error
}
