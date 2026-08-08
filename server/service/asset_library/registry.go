package asset_library

import (
	"fmt"
	"sync"
)

// Registry is the lookup table mapping AssetKind → Descriptor. The
// generic preflight loader, generic REST handler, and frontend list
// component all walk this registry instead of branching per kind —
// adding a fourth asset type becomes a Register call, not a per-layer
// copy-paste.
//
// Concurrency: the registry mutates only at process startup
// (descriptors register from init() blocks in their respective
// concrete files). Reads happen on every chat turn / library page
// load. The mutex therefore protects the registration window; reads
// could be lock-free against an immutable map after init, but the
// performance delta is microseconds and the explicit lock keeps the
// "registration must complete before reads" contract obvious to a
// future maintainer.
type Registry struct {
	mu          sync.RWMutex
	descriptors map[AssetKind]Descriptor
}

// NewRegistry returns an empty registry. Production uses the package-
// level Default() singleton; tests construct fresh registries to
// isolate descriptor registration from other tests.
func NewRegistry() *Registry {
	return &Registry{
		descriptors: make(map[AssetKind]Descriptor, 4),
	}
}

// Register installs a descriptor under its declared Kind(). Idempotent
// re-registration silently overwrites — useful for tests that
// re-register fakes; production init() blocks each register exactly
// once so the overwrite path never fires.
//
// nil descriptor or empty Kind() are ignored (defensive — neither
// produces a useful registry entry).
func (r *Registry) Register(d Descriptor) {
	if d == nil {
		return
	}
	kind := d.Kind()
	if kind == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.descriptors[kind] = d
}

// Get returns the descriptor for the given kind, or an error when
// no descriptor is registered. Caller is expected to have validated
// kind via AssetKind.IsValid() upstream — the error here is a
// defensive backstop, not the primary 400-fast path.
func (r *Registry) Get(kind AssetKind) (Descriptor, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.descriptors[kind]
	if !ok {
		return nil, fmt.Errorf("asset_library: no descriptor registered for kind %q", kind)
	}
	return d, nil
}

// All returns the registered descriptors in canonical kind order
// (AllKinds()). Skips kinds whose descriptors haven't registered yet
// — useful during the Sprint-D rollout when only some kinds have
// migrated. Callers iterating for "every asset type" (preflight
// asset-library-index, library-shell tab list) use this so the
// canonical brand → character → director-style ordering is stable.
func (r *Registry) All() []Descriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Descriptor, 0, len(r.descriptors))
	for _, kind := range AllKinds() {
		if d, ok := r.descriptors[kind]; ok {
			out = append(out, d)
		}
	}
	return out
}

// Has reports whether a descriptor is registered for kind. Convenience
// for code paths that branch on registration status (mid-rollout
// preflight: walk new registry when registered, fall through to
// legacy hard-coded loader otherwise).
func (r *Registry) Has(kind AssetKind) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.descriptors[kind]
	return ok
}

// defaultRegistry is the package-level singleton. Concrete
// descriptors register here from their init() blocks; production
// callers reach it via Default().
var defaultRegistry = NewRegistry()

// Default returns the process-wide registry. Tests that need
// isolation construct their own Registry via NewRegistry() instead.
func Default() *Registry {
	return defaultRegistry
}
