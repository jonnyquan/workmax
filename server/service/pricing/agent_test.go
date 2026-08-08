package pricing

import (
	"math"
	"testing"
)

// AgentCost is the single pricing source shared by workagent (API layer)
// and canvasagent (service layer). A silent drift in the clamp or the
// base cost would mis-bill every agent submission across both surfaces,
// so the math is pinned here with explicit numeric expectations rather
// than re-deriving from the constants the test is validating.

func TestAgentCost(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"zero attachments returns base", 0, 6},
		{"one attachment adds one", 1, 7},
		{"at cap adds five", 5, 11},
		// Above-cap must clamp, never overcharge. A future change that
		// removed the upper clamp would fail here.
		{"above cap is clamped to cap", 100, 11},
		// Negative counts can appear from frontend bugs / stale state;
		// clamp to zero rather than subtracting from the base.
		{"negative is clamped to zero", -3, 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AgentCost(tc.in)
			if got != tc.want {
				t.Fatalf("AgentCost(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// Exported constants are part of the public contract (canvasagent/cost.go
// imports them). Pin their values so a numeric bump has to update this
// test alongside the callers, surfacing the change in review.
func TestAgentPricingConstants(t *testing.T) {
	if AgentBaseCost != 6 {
		t.Fatalf("AgentBaseCost = %d, want 6", AgentBaseCost)
	}
	if AgentAttachmentCap != 5 {
		t.Fatalf("AgentAttachmentCap = %d, want 5", AgentAttachmentCap)
	}
	if CreditsPerUSD != 200.0 {
		t.Fatalf("CreditsPerUSD = %v, want 200.0", CreditsPerUSD)
	}
	if AgentMinTokenCredits != 1 {
		t.Fatalf("AgentMinTokenCredits = %d, want 1", AgentMinTokenCredits)
	}
}

// AgentCostFromUSD is the post-turn settle: takes the SDK-reported
// total_cost_usd and returns the integer credit count we charge.
// Boundary cases matter — fractional credits truncated would
// systematically undercharge at scale, so we round up via Ceil.
func TestAgentCostFromUSD(t *testing.T) {
	cases := []struct {
		name string
		usd  float64
		want int
	}{
		// Typical Anthropic Sonnet turn ($0.03) maps to ~6 credits,
		// matching the legacy file-count-based base. The numbers stay
		// in the same order of magnitude so the migration doesn't
		// silently shift unit economics.
		{"typical sonnet turn", 0.03, 6},
		// Round-up enforcement: $0.0001 = 0.02 credits truncates to 0
		// but Ceil bumps to 1. Multiplied across thousands of small
		// turns the truncation would have lost real revenue.
		{"sub-credit rounds up", 0.0001, 1},
		// Exact integer match — 0.005 USD * 200 credits/USD = 1.
		{"exact one credit", 0.005, 1},
		// Min-floor floor: zero-cost (cache hits) clamps to the
		// floor rather than rounding to free.
		{"zero cost clamps to min", 0.0, 1},
		// Heavy turn: $0.50 = 100 credits.
		{"heavy turn", 0.50, 100},
		// Just-above-credit boundary — 0.0051 = 1.02 ceils to 2.
		// Ceil semantics matter for ops review.
		{"just above credit boundary ceils up", 0.0051, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AgentCostFromUSD(tc.usd)
			if got != tc.want {
				t.Fatalf("AgentCostFromUSD(%v) = %d, want %d", tc.usd, got, tc.want)
			}
		})
	}
}

// Pathological inputs must fail closed — fall back to the legacy
// ceiling so a NaN / negative bug never produces a $0 charge.
func TestAgentCostFromUSD_PathologicalInputs(t *testing.T) {
	cases := []struct {
		name string
		usd  float64
	}{
		{"negative", -1.0},
		{"nan", math.NaN()},
		{"+inf", math.Inf(1)},
		{"-inf", math.Inf(-1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AgentCostFromUSD(tc.usd)
			if got != AgentBaseCost {
				t.Fatalf("AgentCostFromUSD(%v) = %d, want fallback=AgentBaseCost(%d)",
					tc.usd, got, AgentBaseCost)
			}
		})
	}
}
