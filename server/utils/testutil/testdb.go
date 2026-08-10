// Package testutil provides small helpers for Go unit tests that need
// more than pure-function isolation. NewTestDB spins up an in-memory
// pure-Go SQLite database and installs the schemas the calibrator +
// character paths actually touch — fast (<10ms per test), no external
// deps at runtime, no CGo required.
//
// Why raw DDL instead of GORM AutoMigrate: the production models use
// MySQL-specific defaults like `ON UPDATE CURRENT_TIMESTAMP` that
// SQLite doesn't accept. We maintain a short SQLite-safe schema per
// test-targeted table here. Keep in sync with the migrations under
// server/migrations/*.sql when the production schema changes; a
// mismatch won't break prod, it'll just surface as a test failure.
package testutil

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var persistentTestDBSequence atomic.Uint64

// NewTestDB returns an in-memory SQLite GORM instance with a schema
// just rich enough for calibrator + character tests. The DB is fresh
// for every call — tests never share state. Logger is silenced so
// test output stays clean; wrap with db.Debug() at the call site to
// see queries.
func NewTestDB(t *testing.T) *gorm.DB {
	return newTestDB(t, ":memory:", false)
}

// NewPersistentTestDB returns the same SQLite test schema while keeping one
// connection open for the test lifetime. It is intended for cancellation tests:
// database/sql may discard a connection whose context expires, which would
// otherwise destroy a plain :memory: database before a detached recovery write.
// The unique shared-memory name keeps parallel tests fully isolated.
func NewPersistentTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf(
		"file:workmax_test_%d?mode=memory&cache=shared&_pragma=foreign_keys(1)",
		persistentTestDBSequence.Add(1),
	)
	return newTestDB(t, dsn, true)
}

func newTestDB(t *testing.T, dsn string, keepAlive bool) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// SQLite ":memory:" creates a separate database PER connection.
	// Concurrent-write tests (SD-S1.1 budget gate, the unlock race
	// suite) need every goroutine to see the same schema + rows, so
	// we limit the pool to a single shared connection. Sequential
	// tests are unaffected; the only cost is that goroutines hitting
	// the write lock simultaneously serialise instead of fanning out
	// — which mirrors MySQL's row lock anyway, so the test exercises
	// the same logical contract.
	if sqlDB, sqlErr := db.DB(); sqlErr == nil {
		if keepAlive {
			// One connection is the keeper and one remains available to GORM.
			// This preserves the existing single-operation serialization while
			// allowing database/sql to replace a canceled worker connection.
			sqlDB.SetMaxOpenConns(2)
			keeper, keeperErr := sqlDB.Conn(context.Background())
			if keeperErr != nil {
				t.Fatalf("keep sqlite test database alive: %v", keeperErr)
			}
			t.Cleanup(func() {
				_ = keeper.Close()
				_ = sqlDB.Close()
			})
		} else {
			sqlDB.SetMaxOpenConns(1)
		}
	}
	// Foreign-key enforcement is connection-local in SQLite and defaults to
	// disabled. Persistent databases also carry the pragma in their DSN so a
	// canceled worker connection's replacement preserves the InnoDB FK mirror.
	// The one-time pragma remains necessary for plain :memory: databases.
	if err := db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
		t.Fatalf("enable sqlite foreign keys: %v", err)
	}
	for _, stmt := range testSchemaDDL {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("install schema: %v (stmt=%q)", err, stmt)
		}
	}
	return db
}

