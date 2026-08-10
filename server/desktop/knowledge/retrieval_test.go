//go:build desktop && cgo

package knowledge

import (
	"context"
	"strings"
	"testing"

	localrender "server/desktop/local_render"
)

// The behaviour this whole file exists for: KNN always has a nearest
// neighbour, so before thresholding, "继续" retrieved four chunks about
// whatever happened to be closest to the embedding of the word and pushed them
// into the prompt.
func TestHasRetrievalSignal_SkipsGenericTurns(t *testing.T) {
	suppressed := []string{
		"继续", "继续吧", "好的", "好的，继续", "嗯", "下一步", "然后呢", "谢谢",
		"ok", "OK!", "okay", "yes", "yeah", "sure", "go on", "continue",
		"next", "thanks", "thank you", "done", "  ", "", "?", "。",
	}
	for _, q := range suppressed {
		if HasRetrievalSignal(q) {
			t.Errorf("HasRetrievalSignal(%q) = true; a turn carrying no question must not be searched", q)
		}
	}

	searched := []string{
		"去年的营收是多少",
		"帮我看看薪资结构调整方案",
		"what did we decide about the retention policy",
		"resolveFileNames",
		"server/desktop/knowledge/store.go",
		"beta",
		"how does RRF work",
	}
	for _, q := range searched {
		if !HasRetrievalSignal(q) {
			t.Errorf("HasRetrievalSignal(%q) = false; a real question must still be searched", q)
		}
	}
}

// The absolute floor and the relative cut answer different questions and both
// have to bite.
func TestKeepTopRelative(t *testing.T) {
	cases := []struct {
		name   string
		scores []float64
		want   int
	}{
		{"nothing", nil, 0},
		{"top below the floor drops everything", []float64{0.29, 0.28, 0.1}, 0},
		{"tail below the floor is cut", []float64{0.8, 0.6, 0.29, 0.05}, 2},
		{"tail far below the best is cut", []float64{0.9, 0.85, 0.41}, 2},
		{"all comparable are kept", []float64{0.9, 0.85, 0.8}, 3},
		{"exactly at the floor is kept", []float64{0.5, minSimilarity}, 2},
	}
	for _, c := range cases {
		if got := KeepTopRelative(c.scores, autoRecallRatio); got != c.want {
			t.Errorf("%s: KeepTopRelative(%v) = %d, want %d", c.name, c.scores, got, c.want)
		}
	}

	// The relative cut has to be relative, not a second floor: 0.41 survives
	// beside a modest best hit and is dropped beside a strong one. If this
	// pair ever agrees, the ratio has stopped doing anything and only the
	// absolute floor is left.
	if KeepTopRelative([]float64{0.5, 0.41}, autoRecallRatio) != 2 {
		t.Error("a hit close to a modest best hit should survive")
	}
	if KeepTopRelative([]float64{0.95, 0.41}, autoRecallRatio) != 1 {
		t.Error("the same hit beside a much stronger one should be cut")
	}
}

// Chinese is the case plain FTS5 cannot do, so it is the case the segmentation
// is asserted on directly.
func TestSegmentation_ExpandsCJKToUnigramsAndBigrams(t *testing.T) {
	body := segmentForIndex("本季度薪资")
	fields := strings.Fields(body)
	want := []string{"本", "本季", "季", "季度", "度", "度薪", "薪", "薪资", "资"}
	if strings.Join(fields, " ") != strings.Join(want, " ") {
		t.Errorf("segmentForIndex = %v, want %v", fields, want)
	}

	// Latin runs are lowercased and split on internal punctuation, so a query
	// for a bare identifier reaches a chunk that only mentions it in a path.
	got := strings.Fields(segmentForIndex("server/desktop/Store.go"))
	for _, w := range []string{"server", "desktop", "store", "go"} {
		if !contains(got, w) {
			t.Errorf("segmentForIndex(path) = %v, missing %q", got, w)
		}
	}
}

