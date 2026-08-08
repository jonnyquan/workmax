// Per-model image pricing (unified pricing package, step 2 of the
// 2026-05-15 refactor). Before this file, the per-model image
// pricers (quoteNanoBananaBase / quoteNanoBananaPro / quoteGptImage2)
// lived inside server/service/tools/canvas/pricing_catalog.go and
// tools/credit_cost.go re-implemented the same math inline in
// getGenerateCreditCostByFeature. Two homes, identical numbers,
// drift-prone.
//
// This file is THE home. Both canvas (via QuoteCanvasImage's
// adapter) and tools (via getGenerateCreditCostByFeature's
// delegation) call into QuoteImage here.
//
// Design — per-model registry over single-function if-tree:
// WorkMax adds an image-generation model every 1-3 months. The
// alternative (one big QuoteImage with model branches) requires
// readers to load the entire function into their head before
// adding a model. Per-model functions keep each formula isolated,
// give each its own focused test surface, and make the
// "what does X model cost" navigation a one-grep / one-jump.

package pricing

import (
	"errors"
	"strings"

	"server/model"
)

// ImageQuoteInput is the input shape every image pricer accepts.
//
// Quality (GPT_IMAGE_2-specific): "" / "low" / "medium" / "auto"
// / "high". Empty/auto/medium are equivalent (1× multiplier).
// Other models ignore it. Living on the shared input rather than a
// model-specific subtype lets the dispatcher route without
// knowing the model's pricer ahead of time.
type ImageQuoteInput struct {
	Model          string
	NumberOfImages int
	Resolution     string
	Upscale        bool
	Quality        string
}

// ImageQuote is the outcome of any image pricing call.
type ImageQuote struct {
	Credits int
}

// ErrUnknownImageModel surfaces when the caller passes a modelID
// that no pricer handles. The Quote handler classifies this as a
// 400 rather than silently defaulting to 1 credit — pricing-
// unaware models would otherwise route through as cheap.
var ErrUnknownImageModel = errors.New("pricing: unknown image model")

// imagePricingFn is the per-model pricer contract. Add a model by
// registering it in imageModelPricers and writing the function.
type imagePricingFn func(in ImageQuoteInput) (ImageQuote, error)

// imageModelPricers is the registry. NANO_BANANA + _2 share
// quoteNanoBananaBase because they only differ in base cost,
// which BaseImageCredits handles.
var imageModelPricers = map[string]imagePricingFn{
	model.NANO_BANANA:     quoteNanoBananaBase,
	model.NANO_BANANA_2:   quoteNanoBananaBase,
	model.NANO_BANANA_PRO: quoteNanoBananaPro,
	model.GPT_IMAGE_2:     quoteGptImage2,
}

// QuoteImage dispatches to the per-model pricer. Empty modelID
// defaults to NANO_BANANA so legacy clients that omit the field
// keep working. Unknown modelID surfaces ErrUnknownImageModel so
// callers can reject explicitly.
func QuoteImage(in ImageQuoteInput) (ImageQuote, error) {
	modelID := model.NormalizeModelID(in.Model)
	if modelID == "" {
		modelID = model.NANO_BANANA
	}
	pricer, ok := imageModelPricers[modelID]
	if !ok {
		return ImageQuote{}, ErrUnknownImageModel
	}
	in.Model = modelID
	return pricer(in)
}

// IsKnownImageModel reports whether a pricer is registered for
// modelID. Used by callers that want to gate behaviour BEFORE
// computing a quote (e.g. capability endpoints).
func IsKnownImageModel(modelID string) bool {
	_, ok := imageModelPricers[model.NormalizeModelID(modelID)]
	return ok
}

// ──────────────────────────────────────────────────────────────────
// Per-model pricers. Each consumes ImageQuoteInput and returns an
// ImageQuote. Base costs come from BaseImageCredits (config-aware);
// each function only encodes its model's multiplier matrix.
// ──────────────────────────────────────────────────────────────────

// quoteNanoBananaBase handles NANO_BANANA and NANO_BANANA_2 — the
// "plain" tier with no resolution / quality multiplier. Cost is
// simply base × numberOfImages, capped at base when n ≤ 1.
func quoteNanoBananaBase(in ImageQuoteInput) (ImageQuote, error) {
	base, ok := BaseImageCredits(in.Model)
	if !ok {
		return ImageQuote{}, ErrUnknownImageModel
	}
	n := normalizeImageCount(in.NumberOfImages)
	if n > 1 {
		return ImageQuote{Credits: base * n}, nil
	}
	return ImageQuote{Credits: base}, nil
}

// quoteNanoBananaPro handles NANO_BANANA_PRO — the "pro" tier with
// 4k resolution multiplier and a flat 12-credit upscale short-
// circuit. Upscale wins over resolution + count because the legacy
// path special-cased it that way; pinned in the parity test.
func quoteNanoBananaPro(in ImageQuoteInput) (ImageQuote, error) {
	base, ok := BaseImageCredits(in.Model)
	if !ok {
		return ImageQuote{}, ErrUnknownImageModel
	}
	if in.Upscale {
		// Flat 12 — short-circuits resolution + count entirely.
		return ImageQuote{Credits: 12}, nil
	}
	resolution := in.Resolution
	if resolution == "" {
		resolution = "2k"
	}
	cost := base
	if resolution == "4k" {
		cost *= 2
	}
	if n := normalizeImageCount(in.NumberOfImages); n > 1 {
		cost *= n
	}
	return ImageQuote{Credits: cost}, nil
}

// quoteGptImage2 handles GPT_IMAGE_2 — the "OpenAI bills both axes"
// tier with resolution × quality matrix:
//
//   resolution:  1k → 1×, 2k → 2×, 4k → 4×   (default empty → 1×)
//   quality:     low → 0.5×, medium/auto/empty → 1×, high → 2×
//
// Quality multiplier expressed as num/den so the only fractional
// case (low = 1/2) uses ceiling division — never settle for a
// free generation. Final floor at 1 credit defends cheap configs.
// NumberOfImages multiplier is applied BEFORE the quality
// fraction (per the legacy ordering, pinned by parity test).
func quoteGptImage2(in ImageQuoteInput) (ImageQuote, error) {
	base, ok := BaseImageCredits(in.Model)
	if !ok {
		return ImageQuote{}, ErrUnknownImageModel
	}
	quality := strings.ToLower(strings.TrimSpace(in.Quality))
	resMultiplier := 1
	switch in.Resolution {
	case "4k":
		resMultiplier = 4
	case "2k":
		resMultiplier = 2
	}
	qNum, qDen := 1, 1
	switch quality {
	case "high":
		qNum, qDen = 2, 1
	case "low":
		qNum, qDen = 1, 2
	}
	cost := base * resMultiplier
	if n := normalizeImageCount(in.NumberOfImages); n > 1 {
		cost *= n
	}
	// Ceiling division so 0.5× never rounds to 0 on cheap configs.
	cost = (cost*qNum + qDen - 1) / qDen
	if cost < 1 {
		cost = 1
	}
	return ImageQuote{Credits: cost}, nil
}

// normalizeImageCount floors numberOfImages at 1. The legacy path
// applies the same floor; centralising here keeps the per-model
// pricers from each repeating the guard.
func normalizeImageCount(n int) int {
	if n < 1 {
		return 1
	}
	return n
}
