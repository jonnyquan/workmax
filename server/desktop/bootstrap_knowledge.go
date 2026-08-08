//go:build desktop

package desktop

import (
	"log"

	"gorm.io/gorm"

	localinference "server/desktop/local_inference"
	localrender "server/desktop/local_render"
)

// The L3c knowledge package is cgo-only (it embeds ONNX Runtime), and this
// package is deliberately not. Both consumers of RAG are already structural
// interfaces — KnowledgeIndex here, localinference.KnowledgeHooks there — so the
// only thing missing was a construction seam that itself carries no cgo.
//
// That seam is knowledgeProvider: bootstrap_cgo.go installs it from an init
// when the binary is built with cgo, and leaves it nil otherwise. A non-cgo
// build therefore still compiles this whole package and boots with RAG off,
// which is what keeps `go test ./desktop/...` runnable without a C toolchain
// and lets a shell binary opt out of the native resource dependency entirely.

// KnowledgeDeps is what the RAG stack needs from the rest of the boot.
type KnowledgeDeps struct {
	DB    *gorm.DB
	Files *localrender.Store

	// ResourcesDir is the packaged native asset root. Empty → the knowledge
	// package falls back to WORKMAX_RESOURCES_DIR, then a "resources"
	// directory beside the working directory.
	ResourcesDir string
}

// KnowledgeWiring is the RAG surface the rest of the boot consumes. A zero
// value is the RAG-off configuration and is valid everywhere: Index nil
// means uploads are stored but not indexed, Hooks nil means turns are neither
// indexed nor retrieved against.
type KnowledgeWiring struct {
	Index KnowledgeIndex
	Hooks localinference.KnowledgeHooks

	// Close releases the ONNX Runtime environment. Nil when RAG is off.
	// Boot.Shutdown calls it before the process exits — see the ordering
	// note there for why skipping it aborts the process.
	Close func() error

	// Dim is the embedding dimensionality, logged at boot so a support
	// transcript records which model actually loaded.
	Dim int
}

// knowledgeProvider is installed by bootstrap_cgo.go on cgo builds.
var knowledgeProvider func(KnowledgeDeps) (KnowledgeWiring, error)

// resolveKnowledge builds the RAG stack when it is both compiled in and
// resolvable, and otherwise returns the RAG-off zero value. It never fails
// the boot: a desktop that cannot embed is a desktop without retrieval, not a
// desktop that refuses to start.
func resolveKnowledge(deps KnowledgeDeps) KnowledgeWiring {
	if knowledgeProvider == nil {
		log.Printf("knowledge: RAG disabled (built without cgo)")
		return KnowledgeWiring{}
	}
	wiring, err := knowledgeProvider(deps)
	if err != nil {
		log.Printf("knowledge: RAG disabled (%v)", err)
		return KnowledgeWiring{}
	}
	// "available", not "loaded": the model is read on first use, so saying it
	// is enabled here would misreport both the memory in use and when the
	// startup cost is actually paid.
	log.Printf("knowledge: local RAG available (%d-dim embeddings, loaded on first use)", wiring.Dim)
	return wiring
}
