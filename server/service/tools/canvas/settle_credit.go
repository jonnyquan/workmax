// Settle-time credit cost resolution for canvas-routed tasks (the
// 2026-05-15 A2 migration). Before this file, canvas_generation_api.go
// :251 called tools.GetCreditCostByToolID for ALL canvas-routed
// generation submissions (image / upscale / remove-bg / vectorize).
// That coupled canvas settle to the legacy ad-hoc pricing path the
// catalog was meant to replace.
//
// After: canvas_generation_api.go calls SettleCreditCost (here),
// which dispatches per toolID into the catalog's per-op quote
// functions. The catalog's GPT_IMAGE_2 pricer landed in the same
// commit, closing the parity gap that previously blocked this flip
// (see pricing_catalog_settle_parity_test.go).
//
// Posture:
//
//   • Quote and settle now share ONE pricing source of truth. Any
//     future canvas-specific re-pricing lands in pricing_catalog.go
//     and both endpoints pick it up.
//   • Unknown toolID falls back to GetCreditCostByToolID — keeps
//     non-canvas tools (lora / avatar / prompt builder / video
//     prompt builder) routing through the legacy path. That's the
//     legacy path's actual job; canvas is just stepping off it.
//   • Errors fall back to a minimum-3-credit default. Same posture
//     as the old call site so an unrecognised request shape doesn't
//     accidentally create a free generation.

package canvas

import (
	"server/model"
)

// SettleCreditCost resolves the credit cost for a canvas-routed
// generation submission via the catalog. Returns (credits, true)
// when the catalog recognised the toolID + payload shape;
// (0, false) when the caller should fall back to the legacy
// path (tools.GetCreditCostByToolID).
//
// The (int, bool) return shape exists so this file can stay
// inside the canvas package without importing server/service/tools
// — that import direction would create a cycle (tools already
// imports canvas via generator_service). The fallback dispatch
// happens at the API handler in canvas_generation_api.go, where
// both packages are already in scope.
//
// requestData is the loose JSONMap the handler assembled from
// the HTTP request — same map that flows into the task queue.
//
// Why this exists at all (vs leaving canvas_generation_api on
// legacy): the 2026-05-15 A2 refactor closed the parity gap
// for GPT_IMAGE_2 in the catalog. With quote and settle both
// rooting in the same per-model pricers, the previous "two
// pricing tables for the same canvas image op" problem is gone.
func SettleCreditCost(toolID, modelID string, requestData model.JSONMap) (int, bool) {
	switch toolID {
	case model.TOOL_IMAGE_GENERATOR:
		return settleImageCredit(modelID, requestData)
	case model.TOOL_BACKGROUND_REMOVER:
		quote, err := QuoteCanvasFlat(CanvasOpRemoveBg)
		if err != nil {
			return 0, false
		}
		return quote.Credits, true
	case model.TOOL_IMAGE_UPSCALER:
		scale := readIntParam(requestData, "scale")
		enhance := readBoolParam(requestData, "enhanceFace")
		if !enhance {
			enhance = readBoolParam(requestData, "enhanceQuality")
		}
		return QuoteCanvasUpscale(CanvasUpscaleQuoteInput{
			Scale:       scale,
			EnhanceFace: enhance,
		}).Credits, true
	case model.TOOL_IMAGE_VECTORIZER:
		detail := readStringParam(requestData, "detailLevel")
		return QuoteCanvasVectorize(CanvasVectorizeQuoteInput{
			DetailLevel: detail,
		}).Credits, true
	default:
		// Unknown toolID — non-canvas tool routing through, or
		// a new tool not yet covered. Caller falls back to
		// legacy GetCreditCostByToolID.
		return 0, false
	}
}

// settleImageCredit prices an image_generator submission via the
// catalog's QuoteCanvasImage. The canvasOperation field on
// requestData picks which CanvasOp (img2img / outpaint / inpaint
// / etc.); when absent we treat it as a plain "image" generate.
//
// Returns (0, false) on any catalog error — caller falls back to
// legacy. Catalog parity covers all the (canvasOp, model, params)
// combinations the FE produces (pinned by
// pricing_catalog_settle_parity_test.go); the fallback is
// defensive scaffolding for unforeseen payload shapes.
func settleImageCredit(modelID string, requestData model.JSONMap) (int, bool) {
	canvasOp := canvasOpFromRequest(requestData)
	in := CanvasImageQuoteInput{
		Op:             canvasOp,
		Model:          modelID,
		NumberOfImages: readIntParam(requestData, "numberOfImages"),
		Resolution:     readStringParam(requestData, "resolution"),
		Quality:        readStringParam(requestData, "quality"),
		Upscale:        readBoolParam(requestData, "upscale"),
	}
	quote, err := QuoteCanvasImage(in)
	if err != nil {
		return 0, false
	}
	return quote.Credits, true
}

// canvasOpFromRequest reads requestData["canvasOperation"] and maps
// it to a CanvasOp. The FE stamps this field via
// setCanvasOperationMeta (canvas_generation_api.go) — values like
// "img2img" / "outpaint" / "inpaint" / "mockup" / "edit-text" /
// "split-layers" / "generate". Missing or unrecognised values
// default to CanvasOpImage so the most common "plain generate"
// flow doesn't require a stamp.
func canvasOpFromRequest(req model.JSONMap) CanvasOp {
	raw := readStringParam(req, "canvasOperation")
	if raw == "" {
		return CanvasOpImage
	}
	op := CanvasOp(raw)
	if _, ok := canvasPricingTable[op]; !ok {
		return CanvasOpImage
	}
	return op
}

func readStringParam(req model.JSONMap, key string) string {
	if req == nil {
		return ""
	}
	if v, ok := req[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func readIntParam(req model.JSONMap, key string) int {
	if req == nil {
		return 0
	}
	v, ok := req[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int:
		return n
	case int32:
		return int(n)
	case int64:
		return int(n)
	case float32:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

func readBoolParam(req model.JSONMap, key string) bool {
	if req == nil {
		return false
	}
	v, ok := req[key]
	if !ok {
		return false
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}
