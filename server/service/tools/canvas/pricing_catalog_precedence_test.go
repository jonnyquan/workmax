package canvas

import (
	"errors"
	"testing"

	"server/model"
)

// pricing_catalog_test.go and pricing_catalog_more_test.go pin single-
// flag pricing decisions and the headline precedence rules. These fill
// in the quieter multi-flag precedence branches and the category-vs-
// category routing guards that a silent regression would slip past.
//
//   • NANO_BANANA_PRO with Upscale=true AND Resolution="4k" — Upscale
//     is the FIRST check inside the PRO branch, so it must short-
//     circuit the 4k-doubling arm. Existing tests pin "Upscale flat
//     12 with n=5" but not "Upscale beats 4k". A refactor that
//     reordered the branches (e.g. applied 4k doubling first then
//     Upscale) would produce 8*2=16 instead of 12 here.
//   • NANO_BANANA_PRO with Upscale=true AND n>1 — existing tests
//     pin n=5 via a test that also sets 4k (so any of the three
//     short-circuits could be what's winning). Pin the pure
//     upscale × n=3 case so a regression that accidentally applied
//     n-multiplication under Upscale would flip 12 to 36.
//   • QuoteCanvasUpscale with Scale=4 AND EnhanceFace=true —
//     the two conditions are OR'd (`||`), not summed. Both true
//     must still be 5 credits, not 10. Pin so a refactor to additive
//     pricing would double-charge users who clicked both toggles.
//   • QuoteCanvasUpscale with Scale=2 and Scale=5 — distinct from
//     Scale=0 and Scale=4. The only special scale is 4; every other
//     scale (including higher) falls through to 3 credits. Pin a
//     couple of non-4 scales so a refactor that treated "any non-
//     zero scale" or "Scale >= 4" as the 5-credit branch would
//     surface (Scale=5 would flip to 5 under the wrong comparator).
//   • QuoteCanvasImage with empty Op "" — the map lookup returns
//     zero value with ok=false → ErrUnknownCanvasOp. Distinct from
//     the "known Op but wrong category" path (which already has a
//     test for CanvasOpVideo and CanvasOpUpscale). Pin the empty-
//     string path specifically so a refactor that short-circuited
//     on len(op)==0 and skipped the lookup would still produce the
//     sentinel.
//   • QuoteCanvasFlat with CanvasOpVideo — existing suite pins
//     CanvasOpImage (imageBase) and unknown-op; the videoBase case
//     is a third category that must also be rejected with
//     ErrUnknownCanvasOp. A regression that swapped `entry.Category
//     != PricingCategoryFlat` to `entry.Category == PricingCategoryImageBase`
//     would silently let videoBase ops leak through the flat path
//     with FlatCredits=0 (a free-video revenue bug).
//   • IsKnownCanvasOp on CanvasOp("") — must return false. The map
//     lookup works on zero-value string keys, so without an explicit
//     test a refactor that added a sentinel like
//     `canvasPricingTable[""] = CanvasPricingEntry{}` for debugging
//     would flip this to true silently.

