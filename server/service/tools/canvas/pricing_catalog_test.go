package canvas

import (
	"errors"
	"testing"

	"server/model"
)

// pricing_catalog_test.go pins:
//   - Every declared CanvasOp is registered in the table. Missing an
//     op here means the Quote handler will fall through to a
//     not-found error at runtime for a real request, which is worse
//     than a compile-time catch but table-driven ops stay cheap to
//     add.
//   - Every imageBase op prices every image model the same way. This
//     protects against drift if someone adds a per-op override without
//     adding a test case — the default should stay uniform.
//   - Upscale, flat (remove-bg), and the NANO_BANANA_PRO resolution/
//     upscale shortcuts stay pinned at their current credit values.
//   - Unknown ops and models come back as sentinel errors so handlers
//     can classify them into 400s.

// allExpectedCanvasOps is the list the handler surfaces; keep in sync
// with the enum constants — the ops-complete test below catches drift.
var allExpectedCanvasOps = []CanvasOp{
	CanvasOpImage,
	CanvasOpVideo,
	CanvasOpImg2Img,
	CanvasOpOutpaint,
	CanvasOpInpaint,
	CanvasOpMockup,
	CanvasOpEditText,
	CanvasOpSplitLayers,
	CanvasOpUpscale,
	CanvasOpRemoveBg,
	CanvasOpVectorize,
	CanvasOpExtractKeyframe,
}

func TestCanvasPricingTable_CoversEveryDeclaredOp(t *testing.T) {
	for _, op := range allExpectedCanvasOps {
		if !IsKnownCanvasOp(op) {
			t.Errorf("%q missing from canvasPricingTable", op)
		}
	}
	if len(allExpectedCanvasOps) != len(canvasPricingTable) {
		t.Errorf("op count drift: table has %d, expected %d",
			len(canvasPricingTable), len(allExpectedCanvasOps))
	}
}

func TestCanvasPricingTable_EveryEntryHasRequiredFields(t *testing.T) {
	// A flat-category entry without FlatCredits is a silent 0-credit
	// bug; a non-flat entry with FlatCredits set is inert but
	// misleading. Pin both.
	for _, e := range ListCanvasPricingEntries() {
		switch e.Category {
		case PricingCategoryFlat:
			if e.FlatCredits <= 0 {
				t.Errorf("%q flat entry has FlatCredits=%d, want >0", e.Op, e.FlatCredits)
			}
		case PricingCategoryImageBase, PricingCategoryVideoBase, PricingCategoryUpscale, PricingCategoryVectorize:
			if e.FlatCredits != 0 {
				t.Errorf("%q non-flat entry has stray FlatCredits=%d", e.Op, e.FlatCredits)
			}
		default:
			t.Errorf("%q has unknown category %q", e.Op, e.Category)
		}
	}
}

func TestLookupCanvasOp_ReturnsOkForKnown(t *testing.T) {
	entry, ok := LookupCanvasOp(CanvasOpImage)
	if !ok || entry.Category != PricingCategoryImageBase {
		t.Errorf("LookupCanvasOp(image) = (%+v, %v), want imageBase/true", entry, ok)
	}
}

func TestLookupCanvasOp_ReturnsNotOkForUnknown(t *testing.T) {
	if _, ok := LookupCanvasOp(CanvasOp("zzz-unknown")); ok {
		t.Error("unknown op reported as known")
	}
}

func TestCanvasImageBaseCredits_PinsModelTable(t *testing.T) {
	// If this table ever diverges from server/service/tools/pricing_catalog.go
	// without an explicit Canvas-override decision, the divergence
	// must be deliberate and documented. Pin values so the drift is
	// always visible.
	cases := map[string]int{
		model.NANO_BANANA:     3,
		model.NANO_BANANA_2:   4,
		model.NANO_BANANA_PRO: 8,
	}
	for modelID, want := range cases {
		got, ok := CanvasImageBaseCredits(modelID)
		if !ok || got != want {
			t.Errorf("CanvasImageBaseCredits(%q) = (%d,%v), want %d/true", modelID, got, ok, want)
		}
	}
}

func TestCanvasImageBaseCredits_UnknownModelReturnsFalse(t *testing.T) {
	if _, ok := CanvasImageBaseCredits("does-not-exist"); ok {
		t.Error("unknown model reported as known")
	}
}

