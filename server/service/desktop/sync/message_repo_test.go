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

// openMessageRepoTestDB builds SQLite with the columns the message
// repo + IDOR lookup query. Includes both w_workagent_thread (for
// the IDOR check) and w_workagent_message (the actual data source).
func openMessageRepoTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "msg.db")), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Exec(`CREATE TABLE w_workagent_thread (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL DEFAULT 0,
		uuid TEXT NOT NULL,
		name TEXT NOT NULL DEFAULT '',
		updated_at TEXT,
		created_at TEXT
	)`).Error; err != nil {
		t.Fatalf("thread table: %v", err)
	}
	if err := db.Exec(`CREATE TABLE w_workagent_message (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL DEFAULT 0,
		uuid TEXT NOT NULL,
		thread_id INTEGER NOT NULL DEFAULT 0,
		user_text TEXT,
		ai_text TEXT,
		chat_mode TEXT NOT NULL DEFAULT '',
		content_type TEXT,
		structured_content TEXT,
		actions TEXT,
		metadata TEXT,
		use_images TEXT,
		use_files TEXT,
		user_rating INTEGER NOT NULL DEFAULT 0,
		user_feedback TEXT,
		updated_at TEXT,
		created_at TEXT
	)`).Error; err != nil {
		t.Fatalf("message table: %v", err)
	}
	return db
}

