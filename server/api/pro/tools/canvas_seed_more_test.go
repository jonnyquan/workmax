package tools

import (
	"math"
	"testing"

	"server/model"
)

// canvas_seed_test.go covers the headline branches: nil skips,
// negative drops, zero preserved, positive written, existing key
// overwritten on write. These fill quieter branches that a silent
// regression would slip past:
//
//   • Negative seed must NOT clobber a previously-stored seed key.
//     The function returns early on negative, so any earlier write
//     must survive. A refactor to "negative means clear it" would
//     silently drop deterministic seeds some upstream helper set.
//   • Nil seed must NOT clobber a previously-stored seed key. Same
//     argument — nil is a "caller didn't touch this field" signal,
//     not a "clear it" signal. Pin the no-op semantic.
//   • math.MaxInt64 passes through. Boundary pin — a refactor that
//     added an upper-bound guard (e.g. "seed must fit in uint32")
//     would surface here. Providers accept large seeds, the helper
//     shouldn't gatekeep.
//   • Unrelated keys in requestData are untouched. Pin that the
//     helper only writes the "seed" key — a refactor that switched
//     to requestData = map[string]any{"seed": *seed} (re-creating
//     the map) would silently wipe "strength", "prompt", etc.
//   • Stored value type is exactly int64 (not int, not float64).
//     Downstream JSON serialisation is type-sensitive; a refactor
//     that stored `int(*seed)` or `float64(*seed)` would silently
//     change the wire format.
//   • Consecutive applies: the second call's value wins, and the
//     call can also "no-op" (with nil) without touching the first
//     call's write. Pin the compose-with-itself invariant.

func TestApplyOptionalCanvasSeed_NegativeDoesNotClobberExistingKey(t *testing.T) {
	// An upstream helper may have already stashed a deterministic
	// seed. The negative-seed case returns early, so the prior write
	// must survive. A refactor to "clear on negative" would silently
	// flip retries from deterministic to provider-chosen.
	req := model.JSONMap{"seed": int64(42)}
	applyOptionalCanvasSeed(req, ptrInt64(-5))
	if req["seed"] != int64(42) {
		t.Errorf("negative seed cleared a prior seed key: got %v, want 42", req["seed"])
	}
}

func TestApplyOptionalCanvasSeed_NilDoesNotClobberExistingKey(t *testing.T) {
	// Same reasoning: nil signals "caller didn't touch this" not
	// "clear it".
	req := model.JSONMap{"seed": int64(42)}
	applyOptionalCanvasSeed(req, nil)
	if req["seed"] != int64(42) {
		t.Errorf("nil seed cleared a prior seed key: got %v, want 42", req["seed"])
	}
}

func TestApplyOptionalCanvasSeed_MaxInt64PassesThrough(t *testing.T) {
	// Boundary pin: a refactor that added an upper-bound guard
	// (e.g. "fits in uint32" which is common when a provider
	// constrains the range) would surface. The helper itself must
	// stay permissive; any range enforcement belongs at the provider
	// boundary.
	req := model.JSONMap{}
	applyOptionalCanvasSeed(req, ptrInt64(math.MaxInt64))
	if req["seed"] != int64(math.MaxInt64) {
		t.Errorf("MaxInt64 did not pass through: got %T %v", req["seed"], req["seed"])
	}
}

func TestApplyOptionalCanvasSeed_LeavesUnrelatedKeysUntouched(t *testing.T) {
	// The helper must write ONLY the "seed" key — a refactor that
	// switched to reassignment (requestData = map[string]any{...})
	// would silently wipe every other key the caller had set.
	req := model.JSONMap{
		"strength": 0.75,
		"prompt":   "a cat on a mat",
		"model":    "nano-banana",
	}
	applyOptionalCanvasSeed(req, ptrInt64(7))

	if req["seed"] != int64(7) {
		t.Errorf("seed not written: got %v", req["seed"])
	}
	if req["strength"] != 0.75 {
		t.Errorf("strength mutated: got %v", req["strength"])
	}
	if req["prompt"] != "a cat on a mat" {
		t.Errorf("prompt mutated: got %v", req["prompt"])
	}
	if req["model"] != "nano-banana" {
		t.Errorf("model mutated: got %v", req["model"])
	}
}

func TestApplyOptionalCanvasSeed_StoredValueIsInt64NotIntNotFloat(t *testing.T) {
	// Downstream JSON serialisation is type-sensitive — int64 vs
	// int vs float64 produce different wire formats for some
	// providers (e.g. integers coerced to floats in the JSON can
	// trigger "expected integer" errors on strict parsers). Pin
	// the exact stored type.
	req := model.JSONMap{}
	applyOptionalCanvasSeed(req, ptrInt64(100))

	got := req["seed"]
	if _, isInt64 := got.(int64); !isInt64 {
		t.Fatalf("seed stored with wrong type: got %T, want int64", got)
	}
}

func TestApplyOptionalCanvasSeed_ComposesWithItself(t *testing.T) {
	// Scenario: the same request is touched by two helpers — first
	// with a positive seed, then with nil (because the second helper
	// didn't have an opinion). The first write must survive the
	// second call.
	req := model.JSONMap{}
	applyOptionalCanvasSeed(req, ptrInt64(77))
	applyOptionalCanvasSeed(req, nil)
	if req["seed"] != int64(77) {
		t.Errorf("nil follow-up clobbered first write: got %v, want 77", req["seed"])
	}

	// And: second positive call overwrites the first.
	applyOptionalCanvasSeed(req, ptrInt64(99))
	if req["seed"] != int64(99) {
		t.Errorf("second positive did not overwrite: got %v, want 99", req["seed"])
	}

	// And: negative after a positive leaves the positive in place.
	applyOptionalCanvasSeed(req, ptrInt64(-1))
	if req["seed"] != int64(99) {
		t.Errorf("negative follow-up clobbered prior positive: got %v, want 99", req["seed"])
	}
}

func TestApplyOptionalCanvasSeed_ZeroAfterPositiveOverwrites(t *testing.T) {
	// Zero is a VALID deterministic seed (per the file's own
	// comment: mirrors the frontend's seed=0 retry-preservation).
	// So a caller passing seed=0 after an earlier positive MUST
	// overwrite with 0, not be treated like negative/nil.
	req := model.JSONMap{}
	applyOptionalCanvasSeed(req, ptrInt64(42))
	applyOptionalCanvasSeed(req, ptrInt64(0))
	if req["seed"] != int64(0) {
		t.Errorf("seed=0 failed to overwrite prior 42: got %v", req["seed"])
	}
}
