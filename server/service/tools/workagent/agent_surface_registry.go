package workagent

import "sync"

// surfaceRegistry holds AgentSurface implementations keyed by their
// AgentMode discriminator. Surfaces register themselves at process
// startup via init() blocks (see workagent/surfaces/canvas/init.go).
// Canvas's handler consumes this via LookupSurface(canvas.
// CanvasAgentMode); the general work-agent handler reaches its
// GeneralSurface directly because the rest of its lifecycle stays
// inline (see agent_surface.go header for why a unified dispatcher
// isn't worth its weight yet).
//
// RWMutex guards both reads and writes — registry mutations only
// happen at init time so contention is nil, but RWMutex documents
// the read-heavy access pattern correctly. Map entries are never
// removed.
var (
	surfaceRegistryMu sync.RWMutex
	surfaceRegistry   = map[string]AgentSurface{}
)

// RegisterSurface adds a surface to the global registry under its
// AgentMode key. Typically called from init() in surface packages
// so the registration runs once at process startup, before any
// handler can call LookupSurface. Re-registering the same mode
// silently overwrites — production callers should never do this,
// but tests use it to swap in mocks.
func RegisterSurface(s AgentSurface) {
	surfaceRegistryMu.Lock()
	defer surfaceRegistryMu.Unlock()
	surfaceRegistry[s.AgentMode()] = s
}

// LookupSurface returns the registered surface for the given
// AgentMode, or nil when no surface is registered for that mode.
// Callers must nil-check the return — the dispatcher treats a nil
// surface as "this mode is not enabled in this build" and emits a
// structured error to the client.
func LookupSurface(agentMode string) AgentSurface {
	surfaceRegistryMu.RLock()
	defer surfaceRegistryMu.RUnlock()
	return surfaceRegistry[agentMode]
}
