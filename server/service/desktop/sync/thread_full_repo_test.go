package sync

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// openThreadFullTestDB stands up SQLite with the full thread schema
// (including the heavy prompt / latest_plan / plan_history columns
// the single-fetch endpoint reads).
func openThreadFullTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "thread_full.db")), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE w_workagent_thread (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL DEFAULT 0,
		uuid TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL DEFAULT '',
		agent_mode TEXT NOT NULL DEFAULT 'ppt',
		agent_type TEXT NOT NULL DEFAULT 'general_agent',
		model TEXT NOT NULL DEFAULT '',
		workspace_path TEXT,
		max_tokens INTEGER NOT NULL DEFAULT 0,
		context_count INTEGER NOT NULL DEFAULT 0,
		presence_penalty REAL NOT NULL DEFAULT 0,
		frequency_penalty REAL NOT NULL DEFAULT 0,
		temperature REAL NOT NULL DEFAULT 0,
		prompt TEXT,
		message_count INTEGER NOT NULL DEFAULT 0,
		msg_preview TEXT NOT NULL DEFAULT '',
		file_count INTEGER NOT NULL DEFAULT 0,
		is_public INTEGER NOT NULL DEFAULT 0,
		latest_plan TEXT,
		plan_history TEXT,
		updated_at TEXT,
		created_at TEXT
	)`).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func TestGetThreadByCloudID_HappyPath(t *testing.T) {
	db := openThreadFullTestDB(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := db.Exec(
		`INSERT INTO w_workagent_thread
		   (uid, uuid, name, agent_mode, agent_type, model,
		    workspace_path, max_tokens, context_count,
		    presence_penalty, frequency_penalty, temperature,
		    prompt, message_count, msg_preview, file_count, is_public,
		    latest_plan, plan_history, updated_at, created_at)
		 VALUES (7, 'u-full', 'Full Thread', 'ppt', 'general_agent', 'work-pro',
		         '/ws/abc', 4096, 8, 0.1, 0.2, 0.7,
		         'You are a slide designer', 12, 'preview', 3, 0,
		         '{"step":"draft"}', '[{"t":"first"}]',
		         ?, ?)`,
		now, now,
	).Error; err != nil {
		t.Fatal(err)
	}
	var threadID uint64
	if err := db.Raw(`SELECT id FROM w_workagent_thread WHERE uuid = ?`, "u-full").
		Row().Scan(&threadID); err != nil {
		t.Fatal(err)
	}

	row, err := GetThreadByCloudID(context.Background(), db, 7, threadID)
	if err != nil {
		t.Fatal(err)
	}
	if row.UUID != "u-full" || row.Name != "Full Thread" {
		t.Errorf("identity: %+v", row)
	}
	if row.AgentMode != "ppt" || row.AgentType != "general_agent" || row.Model != "work-pro" {
		t.Errorf("agent meta: %+v", row)
	}
	if row.WorkspacePath != "/ws/abc" || row.MaxTokens != 4096 || row.ContextCount != 8 {
		t.Errorf("control plane: %+v", row)
	}
	if row.Temperature < 0.69 || row.Temperature > 0.71 {
		t.Errorf("temperature: %v", row.Temperature)
	}
	if row.Prompt != "You are a slide designer" {
		t.Errorf("prompt: %q", row.Prompt)
	}
	if row.LatestPlan != `{"step":"draft"}` || row.PlanHistory != `[{"t":"first"}]` {
		t.Errorf("plan blobs: latest=%q history=%q", row.LatestPlan, row.PlanHistory)
	}
	if row.MessageCount != 12 || row.FileCount != 3 {
		t.Errorf("counts: %+v", row)
	}
}

func TestGetThreadByCloudID_NotOwnedReturnsErrThreadNotFound(t *testing.T) {
	db := openThreadFullTestDB(t)
	if err := db.Exec(
		`INSERT INTO w_workagent_thread (uid, uuid, name) VALUES (42, 'u-other', 'T')`,
	).Error; err != nil {
		t.Fatal(err)
	}
	var threadID uint64
	db.Raw(`SELECT id FROM w_workagent_thread WHERE uuid = ?`, "u-other").Row().Scan(&threadID)

	// uid=7 tries to read uid=42's thread.
	_, err := GetThreadByCloudID(context.Background(), db, 7, threadID)
	if !errors.Is(err, ErrThreadNotFound) {
		t.Errorf("IDOR: expected ErrThreadNotFound, got %v", err)
	}
}

func TestGetThreadByCloudID_MissingReturnsErrThreadNotFound(t *testing.T) {
	db := openThreadFullTestDB(t)
	_, err := GetThreadByCloudID(context.Background(), db, 7, 9999)
	if !errors.Is(err, ErrThreadNotFound) {
		t.Errorf("missing: expected ErrThreadNotFound (don't leak existence), got %v", err)
	}
}

func TestGetThreadByCloudID_NullableFieldsRoundTripAsEmpty(t *testing.T) {
	// Verify COALESCE handles NULL prompt / latest_plan / plan_history
	// gracefully — the renderer expects empty string, not a Go zero
	// value embedded inside the JSON struct.
	db := openThreadFullTestDB(t)
	if err := db.Exec(
		`INSERT INTO w_workagent_thread (uid, uuid, name) VALUES (7, 'u-bare', 'T')`,
	).Error; err != nil {
		t.Fatal(err)
	}
	var threadID uint64
	db.Raw(`SELECT id FROM w_workagent_thread WHERE uuid = ?`, "u-bare").Row().Scan(&threadID)

	row, err := GetThreadByCloudID(context.Background(), db, 7, threadID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Prompt != "" || row.LatestPlan != "" || row.PlanHistory != "" {
		t.Errorf("NULL fields should COALESCE to empty: prompt=%q plan=%q history=%q",
			row.Prompt, row.LatestPlan, row.PlanHistory)
	}
	if row.WorkspacePath != "" {
		t.Errorf("NULL workspace_path: %q", row.WorkspacePath)
	}
}

func TestGetThreadByCloudID_RejectsBadParams(t *testing.T) {
	if _, err := GetThreadByCloudID(context.Background(), nil, 7, 1); err == nil {
		t.Error("nil db")
	}
	db := openThreadFullTestDB(t)
	if _, err := GetThreadByCloudID(context.Background(), db, 0, 1); err == nil {
		t.Error("uid=0")
	}
	if _, err := GetThreadByCloudID(context.Background(), db, 7, 0); err == nil {
		t.Error("cloud_thread_id=0")
	}
}
