//go:build desktop

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

	cloudproxy "server/desktop/cloud_proxy"
)

const createdThreadTestUUID = "de305d54-75b4-431b-adb2-eb6b9e546014"

func validCreatedThreadTestItem() cloudproxy.ThreadDeltaItem {
	return cloudproxy.ThreadDeltaItem{
		Action:        "upsert",
		CloudThreadID: "42",
		UUID:          createdThreadTestUUID,
		Name:          "Design deck",
		AgentMode:     "ppt",
		AgentType:     "general_agent",
		Model:         "work-pro",
		MessageCount:  0,
		MsgPreview:    "",
		FileCount:     0,
		IsPublic:      false,
		CreatedAt:     "2026-08-06T09:00:00Z",
		UpdatedAt:     "2026-08-06T10:00:00Z",
	}
}

func createdThreadTestLease(t *testing.T, uid uint) (*cloudproxy.TokenStore, cloudproxy.SessionLease, context.Context) {
	t.Helper()
	store := cloudproxy.NewTokenStore(newMemKeychainForJob())
	if err := store.Save(cloudproxy.TokenPair{
		AccessToken:      mintJWTWithUID(uid),
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatalf("seed TokenStore: %v", err)
	}
	lease, err := store.AcquireSessionLease()
	if err != nil {
		t.Fatalf("AcquireSessionLease: %v", err)
	}
	ctx, release := lease.BindContext(context.Background())
	t.Cleanup(release)
	return store, lease, ctx
}

// openWriterTestDB builds a SQLite DB with the w_workagent_thread
// columns UpsertThreads writes. Matches the production migration
// (server/desktop/migrations_desktop/0001_init_workagent_tables.sql)
// at the column-coverage level — keeping schema parity here means
// a test failure surfaces real migration drift.
func openWriterTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "writer.db")), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Exec(`CREATE TABLE w_workagent_thread (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL DEFAULT 0,
		uuid TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL DEFAULT '',
		agent_mode TEXT NOT NULL DEFAULT 'ppt',
		agent_type TEXT NOT NULL DEFAULT 'general_agent',
		model TEXT NOT NULL DEFAULT '',
		message_count INTEGER NOT NULL DEFAULT 0,
		msg_preview TEXT NOT NULL DEFAULT '',
		file_count INTEGER NOT NULL DEFAULT 0,
		is_public INTEGER NOT NULL DEFAULT 0,
		cloud_sync_state TEXT NOT NULL DEFAULT 'synced',
		cloud_thread_id TEXT,
		last_synced_at TEXT,
		created_at TEXT,
		updated_at TEXT
	)`).Error; err != nil {
		t.Fatalf("create thread table: %v", err)
	}
	// P1.A.5a: deleteLocalThread cascades to w_workagent_message,
	// so every test using this DB needs the messages table present
	// (even tests that never write a message — the DELETE runs against
	// an empty table = 0 rows affected = success).
	if err := db.Exec(`CREATE TABLE w_workagent_message (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL DEFAULT 0,
		uuid TEXT NOT NULL UNIQUE,
		thread_id INTEGER NOT NULL DEFAULT 0,
		user_text TEXT, ai_text TEXT,
		chat_mode TEXT, streaming_state TEXT DEFAULT 'complete',
		updated_at TEXT, created_at TEXT
	)`).Error; err != nil {
		t.Fatalf("create message table: %v", err)
	}
	return db
}

func readThreadRow(t *testing.T, db *gorm.DB, uuid string) (uid int, name, mode, agentType, state, cloudID string) {
	t.Helper()
	row := db.Raw(`SELECT uid, name, agent_mode, agent_type, cloud_sync_state, COALESCE(cloud_thread_id, '')
	                 FROM w_workagent_thread WHERE uuid = ?`, uuid).Row()
	if err := row.Scan(&uid, &name, &mode, &agentType, &state, &cloudID); err != nil {
		t.Fatalf("read row uuid=%s: %v", uuid, err)
	}
	return
}

