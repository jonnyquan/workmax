package tools

// canvas_llm_cost_test.go covers the headline matrix (base, thinking
// replaces base, fast/web adds, attachment cap-at-5, negative clamp,
// all-stack total). These fill the quieter gate invariants a silent
// regression would slip past:
//
//   • Thinking is REPLACEMENT semantics (base := 6), not addition. Pin
//     thinking-only-no-mods returns EXACTLY 6 (not 2+6=8). A refactor
//     to additive (`base += 6` when thinking) would silently double the
//     thinking cost.
//   • attachmentCount exactly 5 passes through without clamp. Pin the
//     boundary so a refactor to `>= 5` (off-by-one) would surface at
//     the boundary.
//   • attachmentCount exactly 6 clamps to 5. Pin the first-past-boundary
//     value (base test uses 10 which is well past — this pins the
//     actual inflection point).
//   • MaxInt attachmentCount still clamps safely (no overflow). Pin so a
//     refactor to arithmetic-based clamp (e.g. `min(a+1, 6)`) that might
//     overflow on huge inputs would surface.
//   • MinInt attachmentCount still clamps to 0 (no underflow in the
//     `< 0` check). Pin the negative-clamp floor.
//   • Fast+Web without thinking: 2+1+2 = 5. Pin the additive order for
//     the "free-tier with modifiers" combination.
//   • Thinking+Fast+Web (no attachments): 6+1+2 = 9. Pin so a refactor
//     that re-ordered the conditional blocks would surface.
//   • Thinking+Fast only: 6+1 = 7.
//   • Thinking+Web only: 6+2 = 8.
//   • Function is pure — same args across repeated calls yield same
//     result, no hidden state (no counter, no RNG leak). Pin via a
//     hundred-call loop asserting constant output.
//   • Minimum-output invariant: across the full legal input surface
//     (all bool combos × attachments in [-100, 100]), output is ALWAYS
//     >= 1. The `if total < 1 return 1` floor is currently defensive
//     dead-code (base min is 2), but pin the output invariant so a
//     refactor that reduces base (e.g. `base := 0` as a bug) would
//     surface as a floor-triggering value rather than silently.
//   • CanvasOptimizePromptCost is CONSTANT 2 across many calls. Pin so
//     a refactor to a configurable flat cost would surface.
//   • CanvasOptimizePromptCost has no dependency on any CanvasChatCost
//     call history (no shared package-level mutable state).

import (
	"math"
	"testing"
)

func TestCanvasChatCost_ThinkingReplacesNotAdds(t *testing.T) {
	// Base semantics are assignment (`base = 6`), not addition. If
	// thinking ever becomes additive, cost jumps from 6 to 8 silently.
	if got := CanvasChatCost(true, false, false, 0); got != 6 {
		t.Errorf("thinking-only = %d, want 6 (replacement, not additive)", got)
	}
}

func TestCanvasChatCost_AttachmentBoundaryFive(t *testing.T) {
	// Exactly 5 passes through un-clamped. Guards against `>= 5` typo.
	if got := CanvasChatCost(false, false, false, 5); got != 7 {
		t.Errorf("attachmentCount=5 = %d, want 7 (base 2 + 5 un-clamped)", got)
	}
}

func TestCanvasChatCost_AttachmentBoundarySix(t *testing.T) {
	// First value above cap → clamps to 5. Pin the exact inflection.
	if got := CanvasChatCost(false, false, false, 6); got != 7 {
		t.Errorf("attachmentCount=6 = %d, want 7 (clamped to 5)", got)
	}
}

func TestCanvasChatCost_MaxIntAttachmentSafe(t *testing.T) {
	// Extreme upper bound — no overflow, cleanly clamped.
	if got := CanvasChatCost(false, false, false, math.MaxInt); got != 7 {
		t.Errorf("MaxInt attachment = %d, want 7", got)
	}
}

func TestCanvasChatCost_MinIntAttachmentSafe(t *testing.T) {
	// Extreme lower bound — `< 0` catches it, clamps to 0.
	if got := CanvasChatCost(false, false, false, math.MinInt); got != 2 {
		t.Errorf("MinInt attachment = %d, want 2 (clamped to 0)", got)
	}
}

func TestCanvasChatCost_FastPlusWebNoThinking(t *testing.T) {
	// 2 + 1 + 2 = 5. Pin the additive order of the free-tier modifiers.
	if got := CanvasChatCost(false, true, true, 0); got != 5 {
		t.Errorf("fast+web only = %d, want 5 (2+1+2)", got)
	}
}

func TestCanvasChatCost_ThinkingFastWebNoAttachments(t *testing.T) {
	// 6 + 1 + 2 = 9. Pin the conditional-block order so re-arranging
	// surfaces.
	if got := CanvasChatCost(true, true, true, 0); got != 9 {
		t.Errorf("thinking+fast+web = %d, want 9 (6+1+2)", got)
	}
}

func TestCanvasChatCost_ThinkingFastOnly(t *testing.T) {
	if got := CanvasChatCost(true, true, false, 0); got != 7 {
		t.Errorf("thinking+fast = %d, want 7 (6+1)", got)
	}
}

func TestCanvasChatCost_ThinkingWebOnly(t *testing.T) {
	if got := CanvasChatCost(true, false, true, 0); got != 8 {
		t.Errorf("thinking+web = %d, want 8 (6+2)", got)
	}
}

func TestCanvasChatCost_Pure(t *testing.T) {
	// 100 identical calls must yield the same result — pure function,
	// no hidden counter, no package-level mutable state contaminated
	// across invocations.
	want := CanvasChatCost(true, true, true, 3)
	for i := 0; i < 100; i++ {
		if got := CanvasChatCost(true, true, true, 3); got != want {
			t.Fatalf("call %d: got %d, want %d (non-deterministic)", i, got, want)
		}
	}
}

func TestCanvasChatCost_MinimumOutputInvariant(t *testing.T) {
	// Across the full legal surface, output is always >= 1. Floor is
	// currently unreachable via any real input (base min is 2), but
	// pin the invariant so a regression that reduced `base` initial
	// (e.g. to 0) would surface through floor activation rather than
	// silently returning 0/negative.
	for _, thinking := range []bool{false, true} {
		for _, fast := range []bool{false, true} {
			for _, web := range []bool{false, true} {
				for attach := -100; attach <= 100; attach++ {
					got := CanvasChatCost(thinking, fast, web, attach)
					if got < 1 {
						t.Errorf("CanvasChatCost(%v,%v,%v,%d)=%d violates >=1 floor",
							thinking, fast, web, attach, got)
					}
				}
			}
		}
	}
}

func TestCanvasOptimizePromptCost_ConstantAcrossCalls(t *testing.T) {
	// Flat constant — pin against a refactor to make it configurable.
	for i := 0; i < 50; i++ {
		if got := CanvasOptimizePromptCost(); got != 2 {
			t.Fatalf("call %d: got %d, want 2", i, got)
		}
	}
}

func TestCanvasOptimizePromptCost_IndependentOfChatCostState(t *testing.T) {
	// Interleave chat-cost calls of varying shapes and re-check the
	// optimize cost — no shared state can leak.
	_ = CanvasChatCost(true, true, true, 3)
	_ = CanvasChatCost(false, false, false, 0)
	_ = CanvasChatCost(true, false, true, 10)
	if got := CanvasOptimizePromptCost(); got != 2 {
		t.Errorf("after interleaved chat calls, optimize cost = %d, want 2", got)
	}
}
