//go:build desktop

package desktop

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func openHistoryTestDB(t testing.TB) *gorm.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "history_test.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	exec := func(stmt string) {
		t.Helper()
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	exec(`CREATE TABLE w_workagent_thread (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL DEFAULT 0,
		uuid TEXT NOT NULL,
		name TEXT NOT NULL DEFAULT '',
		agent_mode TEXT NOT NULL DEFAULT 'ppt',
		agent_type TEXT NOT NULL DEFAULT 'general_agent',
		message_count INTEGER NOT NULL DEFAULT 0,
		cloud_sync_state TEXT NOT NULL DEFAULT 'synced',
		cloud_thread_id TEXT,
		created_at TEXT,
		updated_at TEXT
	)`)
	exec(`CREATE TABLE w_desktop_thread_pin (
		uid INTEGER NOT NULL,
		thread_uuid TEXT NOT NULL,
		pinned_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (uid, thread_uuid)
	)`)
	exec(`CREATE TABLE w_workagent_message (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL DEFAULT 0,
		uuid TEXT NOT NULL,
		thread_id INTEGER NOT NULL DEFAULT 0,
		user_text TEXT,
		ai_text TEXT,
		chat_mode TEXT NOT NULL DEFAULT '',
		agent_engine TEXT NOT NULL DEFAULT '',
		agent_model TEXT NOT NULL DEFAULT '',
		streaming_state TEXT NOT NULL DEFAULT 'complete',
		created_at TEXT,
		updated_at TEXT
	)`)
	return db
}

func seedThread(t *testing.T, db *gorm.DB, uid uint64, uuid, name, mode string, updatedSecsAgo int) int64 {
	t.Helper()
	t0 := time.Now().UTC().Add(-time.Duration(updatedSecsAgo) * time.Second).Format(time.RFC3339Nano)
	if err := db.Exec(
		`INSERT INTO w_workagent_thread (uid, uuid, name, agent_mode, message_count, updated_at, created_at)
		 VALUES (?, ?, ?, ?, 0, ?, ?)`,
		uid, uuid, name, mode, t0, t0,
	).Error; err != nil {
		t.Fatalf("seed thread: %v", err)
	}
	var id int64
	if err := db.Raw(`SELECT id FROM w_workagent_thread WHERE uuid = ?`, uuid).Row().Scan(&id); err != nil {
		t.Fatalf("seed thread: scan id: %v", err)
	}
	return id
}

func seedMessage(t *testing.T, db *gorm.DB, threadID int64, uuid, userText, aiText, mode, state string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	seedMessageAt(t, db, threadID, uuid, userText, aiText, mode, state, now)
}