// testSchemaDDL mirrors the columns read/written by handlers under
// test. Extend this list as more surfaces get integration tests — one
// table per CREATE statement so callers can copy the relevant chunk
// into a narrower helper if needed. Table names match production
// (see the TableName() methods on server/model/*.go).
var testSchemaDDL = []string{
	`CREATE TABLE w_generation_task (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		task_id TEXT NOT NULL,
		uid INTEGER NOT NULL DEFAULT 0,
		tool_id TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		status INTEGER NOT NULL DEFAULT 0,
		progress INTEGER DEFAULT 0,
		request_data TEXT,
		result_data TEXT,
		error_msg TEXT,
		credits_used INTEGER DEFAULT 0,
		duration_ms INTEGER DEFAULT 0,
		thread_id INTEGER DEFAULT 0,
		message_id INTEGER DEFAULT 0,
		started_at DATETIME,
		completed_at DATETIME,
		record_id INTEGER DEFAULT 0,
		heartbeat_at DATETIME,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE TABLE w_global_character (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL DEFAULT 0,
		project_id INTEGER,
		name TEXT NOT NULL DEFAULT '',
		slug TEXT NOT NULL DEFAULT '',
		lang TEXT NOT NULL DEFAULT 'en',
		avatar_image_url TEXT NOT NULL DEFAULT '',
		role_type TEXT NOT NULL DEFAULT 'supporting',
		gender TEXT NOT NULL DEFAULT '',
		age_range TEXT NOT NULL DEFAULT '',
		appearance TEXT,
		personality TEXT,
		visual_dna_json TEXT,
		prompt_suffix TEXT,
		negative_prompt TEXT,
		identity_anchors TEXT,
		negative_anchors TEXT,
		anchors_version INTEGER NOT NULL DEFAULT 1,
		appearance_hash TEXT NOT NULL DEFAULT '',
		calibrated_at DATETIME,
		voice_preset TEXT,
		previous_avatar_image_url TEXT NOT NULL DEFAULT '',
		lora_model_id INTEGER,
		source_kind TEXT NOT NULL DEFAULT 'manual',
		status INTEGER NOT NULL DEFAULT 1,
		-- Sprint-E platform-wide lifecycle additions:
		confirmed INTEGER NOT NULL DEFAULT 1,
		confirmed_at DATETIME,
		source_thread_id INTEGER,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		deleted_at DATETIME
	)`,
	// w_brand — Sprint-E 2/N platform-level brand asset table.
	// Mirrors model/brand.go schema with SQLite-safe types: JSONMap
	// columns become TEXT (JSONMap.Scan handles []byte/string).
	// Replaces w_workagent_brand_asset as the canonical brand table;
	// the workagent table stays in this file for one release cycle
	// as a rollback safety net during backfill.
	`CREATE TABLE w_global_brand (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		deleted_at DATETIME,
		uid INTEGER NOT NULL DEFAULT 0,
		project_id INTEGER,
		team_id INTEGER,
		lang TEXT NOT NULL DEFAULT 'en',
		name TEXT NOT NULL DEFAULT '',
		slug TEXT NOT NULL DEFAULT '',
		colors TEXT,
		typography TEXT,
		spacing TEXT,
		layout TEXT,
		components TEXT,
		motion TEXT,
		voice TEXT,
		identity_anchors TEXT,
		negative_anchors TEXT,
		anchors_version INTEGER NOT NULL DEFAULT 1,
		appearance_hash TEXT NOT NULL DEFAULT '',
		calibrated_at DATETIME,
		prompt_suffix TEXT,
		negative_prompt TEXT,
		source_kind TEXT NOT NULL DEFAULT 'manual',
		source_url TEXT NOT NULL DEFAULT '',
		source_thread_id INTEGER,
		raw_spec_md TEXT,
		status INTEGER NOT NULL DEFAULT 1,
		confirmed INTEGER NOT NULL DEFAULT 1,
		confirmed_at DATETIME
	)`,
	`CREATE TABLE w_global_brand_reference (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		deleted_at DATETIME,
		brand_id INTEGER NOT NULL,
		uid INTEGER NOT NULL DEFAULT 0,
		image_url TEXT NOT NULL DEFAULT '',
		reference_type TEXT NOT NULL DEFAULT 'mood_board',
		label TEXT NOT NULL DEFAULT '',
		sort_order INTEGER NOT NULL DEFAULT 0,
		metadata TEXT
	)`,
	// w_global_product — P1 #5 platform product asset table.
	// Mirrors model/product.go with SQLite-safe types.
	// Product-specific JSON sections (specs / visual_guidance /
	// target_audience) replace Brand's M4 sections.
	`CREATE TABLE w_global_product (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		deleted_at DATETIME,
		uid INTEGER NOT NULL DEFAULT 0,
		project_id INTEGER,
		team_id INTEGER,
		lang TEXT NOT NULL DEFAULT 'en',
		name TEXT NOT NULL DEFAULT '',
		slug TEXT NOT NULL DEFAULT '',
		sku TEXT NOT NULL DEFAULT '',
		category TEXT NOT NULL DEFAULT '',
		description TEXT,
		specs TEXT,
		visual_guidance TEXT,
		target_audience TEXT,
		identity_anchors TEXT,
		negative_anchors TEXT,
		anchors_version INTEGER NOT NULL DEFAULT 1,
		appearance_hash TEXT NOT NULL DEFAULT '',
		calibrated_at DATETIME,
		prompt_suffix TEXT,
		negative_prompt TEXT,
		source_kind TEXT NOT NULL DEFAULT 'manual',
		source_url TEXT NOT NULL DEFAULT '',
		source_thread_id INTEGER,
		raw_spec_md TEXT,
		status INTEGER NOT NULL DEFAULT 1,
		confirmed INTEGER NOT NULL DEFAULT 1,
		confirmed_at DATETIME
	)`,
	`CREATE TABLE w_global_product_reference (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		deleted_at DATETIME,
		product_id INTEGER NOT NULL,
		uid INTEGER NOT NULL DEFAULT 0,
		image_url TEXT NOT NULL DEFAULT '',
		reference_type TEXT NOT NULL DEFAULT 'product_shot',
		label TEXT NOT NULL DEFAULT '',
		sort_order INTEGER NOT NULL DEFAULT 0,
		metadata TEXT
	)`,
	// w_director_style — Sprint-E platform-level director-style table.
	`CREATE TABLE w_global_director_style (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		deleted_at DATETIME,
		uid INTEGER NOT NULL DEFAULT 0,
		project_id INTEGER,
		team_id INTEGER,
		lang TEXT NOT NULL DEFAULT 'en',
		name TEXT NOT NULL DEFAULT '',
		slug TEXT NOT NULL DEFAULT '',
		era TEXT NOT NULL DEFAULT '',
		genre TEXT NOT NULL DEFAULT '',
		composition TEXT,
		color TEXT,
		lighting TEXT,
		motion TEXT,
		texture TEXT,
		identity_anchors TEXT,
		negative_anchors TEXT,
		anchors_version INTEGER NOT NULL DEFAULT 1,
		appearance_hash TEXT NOT NULL DEFAULT '',
		calibrated_at DATETIME,
		prompt_suffix TEXT,
		negative_prompt TEXT,
		source_kind TEXT NOT NULL DEFAULT 'manual',
		source_url TEXT NOT NULL DEFAULT '',
		source_thread_id INTEGER,
		raw_spec_md TEXT,
		status INTEGER NOT NULL DEFAULT 1,
		confirmed INTEGER NOT NULL DEFAULT 1,
		confirmed_at DATETIME
	)`,
	`CREATE TABLE w_global_director_style_reference (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		deleted_at DATETIME,
		director_style_id INTEGER NOT NULL,
		uid INTEGER NOT NULL DEFAULT 0,
		image_url TEXT NOT NULL DEFAULT '',
		reference_type TEXT NOT NULL DEFAULT 'still',
		label TEXT NOT NULL DEFAULT '',
		sort_order INTEGER NOT NULL DEFAULT 0,
		metadata TEXT
	)`,
	// w_team_member — minimal columns the visibility predicates touch.
	`CREATE TABLE w_team_member (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			team_id INTEGER NOT NULL,
			uid INTEGER NOT NULL,
			role TEXT NOT NULL DEFAULT 'member',
			status INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			deleted_at DATETIME
		)`,
	`CREATE TABLE w_canvas_shot (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL DEFAULT 0,
		canvas_project_id INTEGER NOT NULL,
		local_card_id TEXT NOT NULL DEFAULT '',
		order_index INTEGER NOT NULL DEFAULT 0,
		title TEXT NOT NULL DEFAULT '',
		timeline_start_ms INTEGER,
		timeline_duration_ms INTEGER,
		status INTEGER NOT NULL DEFAULT 0,
		lock_user_id INTEGER NOT NULL DEFAULT 0,
		lock_job_id TEXT NOT NULL DEFAULT '',
		lock_acquired_at DATETIME,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		deleted_at DATETIME,
		UNIQUE (canvas_project_id, local_card_id)
	)`,
	`CREATE TABLE w_global_project (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL DEFAULT 0,
		system_kind INTEGER NOT NULL DEFAULT 0,
		uuid TEXT NOT NULL DEFAULT '',
		share_token TEXT DEFAULT NULL,
		title TEXT NOT NULL DEFAULT '',
		visibility INTEGER NOT NULL DEFAULT 0,
		thumbnail_url TEXT NOT NULL DEFAULT '',
		latest_version INTEGER NOT NULL DEFAULT 0,
		document TEXT,
		schema_version INTEGER,
		element_count INTEGER,
		budget_credits_cap INTEGER,
		budget_credits_used INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		deleted_at DATETIME,
		CONSTRAINT chk_w_global_project_budget_credits CHECK (
			(budget_credits_cap IS NULL OR budget_credits_cap >= 0)
			AND budget_credits_used >= 0
		)
	)`,
	`CREATE TABLE w_global_project_member (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		project_id INTEGER NOT NULL,
		uid INTEGER NOT NULL,
		role TEXT NOT NULL DEFAULT 'viewer',
		source TEXT NOT NULL DEFAULT 'invite',
		created_by INTEGER NOT NULL DEFAULT 0,
		last_access_at DATETIME,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		deleted_at DATETIME,
		UNIQUE(project_id, uid)
	)`,
	`CREATE TABLE w_global_model (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		model_id TEXT NOT NULL,
		media_type TEXT NOT NULL,
		provider_type TEXT NOT NULL DEFAULT '',
		display_name TEXT NOT NULL DEFAULT '',
		status INTEGER NOT NULL DEFAULT 1,
		pricing_status TEXT NOT NULL DEFAULT '',
		sort_order INTEGER NOT NULL DEFAULT 0,
		required_tier TEXT NOT NULL DEFAULT 'free',
		capabilities TEXT,
		metadata TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		deleted_at DATETIME,
		UNIQUE(model_id, media_type)
	)`,
	// w_desktop_model_gateway_usage - the Desktop model gateway's metering
	// row (migrations/20260815_create_desktop_model_gateway_usage.sql). One
	// row per gateway call: who asked, which catalog model, which platform
	// credential paid, how many tokens moved.
	`CREATE TABLE w_desktop_model_gateway_usage (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		uid INTEGER NOT NULL,
		request_id TEXT NOT NULL,
		protocol TEXT NOT NULL,
		operation TEXT NOT NULL DEFAULT 'messages',
		model_id TEXT NOT NULL,
		upstream_model TEXT NOT NULL DEFAULT '',
		provider_account_id INTEGER NOT NULL DEFAULT 0,
		stream INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'completed',
		http_status INTEGER NOT NULL DEFAULT 0,
		error_class TEXT NOT NULL DEFAULT '',
		input_tokens INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0,
		cache_read_tokens INTEGER NOT NULL DEFAULT 0,
		cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
		total_tokens INTEGER NOT NULL DEFAULT 0,
		duration_ms INTEGER NOT NULL DEFAULT 0,
		started_at DATETIME,
		UNIQUE(request_id)
	)`,
	`CREATE TABLE w_generation_object (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL DEFAULT 0,
		task_id TEXT NOT NULL DEFAULT '',
		record_id INTEGER NOT NULL DEFAULT 0,
		tool_id TEXT NOT NULL DEFAULT '',
		provider TEXT NOT NULL DEFAULT 'r2',
		bucket TEXT NOT NULL DEFAULT '',
		object_key TEXT NOT NULL DEFAULT '',
		asset_kind TEXT NOT NULL DEFAULT '',
		content_type TEXT NOT NULL DEFAULT '',
		size_bytes INTEGER NOT NULL DEFAULT 0,
		etag TEXT NOT NULL DEFAULT '',
		public_url TEXT NOT NULL DEFAULT '',
		source_url TEXT NOT NULL DEFAULT '',
		status INTEGER NOT NULL DEFAULT 1,
		global_asset_id INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE TABLE w_global_asset (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		deleted_at DATETIME,
		uid INTEGER NOT NULL DEFAULT 0,
		team_id INTEGER,
		project_id INTEGER,
		uuid TEXT NOT NULL DEFAULT '',
		kind TEXT NOT NULL DEFAULT 'image',
		source TEXT NOT NULL DEFAULT 'upload',
		source_table TEXT NOT NULL DEFAULT '',
		source_id INTEGER NOT NULL DEFAULT 0,
		source_item_key TEXT NOT NULL DEFAULT '',
		url TEXT NOT NULL DEFAULT '',
		thumb_url TEXT NOT NULL DEFAULT '',
		mime_type TEXT NOT NULL DEFAULT '',
		size_bytes INTEGER NOT NULL DEFAULT 0,
		width INTEGER NOT NULL DEFAULT 0,
		height INTEGER NOT NULL DEFAULT 0,
		duration_ms INTEGER NOT NULL DEFAULT 0,
		content_hash TEXT NOT NULL DEFAULT '',
		status INTEGER NOT NULL DEFAULT 1,
		visibility INTEGER NOT NULL DEFAULT 1,
		parent_asset_id INTEGER NOT NULL DEFAULT 0,
		variant_type TEXT NOT NULL DEFAULT 'original',
		metadata TEXT,
		UNIQUE (source_table, source_id, source_item_key)
	)`,
	`CREATE TABLE w_canvas_snapshot (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		project_id INTEGER NOT NULL,
		snapshot_no INTEGER NOT NULL,
		created_by INTEGER NOT NULL,
		source TEXT NOT NULL DEFAULT 'manual',
		message TEXT NOT NULL DEFAULT '',
		element_count INTEGER NOT NULL DEFAULT 0,
		thumbnail_url TEXT NOT NULL,
		document TEXT NOT NULL,
		schema_version INTEGER NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE (project_id, snapshot_no)
	)`,
	`CREATE TABLE w_canvas_task_binding (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL DEFAULT 0,
		project_id INTEGER NOT NULL DEFAULT 0,
		task_id TEXT NOT NULL DEFAULT '',
		element_id TEXT NOT NULL DEFAULT '',
		generation_run_id TEXT NOT NULL DEFAULT '',
		generation_thread_id TEXT NOT NULL DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE TABLE w_canvas_share_snapshot (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		project_id INTEGER NOT NULL UNIQUE,
		published_version INTEGER NOT NULL DEFAULT 0,
		thumbnail_url TEXT NOT NULL DEFAULT '',
		document TEXT NOT NULL,
		schema_version INTEGER NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE TABLE w_user_asset_ledger (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL DEFAULT 0,
		source TEXT NOT NULL DEFAULT '',
		source_id INTEGER NOT NULL DEFAULT 0,
		global_asset_id INTEGER NOT NULL DEFAULT 0,
		item_key TEXT NOT NULL DEFAULT '',
		visibility_status INTEGER NOT NULL DEFAULT 1,
		container_type TEXT NOT NULL DEFAULT '',
		container_key TEXT NOT NULL DEFAULT '',
		container_title TEXT NOT NULL DEFAULT '',
		container_uuid TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL DEFAULT '',
		kind TEXT NOT NULL DEFAULT '',
		mime_type TEXT NOT NULL DEFAULT '',
		size_bytes INTEGER NOT NULL DEFAULT 0,
		width INTEGER NOT NULL DEFAULT 0,
		height INTEGER NOT NULL DEFAULT 0,
		url TEXT NOT NULL DEFAULT '',
		thumb_url TEXT NOT NULL DEFAULT '',
		preview_url TEXT NOT NULL DEFAULT '',
		project_id INTEGER NOT NULL DEFAULT 0,
		project_title TEXT NOT NULL DEFAULT '',
		project_uuid TEXT NOT NULL DEFAULT '',
		thread_id INTEGER NOT NULL DEFAULT 0,
		thread_name TEXT NOT NULL DEFAULT '',
		thread_uuid TEXT NOT NULL DEFAULT '',
		tool_id TEXT NOT NULL DEFAULT '',
		record_id INTEGER NOT NULL DEFAULT 0,
		task_id TEXT NOT NULL DEFAULT '',
		object_key TEXT NOT NULL DEFAULT '',
		storage_path TEXT NOT NULL DEFAULT '',
		is_attached INTEGER NOT NULL DEFAULT 1,
		has_managed_object INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE (uid, source, source_id, item_key)
	)`,
	// w_user — full GORM-shape so db.Create(&model.User{}) works in
	// tests. The credits services only read member / member_*_time /
	// member_subscription, but Create insists on every column existing.
	`CREATE TABLE w_user (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		email TEXT NOT NULL DEFAULT '',
		phone TEXT NOT NULL DEFAULT '',
		nickname TEXT NOT NULL DEFAULT '',
		avatar TEXT NOT NULL DEFAULT '',
		password TEXT,
		login_time DATETIME,
		login_ip TEXT NOT NULL DEFAULT '',
		login_address TEXT,
		role TEXT NOT NULL DEFAULT 'user',
		identity_code TEXT NOT NULL DEFAULT '',
		fields TEXT,
		ban INTEGER NOT NULL DEFAULT 0,
		auth_email INTEGER NOT NULL DEFAULT 0,
		invite_uid INTEGER NOT NULL DEFAULT 0,
		promotion_amount REAL NOT NULL DEFAULT 0,
		ban_expire_time DATETIME,
		ban_note TEXT,
		member INTEGER NOT NULL DEFAULT 0,
		member_start_time DATETIME,
		member_end_time DATETIME,
		member_subscription TEXT NOT NULL DEFAULT '',
		time_zone TEXT NOT NULL DEFAULT '',
		lang TEXT NOT NULL DEFAULT '',
		notification_setting TEXT NOT NULL DEFAULT '',
		invite_code TEXT NOT NULL DEFAULT '',
		api_key TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	// w_order — payment callbacks use this row as the durable financial owner
	// and lock it before mutating User or CreditsPack state. The generated
	// nullable invoice key mirrors the MySQL webhook-idempotency migration while
	// allowing any number of legacy NULL/empty invoice values.
	`CREATE TABLE w_order (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL DEFAULT 0,
		no TEXT NOT NULL DEFAULT '' UNIQUE,
		pay_method TEXT NOT NULL DEFAULT '0',
		amount INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT '0',
		product_id TEXT NOT NULL DEFAULT '',
		pay_time DATETIME,
		name TEXT NOT NULL DEFAULT '',
		ip TEXT,
		invoice TEXT COLLATE NOCASE,
		invoice_idempotency_key TEXT COLLATE BINARY GENERATED ALWAYS AS (NULLIF(TRIM(invoice), '')) STORED,
		charge_id TEXT,
		trans_id TEXT,
		customer_details TEXT,
		order_mode TEXT,
		subscription_id TEXT,
		order_type TEXT,
		credits_amount INTEGER NOT NULL DEFAULT 0,
		provider_price_id TEXT NOT NULL DEFAULT '',
		billing_period_start DATETIME,
		billing_period_end DATETIME,
		checkout_session_id TEXT COLLATE BINARY NOT NULL DEFAULT '',
		checkout_session_idempotency_key TEXT COLLATE BINARY GENERATED ALWAYS AS (NULLIF(TRIM(checkout_session_id), '')) STORED,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE (invoice_idempotency_key),
		UNIQUE (checkout_session_idempotency_key),
		CHECK (
			(billing_period_start IS NULL AND billing_period_end IS NULL)
			OR
			(billing_period_start IS NOT NULL AND billing_period_end IS NOT NULL AND billing_period_start < billing_period_end)
		),
		CHECK (
			NULLIF(TRIM(checkout_session_id), '') IS NULL
			OR NULLIF(TRIM(provider_price_id), '') IS NOT NULL
		)
	)`,
	`CREATE INDEX idx_w_order_subscription ON w_order(subscription_id)`,
	`CREATE INDEX idx_w_order_membership_resolution
		ON w_order(uid, order_type, status, pay_time, id)`,
	// P0-047 Commerce Provider Event Inbox and Transactional Outbox. Signed
	// Provider payloads remain exact BLOB bytes, while every identity and state
	// discriminator uses binary comparison. The child FK freezes an Event row
	// as the durable producer of each committed external effect.
	`CREATE TABLE w_commerce_provider_event (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		provider TEXT NOT NULL COLLATE BINARY CHECK(
			length(CAST(provider AS BLOB)) BETWEEN 1 AND 32
			AND provider = TRIM(provider)
			AND provider NOT GLOB '*[^!-~]*'
		),
		provider_account_id TEXT NOT NULL COLLATE BINARY CHECK(
			length(CAST(provider_account_id AS BLOB)) BETWEEN 1 AND 255
			AND provider_account_id = TRIM(provider_account_id)
			AND provider_account_id NOT GLOB '*[^!-~]*'
		),
		provider_api_version TEXT NOT NULL DEFAULT '' COLLATE BINARY CHECK(
			length(CAST(provider_api_version AS BLOB)) BETWEEN 0 AND 32
			AND provider_api_version = TRIM(provider_api_version)
			AND provider_api_version NOT GLOB '*[^!-~]*'
		),
		event_id TEXT NOT NULL COLLATE BINARY CHECK(
			length(CAST(event_id AS BLOB)) BETWEEN 1 AND 255
			AND event_id = TRIM(event_id)
			AND event_id NOT GLOB '*[^!-~]*'
		),
		event_type TEXT NOT NULL COLLATE BINARY CHECK(
			length(CAST(event_type AS BLOB)) BETWEEN 1 AND 128
			AND event_type = TRIM(event_type)
			AND event_type NOT GLOB '*[^!-~]*'
		),
		object_id TEXT NOT NULL COLLATE BINARY CHECK(
			length(CAST(object_id AS BLOB)) BETWEEN 1 AND 255
			AND object_id = TRIM(object_id)
			AND object_id NOT GLOB '*[^!-~]*'
		),
		live_mode INTEGER NOT NULL DEFAULT 0 CHECK(live_mode IN (0, 1)),
		provider_created_at DATETIME,
		verification_key_digest TEXT NOT NULL COLLATE BINARY CHECK(
			length(CAST(verification_key_digest AS BLOB)) = 71
			AND substr(verification_key_digest, 1, 7) = 'sha256:'
			AND substr(verification_key_digest, 8) = lower(substr(verification_key_digest, 8))
			AND substr(verification_key_digest, 8) NOT GLOB '*[^0-9a-f]*'
		),
		payload_digest TEXT NOT NULL COLLATE BINARY CHECK(
			length(CAST(payload_digest AS BLOB)) = 64
			AND payload_digest = lower(payload_digest)
			AND payload_digest NOT GLOB '*[^0-9a-f]*'
		),
		payload_json BLOB NOT NULL CHECK(
			typeof(payload_json) = 'blob'
			AND json_valid(CAST(payload_json AS TEXT))
			AND length(payload_json) BETWEEN 1 AND 65536
		),
		status TEXT NOT NULL DEFAULT 'received' COLLATE BINARY CHECK(status IN (
			'received', 'processing', 'retry_wait', 'processed', 'ignored', 'manual_review'
		)),
		attempt_count INTEGER NOT NULL DEFAULT 0 CHECK(attempt_count BETWEEN 0 AND 64),
		processing_version INTEGER NOT NULL DEFAULT 0 CHECK(
			processing_version BETWEEN 0 AND 9223372036854775807
			AND processing_version >= attempt_count
		),
		lease_owner_id TEXT NOT NULL DEFAULT '' COLLATE BINARY CHECK(
			length(CAST(lease_owner_id AS BLOB)) BETWEEN 0 AND 128
			AND lease_owner_id = TRIM(lease_owner_id)
			AND lease_owner_id NOT GLOB '*[^!-~]*'
		),
		lease_expires_at DATETIME,
		next_attempt_at DATETIME,
		processed_at DATETIME,
		outcome_kind TEXT NOT NULL DEFAULT '' COLLATE BINARY CHECK(
			length(CAST(outcome_kind AS BLOB)) BETWEEN 0 AND 64
			AND outcome_kind = TRIM(outcome_kind)
			AND outcome_kind NOT GLOB '*[^!-~]*'
		),
		result_digest TEXT NOT NULL DEFAULT '' COLLATE BINARY CHECK(
			result_digest = '' OR (
				length(CAST(result_digest AS BLOB)) = 64
				AND result_digest = lower(result_digest)
				AND result_digest NOT GLOB '*[^0-9a-f]*'
			)
		),
		last_error_code TEXT NOT NULL DEFAULT '' COLLATE BINARY CHECK(
			length(CAST(last_error_code AS BLOB)) BETWEEN 0 AND 64
			AND last_error_code = TRIM(last_error_code)
			AND last_error_code NOT GLOB '*[^!-~]*'
		),
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		CONSTRAINT chk_w_commerce_provider_event_state_tuple CHECK(
			(status = 'received'
				AND attempt_count = 0 AND processing_version = 0
				AND lease_owner_id = '' AND lease_expires_at IS NULL
				AND next_attempt_at IS NULL AND processed_at IS NULL
				AND outcome_kind = '' AND result_digest = '' AND last_error_code = '')
			OR
			(status = 'processing'
				AND attempt_count >= 1 AND processing_version >= 1
				AND lease_owner_id <> '' AND lease_expires_at IS NOT NULL
				AND next_attempt_at IS NULL AND processed_at IS NULL
				AND outcome_kind = '' AND result_digest = '' AND last_error_code = '')
			OR
			(status = 'retry_wait'
				AND attempt_count >= 1 AND processing_version >= 1
				AND lease_owner_id = '' AND lease_expires_at IS NULL
				AND next_attempt_at IS NOT NULL AND processed_at IS NULL
				AND outcome_kind = '' AND result_digest = '' AND last_error_code <> '')
			OR
			(status IN ('processed', 'ignored')
				AND attempt_count >= 1 AND processing_version >= 1
				AND lease_owner_id = '' AND lease_expires_at IS NULL
				AND next_attempt_at IS NULL AND processed_at IS NOT NULL
				AND outcome_kind <> '' AND length(CAST(result_digest AS BLOB)) = 64
				AND last_error_code = '')
			OR
			(status = 'manual_review'
				AND attempt_count >= 1 AND processing_version >= 1
				AND lease_owner_id = '' AND lease_expires_at IS NULL
				AND next_attempt_at IS NULL AND processed_at IS NULL
				AND outcome_kind = '' AND result_digest = '' AND last_error_code <> '')
		),
		CHECK(lease_expires_at IS NULL OR lease_expires_at > updated_at),
		CHECK(next_attempt_at IS NULL OR next_attempt_at > updated_at),
		CHECK(processed_at IS NULL OR processed_at >= created_at),
		CHECK(updated_at >= created_at)
	)`,
	`CREATE UNIQUE INDEX uk_w_commerce_provider_event_identity
		ON w_commerce_provider_event(provider, provider_account_id, live_mode, event_id)`,
	`CREATE INDEX idx_w_commerce_provider_event_received
		ON w_commerce_provider_event(status, id)`,
	`CREATE INDEX idx_w_commerce_provider_event_retry
		ON w_commerce_provider_event(status, next_attempt_at, id)`,
	`CREATE INDEX idx_w_commerce_provider_event_expired
		ON w_commerce_provider_event(status, lease_expires_at, id)`,
	`CREATE INDEX idx_w_commerce_provider_event_object
		ON w_commerce_provider_event(provider, provider_account_id, live_mode, object_id, provider_created_at, id)`,
	`CREATE TABLE w_commerce_outbox (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		provider_event_id INTEGER NOT NULL,
		ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 15),
		topic TEXT NOT NULL COLLATE BINARY CHECK(
			length(CAST(topic AS BLOB)) BETWEEN 1 AND 128
			AND topic = TRIM(topic)
			AND topic NOT GLOB '*[^!-~]*'
		),
		dedupe_key TEXT NOT NULL COLLATE BINARY CHECK(
			length(CAST(dedupe_key AS BLOB)) = 64
			AND dedupe_key = lower(dedupe_key)
			AND dedupe_key NOT GLOB '*[^0-9a-f]*'
		),
		payload_digest TEXT NOT NULL COLLATE BINARY CHECK(
			length(CAST(payload_digest AS BLOB)) = 64
			AND payload_digest = lower(payload_digest)
			AND payload_digest NOT GLOB '*[^0-9a-f]*'
		),
		payload_json BLOB NOT NULL CHECK(
			typeof(payload_json) = 'blob'
			AND json_valid(CAST(payload_json AS TEXT))
			AND length(payload_json) BETWEEN 1 AND 65536
		),
		status TEXT NOT NULL DEFAULT 'pending' COLLATE BINARY CHECK(
			status IN ('pending', 'delivering', 'delivered', 'dead_letter')
		),
		available_at DATETIME NOT NULL,
		delivery_attempts INTEGER NOT NULL DEFAULT 0 CHECK(
			delivery_attempts BETWEEN 0 AND 9223372036854775807
		),
		dispatch_version INTEGER NOT NULL DEFAULT 0 CHECK(
			dispatch_version BETWEEN 0 AND 9223372036854775807
		),
		lease_owner_id TEXT NOT NULL DEFAULT '' COLLATE BINARY CHECK(
			length(CAST(lease_owner_id AS BLOB)) BETWEEN 0 AND 128
			AND lease_owner_id = TRIM(lease_owner_id)
			AND lease_owner_id NOT GLOB '*[^!-~]*'
		),
		lease_expires_at DATETIME,
		delivered_at DATETIME,
		last_error_code TEXT NOT NULL DEFAULT '' COLLATE BINARY CHECK(
			length(CAST(last_error_code AS BLOB)) BETWEEN 0 AND 64
			AND last_error_code = TRIM(last_error_code)
			AND last_error_code NOT GLOB '*[^!-~]*'
		),
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		CONSTRAINT chk_w_commerce_outbox_state_tuple CHECK(
			(status = 'pending'
				AND lease_owner_id = '' AND lease_expires_at IS NULL
				AND delivered_at IS NULL)
			OR
			(status = 'delivering'
				AND delivery_attempts >= 1 AND dispatch_version >= 1
				AND lease_owner_id <> '' AND lease_expires_at IS NOT NULL
				AND delivered_at IS NULL)
			OR
			(status = 'delivered'
				AND delivery_attempts >= 1 AND dispatch_version >= 1
				AND lease_owner_id = '' AND lease_expires_at IS NULL
				AND delivered_at IS NOT NULL AND last_error_code = '')
			OR
			(status = 'dead_letter'
				AND delivery_attempts >= 1 AND dispatch_version >= 1
				AND lease_owner_id = '' AND lease_expires_at IS NULL
				AND delivered_at IS NULL AND last_error_code <> '')
		),
		CHECK(lease_expires_at IS NULL OR lease_expires_at > updated_at),
		CHECK(delivered_at IS NULL OR delivered_at >= created_at),
		CHECK(updated_at >= created_at),
		FOREIGN KEY(provider_event_id) REFERENCES w_commerce_provider_event(id)
			ON DELETE RESTRICT ON UPDATE RESTRICT
	)`,
	`CREATE UNIQUE INDEX uk_w_commerce_outbox_event_ordinal
		ON w_commerce_outbox(provider_event_id, ordinal)`,
	`CREATE UNIQUE INDEX uk_w_commerce_outbox_dedupe
		ON w_commerce_outbox(topic, dedupe_key)`,
	`CREATE INDEX idx_w_commerce_outbox_pending
		ON w_commerce_outbox(status, available_at, id)`,
	`CREATE INDEX idx_w_commerce_outbox_expired
		ON w_commerce_outbox(status, lease_expires_at, id)`,
	`CREATE TABLE w_credits_pack (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL DEFAULT 0,
		source_type TEXT COLLATE BINARY NOT NULL DEFAULT '',
		source_id TEXT COLLATE BINARY NOT NULL DEFAULT '',
		credits_total INTEGER NOT NULL DEFAULT 0,
		credits_used INTEGER NOT NULL DEFAULT 0,
		expires_at DATETIME,
		remark TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		CONSTRAINT chk_credits_used_bounds CHECK (credits_used >= 0 AND credits_used <= credits_total),
		CONSTRAINT chk_w_credits_pack_source_identity_canonical CHECK (
			source_type = TRIM(source_type)
			AND source_id = TRIM(source_id)
			AND source_type <> ''
			AND source_id <> ''
		),
		UNIQUE (uid, source_type, source_id)
	)`,
	`CREATE INDEX idx_w_credits_pack_uid_id ON w_credits_pack(uid, id)`,
	`CREATE TABLE w_credit_reservation (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			uid INTEGER NOT NULL,
			tool TEXT NOT NULL DEFAULT '' COLLATE BINARY,
			idempotency_key TEXT NOT NULL DEFAULT '',
			request_digest TEXT COLLATE BINARY CHECK(request_digest IS NULL OR (
				length(CAST(request_digest AS BLOB)) = 64
				AND request_digest NOT GLOB '*[^0-9a-f]*'
			)),
			quote_id TEXT NOT NULL DEFAULT '',
			reserved INTEGER NOT NULL DEFAULT 0,
			used INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'reserved' COLLATE BINARY CHECK(status IN ('reserved', 'review_hold', 'refund_pending', 'finalized', 'released', 'expired')),
			expires_at DATETIME NOT NULL,
			finalized_at DATETIME,
			released_at DATETIME,
			remark TEXT NOT NULL DEFAULT '',
			project_id INTEGER NOT NULL DEFAULT 0,
			hold_review_id TEXT COLLATE BINARY CHECK(hold_review_id IS NULL OR length(CAST(hold_review_id AS BLOB)) BETWEEN 1 AND 256),
			hold_settlement_key TEXT COLLATE BINARY CHECK(hold_settlement_key IS NULL OR length(CAST(hold_settlement_key AS BLOB)) BETWEEN 1 AND 256),
			hold_request_digest TEXT COLLATE BINARY CHECK(hold_request_digest IS NULL OR (length(CAST(hold_request_digest AS BLOB)) = 71 AND substr(hold_request_digest, 1, 7) = 'sha256:')),
			review_held_at DATETIME,
			refund_target_status TEXT COLLATE BINARY CHECK(refund_target_status IS NULL OR refund_target_status IN ('finalized', 'released', 'expired')),
			refund_target_used INTEGER CHECK(refund_target_used IS NULL OR refund_target_used >= 0),
			refund_due INTEGER NOT NULL DEFAULT 0,
			refund_attempts INTEGER NOT NULL DEFAULT 0 CHECK(refund_attempts BETWEEN 0 AND 9223372036854775807),
			next_refund_at DATETIME,
			last_refund_error_code TEXT COLLATE BINARY CHECK(last_refund_error_code IS NULL OR length(CAST(last_refund_error_code AS BLOB)) BETWEEN 1 AND 64),
			state_changed_at DATETIME,
			state_version INTEGER NOT NULL DEFAULT 0 CHECK(state_version BETWEEN 0 AND 9223372036854775807),
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (uid, idempotency_key),
			CHECK(reserved >= 0 AND used >= 0 AND used <= reserved AND (status = 'finalized' OR used = 0) AND refund_due BETWEEN 0 AND reserved),
			CHECK(
				(hold_review_id IS NULL AND hold_settlement_key IS NULL AND hold_request_digest IS NULL AND review_held_at IS NULL)
				OR
				(hold_review_id IS NOT NULL AND hold_settlement_key IS NOT NULL AND hold_request_digest IS NOT NULL AND review_held_at IS NOT NULL)
			),
			CHECK(status <> 'reserved' OR (hold_review_id IS NULL AND hold_settlement_key IS NULL AND hold_request_digest IS NULL AND review_held_at IS NULL)),
			CHECK(status <> 'review_hold' OR (hold_review_id IS NOT NULL AND hold_settlement_key IS NOT NULL AND hold_request_digest IS NOT NULL AND review_held_at IS NOT NULL)),
			CHECK(
				(status = 'refund_pending'
					AND refund_target_status IS NOT NULL
					AND refund_target_status IN ('finalized', 'released', 'expired')
					AND refund_target_used IS NOT NULL
					AND refund_target_used BETWEEN 0 AND reserved
					AND refund_due > 0
					AND refund_due = reserved - refund_target_used
					AND (refund_target_status = 'finalized' OR refund_target_used = 0)
					AND next_refund_at IS NOT NULL)
				OR
				(status <> 'refund_pending'
					AND refund_target_status IS NULL
					AND refund_target_used IS NULL
					AND refund_due = 0
					AND next_refund_at IS NULL)
			),
			CHECK(
				(status IN ('reserved', 'review_hold', 'refund_pending') AND finalized_at IS NULL AND released_at IS NULL)
				OR (status = 'finalized' AND finalized_at IS NOT NULL AND released_at IS NULL)
				OR (status IN ('released', 'expired') AND finalized_at IS NULL AND released_at IS NOT NULL)
			),
			CHECK(last_refund_error_code IS NULL OR (status = 'refund_pending' AND last_refund_error_code IN ('project_invariant', 'allocation_invalid', 'allocation_incomplete', 'pack_invariant', 'database_error'))),
			CHECK(review_held_at IS NULL OR review_held_at >= created_at),
			CHECK(state_changed_at IS NULL OR state_changed_at >= created_at)
		)`,
	`CREATE UNIQUE INDEX uk_w_credit_reservation_hold_settlement ON w_credit_reservation(hold_settlement_key)`,
	`CREATE INDEX idx_w_credit_reservation_sweep ON w_credit_reservation(status, expires_at, id)`,
	`CREATE INDEX idx_w_credit_reservation_refund ON w_credit_reservation(status, next_refund_at, id)`,
	`CREATE UNIQUE INDEX uk_w_credit_reservation_agent_binding
		ON w_credit_reservation(id, uid, request_digest, tool, reserved, project_id)`,
	`CREATE TABLE w_credit_reservation_allocation (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			reservation_id INTEGER NOT NULL,
			pack_id INTEGER NOT NULL,
			credits INTEGER NOT NULL DEFAULT 0 CHECK(credits > 0),
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(reservation_id, pack_id),
			CONSTRAINT fk_w_credit_reservation_allocation_reservation
				FOREIGN KEY(reservation_id) REFERENCES w_credit_reservation(id) ON DELETE RESTRICT ON UPDATE RESTRICT,
			CONSTRAINT fk_w_credit_reservation_allocation_pack
				FOREIGN KEY(pack_id) REFERENCES w_credits_pack(id) ON DELETE RESTRICT ON UPDATE RESTRICT
		)`,
	// w_workagent_thread — exercised by the PlanRepository tests
	// (latest_plan / plan_history snapshot). Mirrors only the columns
	// those tests touch — id, latest_plan, plan_history, plus the
	// non-null defaults the model declares (uid, uuid, name, etc.) so a
	// GORM .Create works without column complaints. NULL latest_plan
	// is the production default, so the column omits NOT NULL.
	`CREATE TABLE w_workagent_thread (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL DEFAULT 0,
		uuid TEXT NOT NULL DEFAULT '',
		project_id INTEGER NOT NULL DEFAULT 0,
		agent_session_id TEXT NOT NULL DEFAULT '',
		agent_session_created_at DATETIME,
		name TEXT NOT NULL DEFAULT '',
		agent_mode TEXT NOT NULL DEFAULT 'ppt',
		agent_type TEXT NOT NULL DEFAULT 'general_agent',
		workspace_path TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		max_tokens INTEGER NOT NULL DEFAULT 0,
		context_count INTEGER NOT NULL DEFAULT 0,
		presence_penalty REAL NOT NULL DEFAULT 0,
		frequency_penalty REAL NOT NULL DEFAULT 0,
		temperature REAL NOT NULL DEFAULT 0,
		prompt TEXT NOT NULL DEFAULT '',
		top_sort INTEGER NOT NULL DEFAULT 0,
		plugins TEXT NOT NULL DEFAULT '',
		icon TEXT NOT NULL DEFAULT '',
		local_plugins TEXT NOT NULL DEFAULT '',
		message_count INTEGER NOT NULL DEFAULT 0,
		msg_preview TEXT NOT NULL DEFAULT '',
		file_count INTEGER NOT NULL DEFAULT 0,
		is_public INTEGER NOT NULL DEFAULT 1,
		latest_plan TEXT,
		plan_history TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`,
	// w_workagent_message — exercised by the MessageRepository tests
	// (CreateAgentMessage / LoadByID / DeleteByID / ClearAIText /
	// ClearUserText). Mirrors only the columns those tests touch
	// plus the non-null-default columns the GORM model declares so
	// db.Create works without column complaints.
	`CREATE TABLE w_workagent_message (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL DEFAULT 0,
		uuid TEXT NOT NULL DEFAULT '',
		thread_id INTEGER NOT NULL DEFAULT 0,
		user_text TEXT,
		ai_text TEXT,
		total_prompt TEXT,
		ip TEXT NOT NULL DEFAULT '',
		task_id TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		deduct_integral INTEGER NOT NULL DEFAULT 0,
		refund_integral INTEGER NOT NULL DEFAULT 0,
		use_tokens INTEGER NOT NULL DEFAULT 0,
		prompt_tokens INTEGER NOT NULL DEFAULT 0,
		completion_tokens INTEGER NOT NULL DEFAULT 0,
		context_tokens INTEGER NOT NULL DEFAULT 0,
		use_images TEXT,
		ai_audio TEXT,
		user_audio TEXT,
		append_deduct_integral INTEGER NOT NULL DEFAULT 0,
		use_files TEXT,
		chat_mode TEXT NOT NULL DEFAULT '',
		content_type TEXT NOT NULL DEFAULT '',
		structured_content TEXT,
		actions TEXT,
		metadata TEXT,
		message_idempotency_key TEXT,
		user_rating INTEGER NOT NULL DEFAULT 0,
		user_feedback TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`,
	// w_workagent_thread_file — referenced by getFileByID's IDOR
	// regression test. Schema mirrors model/workagent.ThreadFile with
	// SQLite-safe types. The (uid) column scope is what prevents users
	// from referencing other users' files via crafted file IDs.
	`CREATE TABLE w_workagent_thread_file (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		deleted_at DATETIME,
		uid INTEGER NOT NULL DEFAULT 0,
		thread_id INTEGER NOT NULL DEFAULT 0,
		message_id INTEGER NOT NULL DEFAULT 0,
		file_name TEXT NOT NULL DEFAULT '',
		display_name TEXT NOT NULL DEFAULT '',
		file_size INTEGER NOT NULL DEFAULT 0,
		file_type TEXT NOT NULL DEFAULT '',
		mime_type TEXT NOT NULL DEFAULT '',
		file_path TEXT NOT NULL DEFAULT '',
		file_source TEXT NOT NULL DEFAULT 'upload',
		description TEXT,
		file_hash TEXT NOT NULL DEFAULT '',
		global_asset_id INTEGER NOT NULL DEFAULT 0,
		last_synced_at DATETIME,
		exists_on_disk INTEGER NOT NULL DEFAULT 1
	)`,
	// w_workagent_artifact — P1 artifact-first registry. Tests that
	// seed only thread files still work through the fallback adapter;
	// tests that exercise lifecycle/status/version can write explicit
	// registry rows here.
	`CREATE TABLE w_workagent_artifact (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		uid INTEGER NOT NULL DEFAULT 0,
		thread_id INTEGER NOT NULL DEFAULT 0,
		thread_file_id INTEGER NOT NULL DEFAULT 0,
		artifact_key TEXT NOT NULL DEFAULT '',
		name TEXT NOT NULL DEFAULT '',
		display_name TEXT NOT NULL DEFAULT '',
		artifact_type TEXT NOT NULL DEFAULT '',
		output_type TEXT NOT NULL DEFAULT '',
		preview_type TEXT NOT NULL DEFAULT '',
		export_targets TEXT,
		version INTEGER NOT NULL DEFAULT 1,
		status TEXT NOT NULL DEFAULT 'draft',
		review_state TEXT NOT NULL DEFAULT 'none',
		review_source TEXT NOT NULL DEFAULT '',
		review_summary TEXT,
		review_details_json TEXT,
		comparison_source TEXT NOT NULL DEFAULT '',
		comparison_summary TEXT,
		comparison_decision TEXT NOT NULL DEFAULT '',
		html_preview_diagnostics TEXT,
		design_system_basename TEXT NOT NULL DEFAULT '',
		design_system_title TEXT NOT NULL DEFAULT '',
		design_system_derived_from TEXT NOT NULL DEFAULT '',
		source TEXT NOT NULL DEFAULT 'upload',
		parent_artifact_id INTEGER NOT NULL DEFAULT 0,
		artifact_relation TEXT NOT NULL DEFAULT '',
		file_hash TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE TABLE w_workagent_artifact_asset_candidate (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		uid INTEGER NOT NULL DEFAULT 0,
		thread_id INTEGER NOT NULL DEFAULT 0,
		artifact_id INTEGER NOT NULL DEFAULT 0,
		thread_file_id INTEGER NOT NULL DEFAULT 0,
		asset_kind TEXT NOT NULL DEFAULT '',
		name TEXT NOT NULL DEFAULT '',
		slug TEXT NOT NULL DEFAULT '',
		profile_json TEXT,
		status TEXT NOT NULL DEFAULT 'draft',
		target_kind TEXT NOT NULL DEFAULT '',
		target_id INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE w_workagent_project_design_system (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		uid INTEGER NOT NULL DEFAULT 0,
		project_id INTEGER NOT NULL DEFAULT 0,
		thread_id INTEGER NOT NULL DEFAULT 0,
		artifact_id INTEGER NOT NULL DEFAULT 0,
		candidate_id INTEGER NOT NULL DEFAULT 0,
		name TEXT NOT NULL DEFAULT '',
		slug TEXT NOT NULL DEFAULT '',
		basename TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL DEFAULT '',
		derived_from TEXT NOT NULL DEFAULT '',
		version INTEGER NOT NULL DEFAULT 1,
		body TEXT,
		status TEXT NOT NULL DEFAULT 'confirmed',
		reviewed_by INTEGER NOT NULL DEFAULT 0,
		reviewed_at DATETIME,
		review_note TEXT
	)`,
	`CREATE TABLE w_workagent_prompt_asset (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		uid INTEGER NOT NULL DEFAULT 0,
		project_id INTEGER NOT NULL DEFAULT 0,
		thread_id INTEGER NOT NULL DEFAULT 0,
		artifact_id INTEGER NOT NULL DEFAULT 0,
		candidate_id INTEGER NOT NULL DEFAULT 0,
		name TEXT NOT NULL DEFAULT '',
		slug TEXT NOT NULL DEFAULT '',
		prompt TEXT,
		negative_prompt TEXT,
		profile_json TEXT,
		status TEXT NOT NULL DEFAULT 'confirmed'
	)`,
	`CREATE TABLE w_workagent_skill_access_request (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		uid INTEGER NOT NULL DEFAULT 0,
		agent_mode TEXT NOT NULL DEFAULT '',
		source TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'pending',
		reason TEXT NOT NULL DEFAULT '',
		reviewed_by INTEGER NOT NULL DEFAULT 0,
		reviewed_at DATETIME,
		review_note TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE TABLE w_workagent_artifact_export_job (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		uid INTEGER NOT NULL DEFAULT 0,
		thread_id INTEGER NOT NULL DEFAULT 0,
		artifact_id INTEGER NOT NULL DEFAULT 0,
		thread_file_id INTEGER NOT NULL DEFAULT 0,
		target TEXT NOT NULL DEFAULT '',
		kind TEXT NOT NULL DEFAULT '',
		worker TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'queued',
		reason TEXT NOT NULL DEFAULT '',
		output_extension TEXT NOT NULL DEFAULT '',
		prerequisites_json TEXT,
		plan_json TEXT,
		output_file_id INTEGER NOT NULL DEFAULT 0,
		output_path TEXT NOT NULL DEFAULT '',
		error_message TEXT
	)`,
	// w_agent_turn / w_agent_turn_event - P0-08a durable Agent Kernel
	// persistence foundation. These SQLite-safe tables mirror migration
	// 20260665 plus the fenced execution extension in 20260666. They
	// intentionally do not imply that a production worker, settlement or live
	// stream is wired.
	`CREATE TABLE w_agent_turn (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		turn_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(turn_id AS BLOB)) BETWEEN 1 AND 256),
		principal_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(principal_id AS BLOB)) BETWEEN 1 AND 128),
		thread_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(thread_id AS BLOB)) BETWEEN 1 AND 256),
		idempotency_key TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(idempotency_key AS BLOB)) BETWEEN 1 AND 128),
		command_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(command_digest AS BLOB)) BETWEEN 1 AND 128),
		plugin_snapshot_json TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'queued' CHECK(status IN ('queued', 'running', 'completed', 'stopped', 'failed', 'timeout')),
		last_event_sequence INTEGER NOT NULL DEFAULT 1 CHECK(last_event_sequence BETWEEN 1 AND 9223372036854775807),
		active_attempt_id TEXT COLLATE BINARY CHECK(active_attempt_id IS NULL OR length(CAST(active_attempt_id AS BLOB)) BETWEEN 1 AND 64),
		fencing_token INTEGER NOT NULL DEFAULT 0 CHECK(fencing_token BETWEEN 0 AND 9223372036854775807),
		cancel_requested_at DATETIME,
		started_at DATETIME,
		finished_at DATETIME,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL,
		UNIQUE(turn_id),
		UNIQUE(principal_id, thread_id, idempotency_key),
		CHECK((active_attempt_id IS NULL AND fencing_token BETWEEN 0 AND 9223372036854775807) OR (active_attempt_id IS NOT NULL AND fencing_token BETWEEN 1 AND 9223372036854775807)),
		FOREIGN KEY(turn_id, active_attempt_id, fencing_token) REFERENCES w_agent_turn_attempt(turn_id, attempt_id, fencing_token) ON DELETE RESTRICT ON UPDATE RESTRICT
	)`,
	`CREATE UNIQUE INDEX uk_w_agent_turn_reservation_identity
		ON w_agent_turn(turn_id, principal_id, command_digest)`,
	`CREATE UNIQUE INDEX uk_w_agent_turn_settlement_fence
		ON w_agent_turn(turn_id, fencing_token, status)`,
	`CREATE TABLE w_agent_turn_event (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		turn_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(turn_id AS BLOB)) BETWEEN 1 AND 256),
		sequence INTEGER NOT NULL CHECK(sequence BETWEEN 1 AND 9223372036854775807),
		event_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(event_id AS BLOB)) BETWEEN 1 AND 320),
		schema_version INTEGER NOT NULL,
		event_type TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(event_type AS BLOB)) BETWEEN 1 AND 255),
		event_json TEXT NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(turn_id, sequence),
		UNIQUE(turn_id, event_id),
		FOREIGN KEY(turn_id) REFERENCES w_agent_turn(turn_id) ON DELETE RESTRICT ON UPDATE RESTRICT
	)`,
	`CREATE TABLE w_agent_turn_attempt (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		attempt_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(attempt_id AS BLOB)) BETWEEN 1 AND 64),
		turn_id TEXT NOT NULL COLLATE BINARY,
		fencing_token INTEGER NOT NULL CHECK(fencing_token BETWEEN 1 AND 9223372036854775807),
		status TEXT NOT NULL DEFAULT 'running' CHECK(status IN ('running', 'completed', 'stopped', 'failed', 'timeout', 'expired')),
		worker_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(worker_id AS BLOB)) BETWEEN 1 AND 128),
		worker_build_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(worker_build_digest AS BLOB)) BETWEEN 1 AND 128),
		claimed_at DATETIME NOT NULL,
		last_heartbeat_at DATETIME NOT NULL,
		lease_expires_at DATETIME NOT NULL,
		finished_at DATETIME,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(attempt_id),
		UNIQUE(turn_id, fencing_token),
		UNIQUE(turn_id, attempt_id, fencing_token),
		CHECK((status = 'running' AND finished_at IS NULL) OR (status IN ('completed', 'stopped', 'failed', 'timeout', 'expired') AND finished_at IS NOT NULL)),
		CHECK(claimed_at <= last_heartbeat_at AND last_heartbeat_at < lease_expires_at),
		CHECK(finished_at IS NULL OR finished_at >= last_heartbeat_at),
		CHECK(updated_at >= created_at),
		FOREIGN KEY(turn_id) REFERENCES w_agent_turn(turn_id) ON DELETE RESTRICT ON UPDATE RESTRICT
	)`,
	`CREATE INDEX idx_w_agent_turn_attempt_claim ON w_agent_turn_attempt(status, lease_expires_at, id)`,
	`CREATE TABLE w_agent_turn_operation (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		turn_id TEXT NOT NULL COLLATE BINARY,
		operation_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(operation_id AS BLOB)) BETWEEN 1 AND 128),
		operation_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(operation_digest AS BLOB)) BETWEEN 1 AND 128),
		attempt_id TEXT NOT NULL COLLATE BINARY,
		fencing_token INTEGER NOT NULL CHECK(fencing_token BETWEEN 1 AND 9223372036854775807),
		event_sequence INTEGER NOT NULL CHECK(event_sequence BETWEEN 1 AND 9223372036854775807),
		turn_status TEXT NOT NULL COLLATE BINARY CHECK(turn_status IN ('queued', 'running', 'completed', 'stopped', 'failed', 'timeout')),
		effect_count INTEGER NOT NULL DEFAULT 0 CHECK(effect_count BETWEEN 0 AND 64),
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(turn_id, operation_id),
		UNIQUE(turn_id, operation_id, attempt_id, fencing_token),
		FOREIGN KEY(turn_id, attempt_id, fencing_token) REFERENCES w_agent_turn_attempt(turn_id, attempt_id, fencing_token) ON DELETE RESTRICT ON UPDATE RESTRICT,
		FOREIGN KEY(turn_id, event_sequence) REFERENCES w_agent_turn_event(turn_id, sequence) ON DELETE RESTRICT ON UPDATE RESTRICT
	)`,
	`CREATE INDEX idx_w_agent_turn_operation_attempt ON w_agent_turn_operation(turn_id, attempt_id, fencing_token)`,
	`CREATE INDEX idx_w_agent_turn_operation_event ON w_agent_turn_operation(turn_id, event_sequence)`,
	`CREATE UNIQUE INDEX uk_w_agent_turn_operation_settlement_binding
		ON w_agent_turn_operation(turn_id, operation_id, attempt_id, fencing_token, turn_status)`,
	`CREATE TABLE w_agent_effect_outbox (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		outbox_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(outbox_id AS BLOB)) BETWEEN 1 AND 64),
		turn_id TEXT NOT NULL COLLATE BINARY,
		attempt_id TEXT NOT NULL COLLATE BINARY,
		turn_fencing_token INTEGER NOT NULL CHECK(turn_fencing_token BETWEEN 1 AND 9223372036854775807),
		operation_id TEXT NOT NULL COLLATE BINARY,
		ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 63),
		topic TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(topic AS BLOB)) BETWEEN 1 AND 128),
		dedupe_key TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(dedupe_key AS BLOB)) BETWEEN 1 AND 256),
		payload_json TEXT NOT NULL CHECK(json_valid(payload_json) AND length(CAST(payload_json AS BLOB)) BETWEEN 1 AND 1048576),
		status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending', 'delivering', 'delivered', 'dead_letter', 'review_hold')),
		available_at DATETIME NOT NULL,
		delivery_attempts INTEGER NOT NULL DEFAULT 0 CHECK(delivery_attempts BETWEEN 0 AND 9223372036854775807),
		dispatch_fencing_token INTEGER NOT NULL DEFAULT 0 CHECK(dispatch_fencing_token BETWEEN 0 AND 9223372036854775807),
		lease_owner_id TEXT COLLATE BINARY CHECK(lease_owner_id IS NULL OR length(CAST(lease_owner_id AS BLOB)) BETWEEN 1 AND 128),
		lease_expires_at DATETIME,
		last_error_code TEXT COLLATE BINARY CHECK(last_error_code IS NULL OR length(CAST(last_error_code AS BLOB)) BETWEEN 1 AND 64),
		delivered_at DATETIME,
		dead_lettered_at DATETIME,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(outbox_id),
		UNIQUE(topic, dedupe_key),
		UNIQUE(turn_id, operation_id, ordinal),
		CHECK(
			(status = 'pending' AND lease_owner_id IS NULL AND lease_expires_at IS NULL AND delivered_at IS NULL AND dead_lettered_at IS NULL)
			OR (status = 'delivering' AND lease_owner_id IS NOT NULL AND lease_expires_at IS NOT NULL AND delivery_attempts >= 1 AND dispatch_fencing_token >= 1 AND delivered_at IS NULL AND dead_lettered_at IS NULL)
			OR (status = 'delivered' AND lease_owner_id IS NULL AND lease_expires_at IS NULL AND delivery_attempts >= 1 AND dispatch_fencing_token >= 1 AND delivered_at IS NOT NULL AND dead_lettered_at IS NULL)
			OR (status = 'dead_letter' AND lease_owner_id IS NULL AND lease_expires_at IS NULL AND delivery_attempts >= 1 AND dispatch_fencing_token >= 1 AND delivered_at IS NULL AND dead_lettered_at IS NOT NULL)
			OR (status = 'review_hold' AND lease_owner_id IS NULL AND lease_expires_at IS NULL AND delivered_at IS NULL AND dead_lettered_at IS NULL)
		),
		CHECK(lease_expires_at IS NULL OR lease_expires_at > updated_at),
		CHECK((delivered_at IS NULL OR delivered_at >= created_at) AND (dead_lettered_at IS NULL OR dead_lettered_at >= created_at)),
		CHECK(updated_at >= created_at),
		FOREIGN KEY(turn_id, attempt_id, turn_fencing_token) REFERENCES w_agent_turn_attempt(turn_id, attempt_id, fencing_token) ON DELETE RESTRICT ON UPDATE RESTRICT,
		FOREIGN KEY(turn_id, operation_id, attempt_id, turn_fencing_token) REFERENCES w_agent_turn_operation(turn_id, operation_id, attempt_id, fencing_token) ON DELETE RESTRICT ON UPDATE RESTRICT
	)`,
	`CREATE INDEX idx_w_agent_effect_outbox_pending ON w_agent_effect_outbox(status, available_at, id)`,
	`CREATE INDEX idx_w_agent_effect_outbox_expired ON w_agent_effect_outbox(status, lease_expires_at, id)`,
	`CREATE INDEX idx_w_agent_effect_outbox_attempt ON w_agent_effect_outbox(turn_id, attempt_id, turn_fencing_token)`,
	`CREATE INDEX idx_w_agent_effect_outbox_operation ON w_agent_effect_outbox(turn_id, operation_id, attempt_id, turn_fencing_token)`,
	// P0-045 immutable Meter Release registry and attested scoped-adapter usage
	// journal. A release freezes the exact Plugin/meter/pricing/source-registry
	// snapshot; every journal receipt is fenced to the Attempt that observed it.
	`CREATE TABLE w_agent_usage_meter_release (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		release_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(release_id AS BLOB)) BETWEEN 1 AND 64),
		plugin_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(plugin_id AS BLOB)) BETWEEN 1 AND 512),
		plugin_version TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(plugin_version AS BLOB)) BETWEEN 1 AND 512),
		plugin_release_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(plugin_release_digest AS BLOB)) BETWEEN 1 AND 512),
		plugin_snapshot_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(plugin_snapshot_digest AS BLOB)) BETWEEN 1 AND 128),
		billing_policy_key TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(billing_policy_key AS BLOB)) BETWEEN 1 AND 256),
		pricing_snapshot_json BLOB NOT NULL CHECK(typeof(pricing_snapshot_json) = 'blob' AND json_valid(CAST(pricing_snapshot_json AS TEXT)) AND length(pricing_snapshot_json) BETWEEN 1 AND 65536),
		pricing_snapshot_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(pricing_snapshot_digest AS BLOB)) BETWEEN 1 AND 128),
		meter_key TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(meter_key AS BLOB)) BETWEEN 1 AND 256),
		meter_version TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(meter_version AS BLOB)) BETWEEN 1 AND 256),
		meter_build_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(meter_build_digest AS BLOB)) BETWEEN 1 AND 128),
		source_registry_json BLOB NOT NULL CHECK(typeof(source_registry_json) = 'blob' AND json_valid(CAST(source_registry_json AS TEXT)) AND length(source_registry_json) BETWEEN 1 AND 65536),
		source_registry_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(source_registry_digest AS BLOB)) BETWEEN 1 AND 128),
		release_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(release_digest AS BLOB)) BETWEEN 1 AND 128),
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE UNIQUE INDEX uk_w_agent_usage_meter_release_release_id ON w_agent_usage_meter_release(release_id)`,
	`CREATE UNIQUE INDEX uk_w_agent_usage_meter_release_plugin_snapshot ON w_agent_usage_meter_release(plugin_snapshot_digest)`,
	`CREATE UNIQUE INDEX uk_w_agent_usage_meter_release_digest ON w_agent_usage_meter_release(release_digest)`,
	`CREATE TABLE w_agent_provider_usage_journal (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		receipt_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(receipt_id AS BLOB)) BETWEEN 1 AND 64),
		turn_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(turn_id AS BLOB)) BETWEEN 1 AND 256),
		attempt_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(attempt_id AS BLOB)) BETWEEN 1 AND 64),
		fencing_token INTEGER NOT NULL CHECK(fencing_token BETWEEN 1 AND 9223372036854775807),
		meter_release_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(meter_release_id AS BLOB)) BETWEEN 1 AND 64),
		plugin_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(plugin_id AS BLOB)) BETWEEN 1 AND 512),
		plugin_version TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(plugin_version AS BLOB)) BETWEEN 1 AND 512),
		plugin_release_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(plugin_release_digest AS BLOB)) BETWEEN 1 AND 512),
		plugin_snapshot_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(plugin_snapshot_digest AS BLOB)) BETWEEN 1 AND 128),
		provider_key TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(provider_key AS BLOB)) BETWEEN 1 AND 256),
		provider_account_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(provider_account_digest AS BLOB)) BETWEEN 1 AND 128),
		provider_request_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(provider_request_digest AS BLOB)) BETWEEN 1 AND 128),
		provider_event_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(provider_event_digest AS BLOB)) BETWEEN 1 AND 128),
		source_key TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(source_key AS BLOB)) BETWEEN 1 AND 256),
		source_version TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(source_version AS BLOB)) BETWEEN 1 AND 256),
		source_build_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(source_build_digest AS BLOB)) BETWEEN 1 AND 128),
		source_registration_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(source_registration_digest AS BLOB)) BETWEEN 1 AND 128),
		usage_schema_key TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(usage_schema_key AS BLOB)) BETWEEN 1 AND 256),
		usage_schema_version TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(usage_schema_version AS BLOB)) BETWEEN 1 AND 256),
		source_schema_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(source_schema_digest AS BLOB)) BETWEEN 1 AND 128),
		canonical_usage_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(canonical_usage_digest AS BLOB)) BETWEEN 1 AND 128),
		provider_receipt_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(provider_receipt_digest AS BLOB)) BETWEEN 1 AND 128),
		verification_kind TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(verification_kind AS BLOB)) BETWEEN 1 AND 32),
		verification_key_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(verification_key_digest AS BLOB)) BETWEEN 1 AND 128),
		verification_build_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(verification_build_digest AS BLOB)) BETWEEN 1 AND 128),
		attestation_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(attestation_digest AS BLOB)) BETWEEN 1 AND 128),
		journal_record_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(journal_record_digest AS BLOB)) BETWEEN 1 AND 128),
		provider_usage_json BLOB NOT NULL CHECK(typeof(provider_usage_json) = 'blob' AND json_valid(CAST(provider_usage_json AS TEXT)) AND length(provider_usage_json) BETWEEN 1 AND 65536),
		provider_reported_at DATETIME NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(turn_id, attempt_id, fencing_token) REFERENCES w_agent_turn_attempt(turn_id, attempt_id, fencing_token) ON DELETE RESTRICT ON UPDATE RESTRICT,
		FOREIGN KEY(meter_release_id) REFERENCES w_agent_usage_meter_release(release_id) ON DELETE RESTRICT ON UPDATE RESTRICT
	)`,
	`CREATE UNIQUE INDEX uk_w_agent_provider_usage_journal_receipt_id ON w_agent_provider_usage_journal(receipt_id)`,
	`CREATE UNIQUE INDEX uk_w_agent_provider_usage_journal_provider_event ON w_agent_provider_usage_journal(provider_key, provider_account_digest, provider_event_digest)`,
	`CREATE UNIQUE INDEX uk_w_agent_provider_usage_journal_source_binding ON w_agent_provider_usage_journal(receipt_id, turn_id, meter_release_id, canonical_usage_digest, provider_receipt_digest, journal_record_digest)`,
	`CREATE INDEX idx_w_agent_provider_usage_journal_turn ON w_agent_provider_usage_journal(turn_id, created_at, id)`,
	`CREATE INDEX idx_w_agent_provider_usage_journal_attempt ON w_agent_provider_usage_journal(turn_id, attempt_id, fencing_token, id)`,
	// w_agent_turn_settlement_review - P0-041 durable retry isolation for an
	// ambiguous release, extended by P0-042 with an append-only positive-finalize
	// resolution and P0-044 with a hold for ordinary completed Turns whose usage
	// has not yet been measured. Financial resolution deliberately leaves Effects held.
	`CREATE TABLE w_agent_turn_settlement_review (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		review_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(review_id AS BLOB)) BETWEEN 1 AND 64),
		turn_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(turn_id AS BLOB)) BETWEEN 1 AND 256),
		settlement_key TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(settlement_key AS BLOB)) BETWEEN 1 AND 256),
		request_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(request_digest AS BLOB)) BETWEEN 1 AND 128),
		reason TEXT NOT NULL DEFAULT 'usage_unknown' COLLATE BINARY CHECK(reason IN ('usage_unknown', 'completed_usage_unmeasured', 'terminal_usage_unmeasured')),
		source TEXT NOT NULL COLLATE BINARY CHECK(source IN ('executor_release', 'reconcile_release', 'executor_completion', 'executor_terminal', 'reconcile_terminal')),
		terminal_status TEXT NOT NULL COLLATE BINARY CHECK(terminal_status IN ('completed', 'stopped', 'failed', 'timeout')),
		attempt_id TEXT COLLATE BINARY CHECK(attempt_id IS NULL OR length(CAST(attempt_id AS BLOB)) BETWEEN 1 AND 64),
		fencing_token INTEGER NOT NULL CHECK(fencing_token BETWEEN 1 AND 9223372036854775807),
		operation_id TEXT COLLATE BINARY CHECK(operation_id IS NULL OR length(CAST(operation_id AS BLOB)) BETWEEN 1 AND 128),
		prior_operation_count INTEGER NOT NULL DEFAULT 0 CHECK(prior_operation_count BETWEEN 0 AND 9223372036854775807),
		prior_effect_count INTEGER NOT NULL DEFAULT 0 CHECK(prior_effect_count BETWEEN 0 AND 9223372036854775807),
		prior_provider_usage_count INTEGER NOT NULL DEFAULT 0 CHECK(prior_provider_usage_count BETWEEN 0 AND 9223372036854775807),
		current_effect_count INTEGER NOT NULL DEFAULT 0 CHECK(current_effect_count BETWEEN 0 AND 64),
		status TEXT NOT NULL DEFAULT 'pending' COLLATE BINARY CHECK(status IN ('pending', 'metered_held', 'finalized_held')),
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(review_id),
		UNIQUE(turn_id),
		UNIQUE(settlement_key),
		CHECK(
			source IN ('executor_completion', 'executor_terminal', 'reconcile_terminal')
			OR (source IN ('executor_release', 'reconcile_release')
				AND prior_provider_usage_count = 0
				AND (prior_operation_count > 0 OR prior_effect_count > 0 OR current_effect_count > 0))
		),
		CHECK(
			(source = 'executor_release' AND reason = 'usage_unknown' AND attempt_id IS NOT NULL AND operation_id IS NOT NULL)
			OR (source = 'reconcile_release' AND reason = 'usage_unknown' AND attempt_id IS NULL AND operation_id IS NULL AND current_effect_count = 0)
			OR (source = 'executor_completion' AND reason = 'completed_usage_unmeasured' AND terminal_status = 'completed' AND attempt_id IS NOT NULL AND operation_id IS NOT NULL)
			OR (source = 'executor_terminal' AND reason = 'terminal_usage_unmeasured' AND terminal_status IN ('stopped', 'failed', 'timeout') AND attempt_id IS NOT NULL AND operation_id IS NOT NULL)
			OR (source = 'reconcile_terminal' AND reason = 'terminal_usage_unmeasured' AND terminal_status IN ('stopped', 'failed', 'timeout') AND attempt_id IS NULL AND operation_id IS NULL AND current_effect_count = 0)
		),
		CHECK(updated_at >= created_at),
		FOREIGN KEY(turn_id) REFERENCES w_agent_turn(turn_id) ON DELETE RESTRICT ON UPDATE RESTRICT,
		FOREIGN KEY(turn_id, operation_id, attempt_id, fencing_token) REFERENCES w_agent_turn_operation(turn_id, operation_id, attempt_id, fencing_token) ON DELETE RESTRICT ON UPDATE RESTRICT
	)`,
	`CREATE UNIQUE INDEX uk_w_agent_turn_settlement_review_resolution_binding ON w_agent_turn_settlement_review(review_id, turn_id, settlement_key, request_digest)`,
	`CREATE UNIQUE INDEX uk_w_agent_turn_settlement_review_outcome_binding
		ON w_agent_turn_settlement_review(review_id, turn_id, settlement_key, request_digest, terminal_status)`,
	`CREATE INDEX idx_w_agent_turn_settlement_review_pending ON w_agent_turn_settlement_review(status, created_at, id)`,
	// w_agent_turn_settlement_usage_evidence - P0-043 immutable trusted-meter
	// evidence. One Review can have at most one accepted measurement, while the
	// meter/source identity cannot be replayed into another Review.
	`CREATE TABLE w_agent_turn_settlement_usage_evidence (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		evidence_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(evidence_id AS BLOB)) BETWEEN 1 AND 64),
		review_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(review_id AS BLOB)) BETWEEN 1 AND 64),
		turn_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(turn_id AS BLOB)) BETWEEN 1 AND 256),
		settlement_key TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(settlement_key AS BLOB)) BETWEEN 1 AND 256),
		review_request_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(review_request_digest AS BLOB)) BETWEEN 1 AND 128),
		plugin_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(plugin_id AS BLOB)) BETWEEN 1 AND 512),
		plugin_version TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(plugin_version AS BLOB)) BETWEEN 1 AND 512),
		plugin_release_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(plugin_release_digest AS BLOB)) BETWEEN 1 AND 512),
		billing_policy_key TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(billing_policy_key AS BLOB)) BETWEEN 1 AND 256),
		pricing_snapshot_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(pricing_snapshot_digest AS BLOB)) BETWEEN 1 AND 128),
		meter_key TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(meter_key AS BLOB)) BETWEEN 1 AND 256),
		meter_version TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(meter_version AS BLOB)) BETWEEN 1 AND 256),
		meter_build_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(meter_build_digest AS BLOB)) BETWEEN 1 AND 128),
		meter_release_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(meter_release_id AS BLOB)) BETWEEN 1 AND 64),
		usage_source_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(usage_source_digest AS BLOB)) BETWEEN 1 AND 128),
		source_receipt_count INTEGER NOT NULL CHECK(source_receipt_count BETWEEN 1 AND 64),
		measurement_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(measurement_digest AS BLOB)) BETWEEN 1 AND 128),
		used_units INTEGER NOT NULL CHECK(used_units BETWEEN 1 AND 9223372036854775807),
		meter_receipt_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(meter_receipt_digest AS BLOB)) BETWEEN 1 AND 128),
		evidence_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(evidence_digest AS BLOB)) BETWEEN 1 AND 128),
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		CONSTRAINT fk_w_agent_turn_settlement_usage_evidence_review
			FOREIGN KEY(review_id, turn_id, settlement_key, review_request_digest)
			REFERENCES w_agent_turn_settlement_review(review_id, turn_id, settlement_key, request_digest)
			ON DELETE RESTRICT ON UPDATE RESTRICT,
		CONSTRAINT fk_w_agent_turn_settlement_usage_evidence_meter_release
			FOREIGN KEY(meter_release_id)
			REFERENCES w_agent_usage_meter_release(release_id)
			ON DELETE RESTRICT ON UPDATE RESTRICT
	)`,
	`CREATE UNIQUE INDEX uk_w_agent_turn_settlement_usage_evidence_evidence_id ON w_agent_turn_settlement_usage_evidence(evidence_id)`,
	`CREATE UNIQUE INDEX uk_w_agent_turn_settlement_usage_evidence_review_id ON w_agent_turn_settlement_usage_evidence(review_id)`,
	`CREATE UNIQUE INDEX uk_w_agent_turn_settlement_usage_evidence_meter_source ON w_agent_turn_settlement_usage_evidence(meter_key, meter_version, usage_source_digest)`,
	`CREATE UNIQUE INDEX uk_w_agent_turn_settlement_usage_evidence_resolution_binding ON w_agent_turn_settlement_usage_evidence(review_id, turn_id, settlement_key, review_request_digest, evidence_id, pricing_snapshot_digest, evidence_digest, used_units)`,
	`CREATE UNIQUE INDEX uk_w_agent_turn_settlement_usage_evidence_provenance ON w_agent_turn_settlement_usage_evidence(evidence_id, review_id, turn_id, settlement_key, review_request_digest, meter_release_id, usage_source_digest, evidence_digest, source_receipt_count)`,
	`CREATE INDEX idx_w_agent_turn_settlement_usage_evidence_turn ON w_agent_turn_settlement_usage_evidence(turn_id, created_at, id)`,
	`CREATE TABLE w_agent_turn_settlement_usage_evidence_source (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		evidence_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(evidence_id AS BLOB)) BETWEEN 1 AND 64),
		ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 63),
		review_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(review_id AS BLOB)) BETWEEN 1 AND 64),
		turn_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(turn_id AS BLOB)) BETWEEN 1 AND 256),
		settlement_key TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(settlement_key AS BLOB)) BETWEEN 1 AND 256),
		review_request_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(review_request_digest AS BLOB)) BETWEEN 1 AND 128),
		meter_release_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(meter_release_id AS BLOB)) BETWEEN 1 AND 64),
		usage_source_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(usage_source_digest AS BLOB)) BETWEEN 1 AND 128),
		evidence_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(evidence_digest AS BLOB)) BETWEEN 1 AND 128),
		source_receipt_count INTEGER NOT NULL CHECK(source_receipt_count BETWEEN 1 AND 64),
		receipt_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(receipt_id AS BLOB)) BETWEEN 1 AND 64),
		source_registration_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(source_registration_digest AS BLOB)) BETWEEN 1 AND 128),
		source_schema_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(source_schema_digest AS BLOB)) BETWEEN 1 AND 128),
		canonical_usage_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(canonical_usage_digest AS BLOB)) BETWEEN 1 AND 128),
		provider_receipt_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(provider_receipt_digest AS BLOB)) BETWEEN 1 AND 128),
		journal_record_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(journal_record_digest AS BLOB)) BETWEEN 1 AND 128),
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		CHECK(ordinal < source_receipt_count),
		FOREIGN KEY(evidence_id, review_id, turn_id, settlement_key, review_request_digest,
			meter_release_id, usage_source_digest, evidence_digest, source_receipt_count)
			REFERENCES w_agent_turn_settlement_usage_evidence(evidence_id, review_id, turn_id, settlement_key,
				review_request_digest, meter_release_id, usage_source_digest, evidence_digest, source_receipt_count)
			ON DELETE RESTRICT ON UPDATE RESTRICT,
		FOREIGN KEY(receipt_id, turn_id, meter_release_id, canonical_usage_digest,
			provider_receipt_digest, journal_record_digest)
			REFERENCES w_agent_provider_usage_journal(receipt_id, turn_id, meter_release_id,
				canonical_usage_digest, provider_receipt_digest, journal_record_digest)
			ON DELETE RESTRICT ON UPDATE RESTRICT
	)`,
	`CREATE UNIQUE INDEX uk_w_agent_turn_settlement_usage_evidence_source_ordinal ON w_agent_turn_settlement_usage_evidence_source(evidence_id, ordinal)`,
	`CREATE UNIQUE INDEX uk_w_agent_turn_settlement_usage_evidence_source_receipt ON w_agent_turn_settlement_usage_evidence_source(receipt_id)`,
	`CREATE INDEX idx_w_agent_turn_settlement_usage_evidence_source_turn ON w_agent_turn_settlement_usage_evidence_source(turn_id, evidence_id, ordinal)`,
	`CREATE TABLE w_agent_turn_settlement_review_resolution (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		resolution_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(resolution_id AS BLOB)) BETWEEN 1 AND 64),
		review_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(review_id AS BLOB)) BETWEEN 1 AND 64),
		turn_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(turn_id AS BLOB)) BETWEEN 1 AND 256),
		settlement_key TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(settlement_key AS BLOB)) BETWEEN 1 AND 256),
		review_request_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(review_request_digest AS BLOB)) BETWEEN 1 AND 128),
		evidence_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(evidence_id AS BLOB)) BETWEEN 1 AND 64),
		decision_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(decision_digest AS BLOB)) BETWEEN 1 AND 128),
		resolution_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(resolution_digest AS BLOB)) BETWEEN 1 AND 128),
		intent TEXT NOT NULL DEFAULT 'finalize' COLLATE BINARY CHECK(intent = 'finalize'),
		used_units INTEGER NOT NULL CHECK(used_units BETWEEN 1 AND 9223372036854775807),
		reserved_units INTEGER NOT NULL CHECK(reserved_units BETWEEN used_units AND 9223372036854775807),
		actor_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(actor_id AS BLOB)) BETWEEN 1 AND 256),
		reason TEXT NOT NULL DEFAULT 'metered_usage_confirmed' COLLATE BINARY CHECK(reason = 'metered_usage_confirmed'),
		evidence_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(evidence_digest AS BLOB)) BETWEEN 1 AND 128),
		pricing_snapshot_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(pricing_snapshot_digest AS BLOB)) BETWEEN 1 AND 128),
		authority_receipt_digest TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(authority_receipt_digest AS BLOB)) BETWEEN 1 AND 128),
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		CONSTRAINT fk_w_agent_turn_settlement_review_resolution_review
			FOREIGN KEY(review_id, turn_id, settlement_key, review_request_digest)
			REFERENCES w_agent_turn_settlement_review(review_id, turn_id, settlement_key, request_digest)
			ON DELETE RESTRICT ON UPDATE RESTRICT,
		CONSTRAINT fk_w_agent_turn_settlement_review_resolution_evidence
			FOREIGN KEY(review_id, turn_id, settlement_key, review_request_digest,
				evidence_id, pricing_snapshot_digest, evidence_digest, used_units)
			REFERENCES w_agent_turn_settlement_usage_evidence(review_id, turn_id, settlement_key, review_request_digest,
				evidence_id, pricing_snapshot_digest, evidence_digest, used_units)
			ON DELETE RESTRICT ON UPDATE RESTRICT
	)`,
	`CREATE UNIQUE INDEX uk_w_agent_turn_settlement_review_resolution_resolution_id ON w_agent_turn_settlement_review_resolution(resolution_id)`,
	`CREATE UNIQUE INDEX uk_w_agent_turn_settlement_review_resolution_review_id ON w_agent_turn_settlement_review_resolution(review_id)`,
	`CREATE INDEX idx_w_agent_turn_settlement_review_resolution_binding ON w_agent_turn_settlement_review_resolution(review_id, turn_id, settlement_key, review_request_digest)`,
	`CREATE INDEX idx_w_agent_turn_settlement_review_resolution_evidence ON w_agent_turn_settlement_review_resolution(review_id, turn_id, settlement_key, review_request_digest, evidence_id, pricing_snapshot_digest, evidence_digest, used_units)`,
	`CREATE INDEX idx_w_agent_turn_settlement_review_resolution_turn ON w_agent_turn_settlement_review_resolution(turn_id, created_at, id)`,
	// P0-048 immutable one-to-one admission binding. The outcome below is the
	// sole mutable settlement row for its Turn, Reservation and SettlementKey;
	// its CHECKs mirror the monotonic review/refund/terminal state tuples.
	`CREATE TABLE w_agent_turn_reservation_binding (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		binding_id TEXT NOT NULL COLLATE BINARY CHECK(
			length(CAST(binding_id AS BLOB)) = 64
			AND binding_id NOT GLOB '*[^0-9a-f]*'
		),
		turn_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(turn_id AS BLOB)) BETWEEN 1 AND 256),
		principal_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(principal_id AS BLOB)) BETWEEN 1 AND 128),
		turn_command_digest TEXT NOT NULL COLLATE BINARY CHECK(
			length(CAST(turn_command_digest AS BLOB)) BETWEEN 1 AND 128
			AND turn_command_digest NOT GLOB '*[^!-~]*'
		),
		reservation_id INTEGER NOT NULL,
		reservation_uid INTEGER NOT NULL CHECK(reservation_uid > 0),
		reservation_request_digest TEXT NOT NULL COLLATE BINARY CHECK(
			length(CAST(reservation_request_digest AS BLOB)) = 64
			AND reservation_request_digest NOT GLOB '*[^0-9a-f]*'
		),
		reservation_tool TEXT NOT NULL COLLATE BINARY CHECK(
			length(CAST(reservation_tool AS BLOB)) BETWEEN 1 AND 64
			AND reservation_tool = TRIM(reservation_tool)
		),
		reserved_units INTEGER NOT NULL CHECK(reserved_units BETWEEN 0 AND 2147483647),
		project_id INTEGER NOT NULL CHECK(project_id >= 0),
		pricing_snapshot_digest TEXT NOT NULL COLLATE BINARY CHECK(
			length(CAST(pricing_snapshot_digest AS BLOB)) = 71
			AND substr(pricing_snapshot_digest, 1, 7) = 'sha256:'
			AND substr(pricing_snapshot_digest, 8) NOT GLOB '*[^0-9a-f]*'
		),
		binding_digest TEXT NOT NULL COLLATE BINARY CHECK(
			length(CAST(binding_digest AS BLOB)) = 71
			AND substr(binding_digest, 1, 7) = 'sha256:'
			AND substr(binding_digest, 8) NOT GLOB '*[^0-9a-f]*'
		),
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		CONSTRAINT fk_w_agent_turn_reservation_binding_turn
			FOREIGN KEY(turn_id, principal_id, turn_command_digest)
			REFERENCES w_agent_turn(turn_id, principal_id, command_digest)
			ON DELETE RESTRICT ON UPDATE RESTRICT,
		CONSTRAINT fk_w_agent_turn_reservation_binding_reservation
			FOREIGN KEY(reservation_id, reservation_uid, reservation_request_digest,
				reservation_tool, reserved_units, project_id)
			REFERENCES w_credit_reservation(id, uid, request_digest, tool, reserved, project_id)
			ON DELETE RESTRICT ON UPDATE RESTRICT
	)`,
	`CREATE UNIQUE INDEX uk_w_agent_turn_reservation_binding_binding_id
		ON w_agent_turn_reservation_binding(binding_id)`,
	`CREATE UNIQUE INDEX uk_w_agent_turn_reservation_binding_turn_id
		ON w_agent_turn_reservation_binding(turn_id)`,
	`CREATE UNIQUE INDEX uk_w_agent_turn_reservation_binding_reservation_id
		ON w_agent_turn_reservation_binding(reservation_id)`,
	`CREATE UNIQUE INDEX uk_w_agent_turn_reservation_binding_exact
		ON w_agent_turn_reservation_binding(binding_id, turn_id, reservation_id, binding_digest, reserved_units)`,
	`CREATE TABLE w_agent_turn_settlement_outcome (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		outcome_id TEXT NOT NULL COLLATE BINARY CHECK(
			length(CAST(outcome_id AS BLOB)) = 64
			AND outcome_id NOT GLOB '*[^0-9a-f]*'
		),
		binding_id TEXT NOT NULL COLLATE BINARY CHECK(
			length(CAST(binding_id AS BLOB)) = 64
			AND binding_id NOT GLOB '*[^0-9a-f]*'
		),
		turn_id TEXT NOT NULL COLLATE BINARY CHECK(length(CAST(turn_id AS BLOB)) BETWEEN 1 AND 256),
		reservation_id INTEGER NOT NULL,
		binding_digest TEXT NOT NULL COLLATE BINARY CHECK(
			length(CAST(binding_digest AS BLOB)) = 71
			AND substr(binding_digest, 1, 7) = 'sha256:'
			AND substr(binding_digest, 8) NOT GLOB '*[^0-9a-f]*'
		),
		settlement_key TEXT NOT NULL COLLATE BINARY CHECK(
			length(CAST(settlement_key AS BLOB)) = 86
			AND substr(settlement_key, 1, 22) = 'wm:turn-settlement:v1:'
			AND substr(settlement_key, 23) NOT GLOB '*[^0-9a-f]*'
		),
		ledger_request_digest TEXT NOT NULL COLLATE BINARY CHECK(
			length(CAST(ledger_request_digest AS BLOB)) = 71
			AND substr(ledger_request_digest, 1, 7) = 'sha256:'
			AND substr(ledger_request_digest, 8) NOT GLOB '*[^0-9a-f]*'
		),
		authorization_kind TEXT NOT NULL COLLATE BINARY,
		attempt_id TEXT COLLATE BINARY,
		fencing_token INTEGER NOT NULL CHECK(fencing_token BETWEEN 1 AND 9223372036854775807),
		operation_id TEXT COLLATE BINARY,
		terminal_status TEXT NOT NULL COLLATE BINARY CHECK(terminal_status IN ('completed', 'stopped', 'failed', 'timeout')),
		requested_intent TEXT NOT NULL COLLATE BINARY,
		used_units INTEGER,
		reserved_units INTEGER NOT NULL CHECK(reserved_units BETWEEN 0 AND 2147483647),
		status TEXT NOT NULL COLLATE BINARY,
		refund_target TEXT COLLATE BINARY,
		refund_due INTEGER NOT NULL DEFAULT 0,
		reservation_state_version INTEGER NOT NULL CHECK(reservation_state_version BETWEEN 1 AND 9223372036854775807),
		review_id TEXT COLLATE BINARY,
		review_request_digest TEXT COLLATE BINARY CHECK(review_request_digest IS NULL OR (
			length(CAST(review_request_digest AS BLOB)) = 71
			AND substr(review_request_digest, 1, 7) = 'sha256:'
			AND substr(review_request_digest, 8) NOT GLOB '*[^0-9a-f]*'
		)),
		resolution_id TEXT COLLATE BINARY,
		resolution_request_digest TEXT COLLATE BINARY CHECK(resolution_request_digest IS NULL OR (
			length(CAST(resolution_request_digest AS BLOB)) = 71
			AND substr(resolution_request_digest, 1, 7) = 'sha256:'
			AND substr(resolution_request_digest, 8) NOT GLOB '*[^0-9a-f]*'
		)),
		outcome_digest TEXT NOT NULL COLLATE BINARY CHECK(
			length(CAST(outcome_digest AS BLOB)) = 71
			AND substr(outcome_digest, 1, 7) = 'sha256:'
			AND substr(outcome_digest, 8) NOT GLOB '*[^0-9a-f]*'
		),
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		CONSTRAINT chk_w_agent_turn_settlement_outcome_authorization CHECK(
			(authorization_kind = 'operation'
				AND attempt_id IS NOT NULL AND length(CAST(attempt_id AS BLOB)) BETWEEN 1 AND 64
				AND operation_id IS NOT NULL AND length(CAST(operation_id AS BLOB)) BETWEEN 1 AND 128)
			OR
			(authorization_kind = 'reconcile' AND attempt_id IS NULL AND operation_id IS NULL)
		),
		CONSTRAINT chk_w_agent_turn_settlement_outcome_amounts CHECK(
			(used_units IS NULL OR used_units BETWEEN 0 AND reserved_units)
			AND refund_due BETWEEN 0 AND reserved_units
		),
		CONSTRAINT chk_w_agent_turn_settlement_outcome_review_tuple CHECK(
			(requested_intent = 'review'
				AND review_id IS NOT NULL AND length(CAST(review_id AS BLOB)) BETWEEN 1 AND 64
				AND review_request_digest IS NOT NULL)
			OR
			(requested_intent IN ('finalize', 'release')
				AND review_id IS NULL AND review_request_digest IS NULL)
		),
		CONSTRAINT chk_w_agent_turn_settlement_outcome_resolution_tuple CHECK(
			(resolution_id IS NULL AND resolution_request_digest IS NULL)
			OR
			(requested_intent = 'review'
				AND resolution_id IS NOT NULL
				AND length(CAST(resolution_id AS BLOB)) = 64
				AND resolution_id NOT GLOB '*[^0-9a-f]*'
				AND resolution_request_digest IS NOT NULL
				AND status IN ('refund_pending', 'finalized'))
		),
		CONSTRAINT chk_w_agent_turn_settlement_outcome_state_tuple CHECK(
			(status = 'review_held'
				AND requested_intent = 'review' AND used_units IS NULL
				AND refund_target IS NULL AND refund_due = 0
				AND resolution_id IS NULL AND resolution_request_digest IS NULL)
			OR
			(status = 'refund_pending'
				AND used_units IS NOT NULL AND refund_target IN ('finalized', 'released')
				AND refund_due > 0 AND refund_due = reserved_units - used_units
				AND ((refund_target = 'finalized'
						AND requested_intent IN ('finalize', 'review')
						AND (requested_intent <> 'review' OR resolution_id IS NOT NULL))
					OR (refund_target = 'released'
						AND requested_intent = 'release' AND used_units = 0)))
			OR
			(status = 'finalized'
				AND requested_intent IN ('finalize', 'review') AND used_units IS NOT NULL
				AND refund_target IS NULL AND refund_due = 0
				AND (requested_intent <> 'review' OR resolution_id IS NOT NULL))
			OR
			(status = 'released'
				AND requested_intent = 'release' AND used_units = 0
				AND refund_target IS NULL AND refund_due = 0
				AND resolution_id IS NULL AND resolution_request_digest IS NULL)
		),
		CHECK(updated_at >= created_at),
		CONSTRAINT fk_w_agent_turn_settlement_outcome_binding
			FOREIGN KEY(binding_id, turn_id, reservation_id, binding_digest, reserved_units)
			REFERENCES w_agent_turn_reservation_binding(binding_id, turn_id, reservation_id, binding_digest, reserved_units)
			ON DELETE RESTRICT ON UPDATE RESTRICT,
		CONSTRAINT fk_w_agent_turn_settlement_outcome_turn_fence
			FOREIGN KEY(turn_id, fencing_token, terminal_status)
			REFERENCES w_agent_turn(turn_id, fencing_token, status)
			ON DELETE RESTRICT ON UPDATE RESTRICT,
		CONSTRAINT fk_w_agent_turn_settlement_outcome_operation
			FOREIGN KEY(turn_id, operation_id, attempt_id, fencing_token, terminal_status)
			REFERENCES w_agent_turn_operation(turn_id, operation_id, attempt_id, fencing_token, turn_status)
			ON DELETE RESTRICT ON UPDATE RESTRICT,
		CONSTRAINT fk_w_agent_turn_settlement_outcome_review
			FOREIGN KEY(review_id, turn_id, settlement_key, review_request_digest, terminal_status)
			REFERENCES w_agent_turn_settlement_review(review_id, turn_id, settlement_key, request_digest, terminal_status)
			ON DELETE RESTRICT ON UPDATE RESTRICT
	)`,
	`CREATE UNIQUE INDEX uk_w_agent_turn_settlement_outcome_outcome_id
		ON w_agent_turn_settlement_outcome(outcome_id)`,
	`CREATE UNIQUE INDEX uk_w_agent_turn_settlement_outcome_settlement_key
		ON w_agent_turn_settlement_outcome(settlement_key)`,
	`CREATE UNIQUE INDEX uk_w_agent_turn_settlement_outcome_turn_id
		ON w_agent_turn_settlement_outcome(turn_id)`,
	`CREATE UNIQUE INDEX uk_w_agent_turn_settlement_outcome_reservation_id
		ON w_agent_turn_settlement_outcome(reservation_id)`,
	`CREATE UNIQUE INDEX uk_w_agent_turn_settlement_outcome_review_id
		ON w_agent_turn_settlement_outcome(review_id)`,
	`CREATE INDEX idx_w_agent_turn_settlement_outcome_recovery
		ON w_agent_turn_settlement_outcome(status, updated_at, id)`,
	// Sprint-E 7/8: w_workagent_{brand,character,director_style}_asset
	// DDL retired. Asset library reads/writes now flow through the
	// platform tables w_global_brand / w_global_character /
	// w_global_director_style (see the platform DDL above in this
	// file). The workagent model
	// package's row structs (BrandAsset / CharacterAsset /
	// DirectorStyleAsset) survive solely so test fixture signatures
	// and per-type Summary structs can keep referencing the enum
	// constants (BrandAssetStatusDraft etc.) without a wider
	// rename pass; the structs themselves are no longer wired to
	// any DB table.
	// w_use_case — marketing surface table consumed by the public
	// /api/use-cases/list endpoint. Schema mirrors model.UseCase
	// (server/model/use_case.go) with SQLite-safe types. The
	// status filter (=1 published) is the load-bearing rule — drafts
	// (=2) must never leak to public list / by-slug / by-app-slug
	// reads, and use_case_service_test.go pins all three paths.
	`CREATE TABLE w_global_mcp_connector (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		deleted_at DATETIME,
		uid INTEGER NOT NULL DEFAULT 0,
		name VARCHAR(120) NOT NULL DEFAULT '',
		transport VARCHAR(16) NOT NULL DEFAULT 'stdio',
		command VARCHAR(255) NOT NULL DEFAULT '',
		args TEXT,
		env TEXT,
		url VARCHAR(2048) NOT NULL DEFAULT '',
		headers TEXT,
		enabled TINYINT(1) DEFAULT 1
	)`,
	`CREATE TABLE w_use_case (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME,
		updated_at DATETIME,
		deleted_at DATETIME,
		slug VARCHAR(255) NOT NULL,
		title VARCHAR(255),
		summary VARCHAR(500),
		description TEXT,
		cover VARCHAR(255),
		gallery TEXT,
		app_slug VARCHAR(100),
		prompt_used TEXT,
		steps TEXT,
		tags TEXT,
		difficulty VARCHAR(20) DEFAULT 'Beginner',
		content TEXT,
		author TEXT,
		seo TEXT,
		published_at DATETIME,
		status INTEGER DEFAULT 1,
		lang VARCHAR(10) DEFAULT 'en',
		UNIQUE (slug, lang)
	)`,
}
