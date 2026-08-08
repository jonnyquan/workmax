// Canvas pricing catalog (§13 M1-W1-03). After the 2026-05-15
// unification refactor, this file owns the canvas-op enum + the
// op→category routing table + thin adapters to the neutral
// `server/service/pricing` package. All actual pricing math
// (image / video / upscale / flat / vectorize) lives in pricing.
//
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
// ⚠ WIRE STATUS (2026-05-15, post pricing-unification step 4/N):
//
//	Quote path (canvas_quote_api.go):
//	  ✓ mode=image     → canvas.QuoteCanvasImage     → pricing.QuoteImage
//	  ✓ mode=upscale   → canvas.QuoteCanvasUpscale   → pricing.QuoteUpscale
//	  ✓ mode=remove-bg → canvas.QuoteCanvasFlat      → pricing.QuoteFlat
//	  ✓ mode=vectorize → canvas.QuoteCanvasVectorize → pricing.QuoteVectorize
//	  ✓ mode=video     → tools.QuoteVideoCredits     → pricing.QuoteVideo
//	                       (tools wraps because NormalizeVideoQuoteOptions
//	                       reads the capability registry which is an
//	                       FE-wire concern owned by tools)
//
//	Settle path (canvas_generation_api.go::submitCanvasGenerationTask):
//	  ✓ canvas.SettleCreditCost  → pricing.Quote* per toolID
//	    — Falls back to tools.GetCreditCostByToolID only for
//	    non-canvas toolIDs (lora / avatar / prompt builder / etc.
//	    routing through here accidentally). Those legacy branches
//	    ALSO delegate to pricing internally — see
//	    server/service/tools/credit_cost.go.
//
// Quote AND settle root in the same pricing-package functions; one
// source of truth for all canvas pricing. Future canvas-specific
// re-pricing lands in pricing/, in one place; every surface picks
// it up.
//
// Image pricing uses a per-model registry (pricing.imageModelPricers)
// added in the 2026-05-15 A2 refactor: each model has its OWN
// pricing function. Adding a new image model = register one entry
// + write one function in pricing/image.go — no central if-tree
// to edit.
//
// Base credits resolve config-first via pricing.BaseImageCredits:
// `generator.models.<key>.credit_cost` in config.yaml takes
// precedence over the fallback table. This matters: before
// 2026-05-15 the canvas catalog had its own local table that
// ignored config while legacy settle honoured it — quote and
// settle disagreed under any config override. Pinned by
// TestCatalogSettleParity_RespectsConfigOverride.
//
// Next concrete moves: none scheduled. The unification milestone
// closed the last open item (whether video pricing should move
// into the catalog — yes, done in 2026-05-15 step 4/N).
//
// Parity test pins (pricing_catalog_settle_parity_test.go):
//   - NANO_BANANA / NANO_BANANA_2 / NANO_BANANA_PRO across all
//     imageBase ops: catalog and legacy MUST return identical credit
//     counts.
//   - GPT_IMAGE_2 across the 24-cell resolution × quality × n matrix.
//   - Upscale (scale=2/4 × enhanceFace) — agree.
//   - Remove-bg flat=4 — agree.
//   - Vectorize (medium / high, "HIGH" stays medium per case-
//     sensitive legacy comparison) — agree.
//   - Config override scenario: ops setting
//     generator.models.nanobanana.credit_cost in config.yaml — both
//     sides honour it (and both sides ARE the same implementation
//     now, so they can't disagree).
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
//
// Before this file, pricing was assembled ad-hoc at the handler: the
// Quote handler called tools.GetCreditCostByToolID with a manually
// constructed CreditCostParams, and if any Canvas op ever needed to
// diverge from the generic tool pricing (e.g. a promotion on outpaint,
// a different credit multiplier for inpaint vs plain generation) the
// only way to express that would have been inside the generic tools
// pricing switch — which cascades into every surface that calls it.
//
// The catalog flips the dependency: canvasPricingTable declares every
// priceable Canvas op and routes it into a pricing category (imageBase,
// videoBase, upscale, flat, vectorize). After the unification, the
// per-category quote functions in this file are thin adapters that
// forward to pricing.Quote* — canvas owns "which canvas ops exist
// and how they map to pricing categories"; pricing owns "what each
// model/op actually costs". Two responsibilities, two packages.

package canvas

import (
	"errors"

	"server/service/pricing"
)

// CanvasOp is an enumeration of priceable Canvas operations. Every op
// registered here must appear in canvasPricingTable below.
type CanvasOp string

const (
	CanvasOpImage           CanvasOp = "image"
	CanvasOpVideo           CanvasOp = "video"
	CanvasOpImg2Img         CanvasOp = "img2img"
	CanvasOpOutpaint        CanvasOp = "outpaint"
	CanvasOpInpaint         CanvasOp = "inpaint"
	CanvasOpMockup          CanvasOp = "mockup"
	CanvasOpEditText        CanvasOp = "edit-text"
	CanvasOpSplitLayers     CanvasOp = "split-layers"
	CanvasOpUpscale         CanvasOp = "upscale"
	CanvasOpRemoveBg        CanvasOp = "remove-bg"
	CanvasOpVectorize       CanvasOp = "vectorize"
	CanvasOpExtractKeyframe CanvasOp = "extract-keyframe"
)

