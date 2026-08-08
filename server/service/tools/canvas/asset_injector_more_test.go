package canvas

// Pins the three small pure helpers in asset_injector.go that the
// existing asset_injector_test.go only exercises transitively via
// ApplyAssetContext — joinPromptSegments, nonEmpty, and the empty-input
// branch of dedupIDs (the pre-existing test only covers a populated
// case, missing the nil-return-on-empty contract).
//
// These are the building blocks of the enriched-prompt string that the
// model sees, so a silent regression would ship corrupted prompts
// downstream rather than fail fast at the injection layer.

import (
	"reflect"
	"testing"
)

func TestDedupIDs_EmptyInputReturnsNil(t *testing.T) {
	// Empty slice must return nil — not an empty []int. Callers that
	// serialize the result to JSON would otherwise emit `[]` instead
	// of dropping the key, and canvasService assertions downstream
	// distinguish "no ids attached" (nil) from "explicit empty list".
	if got := dedupIDs(nil); got != nil {
		t.Errorf("dedupIDs(nil) = %v, want nil", got)
	}
	if got := dedupIDs([]int{}); got != nil {
		t.Errorf("dedupIDs([]) = %v, want nil", got)
	}
}

func TestDedupIDs_AllNonPositiveReturnsEmptyButNonNil(t *testing.T) {
	// When every element is filtered out, we still allocated `out`
	// (via make with cap=len(ids)) so the return is a non-nil but
	// empty slice. Pin the distinction so a refactor that collapsed
	// the two empty-cases doesn't silently change the output.
	got := dedupIDs([]int{0, -1, -5})
	if got == nil {
		t.Fatalf("dedupIDs([0,-1,-5]) = nil, want empty-but-non-nil")
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0 (got %v)", len(got), got)
	}
}

func TestDedupIDs_PreservesFirstOccurrenceOrder(t *testing.T) {
	// Dedup must keep the first occurrence and drop later duplicates
	// so prompt-suffix ordering is deterministic — the model conditions
	// on order, and re-ordering characters would shift generation.
	got := dedupIDs([]int{5, 2, 5, 2, 9, 2, 7, 5})
	want := []int{5, 2, 9, 7}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestJoinPromptSegments_EmptyBaseEmitsJoinedPartsOnly(t *testing.T) {
	// No base prompt → the joined parts become the whole prompt, with
	// no leading ", " separator. A regression that always prepended
	// "<base>, " would ship prompts starting with ", hero wearing red".
	got := joinPromptSegments("", []string{"hero wearing red", "dramatic lighting"})
	if got != "hero wearing red, dramatic lighting" {
		t.Errorf("got %q", got)
	}
}

func TestJoinPromptSegments_EmptyPartsReturnsBaseTrimmed(t *testing.T) {
	// Base-only path: must trim trailing/leading whitespace. The
	// mention resolver already emits trimmed output and we pin
	// parity here so the two pipelines produce the same text.
	if got := joinPromptSegments("  cat on a hat  ", nil); got != "cat on a hat" {
		t.Errorf("nil parts: got %q, want trimmed", got)
	}
	if got := joinPromptSegments("  cat on a hat  ", []string{}); got != "cat on a hat" {
		t.Errorf("empty parts: got %q, want trimmed", got)
	}
}

func TestJoinPromptSegments_BothNonEmptyJoinsWithCommaSpace(t *testing.T) {
	// The separator is literally ", " — pin it so a refactor to "; "
	// or " - " fails here instead of silently changing prompt shape.
	got := joinPromptSegments("cat on a hat", []string{"red coat", "blue hat"})
	if got != "cat on a hat, red coat, blue hat" {
		t.Errorf("got %q", got)
	}
}

func TestJoinPromptSegments_BothEmptyReturnsEmptyString(t *testing.T) {
	// Empty base + no parts → empty string (not ", "). A buggy refactor
	// that prepended ", " on every call would ship this as a space-only
	// prompt.
	if got := joinPromptSegments("", nil); got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if got := joinPromptSegments("   ", nil); got != "" {
		t.Errorf("whitespace base + nil parts: got %q, want empty", got)
	}
}

func TestNonEmpty_ReturnsFirstNonBlank(t *testing.T) {
	// First-match semantics — any blank/whitespace candidate is
	// skipped and the first non-blank wins. This matches the
	// "prefer explicit over fallback" contract used when merging
	// per-element overrides with character defaults.
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"all empty", []string{"", "", ""}, ""},
		{"all whitespace", []string{" ", "\t", "\n"}, ""},
		{"no args", nil, ""},
		{"first wins", []string{"alpha", "beta"}, "alpha"},
		{"skips blank, returns next", []string{"", "  ", "gamma"}, "gamma"},
		{"trims the winner", []string{"  padded  ", "later"}, "padded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nonEmpty(tc.args...); got != tc.want {
				t.Errorf("nonEmpty(%q) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}
