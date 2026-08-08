package canvas

import (
	"errors"
	"testing"

	"server/model"
)

// pricing_catalog_test.go pins the happy paths for every model × op
// combo. These fill in the silent-drift branches whose regressions would
// mis-price a Canvas operation without tripping any existing test:
//
//   • QuoteCanvasImage floors numberOfImages to 1 on 0/negative inputs
//     (the `if n < 1 { n = 1 }` clamp). A regression that removed the
//     clamp would collapse n=0 to cost=0 (free credits — silent revenue
//     bug) and make n=-1 produce a bogus negative cost.
//   • The 4k multiplier is ONLY applied to NANO_BANANA_PRO. A regression
//     that hoisted the resolution doubling out of the PRO branch would
//     charge NANO_BANANA_2 users 2× for a resolution that doesn't even
//     apply to their model's pricing path.
//   • The Upscale flag is ONLY honoured for NANO_BANANA_PRO. A regression
//     that promoted it to a top-level shortcut would flat-rate every
//     image at 12 credits — catastrophic for single-image NANO_BANANA
//     calls that should cost 3.
//   • Unknown resolutions on PRO must fall through the "not 4k" arm (8
//     credits), not the 4k-double arm. Pin the semantics so a refactor
//     that used `if resolution != "2k"` (inverted) would surface here.
//   • Explicit "2k" resolution on PRO must price identically to the
//     default (omitted) case. Lets a future UI that sends the canonical
//     "2k" string avoid a silent price shift.
//   • LookupCanvasOp returns a value type (not a pointer) — mutating the
//     returned entry must not leak into the canonical table. Pin the
//     read-only contract: the table is module-level state and a caller
//     who thought they owned a copy could otherwise corrupt pricing for
//     every subsequent request.

func TestQuoteCanvasImage_ClampsZeroAndNegativeCountToOne(t *testing.T) {
	// NANO_BANANA at n=0 and n=-3 must both land on the single-image
	// price (3 credits). A regression that removed the `if n < 1 { n = 1 }`
	// clamp would produce (a) 0 credits for n=0 — a free-shot revenue
	// bug — and (b) negative credits for n=-1, which callers that sum
	// credits across multiple submissions would silently offset.
	for _, n := range []int{-5, -1, 0} {
		got, err := QuoteCanvasImage(CanvasImageQuoteInput{
			Op:             CanvasOpImage,
			Model:          model.NANO_BANANA,
			NumberOfImages: n,
		})
		if err != nil {
			t.Errorf("n=%d: unexpected err: %v", n, err)
		}
		if got.Credits != 3 {
			t.Errorf("n=%d: credits = %d, want 3 (clamp floor)", n, got.Credits)
		}
	}
}

func TestQuoteCanvasImage_4kMultiplierIsProOnly(t *testing.T) {
	// The 4k × 2 multiplier lives inside the NANO_BANANA_PRO branch.
	// Other models must ignore Resolution entirely — a regression that
	// hoisted the doubling out of the PRO branch would charge
	// NANO_BANANA_2 × 4k as 8 credits instead of 4.
	cases := []struct {
		model string
		want  int
	}{
		{model.NANO_BANANA, 3},   // base unchanged despite "4k"
		{model.NANO_BANANA_2, 4}, // base unchanged despite "4k"
	}
	for _, tc := range cases {
		got, err := QuoteCanvasImage(CanvasImageQuoteInput{
			Op:         CanvasOpImage,
			Model:      tc.model,
			Resolution: "4k",
		})
		if err != nil || got.Credits != tc.want {
			t.Errorf("%s × 4k = (%d, %v), want (%d, nil) — 4k must be PRO-only",
				tc.model, got.Credits, err, tc.want)
		}
	}
}

func TestQuoteCanvasImage_UpscaleShortcutIsProOnly(t *testing.T) {
	// The Upscale=true → 12 credits shortcut is only meaningful for
	// NANO_BANANA_PRO. NANO_BANANA and NANO_BANANA_2 must price as if
	// the flag were false — a regression that promoted the shortcut to
	// a top-level branch would charge 12 credits for every image, which
	// on NANO_BANANA's 3-credit base is a 4× overcharge per generation.
	cases := []struct {
		model string
		want  int
	}{
		{model.NANO_BANANA, 3},
		{model.NANO_BANANA_2, 4},
	}
	for _, tc := range cases {
		got, err := QuoteCanvasImage(CanvasImageQuoteInput{
			Op:      CanvasOpImage,
			Model:   tc.model,
			Upscale: true,
		})
		if err != nil || got.Credits != tc.want {
			t.Errorf("%s × upscale = (%d, %v), want (%d, nil) — upscale must be PRO-only",
				tc.model, got.Credits, err, tc.want)
		}
	}
}

