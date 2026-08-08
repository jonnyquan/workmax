package workagent

// GeneralSurface implements AgentSurface for the general work agent
// (not canvas). It represents the surface the existing
// api/pro/tools/workagent.HandleAgentChat handler serves today —
// every ChatThread.AgentMode that isn't "canvas" routes through this
// surface, with the actual per-mode behavior (ppt / flashcard / etc.)
// dispatched inside the handler via the skills.Registry.
//
// This is a Phase 4-prep stub: it captures the surface identity
// (Tool / AgentType / idempotency conventions) so the future shared
// dispatcher (Phase 4 main) can resolve "which surface does this
// turn belong to?" by looking at ChatThread.AgentType. The handler
// itself doesn't consume GeneralSurface yet.
//
// Construction is via NewGeneralSurface(mode) — `mode` mirrors
// ChatThread.AgentMode, which can be any of the registry keys
// (writer, ppt, flashcard, …). All values share the same Tool /
// AgentType / idempotency conventions; only AgentMode varies.
type GeneralSurface struct {
	mode string
}

// NewGeneralSurface returns a GeneralSurface for the given
// ChatThread.AgentMode. Stateless apart from the mode field; safe
// to share or recreate.
func NewGeneralSurface(mode string) GeneralSurface {
	return GeneralSurface{mode: mode}
}

// Compile-time assertion.
var _ AgentSurface = GeneralSurface{}

// GeneralAgentType is the value the general work agent writes to
// ChatThread.AgentType. Distinguishes general-agent threads from
// canvas-agent threads in DB queries.
const GeneralAgentType = "general_agent"

func (g GeneralSurface) Tool() string      { return "workagent" }
func (g GeneralSurface) AgentMode() string { return g.mode }
func (g GeneralSurface) AgentType() string { return GeneralAgentType }

// IdempotencyHeaderName — workagent owns its own header (no other
// tool shares it), so no per-tool prefix is needed.
func (g GeneralSurface) IdempotencyHeaderName() string { return "X-Agent-Request-Id" }
func (g GeneralSurface) IdempotencyKeyPrefix() string  { return "" }
