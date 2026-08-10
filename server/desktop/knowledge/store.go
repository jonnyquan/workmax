//go:build desktop && cgo

package knowledge

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"gorm.io/gorm"
	_ "modernc.org/sqlite/vec" // auto-register sqlite-vec (vec0) on every modernc connection
)

// SourceTypeFile and SourceTypeMessage identify where a chunk originated.
const (
	SourceTypeFile    = "file"
	SourceTypeMessage = "message"
)

// chunksTable is the vec0 virtual table backing the knowledge store.
const chunksTable = "w_desktop_knowledge_chunk"

// selfCheckTable is the temp-schema twin of chunksTable used by the boot
// self-check. It lives in `temp` so a probe can insert, query and delete
// without any chance of touching a user's chunks, and it disappears with the
// connection even if the process dies mid-check.
const selfCheckTable = "temp.w_desktop_knowledge_selfcheck"

// vecSchemaVersion is the on-disk shape of the vec0 chunk table.
//
// It has its own version channel because a vec0 virtual table cannot take part
// in the SQL migration runner: sqlite-vec only exists once the binary imports
// the modernc /vec extension, so a boot-time .sql migration referencing this
// table would fail on any build without it (CGO_ENABLED=0 shells, the cloud
// server). The table is therefore created here, lazily — and before this
// version existed, that meant an old database silently kept running the old
// column layout forever, with no upgrade and no error. Bumping this constant
// is what makes a shape change actually reach an existing install.
//
// v1: all metadata stored as `+` auxiliary columns (unfilterable in KNN).
// v2: uid is a partition key; source_type/source_id/chunk_uid/created_at are
// metadata columns; only chunk_text stays auxiliary.
const vecSchemaVersion = 2

// Keys in _local_meta. The table is the sidecar's general-purpose key/value
// store (see migrations_desktop/0001), reused here rather than adding a
// migration for two rows.
const (
	metaKeyVecSchemaVersion  = "knowledge_vec_schema_version"
	metaKeyVecReindexPending = "knowledge_vec_reindex_pending"
)

// Chunk is a unit of indexed text plus its 384-dim, L2-normalized embedding
// and provenance. ChunkUID is the stable dedupe/upsert key; UID is the local
// identity that owns the chunk and the only thing standing between two local
// accounts on one machine.
type Chunk struct {
	UID        uint64
	ChunkUID   string
	SourceType string
	SourceID   string
	Text       string
	Embedding  []float32
}

// ChunkResult is a retrieval hit. Distance is the sqlite-vec L2 distance
// (lower = closer); because embeddings are L2-normalized, ranking by this
// distance is equivalent to ranking by cosine similarity.
type ChunkResult struct {
	Chunk
	Distance  float64
	CreatedAt int64 // unix seconds, when the chunk was written
}

// Store is the sqlite-vec backed vector store over knowledge chunks. It owns
// no state beyond the database handle and the outcome of the boot-time schema
// reconciliation (see NewStore).
type Store struct {
	db *gorm.DB

	// rebuiltFrom records the schema version this store dropped on
	// construction, empty when nothing was rebuilt. Reported so a boot log can
	// say why the index went empty instead of leaving it a mystery.
	rebuiltFrom string
}

