//go:build desktop && cgo

package knowledge

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	localrender "server/desktop/local_render"
	migrationsdesktop "server/desktop/migrations_desktop"
)

// openIndexerTestDB returns an in-memory, migrated DB pinned to one connection
// so the lazily-created vec0 table and w_workagent_thread_file coexist.
func openIndexerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if _, err := migrationsdesktop.Apply(db); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	return db
}

// fakeVectorizer embeds each text as a distinct basis vector by batch index,
// so a test can retrieve chunk i by querying basis(i) — no onnxruntime needed.
type fakeVectorizer struct{}

func (fakeVectorizer) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = basis(i % EmbeddingDim)
	}
	return out, nil
}

// indexerTestUID is the local user these tests index and retrieve as.
const indexerTestUID uint64 = 42

// Retrieval labels are what the user sees in place of "the model said so".
// This runs on the fake vectorizer, so unlike the real-embedder test below it
// executes everywhere, including CI without the native model.
func TestIndexer_RetrieveLabelsFilesAndScopesNamesToTheOwner(t *testing.T) {
	db := openIndexerTestDB(t)
	fileStore := localrender.NewStore(db, t.TempDir())
	kstore, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	idx := NewIndexer(fileStore, fakeVectorizer{}, kstore)
	ctx := context.Background()

	mine, err := fileStore.SaveThreadFile(indexerTestUID, 7, "thr-1", "q3-plan.txt", strings.NewReader("revenue grew twelve percent"))
	if err != nil {
		t.Fatalf("SaveThreadFile: %v", err)
	}
	if err := idx.IndexFile(ctx, indexerTestUID, mine.FileID); err != nil {
		t.Fatalf("IndexFile: %v", err)
	}

	hits, err := idx.Retrieve(ctx, indexerTestUID, "revenue", 5)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("no hits")
	}
	if hits[0].Kind != "file" {
		t.Errorf("kind = %q, want file", hits[0].Kind)
	}
	if hits[0].Label != "q3-plan.txt" {
		t.Errorf("label = %q, want the name the user uploaded", hits[0].Label)
	}

	// The same chunks, retrieved as the other local identity. The chunk store
	// has no uid column so the text is still found — but the name belongs to a
	// row this uid does not own, and showing it would attribute the passage to
	// a file this session cannot open.
	other, err := idx.Retrieve(ctx, indexerTestUID+1, "revenue", 5)
	if err != nil {
		t.Fatalf("Retrieve as other uid: %v", err)
	}
	if len(other) == 0 {
		t.Fatal("no hits for the other uid")
	}
	if other[0].Label != "Indexed file" {
		t.Errorf("label = %q, want the generic label when the file row is not this uid's", other[0].Label)
	}
}

func TestIndexer_IndexFileAndRemove(t *testing.T) {
	db := openIndexerTestDB(t)
	fileStore := localrender.NewStore(db, t.TempDir())
	kstore, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	idx := NewIndexer(fileStore, fakeVectorizer{}, kstore)

	// ~1.7 KiB of text → several chunks.
	content := strings.Repeat("the quick brown fox jumps over the lazy dog. ", 60)
	saved, err := fileStore.SaveThreadFile(42, 7, "thr-1", "notes.txt", strings.NewReader(content))
	if err != nil {
		t.Fatalf("SaveThreadFile: %v", err)
	}

	ctx := context.Background()
	if err := idx.IndexFile(ctx, 42, saved.FileID); err != nil {
		t.Fatalf("IndexFile: %v", err)
	}

	hits, err := kstore.Search(ctx, basis(0), 50)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("no chunks indexed")
	}
	t.Logf("indexed %d chunks; nearest uid=%s d=%.4f", len(hits), hits[0].ChunkUID, hits[0].Distance)
	wantUID := fmt.Sprintf("file:%d:0", saved.FileID)
	if hits[0].ChunkUID != wantUID {
		t.Errorf("nearest = %s, want %s", hits[0].ChunkUID, wantUID)
	}
	if hits[0].SourceType != SourceTypeFile || hits[0].SourceID != fileSourceID(saved.FileID) {
		t.Errorf("source metadata wrong: %+v", hits[0])
	}
	if strings.TrimSpace(hits[0].Text) == "" {
		t.Error("chunk text is empty")
	}
	count := len(hits)

	// Re-indexing replaces (idempotent): same count, no duplicates.
	if err := idx.IndexFile(ctx, 42, saved.FileID); err != nil {
		t.Fatalf("re-IndexFile: %v", err)
	}
	hits2, _ := kstore.Search(ctx, basis(0), 50)
	if len(hits2) != count {
		t.Errorf("re-index changed count: %d -> %d", count, len(hits2))
	}

	// RemoveFile clears the file's chunks.
	n, err := idx.RemoveFile(ctx, saved.FileID)
	if err != nil {
		t.Fatalf("RemoveFile: %v", err)
	}
	if n == 0 {
		t.Error("RemoveFile removed 0 chunks")
	}
	hits3, _ := kstore.Search(ctx, basis(0), 50)
	if len(hits3) != 0 {
		t.Errorf("expected 0 chunks after remove, got %d", len(hits3))
	}
}

