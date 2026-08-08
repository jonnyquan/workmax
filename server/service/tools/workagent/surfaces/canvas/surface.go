package canvas

import (
	"encoding/json"
	"fmt"

	"server/globals"
	accountService "server/service/account"
	workagentService "server/service/tools/workagent"
)

// Surface implements workagent.AgentSurface for the Canvas Agent.
// Stateless — safe to share across requests or recreate per request.
//
// The handler in api/pro/tools/canvas_agent_api.go uses Surface to
// answer "which tool / agent_mode / agent_type identifies this
// surface?" and constructs a Turn (via NewTurn) per request to drive
// the per-turn behaviors (cost, prompt, attachments, result enrich).
type Surface struct{}

// NewSurface returns a stateless Canvas Agent surface.
func NewSurface() Surface { return Surface{} }

// Compile-time assertion that Surface satisfies workagent.AgentSurface.
var _ workagentService.AgentSurface = Surface{}

func (Surface) Tool() string      { return "canvas_agent" }
func (Surface) AgentMode() string { return CanvasAgentMode }
func (Surface) AgentType() string { return CanvasAgentType }

// IdempotencyHeaderName — canvas reuses the shared canvas-tool header
// across canvas_chat, canvas_optimize_prompt, and canvas_agent.
// IdempotencyKeyPrefix is the per-tool scope token that prevents the
// same client header from collapsing reservations across the three
// tools (which would short-circuit the second tool's reservation
// onto the first's row).
func (Surface) IdempotencyHeaderName() string { return "X-Canvas-Request-Id" }
func (Surface) IdempotencyKeyPrefix() string  { return "canvas_agent:" }

// NewTurn wraps a parsed + validated CanvasAgentChatRequest in a
// SurfaceTurn the shared lifecycle can drive. The handler calls
// NewTurn AFTER its inline body parse + ValidateRequest, so all
// turn methods see only valid input.
func NewTurn(req CanvasAgentChatRequest) workagentService.SurfaceTurn {
	return &canvasTurn{req: req}
}

// canvasTurn implements workagent.SurfaceTurn for one Canvas Agent
// request. Holds the parsed request and exposes the surface-specific
// concerns the shared lifecycle calls back into.
type canvasTurn struct {
	req CanvasAgentChatRequest
}

// Compile-time assertion.
var _ workagentService.SurfaceTurn = (*canvasTurn)(nil)

// Cost returns the canvas-specific cost for this turn (attachment
// count drives it, per pricing.AgentCost).
func (t *canvasTurn) Cost() int {
	return AgentCost(len(t.req.Attachments))
}

// PrepareAttachments delegates to the existing strict variant;
// invalid attachments surface as a structured error rather than
// being silently dropped. Caller (the handler) treats the error as
// a per-attachment validation failure and emits the canonical
// agent-failure payload.
func (t *canvasTurn) PrepareAttachments(workspacePath string) ([]workagentService.AgentFileInfo, error) {
	return PrepareAttachmentsStrict(t.req.Attachments, workspacePath)
}

// BuildSystemPrompt assembles the canvas-context-aware system
// prompt: base canvas-agent persona + canvas state injection
// (elements / selected ids / viewport) + optional skill persona
// + plan-mode protocol when CanvasAgentContext.PlanMode is true
// (#12 plan-then-execute, 2026-05-15).
func (t *canvasTurn) BuildSystemPrompt() string {
	return CanvasAgentSystemPrompt(
		t.req.Context.Elements,
		t.req.Context.SelectedIDs,
		t.req.Context.Viewport,
		t.req.Context.Skill,
		t.req.Context.PlanMode,
	)
}

// EnrichResult attaches the user's remaining credit balance to the
// SDK result before SSE emission. The frontend reads the
// `remaining_credits` field on the Done event to update the credit
// display without a separate API call.
//
// Failure modes (DB read fails, result isn't a JSON object) log a
// warning and pass result through unchanged — never block the SSE
// stream because we couldn't compute a side decoration.
func (t *canvasTurn) EnrichResult(result json.RawMessage, uid uint) json.RawMessage {
	_, _, remaining, err := accountService.NewCreditsPackService().GetBalance(int(uid))
	if err != nil {
		globals.Warn(fmt.Sprintf("[CanvasAgent] Failed to read remaining credits for uid=%d: %v", uid, err))
		return result
	}
	payload := map[string]interface{}{}
	if len(result) > 0 {
		if err := json.Unmarshal(result, &payload); err != nil {
			globals.Warn(fmt.Sprintf("[CanvasAgent] result is not a JSON object, skipping remaining credits attach: %v", err))
			return result
		}
	}
	payload["remaining_credits"] = remaining
	payload["remainingCredits"] = remaining

	data, err := json.Marshal(payload)
	if err != nil {
		return result
	}
	return json.RawMessage(data)
}