func TestQuoteCanvasImage_NanoBananaSingle(t *testing.T) {
	// NANO_BANANA at n=1 is the cheapest path; anyone calling the
	// Quote handler without a numberOfImages parameter should land
	// here.
	got, err := QuoteCanvasImage(CanvasImageQuoteInput{
		Op:    CanvasOpImage,
		Model: model.NANO_BANANA,
	})
	if err != nil || got.Credits != 3 {
		t.Errorf("got (%d,%v), want (3, nil)", got.Credits, err)
	}
}

func TestQuoteCanvasImage_NanoBanana2Multi(t *testing.T) {
	got, err := QuoteCanvasImage(CanvasImageQuoteInput{
		Op:             CanvasOpImg2Img,
		Model:          model.NANO_BANANA_2,
		NumberOfImages: 3,
	})
	if err != nil || got.Credits != 4*3 {
		t.Errorf("got (%d,%v), want (12, nil)", got.Credits, err)
	}
}

func TestQuoteCanvasImage_NanoBananaProDefaultsTo2k(t *testing.T) {
	// Leaving resolution blank on NANO_BANANA_PRO must behave as 2k
	// (8 credits), matching the handler's original default.
	got, err := QuoteCanvasImage(CanvasImageQuoteInput{
		Op:    CanvasOpImage,
		Model: model.NANO_BANANA_PRO,
	})
	if err != nil || got.Credits != 8 {
		t.Errorf("got (%d,%v), want (8, nil)", got.Credits, err)
	}
}

func TestQuoteCanvasImage_NanoBananaPro4kDoubles(t *testing.T) {
	got, err := QuoteCanvasImage(CanvasImageQuoteInput{
		Op:         CanvasOpImage,
		Model:      model.NANO_BANANA_PRO,
		Resolution: "4k",
	})
	if err != nil || got.Credits != 16 {
		t.Errorf("got (%d,%v), want (16, nil) for 4k", got.Credits, err)
	}
}

func TestQuoteCanvasImage_NanoBananaPro4kMulti(t *testing.T) {
	got, err := QuoteCanvasImage(CanvasImageQuoteInput{
		Op:             CanvasOpImage,
		Model:          model.NANO_BANANA_PRO,
		Resolution:     "4k",
		NumberOfImages: 2,
	})
	if err != nil || got.Credits != 8*2*2 {
		t.Errorf("got (%d,%v), want (32, nil)", got.Credits, err)
	}
}

func TestQuoteCanvasImage_NanoBananaProUpscaleShortcut(t *testing.T) {
	// The upscale shortcut is a flat 12 regardless of resolution or
	// numberOfImages — pin that so a future cleanup doesn't forget.
	got, err := QuoteCanvasImage(CanvasImageQuoteInput{
		Op:             CanvasOpImage,
		Model:          model.NANO_BANANA_PRO,
		Resolution:     "4k",
		NumberOfImages: 5,
		Upscale:        true,
	})
	if err != nil || got.Credits != 12 {
		t.Errorf("got (%d,%v), want (12, nil)", got.Credits, err)
	}
}

func TestQuoteCanvasImage_EveryImageBaseOpPricesIdentically(t *testing.T) {
	// Today all image-base ops (img2img, outpaint, inpaint, etc) use
	// the same per-model base. This test pins that parity; when a
	// canvas-specific multiplier lands, replace this with a table
	// that spells the new prices out.
	imageOps := []CanvasOp{
		CanvasOpImage,
		CanvasOpImg2Img,
		CanvasOpOutpaint,
		CanvasOpInpaint,
		CanvasOpMockup,
		CanvasOpEditText,
		CanvasOpSplitLayers,
		CanvasOpExtractKeyframe,
	}
	for _, op := range imageOps {
		for _, modelCase := range []struct {
			model string
			want  int
		}{
			{model.NANO_BANANA, 3},
			{model.NANO_BANANA_2, 4},
			{model.NANO_BANANA_PRO, 8}, // default 2k
		} {
			got, err := QuoteCanvasImage(CanvasImageQuoteInput{
				Op:    op,
				Model: modelCase.model,
			})
			if err != nil || got.Credits != modelCase.want {
				t.Errorf("QuoteCanvasImage(%q,%q) = (%d,%v), want (%d, nil)",
					op, modelCase.model, got.Credits, err, modelCase.want)
			}
		}
	}
}

