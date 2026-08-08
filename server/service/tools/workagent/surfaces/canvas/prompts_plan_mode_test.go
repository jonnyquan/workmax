package canvas

// Plan-mode protocol tests for the canvas system prompt (#12 plan-
// then-execute, 2026-05-15). The non-plan path is already covered
// by prompts_test.go + prompts_more_test.go; this file pins the
// observable behaviour introduced when planMode=true.
//
// What matters for callers (FE parses propose_plan blocks, BE just
// renders the system prompt):
//   • Without planMode: protocol section is ABSENT.
//   • With planMode: protocol section is PRESENT, mentions
//     `propose_plan`, and is placed BEFORE the skill persona so
//     hand-authored personas can't override the plan contract.
//   • Toggling planMode is a strict superset — every byte of the
//     non-plan prompt still appears, plus the protocol.

import (
	"strings"
	"testing"
)

func TestCanvasAgentSystemPrompt_PlanMode_OmittedByDefault(t *testing.T) {
	got := CanvasAgentSystemPrompt(nil, nil, nil, "", false)
	if strings.Contains(got, "propose_plan") {
		t.Errorf("propose_plan must not leak into non-plan prompts; got:\n%s", got)
	}
	if strings.Contains(got, "Plan Mode (active)") {
		t.Errorf("Plan Mode section must not leak into non-plan prompts; got:\n%s", got)
	}
}

func TestCanvasAgentSystemPrompt_PlanMode_EmitsProtocolSection(t *testing.T) {
	got := CanvasAgentSystemPrompt(nil, nil, nil, "", true)
	for _, mustContain := range []string{
		"## Plan Mode (active)",
		"propose_plan",
		"Emit a SINGLE",
		"DO NOT include a step that asks for clarification",
	} {
		if !strings.Contains(got, mustContain) {
			t.Errorf("plan-mode prompt missing required token %q; got:\n%s", mustContain, got)
		}
	}
}

func TestCanvasAgentSystemPrompt_PlanMode_BeforeSkillPersona(t *testing.T) {
	got := CanvasAgentSystemPrompt(nil, nil, nil, "designer", true)
	planIdx := strings.Index(got, "## Plan Mode (active)")
	skillIdx := strings.Index(got, "## Active Skill Persona: designer")
	if planIdx < 0 || skillIdx < 0 {
		t.Fatalf("expected both sections; planIdx=%d skillIdx=%d\n%s", planIdx, skillIdx, got)
	}
	if planIdx > skillIdx {
		t.Errorf("plan-mode section must come BEFORE skill persona to win precedence; planIdx=%d skillIdx=%d", planIdx, skillIdx)
	}
}

func TestCanvasAgentSystemPrompt_PlanMode_IsStrictSuperset(t *testing.T) {
	// Toggling planMode adds content but never removes content. A
	// regression that conditionally suppressed canvas state or
	// the base prompt under plan mode would break the FE's mental
	// model (still expects "Elements on canvas:" etc.).
	base := CanvasAgentSystemPrompt(nil, nil, nil, "", false)
	planned := CanvasAgentSystemPrompt(nil, nil, nil, "", true)
	if !strings.Contains(planned, base) {
		t.Errorf("plan-mode prompt must be a strict superset of the non-plan prompt")
	}
	if len(planned) <= len(base) {
		t.Errorf("plan-mode prompt should be longer; base=%d plan=%d", len(base), len(planned))
	}
}

func TestCanvasAgentSystemPrompt_PlanMode_AllowedToolsListed(t *testing.T) {
	// The protocol enumerates the canvas tools the model
	// may reference inside a plan step. Pin all of them so a
	// future edit that drops one (e.g. move_element) surfaces
	// here rather than as a silent FE drop-through.
	got := CanvasAgentSystemPrompt(nil, nil, nil, "", true)
	for _, toolName := range []string{
		"generate_image",
		"create_element",
		"edit_element",
		"delete_element",
		"move_element",
		"resize_element",
		"create_workflow",
		"run_workflow",
		"branch_workflow",
		"explain_workflow",
		"add_workflow_node",
		"update_workflow_node",
		"connect_workflow_nodes",
		"delete_workflow_node",
		"delete_workflow_edge",
		"run_workflow_from_node",
	} {
		if !strings.Contains(got, toolName) {
			t.Errorf("plan-mode prompt must list allowed tool %q; got:\n%s", toolName, got)
		}
	}
}