func TestUpsertThreads_InsertsNewRow(t *testing.T) {
	db := openWriterTestDB(t)
	items := []cloudproxy.ThreadDeltaItem{
		{
			Action: "upsert", CloudThreadID: "42", UUID: "u-a",
			Name: "Hello", AgentMode: "ppt", AgentType: "general_agent",
			Model: "work-pro", MessageCount: 3, MsgPreview: "preview",
			FileCount: 0, IsPublic: false,
			UpdatedAt: "2026-05-17T22:00:00Z",
			CreatedAt: "2026-05-17T21:00:00Z",
		},
	}
	n, err := UpsertThreads(db, items, 7, nil)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if n != 1 {
		t.Errorf("count: got %d, want 1", n)
	}
	uid, name, mode, agentType, state, cloudID := readThreadRow(t, db, "u-a")
	if uid != 7 || name != "Hello" || mode != "ppt" || agentType != "general_agent" || state != "synced" || cloudID != "42" {
		t.Errorf("row: uid=%d name=%q mode=%q agentType=%q state=%q cloudID=%q", uid, name, mode, agentType, state, cloudID)
	}
}

func TestUpsertThreads_DefaultsMissingAgentTypeToGeneralAgent(t *testing.T) {
	db := openWriterTestDB(t)
	n, err := UpsertThreads(db, []cloudproxy.ThreadDeltaItem{
		{
			Action:        "upsert",
			CloudThreadID: "42",
			UUID:          "u-default-agent-type",
			Name:          "Missing agent type",
			AgentMode:     "ppt",
			UpdatedAt:     "2026-05-17T22:00:00Z",
		},
	}, 7, nil)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if n != 1 {
		t.Fatalf("count: got %d, want 1", n)
	}
	_, _, _, agentType, _, _ := readThreadRow(t, db, "u-default-agent-type")
	if agentType != "general_agent" {
		t.Fatalf("agent_type should default to general_agent, got %q", agentType)
	}
}