// A user's text must never be able to be a query expression. FTS5 treats `.`,
// `(`, `*`, AND/OR/NOT/NEAR as syntax; passing raw text through produced
// either a parse error or (measured) a silent zero-row result.
func TestFTSMatchExpr_QuotesEveryTerm(t *testing.T) {
	if got := ftsMatchExpr("store.go"); got != `"store" OR "go"` {
		t.Errorf("ftsMatchExpr(store.go) = %q", got)
	}
	if got := ftsMatchExpr(`a AND (b`); !strings.Contains(got, `"a"`) || strings.Contains(got, "(") {
		t.Errorf("ftsMatchExpr leaked query syntax: %q", got)
	}
	// Segmentation drops punctuation, so no quote can survive into a term —
	// but the expression builder escapes anyway, and the property that
	// actually has to hold is that nothing but terms and OR reaches FTS5.
	for _, q := range []string{`say "hi"`, `NEAR(a b)`, `foo*`, `-bar^`} {
		got := ftsMatchExpr(q)
		if strings.ContainsAny(strings.ReplaceAll(got, `"`, ""), `*(^)-`) {
			t.Errorf("ftsMatchExpr(%q) = %q leaked FTS5 syntax", q, got)
		}
	}
	if got := ftsMatchExpr("   "); got != "" {
		t.Errorf("a query with no terms must produce no expression, got %q", got)
	}
}

func TestLexicalCoverage(t *testing.T) {
	terms := queryTermSet("resolve file names")
	full := lexicalCoverage(terms, segmentForIndex("func resolve file names(uid uint64)"))
	if full != 1 {
		t.Errorf("coverage of a chunk containing every term = %v, want 1", full)
	}
	partial := lexicalCoverage(terms, segmentForIndex("the file was resolved yesterday"))
	if partial <= 0 || partial >= 1 {
		t.Errorf("partial coverage = %v, want strictly between 0 and 1", partial)
	}
	if lexicalCoverage(terms, segmentForIndex("nothing relevant whatsoever")) != 0 {
		t.Error("a chunk sharing no terms must score 0 coverage")
	}
}

// Reciprocal rank fusion: agreement between the two retrievers beats a first
// place from either one alone.
func TestFuseRRF_AgreementWins(t *testing.T) {
	vec := []ChunkResult{
		{Chunk: Chunk{ChunkUID: "only-vector"}, Distance: 0.1},
		{Chunk: Chunk{ChunkUID: "both"}, Distance: 0.5},
	}
	lex := []LexicalResult{
		{ChunkResult: ChunkResult{Chunk: Chunk{ChunkUID: "both"}, Distance: maxL2Distance}},
		{ChunkResult: ChunkResult{Chunk: Chunk{ChunkUID: "only-lexical"}, Distance: maxL2Distance}},
	}
	fused := fuseRRF(vec, lex)
	if len(fused) != 3 {
		t.Fatalf("fused %d entries, want 3 distinct chunks", len(fused))
	}
	if fused[0].chunkUID != "both" {
		t.Errorf("fusion put %q first; the chunk both retrievers found should lead", fused[0].chunkUID)
	}
	if !fused[0].fromVec || !fused[0].fromFTS {
		t.Error("the shared entry lost track of which retrievers found it")
	}
	// The vector row's real distance must survive the merge; the lexical row's
	// placeholder must not overwrite it.
	if fused[0].result.Distance != 0.5 {
		t.Errorf("distance = %v, want the vector row's 0.5", fused[0].result.Distance)
	}
}