// NewStore wraps the sidecar's GORM DB, reconciles the vec0 table with
// vecSchemaVersion, and self-checks the whole sqlite-vec code path before
// declaring the store usable.
//
// The self-check is not paranoia about SQLite. It is about this specific
// stack: sqlite-vec reaches Go through modernc's transpiled C, loaded by
// glebarez/go-sqlite. Nothing in `go build` verifies that a KNN query with a
// metadata filter still executes after either of those is upgraded, and the
// failure mode without a check is the worst one — retrieval quietly returning
// nothing while indexing appears to succeed. An error here disables RAG
// loudly, which is a state a user can act on.
func NewStore(db *gorm.DB) (*Store, error) {
	s := &Store{db: db}
	ctx := context.Background()
	if err := s.ensureSchema(ctx); err != nil {
		return nil, err
	}
	if err := s.selfCheck(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// RebuiltFrom reports the schema version dropped during construction (e.g.
// "1", or "unversioned" for a table predating the version channel). Empty
// means the existing table was already current or freshly created.
func (s *Store) RebuiltFrom() string { return s.rebuiltFrom }

// ReindexPending reports when the index was last rebuilt from scratch, if that
// rebuild has not been acknowledged. Everything indexed before that moment is
// gone; the marker is what lets the caller say so, and re-index.
func (s *Store) ReindexPending(ctx context.Context) (string, bool) {
	v, ok, err := s.readMeta(ctx, metaKeyVecReindexPending)
	if err != nil || !ok {
		return "", false
	}
	return v, true
}

// ClearReindexPending acknowledges the rebuild, after a backfill has restored
// what the rebuild dropped.
func (s *Store) ClearReindexPending(ctx context.Context) error {
	sqlDB, err := s.sqlDB()
	if err != nil {
		return err
	}
	_, err = sqlDB.ExecContext(ctx, `DELETE FROM _local_meta WHERE key = ?`, metaKeyVecReindexPending)
	return err
}

// vecTableDDL renders the vec0 CREATE for a table name.
//
// Column kinds are the whole point of this schema, and sqlite-vec spells them
// by prefix and position:
//
//   - `uid integer partition key` — a partition key physically shards the
//     index by owner. A KNN scan constrained to one uid never reads another
//     account's vector chunks at all, which is a stronger guarantee than
//     filtering results afterwards and the reason retrieval no longer has to
//     be switched off when a machine has two local accounts.
//   - plain columns (source_type, source_id, chunk_uid, created_at) are
//     *metadata* columns: usable in `WHERE col = ?` alongside a KNN MATCH, and
//     as the predicate of a DELETE.
//   - `+chunk_text` is auxiliary: returned with a hit, but invisible to any
//     filter. Auxiliary is right for the payload and wrong for everything
//     else — which is exactly what v1 got backwards.
//
// Measured, not assumed (modernc.org/sqlite v1.47 /vec via glebarez): both
// `uid integer partition key` and a plain `uid integer` metadata column accept
// the DDL, filter *during* the KNN scan rather than after it (a query for a
// sparse uid still returns that uid's rows even when 40 nearer rows belong to
// another uid), round-trip uid values up to 2^62 exactly, and support DELETE
// by metadata predicate without naming the partition key. The partition key
// won on isolation strength; if a future upgrade ever rejects it, dropping the
// two words "partition key" here plus a vecSchemaVersion bump is the whole
// fallback.
func vecTableDDL(table string) string {
	return fmt.Sprintf(
		"CREATE VIRTUAL TABLE %s USING vec0("+
			"embedding float[%d], "+
			"uid integer partition key, "+
			"source_type text, "+
			"source_id text, "+
			"chunk_uid text, "+
			"created_at integer, "+
			"+chunk_text text)",
		table, EmbeddingDim)
}

// ensureSchema brings the vec0 table to vecSchemaVersion, rebuilding it when
// the stored version disagrees.
//
// A rebuild is a drop: vec0 tables cannot be ALTERed, and there is no way to
// re-shard existing rows into a new column layout in place. That is acceptable
// only because every chunk is derived data — files on disk and messages in
// SQLite can be embedded again — so the rebuild leaves a marker saying the
// index is empty rather than pretending nothing happened.
//
// A table with no recorded version is treated as v1: that is precisely the
// pre-version-channel install, and the one this whole mechanism exists for.
func (s *Store) ensureSchema(ctx context.Context) error {
	if err := s.ensureLocalMeta(ctx); err != nil {
		return err
	}
	sqlDB, err := s.sqlDB()
	if err != nil {
		return err
	}

	exists, err := s.tableExists(ctx, chunksTable)
	if err != nil {
		return err
	}
	stored, hasStored, err := s.readMeta(ctx, metaKeyVecSchemaVersion)
	if err != nil {
		return err
	}
	current := strconv.Itoa(vecSchemaVersion)

	if exists && stored != current {
		from := stored
		if !hasStored {
			from = "unversioned"
		}
		if _, err := sqlDB.ExecContext(ctx, "DROP TABLE "+chunksTable); err != nil {
			return fmt.Errorf("knowledge: drop stale vec0 table (schema %s): %w", from, err)
		}
		exists = false
		s.rebuiltFrom = from
		if err := s.writeMeta(ctx, metaKeyVecReindexPending, time.Now().UTC().Format(time.RFC3339)); err != nil {
			return err
		}
	}

	if !exists {
		if _, err := sqlDB.ExecContext(ctx, vecTableDDL(chunksTable)); err != nil {
			return fmt.Errorf("knowledge: create vec0 table (is sqlite-vec registered?): %w", err)
		}
	}
	return s.writeMeta(ctx, metaKeyVecSchemaVersion, current)
}

// selfCheck runs the store's four load-bearing statement shapes — create,
// insert, uid-filtered KNN, metadata delete — against a throwaway temp table
// with the production DDL, and asserts the isolation property they exist for:
// a search as one uid must not see another uid's row.
func (s *Store) selfCheck(ctx context.Context) error {
	sqlDB, err := s.sqlDB()
	if err != nil {
		return err
	}
	_, _ = sqlDB.ExecContext(ctx, "DROP TABLE IF EXISTS "+selfCheckTable)
	if _, err := sqlDB.ExecContext(ctx, vecTableDDL(selfCheckTable)); err != nil {
		return fmt.Errorf("knowledge: vec0 self-check: create probe table: %w", err)
	}
	defer func() { _, _ = sqlDB.ExecContext(ctx, "DROP TABLE IF EXISTS "+selfCheckTable) }()

	probe := make([]float32, EmbeddingDim)
	probe[0] = 1
	ins := "INSERT INTO " + selfCheckTable +
		"(embedding, uid, source_type, source_id, chunk_uid, created_at, chunk_text) VALUES (?, ?, ?, ?, ?, ?, ?)"
	const (
		mineUID  = int64(1)
		otherUID = int64(2)
	)
	if _, err := sqlDB.ExecContext(ctx, ins, packVec(probe), mineUID, SourceTypeFile, "1", "self-check-mine", int64(0), "mine"); err != nil {
		return fmt.Errorf("knowledge: vec0 self-check: insert: %w", err)
	}
	if _, err := sqlDB.ExecContext(ctx, ins, packVec(probe), otherUID, SourceTypeFile, "2", "self-check-other", int64(0), "other"); err != nil {
		return fmt.Errorf("knowledge: vec0 self-check: insert (second identity): %w", err)
	}

	rows, err := sqlDB.QueryContext(ctx,
		"SELECT chunk_uid FROM "+selfCheckTable+" WHERE embedding MATCH ? AND uid = ? AND k = ? ORDER BY distance",
		packVec(probe), mineUID, 10)
	if err != nil {
		return fmt.Errorf("knowledge: vec0 self-check: uid-filtered knn: %w", err)
	}
	var seen []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("knowledge: vec0 self-check: scan: %w", err)
		}
		seen = append(seen, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("knowledge: vec0 self-check: knn rows: %w", err)
	}
	if len(seen) != 1 || seen[0] != "self-check-mine" {
		return fmt.Errorf("knowledge: vec0 self-check: uid filter is not partitioning the index (got %v); refusing to retrieve", seen)
	}

	res, err := sqlDB.ExecContext(ctx,
		"DELETE FROM "+selfCheckTable+" WHERE source_type = ? AND source_id = ?", SourceTypeFile, "1")
	if err != nil {
		return fmt.Errorf("knowledge: vec0 self-check: metadata delete: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("knowledge: vec0 self-check: metadata delete removed %d rows, want 1", n)
	}
	return nil
}

// ensureLocalMeta creates the sidecar key/value table if it is absent. The
// migration runner normally owns it, but the vec0 store is constructed outside
// that runner and its tests open bare databases; the DDL mirrors
// migrations_desktop/0001 exactly so the two can never diverge in shape.
func (s *Store) ensureLocalMeta(ctx context.Context) error {
	sqlDB, err := s.sqlDB()
	if err != nil {
		return err
	}
	_, err = sqlDB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS _local_meta (
    key         TEXT PRIMARY KEY,
    value       TEXT NOT NULL,
    updated_at  TEXT DEFAULT CURRENT_TIMESTAMP
)`)
	if err != nil {
		return fmt.Errorf("knowledge: ensure _local_meta: %w", err)
	}
	return nil
}

func (s *Store) tableExists(ctx context.Context, name string) (bool, error) {
	sqlDB, err := s.sqlDB()
	if err != nil {
		return false, err
	}
	var n int
	if err := sqlDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n); err != nil {
		return false, fmt.Errorf("knowledge: probe %s: %w", name, err)
	}
	return n > 0, nil
}

func (s *Store) readMeta(ctx context.Context, key string) (string, bool, error) {
	sqlDB, err := s.sqlDB()
	if err != nil {
		return "", false, err
	}
	var v string
	switch err := sqlDB.QueryRowContext(ctx, `SELECT value FROM _local_meta WHERE key = ?`, key).Scan(&v); {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("knowledge: read _local_meta[%s]: %w", key, err)
	}
	return v, true, nil
}

func (s *Store) writeMeta(ctx context.Context, key, value string) error {
	sqlDB, err := s.sqlDB()
	if err != nil {
		return err
	}
	_, err = sqlDB.ExecContext(ctx,
		`INSERT INTO _local_meta (key, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`,
		key, value)
	if err != nil {
		return fmt.Errorf("knowledge: write _local_meta[%s]: %w", key, err)
	}
	return nil
}

func (s *Store) sqlDB() (*sql.DB, error) {
	return s.db.DB()
}

// insertChunkSQL is the single INSERT shape used by both write paths.
func insertChunkSQL() string {
	return "INSERT INTO " + chunksTable +
		"(embedding, uid, source_type, source_id, chunk_uid, created_at, chunk_text) VALUES (?, ?, ?, ?, ?, ?, ?)"
}

// UpsertChunks indexes chunks, replacing any existing chunks with the same
// ChunkUID (idempotent re-indexing). Returns the number of chunks written.
// Each embedding must be EmbeddingDim (384) dimensional.
func (s *Store) UpsertChunks(ctx context.Context, chunks []Chunk) (int, error) {
	if len(chunks) == 0 {
		return 0, nil
	}
	if err := validateChunks(chunks); err != nil {
		return 0, err
	}

	sqlDB, err := s.sqlDB()
	if err != nil {
		return 0, err
	}
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("knowledge: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	delStmt := "DELETE FROM " + chunksTable + " WHERE chunk_uid = ?"
	insStmt := insertChunkSQL()
	now := time.Now().Unix()

	for i, c := range chunks {
		if _, err := tx.ExecContext(ctx, delStmt, c.ChunkUID); err != nil {
			return i, fmt.Errorf("knowledge: delete chunk_uid=%s: %w", c.ChunkUID, err)
		}
		if _, err := tx.ExecContext(ctx, insStmt,
			packVec(c.Embedding), signedUID(c.UID), c.SourceType, c.SourceID, c.ChunkUID, now, c.Text); err != nil {
			return i, fmt.Errorf("knowledge: insert chunk_uid=%s: %w", c.ChunkUID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("knowledge: commit upsert: %w", err)
	}
	committed = true
	return len(chunks), nil
}

// Search returns the topK chunks owned by uid that are nearest to queryVec by
// L2 distance (== cosine ranking for normalized embeddings).
//
// The uid predicate is part of the KNN scan, not a filter applied to its
// output: sqlite-vec prunes non-matching partitions before ranking, so a uid
// with few chunks still gets its own topK rather than losing every slot to a
// busier account on the same machine.
func (s *Store) Search(ctx context.Context, uid uint64, queryVec []float32, topK int) ([]ChunkResult, error) {
	if len(queryVec) != EmbeddingDim {
		return nil, fmt.Errorf("knowledge: query dim %d, want %d", len(queryVec), EmbeddingDim)
	}
	if topK <= 0 {
		return nil, errors.New("knowledge: topK must be > 0")
	}

	sqlDB, err := s.sqlDB()
	if err != nil {
		return nil, err
	}
	rows, err := sqlDB.QueryContext(ctx,
		"SELECT chunk_uid, source_type, source_id, chunk_text, created_at, distance FROM "+chunksTable+
			" WHERE embedding MATCH ? AND uid = ? AND k = ? ORDER BY distance",
		packVec(queryVec), signedUID(uid), topK)
	if err != nil {
		return nil, fmt.Errorf("knowledge: knn search: %w", err)
	}
	defer rows.Close()

	var results []ChunkResult
	for rows.Next() {
		var r ChunkResult
		if err := rows.Scan(&r.ChunkUID, &r.SourceType, &r.SourceID, &r.Text, &r.CreatedAt, &r.Distance); err != nil {
			return nil, fmt.Errorf("knowledge: scan hit: %w", err)
		}
		r.UID = uid
		results = append(results, r)
	}
	return results, rows.Err()
}

// DeleteBySource removes all chunks for a given source (e.g. a deleted file or
// message), returning the count removed. Used when a file is removed or
// re-uploaded (L3c-3) and to age out old messages.
//
// Deliberately not scoped by uid: source ids are globally unique on this
// machine (a file row id, a turn uuid), and a delete that missed chunks
// written under the user's previous identity would leave the text of a deleted
// file retrievable forever. Over-deleting here is impossible; under-deleting
// would be a privacy bug.
func (s *Store) DeleteBySource(ctx context.Context, sourceType, sourceID string) (int, error) {
	sqlDB, err := s.sqlDB()
	if err != nil {
		return 0, err
	}
	res, err := sqlDB.ExecContext(ctx,
		"DELETE FROM "+chunksTable+" WHERE source_type = ? AND source_id = ?", sourceType, sourceID)
	if err != nil {
		return 0, fmt.Errorf("knowledge: delete by source: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// ReplaceSource atomically replaces all chunks for a source: it deletes any
// existing chunks for (sourceType, sourceID) then inserts the given ones under
// uid in the same transaction. Use when re-indexing a file whose contents may
// have changed (old chunk_uids would otherwise be orphaned). Returns the number
// of chunks inserted. An empty chunks slice just clears the source.
func (s *Store) ReplaceSource(ctx context.Context, uid uint64, sourceType, sourceID string, chunks []Chunk) (int, error) {
	if err := validateChunks(chunks); err != nil {
		return 0, err
	}

	sqlDB, err := s.sqlDB()
	if err != nil {
		return 0, err
	}
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("knowledge: begin replace tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(ctx,
		"DELETE FROM "+chunksTable+" WHERE source_type = ? AND source_id = ?", sourceType, sourceID); err != nil {
		return 0, fmt.Errorf("knowledge: replace delete: %w", err)
	}
	insStmt := insertChunkSQL()
	now := time.Now().Unix()
	for i, c := range chunks {
		if _, err := tx.ExecContext(ctx, insStmt,
			packVec(c.Embedding), signedUID(uid), sourceType, sourceID, c.ChunkUID, now, c.Text); err != nil {
			return i, fmt.Errorf("knowledge: replace insert chunk_uid=%s: %w", c.ChunkUID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("knowledge: commit replace: %w", err)
	}
	committed = true
	return len(chunks), nil
}

func validateChunks(chunks []Chunk) error {
	for i, c := range chunks {
		if len(c.Embedding) != EmbeddingDim {
			return fmt.Errorf("knowledge: chunk %d has dim %d, want %d", i, len(c.Embedding), EmbeddingDim)
		}
		if strings.TrimSpace(c.ChunkUID) == "" {
			return fmt.Errorf("knowledge: chunk %d has empty ChunkUID", i)
		}
	}
	return nil
}

// signedUID reinterprets a uid as the int64 SQLite stores. The mapping is
// bit-for-bit and therefore injective, so equality filtering stays exact even
// for uids above 2^63 — which the local identity scheme (2^62 + n) never
// reaches, but which cost nothing to be correct about.
func signedUID(uid uint64) int64 { return int64(uid) }

// packVec packs a float32 slice into the little-endian byte blob sqlite-vec
// expects for float[] columns.
func packVec(v []float32) []byte {
	buf := make([]byte, len(v)*int(unsafe.Sizeof(float32(0))))
	for i, x := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(x))
	}
	return buf
}
