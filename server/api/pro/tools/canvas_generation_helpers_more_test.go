package tools

import (
	"reflect"
	"testing"

	"server/model"
)

// canvas_generation_helpers_test.go covers the headline contracts:
// normalizeCanvasImageModel's blank → NANO_BANANA_2 default,
// dedupe + primary-first order for candidates, blocklist removal that
// preserves order, the 1-entry reference-image shape, and the
// source/operation/meta stamping with empty-meta omit. These fill the
// quieter edge branches a silent regression would slip past:
//
//   • normalizeCanvasModelCandidates emits primary FIRST even when the
//     primary also appears (in a different normalised form) LATER in the
//     candidates list. A refactor that appended primary at the end or
//     ran candidates first would flip the dispatch preference from
//     primary → provider-default. Pin the primary-first dedupe anchor.
//   • normalizeCanvasModelCandidates is order-stable under realistic
//     mixed-case input. A refactor to build via a keys-of-map that
//     randomised Go's map iteration order would silently change the
//     candidate ordering seen by authorizeCanvasModelCandidates.
//   • filterCanvasModelCandidates returns the ORIGINAL slice reference
//     (not a copy) on the fast path (empty blocklist / empty input).
//     Pin so a refactor that always made a copy would quietly cost
//     allocations on every hot-path request. (And callers must know
//     they share the slice — a defensive-copy refactor changes the
//     contract.)
//   • filterCanvasModelCandidates returns a NON-nil empty slice when
//     everything is blocklisted — downstream json.Marshal encodes nil
//     slices as `null` but empty slices as `[]`. A regression that
//     returned nil would silently flip the wire format.
//   • filterCanvasModelCandidates does NOT mutate its input slice when
//     the blocklist DOES trigger a filter. Pin so a refactor that did
//     an in-place shuffle (copy(candidates[j:], candidates[j+1:])) would
//     surface. The caller may still be holding the original pointer.
//   • buildCanvasReferenceImages produces a FRESH map per call. Pin so
//     a memoised refactor (caching by URL) that returned a shared map
//     would surface — the caller mutates the map downstream (stamping
//     strength/weight overrides), and sharing would cross-contaminate
//     parallel requests.
//   • buildCanvasReferenceImages passes an empty URL through verbatim
//     (not nil, not "missing", still produces one entry). Pin the no-
//     validation contract — callers are responsible for URL gating.
//   • setCanvasOperationMeta with an EMPTY but non-nil meta does NOT
//     clear a previously-stored canvasOperationMeta. Pin the "no-op
//     on empty, no clobber" contract so a refactor that always wrote
//     the meta field would surface — a helper that stomped prior meta
//     would break the outpaint → edit-text chain where intermediate
//     helpers accumulate meta.
//   • setCanvasOperationMeta DOES overwrite existing source and
//     canvasOperation fields. These are the CALLER's signal — every
//     generation path re-stamps them. Pin the overwrite so a refactor
//     to "only stamp if unset" (sounds harmless) would break multi-
//     operation chains where the last operation's source/operation
//     must win.
//   • setCanvasOperationMeta stores meta BY REFERENCE (not a deep copy).
//     Callers build meta progressively; a defensive-copy refactor would
//     silently drop late-arriving meta fields. Pin the reference
//     semantic so that regression surfaces for review.
//   • setCanvasOperationMeta with an empty operation string still
//     stamps source="canvas" and canvasOperation="" (the helper is not
//     a validator). Pin so a refactor that added an early-return on
//     blank operation would surface.

func TestNormalizeCanvasModelCandidates_PrimaryFirstEvenWhenRepeatedLater(t *testing.T) {
	// User's UI may echo the primary back into the candidates list in
	// a different case — the normalisation should fold them and the
	// primary slot stays at index 0. A refactor that appended primary
	// at the end would flip the dispatch preference.
	got := normalizeCanvasModelCandidates(
		model.NANO_BANANA_PRO,
		[]string{"other-model", "NANO-BANANA-PRO", model.NANO_BANANA_2},
	)
	want := []string{model.NANO_BANANA_PRO, "other-model", model.NANO_BANANA_2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v (primary must anchor index 0)", got, want)
	}
}