func TestQuoteCanvasImage_EmptyModelDefaultsToNanoBanana(t *testing.T) {
	got, err := QuoteCanvasImage(CanvasImageQuoteInput{
		Op:    CanvasOpImage,
		Model: "",
	})
	if err != nil || got.Credits != 3 {
		t.Errorf("got (%d,%v), want (3, nil)", got.Credits, err)
	}
}

func TestQuoteCanvasImage_UnknownOpReturnsSentinel(t *testing.T) {
	_, err := QuoteCanvasImage(CanvasImageQuoteInput{
		Op:    CanvasOp("nope"),
		Model: model.NANO_BANANA,
	})
	if !errors.Is(err, ErrUnknownCanvasOp) {
		t.Errorf("err = %v, want ErrUnknownCanvasOp", err)
	}
}

func TestQuoteCanvasImage_UnknownModelReturnsSentinel(t *testing.T) {
	_, err := QuoteCanvasImage(CanvasImageQuoteInput{
		Op:    CanvasOpImage,
		Model: "some-future-model",
	})
	if !errors.Is(err, ErrUnknownCanvasModel) {
		t.Errorf("err = %v, want ErrUnknownCanvasModel", err)
	}
}

func TestQuoteCanvasImage_VideoOpRejectedAsNotImageCategory(t *testing.T) {
	// The image-category quote must refuse to price a video op —
	// routing video pricing through here would silently drop the
	// duration/resolution inputs.
	_, err := QuoteCanvasImage(CanvasImageQuoteInput{
		Op:    CanvasOpVideo,
		Model: model.NANO_BANANA,
	})
	if !errors.Is(err, ErrUnknownCanvasOp) {
		t.Errorf("err = %v, want ErrUnknownCanvasOp", err)
	}
}

func TestQuoteCanvasUpscale_DefaultIs3Credits(t *testing.T) {
	got := QuoteCanvasUpscale(CanvasUpscaleQuoteInput{})
	if got.Credits != 3 {
		t.Errorf("default upscale = %d, want 3", got.Credits)
	}
}

func TestQuoteCanvasUpscale_Scale4Is5Credits(t *testing.T) {
	got := QuoteCanvasUpscale(CanvasUpscaleQuoteInput{Scale: 4})
	if got.Credits != 5 {
		t.Errorf("scale=4 upscale = %d, want 5", got.Credits)
	}
}

func TestQuoteCanvasUpscale_EnhanceFaceIs5Credits(t *testing.T) {
	got := QuoteCanvasUpscale(CanvasUpscaleQuoteInput{EnhanceFace: true})
	if got.Credits != 5 {
		t.Errorf("enhanceFace upscale = %d, want 5", got.Credits)
	}
}

func TestQuoteCanvasFlat_RemoveBgIs4Credits(t *testing.T) {
	// Flat pricing is a value contract — the catalog entry is the
	// only place this number lives, so pin it here.
	got, err := QuoteCanvasFlat(CanvasOpRemoveBg)
	if err != nil || got.Credits != 4 {
		t.Errorf("got (%d,%v), want (4, nil)", got.Credits, err)
	}
}

func TestQuoteCanvasFlat_NonFlatOpRejected(t *testing.T) {
	// Calling flat on an imageBase op must not silently return 0;
	// it would mask a handler-side routing bug.
	_, err := QuoteCanvasFlat(CanvasOpImage)
	if !errors.Is(err, ErrUnknownCanvasOp) {
		t.Errorf("err = %v, want ErrUnknownCanvasOp", err)
	}
}

func TestQuoteCanvasFlat_UnknownOpRejected(t *testing.T) {
	_, err := QuoteCanvasFlat(CanvasOp("does-not-exist"))
	if !errors.Is(err, ErrUnknownCanvasOp) {
		t.Errorf("err = %v, want ErrUnknownCanvasOp", err)
	}
}

func TestListCanvasPricingEntries_IsSnapshot(t *testing.T) {
	// Callers mutating the returned slice mustn't corrupt the table.
	a := ListCanvasPricingEntries()
	if len(a) > 0 {
		a[0] = CanvasPricingEntry{Op: "mutated"}
	}
	b := ListCanvasPricingEntries()
	for _, e := range b {
		if e.Op == "mutated" {
			t.Fatal("ListCanvasPricingEntries leaked mutation")
		}
	}
}
