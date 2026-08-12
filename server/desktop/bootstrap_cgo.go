//go:build desktop && cgo

package desktop

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	knowledge "server/desktop/knowledge"
	localinference "server/desktop/local_inference"
)

// This file is the only place the non-cgo desktop package touches the cgo
// knowledge package, and it exists only on cgo builds. Everything it produces
// is handed back through KnowledgeWiring's interface fields, so nothing
// downstream gains a cgo dependency.
//
// Dropping this file from a build (CGO_ENABLED=0) is a supported
// configuration, not a broken one: knowledgeProvider stays nil and the
// sidecar boots with retrieval off.

func init() { knowledgeProvider = buildKnowledgeWiring }

// buildKnowledgeWiring opens the vector store eagerly and defers everything
// expensive.
//
// The split is deliberate and the two halves have opposite costs. The store is
// pure SQL: opening it reconciles the vec0 schema version and self-checks the
// sqlite-vec code path in single-digit milliseconds, and doing that at boot is
// the only way a schema rebuild or a broken sqlite-vec shows up in the boot log
// instead of surfacing as an empty answer an hour later.
//
// The embedder is the opposite: about 223MB resident and a fifth of a second,
// measured — 32MB/48ms without the index against 255MB/136-251ms with it. That
// is a floor paid before a single query. Eager loading meant a user who had
// once downloaded the assets paid it every launch forever, including the
// launches where they never opened the knowledge base. So it is built on first
// use, and the native assets it needs are acquired on first use too.
func buildKnowledgeWiring(deps KnowledgeDeps) (KnowledgeWiring, error) {
	store, err := knowledge.NewStore(deps.DB)
	if err != nil {
		return KnowledgeWiring{}, fmt.Errorf("vector store: %w", err)
	}
	if from := store.RebuiltFrom(); from != "" {
		log.Printf("knowledge: vector index rebuilt from schema %s; previously indexed files are re-embedded in the background, past conversation turns are not", from)
	}

	downloadDir := knowledgeDownloadDir(deps.DataDir)
	lazy := &lazyKnowledge{
		deps:  deps,
		store: store,
		// A delete never needs a model: it removes rows the indexer wrote, and
		// the store that owns them is already open. Giving the delete path its
		// own vectorizer-less indexer is what keeps "delete a thread" from
		// loading 223MB of ONNX Runtime to do it.
		deleter: knowledge.NewIndexer(nil, nil, store),
	}
	// The presence probe is deliberately side-effect free: it answers "are the
	// files there", and load() re-resolves the paths for itself afterwards, so
	// two goroutines asking at once cannot hand each other a stale resolution.
	lazy.assets = newKnowledgeAssets(downloadDir, func() error {
		_, err := lazy.resolveResources()
		return err
	})
	if err := lazy.assets.ensure(); err == nil {
		log.Printf("knowledge: embedding assets ready")
	}

	return KnowledgeWiring{
		Index: lazy,
		Hooks: lazy,
		Close: lazy.Close,
		Dim:   knowledge.EmbeddingDim,
	}, nil
}

// lazyKnowledge builds the real indexer on first use and then delegates.
//
// It satisfies both consumers — desktop.KnowledgeIndex and
// localinference.KnowledgeHooks — because both are structural interfaces, so
// nothing downstream can tell the difference between this and the real thing
// except in how much memory an idle app uses.
type lazyKnowledge struct {
	deps    KnowledgeDeps
	store   *knowledge.Store
	deleter *knowledge.Indexer
	assets  *knowledgeAssets

	loadMu   sync.Mutex
	indexer  *knowledge.Indexer
	embedder *knowledge.Embedder
	// loadErr remembers only *embedder* failures. Asset acquisition failures
	// are deliberately not cached here: they are the ones that fix themselves
	// when a download finishes.
	loadErr error

	// Indexing runs in background goroutines the caller does not wait on —
	// the local inference engine indexes a turn after answering it. Destroying
	// the ONNX Runtime environment while one of those is inside session.Run
	// segfaults the process, which is what "close the app right after a reply"
	// used to do. So calls are counted, and Close waits for them.
	mu       sync.RWMutex
	closed   bool
	inFlight sync.WaitGroup
}

