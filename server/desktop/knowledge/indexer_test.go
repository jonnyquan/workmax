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

// Two local accounts on one machine must not read each other's knowledge. This
// is the property the vec0 uid partition key exists for, and the reason Phase
// 0's blunt stopgap — switch retrieval off entirely whenever a second local
// account exists — could be removed. Asserted end to end through the indexer,
// on the fake vectorizer, so it runs in CI without the native model.
func TestIndexer_RetrievalIsPartitionedByUID(t *testing.T) {
	db := openIndexerTestDB(t)
	fileStore := localrender.NewStore(db, t.TempDir())
	kstore, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	idx := NewIndexer(fileStore, fakeVectorizer{}, kstore)
	ctx := context.Background()

	const accountA, accountB = indexerTestUID, indexerTestUID + 1

	saved, err := fileStore.SaveThreadFile(accountA, 7, "thr-a", "salary.txt",
		strings.NewReader("alice earns one hundred thousand"))
	if err != nil {
		t.Fatalf("SaveThreadFile: %v", err)
	}
	if err := idx.IndexFile(ctx, accountA, saved.FileID); err != nil {
		t.Fatalf("IndexFile: %v", err)
	}
	if err := idx.IndexTurn(ctx, accountA, "turn-a", "what is my salary", "one hundred thousand"); err != nil {
		t.Fatalf("IndexTurn: %v", err)
	}

	mine, err := idx.Retrieve(ctx, accountA, "", "salary", 5)
	if err != nil {
		t.Fatalf("Retrieve as owner: %v", err)
	}
	if len(mine) == 0 {
		t.Fatal("the account that indexed the content cannot retrieve it")
	}

	theirs, err := idx.Retrieve(ctx, accountB, "", "salary", 5)
	if err != nil {
		t.Fatalf("Retrieve as other account: %v", err)
	}
	if len(theirs) != 0 {
		t.Fatalf("account B retrieved account A's content: %+v", theirs)
	}

	// Deleting a source is deliberately not uid-scoped: source ids are unique
	// per machine, and text of a deleted file must not survive under any
	// identity.
	if n, err := idx.RemoveFile(ctx, saved.FileID); err != nil || n == 0 {
		t.Fatalf("RemoveFile = (%d, %v), want a positive count", n, err)
	}
	after, _ := idx.Retrieve(ctx, accountA, "", "salary", 5)
	for _, h := range after {
		if h.Kind == "file" {
			t.Fatalf("deleted file still retrievable: %+v", h)
		}
	}
}

