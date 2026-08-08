// pricing_catalog_settle_parity_test.go — pins where the two
// pricing paths agree and where they diverge, in advance of any
// future migration of canvas_generation_api.go:251 from
// tools.GetCreditCostByToolID (legacy settle) to
// canvas.QuoteCanvasImage (catalog).
//
// Why this exists (vs just flipping the call site):
//
//   - The catalog covers NANO_BANANA / NANO_BANANA_2 / NANO_BANANA_PRO
//     for image-base ops. For these, the two paths return identical
//     credit counts and the migration would be byte-equal.
//   - The catalog does NOT cover GPT_IMAGE_2. The legacy path has
//     ~30 LOC of GPT_IMAGE_2-specific math (resolution × quality
//     matrix) the catalog never absorbed. Flipping the settle path
//     today would surface a "credit changed" regression for every
//     user generating with GPT_IMAGE_2.
//   - This file is the durable proof of which side of that gap a
//     model lives on. A future PR that wants to flip the settle
//     path will run this suite, see what's covered and what isn't,
//     and either extend the catalog or scope the flip to the
//     models that already agree.
//
// The test does NOT call the legacy path's internals directly —
// it calls the public surface (tools.GetCreditCostByToolID) and
// the catalog's public surface (canvas.QuoteCanvasImage). If
// either implementation shifts, the test surfaces the drift.

// External test package (canvas_test) so we can import
// server/service/tools (which depends on canvas) without an
// import cycle. Keeps the parity contract above-the-line.

package canvas_test

import (
	"strconv"
	"testing"

	"server/config"
	"server/globals"
	"server/model"
	toolsService "server/service/tools"
	canvasService "server/service/tools/canvas"
)

type pricingParityCase struct {
	name           string
	op             canvasService.CanvasOp
	modelID        string
	numberOfImages int
	resolution     string
	upscale        bool
	// expectCatalogErr is the catalog's expected error sentinel
	// when the model is outside its coverage. Empty when the
	// catalog should succeed.
	expectCatalogErr error
}

func TestCatalogSettleParity_AgreeForCanvasAwareModels(t *testing.T) {
	// For models the catalog explicitly covers (NANO_BANANA family),
	// catalog and legacy MUST agree to the credit. A future PR that
	// changes either without updating the other surfaces here.
	cases := []pricingParityCase{
		{name: "nano-banana generate 1×", op: canvasService.CanvasOpImage, modelID: model.NANO_BANANA, numberOfImages: 1},
		{name: "nano-banana generate 3×", op: canvasService.CanvasOpImage, modelID: model.NANO_BANANA, numberOfImages: 3},
		{name: "nano-banana-2 generate 1×", op: canvasService.CanvasOpImage, modelID: model.NANO_BANANA_2, numberOfImages: 1},
		{name: "nano-banana-2 generate 4×", op: canvasService.CanvasOpImage, modelID: model.NANO_BANANA_2, numberOfImages: 4},
		{name: "nano-banana-pro generate 2k", op: canvasService.CanvasOpImage, modelID: model.NANO_BANANA_PRO, numberOfImages: 1, resolution: "2k"},
		{name: "nano-banana-pro generate 4k", op: canvasService.CanvasOpImage, modelID: model.NANO_BANANA_PRO, numberOfImages: 1, resolution: "4k"},
		{name: "nano-banana-pro upscale fixed price", op: canvasService.CanvasOpImage, modelID: model.NANO_BANANA_PRO, upscale: true},
		// img2img / outpaint / inpaint / mockup / edit-text / split-layers
		// all share the imageBase category — legacy doesn't differentiate
		// by canvas op (only by model + params), so they all agree by
		// way of "same number for same model".
		{name: "img2img on nano-banana-2", op: canvasService.CanvasOpImg2Img, modelID: model.NANO_BANANA_2, numberOfImages: 1},
		{name: "outpaint on nano-banana", op: canvasService.CanvasOpOutpaint, modelID: model.NANO_BANANA, numberOfImages: 1},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			catalogQuote, catalogErr := canvasService.QuoteCanvasImage(canvasService.CanvasImageQuoteInput{
				Op:             c.op,
				Model:          c.modelID,
				NumberOfImages: c.numberOfImages,
				Resolution:     c.resolution,
				Upscale:        c.upscale,
			})
			if catalogErr != nil {
				t.Fatalf("catalog should cover %s; got error %v", c.name, catalogErr)
			}

			legacyParams := map[string]interface{}{
				"model":          c.modelID,
				"numberOfImages": c.numberOfImages,
				"resolution":     c.resolution,
				"upscale":        c.upscale,
			}
			legacy := toolsService.GetCreditCostByToolID(toolsService.CreditCostParams{
				ToolID: model.TOOL_IMAGE_GENERATOR,
				Model:  c.modelID,
				Params: legacyParams,
			})

			if catalogQuote.Credits != legacy {
				t.Errorf(
					"pricing drift for %s: catalog=%d legacy=%d — quote and settle disagree",
					c.name, catalogQuote.Credits, legacy,
				)
			}
		})
	}
}