func TestNormalizeCanvasModelCandidates_OrderStableUnderMixedCase(t *testing.T) {
	// Verify that the output is ordered by first-occurrence across the
	// primary + candidates stream, not by some map-keys iteration that
	// Go randomises. This catches a "let me dedupe via a map then
	// re-emit keys" refactor.
	got := normalizeCanvasModelCandidates(
		"",
		[]string{"Model-A", "Model-B", "model-a", "Model-C", "MODEL-B"},
	)
	want := []string{"model-a", "model-b", "model-c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestFilterCanvasModelCandidates_FastPathReturnsSameSliceReference(t *testing.T) {
	// When either the input or blocklist is empty, the function returns
	// the input unchanged. Pin that it returns the SAME reference — a
	// refactor that always copied would cost allocations on every
	// hot-path request. (Equally: callers must not assume a copy.)
	in := []string{model.NANO_BANANA_2, "other"}
	got := filterCanvasModelCandidates(in, nil)
	if len(got) != len(in) {
		t.Fatalf("got %v, want %v", got, in)
	}
	// Same backing array check: mutate in[0], observe got[0].
	in[0] = "MUTATED"
	if got[0] != "MUTATED" {
		t.Errorf("got is a copy, not the same reference — fast path allocated unnecessarily")
	}
}

func TestFilterCanvasModelCandidates_AllBlockedReturnsNonNilEmptySlice(t *testing.T) {
	// json.Marshal encodes nil slices as `null` and empty slices as `[]`.
	// A refactor that returned nil when everything is filtered would
	// silently flip the wire format for the client parser.
	in := []string{model.NANO_BANANA_PRO, model.NANO_BANANA_PRO}
	got := filterCanvasModelCandidates(in, map[string]struct{}{
		model.NANO_BANANA_PRO: {},
	})
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
	if got == nil {
		t.Error("got nil, want non-nil empty slice — JSON wire format would drift from [] to null")
	}
}

func TestFilterCanvasModelCandidates_DoesNotMutateInputSlice(t *testing.T) {
	// On the filter path, the caller may still be holding the original
	// slice. A refactor to in-place copy(candidates[j:], candidates[j+1:])
	// would mangle what the caller sees.
	in := []string{"a", "blocked", "c", "blocked", "d"}
	snapshot := append([]string{}, in...)
	_ = filterCanvasModelCandidates(in, map[string]struct{}{"blocked": {}})
	if !reflect.DeepEqual(in, snapshot) {
		t.Errorf("input was mutated: got %v, want %v", in, snapshot)
	}
}

func TestBuildCanvasReferenceImages_FreshMapPerCall(t *testing.T) {
	// Callers stamp extra keys onto the returned map downstream (e.g.
	// strength/weight overrides). A cached/memoised version would let
	// those mutations leak across parallel requests.
	a := buildCanvasReferenceImages("url-1")
	b := buildCanvasReferenceImages("url-1")
	if &a[0] == &b[0] {
		t.Fatal("returned the same map pointer across calls — cross-request contamination risk")
	}
	a[0]["mutated"] = true
	if _, present := b[0]["mutated"]; present {
		t.Error("mutation on one call leaked into another — shared backing map")
	}
}

func TestBuildCanvasReferenceImages_EmptyURLPassesThroughVerbatim(t *testing.T) {
	// No validation — callers gate URLs upstream. Pin the no-validation
	// contract so a refactor that added a "default to placeholder"
	// branch wouldn't silently start injecting a canned URL.
	got := buildCanvasReferenceImages("")
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[0]["url"] != "" {
		t.Errorf("url = %#v, want empty string (no silent fallback)", got[0]["url"])
	}
}

func TestSetCanvasOperationMeta_EmptyMetaDoesNotClearPrior(t *testing.T) {
	// Multi-stage callers accumulate meta by re-invoking the helper.
	// If a later stage passes nil/empty meta, the prior stage's meta
	// must survive. Pin so a refactor that always wrote the meta key
	// (even when the argument is empty, clobbering with nil) would
	// surface.
	req := model.JSONMap{}
	setCanvasOperationMeta(req, "step-1", model.JSONMap{"strength": 0.75})
	if _, present := req["canvasOperationMeta"]; !present {
		t.Fatal("setup: first call should have stamped meta")
	}
	// Second call with nil meta — should NOT clear.
	setCanvasOperationMeta(req, "step-2", nil)
	got, present := req["canvasOperationMeta"].(model.JSONMap)
	if !present {
		t.Fatal("second call with nil meta cleared prior meta")
	}
	if got["strength"] != 0.75 {
		t.Errorf("prior meta content lost: got %+v, want strength=0.75", got)
	}
	// And a third call with an empty-but-non-nil map — also no clobber.
	setCanvasOperationMeta(req, "step-3", model.JSONMap{})
	got2, _ := req["canvasOperationMeta"].(model.JSONMap)
	if got2["strength"] != 0.75 {
		t.Errorf("empty-map call cleared prior meta: got %+v", got2)
	}
}

func TestSetCanvasOperationMeta_OverwritesExistingSourceAndOperation(t *testing.T) {
	// Every generation path re-stamps these — the last caller wins.
	// A refactor to "only stamp if unset" would break chained helpers
	// where operation identity must reflect the CURRENT stage.
	req := model.JSONMap{
		"source":          "legacy",
		"canvasOperation": "old-op",
	}
	setCanvasOperationMeta(req, "new-op", nil)
	if req["source"] != "canvas" {
		t.Errorf("source = %#v, want 'canvas' (must overwrite)", req["source"])
	}
	if req["canvasOperation"] != "new-op" {
		t.Errorf("canvasOperation = %#v, want 'new-op' (must overwrite)", req["canvasOperation"])
	}
}

func TestSetCanvasOperationMeta_StoresMetaByReferenceNotCopy(t *testing.T) {
	// Callers may keep building the meta after the helper returns.
	// Pin the reference semantic so a defensive-copy refactor would
	// silently drop late-arriving keys.
	req := model.JSONMap{}
	meta := model.JSONMap{"initial": "value"}
	setCanvasOperationMeta(req, "op", meta)

	meta["late"] = "arrived-after-set"

	stored, _ := req["canvasOperationMeta"].(model.JSONMap)
	if stored["late"] != "arrived-after-set" {
		t.Errorf("late-arrived key not visible through stored meta: %+v", stored)
	}
}

func TestSetCanvasOperationMeta_EmptyOperationStillStamps(t *testing.T) {
	// The helper is a writer, not a validator. Pin so a refactor that
	// added "if operation == '' return" would surface — callers rely
	// on source/canvasOperation being stamped unconditionally when
	// requestData is non-nil.
	req := model.JSONMap{}
	setCanvasOperationMeta(req, "", nil)
	if req["source"] != "canvas" {
		t.Errorf("source = %#v, want 'canvas' even with empty operation", req["source"])
	}
	if req["canvasOperation"] != "" {
		t.Errorf("canvasOperation = %#v, want '' (stamped empty, not skipped)", req["canvasOperation"])
	}
}
