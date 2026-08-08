package prompts

import (
	"strings"
	"testing"
)

// TestSystemAdditionsComposer_Empty verifies the load-bearing backward
// compat property: an unconfigured composer produces empty output, so
// the framework template's {{.SystemAdditions}} placeholder vanishes
// cleanly and existing snapshot-tested call sites see no change.
func TestSystemAdditionsComposer_Empty(t *testing.T) {
	c := SystemAdditionsComposer{}
	if got := c.Compose(); got != "" {
		t.Errorf("empty composer must produce empty string, got %q", got)
	}
}

// TestSystemAdditionsComposer_OnlyWhitespace — fields with only
// whitespace should be treated as empty (the trim-and-skip path).
func TestSystemAdditionsComposer_OnlyWhitespace(t *testing.T) {
	c := SystemAdditionsComposer{
		DiscoveryContext: "   \n\t  ",
		BrandSpec:        "\n\n",
	}
	if got := c.Compose(); got != "" {
		t.Errorf("whitespace-only fields must compose to empty, got %q", got)
	}
}

// TestSystemAdditionsComposer_SingleField — composing with one field
// produces a clean output without spurious dividers above/below.
func TestSystemAdditionsComposer_SingleField(t *testing.T) {
	c := SystemAdditionsComposer{
		DiscoveryContext: "<discovery-context>audience: exec</discovery-context>",
	}
	got := c.Compose()
	if !strings.Contains(got, "<discovery-context>") {
		t.Errorf("output must contain discovery-context block: %q", got)
	}
	// Exactly one --- divider per non-empty layer; with 1 field
	// we get the leading divider only.
	dividers := strings.Count(got, "\n---\n")
	if dividers != 1 {
		t.Errorf("single-field output should have exactly 1 divider, got %d in %q", dividers, got)
	}
}

// TestSystemAdditionsComposer_LayerOrder — load-bearing test: layers
// MUST appear in the documented order. If the order changes (e.g.
// someone moves checklist before discovery), behavior in
// agent_processor changes silently. Lock it down.
func TestSystemAdditionsComposer_LayerOrder(t *testing.T) {
	// (F2 2026-05-17) AntiSlopProtocol / BrandSpecProtocol removed
	// from the composer (see system_additions.go field block). The
	// layer-order contract now starts at DesignSystem.
	c := SystemAdditionsComposer{
		DesignSystem:     "# DesignSystem 3",
		BrandSpec:        "<brand-spec>4</brand-spec>",
		SkillSideFiles:   "<skill-side-files>5</skill-side-files>",
		DiscoveryContext: "<discovery-context>6</discovery-context>",
		ChecklistDigest:  "<skill-checklist>7</skill-checklist>",
	}
	got := c.Compose()
	// Find each marker; each subsequent one must come after the
	// previous. Using strings.Index gives us byte-offset comparable
	// positions without parsing markdown.
	markers := []string{
		"DesignSystem 3",
		"<brand-spec>", "<skill-side-files>", "<discovery-context>",
		"<skill-checklist>",
	}
	prev := -1
	for _, m := range markers {
		pos := strings.Index(got, m)
		if pos < 0 {
			t.Fatalf("marker %q not in output: %q", m, got)
		}
		if pos <= prev {
			t.Errorf("marker %q out of order (pos=%d, prev=%d)", m, pos, prev)
		}
		prev = pos
	}
}

// TestSystemAdditionsComposer_BrandSpecOverridesDirection — when both
// BrandSpec and SelectedDirection are populated (caller bug, but
// possible), the brand reference wins. Documents the contract.
func TestSystemAdditionsComposer_BrandSpecOverridesDirection(t *testing.T) {
	c := SystemAdditionsComposer{
		BrandSpec:         "<brand-spec>real-brand</brand-spec>",
		SelectedDirection: "<visual-direction>fallback-direction</visual-direction>",
	}
	got := c.Compose()
	if !strings.Contains(got, "real-brand") {
		t.Errorf("brand-spec must appear when both populated: %q", got)
	}
	if strings.Contains(got, "fallback-direction") {
		t.Errorf("direction must be skipped when brand-spec populated: %q", got)
	}
}

// TestSystemAdditionsComposer_DirectionWhenNoBrand — direction is
// emitted when BrandSpec is empty.
func TestSystemAdditionsComposer_DirectionWhenNoBrand(t *testing.T) {
	c := SystemAdditionsComposer{
		SelectedDirection: "<visual-direction>fallback-direction</visual-direction>",
	}
	got := c.Compose()
	if !strings.Contains(got, "fallback-direction") {
		t.Errorf("direction must appear when no brand: %q", got)
	}
}

