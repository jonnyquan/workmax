//go:build desktop

package migrationsdesktop

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestApplyRunsMessageCreatedOrderIndexMigration(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "migrations.db")), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	applied, err := Apply(db)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(applied) != 4 || applied[0] != "0001" || applied[1] != "0002" || applied[2] != "0003" || applied[3] != "0004" {
		t.Fatalf("applied: got %v, want [0001 0002 0003 0004]", applied)
	}

	var indexCount int
	if err := db.Raw(`
		SELECT COUNT(*)
		  FROM sqlite_master
		 WHERE type = 'index'
		   AND name = 'idx_w_workagent_message_thread_created'
	`).Row().Scan(&indexCount); err != nil {
		t.Fatalf("scan index: %v", err)
	}
	if indexCount != 1 {
		t.Fatalf("index count: got %d, want 1", indexCount)
	}
	var intentTableCount int
	if err := db.Raw(`
		SELECT COUNT(*)
		  FROM sqlite_master
		 WHERE type = 'table'
		   AND name = 'w_desktop_agent_turn_intent'
	`).Row().Scan(&intentTableCount); err != nil {
		t.Fatalf("scan intent table: %v", err)
	}
	if intentTableCount != 1 {
		t.Fatalf("intent table count: got %d, want 1", intentTableCount)
	}
	rows, err := db.Raw(`PRAGMA table_info(w_desktop_agent_turn_intent)`).Rows()
	if err != nil {
		t.Fatalf("inspect intent columns: %v", err)
	}
	var columns []string
	var turnUUIDPrimaryKey int
	for rows.Next() {
		var ordinal, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&ordinal, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan intent column: %v", err)
		}
		columns = append(columns, name)
		if name == "turn_uuid" {
			turnUUIDPrimaryKey = primaryKey
		}
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close intent column rows: %v", err)
	}
	wantColumns := "uid,turn_uuid,thread_id,thread_uuid,user_text,chat_mode,request_digest,state,last_error_kind,created_at,updated_at"
	if got := strings.Join(columns, ","); got != wantColumns {
		t.Fatalf("intent columns=%s, want %s", got, wantColumns)
	}
	if turnUUIDPrimaryKey != 1 {
		t.Fatalf("turn_uuid primary key flag=%d, want 1", turnUUIDPrimaryKey)
	}
	if err := db.Exec(`
		INSERT INTO w_desktop_agent_turn_intent
			(uid, turn_uuid, thread_id, thread_uuid, user_text, chat_mode,
			 request_digest, state, last_error_kind, created_at, updated_at)
		VALUES (1, 'de305d54-75b4-431b-adb2-eb6b9e546014', 1, 'thread', 'text',
		        'ppt', 'digest', 'invalid', '', '2026-08-06T00:00:00Z', '2026-08-06T00:00:00Z')
	`).Error; err == nil {
		t.Fatal("intent state CHECK accepted an invalid state")
	}

	appliedAgain, err := Apply(db)
	if err != nil {
		t.Fatalf("apply again: %v", err)
	}
	if len(appliedAgain) != 0 {
		t.Fatalf("second apply should be no-op, got %v", appliedAgain)
	}
}

func TestApplyRejectsFutureSchemaVersion(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "future.db")), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Exec(`
		CREATE TABLE _schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL
		)
	`).Error; err != nil {
		t.Fatalf("create migrations table: %v", err)
	}
	if err := db.Exec(`
		INSERT INTO _schema_migrations (version, applied_at)
		VALUES ('9999', '2026-05-20T00:00:00Z')
	`).Error; err != nil {
		t.Fatalf("seed future version: %v", err)
	}

	applied, err := Apply(db)
	if err == nil {
		t.Fatal("Apply should reject a database with a future migration")
	}
	if len(applied) != 0 {
		t.Fatalf("future schema must not apply local migrations first, got %v", applied)
	}
	if !strings.Contains(err.Error(), "newer than this sidecar supports") {
		t.Fatalf("error should explain future schema, got: %v", err)
	}
}
