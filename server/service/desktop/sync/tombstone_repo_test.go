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

// openTombstoneTestDB stands up SQLite with the tombstone table
// matching migrations/20260642_create_workagent_tombstone.sql.
func openTombstoneTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "tombstone.db")), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE w_workagent_tombstone (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL,
		entity_type TEXT NOT NULL,
		entity_id INTEGER NOT NULL,
		entity_uuid TEXT NOT NULL,
		deleted_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func TestInsertTombstone_Basic(t *testing.T) {
	db := openTombstoneTestDB(t)
	if err := InsertTombstone(db, 7, "thread", 42, "thr-uuid"); err != nil {
		t.Fatal(err)
	}
	var (
		uid       int
		entType   string
		entID     uint
		entUUID   string
		deletedAt string
	)
	db.Raw(`SELECT uid, entity_type, entity_id, entity_uuid, deleted_at FROM w_workagent_tombstone`).
		Row().Scan(&uid, &entType, &entID, &entUUID, &deletedAt)
	if uid != 7 || entType != "thread" || entID != 42 || entUUID != "thr-uuid" {
		t.Errorf("row: uid=%d type=%q id=%d uuid=%q", uid, entType, entID, entUUID)
	}
	if deletedAt == "" {
		t.Error("deleted_at should be populated")
	}
}

func TestInsertTombstone_RejectsBadParams(t *testing.T) {
	db := openTombstoneTestDB(t)
	cases := []struct {
		name       string
		tx         *gorm.DB
		uid        int
		entityType string
		entityID   uint
		entityUUID string
	}{
		{"nil tx", nil, 7, "thread", 1, "u"},
		{"zero uid", db, 0, "thread", 1, "u"},
		{"empty entity_type", db, 7, "", 1, "u"},
		{"typo entity_type", db, 7, "thrad", 1, "u"},   // closed-enum guard: "thrad" must NOT silently store
		{"unknown entity_type", db, 7, "render", 1, "u"}, // new entity types must be added to the enum first
		{"zero entity_id", db, 7, "thread", 0, "u"},
		{"empty entity_uuid", db, 7, "thread", 1, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := InsertTombstone(tc.tx, tc.uid, tc.entityType, tc.entityID, tc.entityUUID)
			if err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestListTombstonesDelta_HappyPath(t *testing.T) {
	db := openTombstoneTestDB(t)
	for i := 0; i < 3; i++ {
		if err := InsertTombstone(db, 7, "thread", uint(i+1), "u-"+string('a'+rune(i))); err != nil {
			t.Fatal(err)
		}
	}
	// Also insert a message tombstone — should NOT appear in "thread" query.
	if err := InsertTombstone(db, 7, "message", 99, "m-99"); err != nil {
		t.Fatal(err)
	}

	rows, _, hasMore, err := ListTombstonesDelta(context.Background(), db, 7, "thread", Cursor{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || hasMore {
		t.Fatalf("rows=%d hasMore=%v want 3/false", len(rows), hasMore)
	}
	// Order: oldest first.
	if rows[0].EntityUUID != "u-a" || rows[2].EntityUUID != "u-c" {
		t.Errorf("order wrong: %v", []string{rows[0].EntityUUID, rows[1].EntityUUID, rows[2].EntityUUID})
	}
	if rows[0].EntityType != "thread" {
		t.Errorf("entity_type should round-trip: %q", rows[0].EntityType)
	}
}

func TestListTombstonesDelta_FiltersByUID(t *testing.T) {
	db := openTombstoneTestDB(t)
	_ = InsertTombstone(db, 7, "thread", 1, "mine")
	_ = InsertTombstone(db, 42, "thread", 2, "theirs")

	rows, _, _, _ := ListTombstonesDelta(context.Background(), db, 7, "thread", Cursor{}, 100)
	if len(rows) != 1 || rows[0].EntityUUID != "mine" {
		t.Errorf("uid=7 filter: %+v", rows)
	}
}

func TestListTombstonesDelta_FiltersByEntityType(t *testing.T) {
	db := openTombstoneTestDB(t)
	_ = InsertTombstone(db, 7, "thread", 1, "t-1")
	_ = InsertTombstone(db, 7, "message", 2, "m-1")

	rows, _, _, _ := ListTombstonesDelta(context.Background(), db, 7, "message", Cursor{}, 100)
	if len(rows) != 1 || rows[0].EntityUUID != "m-1" {
		t.Errorf("entity_type filter: %+v", rows)
	}
}

func TestListTombstonesDelta_PaginatesAndCursors(t *testing.T) {
	db := openTombstoneTestDB(t)
	// Insert 7 tombstones with distinct timestamps (need sleep so
	// SQLite's CURRENT_TIMESTAMP differs; easier to set explicitly).
	for i := 0; i < 7; i++ {
		now := time.Now().UTC().Add(time.Duration(i) * time.Millisecond).Format(time.RFC3339Nano)
		db.Exec(
			`INSERT INTO w_workagent_tombstone (uid, entity_type, entity_id, entity_uuid, deleted_at)
			 VALUES (?, 'thread', ?, ?, ?)`,
			7, i+1, "u-"+string('a'+rune(i)), now,
		)
	}
	// Page 1: limit 3 → 3 rows, hasMore=true.
	rows1, cur1, more1, _ := ListTombstonesDelta(context.Background(), db, 7, "thread", Cursor{}, 3)
	if len(rows1) != 3 || !more1 {
		t.Fatalf("page1: rows=%d hasMore=%v", len(rows1), more1)
	}
	// Page 2 with cursor → next rows.
	rows2, _, _, _ := ListTombstonesDelta(context.Background(), db, 7, "thread", cur1, 3)
	if len(rows2) != 3 {
		t.Fatalf("page2: rows=%d, want 3", len(rows2))
	}
	if rows1[2].EntityUUID == rows2[0].EntityUUID {
		t.Errorf("cursor: page1 last %q duplicated as page2 first", rows1[2].EntityUUID)
	}
}

func TestListTombstonesDelta_EmptyPreservesInputCursor(t *testing.T) {
	db := openTombstoneTestDB(t)
	cur := Cursor{UpdatedAt: time.Now().UTC().Add(-time.Hour), ID: 999}
	_, next, more, _ := ListTombstonesDelta(context.Background(), db, 7, "thread", cur, 100)
	if more {
		t.Error("empty: hasMore should be false")
	}
	if !next.UpdatedAt.Equal(cur.UpdatedAt) || next.ID != cur.ID {
		t.Errorf("empty poll: cursor changed from %+v to %+v", cur, next)
	}
}

// === P1.A.5c prune tests ===

// seedTombstoneAt inserts a tombstone with explicit deleted_at,
// bypassing the now()-stamping in InsertTombstone. Needed for
// prune tests that span the retention window.
func seedTombstoneAt(t *testing.T, db *gorm.DB, uid int, entityType string, entityID uint, uuid string, deletedAt time.Time) {
	t.Helper()
	if err := db.Exec(
		`INSERT INTO w_workagent_tombstone (uid, entity_type, entity_id, entity_uuid, deleted_at)
		 VALUES (?, ?, ?, ?, ?)`,
		uid, entityType, entityID, uuid, deletedAt.UTC().Format(time.RFC3339Nano),
	).Error; err != nil {
		t.Fatal(err)
	}
}

func tombstoneCount(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	db.Raw(`SELECT count(*) FROM w_workagent_tombstone`).Row().Scan(&n)
	return n
}

func TestPruneTombstones_HappyPath(t *testing.T) {
	db := openTombstoneTestDB(t)
	now := time.Now().UTC()
	// 3 old (past retention) + 2 recent (within retention).
	seedTombstoneAt(t, db, 7, "thread", 1, "old-1", now.Add(-100*24*time.Hour))
	seedTombstoneAt(t, db, 7, "thread", 2, "old-2", now.Add(-95*24*time.Hour))
	seedTombstoneAt(t, db, 7, "thread", 3, "old-3", now.Add(-91*24*time.Hour))
	seedTombstoneAt(t, db, 7, "thread", 4, "recent-1", now.Add(-30*24*time.Hour))
	seedTombstoneAt(t, db, 7, "thread", 5, "recent-2", now.Add(-1*time.Hour))

	n, err := PruneTombstones(db, 90*24*time.Hour, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("rows pruned: got %d, want 3", n)
	}
	if got := tombstoneCount(t, db); got != 2 {
		t.Errorf("rows remaining: got %d, want 2 (the recent ones)", got)
	}
}

func TestPruneTombstones_BatchCap(t *testing.T) {
	db := openTombstoneTestDB(t)
	now := time.Now().UTC()
	for i := 0; i < 10; i++ {
		seedTombstoneAt(t, db, 7, "thread", uint(i+1), "u-"+string('a'+rune(i)), now.Add(-100*24*time.Hour))
	}
	n, err := PruneTombstones(db, 90*24*time.Hour, 3)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("batch=3: got %d, want 3", n)
	}
	if got := tombstoneCount(t, db); got != 7 {
		t.Errorf("remaining: got %d, want 7", got)
	}
}

func TestPruneTombstones_BatchZeroDeletesAll(t *testing.T) {
	db := openTombstoneTestDB(t)
	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		seedTombstoneAt(t, db, 7, "thread", uint(i+1), "u-"+string('a'+rune(i)), now.Add(-100*24*time.Hour))
	}
	n, _ := PruneTombstones(db, 90*24*time.Hour, 0)
	if n != 5 {
		t.Errorf("batch=0 (unlimited): got %d, want 5", n)
	}
}

func TestPruneTombstones_NothingToPrune(t *testing.T) {
	db := openTombstoneTestDB(t)
	seedTombstoneAt(t, db, 7, "thread", 1, "u-a", time.Now().UTC().Add(-1*time.Hour))
	n, err := PruneTombstones(db, 90*24*time.Hour, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("nothing to prune: got %d, want 0", n)
	}
}

func TestPruneTombstones_RejectsBadParams(t *testing.T) {
	if _, err := PruneTombstones(nil, time.Hour, 0); err == nil {
		t.Error("nil db should error")
	}
	db := openTombstoneTestDB(t)
	if _, err := PruneTombstones(db, 0, 0); err == nil {
		t.Error("retention=0 should error")
	}
	if _, err := PruneTombstones(db, -time.Hour, 0); err == nil {
		t.Error("negative retention should error")
	}
}

func TestPruneTombstones_DrainViaRepeatedBatch(t *testing.T) {
	// Sweeper pattern: call PruneTombstones in a loop until n==0.
	// Verify the loop terminates + deletes all eligible rows AND
	// leaves the recent survivors intact.
	db := openTombstoneTestDB(t)
	now := time.Now().UTC()
	for i := 0; i < 25; i++ {
		seedTombstoneAt(t, db, 7, "thread", uint(i+1), "u-"+string('a'+rune(i)), now.Add(-100*24*time.Hour))
	}
	seedTombstoneAt(t, db, 7, "thread", 999, "survivor-1", now.Add(-1*time.Hour))
	seedTombstoneAt(t, db, 7, "thread", 1000, "survivor-2", now.Add(-2*time.Hour))

	total := int64(0)
	for i := 0; i < 20; i++ { // hard iteration cap so a test bug can't hang
		n, err := PruneTombstones(db, 90*24*time.Hour, 10)
		if err != nil {
			t.Fatal(err)
		}
		total += n
		if n == 0 {
			break
		}
	}
	if total != 25 {
		t.Errorf("total pruned: got %d, want 25", total)
	}
	if got := tombstoneCount(t, db); got != 2 {
		t.Errorf("survivors: got %d, want 2", got)
	}
}

func TestListTombstonesDelta_RejectsBadParams(t *testing.T) {
	if _, _, _, err := ListTombstonesDelta(context.Background(), nil, 7, "thread", Cursor{}, 10); err == nil {
		t.Error("nil db")
	}
	db := openTombstoneTestDB(t)
	if _, _, _, err := ListTombstonesDelta(context.Background(), db, 0, "thread", Cursor{}, 10); err == nil {
		t.Error("uid=0")
	}
	if _, _, _, err := ListTombstonesDelta(context.Background(), db, 7, "", Cursor{}, 10); err == nil {
		t.Error("empty entity_type")
	}
	// Closed-enum guard: a typo'd entity_type would silently return
	// zero tombstones (since no rows have that type) and the
	// sidecar would never see the deletes. Reject at the boundary.
	if _, _, _, err := ListTombstonesDelta(context.Background(), db, 7, "thrad", Cursor{}, 10); err == nil {
		t.Error("typo entity_type should be rejected")
	}
}