// TestRenderFrameworkTemplate_BackwardCompat — the load-bearing
// snapshot test for PR-2: when systemAdditions is empty, the rendered
// framework MUST be byte-identical to what the pre-PR-2 path
// produced (modulo the trailing newline normalization). Any drift here
// breaks every existing turn.
func TestRenderFrameworkTemplate_BackwardCompat(t *testing.T) {
	const identity = "# Test mode identity"
	const outputFormat = "## Test output format"

	withAdditions := renderFrameworkTemplate(identity, outputFormat, "")
	if !strings.Contains(withAdditions, identity) {
		t.Error("rendered output missing mode identity")
	}
	if !strings.Contains(withAdditions, outputFormat) {
		t.Error("rendered output missing output format")
	}
	// Empty SystemAdditions must produce no orphan placeholder.
	if strings.Contains(withAdditions, "{{.SystemAdditions}}") {
		t.Error("placeholder leaked into rendered output")
	}
	// And no orphan divider at the tail (the TrimRight contract).
	if strings.HasSuffix(withAdditions, "\n\n\n") {
		t.Error("rendered output has trailing blank padding")
	}
}

// TestRenderFrameworkTemplate_WithAdditions — verifies non-empty
// systemAdditions lands in the rendered output at the expected
// position (after both ModeIdentity and OutputFormat).
func TestRenderFrameworkTemplate_WithAdditions(t *testing.T) {
	const identity = "# Test mode identity"
	const outputFormat = "## Test output format"
	const additions = "\n\n---\n\n<discovery-context>audience: exec</discovery-context>\n"

	got := renderFrameworkTemplate(identity, outputFormat, additions)
	identityPos := strings.Index(got, identity)
	additionsPos := strings.Index(got, "<discovery-context>")
	if identityPos < 0 || additionsPos < 0 {
		t.Fatalf("expected both identity and additions in output, got %q", got)
	}
	if additionsPos <= identityPos {
		t.Errorf("SystemAdditions must appear AFTER ModeIdentity (identity@%d, additions@%d)", identityPos, additionsPos)
	}
}

// TestSystemAdditionsComposer_PreviousCritique — P0 #3 critique
// loop. Pins three contracts:
//  1. PreviousCritique is included when populated
//  2. It sits AFTER DiscoveryContext (freshest signal closest to
//     the user message)
//  3. It sits BEFORE ChecklistDigest (which is post-flight
//     "remember these constraints", not "react to last reply")
func TestSystemAdditionsComposer_PreviousCritique(t *testing.T) {
	c := SystemAdditionsComposer{
		DiscoveryContext: "<discovery-context>audience: exec</discovery-context>",
		PreviousCritique: "<previous-critique>less neon</previous-critique>",
		ChecklistDigest:  "<skill-checklist>p0: shapes</skill-checklist>",
	}
	got := c.Compose()
	for _, marker := range []string{"discovery-context", "previous-critique", "skill-checklist"} {
		if !strings.Contains(got, marker) {
			t.Fatalf("missing marker %q in output: %q", marker, got)
		}
	}
	discPos := strings.Index(got, "<discovery-context>")
	critPos := strings.Index(got, "<previous-critique>")
	checkPos := strings.Index(got, "<skill-checklist>")
	if !(discPos < critPos && critPos < checkPos) {
		t.Errorf("order wrong: discovery@%d critique@%d checklist@%d", discPos, critPos, checkPos)
	}
}

// TestSystemAdditionsComposer_PreviousCritique_StandsAlone — when
// only PreviousCritique is set, the composer must still produce
// non-empty output (no isEmpty() short-circuit). Pin so a future
// "let's collapse empty composers" refactor can't silently drop
// the critique-only case.
func TestSystemAdditionsComposer_PreviousCritique_StandsAlone(t *testing.T) {
	c := SystemAdditionsComposer{
		PreviousCritique: "<previous-critique>swap palette</previous-critique>",
	}
	got := c.Compose()
	if got == "" {
		t.Fatal("critique-only composer produced empty output")
	}
	if !strings.Contains(got, "swap palette") {
		t.Errorf("critique content missing: %q", got)
	}
}

// TestSystemAdditionsComposer_RecipeContext —
// (Removed 2026-05-18 with the Recipe layer retirement.) Recipe-
// specific scene direction now lives in skills/<agent_mode>/SKILL.md;
// the composer no longer carries a RecipeContext field.
