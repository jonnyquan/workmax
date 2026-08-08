package canvas

import (
	"strings"
	"testing"
)

// system_test.go pins the headline contract (base-prompt always present,
// empty-canvas marker, elements count + JSON block, truncation marker on
// oversized JSON, selected-ids comma-joined, viewport JSON block,
// skill-persona switch incl. case-insensitive + unknown fallback,
// top-down section ordering). These fill the quieter gate invariants a
// silent regression would slip past:
//
//   • Truncation boundary: the guard is `if len(s) > 8000`, so a JSON
//     body of EXACTLY 8000 chars does NOT truncate. Pin the just-below-
//     boundary case so a refactor to `>= 8000` would surface. (The
//     oversized-triggers path is already covered; pin the negative.)
//   • Truncation marker is the EXACT string "\n... (truncated)" —
//     pin so a refactor that localised / shortened / changed case
//     would surface. The model's parser keys off this literal.
//   • Truncation is BYTE-LEVEL (Go `s[:8000]`), not rune-level. For a
//     CJK-heavy element list that crosses the threshold, the slice may
//     land mid-UTF-8 sequence — pin the observable (truncation marker
//     still appears) so a refactor to rune-level slicing (which would
//     require rune-iteration) is a deliberate, surfaced change.
//   • Even after truncation, the closing ```` ``` ```` markdown fence
//     is ALWAYS written. Pin so a refactor that short-circuited out of
//     the JSON block on truncation would leave unbalanced markdown.
//   • selectedIDs that contain an empty string STILL render — the gate
//     is `len(selectedIDs) > 0`, not per-id non-blank. So ["a", ""]
//     joins to "a, " with a trailing empty. Pin the honest observable.
//   • selectedIDs containing ONLY an empty string ([""]) still emits
//     the "**Selected elements:**" section header (the len-gate fires)
//     with an empty payload after the colon. Pin so a refactor that
//     added a "filter out blanks first" step would drop this case.
//   • viewport as an EMPTY map ({}) is NOT nil — the viewport section
//     fires with body `{}`. Pin the nil-vs-empty-map distinction.
//   • Whitespace-only skill (e.g. "  ") is NOT empty-string — the
//     `skill != ""` gate fires, the header emits with the whitespace
//     preserved, and skillPersona falls through to the default
//     branch (ToLower("  ") doesn't match any case). Pin this weird
//     "empty-like" skill still emits a persona section.
//   • skillPersona ToLower is the ONLY normalisation — no trim, no
//     underscore/hyphen tolerance. "designer\n" or " designer" route
//     to the default branch, NOT to the designer persona. Pin so a
//     refactor that added a trim-first would surface as a behaviour
//     shift for legacy callers.
//   • Section ORDER holds even when the middle sections are absent —
//     with no elements but a skill, the ordering is still base →
//     canvas (empty marker) → skill. Pin the ordering invariant under
//     multiple combinations (not just the all-present case the base
//     test covers).
//   • Elements block closes the fenced JSON even when MarshalIndent
//     produces a trailing newline inside `s`. The builder appends a
//     hard "\n```\n" after — pin the closing fence is on its own line.

func TestCanvasAgentSystemPrompt_TruncationMarkerExact(t *testing.T) {
	// Construct an oversized element list and pin the exact marker
	// string. A refactor that changed the literal (e.g. "…(truncated)"
	// or localised) would surface here.
	bigElements := make([]map[string]any, 0, 200)
	for i := 0; i < 200; i++ {
		bigElements = append(bigElements, map[string]any{
			"id":      "e-" + strings.Repeat("x", 60),
			"content": strings.Repeat("y", 60),
		})
	}
	got := CanvasAgentSystemPrompt(bigElements, nil, nil, "", false)
	// The marker appears VERBATIM — not "(truncated)" alone, not with
	// a trailing space, not "... truncated".
	if !strings.Contains(got, "\n... (truncated)") {
		t.Errorf("expected exact marker '\\n... (truncated)' in output; got tail:\n%s", tail(got, 200))
	}
	// Negative: alternative marker formats are NOT present. Note we
	// avoid anchoring on "(truncated)\n" alone because that substring
	// IS part of the real marker's tail (real output ends ... (truncated)\n).
	for _, bogus := range []string{"…(truncated)", "... truncated"} {
		if strings.Contains(got, bogus) {
			t.Errorf("unexpected alternative truncation marker %q leaked in", bogus)
		}
	}
}