func TestCatalogSettleParity_AgreesOnGPTImage2(t *testing.T) {
	// GPT_IMAGE_2 was the documented divergence point until the
	// 2026-05-14 A2 refactor closed it (per-model registry in
	// canvas pricing_catalog.go). The catalog now has a dedicated
	// quoteGptImage2 pricer that mirrors legacy's resolution ×
	// quality matrix. This test pins agreement across the matrix
	// — if either side drifts (catalog inadvertently changed,
	// legacy stopped pricing GPT_IMAGE_2), the failure surfaces
	// here, with the diff in credit counts already in the error
	// message.
	//
	// Matrix: 3 resolutions × 4 qualities × 2 numberOfImages = 24
	// combinations. The shared parity formula keeps any cell from
	// drifting between catalog and legacy.
	resolutions := []string{"1k", "2k", "4k"}
	qualities := []string{"", "low", "medium", "high"}
	counts := []int{1, 3}

	for _, res := range resolutions {
		for _, q := range qualities {
			for _, n := range counts {
				name := "res=" + res + "/quality=" + q + "/n=" + strconv.Itoa(n)
				t.Run(name, func(t *testing.T) {
					in := canvasService.CanvasImageQuoteInput{
						Op:             canvasService.CanvasOpImage,
						Model:          model.GPT_IMAGE_2,
						NumberOfImages: n,
						Resolution:     res,
						Quality:        q,
					}
					catalogQuote, err := canvasService.QuoteCanvasImage(in)
					if err != nil {
						t.Fatalf("catalog: %v (closing the gap should not introduce errors)", err)
					}
					legacy := toolsService.GetCreditCostByToolID(toolsService.CreditCostParams{
						ToolID: model.TOOL_IMAGE_GENERATOR,
						Model:  model.GPT_IMAGE_2,
						Params: map[string]interface{}{
							"model":          model.GPT_IMAGE_2,
							"numberOfImages": n,
							"resolution":     res,
							"quality":        q,
						},
					})
					if catalogQuote.Credits != legacy {
						t.Errorf("drift: catalog=%d legacy=%d", catalogQuote.Credits, legacy)
					}
				})
			}
		}
	}
}

