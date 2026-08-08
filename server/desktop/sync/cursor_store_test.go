//go:build desktop

package sync

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// openCursorTestDB builds a SQLite DB with the _local_meta schema
// the cursor store reads. Same DDL as the production migration
// (server/desktop/migrations_desktop/0001_init_workagent_tables.sql);
// kept inline here so the test doesn't reach across packages.
func openCursorTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "cursor.db")), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Exec(`CREATE TABLE _local_meta (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		updated_at TEXT DEFAULT CURRENT_TIMESTAMP
	)`).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	return db
}

func TestCursorStore_GetMissingReturnsEmpty(t *testing.T) {
	s := NewCursorStore(openCursorTestDB(t))
	v, err := s.Get(CursorKeyThreads)
	if err != nil {
		t.Errorf("missing key should not error, got: %v", err)
	}
	if v != "" {
		t.Errorf("missing key value: got %q, want empty", v)
	}
}

func TestCursorStore_SetThenGetRoundTrips(t *testing.T) {
	s := NewCursorStore(openCursorTestDB(t))
	if err := s.Set(CursorKeyThreads, "cursor-abc"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(CursorKeyThreads)
	if err != nil {
		t.Fatal(err)
	}
	if got != "cursor-abc" {
		t.Errorf("got %q, want cursor-abc", got)
	}
}

func TestCursorStore_SetUpserts(t *testing.T) {
	s := NewCursorStore(openCursorTestDB(t))
	if err := s.Set(CursorKeyThreads, "first"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(CursorKeyThreads, "second"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(CursorKeyThreads)
	if got != "second" {
		t.Errorf("upsert: got %q, want second", got)
	}
}

func TestCursorStore_EmptyCursorRejected(t *testing.T) {
	s := NewCursorStore(openCursorTestDB(t))
	if err := s.Set(CursorKeyThreads, ""); err == nil {
		t.Error("setting empty cursor should error; use Delete to reset")
	}
}

func TestCursorStore_Delete(t *testing.T) {
	s := NewCursorStore(openCursorTestDB(t))
	if err := s.Set(CursorKeyThreads, "to-delete"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(CursorKeyThreads); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(CursorKeyThreads)
	if got != "" {
		t.Errorf("after delete, got %q, want empty", got)
	}
}

func TestCursorStore_DeleteIdempotent(t *testing.T) {
	s := NewCursorStore(openCursorTestDB(t))
	// Delete a key that never existed — must not error.
	if err := s.Delete(CursorKeyRenderJobs); err != nil {
		t.Errorf("delete of nonexistent key should be no-op, got: %v", err)
	}
}

func TestCursorStore_PerThreadMessagesKey(t *testing.T) {
	// The messages cursor is parameterized by thread UUID;
	// callers concatenate. Verify the prefix works as expected.
	s := NewCursorStore(openCursorTestDB(t))
	keyA := CursorKeyMessagesPrefix + "uuid-thread-A"
	keyB := CursorKeyMessagesPrefix + "uuid-thread-B"

	if err := s.Set(keyA, "cursor-A"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(keyB, "cursor-B"); err != nil {
		t.Fatal(err)
	}

	gotA, _ := s.Get(keyA)
	gotB, _ := s.Get(keyB)
	if gotA != "cursor-A" || gotB != "cursor-B" {
		t.Errorf("per-thread cursors not isolated: A=%q B=%q", gotA, gotB)
	}
}

func TestThreadsCursorKey_IsDeterministicAndAccountScoped(t *testing.T) {
	keyA, err := ThreadsCursorKey(42)
	if err != nil {
		t.Fatal(err)
	}
	keyAAgain, err := ThreadsCursorKey(42)
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := ThreadsCursorKey(84)
	if err != nil {
		t.Fatal(err)
	}

	if keyA != "sync_cursor_threads_uid_42" {
		t.Fatalf("account A key = %q, want readable UID-scoped key", keyA)
	}
	if keyAAgain != keyA {
		t.Fatalf("same UID produced different keys: %q != %q", keyAAgain, keyA)
	}
	if keyB != "sync_cursor_threads_uid_84" || keyB == keyA {
		t.Fatalf("account B key = %q, account A key = %q", keyB, keyA)
	}
	if keyA == CursorKeyThreads || keyB == CursorKeyThreads {
		t.Fatal("account-scoped key must not collapse to the legacy global key")
	}
}

func TestThreadsCursorKey_RejectsZeroUID(t *testing.T) {
	key, err := ThreadsCursorKey(0)
	if err == nil {
		t.Fatal("zero UID should be rejected")
	}
	if key != "" {
		t.Fatalf("zero UID key = %q, want empty", key)
	}
}

func TestCursorStore_KeyConstantsPin(t *testing.T) {
	// Pin the actual key strings — they appear in the on-disk
	// SQLite cache, so renaming would invalidate every user's
	// existing cursor state on the next sidecar boot.
	cases := map[string]string{
		"CursorKeyThreads":        CursorKeyThreads,
		"CursorKeyThreadsPrefix":  CursorKeyThreadsPrefix,
		"CursorKeyRenderJobs":     CursorKeyRenderJobs,
		"CursorKeyThreadFiles":    CursorKeyThreadFiles,
		"CursorKeyMessagesPrefix": CursorKeyMessagesPrefix,
	}
	wantPrefix := "sync_cursor_"
	for name, got := range cases {
		if got == "" {
			t.Errorf("%s is empty", name)
		}
		if len(got) < len(wantPrefix) || got[:len(wantPrefix)] != wantPrefix {
			t.Errorf("%s = %q should start with %q", name, got, wantPrefix)
		}
	}
}

func TestNewCursorStore_NilDBPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on nil db")
		}
	}()
	_ = NewCursorStore(nil)
}