// Hybrid retrieval end to end, on a vectorizer that deliberately knows
// nothing: every chunk embeds to the same basis vector, so the vector half
// cannot tell them apart and only the lexical half can. That is the exact
// situation a literal query creates in production — an exact identifier the
// embedding blurs into its neighbours — reproduced without the native model.
func TestRetrieve_LexicalHalfFindsWhatTheVectorHalfCannot(t *testing.T) {
	db := openIndexerTestDB(t)
	fileStore := localrender.NewStore(db, t.TempDir())
	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if !store.FTSAvailable() {
		t.Fatal("the lexical index did not come up; hybrid retrieval is off")
	}
	idx := NewIndexer(fileStore, constVectorizer{}, store)
	idx.telemetry = &retrievalTelemetry{}
	ctx := context.Background()

	for _, c := range []struct{ turn, user, assistant string }{
		{"t-paths", "where does the store live", "It lives in server/desktop/knowledge/store.go next to the indexer."},
		{"t-noise1", "unrelated", "The weather in Reykjavik is cold and the coffee is expensive."},
		{"t-noise2", "unrelated", "Sourdough needs an active starter and a long cold proof."},
		{"t-salary", "薪资问题", "本季度的薪资结构调整方案已经通过评审，将在下个月执行。"},
	} {
		if err := idx.IndexTurn(ctx, indexerTestUID, c.turn, c.user, c.assistant); err != nil {
			t.Fatalf("IndexTurn %s: %v", c.turn, err)
		}
	}

	// Every chunk sits at the same point in embedding space, so the vector
	// half ranks them by insertion order and has no opinion at all.
	hits, err := idx.Retrieve(ctx, indexerTestUID, "server/desktop/knowledge/store.go", 3)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("a literal path query retrieved nothing")
	}
	if !strings.Contains(hits[0].Text, "store.go") {
		t.Errorf("top hit for a path query is %q; the lexical half did not win", hits[0].Text)
	}

	// The same for a two-character Chinese word inside an unbroken sentence —
	// the case unicode61 alone cannot match and trigram cannot query.
	hits, err = idx.Retrieve(ctx, indexerTestUID, "薪资结构调整", 3)
	if err != nil {
		t.Fatalf("Retrieve (chinese): %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("a Chinese query retrieved nothing")
	}
	if !strings.Contains(hits[0].Text, "薪资") {
		t.Errorf("top hit for a Chinese query is %q", hits[0].Text)
	}

	// The contrast that makes the claim testable rather than asserted: with
	// the lexical index removed, the identical query on the identical data
	// returns something else entirely.
	sqlDB, _ := store.sqlDB()
	if _, err := sqlDB.ExecContext(ctx, "DROP TABLE "+ftsTable); err != nil {
		t.Fatalf("drop fts table: %v", err)
	}
	vectorOnly, err := idx.Retrieve(ctx, indexerTestUID, "server/desktop/knowledge/store.go", 3)
	if err != nil {
		t.Fatalf("Retrieve vector-only: %v", err)
	}
	if len(vectorOnly) > 0 && strings.Contains(vectorOnly[0].Text, "store.go") {
		t.Error("the vector half found the path on its own, so this test proves nothing about the lexical half")
	}

	stats := idx.telemetry.snapshot()
	if stats.LexicalKept == 0 {
		t.Error("telemetry recorded no surviving lexical candidates, so only one half ever ran")
	}
	if stats.LexicalUnavailable != 1 {
		t.Errorf("LexicalUnavailable = %d, want exactly the one search run after the index was dropped", stats.LexicalUnavailable)
	}
}

