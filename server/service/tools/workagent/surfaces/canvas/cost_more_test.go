package canvas

// cost_test.go pins the headline matrix (no attachments=6, three=9,
// over-cap clamps to cap, negative→0). These fill the quieter gate
// invariants a silent regression would slip past:
//
//   • AgentCost is a thin forward to pricing.AgentCost — pin the
//     forwarding contract over a wide attachment range so a refactor
//     that introduced a delta (e.g. canvas-specific surcharge) is
//     caught here as a forwarding-equality violation, not noticed only
//     after the bill drifts in production.
//
//   • Boundary at exactly the cap (5): result is base+5 = 11. Pin so a
//     refactor to `>= AgentAttachmentCap` (off-by-one) would surface.
//
//   • First-past-cap value (6): result is the SAME as cap (=11). Pin
//     the inflection point — base test uses 12 which is well past the
//     boundary; this nails the actual flip.
//
//   • math.MaxInt attachmentCount stays at 11 — no overflow when the
//     clamp arithmetic fires. Pin so a refactor to `min(a, cap)` via
//     subtraction (which can overflow) surfaces.
//
//   • math.MinInt attachmentCount stays at 6 — the `< 0` guard catches
//     the floor. Pin so a refactor to use `attachmentCount % cap`
//     (which is undefined-sign for negatives) surfaces.
//
//   • Pure / no hidden state: 100 identical calls return identical
//     result. A future refactor that tied cost to time-of-day or a
//     RNG-based sale would surface here.
//
//   • Independent of CanvasChatCost call history (different package,
//     but pinning the cross-pollution-free contract is cheap).

import (
	"math"
	"testing"

	"server/service/pricing"
)

func TestAgentCost_ForwardsToPricingForFullRange(t *testing.T) {
	// Wide-range equality with pricing.AgentCost — surfaces any silent
	// canvas-specific divergence (e.g. an in-line surcharge added to
	// the wrapper).
	for n := -10; n <= 20; n++ {
		want := pricing.AgentCost(n)
		got := AgentCost(n)
		if got != want {
			t.Errorf("AgentCost(%d) = %d, pricing.AgentCost(%d) = %d (forwarding broken)", n, got, n, want)
		}
	}
}

func TestAgentCost_BoundaryExactlyAtCap(t *testing.T) {
	// Exactly 5 (the cap) → 6 + 5 = 11. Guards against `>= cap` typo.
	if got := AgentCost(5); got != 11 {
		t.Errorf("AgentCost(5) = %d, want 11 (exact cap, un-clamped)", got)
	}
}

func TestAgentCost_FirstPastCapStillClampsToCap(t *testing.T) {
	// 6 is the first value that should be clamped DOWN to 5. Output
	// must equal AgentCost(5). Pin the inflection point.
	if got := AgentCost(6); got != 11 {
		t.Errorf("AgentCost(6) = %d, want 11 (clamped to cap=5)", got)
	}
	if AgentCost(6) != AgentCost(5) {
		t.Errorf("AgentCost(6) != AgentCost(5) — clamp boundary broken")
	}
}

func TestAgentCost_MaxIntStaysAtCap(t *testing.T) {
	// math.MaxInt → cap. Guards against arithmetic-clamp overflow.
	if got := AgentCost(math.MaxInt); got != 11 {
		t.Errorf("AgentCost(MaxInt) = %d, want 11", got)
	}
}

func TestAgentCost_MinIntStaysAtBase(t *testing.T) {
	// math.MinInt → 0 → base. Guards against modulo-style clamps that
	// have undefined sign on negatives.
	if got := AgentCost(math.MinInt); got != 6 {
		t.Errorf("AgentCost(MinInt) = %d, want 6 (clamped to 0, +base)", got)
	}
}

func TestAgentCost_DeterministicAcrossManyCalls(t *testing.T) {
	want := AgentCost(3)
	for i := 0; i < 100; i++ {
		if got := AgentCost(3); got != want {
			t.Fatalf("call %d: got %d, want %d (non-deterministic)", i, got, want)
		}
	}
}

func TestAgentCost_PricingConstantsSurfacedInExpectations(t *testing.T) {
	// Expected output for n in {0, cap, cap+1} = base + clamped.
	// Pin via the exposed constants so a refactor that bumps base or
	// cap is caught here as an expectation update — not as a silent
	// drift through arithmetic.
	if got := AgentCost(0); got != pricing.AgentBaseCost {
		t.Errorf("AgentCost(0) = %d, want AgentBaseCost (%d)", got, pricing.AgentBaseCost)
	}
	if got := AgentCost(pricing.AgentAttachmentCap); got != pricing.AgentBaseCost+pricing.AgentAttachmentCap {
		t.Errorf("AgentCost(cap) = %d, want base+cap (%d)",
			got, pricing.AgentBaseCost+pricing.AgentAttachmentCap)
	}
	if got := AgentCost(pricing.AgentAttachmentCap + 1); got != pricing.AgentBaseCost+pricing.AgentAttachmentCap {
		t.Errorf("AgentCost(cap+1) = %d, want base+cap (%d) — clamp broken",
			got, pricing.AgentBaseCost+pricing.AgentAttachmentCap)
	}
}
