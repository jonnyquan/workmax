//go:build desktop

package sync

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	cloudproxy "server/desktop/cloud_proxy"
)

// openMessageWriterTestDB builds SQLite with the schema the
// message writer reads/writes. Includes BOTH w_workagent_thread
// (for the uuid→id lookup) and w_workagent_message (the target).
func openMessageWriterTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "msg_writer.db")), &gorm.Config{
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
		updated_at TEXT, created_at TEXT
	)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE w_workagent_message (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL DEFAULT 0,
		uuid TEXT NOT NULL UNIQUE,
		thread_id INTEGER NOT NULL DEFAULT 0,
		user_text TEXT, ai_text TEXT,
		chat_mode TEXT NOT NULL DEFAULT '',
		content_type TEXT, structured_content TEXT, actions TEXT, metadata TEXT,
		use_images TEXT, use_files TEXT,
		user_rating INTEGER NOT NULL DEFAULT 0, user_feedback TEXT,
		streaming_state TEXT NOT NULL DEFAULT 'complete',
		created_at TEXT, updated_at TEXT
	)`).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func seedWriterThread(t *testing.T, db *gorm.DB, uid int, uuid string) {
	t.Helper()
	if err := db.Exec(
		`INSERT INTO w_workagent_thread (uid, uuid, name) VALUES (?, ?, 'T')`,
		uid, uuid,
	).Error; err != nil {
		t.Fatal(err)
	}
}

func readMessageRow(t *testing.T, db *gorm.DB, uuid string) (uid int, threadID int64, userText, aiText, streamState string) {
	t.Helper()
	row := db.Raw(`SELECT uid, thread_id, COALESCE(user_text,''), COALESCE(ai_text,''), streaming_state
	                 FROM w_workagent_message WHERE uuid = ?`, uuid).Row()
	if err := row.Scan(&uid, &threadID, &userText, &aiText, &streamState); err != nil {
		t.Fatalf("read row: %v", err)
	}
	return
}

func TestUpsertMessages_InsertsNewRow(t *testing.T) {
	db := openMessageWriterTestDB(t)
	seedWriterThread(t, db, 7, "thr-a")

	items := []cloudproxy.MessageDeltaItem{
		{
			Action: "upsert", CloudMessageID: "1", UUID: "m-1",
			ThreadUUID: "thr-a", UserText: "hi", AIText: "ok",
			ChatMode: "ppt", UserRating: 1, UserFeedback: "good",
			UpdatedAt: "2026-05-17T22:00:00Z",
			CreatedAt: "2026-05-17T22:00:00Z",
		},
	}
	n, err := UpsertMessages(db, items, 7)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if n != 1 {
		t.Errorf("count: %d", n)
	}
	uid, threadID, userText, aiText, state := readMessageRow(t, db, "m-1")
	if uid != 7 || threadID != 1 {
		t.Errorf("uid=%d threadID=%d, want 7/1", uid, threadID)
	}
	if userText != "hi" || aiText != "ok" {
		t.Errorf("text: %q / %q", userText, aiText)
	}
	if state != "complete" {
		t.Errorf("state: %q, want complete (cloud is authoritative)", state)
	}
}

func TestUpsertMessages_UpdatesExisting(t *testing.T) {
	db := openMessageWriterTestDB(t)
	seedWriterThread(t, db, 7, "thr-a")
	first := []cloudproxy.MessageDeltaItem{
		{Action: "upsert", CloudMessageID: "1", UUID: "m-1", ThreadUUID: "thr-a",
			UserText: "v1", AIText: "v1a", ChatMode: "ppt",
			UpdatedAt: "2026-05-17T22:00:00Z"},
	}
	if _, err := UpsertMessages(db, first, 7); err != nil {
		t.Fatal(err)
	}
	// Re-upsert with updated text.
	second := []cloudproxy.MessageDeltaItem{
		{Action: "upsert", CloudMessageID: "1", UUID: "m-1", ThreadUUID: "thr-a",
			UserText: "v2", AIText: "v2a", ChatMode: "ppt",
			UpdatedAt: "2026-05-17T22:30:00Z"},
	}
	if _, err := UpsertMessages(db, second, 7); err != nil {
		t.Fatal(err)
	}
	_, _, userText, aiText, _ := readMessageRow(t, db, "m-1")
	if userText != "v2" || aiText != "v2a" {
		t.Errorf("re-upsert: %q / %q", userText, aiText)
	}
	var count int64
	db.Raw(`SELECT count(*) FROM w_workagent_message`).Row().Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 row after re-upsert, got %d", count)
	}
}

func TestUpsertMessages_FlipsPartialToComplete(t *testing.T) {
	// A local row in 'partial' state (interrupted local cache_writer)
	// gets re-upserted by cloud sync → should flip to 'complete'.
	db := openMessageWriterTestDB(t)
	seedWriterThread(t, db, 7, "thr-a")
	if err := db.Exec(
		`INSERT INTO w_workagent_message
		   (uid, uuid, thread_id, user_text, ai_text, chat_mode,
		    streaming_state, updated_at, created_at)
		 VALUES (7, 'm-1', 1, 'partial-text', 'partial-ai', 'ppt',
		         'partial', '2026-05-17T22:00:00Z', '2026-05-17T22:00:00Z')`,
	).Error; err != nil {
		t.Fatal(err)
	}

	items := []cloudproxy.MessageDeltaItem{
		{Action: "upsert", UUID: "m-1", ThreadUUID: "thr-a",
			UserText: "final-text", AIText: "final-ai", ChatMode: "ppt",
			UpdatedAt: "2026-05-17T22:30:00Z"},
	}
	if _, err := UpsertMessages(db, items, 7); err != nil {
		t.Fatal(err)
	}
	_, _, userText, aiText, state := readMessageRow(t, db, "m-1")
	if state != "complete" {
		t.Errorf("state: got %q, want complete (cloud authoritative)", state)
	}
	if userText != "final-text" || aiText != "final-ai" {
		t.Errorf("body not replaced: %q / %q", userText, aiText)
	}
}

func TestUpsertMessages_MissingThreadIsRetryableError(t *testing.T) {
	// Race: messages sync runs before threads sync has landed
	// the parent thread. The writer must return an error so the
	// caller does not advance the cursor past an unwritten message.
	db := openMessageWriterTestDB(t)
	// Don't seed any thread — m-1's parent doesn't exist locally.

	items := []cloudproxy.MessageDeltaItem{
		{Action: "upsert", UUID: "m-1", ThreadUUID: "thr-missing",
			UserText: "hi", AIText: "ok", ChatMode: "ppt",
			UpdatedAt: "2026-05-17T22:00:00Z"},
	}
	n, err := UpsertMessages(db, items, 7)
	if err == nil {
		t.Fatal("missing thread should error to prevent cursor advance")
	}
	if !errors.Is(err, ErrMissingParentThread) {
		t.Fatalf("got %v, want ErrMissingParentThread", err)
	}
	if n != 0 {
		t.Errorf("missing thread should yield 0 successful upserts, got %d", n)
	}
	var count int64
	db.Raw(`SELECT count(*) FROM w_workagent_message`).Row().Scan(&count)
	if count != 0 {
		t.Errorf("no messages should be written: got %d rows", count)
	}
}

func TestUpsertMessages_ParentThreadMustMatchUID(t *testing.T) {
	db := openMessageWriterTestDB(t)
	seedWriterThread(t, db, 99, "thr-other-user")

	n, err := UpsertMessages(db, []cloudproxy.MessageDeltaItem{
		{
			Action: "upsert", UUID: "m-cross-user", ThreadUUID: "thr-other-user",
			UserText: "should not attach", ChatMode: "ppt",
			UpdatedAt: "2026-05-17T22:00:00Z",
		},
	}, 7)
	if !errors.Is(err, ErrMissingParentThread) {
		t.Fatalf("got %v, want ErrMissingParentThread", err)
	}
	if n != 0 {
		t.Fatalf("count: got %d, want 0", n)
	}
	var count int64
	db.Raw(`SELECT count(*) FROM w_workagent_message WHERE uuid = ?`, "m-cross-user").Row().Scan(&count)
	if count != 0 {
		t.Fatalf("cross-user message should not be written, got %d row(s)", count)
	}
}

func TestUpsertMessages_MissingThreadAfterPartialBatchReturnsPartialCount(t *testing.T) {
	db := openMessageWriterTestDB(t)
	seedWriterThread(t, db, 7, "thr-a")

	n, err := UpsertMessages(db, []cloudproxy.MessageDeltaItem{
		{Action: "upsert", UUID: "m-1", ThreadUUID: "thr-a",
			UserText: "first", ChatMode: "ppt", UpdatedAt: "2026-05-17T22:00:00Z"},
		{Action: "upsert", UUID: "m-2", ThreadUUID: "thr-missing",
			UserText: "second", ChatMode: "ppt", UpdatedAt: "2026-05-17T22:01:00Z"},
	}, 7)
	if !errors.Is(err, ErrMissingParentThread) {
		t.Fatalf("got %v, want ErrMissingParentThread", err)
	}
	if n != 1 {
		t.Fatalf("partial count: got %d, want 1", n)
	}
	var count int64
	db.Raw(`SELECT count(*) FROM w_workagent_message`).Row().Scan(&count)
	if count != 1 {
		t.Fatalf("only the first row should be written, got %d", count)
	}
}

func TestUpsertMessages_CachesThreadLookupAcrossBatch(t *testing.T) {
	// Sanity: a batch with 5 messages all from the same thread
	// shouldn't issue 5 redundant SELECTs against w_workagent_thread.
	// We can't easily count queries with the test DB, but we can
	// at least confirm the batch works correctly + 5 rows land.
	db := openMessageWriterTestDB(t)
	seedWriterThread(t, db, 7, "thr-a")
	items := make([]cloudproxy.MessageDeltaItem, 5)
	for i := range items {
		items[i] = cloudproxy.MessageDeltaItem{
			Action: "upsert", UUID: "m-" + string('a'+rune(i)), ThreadUUID: "thr-a",
			UserText: "u", AIText: "a", ChatMode: "ppt",
			UpdatedAt: "2026-05-17T22:00:00Z",
		}
	}
	n, err := UpsertMessages(db, items, 7)
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Errorf("count: %d, want 5", n)
	}
}

func TestUpsertMessages_UnknownActionAborts(t *testing.T) {
	// "delete" is now valid (P1.A.5a); use a different unknown
	// action to pin the still-rejected behavior.
	db := openMessageWriterTestDB(t)
	seedWriterThread(t, db, 7, "thr-a")
	items := []cloudproxy.MessageDeltaItem{
		{Action: "upsert", UUID: "m-1", ThreadUUID: "thr-a", UpdatedAt: "2026-05-17T22:00:00Z"},
		{Action: "weird_future_action", UUID: "m-2", ThreadUUID: "thr-a"},
		{Action: "upsert", UUID: "m-3", ThreadUUID: "thr-a", UpdatedAt: "2026-05-17T22:02:00Z"},
	}
	n, err := UpsertMessages(db, items, 7)
	if err == nil {
		t.Fatal("expected ErrUnknownAction")
	}
	if !errors.Is(err, ErrUnknownAction) {
		t.Errorf("got %v, want ErrUnknownAction", err)
	}
	if n != 1 {
		t.Errorf("partial count: %d, want 1 (one row before abort)", n)
	}
}

// === Delete-action tests (P1.A.5a) ===

func TestUpsertMessages_DeleteRemovesRow(t *testing.T) {
	db := openMessageWriterTestDB(t)
	seedWriterThread(t, db, 7, "thr-a")
	// Seed: insert via upsert.
	if _, err := UpsertMessages(db, []cloudproxy.MessageDeltaItem{
		{Action: "upsert", UUID: "m-del", ThreadUUID: "thr-a",
			UserText: "doomed", ChatMode: "ppt", UpdatedAt: "2026-05-17T22:00:00Z"},
	}, 7); err != nil {
		t.Fatal(err)
	}
	// Delete via action="delete" (no thread_uuid needed — uuid alone identifies).
	n, err := UpsertMessages(db, []cloudproxy.MessageDeltaItem{
		{Action: "delete", UUID: "m-del"},
	}, 7)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("count: got %d, want 1", n)
	}
	var count int64
	db.Raw(`SELECT count(*) FROM w_workagent_message WHERE uuid = ?`, "m-del").Row().Scan(&count)
	if count != 0 {
		t.Errorf("row should be gone, got %d", count)
	}
}

func TestUpsertMessages_DeleteMissingUUIDIsNoOp(t *testing.T) {
	db := openMessageWriterTestDB(t)
	n, err := UpsertMessages(db, []cloudproxy.MessageDeltaItem{
		{Action: "delete", UUID: "m-never-existed"},
	}, 7)
	if err != nil {
		t.Errorf("delete of nonexistent uuid should not error: %v", err)
	}
	if n != 0 {
		t.Errorf("count: %d, want 0", n)
	}
}

func TestUpsertMessages_DeleteFiltersByUID(t *testing.T) {
	// Defense-in-depth: deleting a uuid that belongs to a
	// different uid should be a no-op.
	db := openMessageWriterTestDB(t)
	seedWriterThread(t, db, 99, "thr-other")
	if _, err := UpsertMessages(db, []cloudproxy.MessageDeltaItem{
		{Action: "upsert", UUID: "m-other", ThreadUUID: "thr-other",
			UserText: "secret", ChatMode: "ppt", UpdatedAt: "2026-05-17T22:00:00Z"},
	}, 99); err != nil {
		t.Fatal(err)
	}
	n, err := UpsertMessages(db, []cloudproxy.MessageDeltaItem{
		{Action: "delete", UUID: "m-other"},
	}, 7)
	if err != nil {
		t.Errorf("err: %v", err)
	}
	if n != 0 {
		t.Errorf("uid mismatch should not delete: got %d", n)
	}
	var count int64
	db.Raw(`SELECT count(*) FROM w_workagent_message WHERE uuid = ?`, "m-other").Row().Scan(&count)
	if count != 1 {
		t.Error("uid=99's message should still exist")
	}
}

func TestUpsertMessages_UpsertRejectsDifferentUIDConflict(t *testing.T) {
	db := openMessageWriterTestDB(t)
	seedWriterThread(t, db, 99, "thr-other")
	if _, err := UpsertMessages(db, []cloudproxy.MessageDeltaItem{
		{Action: "upsert", UUID: "m-shared", ThreadUUID: "thr-other",
			UserText: "other secret", AIText: "other answer", ChatMode: "ppt",
			UpdatedAt: "2026-05-17T22:00:00Z"},
	}, 99); err != nil {
		t.Fatal(err)
	}
	seedWriterThread(t, db, 7, "thr-active")

	n, err := UpsertMessages(db, []cloudproxy.MessageDeltaItem{
		{Action: "upsert", UUID: "m-shared", ThreadUUID: "thr-active",
			UserText: "active text", AIText: "active answer", ChatMode: "ppt",
			UpdatedAt: "2026-05-17T23:00:00Z"},
	}, 7)
	if !errors.Is(err, ErrUIDConflict) {
		t.Fatalf("got %v, want ErrUIDConflict", err)
	}
	if n != 0 {
		t.Fatalf("count: got %d, want 0", n)
	}

	uid, _, userText, aiText, _ := readMessageRow(t, db, "m-shared")
	if uid != 99 || userText != "other secret" || aiText != "other answer" {
		t.Fatalf("conflicting row was modified: uid=%d userText=%q aiText=%q", uid, userText, aiText)
	}
	var activeCount int64
	db.Raw(`SELECT count(*) FROM w_workagent_message WHERE uuid = ? AND uid = 7`, "m-shared").
		Row().Scan(&activeCount)
	if activeCount != 0 {
		t.Fatalf("active uid should not get a silently skipped duplicate row, got %d", activeCount)
	}
}

func TestUpsertMessages_MissingUUIDAborts(t *testing.T) {
	db := openMessageWriterTestDB(t)
	seedWriterThread(t, db, 7, "thr-a")
	items := []cloudproxy.MessageDeltaItem{
		{Action: "upsert", UUID: "", ThreadUUID: "thr-a"},
	}
	if _, err := UpsertMessages(db, items, 7); err == nil {
		t.Error("empty uuid should error")
	}
}

func TestUpsertMessages_MissingThreadUUIDAborts(t *testing.T) {
	db := openMessageWriterTestDB(t)
	items := []cloudproxy.MessageDeltaItem{
		{Action: "upsert", UUID: "m-1", ThreadUUID: ""},
	}
	if _, err := UpsertMessages(db, items, 7); err == nil {
		t.Error("empty thread_uuid should error")
	}
}

func TestUpsertMessages_RejectsBadParams(t *testing.T) {
	if _, err := UpsertMessages(nil, nil, 7); err == nil {
		t.Error("nil db should error")
	}
	db := openMessageWriterTestDB(t)
	if _, err := UpsertMessages(db, nil, 0); err == nil {
		t.Error("uid=0 should error")
	}
}

func TestUpsertMessages_EmptyBatch(t *testing.T) {
	db := openMessageWriterTestDB(t)
	n, err := UpsertMessages(db, nil, 7)
	if err != nil {
		t.Error("empty batch should not error")
	}
	if n != 0 {
		t.Errorf("empty batch count: %d", n)
	}
}
