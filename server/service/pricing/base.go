// Package pricing is the single source of truth for credit-cost
// calculations across WorkMax's image / video / canvas / tooling
// surfaces. Before this package existed, the per-model base credit
// table and per-op pricing math was duplicated across
// service/tools/pricing_catalog.go and
// service/tools/canvas/pricing_catalog.go — same numbers, two
// places, drift-prone (the 2026-05-15 audit caught one such drift
// already: canvas catalog ignored config.yaml overrides while
// legacy tools honoured them).
//
// Design posture:
//
//   • This package depends only on `config`, `globals`, and
//     `model`. None of those depend on pricing, so there's no
//     cycle risk regardless of how upstream packages evolve.
//   • Both server/service/tools/* and server/service/tools/canvas/*
//     import this package. Their public APIs (BaseImageCredits in
//     tools, canvasBaseImageCredits in canvas) become thin
//     delegations — same call sites continue to work; the math
//     happens here.
//   • Config-aware by default. config.yaml
//     `generator.models.<key>.credit_cost` overrides take
//     precedence over the hardcoded fallback table. Both tools and
//     canvas pick up the override automatically because they share
//     this implementation.

package pricing

import (
	"server/globals"
	"server/model"
)

// imageBaseCredits is the single source of truth for per-model
// image-generation base credits. Per-model pricing functions
// (QuoteImage and friends) read this via BaseImageCredits.
//
// Values mirror what was previously duplicated across
// tools.imageBaseCredits and canvas.canvasImageBaseCredits.
// GPT_IMAGE_2 base = 6 covers the catalog migration that landed
// in commit f264e10a (the 2026-05-15 A2 refactor).
var imageBaseCredits = map[string]int{
	model.NANO_BANANA:     3,
	model.NANO_BANANA_2:   4,
	model.NANO_BANANA_PRO: 8,
	// GPT Image 2 base credit. Resolution × quality multipliers are
	// applied in the per-model pricer (image.go::quoteGptImage2).
	model.GPT_IMAGE_2: 6,
}

// BaseImageCredits resolves the per-model base credit cost honoring
// the three-tier resolution order:
//
//   1. config.yaml `generator.models.<key>.credit_cost` (highest)
//   2. imageBaseCredits hardcoded fallback table
//   3. (0, false) — unknown model; caller decides whether to
//      reject or default to 1
//
// The second return is false ONLY when no fallback exists. config-
// override values of 0 are ignored (treated as "not set") so an
// accidentally-zeroed config entry doesn't grant free generations.
//
// Replaces the legacy tools.BaseImageCredits (which returns int and
// no ok flag) plus canvas.canvasBaseImageCredits. Both are now thin
// wrappers around this function.
func BaseImageCredits(modelID string) (int, bool) {
	cfg := globals.GraConf.Generator
	for _, key := range model.ModelConfigKeys(modelID) {
		if mc, ok := cfg.Models[key]; ok && mc.CreditCost > 0 {
			return mc.CreditCost, true
		}
	}
	if credits, ok := imageBaseCredits[model.NormalizeModelID(modelID)]; ok {
		return credits, true
	}
	return 0, false
}
