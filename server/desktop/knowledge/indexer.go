//go:build desktop && cgo

package knowledge

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	localrender "server/desktop/local_render"
)

// attachmentKindText mirrors local_inference.Attachment.Kind == "text" (the
// only kind that yields indexable text; images are skipped).
const attachmentKindText = "text"

// vectorizer is the embedding capability the indexer depends on. The concrete
// *Embedder (onnxruntime, L3c-1) satisfies it; tests inject a fake so indexer
// logic is exercisable without the native model.
type vectorizer interface {
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
}

// Indexer turns L3b file attachments into searchable knowledge chunks:
// extract → chunk → embed → store. It is the L3c-3 file-indexing pipeline,
// wiring the Embedder (L3c-1) and Store (L3c-2) onto local_render's file store.
type Indexer struct {
	files *localrender.Store
	vec   vectorizer
	store *Store
}

// NewIndexer wires the file store, embedder, and vector store. embedder is
// typed as vectorizer so the production *Embedder and test fakes both apply.
func NewIndexer(files *localrender.Store, embedder vectorizer, store *Store) *Indexer {
	return &Indexer{files: files, vec: embedder, store: store}
}

// IndexFile extracts, chunks, embeds, and indexes a single thread file, then
// atomically replaces that file's prior chunks (so a re-upload reflects
// current content). Non-text files (images) and empty extractions are skipped
// after clearing any stale chunks. Safe to call asynchronously from the upload
// handler.
func (i *Indexer) IndexFile(ctx context.Context, uid uint64, fileID int64) error {
	atts, err := i.files.Load([]int64{fileID}, uid)
	if err != nil {
		return fmt.Errorf("knowledge: load file %d: %w", fileID, err)
	}
	sourceID := fileSourceID(fileID)
	if len(atts) == 0 {
		return nil
	}

	att := atts[0]
	if att.Kind != attachmentKindText || strings.TrimSpace(att.Text) == "" {
		// Non-text (image) or empty: clear any stale chunks, nothing to embed.
		if _, err := i.store.ReplaceSource(ctx, SourceTypeFile, sourceID, nil); err != nil {
			return fmt.Errorf("knowledge: clear file %d: %w", fileID, err)
		}
		return nil
	}

	pieces := ChunkText(att.Text, DefaultChunkChars)
	if len(pieces) == 0 {
		if _, err := i.store.ReplaceSource(ctx, SourceTypeFile, sourceID, nil); err != nil {
			return fmt.Errorf("knowledge: clear file %d: %w", fileID, err)
		}
		return nil
	}

	vecs, err := i.vec.EmbedBatch(ctx, pieces)
	if err != nil {
		return fmt.Errorf("knowledge: embed file %d: %w", fileID, err)
	}
	if len(vecs) != len(pieces) {
		return fmt.Errorf("knowledge: embed count mismatch for file %d: %d chunks, %d vectors", fileID, len(pieces), len(vecs))
	}

	chunks := make([]Chunk, len(pieces))
	for idx, text := range pieces {
		chunks[idx] = Chunk{
			ChunkUID:   fmt.Sprintf("file:%d:%d", fileID, idx),
			SourceType: SourceTypeFile,
			SourceID:   sourceID,
			Text:       text,
			Embedding:  vecs[idx],
		}
	}
	if _, err := i.store.ReplaceSource(ctx, SourceTypeFile, sourceID, chunks); err != nil {
		return fmt.Errorf("knowledge: store file %d: %w", fileID, err)
	}
	return nil
}

// RemoveFile drops all chunks for a file (e.g. when the file or its thread is
// deleted). Returns the count removed.
func (i *Indexer) RemoveFile(ctx context.Context, fileID int64) (int, error) {
	return i.store.DeleteBySource(ctx, SourceTypeFile, fileSourceID(fileID))
}

// IndexTurn indexes a completed conversation turn (user question + assistant
// answer) for cross-thread long-term memory (L3c-4). The combined text is
// chunked, embedded, and stored under source_type=message keyed by turnUUID,
// so re-indexing the same turn replaces its chunks. Empty turns clear any
// stale chunks. Satisfies local_inference.ConversationIndexer.
func (i *Indexer) IndexTurn(ctx context.Context, turnUUID, userText, assistantText string) error {
	var combined string
	if u := strings.TrimSpace(userText); u != "" {
		combined = u + "\n\n"
	}
	combined += strings.TrimSpace(assistantText)
	combined = strings.TrimSpace(combined)

	sourceID := turnSourceID(turnUUID)
	if combined == "" {
		if _, err := i.store.ReplaceSource(ctx, SourceTypeMessage, sourceID, nil); err != nil {
			return fmt.Errorf("knowledge: clear turn %s: %w", turnUUID, err)
		}
		return nil
	}

	pieces := ChunkText(combined, DefaultChunkChars)
	if len(pieces) == 0 {
		if _, err := i.store.ReplaceSource(ctx, SourceTypeMessage, sourceID, nil); err != nil {
			return fmt.Errorf("knowledge: clear turn %s: %w", turnUUID, err)
		}
		return nil
	}

	vecs, err := i.vec.EmbedBatch(ctx, pieces)
	if err != nil {
		return fmt.Errorf("knowledge: embed turn %s: %w", turnUUID, err)
	}
	if len(vecs) != len(pieces) {
		return fmt.Errorf("knowledge: embed count mismatch for turn %s: %d chunks, %d vectors", turnUUID, len(pieces), len(vecs))
	}

	chunks := make([]Chunk, len(pieces))
	for idx, text := range pieces {
		chunks[idx] = Chunk{
			ChunkUID:   fmt.Sprintf("msg:%s:%d", turnUUID, idx),
			SourceType: SourceTypeMessage,
			SourceID:   sourceID,
			Text:       text,
			Embedding:  vecs[idx],
		}
	}
	if _, err := i.store.ReplaceSource(ctx, SourceTypeMessage, sourceID, chunks); err != nil {
		return fmt.Errorf("knowledge: store turn %s: %w", turnUUID, err)
	}
	return nil
}

