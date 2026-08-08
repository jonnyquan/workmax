//go:build desktop && cgo

package desktop

import (
	"fmt"

	knowledge "server/desktop/knowledge"
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

func buildKnowledgeWiring(deps KnowledgeDeps) (KnowledgeWiring, error) {
	res, err := knowledge.ResolveResourcesIn(deps.ResourcesDir)
	if err != nil {
		return KnowledgeWiring{}, fmt.Errorf("native resources unresolved: %w", err)
	}

	embedder, err := knowledge.NewEmbedder(res)
	if err != nil {
		return KnowledgeWiring{}, fmt.Errorf("embedder init: %w", err)
	}

	store, err := knowledge.NewStore(deps.DB)
	if err != nil {
		// The embedder already holds an ONNX Runtime environment; release it
		// rather than leaking it into a boot that will run without RAG.
		_ = embedder.Close()
		return KnowledgeWiring{}, fmt.Errorf("vector store init: %w", err)
	}

	indexer := knowledge.NewIndexer(deps.Files, embedder, store)
	return KnowledgeWiring{
		FileIndexer: indexer,
		Hooks:       indexer,
		Close:       embedder.Close,
		Dim:         embedder.Dim(),
	}, nil
}