func TestUpsertThreads_UpdatesExistingRow(t *testing.T) {
	db := openWriterTestDB(t)
	// First insert.
	_, err := UpsertThreads(db, []cloudproxy.ThreadDeltaItem{
		{Action: "upsert", CloudThreadID: "42", UUID: "u-x", Name: "Original",
			AgentMode: "ppt", AgentType: "general_agent",
			UpdatedAt: "2026-05-17T22:00:00Z"},
	}, 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Re-upsert with new name.
	_, err = UpsertThreads(db, []cloudproxy.ThreadDeltaItem{
		{Action: "upsert", CloudThreadID: "42", UUID: "u-x", Name: "Renamed",
			AgentMode: "ppt", AgentType: "general_agent",
			UpdatedAt: "2026-05-17T22:30:00Z"},
	}, 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, name, _, _, _, _ := readThreadRow(t, db, "u-x")
	if name != "Renamed" {
		t.Errorf("after re-upsert, name = %q, want Renamed", name)
	}
	// Confirm we have ONE row total (no dup).
	var count int64
	db.Raw(`SELECT count(*) FROM w_workagent_thread`).Row().Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 row after upsert, got %d", count)
	}
}

func TestUpsertThreads_PreservesPausedStateOnRefresh(t *testing.T) {
	db := openWriterTestDB(t)
	if _, err := UpsertThreads(db, []cloudproxy.ThreadDeltaItem{
		{Action: "upsert", CloudThreadID: "42", UUID: "u-paused", Name: "Original",
			AgentMode: "ppt", AgentType: "general_agent", UpdatedAt: "2026-05-17T22:00:00Z"},
	}, 7, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`UPDATE w_workagent_thread SET cloud_sync_state = 'paused' WHERE uuid = 'u-paused'`).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := UpsertThreads(db, []cloudproxy.ThreadDeltaItem{
		{Action: "upsert", CloudThreadID: "42", UUID: "u-paused", Name: "Renamed",
			AgentMode: "ppt", AgentType: "general_agent", UpdatedAt: "2026-05-17T22:30:00Z"},
	}, 7, nil); err != nil {
		t.Fatal(err)
	}

	_, name, _, _, state, _ := readThreadRow(t, db, "u-paused")
	if name != "Renamed" {
		t.Fatalf("metadata should still refresh while paused, name=%q", name)
	}
	if state != "paused" {
		t.Fatalf("paused local preference should survive cloud upsert, got %q", state)
	}
}

func TestUpsertThreads_BatchHappyPath(t *testing.T) {
	db := openWriterTestDB(t)
	items := []cloudproxy.ThreadDeltaItem{
		{Action: "upsert", CloudThreadID: "1", UUID: "u-1", Name: "A", UpdatedAt: "2026-05-17T22:00:00Z"},
		{Action: "upsert", CloudThreadID: "2", UUID: "u-2", Name: "B", UpdatedAt: "2026-05-17T22:01:00Z"},
		{Action: "upsert", CloudThreadID: "3", UUID: "u-3", Name: "C", UpdatedAt: "2026-05-17T22:02:00Z"},
	}
	n, err := UpsertThreads(db, items, 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("count: %d, want 3", n)
	}
}

func TestUpsertThreads_UnknownActionAborts(t *testing.T) {
	// "delete" is now a valid action (P1.A.5a); use a different
	// unknown value to pin the still-rejected behavior.
	db := openWriterTestDB(t)
	items := []cloudproxy.ThreadDeltaItem{
		{Action: "upsert", CloudThreadID: "1", UUID: "u-1", UpdatedAt: "2026-05-17T22:00:00Z"},
		{Action: "weird_future_action", CloudThreadID: "2", UUID: "u-2"},
		{Action: "upsert", CloudThreadID: "3", UUID: "u-3", UpdatedAt: "2026-05-17T22:02:00Z"},
	}
	n, err := UpsertThreads(db, items, 7, nil)
	if err == nil {
		t.Fatal("expected error on unknown action")
	}
	if !errors.Is(err, ErrUnknownAction) {
		t.Errorf("expected ErrUnknownAction, got %v", err)
	}
	if n != 1 {
		t.Errorf("count: got %d, want 1 (one row before abort)", n)
	}
	var count int64
	db.Raw(`SELECT count(*) FROM w_workagent_thread`).Row().Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 row, got %d", count)
	}
}

// === Delete-action tests (P1.A.5a) ===

func TestUpsertThreads_DeleteRemovesRow(t *testing.T) {
	db := openWriterTestDB(t)
	// Seed: insert via upsert.
	if _, err := UpsertThreads(db, []cloudproxy.ThreadDeltaItem{
		{Action: "upsert", CloudThreadID: "1", UUID: "u-del", Name: "Doomed",
			UpdatedAt: "2026-05-17T22:00:00Z"},
	}, 7, nil); err != nil {
		t.Fatal(err)
	}
	// Confirm row exists.
	var count int64
	db.Raw(`SELECT count(*) FROM w_workagent_thread WHERE uuid = ?`, "u-del").Row().Scan(&count)
	if count != 1 {
		t.Fatalf("seed: row should exist, got %d", count)
	}

	// Delete via action="delete".
	n, err := UpsertThreads(db, []cloudproxy.ThreadDeltaItem{
		{Action: "delete", CloudThreadID: "1", UUID: "u-del"},
	}, 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("delete count: got %d, want 1", n)
	}
	db.Raw(`SELECT count(*) FROM w_workagent_thread WHERE uuid = ?`, "u-del").Row().Scan(&count)
	if count != 0 {
		t.Errorf("row should be gone, got %d", count)
	}
}