// TestIndexer_SkipsNonText verifies image/empty files are not indexed (and
// clear any stale chunks rather than erroring).
func TestIndexer_SkipsNonText(t *testing.T) {
	db := openIndexerTestDB(t)
	fileStore := localrender.NewStore(db, t.TempDir())
	kstore, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	idx := NewIndexer(fileStore, fakeVectorizer{}, kstore)

	// Empty text file: nothing to index, no error.
	saved, err := fileStore.SaveThreadFile(1, 1, "thr", "empty.txt", strings.NewReader("   "))
	if err != nil {
		t.Fatalf("SaveThreadFile: %v", err)
	}
	if err := idx.IndexFile(context.Background(), 1, saved.FileID); err != nil {
		t.Fatalf("IndexFile empty: %v", err)
	}
	hits, _ := kstore.Search(context.Background(), basis(0), 5)
	if len(hits) != 0 {
		t.Errorf("empty file should index nothing, got %d chunks", len(hits))
	}

	// Missing file (bad id) is a hard error, not a silent skip.
	if err := idx.IndexFile(context.Background(), 1, 999999); err == nil {
		t.Error("IndexFile on missing id should error")
	}
}

// TestIndexer_RealEmbedder is the full L3c-3 pipeline e2e: real MiniLM
// embeddings through the indexer into the store, then semantic retrieval.
// Skipped without the native onnxruntime resources.
func TestIndexer_RealEmbedder(t *testing.T) {
	res, err := ResolveResources()
	if err != nil {
		t.Skipf("native embedding resources unavailable: %v", err)
	}
	emb, err := NewEmbedder(res)
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}
	defer emb.Close()

	db := openIndexerTestDB(t)
	fileStore := localrender.NewStore(db, t.TempDir())
	kstore, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	idx := NewIndexer(fileStore, emb, kstore)

	content := "Cats are small carnivorous mammals and popular household pets.\n\n" +
		"Cats sleep up to sixteen hours per day and are most active at dawn and dusk.\n\n" +
		"The domestic cat has excellent hearing and a powerful sense of smell."
	saved, err := fileStore.SaveThreadFile(1, 1, "thr", "cats.txt", strings.NewReader(content))
	if err != nil {
		t.Fatalf("SaveThreadFile: %v", err)
	}
	ctx := context.Background()
	if err := idx.IndexFile(ctx, 1, saved.FileID); err != nil {
		t.Fatalf("IndexFile: %v", err)
	}

	q, err := emb.Embed(ctx, "How long do cats sleep each day?")
	if err != nil {
		t.Fatalf("Embed query: %v", err)
	}
	hits, err := kstore.Search(ctx, q, 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("no retrieval hits")
	}
	t.Logf("top hit: %q d=%.4f", hits[0].Text, hits[0].Distance)
	low := strings.ToLower(hits[0].Text)
	if !strings.Contains(low, "cat") && !strings.Contains(low, "sleep") {
		t.Errorf("top hit not about cats/sleep: %q", hits[0].Text)
	}
}

func TestIndexer_IndexTurnAndRemove(t *testing.T) {
	db := openIndexerTestDB(t)
	kstore, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	// files is unused by IndexTurn; pass nil.
	idx := NewIndexer(nil, fakeVectorizer{}, kstore)
	ctx := context.Background()

	userText := "What is retrieval-augmented generation?"
	aiText := strings.Repeat("RAG combines retrieval with generation. ", 40) // multi-chunk
	if err := idx.IndexTurn(ctx, "turn-1", userText, aiText); err != nil {
		t.Fatalf("IndexTurn: %v", err)
	}
	hits, _ := kstore.Search(ctx, basis(0), 10)
	if len(hits) == 0 {
		t.Fatal("no turns indexed")
	}
	if hits[0].SourceType != SourceTypeMessage {
		t.Errorf("source_type = %s, want %s", hits[0].SourceType, SourceTypeMessage)
	}
	if hits[0].SourceID != "turn:turn-1" {
		t.Errorf("source_id = %s, want turn:turn-1", hits[0].SourceID)
	}
	count := len(hits)

	// Re-index is idempotent (replace): same count, no duplicates.
	if err := idx.IndexTurn(ctx, "turn-1", userText, aiText); err != nil {
		t.Fatalf("re-IndexTurn: %v", err)
	}
	hits2, _ := kstore.Search(ctx, basis(0), 10)
	if len(hits2) != count {
		t.Errorf("re-index changed count: %d -> %d", count, len(hits2))
	}

	// Empty turn clears the source's chunks.
	if err := idx.IndexTurn(ctx, "turn-1", "  ", ""); err != nil {
		t.Fatalf("empty IndexTurn: %v", err)
	}
	hits3, _ := kstore.Search(ctx, basis(0), 10)
	if len(hits3) != 0 {
		t.Errorf("empty turn should clear chunks, got %d", len(hits3))
	}

	// RemoveTurn drops a turn's chunks.
	if err := idx.IndexTurn(ctx, "turn-2", "q", "meaningful answer text"); err != nil {
		t.Fatalf("IndexTurn turn-2: %v", err)
	}
	n, err := idx.RemoveTurn(ctx, "turn-2")
	if err != nil {
		t.Fatalf("RemoveTurn: %v", err)
	}
	if n == 0 {
		t.Error("RemoveTurn removed 0 chunks")
	}
}

