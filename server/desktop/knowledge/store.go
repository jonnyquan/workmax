//go:build desktop && cgo

package knowledge

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"sync"
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

// ftsTable is the FTS5 shadow index over the same chunks — the lexical half of
// hybrid retrieval. It carries the chunk's payload and provenance as
// UNINDEXED columns so a lexical hit is a complete result without a second
// lookup, and one indexed column (`body`) holding the segmented term stream
// (see fts.go for why the text is segmented before it gets here).
//
// It is created here rather than in migrations_desktop for one reason only:
// consistency with the vec0 table it shadows. The two are written in the same
// transaction and must be dropped and rebuilt together — a chunk present in
// one and absent from the other is a silent retrieval bug — so they share one
// version channel (vecSchemaVersion) and one boot self-check. Splitting them
// across two schema mechanisms would make "are these two indexes in step" an
// unanswerable question.
const ftsTable = "w_desktop_knowledge_fts"

// selfCheckTable is the temp-schema twin of chunksTable used by the boot
// self-check. It lives in `temp` so a probe can insert, query and delete
// without any chance of touching a user's chunks, and it disappears with the
// connection even if the process dies mid-check.
const selfCheckTable = "temp.w_desktop_knowledge_selfcheck"

// ftsSelfCheckTable is the same idea for the lexical index, and
// ftsSelfCheckName is its bare name. Both are needed because FTS5's MATCH
// operator and bm25() take the table's *name*, not a schema-qualified
// reference: `FROM temp.x WHERE temp.x MATCH ?` is a "no such column" error,
// while `FROM temp.x WHERE x MATCH ?` resolves against the FROM clause.
const (
	ftsSelfCheckName  = "w_desktop_knowledge_fts_selfcheck"
	ftsSelfCheckTable = "temp." + ftsSelfCheckName
)

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
// v3: an FTS5 shadow table joins the vec0 table under this same version. The
// bump is load-bearing rather than cosmetic: existing chunks have no lexical
// rows, and a half-populated FTS index would make hybrid retrieval worse than
// no hybrid retrieval — it would rank the handful of newly-written chunks
// above everything indexed before the upgrade. Dropping both and re-indexing
// is the only state that is honest.
const vecSchemaVersion = 3

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

	// ftsMu guards ftsReady, which is cleared the moment any lexical statement
	// fails. Hybrid retrieval degrades to pure vector retrieval rather than
	// failing: FTS5 is an improvement on the vector index, and an improvement
	// that can take retrieval down with it is not one. Every read of this flag
	// is a decision about whether to attempt lexical work, never about whether
	// to return results.
	ftsMu    sync.Mutex
	ftsReady bool
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

