package canvas

import (
	"strings"
	"testing"
)

// CanvasAgentSystemPrompt is the sole system-prompt builder for the
// Canvas Agent. It concatenates a static base prompt with the per-request
// canvas state (elements count, selected ids, viewport) and an optional
// skill persona. Every downstream LLM call reads what this function
// returns, so the tests pin both the structural invariants (known
// sections are always emitted in order) and the truncation guard that
// prevents a pathologically large canvas from bursting the model context.

func TestCanvasAgentSystemPrompt_IncludesBasePrompt(t *testing.T) {
	// basePrompt is the load-bearing constant — it spells out the
	// coordinate system, the tool catalogue, and the formatting rules.
	// The builder must always emit it or the agent loses all guard rails.
	got := CanvasAgentSystemPrompt(nil, nil, nil, "", false)
	if !strings.Contains(got, "You are Canvas Agent for workmax.app") {
		t.Errorf("expected base prompt preamble in output; got:\n%s", got)
	}
	if !strings.Contains(got, "## Your Capabilities") {
		t.Errorf("expected capabilities section; got:\n%s", got)
	}
	if !strings.Contains(got, "## Coordinate System") {
		t.Errorf("expected coordinate-system section; got:\n%s", got)
	}
}

func TestCanvasAgentSystemPrompt_EmitsEmptyCanvasMarkerWhenNoElements(t *testing.T) {
	// When the canvas has no elements, the builder must say so
	// explicitly rather than silently omitting the section — the
	// model uses the explicit marker to disambiguate "no context"
	// from "context not included".
	got := CanvasAgentSystemPrompt(nil, nil, nil, "", false)
	if !strings.Contains(got, "**Canvas is empty**") {
		t.Errorf("expected empty-canvas marker; got:\n%s", got)
	}
}

func TestCanvasAgentSystemPrompt_IncludesElementsCountAndJSON(t *testing.T) {
	// The elements block advertises BOTH a human-readable count and
	// a JSON dump so the model can reason about identity/size. Both
	// must be present — a refactor that dropped the count would make
	// the model re-count by hand every turn.
	elements := []map[string]any{
		{"id": "e1", "type": "image", "x": 0, "y": 0},
		{"id": "e2", "type": "text", "content": "hello"},
	}
	got := CanvasAgentSystemPrompt(elements, nil, nil, "", false)
	if !strings.Contains(got, "**Elements on canvas:** 2") {
		t.Errorf("expected element count header; got:\n%s", got)
	}
	if !strings.Contains(got, "```json") {
		t.Errorf("expected fenced JSON block; got:\n%s", got)
	}
	if !strings.Contains(got, `"id": "e1"`) || !strings.Contains(got, `"id": "e2"`) {
		t.Errorf("expected element ids rendered in JSON; got:\n%s", got)
	}
}

func TestCanvasAgentSystemPrompt_TruncatesHugeElementsJSON(t *testing.T) {
	// Canvas documents grow unbounded in real user sessions. Past
	// ~8000 chars the JSON alone risks blowing out the LLM context
	// window. The builder truncates and appends a marker so the
	// model knows the list is incomplete.
	bigElements := make([]map[string]any, 0, 200)
	for i := 0; i < 200; i++ {
		bigElements = append(bigElements, map[string]any{
			"id":      "e-" + strings.Repeat("x", 60),
			"content": strings.Repeat("y", 60),
		})
	}
	got := CanvasAgentSystemPrompt(bigElements, nil, nil, "", false)
	if !strings.Contains(got, "... (truncated)") {
		t.Errorf("expected truncation marker for oversized element list; got (len=%d):\n%s", len(got), got)
	}
}

func TestCanvasAgentSystemPrompt_JoinsSelectedIDsWithCommaSpace(t *testing.T) {
	// The "Selected elements" line is what the agent reads when the
	// user says "make this bigger". Joining with ", " matches how
	// the Claude-family models habitually parse lists; a different
	// separator would work too, but pin the exact one we've chosen.
	got := CanvasAgentSystemPrompt(nil, []string{"a", "b", "c"}, nil, "", false)
	if !strings.Contains(got, "**Selected elements:** a, b, c") {
		t.Errorf("expected comma-joined selected ids; got:\n%s", got)
	}
}

func TestCanvasAgentSystemPrompt_OmitsSelectedIDsSectionWhenEmpty(t *testing.T) {
	got := CanvasAgentSystemPrompt(nil, nil, nil, "", false)
	if strings.Contains(got, "**Selected elements:**") {
		t.Errorf("selected-elements section must not appear when ids is empty; got:\n%s", got)
	}
}