func seedMessageAt(t *testing.T, db *gorm.DB, threadID int64, uuid, userText, aiText, mode, state, timestamp string) {
	t.Helper()
	var uid uint64
	if err := db.Raw(`SELECT uid FROM w_workagent_thread WHERE id = ?`, threadID).Row().Scan(&uid); err != nil {
		t.Fatalf("seed message: scan thread uid: %v", err)
	}
	if err := db.Exec(
		`INSERT INTO w_workagent_message (uid, uuid, thread_id, user_text, ai_text, chat_mode, streaming_state, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uid, uuid, threadID, userText, aiText, mode, state, timestamp, timestamp,
	).Error; err != nil {
		t.Fatalf("seed message: %v", err)
	}
}

func TestListLocalThreads_OrderAndLimit(t *testing.T) {
	db := openHistoryTestDB(t)
	seedThread(t, db, 0, "thr_a", "A", "ppt", 30)
	seedThread(t, db, 0, "thr_b", "B", "ppt", 10) // most recent
	seedThread(t, db, 0, "thr_c", "C", "ppt", 20)

	rows, err := ListLocalThreads(db, 0, 50, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	// Most-recent first.
	if rows[0].UUID != "thr_b" || rows[1].UUID != "thr_c" || rows[2].UUID != "thr_a" {
		t.Errorf("order wrong: %+v", []string{rows[0].UUID, rows[1].UUID, rows[2].UUID})
	}
	// Limit clamps.
	limited, _ := ListLocalThreads(db, 0, 2, true)
	if len(limited) != 2 {
		t.Errorf("limit=2 got %d rows", len(limited))
	}
}

func TestListLocalThreads_FiltersByUID(t *testing.T) {
	db := openHistoryTestDB(t)
	seedThread(t, db, 7, "thr_user7", "U7", "ppt", 5)
	seedThread(t, db, 99, "thr_user99", "U99", "ppt", 5)

	// uid=0 → return all
	all, _ := ListLocalThreads(db, 0, 50, true)
	if len(all) != 2 {
		t.Errorf("uid=0 should return all, got %d", len(all))
	}
	// uid=7 → only that user's
	mine, _ := ListLocalThreads(db, 7, 50, true)
	if len(mine) != 1 || mine[0].UUID != "thr_user7" {
		t.Errorf("uid=7 filter wrong: %+v", mine)
	}
}

func TestListLocalThreads_FiltersToGeneralAgent(t *testing.T) {
	db := openHistoryTestDB(t)
	seedThread(t, db, 7, "thr_general", "General", "ppt", 5)
	if err := db.Exec(
		`INSERT INTO w_workagent_thread (uid, uuid, name, agent_mode, agent_type, message_count, updated_at, created_at)
		 VALUES (7, 'thr_other', 'Other', 'ppt', 'other_agent', 0, '2026-05-20T12:00:00Z', '2026-05-20T12:00:00Z')`,
	).Error; err != nil {
		t.Fatal(err)
	}

	rows, err := ListLocalThreads(db, 7, 50, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].UUID != "thr_general" {
		t.Fatalf("thread list should only include general_agent rows, got %+v", rows)
	}
}

func TestListLocalThreads_QueryUsesListingIndex(t *testing.T) {
	db := openHistoryTestDB(t)
	exec := func(stmt string) {
		t.Helper()
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	exec(`CREATE INDEX idx_w_workagent_thread_listing ON w_workagent_thread (uid, agent_type, updated_at DESC, id DESC)`)
	seedThread(t, db, 7, "thr_index", "Index", "ppt", 0)

	var detail string
	if err := db.Raw(`
		EXPLAIN QUERY PLAN
		SELECT uuid, name, agent_mode, message_count, updated_at,
		       COALESCE(cloud_sync_state, 'synced')
		  FROM w_workagent_thread
		 WHERE uid = ?
		   AND agent_type = 'general_agent'
		   AND COALESCE(cloud_sync_state, 'synced') <> 'paused'
		 ORDER BY updated_at DESC, id DESC
		 LIMIT ?`, uint64(7), 50,
	).Row().Scan(new(int), new(int), new(int), &detail); err != nil {
		t.Fatalf("explain query plan: %v", err)
	}
	if !strings.Contains(detail, "idx_w_workagent_thread_listing") {
		t.Fatalf("query plan should use listing index, got: %s", detail)
	}
	if strings.Contains(detail, "USE TEMP B-TREE") {
		t.Fatalf("query plan should not sort with temp b-tree, got: %s", detail)
	}
}

func TestListLocalThreads_DefaultLimit(t *testing.T) {
	db := openHistoryTestDB(t)
	// Pass limit=0 → defaults to 50; just confirm no error.
	if _, err := ListLocalThreads(db, 0, 0, true); err != nil {
		t.Errorf("default limit should be accepted: %v", err)
	}
}

func TestListLocalThreads_NilDB(t *testing.T) {
	if _, err := ListLocalThreads(nil, 0, 10, true); err == nil {
		t.Error("nil db should error")
	}
}

func TestListLocalThreads_IncludePausedFilter(t *testing.T) {
	db := openHistoryTestDB(t)
	seedThread(t, db, 0, "thr_active", "Active", "ppt", 5)
	seedThread(t, db, 0, "thr_paused", "Paused", "ppt", 1)
	if err := db.Exec(`UPDATE w_workagent_thread SET cloud_sync_state = 'paused' WHERE uuid = 'thr_paused'`).Error; err != nil {
		t.Fatal(err)
	}

	all, err := ListLocalThreads(db, 0, 50, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("includePaused=true got %d rows, want 2", len(all))
	}

	activeOnly, err := ListLocalThreads(db, 0, 50, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(activeOnly) != 1 || activeOnly[0].UUID != "thr_active" {
		t.Fatalf("includePaused=false rows: %+v", activeOnly)
	}
}

func TestListLocalMessages_HappyPath(t *testing.T) {
	db := openHistoryTestDB(t)
	threadID := seedThread(t, db, 0, "thr_1", "Test", "ppt", 0)
	seedMessage(t, db, threadID, "msg_1", "say hi", "hello", "ppt", "complete")
	seedMessage(t, db, threadID, "msg_2", "again", "world", "ppt", "partial")

	rows, err := ListLocalMessages(db, 0, "thr_1", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	// Oldest first (id ascending).
	if rows[0].UUID != "msg_1" || rows[1].UUID != "msg_2" {
		t.Errorf("order wrong: %v", []string{rows[0].UUID, rows[1].UUID})
	}
	if rows[0].UserText != "say hi" || rows[0].AIText != "hello" {
		t.Errorf("row[0] content: %+v", rows[0])
	}
	if rows[1].StreamingState != "partial" {
		t.Errorf("row[1] state: got %q, want partial", rows[1].StreamingState)
	}
}

func TestListLocalMessages_FiltersParentThreadByUID(t *testing.T) {
	db := openHistoryTestDB(t)
	mine := seedThread(t, db, 7, "thr_mine", "Mine", "ppt", 0)
	theirs := seedThread(t, db, 99, "thr_theirs", "Theirs", "ppt", 0)
	seedMessage(t, db, mine, "msg_mine", "mine", "ok", "ppt", "complete")
	seedMessage(t, db, theirs, "msg_theirs", "theirs", "hidden", "ppt", "complete")

	rows, err := ListLocalMessages(db, 7, "thr_mine", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].UUID != "msg_mine" {
		t.Fatalf("uid=7 own thread rows: %+v", rows)
	}

	rows, err = ListLocalMessages(db, 7, "thr_theirs", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("uid=7 should not see uid=99 messages: %+v", rows)
	}

	all, err := ListLocalMessages(db, 0, "thr_theirs", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].UUID != "msg_theirs" {
		t.Fatalf("uid=0 diagnostic mode should still see all rows: %+v", all)
	}
}

func TestListLocalMessages_FiltersMessageRowsByUID(t *testing.T) {
	db := openHistoryTestDB(t)
	threadID := seedThread(t, db, 7, "thr_mine", "Mine", "ppt", 0)
	seedMessage(t, db, threadID, "msg_mine", "mine", "ok", "ppt", "complete")
	if err := db.Exec(
		`INSERT INTO w_workagent_message
			(uid, uuid, thread_id, user_text, ai_text, chat_mode, streaming_state, created_at, updated_at)
		 VALUES
			(99, 'msg_wrong_uid', ?, 'other user', 'hidden', 'ppt', 'complete', '2026-05-21T00:00:00Z', '2026-05-21T00:00:00Z')`,
		threadID,
	).Error; err != nil {
		t.Fatalf("seed wrong uid message: %v", err)
	}

	rows, err := ListLocalMessages(db, 7, "thr_mine", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].UUID != "msg_mine" {
		t.Fatalf("uid=7 should only see uid=7 message rows: %+v", rows)
	}

	all, err := ListLocalMessages(db, 0, "thr_mine", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("uid=0 diagnostic mode should still see all message rows, got %+v", all)
	}
}

func TestListLocalMessages_OrdersByMessageTimestampNotInsertID(t *testing.T) {
	db := openHistoryTestDB(t)
	threadID := seedThread(t, db, 0, "thr_1", "Test", "ppt", 0)
	latest := time.Date(2026, 5, 20, 12, 5, 0, 0, time.UTC).Format(time.RFC3339Nano)
	earliest := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)

	// Cloud sync can upsert an older message after a newer local/cache
	// row already exists. Rendering by SQLite insertion id would invert
	// the conversation; created_at is the actual chat chronology.
	seedMessageAt(t, db, threadID, "msg-latest", "newer", "second", "ppt", "complete", latest)
	seedMessageAt(t, db, threadID, "msg-earliest", "older", "first", "ppt", "complete", earliest)

	rows, err := ListLocalMessages(db, 0, "thr_1", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0].UUID != "msg-earliest" || rows[1].UUID != "msg-latest" {
		t.Fatalf("order wrong: got %v", []string{rows[0].UUID, rows[1].UUID})
	}
}

func TestListLocalMessages_QueryUsesChronologicalIndex(t *testing.T) {
	db := openHistoryTestDB(t)
	exec := func(stmt string) {
		t.Helper()
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	exec(`CREATE INDEX idx_w_workagent_message_thread_created ON w_workagent_message (thread_id, created_at, id)`)
	threadID := seedThread(t, db, 0, "thr_index", "Test", "ppt", 0)
	seedMessage(t, db, threadID, "msg-1", "hi", "ok", "ppt", "complete")

	var detail string
	if err := db.Raw(`
		EXPLAIN QUERY PLAN
		SELECT uuid,
		       COALESCE(user_text, ''),
		       COALESCE(ai_text, ''),
		       chat_mode,
		       streaming_state,
		       created_at,
		       updated_at
		  FROM w_workagent_message
		 WHERE thread_id = ?
		 ORDER BY created_at ASC, id ASC
		 LIMIT ?`, threadID, 200,
	).Row().Scan(new(int), new(int), new(int), &detail); err != nil {
		t.Fatalf("explain query plan: %v", err)
	}
	if !strings.Contains(detail, "idx_w_workagent_message_thread_created") {
		t.Fatalf("query plan should use chronological index, got: %s", detail)
	}
	if strings.Contains(detail, "USE TEMP B-TREE") {
		t.Fatalf("query plan should not sort with temp b-tree, got: %s", detail)
	}
}

func TestListLocalMessages_MissingThreadReturnsEmpty(t *testing.T) {
	db := openHistoryTestDB(t)
	rows, err := ListLocalMessages(db, 0, "thr_does_not_exist", 50)
	if err != nil {
		t.Errorf("missing thread should not error, got: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("missing thread should return empty, got %d rows", len(rows))
	}
}

func TestListLocalMessages_RejectsEmptyUUID(t *testing.T) {
	db := openHistoryTestDB(t)
	if _, err := ListLocalMessages(db, 0, "", 50); err == nil {
		t.Error("empty uuid should error")
	}
}

func TestListLocalMessages_NilDB(t *testing.T) {
	if _, err := ListLocalMessages(nil, 0, "thr_1", 50); err == nil {
		t.Error("nil db should error")
	}
}

func TestParseSQLiteTime(t *testing.T) {
	cases := []string{
		time.Now().UTC().Format(time.RFC3339Nano),
		time.Now().UTC().Format(time.RFC3339),
		"2026-05-17 22:15:00",
	}
	for _, in := range cases {
		got := parseSQLiteTime(in)
		if got.IsZero() {
			t.Errorf("parseSQLiteTime(%q) returned zero time", in)
		}
	}
	if !parseSQLiteTime("").IsZero() {
		t.Error("empty string should yield zero time")
	}
	if !parseSQLiteTime("not a time").IsZero() {
		t.Error("garbage should yield zero time")
	}
}

func BenchmarkListLocalThreads_5000Rows(b *testing.B) {
	db := openHistoryBenchDB(b)
	now := time.Now().UTC()
	for i := 0; i < 5000; i++ {
		ts := now.Add(-time.Duration(i) * time.Second).Format(time.RFC3339Nano)
		if err := db.Exec(
			`INSERT INTO w_workagent_thread (uid, uuid, name, agent_mode, message_count, cloud_sync_state, updated_at, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			7,
			fmt.Sprintf("thr_%04d", i),
			fmt.Sprintf("Thread %04d", i),
			"ppt",
			i%200,
			"synced",
			ts,
			ts,
		).Error; err != nil {
			b.Fatalf("seed thread %d: %v", i, err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, err := ListLocalThreads(db, 7, 1000, false)
		if err != nil {
			b.Fatal(err)
		}
		if len(rows) != 1000 {
			b.Fatalf("got %d rows, want 1000", len(rows))
		}
	}
}

func BenchmarkListLocalMessages_10000Rows(b *testing.B) {
	db := openHistoryBenchDB(b)
	threadID := seedBenchThread(b, db, "thr_bench")
	now := time.Now().UTC()
	for i := 0; i < 10000; i++ {
		ts := now.Add(time.Duration(i) * time.Millisecond).Format(time.RFC3339Nano)
		if err := db.Exec(
			`INSERT INTO w_workagent_message (uuid, thread_id, user_text, ai_text, chat_mode, streaming_state, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			fmt.Sprintf("msg_%05d", i),
			threadID,
			fmt.Sprintf("user prompt %05d", i),
			fmt.Sprintf("assistant response %05d", i),
			"ppt",
			"complete",
			ts,
			ts,
		).Error; err != nil {
			b.Fatalf("seed message %d: %v", i, err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, err := ListLocalMessages(db, 7, "thr_bench", 1000)
		if err != nil {
			b.Fatal(err)
		}
		if len(rows) != 1000 {
			b.Fatalf("got %d rows, want 1000", len(rows))
		}
	}
}

func openHistoryBenchDB(tb testing.TB) *gorm.DB {
	tb.Helper()
	db := openHistoryTestDB(tb)
	exec := func(stmt string) {
		tb.Helper()
		if err := db.Exec(stmt).Error; err != nil {
			tb.Fatalf("exec %q: %v", stmt, err)
		}
	}
	// Match production desktop migration indexes so the benchmark
	// reflects the real local cache schema instead of an idealized one.
	exec(`CREATE INDEX idx_w_workagent_thread_listing ON w_workagent_thread (uid, agent_type, updated_at DESC, id DESC)`)
	exec(`CREATE INDEX idx_w_workagent_message_thread_id_desc ON w_workagent_message (thread_id, id DESC)`)
	exec(`CREATE INDEX idx_w_workagent_message_thread_created ON w_workagent_message (thread_id, created_at, id)`)
	return db
}

func seedBenchThread(tb testing.TB, db *gorm.DB, uuid string) int64 {
	tb.Helper()
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	if err := db.Exec(
		`INSERT INTO w_workagent_thread (uid, uuid, name, agent_mode, message_count, updated_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		7,
		uuid,
		"Benchmark thread",
		"ppt",
		0,
		ts,
		ts,
	).Error; err != nil {
		tb.Fatalf("seed bench thread: %v", err)
	}
	var id int64
	if err := db.Raw(`SELECT id FROM w_workagent_thread WHERE uuid = ?`, uuid).Row().Scan(&id); err != nil {
		tb.Fatalf("seed bench thread scan id: %v", err)
	}
	return id
}

// A thread that only ever existed on this machine has no cloud-written
// message_count, so the sidebar said "0 messages" beside a conversation the
// user had just had. Counting the rows is what makes the local-first list
// describe itself honestly.
func TestListLocalThreads_CountsLocalMessagesWhenCloudCountIsBehind(t *testing.T) {
	db := openHistoryTestDB(t)
	id := seedThread(t, db, 7, "thr_local", "Local only", "ppt", 5)
	seedMessage(t, db, id, "m1", "hi", "hello", "ppt", "complete")
	seedMessage(t, db, id, "m2", "again", "sure", "ppt", "complete")

	rows, err := ListLocalThreads(db, 7, 50, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].MessageCount != 2 {
		t.Errorf("message_count = %d, want 2 (the rows that exist)", rows[0].MessageCount)
	}
}

// The stored count is not simply replaced: the cloud can know about messages
// this machine has never pulled, and reporting only what is cached would make
// a synced thread look emptier than it is.
func TestListLocalThreads_KeepsCloudCountWhenItIsAhead(t *testing.T) {
	db := openHistoryTestDB(t)
	id := seedThread(t, db, 7, "thr_synced", "Synced", "ppt", 5)
	if err := db.Exec(`UPDATE w_workagent_thread SET message_count = 40 WHERE id = ?`, id).Error; err != nil {
		t.Fatal(err)
	}
	seedMessage(t, db, id, "m1", "hi", "hello", "ppt", "complete")

	rows, err := ListLocalThreads(db, 7, 50, true)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].MessageCount != 40 {
		t.Errorf("message_count = %d, want the cloud's 40", rows[0].MessageCount)
	}
}

// Counting must respect the same uid filter as everything else, or one local
// identity's threads would be described using another's messages.
func TestListLocalThreads_CountIsScopedToTheOwner(t *testing.T) {
	db := openHistoryTestDB(t)
	id := seedThread(t, db, 7, "thr_mine", "Mine", "ppt", 5)
	seedMessage(t, db, id, "m1", "hi", "hello", "ppt", "complete")
	// A message row that points at the thread but belongs to another uid.
	if err := db.Exec(
		`INSERT INTO w_workagent_message (uid, uuid, thread_id, user_text, ai_text, chat_mode, streaming_state)
		 VALUES (?, ?, ?, '', '', 'ppt', 'complete')`, 8, "m-other", id,
	).Error; err != nil {
		t.Fatal(err)
	}

	rows, err := ListLocalThreads(db, 7, 50, true)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].MessageCount != 1 {
		t.Errorf("message_count = %d, want 1; the other uid's row must not be counted", rows[0].MessageCount)
	}
}