// RemoveTurn drops all chunks for a conversation turn (e.g. when the turn or
// its thread is deleted). Returns the count removed.
func (i *Indexer) RemoveTurn(ctx context.Context, turnUUID string) (int, error) {
	return i.store.DeleteBySource(ctx, SourceTypeMessage, turnSourceID(turnUUID))
}

// Retrieved is one retrieval hit with the provenance needed to say where it
// came from. Label is resolved here rather than by the caller because this is
// the only layer that knows a source id is a file row id.
type Retrieved struct {
	Kind  string // "file" | "conversation"
	Label string
	Text  string
	Score float64 // 0..1, higher = closer
}

// Retrieve returns the topK chunks most relevant to query, best first, for
// injecting as context into the next turn (L3c-5). Empty query or topK<=0
// returns nil.
//
// uid scopes only the file-name lookup, not the search. The chunk store has no
// uid column: it is one index for one machine's single local user. A machine
// that was used signed-out and then signed in holds chunks under both
// localSingleUserUID and the cloud uid — the same person's own data either
// way, which is why they are searched together. It does mean a file whose row
// belongs to the other identity resolves to no name, so it is labelled
// generically rather than shown under a name that could be wrong.
func (i *Indexer) Retrieve(ctx context.Context, uid uint64, query string, topK int) ([]Retrieved, error) {
	if strings.TrimSpace(query) == "" || topK <= 0 {
		return nil, nil
	}
	qvecs, err := i.vec.EmbedBatch(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("knowledge: embed query: %w", err)
	}
	if len(qvecs) != 1 {
		return nil, fmt.Errorf("knowledge: query embed returned %d vectors", len(qvecs))
	}
	hits, err := i.store.Search(ctx, qvecs[0], topK)
	if err != nil {
		return nil, fmt.Errorf("knowledge: retrieve search: %w", err)
	}

	names := i.resolveFileNames(uid, hits)
	out := make([]Retrieved, 0, len(hits))
	for _, h := range hits {
		r := Retrieved{Text: h.Text, Score: similarityFromDistance(h.Distance)}
		if h.SourceType == SourceTypeFile {
			r.Kind = "file"
			if name, ok := names[h.SourceID]; ok {
				r.Label = name
			} else {
				r.Label = "Indexed file"
			}
		} else {
			r.Kind = "conversation"
			r.Label = "Earlier conversation"
		}
		out = append(out, r)
	}
	return out, nil
}

// resolveFileNames turns the file source ids among the hits into display
// names. A failure here loses labels, not results: retrieval already
// succeeded, and answering with unnamed context beats not answering.
func (i *Indexer) resolveFileNames(uid uint64, hits []ChunkResult) map[string]string {
	ids := make([]int64, 0, len(hits))
	seen := make(map[string]bool, len(hits))
	for _, h := range hits {
		if h.SourceType != SourceTypeFile || seen[h.SourceID] {
			continue
		}
		seen[h.SourceID] = true
		id, err := strconv.ParseInt(h.SourceID, 10, 64)
		if err != nil {
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	byID, err := i.files.FileNames(uid, ids)
	if err != nil {
		return nil
	}
	names := make(map[string]string, len(byID))
	for id, name := range byID {
		names[fileSourceID(id)] = name
	}
	return names
}

// similarityFromDistance converts the store's L2 distance to cosine
// similarity. Embeddings are L2-normalized, so |a-b|² = 2 - 2·cos(a,b) and the
// inversion is exact rather than a heuristic. Clamped because floating-point
// error can push a perfect match a hair past 1.
func similarityFromDistance(distance float64) float64 {
	sim := 1 - (distance*distance)/2
	if sim < 0 {
		return 0
	}
	if sim > 1 {
		return 1
	}
	return sim
}

// fileSourceID is the stable string key under which a file's chunks are stored.
func fileSourceID(fileID int64) string { return strconv.FormatInt(fileID, 10) }

// turnSourceID is the stable string key under which a turn's chunks are stored.
func turnSourceID(turnUUID string) string { return "turn:" + turnUUID }