// TestIndexer_IndexTurn_RealEmbedder is the L3c-4 e2e: a real conversation
// turn indexed with MiniLM, then semantic retrieval. Skipped without resources.
func TestIndexer_IndexTurn_RealEmbedder(t *testing.T) {
	res, err := ResolveResources()
	if err != nil {
		t.Skipf("native embedding resources unavailable: %v", err)
	}
	emb, err := NewEmbedder(res)
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}
	defer emb.Close()

	db := openIndexerTestDB(t)
	kstore, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	idx := NewIndexer(nil, emb, kstore)
	ctx := context.Background()

	if err := idx.IndexTurn(ctx, "turn-sd",
		"How do I bake sourdough bread at home?",
		"To bake sourdough: feed the starter until active, mix flour water and salt, "+
			"bulk ferment for a few hours, shape the loaf, cold proof overnight, "+
			"then bake in a preheated dutch oven at 230C with the lid on."); err != nil {
		t.Fatalf("IndexTurn: %v", err)
	}

	q, err := emb.Embed(ctx, "sourdough bread baking instructions")
	if err != nil {
		t.Fatalf("Embed query: %v", err)
	}
	hits, err := kstore.Search(ctx, q, 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("no retrieval hits")
	}
	t.Logf("top hit: %q d=%.4f", hits[0].Text, hits[0].Distance)
	low := strings.ToLower(hits[0].Text)
	if !strings.Contains(low, "sourdough") && !strings.Contains(low, "bread") && !strings.Contains(low, "bake") {
		t.Errorf("top hit not about sourdough baking: %q", hits[0].Text)
	}
}

// TestIndexer_Retrieve_RealEmbedder is the L3c-5 retrieval e2e: index two
// distinct topics, then retrieve the one matching the query first. Skipped
// without onnxruntime resources.
func TestIndexer_Retrieve_RealEmbedder(t *testing.T) {
	res, err := ResolveResources()
	if err != nil {
		t.Skipf("native embedding resources unavailable: %v", err)
	}
	emb, err := NewEmbedder(res)
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}
	defer emb.Close()

	db := openIndexerTestDB(t)
	kstore, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	idx := NewIndexer(nil, emb, kstore)
	ctx := context.Background()

	if err := idx.IndexTurn(ctx, "turn-cats", "tell me about cats",
		"Cats are carnivorous mammals that sleep up to sixteen hours per day."); err != nil {
		t.Fatalf("index cats: %v", err)
	}
	if err := idx.IndexTurn(ctx, "turn-python", "how do I read a file in python",
		"In Python, use open() to open a file then call read() to get its contents."); err != nil {
		t.Fatalf("index python: %v", err)
	}

	chunks, err := idx.Retrieve(ctx, indexerTestUID, "how many hours do cats sleep each day?", 2)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("no chunks retrieved")
	}
	t.Logf("retrieved top: %q (%s, score %.3f)", chunks[0].Text, chunks[0].Label, chunks[0].Score)
	if !strings.Contains(strings.ToLower(chunks[0].Text), "cat") {
		t.Errorf("top retrieved chunk not about cats: %q", chunks[0].Text)
	}
	// Provenance is the point of the return type: a hit the caller cannot
	// attribute is a hit the user cannot check.
	if chunks[0].Kind != "conversation" {
		t.Errorf("kind = %q, want conversation for an indexed turn", chunks[0].Kind)
	}
	if chunks[0].Score <= 0 || chunks[0].Score > 1 {
		t.Errorf("score = %v, want a similarity in (0,1]", chunks[0].Score)
	}
	if len(chunks) > 1 && chunks[1].Score > chunks[0].Score {
		t.Errorf("scores not ordered best-first: %v then %v", chunks[0].Score, chunks[1].Score)
	}
}

// The store returns L2 distance; the UI shows similarity. Getting the
// conversion backwards would rank every source upside down while still
// looking plausible.
func TestSimilarityFromDistance(t *testing.T) {
	cases := []struct {
		name     string
		distance float64
		want     float64
	}{
		{"identical vectors", 0, 1},
		{"orthogonal", math.Sqrt2, 0},
		{"opposite", 2, 0}, // -1 cosine, clamped
	}
	for _, c := range cases {
		if got := similarityFromDistance(c.distance); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: similarityFromDistance(%v) = %v, want %v", c.name, c.distance, got, c.want)
		}
	}
	near, far := similarityFromDistance(0.4), similarityFromDistance(1.2)
	if near <= far {
		t.Errorf("a closer hit scored %v, no better than a distant one at %v", near, far)
	}
}
