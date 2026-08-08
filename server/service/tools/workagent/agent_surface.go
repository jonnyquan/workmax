package workagent

import "encoding/json"

// AgentSurface is the per-mode plugin contract that future surface
// dispatchers will use to delegate canvas-specific (or other-surface-
// specific) behavior away from the shared SSE / credit / persistence
// lifecycle. Implementations bundle:
//
//   - identity: the credit-reservation tool name + the ChatThread
//     discriminator values written when a new thread is created.
//
// SurfaceTurn (separate interface) bundles per-turn behaviors —
// cost, attachment materialization, system prompt assembly, result
// enrichment.
//
// Today canvasagent.Surface implements this; GeneralSurface (in
// agent_surface_general.go) registers itself for the general
// work-agent modes (ppt / writer / flashcard / ...). The surface
// registry is consumed by canvas_agent_api.go's LookupSurface call
// site; the general HandleAgentChat handler still bakes its
// per-turn lifecycle (callbacks, persistence, billing) inline
// because the two surfaces' OnDone shapes diverge meaningfully
// enough that a shared TurnRunner would force-fit ~250 lines of
// work-agent-specific logic into a sub-interface that canvas
// barely uses.
//
// The shared lifecycle that DOES make sense — turn-lock acquisition,
// THREAD_BUSY envelope, SSE response headers, marshal-and-frame
// event emission — lives in turn_lifecycle.go and
// turn_sse_emitter.go (B5 Slices 1 and 2a, 2026-05-16). Further
// consolidation is gated on a concrete drift incident: a "bug
// fixed in one handler that should also have landed in the other"
// PR will trigger Slice 2c (TurnRunner). Pre-emptive abstraction
// here doesn't pay.
//
// The contract is intentionally lifecycle-agnostic — no SSE writer,
// no credit reserver, no DB session passes through. Surfaces produce
// values the lifecycle composes, not actions; this keeps lifecycle
// bugs out of surface implementations and surface-specific bugs out
// of the shared lifecycle.
type AgentSurface interface {
	// Tool returns the credit-reservation tool name (e.g.
	// "canvas_agent", "work_agent"). Must be unique per surface so
	// billing audits distinguish surfaces in the usage_record log.
	// Also drives the fallback idempotency-key shape (tool_uid_…).
	Tool() string

	// AgentMode is the value written to ChatThread.AgentMode when a
	// new thread is created. Mirrors the de facto mode discriminator
	// already on the schema.
	AgentMode() string

	// AgentType is the value written to ChatThread.AgentType.
	AgentType() string

	// IdempotencyHeaderName returns the HTTP header the surface
	// inspects for client-supplied idempotency keys. Different
	// surfaces use different headers — canvas uses
	// "X-Canvas-Request-Id" (shared with canvas_chat /
	// canvas_optimize_prompt), the general work agent uses
	// "X-Agent-Request-Id".
	IdempotencyHeaderName() string

	// IdempotencyKeyPrefix returns the prefix prepended to client-
	// supplied idempotency keys to scope them to this surface. Empty
	// string disables prefixing. Canvas needs "canvas_agent:" because
	// three canvas tools share one client header; collision on the
	// (uid, idempotency_key) unique index would short-circuit the
	// second tool's reservation onto the first's. The general work
	// agent uses "" because it owns its own header.
	IdempotencyKeyPrefix() string
}

// SurfaceTurn captures one validated turn's surface-specific
// behaviors. Surfaces produce SurfaceTurns from a typed request
// (canvasagent.NewTurn for Canvas Agent, future
// workagent.NewWorkAgentTurn for general work agent); the shared
// lifecycle calls back for surface-specific concerns at each
// lifecycle stage.
//
// Method order roughly matches the lifecycle stages where they are
// called: cost (reserve credits) → workspace (PrepareAttachments)
// → prompt (BuildSystemPrompt) → SDK call (driven by lifecycle) →
// enrich (EnrichResult).
type SurfaceTurn interface {
	// Cost returns the credit cost for this turn. Computed from the
	// surface's request shape (typically attachment count for now).
	Cost() int

	// PrepareAttachments materializes attachments to disk under
	// workspacePath, returning AgentFileInfo records the SDK can
	// consume. May perform downloads / decoding / sanitization;
	// callers should call this only AFTER the credit reservation
	// completes so a failed attachment build leaves the turn cost
	// reserved (refunded by the failure path).
	PrepareAttachments(workspacePath string) ([]AgentFileInfo, error)

	// BuildSystemPrompt assembles the system prompt for this turn.
	// Surfaces inject their per-turn context (canvas state, ppt
	// outline, etc.) here.
	BuildSystemPrompt() string

	// EnrichResult is called on the SDK result before SSE emission.
	// Canvas Agent attaches remaining_credits here so the frontend
	// can update the credit display without a separate API call.
	// Surfaces that don't need enrichment return result unchanged.
	EnrichResult(result json.RawMessage, uid uint) json.RawMessage
}