func TestUpsertThreads_DeleteCascadesMessages(t *testing.T) {
	db := openWriterTestDB(t) // helper now creates both tables
	// Seed thread + 3 messages.
	if _, err := UpsertThreads(db, []cloudproxy.ThreadDeltaItem{
		{Action: "upsert", CloudThreadID: "1", UUID: "u-cascade", Name: "T",
			UpdatedAt: "2026-05-17T22:00:00Z"},
	}, 7, nil); err != nil {
		t.Fatal(err)
	}
	var threadID int64
	db.Raw(`SELECT id FROM w_workagent_thread WHERE uuid = ?`, "u-cascade").Row().Scan(&threadID)
	for i := 0; i < 3; i++ {
		db.Exec(`INSERT INTO w_workagent_message (uid, uuid, thread_id) VALUES (7, ?, ?)`,
			"m-"+string('a'+rune(i)), threadID)
	}
	var msgCount int64
	db.Raw(`SELECT count(*) FROM w_workagent_message WHERE thread_id = ?`, threadID).Row().Scan(&msgCount)
	if msgCount != 3 {
		t.Fatalf("seed: want 3 messages, got %d", msgCount)
	}

	// Delete thread → cascade messages.
	if _, err := UpsertThreads(db, []cloudproxy.ThreadDeltaItem{
		{Action: "delete", UUID: "u-cascade"},
	}, 7, nil); err != nil {
		t.Fatal(err)
	}
	db.Raw(`SELECT count(*) FROM w_workagent_message WHERE thread_id = ?`, threadID).Row().Scan(&msgCount)
	if msgCount != 0 {
		t.Errorf("cascade: messages should be gone, got %d", msgCount)
	}
}

func TestUpsertThreads_DeleteMissingThreadIsNoOp(t *testing.T) {
	db := openWriterTestDB(t)
	n, err := UpsertThreads(db, []cloudproxy.ThreadDeltaItem{
		{Action: "delete", UUID: "u-never-existed"},
	}, 7, nil)
	if err != nil {
		t.Errorf("delete of nonexistent uuid should not error: %v", err)
	}
	if n != 0 {
		t.Errorf("count: got %d, want 0", n)
	}
}

func TestUpsertThreads_DeleteFiltersByUID(t *testing.T) {
	// Defense-in-depth: deleting a uuid that belongs to a
	// different uid should be a no-op, not actually delete.
	db := openWriterTestDB(t)
	if _, err := UpsertThreads(db, []cloudproxy.ThreadDeltaItem{
		{Action: "upsert", UUID: "u-other", Name: "T", UpdatedAt: "2026-05-17T22:00:00Z"},
	}, 99, nil); err != nil {
		t.Fatal(err)
	}
	// uid=7 tries to delete uid=99's row.
	n, err := UpsertThreads(db, []cloudproxy.ThreadDeltaItem{
		{Action: "delete", UUID: "u-other"},
	}, 7, nil)
	if err != nil {
		t.Errorf("err: %v", err)
	}
	if n != 0 {
		t.Errorf("uid mismatch should not delete: got %d", n)
	}
	var count int64
	db.Raw(`SELECT count(*) FROM w_workagent_thread WHERE uuid = ?`, "u-other").Row().Scan(&count)
	if count != 1 {
		t.Error("uid=99's row should still exist after uid=7's bogus delete")
	}
}

func TestUpsertThreads_UpsertRejectsDifferentUIDConflict(t *testing.T) {
	db := openWriterTestDB(t)
	if _, err := UpsertThreads(db, []cloudproxy.ThreadDeltaItem{
		{Action: "upsert", UUID: "u-shared", Name: "Other user", UpdatedAt: "2026-05-17T22:00:00Z"},
	}, 99, nil); err != nil {
		t.Fatal(err)
	}

	n, err := UpsertThreads(db, []cloudproxy.ThreadDeltaItem{
		{Action: "upsert", UUID: "u-shared", Name: "Active user", UpdatedAt: "2026-05-17T23:00:00Z"},
	}, 7, nil)
	if !errors.Is(err, ErrUIDConflict) {
		t.Fatalf("got %v, want ErrUIDConflict", err)
	}
	if n != 0 {
		t.Fatalf("count: got %d, want 0", n)
	}

	uid, name, _, _, _, _ := readThreadRow(t, db, "u-shared")
	if uid != 99 || name != "Other user" {
		t.Fatalf("conflicting row was modified: uid=%d name=%q", uid, name)
	}
}

