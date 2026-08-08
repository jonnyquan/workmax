package workagent

// agent_surface_general_test.go — pins GeneralSurface's identity
// contract. The values are referenced by DB-side queries (AgentType
// discriminates general vs canvas threads) and HTTP-side idempotency
// (header name + key prefix). Accidentally renaming any of these
// silently breaks credit-reservation lookups or rejects valid client
// requests; the values look like trivial getters but they're really
// load-bearing constants that should never drift without a thoughtful
// migration.

import (
	"testing"
)

func TestGeneralSurface_ToolIsWorkagent(t *testing.T) {
	// "workagent" is the credit-reservation tool name in
	// w_credit_reservation; flipping it to e.g. "agent" or
	// "work_agent" would orphan every in-flight reservation
	// from this tool.
	g := NewGeneralSurface("ppt")
	if g.Tool() != "workagent" {
		t.Errorf("Tool() = %q, want %q", g.Tool(), "workagent")
	}
}

func TestGeneralSurface_AgentModeReflectsConstructor(t *testing.T) {
	cases := []string{"ppt", "flashcard", "writer", "", "any-future-mode"}
	for _, mode := range cases {
		t.Run(mode, func(t *testing.T) {
			g := NewGeneralSurface(mode)
			if g.AgentMode() != mode {
				t.Errorf("AgentMode() = %q, want %q", g.AgentMode(), mode)
			}
		})
	}
}

func TestGeneralSurface_AgentTypeIsGeneralAgent(t *testing.T) {
	// AgentType discriminates general-agent threads from canvas
	// threads in DB queries. Mid-migration this would orphan
	// historical threads from their new surface.
	if GeneralAgentType != "general_agent" {
		t.Errorf("GeneralAgentType = %q, want %q", GeneralAgentType, "general_agent")
	}
	g := NewGeneralSurface("ppt")
	if g.AgentType() != "general_agent" {
		t.Errorf("AgentType() = %q, want %q", g.AgentType(), "general_agent")
	}
}

func TestGeneralSurface_IdempotencyHeaderIsAgentRequestId(t *testing.T) {
	// Frontend posts X-Agent-Request-Id on the agent-chat surface;
	// renaming would silently drop client-supplied idempotency keys.
	g := NewGeneralSurface("ppt")
	if g.IdempotencyHeaderName() != "X-Agent-Request-Id" {
		t.Errorf("IdempotencyHeaderName() = %q, want %q", g.IdempotencyHeaderName(), "X-Agent-Request-Id")
	}
}

func TestGeneralSurface_IdempotencyKeyPrefixIsEmpty(t *testing.T) {
	// Empty prefix because the general work agent owns its own
	// header (no per-tool collision risk on the
	// (uid, idempotency_key) unique index). Canvas uses
	// "canvas_agent:" precisely because three canvas tools share
	// one client header — DO NOT add a prefix here without
	// understanding that contrast.
	g := NewGeneralSurface("ppt")
	if g.IdempotencyKeyPrefix() != "" {
		t.Errorf("IdempotencyKeyPrefix() = %q, want \"\"", g.IdempotencyKeyPrefix())
	}
}

func TestGeneralSurface_ImplementsAgentSurfaceInterface(t *testing.T) {
	// The agent_surface_general.go file has a `var _ AgentSurface =
	// GeneralSurface{}` compile-time assertion. Mirror it here so
	// removing that line still surfaces the contract drift.
	var _ AgentSurface = NewGeneralSurface("ppt")
	var _ AgentSurface = GeneralSurface{}
}