func TestQuoteCanvasImage_ProExplicit2kMatchesDefault(t *testing.T) {
	// PRO with Resolution="2k" must match PRO with Resolution unset —
	// the default arm treats an empty string as "2k". Pin both so a UI
	// change that sends the canonical "2k" literal instead of "" can't
	// silently shift the price through some future `if resolution != "2k"`
	// check that treated the literal and the empty string differently.
	defaulted, err1 := QuoteCanvasImage(CanvasImageQuoteInput{
		Op:    CanvasOpImage,
		Model: model.NANO_BANANA_PRO,
	})
	explicit, err2 := QuoteCanvasImage(CanvasImageQuoteInput{
		Op:         CanvasOpImage,
		Model:      model.NANO_BANANA_PRO,
		Resolution: "2k",
	})
	if err1 != nil || err2 != nil {
		t.Fatalf("unexpected errors: %v / %v", err1, err2)
	}
	if defaulted.Credits != explicit.Credits {
		t.Errorf("default vs explicit 2k: %d vs %d (must match)",
			defaulted.Credits, explicit.Credits)
	}
	if explicit.Credits != 8 {
		t.Errorf("explicit 2k = %d, want 8", explicit.Credits)
	}
}

func TestQuoteCanvasImage_ProUnknownResolutionFallsThroughTo2kPricing(t *testing.T) {
	// An unknown resolution string (e.g. "8k", or a typo like "4K")
	// must NOT accidentally trip the 4k-doubling arm. The only condition
	// that doubles is `resolution == "4k"` — anything else stays at the
	// 2k base rate. Pin a couple of common typos so a regression that
	// lowercased or fuzzy-matched would surface here.
	cases := []string{"4K", "8k", "ultra", "HD"}
	for _, res := range cases {
		got, err := QuoteCanvasImage(CanvasImageQuoteInput{
			Op:         CanvasOpImage,
			Model:      model.NANO_BANANA_PRO,
			Resolution: res,
		})
		if err != nil {
			t.Errorf("res=%q: unexpected err: %v", res, err)
		}
		if got.Credits != 8 {
			t.Errorf("res=%q: credits = %d, want 8 (non-4k fall-through)",
				res, got.Credits)
		}
	}
}

func TestLookupCanvasOp_ReturnsValueCopyNotReference(t *testing.T) {
	// LookupCanvasOp must return a value copy of the entry. A caller
	// that mutates the returned struct's fields must NOT corrupt the
	// canonical canvasPricingTable — otherwise a single buggy handler
	// could rewrite pricing for every subsequent request on the server.
	entry, ok := LookupCanvasOp(CanvasOpRemoveBg)
	if !ok {
		t.Fatal("expected RemoveBg entry")
	}
	entry.FlatCredits = 999
	entry.Op = "mutated"

	// Re-read the table directly — it must be unchanged.
	fresh, _ := LookupCanvasOp(CanvasOpRemoveBg)
	if fresh.FlatCredits != 4 {
		t.Errorf("table mutated via returned copy: FlatCredits=%d, want 4",
			fresh.FlatCredits)
	}
	if fresh.Op != CanvasOpRemoveBg {
		t.Errorf("table mutated via returned copy: Op=%q, want %q",
			fresh.Op, CanvasOpRemoveBg)
	}
}

func TestQuoteCanvasFlat_EveryFlatOpReturnsPositiveCredits(t *testing.T) {
	// Every entry whose Category == Flat must have FlatCredits > 0.
	// The existing TestCanvasPricingTable_EveryEntryHasRequiredFields
	// pins the same invariant at the table level, but the quote
	// function adds another layer — pin that QuoteCanvasFlat surfaces
	// the correct cost (not 0) for every flat op so a regression that
	// read the wrong table field (e.g. a field rename without updating
	// the quote) would trip here.
	for _, entry := range ListCanvasPricingEntries() {
		if entry.Category != PricingCategoryFlat {
			continue
		}
		got, err := QuoteCanvasFlat(entry.Op)
		if err != nil {
			t.Errorf("%q: QuoteCanvasFlat err = %v", entry.Op, err)
			continue
		}
		if got.Credits <= 0 {
			t.Errorf("%q: credits = %d, want >0", entry.Op, got.Credits)
		}
		if got.Credits != entry.FlatCredits {
			t.Errorf("%q: credits = %d, want %d (must mirror table entry)",
				entry.Op, got.Credits, entry.FlatCredits)
		}
	}
}

func TestQuoteCanvasImage_VideoOpCategoryMismatch(t *testing.T) {
	// TestQuoteCanvasImage_VideoOpRejectedAsNotImageCategory pins that
	// CanvasOpVideo (registered, but videoBase category) returns
	// ErrUnknownCanvasOp. This is the symmetric case for upscale —
	// CanvasOpUpscale IS registered but in PricingCategoryUpscale, so
	// QuoteCanvasImage must also reject it rather than silently pricing
	// it through the image path. Otherwise an ImageBase image table
	// miss would poison the upscale pricing channel with an unexpected
	// ErrUnknownCanvasModel when the upscale handler was fed an image
	// model. Pin the rejection.
	_, err := QuoteCanvasImage(CanvasImageQuoteInput{
		Op:    CanvasOpUpscale,
		Model: model.NANO_BANANA,
	})
	if !errors.Is(err, ErrUnknownCanvasOp) {
		t.Errorf("upscale op via QuoteCanvasImage: err = %v, want ErrUnknownCanvasOp", err)
	}
}