// ftsTableDDL renders the FTS5 CREATE for a table name.
//
// `tokenize='unicode61'` is the default tokenizer and the deliberate choice:
// the CJK segmentation happens in Go before the text arrives (fts.go explains
// why `trigram`, the obvious alternative, was measured and rejected). Every
// column except `body` is UNINDEXED — they are payload, and indexing a uid or
// a source id as searchable text would let a query match on provenance.
//
// The table owns its content rather than running in FTS5's contentless mode:
// a contentless table cannot be DELETEd from, measured — "cannot DELETE from
// contentless fts5 table" — and every write path here replaces a source's
// rows.
func ftsTableDDL(table string) string {
	return fmt.Sprintf(
		"CREATE VIRTUAL TABLE %s USING fts5("+
			"chunk_uid UNINDEXED, "+
			"uid UNINDEXED, "+
			"source_type UNINDEXED, "+
			"source_id UNINDEXED, "+
			"chunk_text UNINDEXED, "+
			"created_at UNINDEXED, "+
			"body, "+
			"tokenize='unicode61')",
		table)
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
		// The lexical shadow goes with it, unconditionally. The two indexes
		// describe the same chunks; keeping one across a rebuild of the other
		// is how a search starts returning text that no longer exists.
		if _, err := sqlDB.ExecContext(ctx, "DROP TABLE IF EXISTS "+ftsTable); err != nil {
			return fmt.Errorf("knowledge: drop stale fts5 table (schema %s): %w", from, err)
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

	// The lexical half is optional in a way the vector half is not: a build
	// whose SQLite lacks FTS5 must still retrieve. So a creation failure is
	// logged and disables hybrid search, rather than refusing the store.
	ftsExists, err := s.tableExists(ctx, ftsTable)
	if err != nil {
		return err
	}
	if !ftsExists {
		if _, err := sqlDB.ExecContext(ctx, ftsTableDDL(ftsTable)); err != nil {
			log.Printf("knowledge: FTS5 shadow index unavailable, retrieval stays vector-only: %v", err)
			return s.writeMeta(ctx, metaKeyVecSchemaVersion, current)
		}
	}
	s.setFTSReady(true)
	return s.writeMeta(ctx, metaKeyVecSchemaVersion, current)
}

// setFTSReady records whether lexical statements are worth attempting.
func (s *Store) setFTSReady(ok bool) {
	s.ftsMu.Lock()
	s.ftsReady = ok
	s.ftsMu.Unlock()
}

// FTSAvailable reports whether hybrid retrieval has its lexical half. False
// means every search is pure vector KNN — a supported, degraded mode, and one
// the diagnostics snapshot surfaces so it is not invisible.
func (s *Store) FTSAvailable() bool {
	s.ftsMu.Lock()
	defer s.ftsMu.Unlock()
	return s.ftsReady
}

// ftsFailed turns off the lexical half after a statement failed at runtime
// (the table was dropped underneath us, the extension went away on an
// upgrade). Logged once per transition, not once per statement.
func (s *Store) ftsFailed(op string, err error) {
	s.ftsMu.Lock()
	was := s.ftsReady
	s.ftsReady = false
	s.ftsMu.Unlock()
	if was {
		log.Printf("knowledge: lexical index disabled after %s failed; retrieval continues vector-only: %v", op, err)
	}
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
	return s.selfCheckFTS(ctx)
}

// selfCheckFTS proves the lexical half end to end on a throwaway table:
// create, insert, a CJK MATCH with a uid filter and a bm25 ordering, then a
// delete by provenance.
//
// The CJK assertion is the point. A build where FTS5 exists but the
// segmentation stopped matching Chinese would look completely healthy — every
// English query would still work — while quietly halving retrieval quality for
// the language most of this product's text is in. So the probe searches for a
// two-character word inside an unbroken Chinese sentence, which is exactly the
// case plain unicode61 cannot do and the segmentation exists to fix.
//
// A failure here disables hybrid retrieval; it does not fail the store.
func (s *Store) selfCheckFTS(ctx context.Context) error {
	if !s.FTSAvailable() {
		return nil
	}
	sqlDB, err := s.sqlDB()
	if err != nil {
		return err
	}
	fail := func(stage string, err error) error {
		s.ftsFailed("self-check ("+stage+")", err)
		return nil
	}

	_, _ = sqlDB.ExecContext(ctx, "DROP TABLE IF EXISTS "+ftsSelfCheckTable)
	if _, err := sqlDB.ExecContext(ctx, ftsTableDDL(ftsSelfCheckTable)); err != nil {
		return fail("create probe table", err)
	}
	defer func() { _, _ = sqlDB.ExecContext(ctx, "DROP TABLE IF EXISTS "+ftsSelfCheckTable) }()

	const probeText = "本季度的薪资结构调整方案 hybrid retrieval"
	if _, err := sqlDB.ExecContext(ctx, insertFTSSQL(ftsSelfCheckTable),
		"self-check-fts", int64(1), SourceTypeFile, "1", probeText, int64(0), segmentForIndex(probeText)); err != nil {
		return fail("insert", err)
	}
	if _, err := sqlDB.ExecContext(ctx, insertFTSSQL(ftsSelfCheckTable),
		"self-check-fts-other", int64(2), SourceTypeFile, "2", probeText, int64(0), segmentForIndex(probeText)); err != nil {
		return fail("insert (second identity)", err)
	}

	rows, err := sqlDB.QueryContext(ctx,
		"SELECT chunk_uid FROM "+ftsSelfCheckTable+" WHERE "+ftsSelfCheckName+
			" MATCH ? AND uid = ? ORDER BY bm25("+ftsSelfCheckName+") LIMIT 10",
		ftsMatchExpr("薪资"), int64(1))
	if err != nil {
		return fail("cjk match", err)
	}
	var seen []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fail("scan", err)
		}
		seen = append(seen, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fail("cjk match rows", err)
	}
	if len(seen) != 1 || seen[0] != "self-check-fts" {
		return fail("cjk match", fmt.Errorf(
			"a two-character Chinese query inside an unbroken sentence returned %v, want exactly the owning identity's row", seen))
	}

	res, err := sqlDB.ExecContext(ctx,
		"DELETE FROM "+ftsSelfCheckTable+" WHERE source_type = ? AND source_id = ?", SourceTypeFile, "1")
	if err != nil {
		return fail("delete by provenance", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fail("delete by provenance", fmt.Errorf("removed %d rows, want 1", n))
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

// insertFTSSQL is the matching INSERT for the lexical shadow.
func insertFTSSQL(table string) string {
	return "INSERT INTO " + table +
		"(chunk_uid, uid, source_type, source_id, chunk_text, created_at, body) VALUES (?, ?, ?, ?, ?, ?, ?)"
}

// execFTS runs one lexical statement, and treats a failure as "the lexical
// half is gone" rather than as a failure of the operation it was part of.
//
// This is what makes FTS strictly additive. A write that succeeded in the
// vector index and failed in the shadow must still be a successful write:
// otherwise adding hybrid retrieval would have introduced a new way for
// indexing a file to fail, which is a worse outcome than searching without
// bm25. The chunk stays vector-retrievable, and the next schema bump rebuilds
// both indexes together.
func (s *Store) execFTS(ctx context.Context, db execer, op, query string, args ...any) {
	if !s.FTSAvailable() {
		return
	}
	if _, err := db.ExecContext(ctx, query, args...); err != nil {
		s.ftsFailed(op, err)
	}
}

// execer is the part of *sql.DB and *sql.Tx the lexical writes need, so the
// same helper serves the transactional and non-transactional paths.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
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
	ftsDelStmt := "DELETE FROM " + ftsTable + " WHERE chunk_uid = ?"
	ftsInsStmt := insertFTSSQL(ftsTable)
	now := time.Now().Unix()

	for i, c := range chunks {
		if _, err := tx.ExecContext(ctx, delStmt, c.ChunkUID); err != nil {
			return i, fmt.Errorf("knowledge: delete chunk_uid=%s: %w", c.ChunkUID, err)
		}
		if _, err := tx.ExecContext(ctx, insStmt,
			packVec(c.Embedding), signedUID(c.UID), c.SourceType, c.SourceID, c.ChunkUID, now, c.Text); err != nil {
			return i, fmt.Errorf("knowledge: insert chunk_uid=%s: %w", c.ChunkUID, err)
		}
		s.execFTS(ctx, tx, "upsert delete", ftsDelStmt, c.ChunkUID)
		s.execFTS(ctx, tx, "upsert insert", ftsInsStmt,
			c.ChunkUID, signedUID(c.UID), c.SourceType, c.SourceID, c.Text, now, segmentForIndex(c.Text))
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

// LexicalResult is one bm25 hit from the FTS5 shadow index. Body is the
// segmented term stream that was actually matched — carried back so the
// caller can measure how much of the query is present without re-deriving it
// from the display text.
type LexicalResult struct {
	ChunkResult
	Body string
}

// SearchLexical returns the topK chunks owned by uid whose text best matches
// query by bm25, best first. It returns (nil, nil) when the lexical index is
// unavailable or the query segments to nothing — both mean "no lexical
// opinion", which the caller folds into a vector-only result.
//
// The uid predicate here is an ordinary WHERE on an UNINDEXED column, not the
// partition-level pruning the vec0 table gets. It is applied by SQLite before
// LIMIT, so the isolation is exact; what it does not get is the guarantee that
// a busy account cannot crowd out a quiet one's slots. That asymmetry is
// acceptable because the lexical half is a candidate generator feeding a
// fusion step, not the final ranking — and it is asserted in the boot
// self-check rather than assumed.
func (s *Store) SearchLexical(ctx context.Context, uid uint64, query string, topK int) ([]LexicalResult, error) {
	if !s.FTSAvailable() || topK <= 0 {
		return nil, nil
	}
	expr := ftsMatchExpr(query)
	if expr == "" {
		return nil, nil
	}
	sqlDB, err := s.sqlDB()
	if err != nil {
		return nil, err
	}
	rows, err := sqlDB.QueryContext(ctx,
		"SELECT chunk_uid, source_type, source_id, chunk_text, created_at, body FROM "+ftsTable+
			" WHERE "+ftsTable+" MATCH ? AND uid = ? ORDER BY bm25("+ftsTable+") LIMIT ?",
		expr, signedUID(uid), topK)
	if err != nil {
		s.ftsFailed("lexical search", err)
		return nil, nil
	}
	defer rows.Close()

	var out []LexicalResult
	for rows.Next() {
		var r LexicalResult
		if err := rows.Scan(&r.ChunkUID, &r.SourceType, &r.SourceID, &r.Text, &r.CreatedAt, &r.Body); err != nil {
			s.ftsFailed("lexical scan", err)
			return nil, nil
		}
		r.UID = uid
		// A lexical hit has no distance of its own; DistancesFor fills it in
		// for the ones that survive fusion. Until then it is deliberately the
		// worst possible value rather than a plausible-looking zero.
		r.Distance = maxL2Distance
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		s.ftsFailed("lexical rows", err)
		return nil, nil
	}
	return out, nil
}

// maxL2Distance is the largest L2 distance between two unit vectors (opposite
// directions), i.e. cosine similarity 0 after the clamp in
// similarityFromDistance.
const maxL2Distance = 2.0

// DistancesFor returns the L2 distance from queryVec to each named chunk.
//
// It exists so a chunk found only by the lexical index still reports a real
// similarity to the user instead of a placeholder. sqlite-vec evaluates
// vec_distance_L2 against stored rows directly — no KNN, no partition scan —
// so this is one statement over a handful of ids, measured working through
// glebarez/modernc with `chunk_uid IN (...)`.
//
// Missing ids are simply absent from the result: the chunk was deleted
// between the two queries, and a caller that treats absence as "no score" is
// already correct.
func (s *Store) DistancesFor(ctx context.Context, uid uint64, chunkUIDs []string, queryVec []float32) (map[string]float64, error) {
	if len(chunkUIDs) == 0 {
		return nil, nil
	}
	if len(queryVec) != EmbeddingDim {
		return nil, fmt.Errorf("knowledge: query dim %d, want %d", len(queryVec), EmbeddingDim)
	}
	sqlDB, err := s.sqlDB()
	if err != nil {
		return nil, err
	}
	args := make([]any, 0, len(chunkUIDs)+2)
	args = append(args, packVec(queryVec))
	placeholders := make([]string, len(chunkUIDs))
	for i, id := range chunkUIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	args = append(args, signedUID(uid))
	rows, err := sqlDB.QueryContext(ctx,
		"SELECT chunk_uid, vec_distance_L2(embedding, ?) FROM "+chunksTable+
			" WHERE chunk_uid IN ("+strings.Join(placeholders, ", ")+") AND uid = ?", args...)
	if err != nil {
		return nil, fmt.Errorf("knowledge: distances for lexical hits: %w", err)
	}
	defer rows.Close()
	out := make(map[string]float64, len(chunkUIDs))
	for rows.Next() {
		var id string
		var d float64
		if err := rows.Scan(&id, &d); err != nil {
			return nil, fmt.Errorf("knowledge: scan distance: %w", err)
		}
		out[id] = d
	}
	return out, rows.Err()
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
	s.execFTS(ctx, sqlDB, "delete by source",
		"DELETE FROM "+ftsTable+" WHERE source_type = ? AND source_id = ?", sourceType, sourceID)
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
	s.execFTS(ctx, tx, "replace delete",
		"DELETE FROM "+ftsTable+" WHERE source_type = ? AND source_id = ?", sourceType, sourceID)
	insStmt := insertChunkSQL()
	ftsInsStmt := insertFTSSQL(ftsTable)
	now := time.Now().Unix()
	for i, c := range chunks {
		if _, err := tx.ExecContext(ctx, insStmt,
			packVec(c.Embedding), signedUID(uid), sourceType, sourceID, c.ChunkUID, now, c.Text); err != nil {
			return i, fmt.Errorf("knowledge: replace insert chunk_uid=%s: %w", c.ChunkUID, err)
		}
		s.execFTS(ctx, tx, "replace insert", ftsInsStmt,
			c.ChunkUID, signedUID(uid), sourceType, sourceID, c.Text, now, segmentForIndex(c.Text))
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
