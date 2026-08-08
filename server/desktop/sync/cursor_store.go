//go:build desktop

// Package sync owns the desktop sidecar's pull-sync orchestration —
// the worker goroutine that periodically calls cloud sync endpoints
// and writes the deltas into local SQLite.
//
// Distinct from server/service/desktop/sync (cloud-side endpoint
// repository) and from server/desktop/cloud_proxy (HTTP client).
// Import aliases recommended where both might appear:
//
//	import desktopsync "server/desktop/sync"
//	import cloudsync   "server/service/desktop/sync"
//
// The platform-level Desktop sync contract is described in
// ProjectDocs/writer-work-agent-plugin-platform-design-2026-08.md.
package sync

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// metaRow mirrors server/desktop/db.go::metaRow. Redefined locally
// to avoid the cyclic import (server/desktop already imports this
// package via the wire-up in main.go; making server/desktop/sync
// import server/desktop would close the loop).
type metaRow struct {
	Key       string `gorm:"column:key;primaryKey"`
	Value     string `gorm:"column:value"`
	UpdatedAt string `gorm:"column:updated_at"`
}

func (metaRow) TableName() string { return "_local_meta" }

// Cursor keys live in the local meta table. Parameterized cursors
// (threads-by-account and messages-by-thread) use the prefixes below;
// render_jobs / thread_files retain fixed keys.
//
// Adding a new entity means: add the const + thread it through
// the worker. Keep the constants flat (no enum/iota) so a code
// search for the key string finds every callsite.
const (
	// CursorKeyThreads is the pre-account-scoping legacy key. Keep the
	// string stable so old local databases remain readable for diagnostics,
	// but production thread sync must never read or write it: a global
	// cursor can cause account B to skip history after account A signs out.
	CursorKeyThreads = "sync_cursor_threads"
	// CursorKeyThreadsPrefix is completed by ThreadsCursorKey with the
	// positive JWT subject UID, for example sync_cursor_threads_uid_42.
	CursorKeyThreadsPrefix = "sync_cursor_threads_uid_"

	CursorKeyRenderJobs  = "sync_cursor_render_jobs"
	CursorKeyThreadFiles = "sync_cursor_thread_files"
	// CursorKeyMessages is a PREFIX — actual keys are
	// "sync_cursor_messages_<thread_uuid>". A SyncWorker that
	// handles N threads holds N cursors, one per thread, since
	// the /sync/messages endpoint scopes to thread_id.
	CursorKeyMessagesPrefix = "sync_cursor_messages_"
)

// ThreadsCursorKey returns the local cursor key for one authenticated
// account. UID zero is never a valid account scope: accepting it would
// recreate a shared cursor bucket for malformed or subject-less tokens.
func ThreadsCursorKey(uid uint) (string, error) {
	if uid == 0 {
		return "", fmt.Errorf("threads cursor key: uid must be positive")
	}
	return fmt.Sprintf("%s%d", CursorKeyThreadsPrefix, uid), nil
}

// CursorStore persists sync resume points in the _local_meta table.
// One instance per sidecar process; safe for concurrent calls
// (SQLite is single-writer + GORM serializes through the *sql.DB
// pool — the lock-free design here is intentional).
//
// All methods take a key string explicitly rather than typed
// methods per entity. Reason: messages cursor is parameterized by
// thread UUID (CursorKeyMessagesPrefix + uuid) — a typed API
// would either expose that gluing or fail to model it cleanly.
// String-keyed keeps the contract simple.
type CursorStore struct {
	db *gorm.DB
}

// NewCursorStore returns a store backed by the given DB. Panics
// (intentionally) if db is nil — passing nil here is a programming
// error, not a runtime condition, and we'd rather fail at boot
// than discover later when a cursor write silently does nothing.
func NewCursorStore(db *gorm.DB) *CursorStore {
	if db == nil {
		panic("sync: NewCursorStore requires non-nil db")
	}
	return &CursorStore{db: db}
}

// WithContext returns a lightweight view whose SQL calls participate in ctx
// cancellation. The underlying database and cursor namespace are unchanged.
// Sync jobs bind ctx to their session lease so logout/re-login can abort an
// in-flight page transaction before either data or its cursor commits.
func (s *CursorStore) WithContext(ctx context.Context) *CursorStore {
	if s == nil || s.db == nil {
		panic("sync: CursorStore.WithContext requires initialized store")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return &CursorStore{db: s.db.WithContext(ctx)}
}

// withDB preserves the store abstraction while moving cursor operations into
// the caller's transaction. It is intentionally package-private: only sync
// jobs should couple cursor and entity commits this tightly.
func (s *CursorStore) withDB(db *gorm.DB) *CursorStore {
	if s == nil || s.db == nil || db == nil {
		panic("sync: CursorStore.withDB requires initialized store and db")
	}
	return &CursorStore{db: db}
}

// Get reads the cursor for `key`. Returns ("", nil) when the key
// has never been written — the caller (worker) treats this as
// "full sync from beginning". Returns ("", err) on genuine DB
// failure (rare; surfaces upstream).
func (s *CursorStore) Get(key string) (string, error) {
	var row metaRow
	err := s.db.Where("key = ?", key).First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", nil
		}
		return "", fmt.Errorf("cursor store: read %q: %w", key, err)
	}
	return row.Value, nil
}

// Set writes (upserts) the cursor for `key`. Empty cursor is
// rejected — the worker uses the absence of a key to mean "start
// from beginning", and a saved empty-string would be indistinguishable
// from that case while still triggering an UPDATE timestamp.
// Caller should Delete if it wants to reset.
func (s *CursorStore) Set(key, cursor string) error {
	if cursor == "" {
		return fmt.Errorf("cursor store: empty cursor not allowed; use Delete to reset %q", key)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res := s.db.Exec(`
		INSERT INTO _local_meta (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, cursor, now,
	)
	if res.Error != nil {
		return fmt.Errorf("cursor store: upsert %q: %w", key, res.Error)
	}
	return nil
}

// Delete removes the cursor row. Idempotent — calling for a key
// that doesn't exist is not an error. Used to reset the sync state
// (e.g. user clicked "sync everything fresh" or after a corruption
// recovery).
func (s *CursorStore) Delete(key string) error {
	res := s.db.Exec(`DELETE FROM _local_meta WHERE key = ?`, key)
	if res.Error != nil {
		return fmt.Errorf("cursor store: delete %q: %w", key, res.Error)
	}
	return nil
}