func TestQuoteCanvasImage_ProUpscaleBeats4kDoubling(t *testing.T) {
	// Upscale check comes FIRST in the PRO branch. If a refactor
	// reordered the branches so 4k-doubling happened before the
	// Upscale short-circuit, this case would produce 16 (8 × 2) instead
	// of the flat 12. Pin the precedence.
	got, err := QuoteCanvasImage(CanvasImageQuoteInput{
		Op:         CanvasOpImage,
		Model:      model.NANO_BANANA_PRO,
		Resolution: "4k",
		Upscale:    true,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Credits != 12 {
		t.Errorf("upscale+4k credits = %d, want 12 (Upscale must short-circuit 4k)", got.Credits)
	}
}

func TestQuoteCanvasImage_ProUpscaleIgnoresNumberOfImages(t *testing.T) {
	// The upscale shortcut is flat 12 regardless of n. Existing test
	// sets n=5 but also 4k; with both active, either the Upscale
	// short-circuit OR the 4k-doubling bypass could be what pins the
	// result. Pin the pure Upscale × n=3 case so a refactor that
	// multiplied upscale by n (12 × 3 = 36) surfaces here.
	got, err := QuoteCanvasImage(CanvasImageQuoteInput{
		Op:             CanvasOpImage,
		Model:          model.NANO_BANANA_PRO,
		NumberOfImages: 3,
		Upscale:        true,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Credits != 12 {
		t.Errorf("upscale × n=3 credits = %d, want 12 (flat, not multiplied)", got.Credits)
	}
}

func TestQuoteCanvasUpscale_Scale4AndEnhanceBothSetStaysAt5(t *testing.T) {
	// The function uses `||`, not additive logic. Both flags set must
	// still yield 5 credits — a refactor that summed the two branches
	// (5 + 5 = 10, or base 3 + 5 = 8) would silently double-charge
	// users who select both quality toggles on the upscale UI.
	got := QuoteCanvasUpscale(CanvasUpscaleQuoteInput{
		Scale:       4,
		EnhanceFace: true,
	})
	if got.Credits != 5 {
		t.Errorf("scale=4 + enhance credits = %d, want 5 (OR, not sum)", got.Credits)
	}
}

func TestQuoteCanvasUpscale_NonFourScalesDefaultTo3(t *testing.T) {
	// The only special scale is 4. Every other scale (including higher
	// values like 5, 8) falls through to the default 3 credits. Pin a
	// couple so a refactor that used `Scale >= 4` would flip Scale=5
	// to 5 credits and silently overprice.
	cases := []int{1, 2, 3, 5, 8, 16}
	for _, scale := range cases {
		got := QuoteCanvasUpscale(CanvasUpscaleQuoteInput{Scale: scale})
		if got.Credits != 3 {
			t.Errorf("scale=%d credits = %d, want 3 (only Scale==4 is special)", scale, got.Credits)
		}
	}
}

func TestQuoteCanvasImage_EmptyOpReturnsSentinel(t *testing.T) {
	// Empty string Op takes the `canvasPricingTable[""]` branch → ok=false
	// → ErrUnknownCanvasOp. Distinct from the "known op but wrong
	// category" reject path. Pin so a refactor that added a
	// length-guard before the lookup (e.g. "empty op → panic") would
	// surface as a distinct failure mode instead of the expected
	// sentinel.
	_, err := QuoteCanvasImage(CanvasImageQuoteInput{
		Op:    CanvasOp(""),
		Model: model.NANO_BANANA,
	})
	if !errors.Is(err, ErrUnknownCanvasOp) {
		t.Errorf("empty op err = %v, want ErrUnknownCanvasOp", err)
	}
}

func TestQuoteCanvasFlat_VideoOpRejectedAsNotFlat(t *testing.T) {
	// CanvasOpVideo is a registered op but its category is videoBase,
	// not flat. The flat-quote entry must reject it with
	// ErrUnknownCanvasOp. Existing suite covers imageBase and truly-
	// unknown ops; the videoBase case is a third category that a
	// refactor could accidentally route through flat (e.g. by
	// inverting the category check).
	_, err := QuoteCanvasFlat(CanvasOpVideo)
	if !errors.Is(err, ErrUnknownCanvasOp) {
		t.Errorf("video op via QuoteCanvasFlat: err = %v, want ErrUnknownCanvasOp", err)
	}
}

func TestQuoteCanvasFlat_UpscaleOpRejectedAsNotFlat(t *testing.T) {
	// Symmetric to video: CanvasOpUpscale is registered but its
	// category is PricingCategoryUpscale, not flat. Must also be
	// rejected.
	_, err := QuoteCanvasFlat(CanvasOpUpscale)
	if !errors.Is(err, ErrUnknownCanvasOp) {
		t.Errorf("upscale op via QuoteCanvasFlat: err = %v, want ErrUnknownCanvasOp", err)
	}
}

func TestIsKnownCanvasOp_EmptyStringReturnsFalse(t *testing.T) {
	// A Go map lookup on zero-value key works as expected — empty
	// string must not match any registered op. Pin so a future
	// debug-sentinel like `canvasPricingTable[""] = {...}` would
	// surface here rather than silently route empty-op callers
	// through the debug entry's pricing.
	if IsKnownCanvasOp(CanvasOp("")) {
		t.Error("empty-string op reported as known")
	}
}
