//go:build desktop && cgo

package knowledge

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
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

// Chunk is a unit of indexed text plus its 384-dim, L2-normalized embedding
// and provenance. ChunkUID is the stable dedupe/upsert key.
type Chunk struct {
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
	Distance float64
}

// Store is the sqlite-vec backed vector store over knowledge chunks. It owns
// no state beyond the database handle and creates its vec0 table lazily on
// construction (see NewStore).
type Store struct {
	db *gorm.DB
}

// NewStore wraps the sidecar's GORM DB and ensures the vec0 virtual table
// exists. The table is created here (not via the migration runner) because
// sqlite-vec only registers once the binary imports this package — so the
// table must not be referenced by boot-time migrations until the store is
// wired in.
func NewStore(db *gorm.DB) (*Store, error) {
	s := &Store{db: db}
	if err := s.ensureSchema(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

// ensureSchema creates the vec0 virtual table if absent.
func (s *Store) ensureSchema(ctx context.Context) error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return fmt.Errorf("knowledge: get *sql.DB: %w", err)
	}
	// embedding float[384] + auxiliary metadata columns. The `+` prefix marks
	// stored columns retrievable in KNN results and usable in DELETE filters.
	stmt := fmt.Sprintf(
		"CREATE VIRTUAL TABLE IF NOT EXISTS %s USING vec0(embedding float[%d], +chunk_uid TEXT, +source_type TEXT, +source_id TEXT, +chunk_text TEXT)",
		chunksTable, EmbeddingDim)
	if _, err := sqlDB.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("knowledge: create vec0 table (is sqlite-vec registered?): %w", err)
	}
	return nil
}

func (s *Store) sqlDB() (*sql.DB, error) {
	return s.db.DB()
}

// UpsertChunks indexes chunks, replacing any existing chunks with the same
// ChunkUID (idempotent re-indexing). Returns the number of chunks written.
// Each embedding must be EmbeddingDim (384) dimensional.
func (s *Store) UpsertChunks(ctx context.Context, chunks []Chunk) (int, error) {
	if len(chunks) == 0 {
		return 0, nil
	}
	for i, c := range chunks {
		if len(c.Embedding) != EmbeddingDim {
			return 0, fmt.Errorf("knowledge: chunk %d has dim %d, want %d", i, len(c.Embedding), EmbeddingDim)
		}
		if strings.TrimSpace(c.ChunkUID) == "" {
			return 0, fmt.Errorf("knowledge: chunk %d has empty ChunkUID", i)
		}
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

	delStmt := fmt.Sprintf("DELETE FROM %s WHERE chunk_uid = ?", chunksTable)
	insStmt := fmt.Sprintf(
		"INSERT INTO %s(embedding, chunk_uid, source_type, source_id, chunk_text) VALUES (?, ?, ?, ?, ?)",
		chunksTable)

	for i, c := range chunks {
		if _, err := tx.ExecContext(ctx, delStmt, c.ChunkUID); err != nil {
			return i, fmt.Errorf("knowledge: delete chunk_uid=%s: %w", c.ChunkUID, err)
		}
		if _, err := tx.ExecContext(ctx, insStmt, packVec(c.Embedding), c.ChunkUID, c.SourceType, c.SourceID, c.Text); err != nil {
			return i, fmt.Errorf("knowledge: insert chunk_uid=%s: %w", c.ChunkUID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("knowledge: commit upsert: %w", err)
	}
	committed = true
	return len(chunks), nil
}

// Search returns the topK chunks nearest to queryVec by L2 distance (== cosine
// ranking for normalized embeddings).
func (s *Store) Search(ctx context.Context, queryVec []float32, topK int) ([]ChunkResult, error) {
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
	rows, err := sqlDB.QueryContext(ctx, fmt.Sprintf(
		"SELECT chunk_uid, source_type, source_id, chunk_text, distance FROM %s WHERE embedding MATCH ? ORDER BY distance LIMIT ?",
		chunksTable), packVec(queryVec), topK)
	if err != nil {
		return nil, fmt.Errorf("knowledge: knn search: %w", err)
	}
	defer rows.Close()

	var results []ChunkResult
	for rows.Next() {
		var r ChunkResult
		if err := rows.Scan(&r.ChunkUID, &r.SourceType, &r.SourceID, &r.Text, &r.Distance); err != nil {
			return nil, fmt.Errorf("knowledge: scan hit: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// DeleteBySource removes all chunks for a given source (e.g. a deleted file
// or message), returning the count removed. Used when a file is removed or
// re-uploaded (L3c-3) and to age out old messages.
func (s *Store) DeleteBySource(ctx context.Context, sourceType, sourceID string) (int, error) {
	sqlDB, err := s.sqlDB()
	if err != nil {
		return 0, err
	}
	res, err := sqlDB.ExecContext(ctx, fmt.Sprintf(
		"DELETE FROM %s WHERE source_type = ? AND source_id = ?", chunksTable), sourceType, sourceID)
	if err != nil {
		return 0, fmt.Errorf("knowledge: delete by source: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// ReplaceSource atomically replaces all chunks for a source: it deletes any
// existing chunks for (sourceType, sourceID) then inserts the given ones in
// the same transaction. Use when re-indexing a file whose contents may have
// changed (old chunk_uids would otherwise be orphaned). Returns the number of
// chunks inserted. An empty chunks slice just clears the source.
func (s *Store) ReplaceSource(ctx context.Context, sourceType, sourceID string, chunks []Chunk) (int, error) {
	for i, c := range chunks {
		if len(c.Embedding) != EmbeddingDim {
			return 0, fmt.Errorf("knowledge: chunk %d has dim %d, want %d", i, len(c.Embedding), EmbeddingDim)
		}
		if strings.TrimSpace(c.ChunkUID) == "" {
			return 0, fmt.Errorf("knowledge: chunk %d has empty ChunkUID", i)
		}
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

	if _, err := tx.ExecContext(ctx, fmt.Sprintf(
		"DELETE FROM %s WHERE source_type = ? AND source_id = ?", chunksTable), sourceType, sourceID); err != nil {
		return 0, fmt.Errorf("knowledge: replace delete: %w", err)
	}
	insStmt := fmt.Sprintf(
		"INSERT INTO %s(embedding, chunk_uid, source_type, source_id, chunk_text) VALUES (?, ?, ?, ?, ?)",
		chunksTable)
	for i, c := range chunks {
		if _, err := tx.ExecContext(ctx, insStmt,
			packVec(c.Embedding), c.ChunkUID, sourceType, sourceID, c.Text); err != nil {
			return i, fmt.Errorf("knowledge: replace insert chunk_uid=%s: %w", c.ChunkUID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("knowledge: commit replace: %w", err)
	}
	committed = true
	return len(chunks), nil
}

// packVec packs a float32 slice into the little-endian byte blob sqlite-vec
// expects for float[] columns.
func packVec(v []float32) []byte {
	buf := make([]byte, len(v)*int(unsafe.Sizeof(float32(0))))
	for i, x := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(x))
	}
	return buf
}