func TestCatalogSettleParity_UpscaleAndRemoveBgPaths(t *testing.T) {
	// Sibling parity checks for the two non-image-base catalog ops.
	// Upscale: catalog returns 5 for scale=4 or enhanceFace, else 3.
	// Legacy returns the same numbers via TOOL_IMAGE_UPSCALER.
	upscaleCases := []struct {
		name        string
		scale       int
		enhanceFace bool
		wantCredits int
	}{
		{"scale=2 no enhance", 2, false, 3},
		{"scale=4 no enhance", 4, false, 5},
		{"scale=2 + enhance", 2, true, 5},
	}
	for _, c := range upscaleCases {
		t.Run("upscale/"+c.name, func(t *testing.T) {
			cat := canvasService.QuoteCanvasUpscale(canvasService.CanvasUpscaleQuoteInput{
				Scale:       c.scale,
				EnhanceFace: c.enhanceFace,
			}).Credits
			leg := toolsService.GetCreditCostByToolID(toolsService.CreditCostParams{
				ToolID:      model.TOOL_IMAGE_UPSCALER,
				Scale:       c.scale,
				EnhanceFace: c.enhanceFace,
			})
			if cat != leg || cat != c.wantCredits {
				t.Errorf("upscale parity %s: catalog=%d legacy=%d want=%d",
					c.name, cat, leg, c.wantCredits)
			}
		})
	}

	// Remove-bg: catalog flat-prices at 4. Legacy via
	// TOOL_BACKGROUND_REMOVER also returns 4.
	t.Run("remove-bg flat=4", func(t *testing.T) {
		cat, err := canvasService.QuoteCanvasFlat(canvasService.CanvasOpRemoveBg)
		if err != nil {
			t.Fatalf("catalog remove-bg: %v", err)
		}
		leg := toolsService.GetCreditCostByToolID(toolsService.CreditCostParams{
			ToolID: model.TOOL_BACKGROUND_REMOVER,
		})
		if cat.Credits != leg || cat.Credits != 4 {
			t.Errorf("remove-bg parity: catalog=%d legacy=%d want=4", cat.Credits, leg)
		}
	})

	// Vectorize: catalog returns 4 for detailLevel="high", 3 for
	// anything else (medium default). Legacy via
	// TOOL_IMAGE_VECTORIZER prices by the same rule. Empty
	// detailLevel defaults to medium on the legacy side; the
	// catalog input is loose-strings and reads "" as also
	// non-high → 3. Both agree.
	vectorizeCases := []struct {
		name        string
		detailLevel string
		wantCredits int
	}{
		{"medium default", "medium", 3},
		{"empty defaults to medium", "", 3},
		{"low same as medium", "low", 3},
		{"high tier", "high", 4},
		// Case-sensitive: legacy compares "detail == 'high'" exactly,
		// so "HIGH" rolls down to medium-tier 3. Catalog matches.
		// If a future PR liberalises one side (e.g. lower-cases at
		// the entry), both must move together.
		{"HIGH stays medium (case-sensitive)", "HIGH", 3},
	}
	for _, c := range vectorizeCases {
		t.Run("vectorize/"+c.name, func(t *testing.T) {
			cat := canvasService.QuoteCanvasVectorize(canvasService.CanvasVectorizeQuoteInput{
				DetailLevel: c.detailLevel,
			}).Credits
			leg := toolsService.GetCreditCostByToolID(toolsService.CreditCostParams{
				ToolID:      model.TOOL_IMAGE_VECTORIZER,
				DetailLevel: c.detailLevel,
			})
			if cat != leg || cat != c.wantCredits {
				t.Errorf("vectorize parity %s: catalog=%d legacy=%d want=%d",
					c.name, cat, leg, c.wantCredits)
			}
		})
	}
}

// TestCatalogSettleParity_RespectsConfigOverride pins the bug that
// the 2026-05-14 A2 refactor fixed: canvas catalog ignored config
// .yaml `generator.models.<key>.credit_cost` overrides, while
// legacy settle (tools.BaseImageCredits) honoured them. Quote
// would report 3 credits then submit would charge 7. The catalog
// now reads the same config table, so both agree.
//
// Mutates globals.GraConf.Generator.Models temporarily. The
// teardown restores the previous map so other tests run in the
// same suite aren't poisoned by the override.
func TestCatalogSettleParity_RespectsConfigOverride(t *testing.T) {
	original := globals.GraConf.Generator.Models
	defer func() { globals.GraConf.Generator.Models = original }()

	// Build a fresh map with an override for nanobanana. Don't
	// mutate `original` in-place — it may be shared.
	override := make(map[string]config.ModelConfig, len(original)+1)
	for k, v := range original {
		override[k] = v
	}
	const overrideCost = 7
	override["nanobanana"] = config.ModelConfig{CreditCost: overrideCost}
	globals.GraConf.Generator.Models = override

	in := canvasService.CanvasImageQuoteInput{
		Op:             canvasService.CanvasOpImage,
		Model:          model.NANO_BANANA,
		NumberOfImages: 1,
	}
	catalogQuote, err := canvasService.QuoteCanvasImage(in)
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	if catalogQuote.Credits != overrideCost {
		t.Errorf("catalog ignored config override: got %d, want %d", catalogQuote.Credits, overrideCost)
	}

	legacy := toolsService.GetCreditCostByToolID(toolsService.CreditCostParams{
		ToolID: model.TOOL_IMAGE_GENERATOR,
		Model:  model.NANO_BANANA,
		Params: map[string]interface{}{
			"model":          model.NANO_BANANA,
			"numberOfImages": 1,
		},
	})
	if legacy != overrideCost {
		t.Errorf("legacy ignored config override: got %d, want %d", legacy, overrideCost)
	}
	if catalogQuote.Credits != legacy {
		t.Errorf("config-override parity drift: catalog=%d legacy=%d", catalogQuote.Credits, legacy)
	}
}
