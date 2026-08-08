package canvas

import (
	"reflect"
	"testing"

	"server/model"
)

// canvas_project_service_test.go covers the headline branches of the
// pure layer. These fill in the boundary + drift-guard branches that
// a silent regression could slip past the existing suite:
//
//   • BuildInitialCanvasDocument viewport origin — existing test only
//     pins `scale == 1`. The x/y fields default to 0 and a regression
//     that swapped in a centred default (e.g. canvas-width/2) would
//     reset every new project's pan position. Pin x=0, y=0.
//   • ElementCountFromDocument with an explicitly nil `elements` value
//     vs a missing key — different code paths. Existing suite pins
//     missing-key and bad-type; a nil value takes the
//     `raw, ok := doc["elements"]` true branch but then fails every
//     type assertion. Pin the 0 return so a refactor that dropped the
//     final default-zero path wouldn't surface only on hostile input.
//   • ElementCountFromDocument with an EMPTY []interface{} slice —
//     existing tests pin len=3 but not len=0, so a regression that
//     returned `len(arr)+1` or pre-seeded an element would not
//     surface unless both branches were hit.
//   • ApplyElementPatches with patch `id` set to the empty string —
//     symmetric to the "missing id" guard. The source uses
//     `idStr == ""` after the type assert, so a patch like
//     `{"id": "", "x": 99}` must be skipped. A regression that
//     tightened to `idStr == "" && hasIDKey == false` would let empty
//     strings through and attempt to match the first empty-string
//     element (corrupting legacy data).
//   • ApplyElementPatches with multiple patches targeting the same
//     element in a single call — later patch overwrites earlier (map
//     key collision is expected behaviour). Pin so a refactor to
//     `append`-based collection would change semantics silently.
//   • ApplyElementPatches updating MULTIPLE distinct elements in a
//     single call — existing tests pin a single element; a regression
//     that short-circuited after the first match would pass every
//     single-element test but silently drop batched patches.
//   • ApplyElementPatches must NOT mutate the INPUT document's
//     elements slice/maps. Existing NoOp tests show the no-op path
//     returns the input reference, but none of the change-path tests
//     prove the original input map is untouched. A regression that
//     wrote to `rawElements` in-place (skipping the copy-slice dance)
//     would poison the previous version in callers that kept a
//     reference to it.

func TestBuildInitialCanvasDocument_ViewportOriginIsZeroZero(t *testing.T) {
	// Pinning x=y=0 guards against a refactor that "helpfully" centred
	// new projects by shifting the origin — every downstream import
	// assumes new projects open at world-origin.
	doc := BuildInitialCanvasDocument()
	viewport, ok := doc["viewport"].(map[string]interface{})
	if !ok {
		t.Fatalf("viewport is %T, want map[string]interface{}", doc["viewport"])
	}
	if viewport["x"] != 0 {
		t.Errorf("viewport.x = %v, want 0", viewport["x"])
	}
	if viewport["y"] != 0 {
		t.Errorf("viewport.y = %v, want 0", viewport["y"])
	}
}

func TestElementCountFromDocument_NilElementsValueReturnsZero(t *testing.T) {
	// Explicit nil takes the "key present" branch but fails every type
	// assertion — must land on the final `return 0`. A regression that
	// panicked on nil would only show up against hostile input.
	doc := model.JSONMap{"elements": nil}
	if got := ElementCountFromDocument(doc); got != 0 {
		t.Errorf("nil elements → %d, want 0", got)
	}
}

func TestElementCountFromDocument_EmptyInterfaceSliceReturnsZero(t *testing.T) {
	// Existing suite only tests len>0 for []interface{}. Pin the
	// zero-length path so a regression that returned `len(arr) + 1`
	// (or pre-seeded an element) would surface here as well.
	doc := model.JSONMap{"elements": []interface{}{}}
	if got := ElementCountFromDocument(doc); got != 0 {
		t.Errorf("empty []interface{} → %d, want 0", got)
	}
}

func TestElementCountFromDocument_EmptyMapSliceReturnsZero(t *testing.T) {
	// Same argument on the []map[string]interface{} path — the GORM
	// JSON round-trip can produce a non-nil, empty slice.
	doc := model.JSONMap{"elements": []map[string]interface{}{}}
	if got := ElementCountFromDocument(doc); got != 0 {
		t.Errorf("empty []map → %d, want 0", got)
	}
}

