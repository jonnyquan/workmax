package model

import (
	"math"
	"strconv"
)

// AssetBinding is the canonical shape for a canvas §8.4 asset binding
// (character / brand / product IDs attached to an element, shot, or the
// whole project). It lives in the model package — rather than under
// service/tools/canvas — so model.TaskRequestData can embed it without
// an import cycle (canvas already depends on model).
//
// The canvas package re-exports this type as `canvas.AssetBinding` via
// a Go type alias, so existing canvas callers keep their local names
// and JSON roundtrips unchanged.

// AssetBindingScope enumerates where an AssetBinding attaches.
type AssetBindingScope string

const (
	AssetScopeElement AssetBindingScope = "element"
	AssetScopeShot    AssetBindingScope = "shot"
	AssetScopeProject AssetBindingScope = "project"
)

// Weight bounds for AssetBinding multiplier hints. Exported here so the
// API parser, the injector, and any future consumer share one source of
// truth. Frontend mention-parser uses the same [0.1, 2] window.
const (
	AssetWeightDefault = 1.0
	AssetWeightMin     = 0.1
	AssetWeightMax     = 2.0
)

// AssetWeightMap is an optional per-id multiplier hint the frontend
// produces from `@kind/slug:weight` tails. Keys are stringified ids —
// JSON objects can't use numeric keys — and values are clamped to
// [AssetWeightMin, AssetWeightMax] at parse time so the generator
// always sees a safe multiplier. Omitted entries default to 1.
type AssetWeightMap = map[string]float64

// AssetBinding links a piece of the canvas document to catalogue
// entities. Nil slices mean "no binding on this channel". Weight maps
// are optional — when absent, each referenced id keeps the default
// multiplier of 1 end-to-end.
type AssetBinding struct {
	Scope            AssetBindingScope `json:"scope"`
	CharacterIDs     []int             `json:"characterIds,omitempty"`
	BrandIDs         []int             `json:"brandIds,omitempty"`
	ProductIDs       []int             `json:"productIds,omitempty"`
	CharacterWeights AssetWeightMap    `json:"characterWeights,omitempty"`
	BrandWeights     AssetWeightMap    `json:"brandWeights,omitempty"`
	ProductWeights   AssetWeightMap    `json:"productWeights,omitempty"`
	// CanvasProjectID is the project the binding came from. Set by
	// the canvas API layer when forwarding a generation request
	// through the task queue. The injector reads it to scope the
	// continuity-tracked-references lookup (most recent successful
	// render per character in this project). Nil = "no project
	// context", which makes BuildContinuityIndex a no-op — same as
	// the rest of the field's guards.
	//
	// Lives on AssetBinding rather than TaskRequestData because the
	// continuity lookup is asset-bindings-specific; widening
	// TaskRequestData with a canvas-flavored field would tempt
	// non-canvas callers to populate it and add noise.
	CanvasProjectID *uint64 `json:"canvasProjectId,omitempty"`
}

// sanitizeAssetWeightMap drops entries whose key is not a base-10 integer
// or whose value is non-finite, and clamps surviving values into
// [AssetWeightMin, AssetWeightMax]. Returns nil when the cleaned map is
// empty so the JSON `omitempty` tag emits no wire bytes for it.
//
// Sanitization is deliberately structural — it never invents or removes
// id entries; it only cleans the multiplier values that the caller can
// forge. The matching id slice (e.g. CharacterIDs) is the authoritative
// list of bound entities and is left alone.
func sanitizeAssetWeightMap(weights AssetWeightMap) AssetWeightMap {
	if len(weights) == 0 {
		return nil
	}
	cleaned := make(AssetWeightMap, len(weights))
	for key, value := range weights {
		if _, err := strconv.Atoi(key); err != nil {
			// Non-integer keys cannot point at a valid id and would
			// be silently ignored by the injector anyway. Drop now so
			// the persisted TaskRequestData stays clean.
			continue
		}
		if math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		if value < AssetWeightMin {
			value = AssetWeightMin
		} else if value > AssetWeightMax {
			value = AssetWeightMax
		}
		cleaned[key] = value
	}
	if len(cleaned) == 0 {
		return nil
	}
	return cleaned
}

// Sanitize normalises the binding's weight maps in place. Called by
// the canvas API header parser right after JSON unmarshal so any
// client-forged out-of-range or non-finite weights are clamped before
// the binding flows into TaskRequestData persistence. The injector's
// own weightFor still clamps at consumption time as defense in depth
// — Sanitize prevents bad values from being stored in the first place.
func (b *AssetBinding) Sanitize() {
	if b == nil {
		return
	}
	b.CharacterWeights = sanitizeAssetWeightMap(b.CharacterWeights)
	b.BrandWeights = sanitizeAssetWeightMap(b.BrandWeights)
	b.ProductWeights = sanitizeAssetWeightMap(b.ProductWeights)
}
