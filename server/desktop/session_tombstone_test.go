//go:build desktop

package desktop

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	cloudproxy "server/desktop/cloud_proxy"
)

func openSessionTombstoneTestDB(t *testing.T, path string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS _local_meta (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		updated_at TEXT DEFAULT CURRENT_TIMESTAMP
	)`).Error; err != nil {
		t.Fatalf("create _local_meta: %v", err)
	}
	return db
}

func closeSessionTombstoneTestDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close SQLite: %v", err)
	}
}

func TestSQLiteSessionTombstoneMarker_PersistsOnlyFixedNonSecretState(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "session-state.db")
	db := openSessionTombstoneTestDB(t, dbPath)
	marker := NewSQLiteSessionTombstoneMarker(db)
	marked, err := marker.IsMarked()
	if err != nil || marked {
		t.Fatalf("fresh IsMarked: marked=%v err=%v", marked, err)
	}
	if err := marker.Mark(); err != nil {
		t.Fatalf("Mark: %v", err)
	}

	var row metaRow
	if err := db.Where("key = ?", sessionTombstoneMetaKey).First(&row).Error; err != nil {
		t.Fatalf("read marker row: %v", err)
	}
	if row.Key != sessionTombstoneMetaKey || row.Value != sessionTombstoneMetaValue {
		t.Fatalf("unexpected marker row: key=%q value=%q", row.Key, row.Value)
	}
	if strings.Contains(row.Key+row.Value, "token") || strings.Contains(row.Key+row.Value, "secret") {
		t.Fatalf("marker row appears to contain credential material: key=%q value=%q", row.Key, row.Value)
	}

	closeSessionTombstoneTestDB(t, db)
	db = openSessionTombstoneTestDB(t, dbPath)
	marker = NewSQLiteSessionTombstoneMarker(db)
	marked, err = marker.IsMarked()
	if err != nil || !marked {
		t.Fatalf("reopened IsMarked: marked=%v err=%v", marked, err)
	}
	// Row presence is authoritative even if another binary used a different
	// fixed value.
	if err := db.Exec(`UPDATE _local_meta SET value = ? WHERE key = ?`, "future_value", sessionTombstoneMetaKey).Error; err != nil {
		t.Fatalf("update marker value: %v", err)
	}
	marked, err = marker.IsMarked()
	if err != nil || !marked {
		t.Fatalf("future-value IsMarked: marked=%v err=%v", marked, err)
	}
	if err := marker.Unmark(); err != nil {
		t.Fatalf("Unmark: %v", err)
	}
	marked, err = marker.IsMarked()
	if err != nil || marked {
		t.Fatalf("unmarked IsMarked: marked=%v err=%v", marked, err)
	}
	closeSessionTombstoneTestDB(t, db)
}

type sqliteTombstoneTestKeychain struct {
	raw       []byte
	deleteErr error
}

func (k *sqliteTombstoneTestKeychain) Write(_, _ string, value []byte) error {
	k.raw = append(k.raw[:0], value...)
	return nil
}

func (k *sqliteTombstoneTestKeychain) Read(_, _ string) ([]byte, error) {
	if k.raw == nil {
		return nil, cloudproxy.ErrKeychainNoEntry
	}
	return append([]byte(nil), k.raw...), nil
}

func (k *sqliteTombstoneTestKeychain) Delete(_, _ string) error {
	if k.deleteErr != nil {
		return k.deleteErr
	}
	k.raw = nil
	return nil
}

func TestSQLiteSessionTombstoneMarker_BlocksStaleKeychainAfterDBReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "restart-state.db")
	db := openSessionTombstoneTestDB(t, dbPath)
	keychain := &sqliteTombstoneTestKeychain{}
	marker := NewSQLiteSessionTombstoneMarker(db)
	store := cloudproxy.NewTokenStoreWithTombstone(keychain, marker)
	if err := store.Save(cloudproxy.TokenPair{
		AccessToken:  "access-material-must-not-enter-sqlite",
		RefreshToken: "refresh-material-must-not-enter-sqlite",
	}); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	keychain.deleteErr = errors.New("simulated Keychain delete failure")
	if err := store.Clear(); !errors.Is(err, cloudproxy.ErrSessionPersistence) {
		t.Fatalf("Clear error=%v, want ErrSessionPersistence", err)
	}

	var row metaRow
	if err := db.Where("key = ?", sessionTombstoneMetaKey).First(&row).Error; err != nil {
		t.Fatalf("read persisted tombstone: %v", err)
	}
	if row.Value != sessionTombstoneMetaValue {
		t.Fatalf("persisted tombstone value=%q", row.Value)
	}
	if strings.Contains(row.Key+row.Value, "access-material") || strings.Contains(row.Key+row.Value, "refresh-material") {
		t.Fatal("SQLite tombstone stored credential material")
	}
	if len(keychain.raw) == 0 {
		t.Fatal("test setup: failed Keychain delete did not retain stale credentials")
	}

	closeSessionTombstoneTestDB(t, db)
	db = openSessionTombstoneTestDB(t, dbPath)
	restarted := cloudproxy.NewTokenStoreWithTombstone(
		keychain,
		NewSQLiteSessionTombstoneMarker(db),
	)
	if _, err := restarted.Get(); !errors.Is(err, cloudproxy.ErrNoSession) {
		t.Fatalf("restarted TokenStore accepted stale Keychain entry: %v", err)
	}
	closeSessionTombstoneTestDB(t, db)
}