// CanvasPricingCategory groups ops by the shape of their pricing
// resolver. New categories should be introduced sparingly — each one
// adds a branch to every caller that wants to quote an op generically.
type CanvasPricingCategory string

const (
	PricingCategoryImageBase CanvasPricingCategory = "imageBase"
	PricingCategoryVideoBase CanvasPricingCategory = "videoBase"
	PricingCategoryUpscale   CanvasPricingCategory = "upscale"
	PricingCategoryVectorize CanvasPricingCategory = "vectorize"
	PricingCategoryFlat      CanvasPricingCategory = "flat"
)

// CanvasPricingEntry is one row of canvasPricingTable.
type CanvasPricingEntry struct {
	Op          CanvasOp
	Category    CanvasPricingCategory
	FlatCredits int // only populated when Category == PricingCategoryFlat
}

// canvasPricingTable is the authoritative Canvas op → pricing-path
// map. Every priceable Canvas op MUST appear here; the pricing-table
// completeness test pins that invariant.
var canvasPricingTable = map[CanvasOp]CanvasPricingEntry{
	CanvasOpImage:           {Op: CanvasOpImage, Category: PricingCategoryImageBase},
	CanvasOpVideo:           {Op: CanvasOpVideo, Category: PricingCategoryVideoBase},
	CanvasOpImg2Img:         {Op: CanvasOpImg2Img, Category: PricingCategoryImageBase},
	CanvasOpOutpaint:        {Op: CanvasOpOutpaint, Category: PricingCategoryImageBase},
	CanvasOpInpaint:         {Op: CanvasOpInpaint, Category: PricingCategoryImageBase},
	CanvasOpMockup:          {Op: CanvasOpMockup, Category: PricingCategoryImageBase},
	CanvasOpEditText:        {Op: CanvasOpEditText, Category: PricingCategoryImageBase},
	CanvasOpSplitLayers:     {Op: CanvasOpSplitLayers, Category: PricingCategoryImageBase},
	CanvasOpUpscale:         {Op: CanvasOpUpscale, Category: PricingCategoryUpscale},
	CanvasOpRemoveBg:        {Op: CanvasOpRemoveBg, Category: PricingCategoryFlat, FlatCredits: 4},
	CanvasOpVectorize:       {Op: CanvasOpVectorize, Category: PricingCategoryVectorize},
	CanvasOpExtractKeyframe: {Op: CanvasOpExtractKeyframe, Category: PricingCategoryImageBase},
}

// canvasBaseImageCredits is now a thin delegation to
// pricing.BaseImageCredits — the unified base table moved to
// server/service/pricing in the 2026-05-15 pricing unification.
// Canvas, tools, and any future surface all read from the same
// place, with config.yaml overrides honoured identically.
//
// Previously this lived as a local copy of the per-model map
// (canvasImageBaseCredits). The duplication was a real source of
// drift: the 2026-05-15 audit found canvas ignored config.yaml
// overrides while legacy tools honoured them. The unification
// eliminates that class of bug structurally.
func canvasBaseImageCredits(modelID string) (int, bool) {
	return pricing.BaseImageCredits(modelID)
}

// Sentinel errors surface back to handlers so they can classify bad
// op/model combinations into 400s instead of 500s.
var (
	ErrUnknownCanvasOp    = errors.New("canvas: unknown pricing op")
	ErrUnknownCanvasModel = errors.New("canvas: unknown pricing model")
)

// LookupCanvasOp returns the pricing entry for op.
func LookupCanvasOp(op CanvasOp) (CanvasPricingEntry, bool) {
	e, ok := canvasPricingTable[op]
	return e, ok
}

// IsKnownCanvasOp reports whether op is registered in the table.
func IsKnownCanvasOp(op CanvasOp) bool {
	_, ok := canvasPricingTable[op]
	return ok
}

// ListCanvasPricingEntries returns a snapshot copy for diagnostics.
func ListCanvasPricingEntries() []CanvasPricingEntry {
	out := make([]CanvasPricingEntry, 0, len(canvasPricingTable))
	for _, v := range canvasPricingTable {
		out = append(out, v)
	}
	return out
}

// CanvasImageBaseCredits exposes the per-model image base table with
// config overrides applied. The second return is false for unknown
// models so callers can decide whether to reject or fall back to a
// default. Wraps canvasBaseImageCredits — exists for the legacy
// public API contract; new callers should expect identical results.
func CanvasImageBaseCredits(modelID string) (int, bool) {
	return canvasBaseImageCredits(modelID)
}

// CanvasImageQuoteInput bundles an imageBase-category pricing request.
//
// Quality (GPT_IMAGE_2-specific): one of "" / "low" / "medium" /
// "auto" / "high". Empty/auto/medium are equivalent (1× multiplier).
// Ignored by other models. Exposed on the shared input rather than a
// model-specific subtype because the FE / Quote handler doesn't know
// which model's pricer the dispatch will land on.
type CanvasImageQuoteInput struct {
	Op             CanvasOp
	Model          string
	NumberOfImages int
	Resolution     string
	Upscale        bool
	Quality        string
}