func TestCanvasAgentSystemPrompt_EmitsViewportWhenProvided(t *testing.T) {
	// Viewport shows up as raw JSON so the model can compute the
	// viewport-centered drop coordinates the base prompt references
	// (viewport.x + 600/scale, etc.).
	vp := map[string]any{"x": 100, "y": 200, "scale": 1.5}
	got := CanvasAgentSystemPrompt(nil, nil, vp, "", false)
	if !strings.Contains(got, "**Viewport:**") {
		t.Errorf("expected viewport header; got:\n%s", got)
	}
	// JSON ordering isn't guaranteed — just check the numbers are there.
	for _, frag := range []string{`"x":100`, `"y":200`, `"scale":1.5`} {
		if !strings.Contains(got, frag) {
			t.Errorf("expected viewport JSON fragment %q; got:\n%s", frag, got)
		}
	}
}

func TestCanvasAgentSystemPrompt_OmitsViewportWhenNil(t *testing.T) {
	got := CanvasAgentSystemPrompt(nil, nil, nil, "", false)
	if strings.Contains(got, "**Viewport:**") {
		t.Errorf("viewport header must not appear when viewport is nil; got:\n%s", got)
	}
}

func TestCanvasAgentSystemPrompt_AppendsSkillPersonaSection(t *testing.T) {
	got := CanvasAgentSystemPrompt(nil, nil, nil, "designer", false)
	if !strings.Contains(got, "## Active Skill Persona: designer") {
		t.Errorf("expected skill header; got:\n%s", got)
	}
	if !strings.Contains(got, "visual design expert") {
		t.Errorf("expected designer persona body; got:\n%s", got)
	}
}

func TestCanvasAgentSystemPrompt_OmitsSkillSectionWhenEmpty(t *testing.T) {
	// Empty-string skill means "no persona" — skip the section
	// entirely rather than emitting a bare "## Active Skill Persona: "
	// header which would confuse the model.
	got := CanvasAgentSystemPrompt(nil, nil, nil, "", false)
	if strings.Contains(got, "Active Skill Persona") {
		t.Errorf("skill section must not appear when skill is empty; got:\n%s", got)
	}
}

func TestSkillPersona_KnownSkillsHaveDistinctBodies(t *testing.T) {
	// Each skill must yield a different persona body — copy/paste
	// drift would silently merge two personas into one and strip
	// the intended specialisation.
	skills := []string{"designer", "illustrator", "brand", "ux", "photo"}
	seen := make(map[string]string, len(skills))
	for _, s := range skills {
		body := skillPersona(s)
		if body == "" {
			t.Errorf("skill %q produced empty persona body", s)
		}
		for other, prev := range seen {
			if prev == body {
				t.Errorf("skill %q and %q share identical persona body", s, other)
			}
		}
		seen[s] = body
	}
}

func TestSkillPersona_IsCaseInsensitive(t *testing.T) {
	// The switch lowercases the key so callers can pass "Designer"
	// or "DESIGNER" and still land on the same persona. A refactor
	// that dropped the ToLower would route "Designer" to the default
	// "Apply expertise related to..." branch.
	for _, variant := range []string{"designer", "Designer", "DESIGNER", "DeSiGnEr"} {
		body := skillPersona(variant)
		if !strings.Contains(body, "visual design expert") {
			t.Errorf("skill variant %q failed to route to designer persona; got: %s", variant, body)
		}
	}
}

func TestSkillPersona_UnknownSkillFallsBackToGenericLine(t *testing.T) {
	// An unknown skill name flows into the default branch which
	// interpolates the raw key into a generic "Apply expertise..."
	// line. This keeps the agent calibrated even for new skills
	// that haven't been added to the switch yet.
	body := skillPersona("completely-new-skill")
	if !strings.Contains(body, "Apply expertise related to 'completely-new-skill'") {
		t.Errorf("expected fallback persona for unknown skill; got: %s", body)
	}
}

func TestCanvasAgentSystemPrompt_SectionOrdering(t *testing.T) {
	// The prompt reads top-down into the model's attention: base
	// prompt → canvas state → selected ids → viewport → skill persona.
	// Reordering would change how the model prioritises context;
	// pin the order explicitly.
	elements := []map[string]any{{"id": "e1"}}
	vp := map[string]any{"x": 0}
	got := CanvasAgentSystemPrompt(elements, []string{"e1"}, vp, "designer", false)

	basePos := strings.Index(got, "Your Capabilities")
	canvasPos := strings.Index(got, "## Current Canvas State")
	selectedPos := strings.Index(got, "**Selected elements:**")
	viewportPos := strings.Index(got, "**Viewport:**")
	skillPos := strings.Index(got, "## Active Skill Persona")

	for _, tc := range []struct {
		name string
		a    int
		b    int
	}{
		{"base before canvas state", basePos, canvasPos},
		{"canvas before selected", canvasPos, selectedPos},
		{"selected before viewport", selectedPos, viewportPos},
		{"viewport before skill", viewportPos, skillPos},
	} {
		if tc.a < 0 || tc.b < 0 {
			t.Errorf("%s: section not found (a=%d b=%d)", tc.name, tc.a, tc.b)
			continue
		}
		if tc.a >= tc.b {
			t.Errorf("%s: expected a<b, got a=%d b=%d", tc.name, tc.a, tc.b)
		}
	}
}