func TestUpsertThreads_DeleteClearsMessagesCursor(t *testing.T) {
	db := openWriterTestDB(t)
	// Need _local_meta for the cursor store.
	if err := db.Exec(`CREATE TABLE _local_meta (
		key TEXT PRIMARY KEY, value TEXT NOT NULL,
		updated_at TEXT
	)`).Error; err != nil {
		t.Fatal(err)
	}
	cursorStore := NewCursorStore(db)
	if err := cursorStore.Set(CursorKeyMessagesPrefix+"u-cs", "some-cursor-value"); err != nil {
		t.Fatal(err)
	}
	// Seed thread.
	if _, err := UpsertThreads(db, []cloudproxy.ThreadDeltaItem{
		{Action: "upsert", UUID: "u-cs", Name: "T", UpdatedAt: "2026-05-17T22:00:00Z"},
	}, 7, cursorStore); err != nil {
		t.Fatal(err)
	}
	// Delete with cursor store passed → should also clear the cursor.
	if _, err := UpsertThreads(db, []cloudproxy.ThreadDeltaItem{
		{Action: "delete", UUID: "u-cs"},
	}, 7, cursorStore); err != nil {
		t.Fatal(err)
	}
	got, err := cursorStore.Get(CursorKeyMessagesPrefix + "u-cs")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("cursor should be cleared after delete, got %q", got)
	}
}

func TestUpsertThreads_MissingUUIDAborts(t *testing.T) {
	db := openWriterTestDB(t)
	items := []cloudproxy.ThreadDeltaItem{
		{Action: "upsert", CloudThreadID: "1", UUID: "", Name: "no uuid"},
	}
	_, err := UpsertThreads(db, items, 7, nil)
	if err == nil {
		t.Fatal("expected error on missing uuid")
	}
}