// CanvasQuote is the outcome of any pricing call.
type CanvasQuote struct {
	Credits int
}

// QuoteCanvasImage prices an imageBase-category canvas op. After
// the 2026-05-15 unification, per-model image pricing math lives
// in the neutral pricing package; this function is now a thin
// adapter that:
//
//   1. Gates on the canvas op enum (canvas-specific concept that
//      pricing package shouldn't know about).
//   2. Forwards the model + params to pricing.QuoteImage.
//   3. Maps pricing's sentinel errors back to canvas's so handlers
//      keep one classification surface.
//
// Op gate: passing CanvasOpVideo / CanvasOpUpscale here returns
// ErrUnknownCanvasOp — those have their own quote functions
// (QuoteCanvasUpscale / QuoteCanvasFlat / QuoteCanvasVectorize).
func QuoteCanvasImage(in CanvasImageQuoteInput) (CanvasQuote, error) {
	entry, ok := canvasPricingTable[in.Op]
	if !ok {
		return CanvasQuote{}, ErrUnknownCanvasOp
	}
	if entry.Category != PricingCategoryImageBase {
		return CanvasQuote{}, ErrUnknownCanvasOp
	}

	quote, err := pricing.QuoteImage(pricing.ImageQuoteInput{
		Model:          in.Model,
		NumberOfImages: in.NumberOfImages,
		Resolution:     in.Resolution,
		Upscale:        in.Upscale,
		Quality:        in.Quality,
	})
	if err != nil {
		if errors.Is(err, pricing.ErrUnknownImageModel) {
			return CanvasQuote{}, ErrUnknownCanvasModel
		}
		return CanvasQuote{}, err
	}
	return CanvasQuote{Credits: quote.Credits}, nil
}

// CanvasUpscaleQuoteInput bundles the upscale category's inputs.
type CanvasUpscaleQuoteInput struct {
	Scale       int
	EnhanceFace bool
}

// QuoteCanvasUpscale prices the upscale canvas op. After the
// 2026-05-15 unification, the pricing math lives in
// pricing.QuoteUpscale; this is a thin adapter that translates
// the canvas input shape to the neutral pricing shape.
func QuoteCanvasUpscale(in CanvasUpscaleQuoteInput) CanvasQuote {
	q := pricing.QuoteUpscale(pricing.UpscaleQuoteInput{
		Scale:       in.Scale,
		EnhanceFace: in.EnhanceFace,
	})
	return CanvasQuote{Credits: q.Credits}
}

// CanvasVectorizeQuoteInput bundles the vectorize category's input.
type CanvasVectorizeQuoteInput struct {
	DetailLevel string
}

// QuoteCanvasVectorize prices the vectorize canvas op. Thin
// adapter around pricing.QuoteVectorize.
func QuoteCanvasVectorize(in CanvasVectorizeQuoteInput) CanvasQuote {
	q := pricing.QuoteVectorize(pricing.VectorizeQuoteInput{
		DetailLevel: in.DetailLevel,
	})
	return CanvasQuote{Credits: q.Credits}
}

// QuoteCanvasFlat returns the flat cost registered for op. Maps
// canvas-side CanvasOp to pricing-side FlatOp; only the registered
// flat ops produce a quote. The canvas op gate ensures imageBase
// ops can't accidentally route through here.
//
// Pricing-side FlatOp is currently mirrored from the canvas
// FlatCredits table — see PricingCategoryFlat entries in
// canvasPricingTable below. If you add a new flat op, register
// it BOTH in canvasPricingTable (canvas op enum) AND in
// pricing.flatOpCredits (the numeric truth). Yes that's still
// two-places, but: the canvas table is about WHICH canvas ops
// are flat-shaped (a canvas concept); the pricing table is about
// WHAT VALUE each flat op carries (a pricing concept). Different
// axes.
func QuoteCanvasFlat(op CanvasOp) (CanvasQuote, error) {
	entry, ok := canvasPricingTable[op]
	if !ok || entry.Category != PricingCategoryFlat {
		return CanvasQuote{}, ErrUnknownCanvasOp
	}
	q, err := pricing.QuoteFlat(canvasOpToFlatOp(op))
	if err != nil {
		// Pricing doesn't know about this op yet; fall back to
		// the canvas-side FlatCredits value (preserves behaviour
		// until both tables align).
		return CanvasQuote{Credits: entry.FlatCredits}, nil
	}
	return CanvasQuote{Credits: q.Credits}, nil
}

// canvasOpToFlatOp maps a canvas CanvasOp into the pricing
// FlatOp domain. Only the registered flat ops have a mapping;
// unknown ops produce a sentinel that QuoteFlat rejects.
func canvasOpToFlatOp(op CanvasOp) pricing.FlatOp {
	switch op {
	case CanvasOpRemoveBg:
		return pricing.FlatOpRemoveBg
	default:
		return pricing.FlatOp("")
	}
}