// The two indexes describe the same chunks, so a chunk that leaves one must
// leave the other. A lexical row surviving a delete would make text the user
// removed retrievable forever — and searchable by its content, which is the
// worse half of that.
func TestStore_LexicalShadowStaysInStepWithDeletes(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if !store.FTSAvailable() {
		t.Fatal("the lexical index did not come up")
	}

	chunks := []Chunk{
		{UID: storeTestUID, ChunkUID: "c1", SourceType: SourceTypeFile, SourceID: "f1",
			Text: "quarterly compensation review notes", Embedding: basis(0)},
		{UID: storeTestUID, ChunkUID: "c2", SourceType: SourceTypeFile, SourceID: "f2",
			Text: "本季度的薪资结构调整方案", Embedding: basis(1)},
	}
	if _, err := store.UpsertChunks(ctx, chunks); err != nil {
		t.Fatalf("UpsertChunks: %v", err)
	}

	mustFind := func(query string, want int) []LexicalResult {
		t.Helper()
		hits, err := store.SearchLexical(ctx, storeTestUID, query, 10)
		if err != nil {
			t.Fatalf("SearchLexical(%q): %v", query, err)
		}
		if len(hits) != want {
			t.Fatalf("SearchLexical(%q) = %d hits, want %d", query, len(hits), want)
		}
		return hits
	}
	mustFind("compensation review", 1)
	// The Chinese case: a two-character word inside an unbroken sentence.
	mustFind("薪资", 1)
	// Another identity sees neither.
	if hits, _ := store.SearchLexical(ctx, storeTestUID+1, "compensation", 10); len(hits) != 0 {
		t.Errorf("a second local account read %d lexical hits it does not own", len(hits))
	}

	// Re-upserting the same chunk_uid with new text must not leave the old
	// text searchable.
	chunks[0].Text = "quarterly headcount planning notes"
	if _, err := store.UpsertChunks(ctx, chunks[:1]); err != nil {
		t.Fatalf("re-UpsertChunks: %v", err)
	}
	mustFind("compensation", 0)
	mustFind("headcount planning", 1)

	// DeleteBySource and ReplaceSource both clear the shadow.
	if _, err := store.DeleteBySource(ctx, SourceTypeFile, "f1"); err != nil {
		t.Fatalf("DeleteBySource: %v", err)
	}
	mustFind("headcount", 0)
	if _, err := store.ReplaceSource(ctx, storeTestUID, SourceTypeFile, "f2", nil); err != nil {
		t.Fatalf("ReplaceSource: %v", err)
	}
	mustFind("薪资", 0)
}

