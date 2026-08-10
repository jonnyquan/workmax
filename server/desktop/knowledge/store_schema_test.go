//go:build desktop && cgo

package knowledge

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// openFileDB opens a real on-disk database. The schema-reconciliation tests
// need one: an in-memory database cannot be closed and reopened, and "does an
// existing database get upgraded" is the entire question here.
//
// It is also what gives CI a run of the vec0 stack that does not depend on the
// native embedding model — the store, its schema channel and its self-check are
// exercised end to end with plain vectors, so a glebarez × modernc regression is
// caught by a test that never skips.
func openFileDB(t *testing.T, path string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// The whole chain on a real file, with no model: create, write, uid-scoped
// read, isolation, delete, reopen. If sqlite-vec ever stops working under
// modernc, this is the test that says so.
func TestStoreSmokeOnFileDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "knowledge.db")
	ctx := context.Background()

	const owner, stranger = uint64(1<<62) + 1, uint64(1<<62) + 2

	db := openFileDB(t, path)
	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if store.RebuiltFrom() != "" {
		t.Errorf("a fresh database reported a rebuild from %q", store.RebuiltFrom())
	}
	if _, pending := store.ReindexPending(ctx); pending {
		t.Error("a fresh database must not ask for a re-index")
	}

	if _, err := store.UpsertChunks(ctx, []Chunk{
		{UID: owner, ChunkUID: "c1", SourceType: SourceTypeFile, SourceID: "7", Text: "owner text", Embedding: basis(0)},
		{UID: stranger, ChunkUID: "c2", SourceType: SourceTypeFile, SourceID: "8", Text: "stranger text", Embedding: basis(0)},
	}); err != nil {
		t.Fatalf("UpsertChunks: %v", err)
	}

	hits, err := store.Search(ctx, owner, basis(0), 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].ChunkUID != "c1" {
		t.Fatalf("owner search returned %+v, want only c1", hits)
	}
	if hits[0].UID != owner {
		t.Errorf("hit uid = %d, want %d", hits[0].UID, owner)
	}
	if hits[0].CreatedAt <= 0 {
		t.Errorf("created_at = %d, want a write timestamp", hits[0].CreatedAt)
	}

	// Identical vector, different owner: distance alone would have ranked the
	// stranger's chunk first.
	if n, err := store.DeleteBySource(ctx, SourceTypeFile, "8"); err != nil || n != 1 {
		t.Fatalf("DeleteBySource = (%d, %v), want (1, nil)", n, err)
	}

	// Reopen: the table persists and is recognised as current.
	sqlDB, _ := db.DB()
	_ = sqlDB.Close()
	reopened, err := NewStore(openFileDB(t, path))
	if err != nil {
		t.Fatalf("NewStore on reopen: %v", err)
	}
	if reopened.RebuiltFrom() != "" {
		t.Errorf("reopening a current database rebuilt it (from %q)", reopened.RebuiltFrom())
	}
	hits, err = reopened.Search(ctx, owner, basis(0), 10)
	if err != nil {
		t.Fatalf("Search after reopen: %v", err)
	}
	if len(hits) != 1 || hits[0].Text != "owner text" {
		t.Fatalf("chunks did not survive a reopen: %+v", hits)
	}
}

