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

// buildKnowledgeWiring resolves the native assets but does NOT load them.
//
// Loading them costs about 223MB resident and a fifth of a second, measured:
// 32MB/48ms without the index against 255MB/136-251ms with it. That is the
// floor, paid before a single query — the ONNX Runtime environment and the
// model session, not anything the user asked for.
//
// Eager loading meant a user who had once downloaded the assets paid that
// every launch forever, including the launches where they never opened the
// knowledge base. So construction is deferred to first use: presence of the
// assets decides whether retrieval is *available*, and the first actual call
// decides when it is *loaded*.
func buildKnowledgeWiring(deps KnowledgeDeps) (KnowledgeWiring, error) {
	res, err := knowledge.ResolveResourcesIn(deps.ResourcesDir)
	if err != nil {
		return KnowledgeWiring{}, fmt.Errorf("native resources unresolved: %w", err)
	}

	lazy := &lazyKnowledge{deps: deps, res: res}
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
	deps KnowledgeDeps
	res  knowledge.Resources

	once     sync.Once
	indexer  *knowledge.Indexer
	embedder *knowledge.Embedder
	initErr  error

	// Indexing runs in background goroutines the caller does not wait on —
	// the local inference engine indexes a turn after answering it. Destroying
	// the ONNX Runtime environment while one of those is inside session.Run
	// segfaults the process, which is what "close the app right after a reply"
	// used to do. So calls are counted, and Close waits for them.
	mu       sync.RWMutex
	closed   bool
	inFlight sync.WaitGroup
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

// load runs at most once. A failure is remembered rather than retried: the
// causes are missing or corrupt assets, which will not fix themselves between
// two calls, and retrying would mean re-paying the load cost on every message.
func (l *lazyKnowledge) load() error {
	l.once.Do(func() {
		embedder, err := knowledge.NewEmbedder(l.res)
		if err != nil {
			l.initErr = fmt.Errorf("embedder init: %w", err)
			return
		}
		store, err := knowledge.NewStore(l.deps.DB)
		if err != nil {
			_ = embedder.Close()
			l.initErr = fmt.Errorf("vector store init: %w", err)
			return
		}
		l.embedder = embedder
		l.indexer = knowledge.NewIndexer(l.deps.Files, embedder, store)
		log.Printf("knowledge: local RAG loaded on demand (%d-dim embeddings)", embedder.Dim())
	})
	return l.initErr
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

// RemoveFile deletes rows the indexer wrote. It still has to load, because the
// vector store is what owns those rows — but a delete on an index that was
// never built is a no-op, not an error, so a load failure here is swallowed
// rather than failing the caller's delete.
func (l *lazyKnowledge) RemoveFile(ctx context.Context, fileID int64) (int, error) {
	if !l.begin() {
		return 0, nil
	}
	defer l.end()
	if err := l.load(); err != nil {
		return 0, nil
	}
	return l.indexer.RemoveFile(ctx, fileID)
}

// RemoveTurn mirrors RemoveFile's shape for conversation chunks: a delete
// against an index that never built is a no-op, not an error.
func (l *lazyKnowledge) RemoveTurn(ctx context.Context, turnUUID string) (int, error) {
	if !l.begin() {
		return 0, nil
	}
	defer l.end()
	if err := l.load(); err != nil {
		return 0, nil
	}
	return l.indexer.RemoveTurn(ctx, turnUUID)
}

func (l *lazyKnowledge) IndexTurn(ctx context.Context, turnUUID, userText, assistantText string) error {
	if !l.begin() {
		return errKnowledgeClosed
	}
	defer l.end()
	if err := l.load(); err != nil {
		return err
	}
	return l.indexer.IndexTurn(ctx, turnUUID, userText, assistantText)
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
	if err := l.load(); err != nil {
		return nil, err
	}
	hits, err := l.indexer.Retrieve(ctx, uid, query, topK)
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
