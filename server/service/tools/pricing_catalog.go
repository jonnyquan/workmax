package tools

import (
	"server/model"
	"server/service/pricing"
)

// videoModelOrder is the canonical display order of video models. Shared by
// ListVideoModelCapabilities (capability endpoint) and buildVideoPricingCatalog
// (pricing endpoint) so reordering happens in one place.
var videoModelOrder = []string{
	model.SEEDANCE_2,
	model.SEEDANCE_2_FAST,
	model.SEEDANCE,
	model.KLING_2_6,
	model.SORA_2,
	model.VEO_31,
	model.VEO_31_FAST,
	model.MINIMAX_VIDEO,
}

// BaseImageCredits returns the base credit cost for an image-generation
// model. Thin delegation to pricing.BaseImageCredits — the unified base
// table moved to the pricing package (2026-05-15 unification refactor)
// so canvas, tools, and any future surface read from the same place.
//
// Legacy API shape preserved: this function returns int (with a default
// of 1 for unknown models) rather than (int, bool). Callers that need
// to distinguish "unknown model" from "model with cost 1" should use
// pricing.BaseImageCredits directly.
func BaseImageCredits(modelID string) int {
	if credits, ok := pricing.BaseImageCredits(modelID); ok {
		return credits
	}
	return 1
}

// VideoModelOrder returns a copy of the canonical video model display order.
func VideoModelOrder() []string {
	out := make([]string, len(videoModelOrder))
	copy(out, videoModelOrder)
	return out
}