// The migration story end to end: a schema bump drops every chunk, and the
// files the user uploaded come back on their own. Without this, bumping
// vecSchemaVersion would be a silent data loss dressed up as an upgrade.
func TestIndexer_ReindexAfterSchemaRebuild(t *testing.T) {
	db := openIndexerTestDB(t)
	fileStore := localrender.NewStore(db, t.TempDir())
	ctx := context.Background()

	const accountA, accountB = indexerTestUID, indexerTestUID + 1

	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	idx := NewIndexer(fileStore, fakeVectorizer{}, store)

	a, err := fileStore.SaveThreadFile(accountA, 1, "thr-a", "a.txt", strings.NewReader("alpha content here"))
	if err != nil {
		t.Fatalf("save a: %v", err)
	}
	b, err := fileStore.SaveThreadFile(accountB, 2, "thr-b", "b.txt", strings.NewReader("beta content here"))
	if err != nil {
		t.Fatalf("save b: %v", err)
	}
	if err := idx.IndexFile(ctx, accountA, a.FileID); err != nil {
		t.Fatalf("index a: %v", err)
	}
	if err := idx.IndexFile(ctx, accountB, b.FileID); err != nil {
		t.Fatalf("index b: %v", err)
	}

	// A version this binary does not know: the table is dropped and rebuilt.
	sqlDB, _ := db.DB()
	if _, err := sqlDB.ExecContext(ctx,
		`UPDATE _local_meta SET value = ? WHERE key = ?`, "99", metaKeyVecSchemaVersion); err != nil {
		t.Fatalf("forge version: %v", err)
	}
	rebuilt, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore after drift: %v", err)
	}
	if rebuilt.RebuiltFrom() == "" {
		t.Fatal("the store did not rebuild on a version mismatch")
	}
	idx2 := NewIndexer(fileStore, fakeVectorizer{}, rebuilt)
	if hits, _ := idx2.Retrieve(ctx, accountA, "", "alpha", 5); len(hits) != 0 {
		t.Fatalf("chunks survived a rebuild: %+v", hits)
	}

	n, err := idx2.ReindexPendingFiles(ctx)
	if err != nil {
		t.Fatalf("ReindexPendingFiles: %v", err)
	}
	if n != 2 {
		t.Fatalf("re-indexed %d files, want 2", n)
	}
	if _, pending := rebuilt.ReindexPending(ctx); pending {
		t.Error("a completed re-index must clear the marker")
	}

	// Restored, and restored to the right owners.
	for _, c := range []struct {
		uid   uint64
		query string
		label string
	}{{accountA, "alpha", "a.txt"}, {accountB, "beta", "b.txt"}} {
		hits, err := idx2.Retrieve(ctx, c.uid, "", c.query, 5)
		if err != nil {
			t.Fatalf("Retrieve as %d: %v", c.uid, err)
		}
		if len(hits) != 1 {
			t.Fatalf("uid %d got %d hits after re-index, want exactly its own file", c.uid, len(hits))
		}
		if hits[0].Label != c.label {
			t.Errorf("uid %d retrieved %q, want %q", c.uid, hits[0].Label, c.label)
		}
	}

	// Idempotent: nothing pending, nothing to do.
	if n, err := idx2.ReindexPendingFiles(ctx); err != nil || n != 0 {
		t.Errorf("second ReindexPendingFiles = (%d, %v), want (0, nil)", n, err)
	}
}

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

	hits, err := idx.Retrieve(ctx, indexerTestUID, "", "revenue", 5)
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

	// The generic label is the fallback for a hit whose file row has gone away
	// (deleted from disk, or written before the row existed). Retrieval already
	// succeeded; a missing name must cost the label, not the answer.
	if _, err := fileStore.DeleteThreadFiles(indexerTestUID, 7, "thr-1"); err != nil {
		t.Fatalf("DeleteThreadFiles: %v", err)
	}
	orphan, err := idx.Retrieve(ctx, indexerTestUID, "", "revenue", 5)
	if err != nil {
		t.Fatalf("Retrieve after the file row went away: %v", err)
	}
	if len(orphan) == 0 {
		t.Fatal("no hits once the file row is gone")
	}
	if orphan[0].Label != "Indexed file" {
		t.Errorf("label = %q, want the generic label when the file row no longer exists", orphan[0].Label)
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
	saved, err := fileStore.SaveThreadFile(indexerTestUID, 7, "thr-1", "notes.txt", strings.NewReader(content))
	if err != nil {
		t.Fatalf("SaveThreadFile: %v", err)
	}

	ctx := context.Background()
	if err := idx.IndexFile(ctx, indexerTestUID, saved.FileID); err != nil {
		t.Fatalf("IndexFile: %v", err)
	}

	hits, err := kstore.Search(ctx, indexerTestUID, basis(0), 50)
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
	if err := idx.IndexFile(ctx, indexerTestUID, saved.FileID); err != nil {
		t.Fatalf("re-IndexFile: %v", err)
	}
	hits2, _ := kstore.Search(ctx, indexerTestUID, basis(0), 50)
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
	hits3, _ := kstore.Search(ctx, indexerTestUID, basis(0), 50)
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
	hits, _ := kstore.Search(context.Background(), 1, basis(0), 5)
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
	saved, err := fileStore.SaveThreadFile(indexerTestUID, 1, "thr", "cats.txt", strings.NewReader(content))
	if err != nil {
		t.Fatalf("SaveThreadFile: %v", err)
	}
	ctx := context.Background()
	if err := idx.IndexFile(ctx, indexerTestUID, saved.FileID); err != nil {
		t.Fatalf("IndexFile: %v", err)
	}

	q, err := emb.Embed(ctx, "How long do cats sleep each day?")
	if err != nil {
		t.Fatalf("Embed query: %v", err)
	}
	hits, err := kstore.Search(ctx, indexerTestUID, q, 3)
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
	if err := idx.IndexTurn(ctx, indexerTestUID, "turn-1", userText, aiText); err != nil {
		t.Fatalf("IndexTurn: %v", err)
	}
	hits, _ := kstore.Search(ctx, indexerTestUID, basis(0), 10)
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
	if err := idx.IndexTurn(ctx, indexerTestUID, "turn-1", userText, aiText); err != nil {
		t.Fatalf("re-IndexTurn: %v", err)
	}
	hits2, _ := kstore.Search(ctx, indexerTestUID, basis(0), 10)
	if len(hits2) != count {
		t.Errorf("re-index changed count: %d -> %d", count, len(hits2))
	}

	// Empty turn clears the source's chunks.
	if err := idx.IndexTurn(ctx, indexerTestUID, "turn-1", "  ", ""); err != nil {
		t.Fatalf("empty IndexTurn: %v", err)
	}
	hits3, _ := kstore.Search(ctx, indexerTestUID, basis(0), 10)
	if len(hits3) != 0 {
		t.Errorf("empty turn should clear chunks, got %d", len(hits3))
	}

	// RemoveTurn drops a turn's chunks.
	if err := idx.IndexTurn(ctx, indexerTestUID, "turn-2", "q", "meaningful answer text"); err != nil {
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

	if err := idx.IndexTurn(ctx, indexerTestUID, "turn-sd",
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
	hits, err := kstore.Search(ctx, indexerTestUID, q, 3)
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

	if err := idx.IndexTurn(ctx, indexerTestUID, "turn-cats", "tell me about cats",
		"Cats are carnivorous mammals that sleep up to sixteen hours per day."); err != nil {
		t.Fatalf("index cats: %v", err)
	}
	if err := idx.IndexTurn(ctx, indexerTestUID, "turn-python", "how do I read a file in python",
		"In Python, use open() to open a file then call read() to get its contents."); err != nil {
		t.Fatalf("index python: %v", err)
	}

	chunks, err := idx.Retrieve(ctx, indexerTestUID, "", "how many hours do cats sleep each day?", 2)
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
	// Deliberately not asserted: that scores descend. Results are ordered by
	// reciprocal-rank fusion across the vector and lexical retrievers, so a
	// chunk both of them found can lead one the vector half scored higher.
	// Every score is still a real cosine similarity — asserted above — it is
	// just no longer the sort key.
	for n, c := range chunks {
		if c.Kind == "" || c.Label == "" {
			t.Errorf("chunk %d reached the caller without provenance: %+v", n, c)
		}
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