func TestUpsertThreads_DefaultsTimestampsWhenAbsent(t *testing.T) {
	db := openWriterTestDB(t)
	items := []cloudproxy.ThreadDeltaItem{
		{Action: "upsert", CloudThreadID: "1", UUID: "u-1", Name: "T"},
	}
	_, err := UpsertThreads(db, items, 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Defaults should make timestamps non-empty.
	var updatedAt, createdAt string
	db.Raw(`SELECT updated_at, created_at FROM w_workagent_thread WHERE uuid = ?`, "u-1").
		Row().Scan(&updatedAt, &createdAt)
	if updatedAt == "" || createdAt == "" {
		t.Errorf("timestamps default missing: updated=%q created=%q", updatedAt, createdAt)
	}
}

func TestUpsertThreads_RejectsBadParams(t *testing.T) {
	if _, err := UpsertThreads(nil, []cloudproxy.ThreadDeltaItem{}, 7, nil); err == nil {
		t.Error("nil db should error")
	}
	db := openWriterTestDB(t)
	if _, err := UpsertThreads(db, []cloudproxy.ThreadDeltaItem{}, 0, nil); err == nil {
		t.Error("uid=0 should error")
	}
	if _, err := UpsertThreads(db, []cloudproxy.ThreadDeltaItem{}, -1, nil); err == nil {
		t.Error("uid<0 should error")
	}
}

func TestUpsertThreads_LastSyncedAtUpdated(t *testing.T) {
	db := openWriterTestDB(t)
	items := []cloudproxy.ThreadDeltaItem{
		{Action: "upsert", CloudThreadID: "1", UUID: "u-1", UpdatedAt: "2026-05-17T22:00:00Z"},
	}
	before := time.Now().UTC()
	_, _ = UpsertThreads(db, items, 7, nil)
	var lastSynced string
	db.Raw(`SELECT last_synced_at FROM w_workagent_thread WHERE uuid = ?`, "u-1").Row().Scan(&lastSynced)
	parsed, err := time.Parse(time.RFC3339Nano, lastSynced)
	if err != nil {
		t.Fatalf("last_synced_at not RFC3339Nano: %q (%v)", lastSynced, err)
	}
	if parsed.Before(before) {
		t.Errorf("last_synced_at should be after the upsert time: %v vs before %v", parsed, before)
	}
}

func TestCommitCreatedThread_AtomicallyUpsertsWithoutAdvancingCursor(t *testing.T) {
	db := openWriterTestDB(t)
	if err := db.Exec(`CREATE TABLE _local_meta (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		updated_at TEXT
	)`).Error; err != nil {
		t.Fatal(err)
	}
	cursorKey, err := ThreadsCursorKey(7)
	if err != nil {
		t.Fatal(err)
	}
	cursorStore := NewCursorStore(db)
	if err := cursorStore.Set(cursorKey, "before-create"); err != nil {
		t.Fatal(err)
	}
	_, lease, ctx := createdThreadTestLease(t, 7)

	committed, err := CommitCreatedThread(ctx, db, lease, 7, validCreatedThreadTestItem())
	if err != nil {
		t.Fatalf("CommitCreatedThread: %v", err)
	}
	if committed.UUID != createdThreadTestUUID || committed.Name != "Design deck" ||
		committed.AgentMode != "ppt" || committed.MessageCount != 0 ||
		committed.CloudSyncState != "synced" || committed.UpdatedAt.IsZero() {
		t.Fatalf("committed projection = %+v", committed)
	}
	uid, name, mode, agentType, state, cloudID := readThreadRow(t, db, createdThreadTestUUID)
	if uid != 7 || name != "Design deck" || mode != "ppt" || agentType != "general_agent" ||
		state != "synced" || cloudID != "42" {
		t.Fatalf("created row = uid:%d name:%q mode:%q type:%q state:%q cloud:%q",
			uid, name, mode, agentType, state, cloudID)
	}
	cursor, err := cursorStore.Get(cursorKey)
	if err != nil {
		t.Fatal(err)
	}
	if cursor != "before-create" {
		t.Fatalf("thread create advanced cursor to %q", cursor)
	}

	// Replaying the same canonical resource is one local row and returns the
	// actual local projection, including the preserved pause preference.
	if err := db.Exec(`UPDATE w_workagent_thread SET cloud_sync_state = 'paused' WHERE uuid = ?`, createdThreadTestUUID).Error; err != nil {
		t.Fatal(err)
	}
	replayed, err := CommitCreatedThread(ctx, db, lease, 7, validCreatedThreadTestItem())
	if err != nil {
		t.Fatalf("idempotent CommitCreatedThread: %v", err)
	}
	if replayed.CloudSyncState != "paused" {
		t.Fatalf("replay hid preserved local state: %+v", replayed)
	}
	var rows int64
	if err := db.Raw(`SELECT count(*) FROM w_workagent_thread WHERE uuid = ?`, createdThreadTestUUID).Row().Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("idempotent create left %d rows", rows)
	}
}

func TestCommitCreatedThread_ReplacementWinsBeforeSQLiteAndWritesNothing(t *testing.T) {
	db := openWriterTestDB(t)
	store, lease, ctx := createdThreadTestLease(t, 7)
	if err := store.Save(cloudproxy.TokenPair{
		AccessToken:      mintJWTWithUID(99),
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "replacement-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatalf("replace session: %v", err)
	}

	_, err := CommitCreatedThread(ctx, db, lease, 7, validCreatedThreadTestItem())
	if !errors.Is(err, cloudproxy.ErrSessionChanged) {
		t.Fatalf("error = %v, want ErrSessionChanged", err)
	}
	var rows int64
	if err := db.Raw(`SELECT count(*) FROM w_workagent_thread`).Row().Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("retired session wrote %d thread row(s)", rows)
	}
}