// resolveResources looks for the native assets in the packaged bundle first,
// then in the per-user download directory. Both are tried because they answer
// different questions: the bundle is what a full installer shipped, the
// download dir is what this machine fetched or the user supplied.
func (l *lazyKnowledge) resolveResources() (knowledge.Resources, error) {
	res, err := knowledge.ResolveResourcesIn(l.deps.ResourcesDir)
	if err == nil {
		return res, nil
	}
	return knowledge.ResolveResourcesIn(knowledgeDownloadDir(l.deps.DataDir))
}

// begin registers an in-flight call, or reports that the environment is being
// torn down and the caller must not touch it.
func (l *lazyKnowledge) begin() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.closed {
		return false
	}
	l.inFlight.Add(1)
	return true
}

func (l *lazyKnowledge) end() { l.inFlight.Done() }

// errKnowledgeClosed is returned to work that arrives during shutdown. It is
// not a failure worth surfacing: the answer was already delivered, and only
// the background indexing of it is being skipped.
var errKnowledgeClosed = errors.New("knowledge: shutting down")

// load builds the embedder once the assets exist.
//
// An embedder failure is remembered rather than retried: its causes are a
// corrupt or incompatible model, which will not fix themselves between two
// calls, and retrying would mean re-paying the load cost on every message. An
// *asset* failure is not remembered, because "not downloaded yet" is exactly
// the condition that stops being true on its own.
func (l *lazyKnowledge) load() error {
	l.loadMu.Lock()
	defer l.loadMu.Unlock()
	if l.indexer != nil {
		return nil
	}
	if l.loadErr != nil {
		return l.loadErr
	}
	if err := l.assets.ensure(); err != nil {
		return err
	}

	res, err := l.resolveResources()
	if err != nil {
		// The assets passed the presence check a moment ago and are gone now.
		// Not cached: this is a disk-level accident, not a broken model.
		return fmt.Errorf("knowledge: embedding assets vanished after being verified: %w", err)
	}
	embedder, err := knowledge.NewEmbedder(res)
	if err != nil {
		l.loadErr = fmt.Errorf("embedder init: %w", err)
		return l.loadErr
	}
	l.embedder = embedder
	l.indexer = knowledge.NewIndexer(l.deps.Files, embedder, l.store)
	log.Printf("knowledge: local RAG loaded on demand (%d-dim embeddings)", embedder.Dim())

	// A schema rebuild dropped every chunk. Now that a model exists, put the
	// files back — in the background, because the call that triggered this load
	// is a user's turn and must not wait on re-embedding their whole library.
	go l.backfill()
	return nil
}

// backfill re-embeds the files a schema rebuild dropped. Best-effort by
// design: it is repairing derived data, so a failure means the repair is tried
// again next launch, not that anything the user did was lost.
func (l *lazyKnowledge) backfill() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("knowledge: reindex after schema rebuild panicked: %v", r)
		}
	}()
	if !l.begin() {
		return
	}
	defer l.end()
	ctx, cancel := context.WithTimeout(context.Background(), knowledgeBackfillTimeout)
	defer cancel()
	n, err := l.indexer.ReindexPendingFiles(ctx)
	if err != nil {
		log.Printf("knowledge: reindex after schema rebuild: %v", err)
		return
	}
	if n > 0 {
		log.Printf("knowledge: re-indexed %d files after the vector schema rebuild", n)
	}
}

func (l *lazyKnowledge) IndexFile(ctx context.Context, uid uint64, fileID int64) error {
	if !l.begin() {
		return errKnowledgeClosed
	}
	defer l.end()
	if err := l.load(); err != nil {
		return err
	}
	return l.indexer.IndexFile(ctx, uid, fileID)
}

