//go:build desktop

package desktop

import (
	"fmt"

	"gorm.io/gorm"
)

// sessionTombstoneMetaKey is deliberately a fixed, non-secret local metadata
// key. Its value is also fixed; row presence alone is authoritative. No token,
// user identifier, OAuth scope, or credential revision is stored in SQLite.
const (
	sessionTombstoneMetaKey   = "auth_session_tombstone_v1"
	sessionTombstoneMetaValue = "logged_out"
)

// SQLiteSessionTombstoneMarker stores TokenStore's durable fail-closed bit in
// the existing _local_meta table. OpenLocalDB applies that table's migration
// before the marker is constructed in the Desktop sidecar.
type SQLiteSessionTombstoneMarker struct {
	db *gorm.DB
}

// NewSQLiteSessionTombstoneMarker binds a marker to Desktop's existing local
// SQLite database. A nil DB is a programming error and is rejected at boot.
func NewSQLiteSessionTombstoneMarker(db *gorm.DB) *SQLiteSessionTombstoneMarker {
	if db == nil {
		panic("desktop: NewSQLiteSessionTombstoneMarker requires non-nil db")
	}
	return &SQLiteSessionTombstoneMarker{db: db}
}

// IsMarked checks row presence, intentionally ignoring the stored value. If a
// future or older binary wrote a different fixed value, the safer behavior is
// still to reject Keychain credentials until a complete Save clears the row.
func (m *SQLiteSessionTombstoneMarker) IsMarked() (bool, error) {
	var count int64
	res := m.db.Raw(`SELECT COUNT(*) FROM _local_meta WHERE key = ?`, sessionTombstoneMetaKey).Scan(&count)
	if res.Error != nil {
		return false, fmt.Errorf("desktop session tombstone read: %w", res.Error)
	}
	return count > 0, nil
}

// Mark is an idempotent SQLite upsert. It must commit before TokenStore touches
// Keychain, so a process crash during credential rotation restarts logged out.
func (m *SQLiteSessionTombstoneMarker) Mark() error {
	res := m.db.Exec(`
		INSERT INTO _local_meta (key, value, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET
			value = excluded.value,
			updated_at = excluded.updated_at`,
		sessionTombstoneMetaKey,
		sessionTombstoneMetaValue,
	)
	if res.Error != nil {
		return fmt.Errorf("desktop session tombstone mark: %w", res.Error)
	}
	return nil
}

// Unmark is idempotent. TokenStore calls it only after the complete TokenPair
// has reached Keychain; until this delete commits, restart remains logged out.
func (m *SQLiteSessionTombstoneMarker) Unmark() error {
	res := m.db.Exec(`DELETE FROM _local_meta WHERE key = ?`, sessionTombstoneMetaKey)
	if res.Error != nil {
		return fmt.Errorf("desktop session tombstone unmark: %w", res.Error)
	}
	return nil
}