func TestCommitCreatedThread_SQLiteFailureRollsBack(t *testing.T) {
	db := openWriterTestDB(t)
	if err := db.Exec(`CREATE TRIGGER reject_created_thread
		BEFORE INSERT ON w_workagent_thread
		BEGIN
			SELECT RAISE(ABORT, 'forced created-thread failure');
		END`).Error; err != nil {
		t.Fatal(err)
	}
	_, lease, ctx := createdThreadTestLease(t, 7)

	if _, err := CommitCreatedThread(ctx, db, lease, 7, validCreatedThreadTestItem()); err == nil {
		t.Fatal("expected SQLite failure")
	}
	var rows int64
	if err := db.Raw(`SELECT count(*) FROM w_workagent_thread`).Row().Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("failed transaction left %d row(s)", rows)
	}
}

func TestCommitCreatedThread_RejectsCrossUIDConflictWithoutOverwrite(t *testing.T) {
	db := openWriterTestDB(t)
	if _, err := UpsertThreads(db, []cloudproxy.ThreadDeltaItem{validCreatedThreadTestItem()}, 99, nil); err != nil {
		t.Fatalf("seed other UID: %v", err)
	}
	_, lease, ctx := createdThreadTestLease(t, 7)
	item := validCreatedThreadTestItem()
	item.Name = "Wrong-account overwrite"

	_, err := CommitCreatedThread(ctx, db, lease, 7, item)
	if !errors.Is(err, ErrUIDConflict) {
		t.Fatalf("error = %v, want ErrUIDConflict", err)
	}
	uid, name, _, _, _, _ := readThreadRow(t, db, createdThreadTestUUID)
	if uid != 99 || name != "Design deck" {
		t.Fatalf("conflicting row changed to uid=%d name=%q", uid, name)
	}
}

func TestCommitCreatedThread_RejectsMalformedCanonicalItemBeforeTransaction(t *testing.T) {
	db := openWriterTestDB(t)
	_, lease, ctx := createdThreadTestLease(t, 7)
	tests := []struct {
		name   string
		mutate func(*cloudproxy.ThreadDeltaItem)
	}{
		{name: "delete action", mutate: func(item *cloudproxy.ThreadDeltaItem) { item.Action = "delete" }},
		{name: "non-v4 UUID", mutate: func(item *cloudproxy.ThreadDeltaItem) { item.UUID = "de305d54-75b4-11d3-adb2-eb6b9e546014" }},
		{name: "Microsoft variant UUID", mutate: func(item *cloudproxy.ThreadDeltaItem) { item.UUID = "de305d54-75b4-431b-c456-eb6b9e546014" }},
		{name: "noncanonical cloud id", mutate: func(item *cloudproxy.ThreadDeltaItem) { item.CloudThreadID = "042" }},
		{name: "negative message count", mutate: func(item *cloudproxy.ThreadDeltaItem) { item.MessageCount = -1 }},
		{name: "wrong agent type", mutate: func(item *cloudproxy.ThreadDeltaItem) { item.AgentType = "admin_agent" }},
		{name: "empty model", mutate: func(item *cloudproxy.ThreadDeltaItem) { item.Model = "" }},
		{name: "reversed timestamps", mutate: func(item *cloudproxy.ThreadDeltaItem) { item.UpdatedAt = "2026-08-06T08:00:00Z" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			item := validCreatedThreadTestItem()
			test.mutate(&item)
			_, err := CommitCreatedThread(ctx, db, lease, 7, item)
			if !errors.Is(err, ErrInvalidCreatedThread) {
				t.Fatalf("error = %v, want ErrInvalidCreatedThread", err)
			}
		})
	}
	if _, err := CommitCreatedThread(ctx, nil, lease, 7, validCreatedThreadTestItem()); !errors.Is(err, ErrInvalidCreatedThread) {
		t.Fatalf("nil DB error = %v", err)
	}
	var rows int64
	if err := db.Raw(`SELECT count(*) FROM w_workagent_thread`).Row().Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("invalid canonical items wrote %d row(s)", rows)
	}
}