// v1 shipped without any version record, so an existing install is recognised
// by the shape of what it has, not by what it claims. Everything indexed under
// it is derived data and is dropped; the marker is what tells the app to put it
// back.
func TestStoreRebuildsUnversionedLegacyTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db := openFileDB(t, path)
	sqlDB, _ := db.DB()
	ctx := context.Background()

	legacyDDL := fmt.Sprintf(
		"CREATE VIRTUAL TABLE %s USING vec0(embedding float[%d], +chunk_uid TEXT, +source_type TEXT, +source_id TEXT, +chunk_text TEXT)",
		chunksTable, EmbeddingDim)
	if _, err := sqlDB.ExecContext(ctx, legacyDDL); err != nil {
		t.Fatalf("create v1 table: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx,
		fmt.Sprintf("INSERT INTO %s(embedding, chunk_uid, source_type, source_id, chunk_text) VALUES (?,?,?,?,?)", chunksTable),
		packVec(basis(0)), "old", SourceTypeFile, "1", "written under v1"); err != nil {
		t.Fatalf("insert into v1 table: %v", err)
	}

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore over a v1 table: %v", err)
	}
	if store.RebuiltFrom() != "unversioned" {
		t.Fatalf("RebuiltFrom = %q, want \"unversioned\"", store.RebuiltFrom())
	}
	if _, pending := store.ReindexPending(ctx); !pending {
		t.Fatal("a rebuild that dropped the user's chunks must ask for a re-index")
	}

	// The new shape works, and the v1 rows are gone rather than half-readable.
	const uid = uint64(1<<62) + 5
	if _, err := store.UpsertChunks(ctx, []Chunk{
		{UID: uid, ChunkUID: "new", SourceType: SourceTypeFile, SourceID: "1", Text: "written under v2", Embedding: basis(0)},
	}); err != nil {
		t.Fatalf("UpsertChunks after rebuild: %v", err)
	}
	hits, err := store.Search(ctx, uid, basis(0), 10)
	if err != nil {
		t.Fatalf("Search after rebuild: %v", err)
	}
	if len(hits) != 1 || hits[0].Text != "written under v2" {
		t.Fatalf("after rebuild got %+v, want only the v2 row", hits)
	}

	if err := store.ClearReindexPending(ctx); err != nil {
		t.Fatalf("ClearReindexPending: %v", err)
	}
	if _, pending := store.ReindexPending(ctx); pending {
		t.Error("the marker survived being cleared")
	}
}

// A version this binary does not recognise is still a mismatch: the table is
// rebuilt rather than read with the wrong column meanings.
func TestStoreRebuildsOnVersionMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.db")
	db := openFileDB(t, path)
	ctx := context.Background()

	if _, err := NewStore(db); err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	sqlDB, _ := db.DB()
	if _, err := sqlDB.ExecContext(ctx,
		`UPDATE _local_meta SET value = ? WHERE key = ?`, "99", metaKeyVecSchemaVersion); err != nil {
		t.Fatalf("forge version: %v", err)
	}

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore after version drift: %v", err)
	}
	if store.RebuiltFrom() != "99" {
		t.Errorf("RebuiltFrom = %q, want \"99\"", store.RebuiltFrom())
	}
	var stored string
	if err := sqlDB.QueryRowContext(ctx,
		`SELECT value FROM _local_meta WHERE key = ?`, metaKeyVecSchemaVersion).Scan(&stored); err != nil {
		t.Fatalf("read version back: %v", err)
	}
	if stored != strconv.Itoa(vecSchemaVersion) {
		t.Errorf("stored version = %q, want the current one", stored)
	}
}

// The self-check writes rows. It must leave none behind, in a table that is not
// the user's.
func TestStoreSelfCheckLeavesNothingBehind(t *testing.T) {
	db := openFileDB(t, filepath.Join(t.TempDir(), "probe.db"))
	ctx := context.Background()
	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	sqlDB, _ := db.DB()

	var probes int
	if err := sqlDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE '%selfcheck%'`).Scan(&probes); err != nil {
		t.Fatalf("scan for probe leftovers: %v", err)
	}
	if probes != 0 {
		t.Errorf("%d self-check tables were left in the user's database", probes)
	}

	// The lexical half has to have actually been probed, not skipped: a
	// self-check that silently does nothing is worse than no self-check.
	if !store.FTSAvailable() {
		t.Error("the lexical self-check disabled the index it was meant to verify")
	}
	if hits, err := store.SearchLexical(ctx, 1, "薪资", 10); err != nil || len(hits) != 0 {
		t.Errorf("SearchLexical = (%d hits, %v); self-check rows leaked into the real index", len(hits), err)
	}

	// And no probe rows leaked into the real table under any identity.
	for _, uid := range []uint64{0, 1, 2, 1 << 62} {
		hits, err := store.Search(ctx, uid, basis(0), 10)
		if err != nil {
			t.Fatalf("Search as %d: %v", uid, err)
		}
		if len(hits) != 0 {
			t.Errorf("self-check rows visible as uid %d: %+v", uid, hits)
		}
	}
}