// RemoveFile deletes rows the indexer wrote. It goes straight to the store: the
// vector table is open from boot, so a delete neither needs nor triggers the
// embedding model.
func (l *lazyKnowledge) RemoveFile(ctx context.Context, fileID int64) (int, error) {
	if !l.begin() {
		return 0, nil
	}
	defer l.end()
	return l.deleter.RemoveFile(ctx, fileID)
}

// RemoveTurn mirrors RemoveFile's shape for conversation chunks.
func (l *lazyKnowledge) RemoveTurn(ctx context.Context, turnUUID string) (int, error) {
	if !l.begin() {
		return 0, nil
	}
	defer l.end()
	return l.deleter.RemoveTurn(ctx, turnUUID)
}

func (l *lazyKnowledge) IndexTurn(ctx context.Context, uid uint64, turnUUID, userText, assistantText string) error {
	if !l.begin() {
		return errKnowledgeClosed
	}
	defer l.end()
	if err := l.load(); err != nil {
		return err
	}
	return l.indexer.IndexTurn(ctx, uid, turnUUID, userText, assistantText)
}

// IndexMindMaterial feeds a mind through the same lazy-load path as a file:
// the embedder comes up on first use and the store write is atomic per
// material.
func (l *lazyKnowledge) IndexMindMaterial(ctx context.Context, uid uint64, mindID, title, text string) (int, error) {
	if !l.begin() {
		return 0, errKnowledgeClosed
	}
	defer l.end()
	if err := l.load(); err != nil {
		return 0, err
	}
	return l.indexer.IndexMindMaterial(ctx, uid, mindID, title, text)
}

// MindSources reads what a mind has been fed. Like the delete paths it goes
// straight to the store: listing a mind's memory must not load 223MB of
// ONNX Runtime to answer.
func (l *lazyKnowledge) MindSources(ctx context.Context, uid uint64, mindID string) ([]MindSourceStat, error) {
	if !l.begin() {
		return nil, errKnowledgeClosed
	}
	defer l.end()
	stats, _, err := l.deleter.MindSources(ctx, uid, mindID)
	if err != nil {
		return nil, err
	}
	out := make([]MindSourceStat, 0, len(stats))
	for _, stat := range stats {
		out = append(out, MindSourceStat{
			Title:     stat.Title,
			Chunks:    stat.Chunks,
			IndexedAt: stat.IndexedAt,
		})
	}
	return out, nil
}

// Retrieve is also the translation point between the two packages' retrieval
// types. They are deliberately not shared: local_inference must stay free of
// cgo, so it cannot name knowledge.Retrieved, and knowledge should not import
// the inference package to borrow a struct. The copy is three fields wide and
// this is the only file that has to know both.
func (l *lazyKnowledge) Retrieve(ctx context.Context, uid uint64, query string, topK int) ([]localinference.RetrievedSource, error) {
	if !l.begin() {
		return nil, errKnowledgeClosed
	}
	defer l.end()
	// Checked before load(), not only inside the retriever. The experiment
	// switch is meant to answer "is retrieval worth it", and a control arm that
	// still pays 223MB and a fifth of a second to load a model it will never
	// query would be measuring the wrong thing.
	if knowledge.RetrievalDisabled() {
		knowledge.NoteRetrievalDisabled(query)
		return nil, nil
	}
	if err := l.load(); err != nil {
		return nil, err
	}
	// Which mind is active is a property of the IDENTITY, not of whichever
	// engine is asking, so it is resolved here rather than threaded through
	// KnowledgeHooks. This file is already the one place that knows both
	// worlds, and doing it here means the local inference path and the agent
	// path are scoped identically without either having to remember to be.
	//
	// A mind table that cannot be read scopes nothing rather than failing the
	// turn: the cost of answering from the identity's whole memory is a wider
	// answer, and the cost of erroring is no answer at all.
	mindID := ""
	if l.deps.DB != nil {
		if active, ok, merr := NewMindStore(l.deps.DB).Active(uid); merr != nil {
			log.Printf("knowledge: active mind for retrieval: %v", merr)
		} else if ok {
			mindID = active.ID
		}
	}
	hits, err := l.indexer.Retrieve(ctx, uid, mindID, query, topK)
	if err != nil {
		return nil, err
	}
	out := make([]localinference.RetrievedSource, 0, len(hits))
	for _, h := range hits {
		out = append(out, localinference.RetrievedSource{
			Kind:  h.Kind,
			Label: h.Label,
			Text:  h.Text,
			Score: h.Score,
		})
	}
	return out, nil
}

