package workagent

import (
	"strings"
	"testing"

	"server/model"
)

// preflight_brand_budget_test.go — B3 (2026-05-16) coverage for
// the brand-spec token budget. Pins three contracts:
//
//   • A brand whose sections fit comfortably under the budget
//     renders EVERY populated section — no opportunistic dropping.
//   • A brand whose later sections would push past the budget
//     drops them AND emits an `omitted: [...]` line so the agent
//     knows what it didn't see.
//   • The priority order is voice → colors → typography → motion
//     → layout → spacing → components, so a budget-constrained
//     render keeps the most-referenced visual constraints.

func TestFormatBrandSpecXML_AllSectionsFitUnderBudget(t *testing.T) {
	b := &model.Brand{
		Name:       "Lean",
		Slug:       "lean",
		Confirmed:  true,
		Voice:      model.JSONMap{"tone": "kinetic"},
		Colors:     model.JSONMap{"primary": "#FF0000"},
		Typography: model.JSONMap{"heading": "Inter"},
	}
	got := formatBrandSpecXML(b)
	for _, want := range []string{"voice:", "colors:", "typography:"} {
		if !strings.Contains(got, want) {
			t.Errorf("under-budget render is missing %q section in: %q", want, got)
		}
	}
	if strings.Contains(got, "omitted:") {
		t.Errorf("under-budget render must NOT emit omitted: line; got %q", got)
	}
}

func TestFormatBrandSpecXML_OversizedSectionDropsToOmitted(t *testing.T) {
	// A single section whose JSON payload alone would exceed the
	// budget: it must drop (the pre-emit check refuses to render
	// it) and land in the `omitted: [...]` list, while smaller
	// sections continue to emit. Contract: no mid-section
	// truncation — a partial JSON object would confuse the model
	// more than its absence.
	bigValue := strings.Repeat("x", brandSpecPromptBudgetChars+200)
	b := &model.Brand{
		Name:      "Heavy",
		Slug:      "heavy",
		Confirmed: true,
		Voice:     model.JSONMap{"tone": "kinetic"},
		// Oversized — the pre-check drops it entirely.
		Colors: model.JSONMap{"palette": bigValue},
		// Small enough to render even though Colors blew the budget;
		// the loop continues evaluating later sections so a single
		// rogue heavyweight doesn't black-hole everything.
		Typography: model.JSONMap{"heading": "Inter"},
		Motion:     model.JSONMap{"easing": "ease-in-out"},
	}
	got := formatBrandSpecXML(b)

	if !strings.Contains(got, "voice:") {
		t.Errorf("voice must always emit (priority 1); got %q", got)
	}
	if strings.Contains(got, `colors: {"palette":`) {
		t.Errorf("oversized colors section must NOT emit its body; got %q", got)
	}
	if !strings.Contains(got, "omitted:") {
		t.Fatalf("budget-blowing render must emit omitted: line; got %q", got)
	}
	if !strings.Contains(got, "colors") {
		// The omitted: list should reference colors by name.
		t.Errorf("omitted: list must reference dropped 'colors'; got %q", got)
	}
	// Small downstream sections survive.
	for _, kept := range []string{"typography:", "motion:"} {
		if !strings.Contains(got, kept) {
			t.Errorf("small section %q after oversized drop should still emit; got %q", kept, got)
		}
	}
}

func TestFormatBrandSpecXML_RenderUnderBudgetWhenOmittedSectionsExist(t *testing.T) {
	// The total rendered string MUST stay under the soft budget
	// even when omitted-list entries get appended. The
	// `omitted: [...]` line is small enough that adding it after
	// dropping a 4KB section never reintroduces an overflow.
	bigValue := strings.Repeat("x", brandSpecPromptBudgetChars+200)
	b := &model.Brand{
		Name:      "BudgetSafe",
		Slug:      "budget-safe",
		Confirmed: true,
		Voice:     model.JSONMap{"tone": "kinetic"},
		Colors:    model.JSONMap{"palette": bigValue}, // omitted
	}
	got := formatBrandSpecXML(b)
	if len(got) > brandSpecPromptBudgetChars {
		t.Errorf("rendered length %d exceeds brand-spec budget %d; got %q", len(got), brandSpecPromptBudgetChars, got)
	}
}

func TestFormatBrandSpecXML_VoiceFirstUnderTightBudget(t *testing.T) {
	// Voice is the agent's instruction layer — it must land before
	// any visual section even when budget is tight. Pin the
	// priority order by constructing a brand whose colors alone
	// would consume the budget if it went first.
	b := &model.Brand{
		Name:      "Tight",
		Slug:      "tight",
		Confirmed: true,
		Voice:     model.JSONMap{"tone": "kinetic"},
		Colors:    model.JSONMap{"palette": strings.Repeat("c", brandSpecPromptBudgetChars-100)},
	}
	got := formatBrandSpecXML(b)
	voiceIdx := strings.Index(got, "voice:")
	colorsIdx := strings.Index(got, "colors:")
	if voiceIdx == -1 {
		t.Fatalf("voice must emit; got %q", got)
	}
	if colorsIdx != -1 && voiceIdx > colorsIdx {
		t.Errorf("voice must precede colors in render order; voiceIdx=%d colorsIdx=%d", voiceIdx, colorsIdx)
	}
}