func TestApplyElementPatches_EmptyStringIDIsSkipped(t *testing.T) {
	// Symmetric to PatchWithoutIDIsSkipped — the source normalises both
	// missing-key and empty-string `id` to "skip this patch". A patch
	// like {"id": "", "x": 99} must be a no-op.
	doc := model.JSONMap{
		"elements": []interface{}{
			map[string]interface{}{"id": "e1", "x": 10.0},
		},
	}
	patches := []map[string]interface{}{{"id": "", "x": 99.0}}
	got, changed, err := ApplyElementPatches(doc, patches)
	if err != nil || changed {
		t.Fatalf("unexpected err=%v changed=%v", err, changed)
	}
	if !reflect.DeepEqual(got, doc) {
		t.Errorf("doc mutated despite empty-string id patch: %+v", got)
	}
}

func TestApplyElementPatches_DuplicatePatchIDsKeepLastWriteWins(t *testing.T) {
	// `updatesByID[idStr] = patch` means a later patch with the same
	// id overwrites the earlier one. Pin this last-write-wins contract
	// so a refactor to an append-based slice would surface as a changed
	// behaviour in this test.
	doc := model.JSONMap{
		"elements": []interface{}{
			map[string]interface{}{"id": "e1", "x": 10.0},
		},
	}
	patches := []map[string]interface{}{
		{"id": "e1", "x": 11.0},
		{"id": "e1", "x": 99.0},
	}
	next, changed, err := ApplyElementPatches(doc, patches)
	if err != nil || !changed {
		t.Fatalf("unexpected err=%v changed=%v", err, changed)
	}
	got := next["elements"].([]interface{})[0].(map[string]interface{})["x"]
	if got != 99.0 {
		t.Errorf("x = %v, want 99.0 (last patch wins)", got)
	}
}

func TestApplyElementPatches_BatchUpdatesMultipleElements(t *testing.T) {
	// Existing tests only patch a single element. Pin that batched
	// patches across distinct ids all land — a regression that broke
	// out of the loop after the first match would silently discard
	// every patch after the first.
	doc := model.JSONMap{
		"elements": []interface{}{
			map[string]interface{}{"id": "e1", "x": 10.0},
			map[string]interface{}{"id": "e2", "x": 20.0},
			map[string]interface{}{"id": "e3", "x": 30.0},
		},
	}
	patches := []map[string]interface{}{
		{"id": "e1", "x": 11.0},
		{"id": "e3", "x": 33.0},
	}
	next, changed, err := ApplyElementPatches(doc, patches)
	if err != nil || !changed {
		t.Fatalf("unexpected err=%v changed=%v", err, changed)
	}
	elems := next["elements"].([]interface{})
	if got := elems[0].(map[string]interface{})["x"]; got != 11.0 {
		t.Errorf("e1.x = %v, want 11.0", got)
	}
	if got := elems[1].(map[string]interface{})["x"]; got != 20.0 {
		t.Errorf("e2.x = %v, want 20.0 (unpatched)", got)
	}
	if got := elems[2].(map[string]interface{})["x"]; got != 33.0 {
		t.Errorf("e3.x = %v, want 33.0", got)
	}
}

func TestApplyElementPatches_DoesNotMutateInputElementMaps(t *testing.T) {
	// The copy-slice + per-element map-copy dance protects the caller's
	// reference to the previous version. A regression that wrote
	// `elMap[k] = v` in-place would corrupt the previous CanvasVersion
	// document that the handler still holds a pointer to. Pin: after
	// a successful patch, the INPUT map is byte-for-byte unchanged.
	originalEl := map[string]interface{}{"id": "e1", "x": 10.0}
	doc := model.JSONMap{"elements": []interface{}{originalEl}}
	patches := []map[string]interface{}{{"id": "e1", "x": 99.0}}

	_, changed, err := ApplyElementPatches(doc, patches)
	if err != nil || !changed {
		t.Fatalf("unexpected err=%v changed=%v", err, changed)
	}
	// Original element map must still read x=10.0 — the new doc got a
	// *copied* map with x=99.0. A regression that mutated in-place
	// would flip this to 99.0.
	if originalEl["x"] != 10.0 {
		t.Errorf("input element mutated in-place: x=%v, want 10.0", originalEl["x"])
	}
	// The input doc's elements slice is untouched too — the new doc
	// points at a fresh slice.
	inputElems := doc["elements"].([]interface{})
	if inputElems[0].(map[string]interface{})["x"] != 10.0 {
		t.Errorf("input doc's element 0 was mutated: %+v", inputElems[0])
	}
}
