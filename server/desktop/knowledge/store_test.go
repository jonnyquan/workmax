//go:build desktop && cgo

package knowledge

import (
	"context"
	"math"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// newTestStore opens an in-memory glebarez DB, pins it to a single connection
// (so the :memory: vec0 table persists across operations), and returns a Store.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("gorm open: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1) // keep :memory: on one connection
	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

func TestStoreUpsertSearchDelete(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	chunks := []Chunk{
		{ChunkUID: "c1", SourceType: SourceTypeFile, SourceID: "f1", Text: "cats sit on mats", Embedding: basis(0)},
		{ChunkUID: "c2", SourceType: SourceTypeFile, SourceID: "f1", Text: "dogs run in parks", Embedding: basis(1)},
		{ChunkUID: "c3", SourceType: SourceTypeFile, SourceID: "f2", Text: "kittens nap on rugs", Embedding: nearBasis(0)},
	}
	if n, err := store.UpsertChunks(ctx, chunks); err != nil {
		t.Fatalf("UpsertChunks: %v", err)
	} else if n != 3 {
		t.Fatalf("UpsertChunks wrote %d, want 3", n)
	}

	// KNN from basis(0): c1 (self), then c3 (near basis 0), then c2 (orthogonal).
	hits, err := store.Search(ctx, basis(0), 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("got %d hits, want 3", len(hits))
	}
	t.Logf("hits: c=%s d=%.4f | c=%s d=%.4f | c=%s d=%.4f",
		hits[0].ChunkUID, hits[0].Distance, hits[1].ChunkUID, hits[1].Distance, hits[2].ChunkUID, hits[2].Distance)
	if hits[0].ChunkUID != "c1" {
		t.Errorf("nearest = %s, want c1", hits[0].ChunkUID)
	}
	if hits[1].ChunkUID != "c3" {
		t.Errorf("second = %s, want c3", hits[1].ChunkUID)
	}
	// Self-match distance ~ 0; orthogonal ~ sqrt(2).
	if math.Abs(hits[0].Distance) > 1e-5 {
		t.Errorf("self distance = %v, want ~0", hits[0].Distance)
	}
	if hits[0].SourceType != SourceTypeFile || hits[0].SourceID != "f1" || hits[0].Text != "cats sit on mats" {
		t.Errorf("hit metadata not round-tripped: %+v", hits[0])
	}

	// Idempotent re-index: re-upserting c1 with new text replaces, no dup.
	chunks[0].Text = "cats love mats (updated)"
	if n, err := store.UpsertChunks(ctx, chunks); err != nil {
		t.Fatalf("re-UpsertChunks: %v", err)
	} else if n != 3 {
		t.Fatalf("re-UpsertChunks wrote %d, want 3", n)
	}
	hits, _ = store.Search(ctx, basis(0), 1)
	if len(hits) != 1 || hits[0].ChunkUID != "c1" || hits[0].Text != "cats love mats (updated)" {
		t.Fatalf("re-index did not replace c1: %+v", hits)
	}

	// Delete by source removes f1's two chunks; f2's c3 remains.
	if n, err := store.DeleteBySource(ctx, SourceTypeFile, "f1"); err != nil {
		t.Fatalf("DeleteBySource: %v", err)
	} else if n != 2 {
		t.Fatalf("deleted %d, want 2", n)
	}
	hits, _ = store.Search(ctx, basis(0), 5)
	if len(hits) != 1 || hits[0].ChunkUID != "c3" {
		t.Fatalf("expected only c3 after delete, got %+v", hits)
	}

	// topK cap respected.
	hits, _ = store.Search(ctx, basis(0), 0)
	if hits != nil {
		t.Errorf("topK=0 should error/empty, got %+v", hits)
	}
}

func TestStoreValidation(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Wrong embedding dimension.
	if _, err := store.UpsertChunks(ctx, []Chunk{
		{ChunkUID: "x", SourceType: SourceTypeFile, SourceID: "f", Text: "t", Embedding: []float32{1, 2, 3}},
	}); err == nil {
		t.Fatal("expected error for wrong dim")
	}
	// Empty ChunkUID.
	if _, err := store.UpsertChunks(ctx, []Chunk{
		{ChunkUID: " ", SourceType: SourceTypeFile, SourceID: "f", Text: "t", Embedding: basis(0)},
	}); err == nil {
		t.Fatal("expected error for empty ChunkUID")
	}
	// Wrong query dim.
	if _, err := store.Search(ctx, []float32{1, 2, 3}, 1); err == nil {
		t.Fatal("expected error for wrong query dim")
	}
}

// basis returns a 384-dim unit vector with 1 at idx.
func basis(idx int) []float32 {
	v := make([]float32, EmbeddingDim)
	v[idx] = 1
	return v
}

// nearBasis returns a normalized 384-dim vector close to basis(idx).
func nearBasis(idx int) []float32 {
	v := make([]float32, EmbeddingDim)
	v[idx] = 0.995
	v[(idx+1)%EmbeddingDim] = 0.1
	normalize(v)
	return v
}

func normalize(v []float32) {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	s = math.Sqrt(s)
	for i := range v {
		v[i] = float32(float64(v[i]) / s)
	}
}