// A hit only the lexical index found still owes the user a real similarity;
// showing it as 0 (or as a placeholder distance) would misreport how good the
// match is in the very panel that exists so the user can judge it.
func TestStore_DistancesForBackfillsLexicalOnlyHits(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if _, err := store.UpsertChunks(ctx, []Chunk{
		{UID: storeTestUID, ChunkUID: "c1", SourceType: SourceTypeFile, SourceID: "f1", Text: "a", Embedding: basis(0)},
		{UID: storeTestUID, ChunkUID: "c2", SourceType: SourceTypeFile, SourceID: "f1", Text: "b", Embedding: basis(1)},
	}); err != nil {
		t.Fatalf("UpsertChunks: %v", err)
	}
	got, err := store.DistancesFor(ctx, storeTestUID, []string{"c1", "c2", "gone"}, basis(0))
	if err != nil {
		t.Fatalf("DistancesFor: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("DistancesFor returned %v, want two known ids and no entry for the missing one", got)
	}
	if similarityFromDistance(got["c1"]) < 0.99 {
		t.Errorf("c1 similarity = %v, want ~1", similarityFromDistance(got["c1"]))
	}
	if similarityFromDistance(got["c2"]) > 0.01 {
		t.Errorf("c2 similarity = %v, want ~0", similarityFromDistance(got["c2"]))
	}
	// Another identity's chunks are not measurable through this path either.
	if other, _ := store.DistancesFor(ctx, storeTestUID+1, []string{"c1"}, basis(0)); len(other) != 0 {
		t.Errorf("DistancesFor leaked another identity's chunk: %v", other)
	}
}

// FTS5 is an improvement on the vector index; an improvement that can take
// retrieval down with it is not one.
func TestRetrieve_FallsBackToVectorWhenLexicalIndexIsGone(t *testing.T) {
	db := openIndexerTestDB(t)
	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	idx := NewIndexer(nil, fakeVectorizer{}, store)
	idx.telemetry = &retrievalTelemetry{}
	ctx := context.Background()

	if err := idx.IndexTurn(ctx, indexerTestUID, "t1", "what is RRF", "Reciprocal rank fusion sums 1/(k+rank)."); err != nil {
		t.Fatalf("IndexTurn: %v", err)
	}

	// The table disappears underneath a running store — an upgrade, a manual
	// vacuum, a corrupt shadow. Writes and reads must both survive it.
	sqlDB, _ := store.sqlDB()
	if _, err := sqlDB.ExecContext(ctx, "DROP TABLE "+ftsTable); err != nil {
		t.Fatalf("drop fts table: %v", err)
	}

	hits, err := idx.Retrieve(ctx, indexerTestUID, "reciprocal rank fusion", 3)
	if err != nil {
		t.Fatalf("Retrieve with no lexical index: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("losing the lexical index took vector retrieval down with it")
	}
	if store.FTSAvailable() {
		t.Error("the store still believes the lexical index is available")
	}

	// And writing keeps working, still without the shadow.
	if err := idx.IndexTurn(ctx, indexerTestUID, "t2", "another", "another answer about fusion"); err != nil {
		t.Fatalf("IndexTurn after the lexical index vanished: %v", err)
	}
	if stats := idx.telemetry.snapshot(); stats.LexicalUnavailable == 0 {
		t.Error("a vector-only search was not recorded as such")
	}
}

// Low-scoring candidates must not reach the prompt, and a turn that carries no
// question must not even be searched.
func TestRetrieve_ThresholdsAndSuppression(t *testing.T) {
	db := openIndexerTestDB(t)
	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	idx := NewIndexer(nil, fakeVectorizer{}, store)
	tel := &retrievalTelemetry{}
	idx.telemetry = tel
	ctx := context.Background()

	// fakeVectorizer embeds text i of a batch as basis(i). Two chunks are
	// written, so the second sits orthogonal to any single-text query — cosine
	// 0, far below the floor.
	if err := idx.IndexTurn(ctx, indexerTestUID, "t1", "first paragraph about fusion",
		"second paragraph\n\n"+strings.Repeat("filler text that forms its own chunk. ", 30)); err != nil {
		t.Fatalf("IndexTurn: %v", err)
	}
	raw, err := store.Search(ctx, indexerTestUID, basis(0), 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(raw) < 2 {
		t.Fatalf("expected several chunks in the store, got %d", len(raw))
	}

	hits, err := idx.Retrieve(ctx, indexerTestUID, "unrelated question about orthogonal vectors", 10)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	for _, h := range hits {
		if h.Score < minSimilarity {
			t.Errorf("a hit scoring %.3f survived the %.2f floor: %q", h.Score, minSimilarity, h.Text)
		}
	}
	if len(hits) >= len(raw) {
		t.Errorf("thresholding kept %d of %d candidates; the floor is not biting", len(hits), len(raw))
	}

	// A generic turn is not searched at all — no hits, and no telemetry
	// claiming a search happened.
	before := tel.snapshot().Searched
	for _, q := range []string{"继续", "ok", "好的"} {
		got, err := idx.Retrieve(ctx, indexerTestUID, q, 4)
		if err != nil {
			t.Fatalf("Retrieve(%q): %v", q, err)
		}
		if len(got) != 0 {
			t.Errorf("Retrieve(%q) returned %d hits; a continuation carries no query", q, len(got))
		}
	}
	after := tel.snapshot()
	if after.Searched != before {
		t.Errorf("a suppressed turn still ran a search (%d -> %d)", before, after.Searched)
	}
	if after.Suppressed != 3 {
		t.Errorf("Suppressed = %d, want 3", after.Suppressed)
	}
}

// The counterfactual arm: retrieval is hidden, indexing is not. Without the
// second half the control run would drift into an empty index and stop being
// comparable to anything.
func TestRetrieve_ExperimentSwitchHidesRecallButKeepsIndexing(t *testing.T) {
	db := openIndexerTestDB(t)
	store, err := NewStore(db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	idx := NewIndexer(nil, fakeVectorizer{}, store)
	idx.telemetry = &retrievalTelemetry{}
	ctx := context.Background()

	t.Setenv(experimentNoRAGEnv, "1")
	if !RetrievalDisabled() {
		t.Fatal("the experiment switch did not take effect")
	}

	if err := idx.IndexTurn(ctx, indexerTestUID, "t1", "what is RRF", "Reciprocal rank fusion sums reciprocal ranks."); err != nil {
		t.Fatalf("IndexTurn under the experiment switch: %v", err)
	}
	if raw, _ := store.Search(ctx, indexerTestUID, basis(0), 5); len(raw) == 0 {
		t.Fatal("the experiment switch stopped indexing; the control arm's index would go cold")
	}

	hits, err := idx.Retrieve(ctx, indexerTestUID, "reciprocal rank fusion", 5)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("retrieval returned %d hits with WORKMAX_EXPERIMENT_NO_RAG set", len(hits))
	}
	if stats := idx.telemetry.snapshot(); stats.Disabled != 1 || stats.Searched != 0 {
		t.Errorf("telemetry = %+v, want exactly one disabled call and no searches", stats)
	}

	// Unset: the same query now finds what was indexed while the flag was on.
	t.Setenv(experimentNoRAGEnv, "")
	hits, err = idx.Retrieve(ctx, indexerTestUID, "reciprocal rank fusion", 5)
	if err != nil {
		t.Fatalf("Retrieve after clearing the switch: %v", err)
	}
	if len(hits) == 0 {
		t.Error("what was indexed during the control run is not retrievable after it")
	}
}

// Telemetry is the evidence a with/without comparison rests on, and it must
// carry no evidence of what anyone typed.
func TestTelemetry_CountsWithoutContent(t *testing.T) {
	tel := &retrievalTelemetry{}
	tel.record(retrievalOutcome{suppressed: true, queryRunes: 2})
	tel.record(retrievalOutcome{disabled: true, queryRunes: 40})
	tel.record(retrievalOutcome{failed: true, queryRunes: 10})
	tel.record(retrievalOutcome{
		queryRunes: 12, vectorCandidates: 8, vectorKept: 3,
		lexicalCandidates: 5, lexicalKept: 2, lexicalOnly: 1, injected: 4, topScore: 0.64,
	})
	tel.record(retrievalOutcome{queryRunes: 12, vectorCandidates: 8, injected: 0})

	s := tel.snapshot()
	if s.Calls != 5 || s.Suppressed != 1 || s.Disabled != 1 || s.Errors != 1 || s.Searched != 2 {
		t.Errorf("call accounting wrong: %+v", s)
	}
	if s.Empty != 1 {
		t.Errorf("Empty = %d, want 1 — a search that found nothing is a distinct outcome", s.Empty)
	}
	if s.Injected != 4 || s.LexicalOnlyInjected != 1 {
		t.Errorf("injection accounting wrong: %+v", s)
	}
	if s.TopScoreBuckets[6] != 1 {
		t.Errorf("a 0.64 top score landed in %v, want bucket 6", s.TopScoreBuckets)
	}

	if got := bucketLabel(0.64); got != "0.6-0.7" {
		t.Errorf("bucketLabel(0.64) = %q", got)
	}
	if got := bucketLabel(1); got != "1.0" {
		t.Errorf("bucketLabel(1) = %q", got)
	}
	if got := lengthBucket(12); got != "8-23" {
		t.Errorf("lengthBucket(12) = %q; exact lengths must not be reported", got)
	}
}

// constVectorizer embeds every text identically, so the vector half is
// deliberately blind and any ranking difference is the lexical half's doing.
type constVectorizer struct{}

func (constVectorizer) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = basis(0)
	}
	return out, nil
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
