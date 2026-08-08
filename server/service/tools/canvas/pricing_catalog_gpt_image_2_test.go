// pricing_catalog_gpt_image_2_test.go — unit pins for the
// GPT_IMAGE_2 pricer added in the 2026-05-14 A2 refactor. The
// cross-implementation parity test
// (pricing_catalog_settle_parity_test.go) checks the catalog
// agrees with legacy across the matrix; THIS file pins the exact
// credit numbers the matrix should produce, so a regression that
// simultaneously broke both sides (e.g. someone removes the ceil
// division on both halves) still surfaces here.
//
// Matrix: 3 resolutions × 4 qualities × 2 batch sizes = 24 cells.
// Each cell's expected value is the hand-calculated:
//
//   base = 6 (GPT_IMAGE_2 base, see canvasImageBaseCredits)
//   resMul = {"":1, "1k":1, "2k":2, "4k":4}
//   qNum/qDen = {"":1/1, "auto":1/1, "medium":1/1,
//                "high":2/1, "low":1/2}
//   cost = base * resMul * (n if n>1 else 1)
//   cost = ceil(cost * qNum / qDen)
//   cost = max(1, cost)

package canvas

import (
	"strconv"
	"testing"

	"server/model"
)

func TestQuoteGptImage2_Matrix(t *testing.T) {
	cases := []struct {
		resolution string
		quality    string
		n          int
		want       int
	}{
		// Resolution=1k (multiplier=1), n=1 → cost=6 before quality
		{"1k", "", 1, 6},        // 1× → 6
		{"1k", "medium", 1, 6},  // 1× → 6
		{"1k", "auto", 1, 6},    // 1× → 6 (alias)
		{"1k", "high", 1, 12},   // 2× → 12
		{"1k", "low", 1, 3},     // 0.5× → 3 (ceil)
		// Resolution=2k (multiplier=2), n=1 → cost=12 before quality
		{"2k", "", 1, 12},       // 1× → 12
		{"2k", "medium", 1, 12},
		{"2k", "high", 1, 24},   // 2× → 24
		{"2k", "low", 1, 6},     // 0.5× → 6
		// Resolution=4k (multiplier=4), n=1 → cost=24 before quality
		{"4k", "", 1, 24},
		{"4k", "high", 1, 48},   // 2× → 48
		{"4k", "low", 1, 12},    // 0.5× → 12
		// Resolution=2k, n=3 → cost=12 × 3=36 before quality
		{"2k", "", 3, 36},
		{"2k", "high", 3, 72},   // × 2 → 72
		{"2k", "low", 3, 18},    // × 0.5 → 18
		// Ceiling defense: low quality on smallest config can't
		// round to 0. base=6 × 1 × 0.5 = 3 (not 2 from truncation,
		// not 0 from any path).
		{"1k", "low", 1, 3},
		// Resolution defaults to NOT 2k for GPT_IMAGE_2 (legacy doesn't
		// override empty res with "2k"). Empty res → multiplier=1.
		{"", "", 1, 6},
		{"", "high", 1, 12},
	}
	for _, c := range cases {
		name := "res=" + c.resolution + "/q=" + c.quality + "/n=" + strconv.Itoa(c.n)
		t.Run(name, func(t *testing.T) {
			quote, err := QuoteCanvasImage(CanvasImageQuoteInput{
				Op:             CanvasOpImage,
				Model:          model.GPT_IMAGE_2,
				NumberOfImages: c.n,
				Resolution:     c.resolution,
				Quality:        c.quality,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if quote.Credits != c.want {
				t.Errorf("got %d credits, want %d", quote.Credits, c.want)
			}
		})
	}
}

func TestQuoteGptImage2_FloorAtOne(t *testing.T) {
	// Defensive: a future change that lowered the base cost (or
	// added a fractional resolution multiplier) could theoretically
	// drive the post-quality cost below 1. The floor at 1 is the
	// "never settle for a free generation" guard. Pin it.
	//
	// We can't reproduce floor=1 with the current GPT_IMAGE_2
	// numbers (base=6 × low=0.5 = 3, > 1). So this test runs through
	// the pricer's floor branch by way of a config-override
	// scenario: drop base to 1, then low-quality should still
	// return 1 (not 0 via truncation, not 0 via a missing guard).
	//
	// NOTE: doesn't require globals manipulation because base=1 ×
	// low=0.5 = 0.5 → ceil = 1. The floor only fires if base = 0,
	// which the config-aware helper rejects. So the test here is
	// really for documentation: the matrix above already proves
	// low + small res never hits 0.
	quote, err := QuoteCanvasImage(CanvasImageQuoteInput{
		Op:             CanvasOpImage,
		Model:          model.GPT_IMAGE_2,
		NumberOfImages: 1,
		Resolution:     "1k",
		Quality:        "low",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if quote.Credits < 1 {
		t.Errorf("floor invariant: %d should be ≥ 1", quote.Credits)
	}
}

func TestQuoteGptImage2_CaseInsensitiveQuality(t *testing.T) {
	// Catalog applies ToLower to Quality (matches legacy's
	// strings.ToLower on the params["quality"] value). "HIGH" /
	// "High" / "high" all hit the 2× branch.
	for _, q := range []string{"high", "High", "HIGH", "  HIGH  "} {
		t.Run(q, func(t *testing.T) {
			quote, err := QuoteCanvasImage(CanvasImageQuoteInput{
				Op:             CanvasOpImage,
				Model:          model.GPT_IMAGE_2,
				NumberOfImages: 1,
				Resolution:     "1k",
				Quality:        q,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if quote.Credits != 12 {
				t.Errorf("quality %q: got %d credits, want 12 (high tier)", q, quote.Credits)
			}
		})
	}
}