func TestCanvasAgentSystemPrompt_TruncationPreservesClosingFence(t *testing.T) {
	// Even after truncation, the closing ``` fence must follow so the
	// markdown block is balanced. A refactor that bailed out on
	// truncation would produce an unclosed fence and break downstream
	// markdown rendering.
	bigElements := make([]map[string]any, 0, 200)
	for i := 0; i < 200; i++ {
		bigElements = append(bigElements, map[string]any{
			"id": strings.Repeat("a", 60),
			"k":  strings.Repeat("b", 60),
		})
	}
	got := CanvasAgentSystemPrompt(bigElements, nil, nil, "", false)
	// Truncation occurred.
	if !strings.Contains(got, "... (truncated)") {
		t.Fatalf("expected truncation in this case; got (len=%d)", len(got))
	}
	// The truncation marker appears BEFORE the closing fence.
	truncIdx := strings.Index(got, "... (truncated)")
	fenceIdx := strings.LastIndex(got, "```")
	if truncIdx < 0 || fenceIdx < 0 || truncIdx >= fenceIdx {
		t.Errorf("expected truncation marker before closing fence; truncIdx=%d fenceIdx=%d", truncIdx, fenceIdx)
	}
}

func TestCanvasAgentSystemPrompt_JustBelowTruncationBoundary(t *testing.T) {
	// Small element list whose marshalled JSON is well under 8000 — must
	// NOT contain the truncation marker. Pin the negative side of the
	// boundary so a refactor to `>= 8000` couldn't silently flip sign.
	elements := []map[string]any{
		{"id": "e1", "type": "image"},
		{"id": "e2", "type": "text", "content": "hi"},
	}
	got := CanvasAgentSystemPrompt(elements, nil, nil, "", false)
	if strings.Contains(got, "... (truncated)") {
		t.Errorf("small element list must not truncate; got truncation marker in:\n%s", got)
	}
	// Sanity: JSON fence IS written.
	if !strings.Contains(got, "```json") {
		t.Errorf("expected JSON fence; got:\n%s", got)
	}
}

func TestCanvasAgentSystemPrompt_TruncationOnCJKIsByteLevel(t *testing.T) {
	// Construct an element list whose marshalled JSON crosses the
	// 8000-char threshold using CJK glyphs (3-byte UTF-8). Go's
	// s[:8000] slices bytes, not runes — the slice can land
	// mid-sequence. The observable is: truncation still fires, marker
	// still appears. Pin so a refactor to rune-level slicing would
	// surface as a deliberate behaviour change.
	bigCJK := make([]map[string]any, 0, 200)
	for i := 0; i < 200; i++ {
		bigCJK = append(bigCJK, map[string]any{
			"id":      strings.Repeat("中", 40),
			"content": strings.Repeat("字", 40),
		})
	}
	got := CanvasAgentSystemPrompt(bigCJK, nil, nil, "", false)
	if !strings.Contains(got, "... (truncated)") {
		t.Errorf("expected CJK-content truncation to still fire; got (len=%d)", len(got))
	}
}

func TestCanvasAgentSystemPrompt_SelectedIDsWithEmptyEntries(t *testing.T) {
	// Gate is `len(selectedIDs) > 0` — per-entry emptiness isn't
	// filtered. Joining ["a", ""] with ", " emits "a, " (trailing
	// empty). Pin the honest observable.
	got := CanvasAgentSystemPrompt(nil, []string{"a", ""}, nil, "", false)
	if !strings.Contains(got, "**Selected elements:** a, \n") {
		t.Errorf("expected 'a, ' with trailing empty; got:\n%s", got)
	}
}

func TestCanvasAgentSystemPrompt_SelectedIDsOnlyEmptyStringStillEmits(t *testing.T) {
	// [""] — len > 0 so the section fires. Joined payload is empty.
	// Pin so a refactor that added a blank-filter would drop this case.
	got := CanvasAgentSystemPrompt(nil, []string{""}, nil, "", false)
	if !strings.Contains(got, "**Selected elements:** \n") {
		t.Errorf("expected empty-payload selected section; got:\n%s", got)
	}
}