func seedThread(t *testing.T, db *gorm.DB, uid int, uuid string) uint64 {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := db.Exec(
		`INSERT INTO w_workagent_thread (uid, uuid, name, updated_at, created_at)
		 VALUES (?, ?, 'T', ?, ?)`, uid, uuid, now, now,
	).Error; err != nil {
		t.Fatal(err)
	}
	var id uint64
	if err := db.Raw(`SELECT id FROM w_workagent_thread WHERE uuid = ?`, uuid).Row().Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func seedMessage(t *testing.T, db *gorm.DB, uid int, threadID uint64, uuid string, userText, aiText string, updatedAt time.Time) {
	t.Helper()
	ts := updatedAt.UTC().Format(time.RFC3339Nano)
	if err := db.Exec(
		`INSERT INTO w_workagent_message
		   (uid, uuid, thread_id, user_text, ai_text, chat_mode,
		    user_rating, updated_at, created_at)
		 VALUES (?, ?, ?, ?, ?, 'ppt', 0, ?, ?)`,
		uid, uuid, threadID, userText, aiText, ts, ts,
	).Error; err != nil {
		t.Fatal(err)
	}
}

func TestListMessagesDelta_HappyPath(t *testing.T) {
	db := openMessageRepoTestDB(t)
	threadID := seedThread(t, db, 7, "thr-a")
	base := time.Date(2026, 5, 17, 22, 0, 0, 0, time.UTC)
	seedMessage(t, db, 7, threadID, "m-1", "hi", "hello", base)
	seedMessage(t, db, 7, threadID, "m-2", "again", "world", base.Add(time.Minute))

	rows, next, hasMore, err := ListMessagesDelta(context.Background(), db, 7, threadID, Cursor{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || hasMore {
		t.Fatalf("rows=%d hasMore=%v want 2/false", len(rows), hasMore)
	}
	if rows[0].UUID != "m-1" || rows[0].UserText != "hi" || rows[0].AIText != "hello" {
		t.Errorf("row[0]: %+v", rows[0])
	}
	if rows[0].ThreadUUID != "thr-a" {
		t.Errorf("ThreadUUID should be populated from joined lookup: got %q", rows[0].ThreadUUID)
	}
	if rows[0].ChatMode != "ppt" {
		t.Errorf("ChatMode: %q", rows[0].ChatMode)
	}
	if !next.UpdatedAt.Equal(base.Add(time.Minute)) {
		t.Errorf("cursor advanced to last row: got %v", next.UpdatedAt)
	}
}

func TestListMessagesDelta_IDOR_RejectsOtherUser(t *testing.T) {
	db := openMessageRepoTestDB(t)
	mineID := seedThread(t, db, 7, "thr-mine")
	theirsID := seedThread(t, db, 42, "thr-theirs")
	seedMessage(t, db, 7, mineID, "m-mine", "hi", "ok", time.Now().UTC())
	seedMessage(t, db, 42, theirsID, "m-theirs", "secret", "alsosecret", time.Now().UTC())

	// uid=7 tries to read uid=42's thread → must error, even though
	// the integer thread_id is valid.
	_, _, _, err := ListMessagesDelta(context.Background(), db, 7, theirsID, Cursor{}, 100)
	if err == nil {
		t.Fatal("IDOR: uid=7 should NOT be able to list uid=42's messages")
	}
	if !errors.Is(err, ErrThreadNotOwned) {
		t.Errorf("expected ErrThreadNotOwned, got %v", err)
	}
}

func TestListMessagesDelta_MissingThreadReturnsErrThreadNotOwned(t *testing.T) {
	db := openMessageRepoTestDB(t)
	_, _, _, err := ListMessagesDelta(context.Background(), db, 7, 99999, Cursor{}, 100)
	if err == nil || !errors.Is(err, ErrThreadNotOwned) {
		t.Errorf("missing thread should map to ErrThreadNotOwned (don't leak existence): %v", err)
	}
}

func TestListMessagesDelta_PaginatesAndReportsHasMore(t *testing.T) {
	db := openMessageRepoTestDB(t)
	threadID := seedThread(t, db, 7, "thr")
	base := time.Date(2026, 5, 17, 22, 0, 0, 0, time.UTC)
	for i := 0; i < 25; i++ {
		seedMessage(t, db, 7, threadID, "m-"+string('a'+rune(i)), "u", "a", base.Add(time.Duration(i)*time.Second))
	}

	// Page 1: limit 10.
	rows1, c1, more1, _ := ListMessagesDelta(context.Background(), db, 7, threadID, Cursor{}, 10)
	if len(rows1) != 10 || !more1 {
		t.Errorf("page 1: rows=%d hasMore=%v want 10/true", len(rows1), more1)
	}
	// Page 2.
	rows2, _, more2, _ := ListMessagesDelta(context.Background(), db, 7, threadID, c1, 10)
	if len(rows2) != 10 || !more2 {
		t.Errorf("page 2: rows=%d hasMore=%v want 10/true", len(rows2), more2)
	}
	// Page 3 — drain remainder. Use c1's resume point AND skip via
	// page 2's last row, then assert total seen = 25 across all 3.
	rows3, _, more3, _ := ListMessagesDelta(context.Background(), db, 7, threadID, Cursor{}, 100)
	if more3 {
		t.Errorf("full pull (limit=100) hasMore should be false (only 25 rows total)")
	}
	if len(rows3) != 25 {
		t.Errorf("full pull: got %d rows, want 25", len(rows3))
	}
}

func TestListMessagesDelta_RoundsTripsAllFields(t *testing.T) {
	db := openMessageRepoTestDB(t)
	threadID := seedThread(t, db, 7, "thr")
	now := time.Now().UTC()
	if err := db.Exec(
		`INSERT INTO w_workagent_message
		   (uid, uuid, thread_id, user_text, ai_text, chat_mode,
		    content_type, structured_content, actions, metadata,
		    use_images, use_files, user_rating, user_feedback,
		    updated_at, created_at)
		 VALUES (?, 'm-full', ?, ?, ?, 'ppt',
		         'text', '{"blocks":[]}', '[{"label":"OK"}]', '{"plan":"go"}',
		         'img-1.png', 'file-1.pdf', -1, 'wrong layout',
		         ?, ?)`,
		7, threadID, "build a slide deck", "here you go", now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
	).Error; err != nil {
		t.Fatal(err)
	}

	rows, _, _, err := ListMessagesDelta(context.Background(), db, 7, threadID, Cursor{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	r := rows[0]
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"CloudMessageID nonempty", r.CloudMessageID, ""}, // just want non-empty
		{"UUID", r.UUID, "m-full"},
		{"ThreadUUID", r.ThreadUUID, "thr"},
		{"UserText", r.UserText, "build a slide deck"},
		{"AIText", r.AIText, "here you go"},
		{"ContentType", r.ContentType, "text"},
		{"StructuredContent", r.StructuredContent, `{"blocks":[]}`},
		{"Actions", r.Actions, `[{"label":"OK"}]`},
		{"Metadata", r.Metadata, `{"plan":"go"}`},
		{"UseImages", r.UseImages, "img-1.png"},
		{"UseFiles", r.UseFiles, "file-1.pdf"},
		{"UserFeedback", r.UserFeedback, "wrong layout"},
	}
	for _, c := range cases {
		if c.name == "CloudMessageID nonempty" {
			if c.got == "" {
				t.Errorf("%s: empty", c.name)
			}
			continue
		}
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}
	if r.UserRating != -1 {
		t.Errorf("UserRating: got %d, want -1", r.UserRating)
	}
}

func TestListMessagesDelta_EmptyResult(t *testing.T) {
	db := openMessageRepoTestDB(t)
	threadID := seedThread(t, db, 7, "thr")
	rows, next, hasMore, err := ListMessagesDelta(context.Background(), db, 7, threadID, Cursor{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 || hasMore {
		t.Errorf("empty thread: rows=%d hasMore=%v", len(rows), hasMore)
	}
	if !next.IsZero() {
		t.Errorf("empty: cursor should remain zero, got %+v", next)
	}
}

func TestListMessagesDelta_CursorSkipsConsumed(t *testing.T) {
	db := openMessageRepoTestDB(t)
	threadID := seedThread(t, db, 7, "thr")
	base := time.Date(2026, 5, 17, 22, 0, 0, 0, time.UTC)
	seedMessage(t, db, 7, threadID, "m-1", "u", "a", base)
	seedMessage(t, db, 7, threadID, "m-2", "u", "a", base.Add(time.Minute))
	// First fetch: get all + the cursor.
	_, cur, _, _ := ListMessagesDelta(context.Background(), db, 7, threadID, Cursor{}, 100)
	// Resume: should yield zero rows (caught up).
	rows, _, more, _ := ListMessagesDelta(context.Background(), db, 7, threadID, cur, 100)
	if len(rows) != 0 || more {
		t.Errorf("after-catchup: rows=%d hasMore=%v want 0/false", len(rows), more)
	}
}

func TestListMessagesDelta_RejectsBadParams(t *testing.T) {
	if _, _, _, err := ListMessagesDelta(context.Background(), nil, 7, 1, Cursor{}, 10); err == nil {
		t.Error("nil db")
	}
	db := openMessageRepoTestDB(t)
	if _, _, _, err := ListMessagesDelta(context.Background(), db, 0, 1, Cursor{}, 10); err == nil {
		t.Error("uid=0")
	}
	if _, _, _, err := ListMessagesDelta(context.Background(), db, 7, 0, Cursor{}, 10); err == nil {
		t.Error("cloud_thread_id=0")
	}
}
