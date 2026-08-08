package sync

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// openThreadRepoTestDB stands up SQLite with the subset of
// w_workagent_thread columns the repo reads. Mirrors the cloud
// MySQL DDL closely enough that our tests catch real schema
// drift (we test the exact SELECT the repo issues), without
// pulling in every column the production server tracks.
func openThreadRepoTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "thread_repo.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.Exec(`CREATE TABLE w_workagent_thread (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL DEFAULT 0,
		uuid TEXT NOT NULL,
		name TEXT NOT NULL DEFAULT '',
		agent_mode TEXT NOT NULL DEFAULT 'ppt',
		agent_type TEXT NOT NULL DEFAULT 'general_agent',
		model TEXT NOT NULL DEFAULT '',
		message_count INTEGER NOT NULL DEFAULT 0,
		msg_preview TEXT NOT NULL DEFAULT '',
		file_count INTEGER NOT NULL DEFAULT 0,
		is_public INTEGER NOT NULL DEFAULT 0,
		updated_at TEXT,
		created_at TEXT
	)`).Error; err != nil {
		t.Fatalf("create table: %v", err)
	}
	return db
}

// seedThreadRow inserts a thread row at a deterministic timestamp.
// updatedAt MUST be controlled per-test because the repo's cursor
// semantics are timestamp-driven.
func seedThreadRow(t *testing.T, db *gorm.DB, uid int, uuid, name string, updatedAt time.Time) uint {
	t.Helper()
	ts := updatedAt.UTC().Format(time.RFC3339Nano)
	if err := db.Exec(
		`INSERT INTO w_workagent_thread
		   (uid, uuid, name, agent_mode, agent_type, model, message_count,
		    msg_preview, file_count, is_public, updated_at, created_at)
		 VALUES (?, ?, ?, 'ppt', 'general_agent', 'work-pro', 0, '', 0, 0, ?, ?)`,
		uid, uuid, name, ts, ts,
	).Error; err != nil {
		t.Fatalf("seed thread: %v", err)
	}
	var id int64
	if err := db.Raw(`SELECT id FROM w_workagent_thread WHERE uuid = ?`, uuid).Row().Scan(&id); err != nil {
		t.Fatalf("scan id: %v", err)
	}
	return uint(id)
}

func TestListThreadsDelta_HappyPath(t *testing.T) {
	db := openThreadRepoTestDB(t)
	base := time.Date(2026, 5, 17, 22, 0, 0, 0, time.UTC)
	seedThreadRow(t, db, 7, "uuid-a", "A", base)
	seedThreadRow(t, db, 7, "uuid-b", "B", base.Add(time.Minute))
	seedThreadRow(t, db, 7, "uuid-c", "C", base.Add(2*time.Minute))

	rows, next, hasMore, err := ListThreadsDelta(context.Background(), db, 7, Cursor{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows: got %d, want 3", len(rows))
	}
	if hasMore {
		t.Error("hasMore should be false on full result")
	}
	// Oldest first.
	if rows[0].UUID != "uuid-a" || rows[1].UUID != "uuid-b" || rows[2].UUID != "uuid-c" {
		t.Errorf("order wrong: %v %v %v", rows[0].UUID, rows[1].UUID, rows[2].UUID)
	}
	// next cursor points at the last row.
	if !next.UpdatedAt.Equal(base.Add(2 * time.Minute)) {
		t.Errorf("next.UpdatedAt: got %v", next.UpdatedAt)
	}
	// Field round-trips.
	if rows[0].AgentMode != "ppt" || rows[0].AgentType != "general_agent" || rows[0].Model != "work-pro" {
		t.Errorf("fields: %+v", rows[0])
	}
}

func TestListThreadsDelta_FiltersByUID(t *testing.T) {
	db := openThreadRepoTestDB(t)
	base := time.Now().UTC()
	seedThreadRow(t, db, 7, "uuid-7", "mine", base)
	seedThreadRow(t, db, 42, "uuid-42", "theirs", base)

	rows, _, _, _ := ListThreadsDelta(context.Background(), db, 7, Cursor{}, 100)
	if len(rows) != 1 {
		t.Fatalf("uid=7 filter: got %d rows, want 1", len(rows))
	}
	if rows[0].UUID != "uuid-7" {
		t.Errorf("wrong row leaked: %q", rows[0].UUID)
	}
	// Cross-check: uid=42 sees their own.
	rows42, _, _, _ := ListThreadsDelta(context.Background(), db, 42, Cursor{}, 100)
	if len(rows42) != 1 || rows42[0].UUID != "uuid-42" {
		t.Errorf("uid=42 view wrong: %+v", rows42)
	}
}

func TestListThreadsDelta_PaginatesAndReportsHasMore(t *testing.T) {
	db := openThreadRepoTestDB(t)
	base := time.Date(2026, 5, 17, 22, 0, 0, 0, time.UTC)
	for i := 0; i < 50; i++ {
		seedThreadRow(t, db, 7, "uuid-"+rune2s('a'+rune(i)), "T", base.Add(time.Duration(i)*time.Second))
	}

	// Page 1: limit 10.
	rows1, cursor1, hasMore1, err := ListThreadsDelta(context.Background(), db, 7, Cursor{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows1) != 10 {
		t.Errorf("page 1 size: got %d, want 10", len(rows1))
	}
	if !hasMore1 {
		t.Error("page 1 hasMore should be true")
	}
	// Page 2..5
	cursor := cursor1
	totalSeen := len(rows1)
	for page := 2; page <= 5; page++ {
		rows, next, hasMore, err := ListThreadsDelta(context.Background(), db, 7, cursor, 10)
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		totalSeen += len(rows)
		// Last page is page 5: should have rows but no more.
		if page < 5 && !hasMore {
			t.Errorf("page %d hasMore should be true", page)
		}
		if page == 5 && hasMore {
			t.Errorf("page 5 hasMore should be false")
		}
		cursor = next
	}
	if totalSeen != 50 {
		t.Errorf("total seen across pages: got %d, want 50 (duplicates or skips)", totalSeen)
	}
}

func TestListThreadsDelta_CursorSkipsConsumed(t *testing.T) {
	db := openThreadRepoTestDB(t)
	base := time.Date(2026, 5, 17, 22, 0, 0, 0, time.UTC)
	idA := seedThreadRow(t, db, 7, "uuid-a", "A", base)
	seedThreadRow(t, db, 7, "uuid-b", "B", base.Add(time.Minute))
	seedThreadRow(t, db, 7, "uuid-c", "C", base.Add(2*time.Minute))

	// Cursor pointing AT row A → next pull should return B and C.
	cur := Cursor{UpdatedAt: base, ID: int64(idA)}
	rows, _, _, _ := ListThreadsDelta(context.Background(), db, 7, cur, 100)
	if len(rows) != 2 {
		t.Fatalf("after cursor: got %d rows, want 2", len(rows))
	}
	if rows[0].UUID != "uuid-b" || rows[1].UUID != "uuid-c" {
		t.Errorf("cursor skip wrong: %+v", rows)
	}
}

func TestListThreadsDelta_SameTimestampUsesIDTiebreak(t *testing.T) {
	db := openThreadRepoTestDB(t)
	t0 := time.Date(2026, 5, 17, 22, 0, 0, 0, time.UTC)
	id1 := seedThreadRow(t, db, 7, "uuid-1", "1", t0)
	id2 := seedThreadRow(t, db, 7, "uuid-2", "2", t0)
	seedThreadRow(t, db, 7, "uuid-3", "3", t0)

	// Cursor at (t0, id1) — should NOT return uuid-1 (already
	// consumed) but should return uuid-2 and uuid-3 (same timestamp,
	// higher ids).
	cur := Cursor{UpdatedAt: t0, ID: int64(id1)}
	rows, _, _, _ := ListThreadsDelta(context.Background(), db, 7, cur, 100)
	if len(rows) != 2 {
		t.Fatalf("tiebreak: got %d rows, want 2", len(rows))
	}
	if rows[0].UUID != "uuid-2" || rows[1].UUID != "uuid-3" {
		t.Errorf("tiebreak order: %+v", rows)
	}
	// And cursor at (t0, id2) → only uuid-3.
	cur = Cursor{UpdatedAt: t0, ID: int64(id2)}
	rows, _, _, _ = ListThreadsDelta(context.Background(), db, 7, cur, 100)
	if len(rows) != 1 || rows[0].UUID != "uuid-3" {
		t.Errorf("second tiebreak: %+v", rows)
	}
}

func TestListThreadsDelta_EmptyResult(t *testing.T) {
	db := openThreadRepoTestDB(t)
	rows, next, hasMore, err := ListThreadsDelta(context.Background(), db, 7, Cursor{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("empty: got %d rows", len(rows))
	}
	if hasMore {
		t.Error("empty: hasMore should be false")
	}
	if !next.IsZero() {
		t.Errorf("empty: next cursor should be zero, got %+v", next)
	}
}

func TestListThreadsDelta_EmptyResultWithNonZeroInputCursor(t *testing.T) {
	// Poll-with-no-changes: client sends a cursor, we have no new
	// rows. The returned cursor must be the input cursor (not zero)
	// so the client doesn't restart from the beginning next poll.
	db := openThreadRepoTestDB(t)
	cur := Cursor{UpdatedAt: time.Now().UTC().Add(-time.Hour), ID: 999}
	_, next, hasMore, _ := ListThreadsDelta(context.Background(), db, 7, cur, 100)
	if hasMore {
		t.Error("empty: hasMore should be false")
	}
	if !next.UpdatedAt.Equal(cur.UpdatedAt) || next.ID != cur.ID {
		t.Errorf("empty-poll: cursor changed from %+v to %+v", cur, next)
	}
}

func TestListThreadsDelta_RejectsBadParams(t *testing.T) {
	db := openThreadRepoTestDB(t)
	if _, _, _, err := ListThreadsDelta(context.Background(), nil, 7, Cursor{}, 10); err == nil {
		t.Error("nil db should error")
	}
	if _, _, _, err := ListThreadsDelta(context.Background(), db, 0, Cursor{}, 10); err == nil {
		t.Error("uid=0 should error")
	}
	if _, _, _, err := ListThreadsDelta(context.Background(), db, -1, Cursor{}, 10); err == nil {
		t.Error("uid<0 should error")
	}
}

func TestListThreadsDelta_DefaultsLimitWhenZero(t *testing.T) {
	db := openThreadRepoTestDB(t)
	base := time.Now().UTC()
	for i := 0; i < 5; i++ {
		seedThreadRow(t, db, 7, "uuid-"+rune2s('a'+rune(i)), "T", base.Add(time.Duration(i)*time.Second))
	}
	rows, _, _, err := ListThreadsDelta(context.Background(), db, 7, Cursor{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 {
		t.Errorf("limit=0 default: got %d rows, want 5 (all within default 100)", len(rows))
	}
}

// rune2s converts a rune to its single-char string. Tiny helper so
// the seed loop is readable (uuid-a, uuid-b, ...) without
// fmt.Sprintf bloating each call.
func rune2s(r rune) string { return string(r) }