// RetrievalDiagnostics reports the process's retrieval counters for
// /system/diagnostics.
//
// It returns a plain map rather than a typed struct so the non-cgo desktop
// package never has to name a type from the cgo knowledge package; the
// diagnostics handler discovers this method by interface assertion and omits
// the block entirely on a build where RAG does not exist.
//
// Everything in here is aggregate and content-free. Nothing in this map can be
// inverted into a query, a chunk, a file name or a count precise enough to
// fingerprint one — see the privacy note in knowledge/telemetry.go, which any
// future addition here has to satisfy too.
func (l *lazyKnowledge) RetrievalDiagnostics() map[string]any {
	s := knowledge.RetrievalTelemetrySnapshot()
	buckets := make([]int64, len(s.TopScoreBuckets))
	copy(buckets, s.TopScoreBuckets[:])
	return map[string]any{
		"experiment_no_rag":     knowledge.RetrievalDisabled(),
		"lexical_index":         l.store.FTSAvailable(),
		"calls":                 s.Calls,
		"disabled":              s.Disabled,
		"suppressed":            s.Suppressed,
		"searched":              s.Searched,
		"errors":                s.Errors,
		"empty":                 s.Empty,
		"vector_candidates":     s.VectorCandidates,
		"vector_kept":           s.VectorKept,
		"lexical_candidates":    s.LexicalCandidates,
		"lexical_kept":          s.LexicalKept,
		"lexical_only_injected": s.LexicalOnlyInjected,
		"lexical_unavailable":   s.LexicalUnavailable,
		"injected":              s.Injected,
		"top_score_buckets":     buckets,
	}
}

// Close stops accepting work, waits for what is already running, and only then
// releases the ONNX Runtime environment.
//
// Both halves matter. Closing something that never loaded must stay a no-op —
// the environment is process-global, and destroying one that was never
// initialised is how the "mutex lock failed" abort happens. And destroying one
// while a background index is inside session.Run segfaults, which is what
// closing the app immediately after a reply used to do.
//
// If the wait times out the environment is deliberately left alive: leaking it
// for the few milliseconds before the process exits is strictly better than
// tearing it out from under running native code.
func (l *lazyKnowledge) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	l.mu.Unlock()

	drained := make(chan struct{})
	go func() {
		l.inFlight.Wait()
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(knowledgeDrainTimeout):
		return fmt.Errorf("knowledge: indexing still running after %s; leaving the ONNX environment alive rather than destroying it mid-call", knowledgeDrainTimeout)
	}

	if l.embedder == nil {
		return nil
	}
	return l.embedder.Close()
}

// knowledgeDrainTimeout bounds how long shutdown waits for background indexing.
// Embedding one turn is sub-second; this is generous enough that hitting it
// means something is wedged, not merely slow.
const knowledgeDrainTimeout = 5 * time.Second

// knowledgeBackfillTimeout bounds a post-rebuild re-index. Re-embedding a large
// library legitimately takes minutes; past this the run is abandoned and picked
// up next launch, since the marker is only cleared on a clean pass.
const knowledgeBackfillTimeout = 15 * time.Minute
