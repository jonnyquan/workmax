//go:build desktop && cgo

package knowledge

import (
	"context"
	"strings"
	"testing"
)

// Feeding a mind is the same pipeline as indexing a file, with a different
// provenance mark. This exercises it end to end on the fake vectorizer: the
// material becomes retrievable by the identity that fed it, its memory
// listing reads back what was fed, and a second identity on the same machine
// sees none of it.
func TestIndexer_MindFeedMarksAndRetrieves(t *testing.T) {
	db := openIndexerTestDB(t)
	kstore, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	idx := NewIndexer(nil, fakeVectorizer{}, kstore)
	ctx := context.Background()

	const mindID = "mind-de305d54-75b4-431b-adb2-eb6b9e546014"
	n, err := idx.IndexMindMaterial(ctx, indexerTestUID, mindID, "Compensation bands",
		"the 2026 compensation bands put L4 at one hundred eighty thousand")
	if err != nil {
		t.Fatalf("IndexMindMaterial: %v", err)
	}
	if n < 1 {
		t.Fatal("feeding a non-empty material must index at least one chunk")
	}

	sources, total, err := kstore.MindSources(ctx, indexerTestUID, mindID)
	if err != nil {
		t.Fatalf("MindSources: %v", err)
	}
	if total != n || len(sources) != 1 {
		t.Fatalf("memory listing = %+v total %d, want one material of %d chunks", sources, total, n)
	}
	if sources[0].Title != "Compensation bands" || sources[0].IndexedAt == 0 {
		t.Fatalf("the listing must carry the title and a write time: %+v", sources[0])
	}

	// Another mind of the same identity has its own, empty memory.
	otherSources, otherTotal, err := kstore.MindSources(ctx, indexerTestUID,
		"mind-11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatalf("MindSources other mind: %v", err)
	}
	if otherTotal != 0 || len(otherSources) != 0 {
		t.Fatalf("a mind must not see another mind's memory: %+v", otherSources)
	}

	// And another identity sees nothing of either.
	crossSources, crossTotal, err := kstore.MindSources(ctx, indexerTestUID+1, mindID)
	if err != nil {
		t.Fatalf("MindSources other identity: %v", err)
	}
	if crossTotal != 0 || len(crossSources) != 0 {
		t.Fatalf("uid isolation failed for mind memory: %+v", crossSources)
	}

	// The material participates in retrieval like any other knowledge, and is
	// labelled with the title it was fed under.
	hits, err := idx.Retrieve(ctx, indexerTestUID, "compensation bands", 5)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("fed material must be retrievable")
	}
	if hits[0].Label != "Compensation bands" {
		t.Fatalf("retrieval label = %q, want the material's title", hits[0].Label)
	}
}

// Re-feeding a title replaces it rather than piling on: ReplaceSource is the
// whole write path, so a corrected document never leaves its stale self
// behind in the mind's memory.
func TestIndexer_MindFeedReplacesSameTitle(t *testing.T) {
	db := openIndexerTestDB(t)
	kstore, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	idx := NewIndexer(nil, fakeVectorizer{}, kstore)
	ctx := context.Background()

	const mindID = "mind-de305d54-75b4-431b-adb2-eb6b9e546014"
	long := strings.Repeat("a paragraph of material. ", 200) // forces several chunks
	if _, err := idx.IndexMindMaterial(ctx, indexerTestUID, mindID, "Runbook", long); err != nil {
		t.Fatalf("first feed: %v", err)
	}
	if _, err := idx.IndexMindMaterial(ctx, indexerTestUID, mindID, "Runbook", "short runbook"); err != nil {
		t.Fatalf("second feed: %v", err)
	}

	sources, total, err := kstore.MindSources(ctx, indexerTestUID, mindID)
	if err != nil {
		t.Fatalf("MindSources: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("re-feeding a title must still be one material, got %+v", sources)
	}
	if total != sources[0].Chunks || total >= 10 {
		t.Fatalf("the replacement must not keep the long version's chunks: total %d", total)
	}
}