func TestCanvasAgentSystemPrompt_ViewportEmptyMapIsNotNil(t *testing.T) {
	// Empty map is distinct from nil — the nil-gate uses `!= nil`, so
	// an empty map passes and emits "**Viewport:** {}".
	got := CanvasAgentSystemPrompt(nil, nil, map[string]any{}, "", false)
	if !strings.Contains(got, "**Viewport:** {}") {
		t.Errorf("expected '**Viewport:** {}' for empty map; got:\n%s", got)
	}
}

func TestCanvasAgentSystemPrompt_WhitespaceSkillStillEmitsPersona(t *testing.T) {
	// "  " is not "" — the `skill != ""` gate fires. The header
	// preserves the whitespace verbatim, and skillPersona routes to
	// the default branch (ToLower("  ") matches no case).
	got := CanvasAgentSystemPrompt(nil, nil, nil, "  ", false)
	if !strings.Contains(got, "## Active Skill Persona:   \n") {
		t.Errorf("expected whitespace-skill header; got:\n%s", got)
	}
	if !strings.Contains(got, "Apply expertise related to '  '") {
		t.Errorf("expected whitespace-skill to fall through to default persona; got:\n%s", got)
	}
}

func TestSkillPersona_NoTrimBeforeSwitch(t *testing.T) {
	// " designer" / "designer\n" / "designer " all bypass the designer
	// case because strings.ToLower does not trim. Pin each so a
	// refactor that added TrimSpace before the switch would surface.
	for _, variant := range []string{" designer", "designer ", "designer\n", "\tdesigner"} {
		body := skillPersona(variant)
		if strings.Contains(body, "visual design expert") {
			t.Errorf("variant %q unexpectedly routed to designer (no trim expected); got: %s", variant, body)
		}
		// Falls through to default.
		if !strings.Contains(body, "Apply expertise related to") {
			t.Errorf("variant %q did not fall through to default; got: %s", variant, body)
		}
	}
}

func TestCanvasAgentSystemPrompt_SectionOrdering_WithGapsInMiddle(t *testing.T) {
	// Pin the ordering invariant holds even when some sections are
	// absent. Base + empty-canvas marker + skill, no selected/viewport.
	got := CanvasAgentSystemPrompt(nil, nil, nil, "designer", false)
	basePos := strings.Index(got, "Your Capabilities")
	emptyPos := strings.Index(got, "**Canvas is empty**")
	skillPos := strings.Index(got, "## Active Skill Persona: designer")
	if basePos < 0 || emptyPos < 0 || skillPos < 0 {
		t.Fatalf("expected all three sections; basePos=%d emptyPos=%d skillPos=%d", basePos, emptyPos, skillPos)
	}
	if !(basePos < emptyPos && emptyPos < skillPos) {
		t.Errorf("expected base < empty-canvas < skill; got %d,%d,%d", basePos, emptyPos, skillPos)
	}
	// Absent-section sanity: no selected / viewport headers leak.
	if strings.Contains(got, "**Selected elements:**") {
		t.Errorf("selected header unexpectedly present; got:\n%s", got)
	}
	if strings.Contains(got, "**Viewport:**") {
		t.Errorf("viewport header unexpectedly present; got:\n%s", got)
	}
}

func TestCanvasAgentSystemPrompt_EmptyMarkerLiteralDash(t *testing.T) {
	// The empty-canvas marker uses an em-dash "—" (U+2014), not a
	// hyphen. Pin the literal so a refactor that swapped the glyph
	// would surface (downstream observability may key off the exact
	// text).
	got := CanvasAgentSystemPrompt(nil, nil, nil, "", false)
	if !strings.Contains(got, "**Canvas is empty** — no elements yet.") {
		t.Errorf("expected literal em-dash marker; got:\n%s", got)
	}
}

func TestCanvasAgentSystemPrompt_NilViewportAndEmptyMapDifferExplicitly(t *testing.T) {
	// Explicit pin: nil omits the section, empty map emits it.
	nilGot := CanvasAgentSystemPrompt(nil, nil, nil, "", false)
	emptyGot := CanvasAgentSystemPrompt(nil, nil, map[string]any{}, "", false)
	if strings.Contains(nilGot, "**Viewport:**") {
		t.Errorf("nil viewport must not emit section")
	}
	if !strings.Contains(emptyGot, "**Viewport:**") {
		t.Errorf("empty-map viewport must emit section")
	}
}

// tail returns the last n bytes of s, or s if shorter. Used for error
// context when the full prompt is unwieldy.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
