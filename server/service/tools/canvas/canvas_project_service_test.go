package canvas

import (
	"errors"
	"reflect"
	"testing"

	"server/model"
)

// canvas_project_service_test.go covers the pure layer of the project
// service — title normalisation, initial-document shape, element
// counting, and patch application. The DB-coupled wrappers (Create /
// Update / Patch / Delete) are exercised by the handler-level tests
// that already run against an integration MySQL; the pure layer is
// where we pin the invariants that make those wrappers correct.

func TestNormalizeProjectTitle_EmptyDefaultsToUntitled(t *testing.T) {
	if got := NormalizeProjectTitle(""); got != "Untitled" {
		t.Errorf("NormalizeProjectTitle(\"\") = %q, want %q", got, "Untitled")
	}
}

func TestNormalizeProjectTitle_WhitespaceDefaultsToUntitled(t *testing.T) {
	// Handler stripped whitespace before falling through to the
	// "Untitled" default; preserve that behaviour so drag-and-drop
	// creations with a " " title don't leak empty rows.
	if got := NormalizeProjectTitle("   "); got != "Untitled" {
		t.Errorf("NormalizeProjectTitle(spaces) = %q, want Untitled", got)
	}
}

func TestNormalizeProjectTitle_KeepsValid(t *testing.T) {
	if got := NormalizeProjectTitle("  Storyboard  "); got != "Storyboard" {
		t.Errorf("NormalizeProjectTitle trim = %q, want Storyboard", got)
	}
}

func TestBuildInitialCanvasDocument_ShapeIsDeterministic(t *testing.T) {
	// The shape is part of the current document contract. Pin it so a
	// future cleanup can't silently reshape new projects.
	doc := BuildInitialCanvasDocument()
	if sv, _ := doc["schemaVersion"].(int); sv != SchemaVersionV2 {
		t.Errorf("schemaVersion = %v, want %d", doc["schemaVersion"], SchemaVersionV2)
	}
	elements, ok := doc["elements"].([]interface{})
	if !ok {
		t.Fatalf("elements is %T, want []interface{}", doc["elements"])
	}
	if len(elements) != 0 {
		t.Errorf("elements len = %d, want 0", len(elements))
	}
	viewport, ok := doc["viewport"].(map[string]interface{})
	if !ok {
		t.Fatalf("viewport is %T, want map[string]interface{}", doc["viewport"])
	}
	if viewport["scale"] != 1 {
		t.Errorf("viewport scale = %v, want 1", viewport["scale"])
	}
	if _, ok := doc["selectedIds"].([]string); !ok {
		t.Errorf("selectedIds is %T, want []string", doc["selectedIds"])
	}
}

func TestBuildInitialCanvasDocument_ReturnsFreshMapEachCall(t *testing.T) {
	// Callers mutate these into CanvasVersion rows; a shared map would
	// cause test-to-test bleed.
	a := BuildInitialCanvasDocument()
	b := BuildInitialCanvasDocument()
	a["elements"] = []interface{}{"x"}
	if got, _ := b["elements"].([]interface{}); len(got) != 0 {
		t.Errorf("mutating a leaked into b: %v", got)
	}
}

func TestElementCountFromDocument_MissingKey(t *testing.T) {
	if got := ElementCountFromDocument(model.JSONMap{}); got != 0 {
		t.Errorf("missing elements → %d, want 0", got)
	}
}

func TestElementCountFromDocument_InterfaceSlice(t *testing.T) {
	doc := model.JSONMap{"elements": []interface{}{1, 2, 3}}
	if got := ElementCountFromDocument(doc); got != 3 {
		t.Errorf("[]interface len = %d, want 3", got)
	}
}

func TestElementCountFromDocument_MapSlice(t *testing.T) {
	// GORM decodes JSON documents into []map[string]interface{} in some
	// paths. Both shapes must count the same way or the patch path
	// will write element_count=0 and a later LayerPanel render will
	// show a blank list even though the data is fine.
	doc := model.JSONMap{"elements": []map[string]interface{}{{"id": "a"}, {"id": "b"}}}
	if got := ElementCountFromDocument(doc); got != 2 {
		t.Errorf("[]map len = %d, want 2", got)
	}
}

func TestElementCountFromDocument_BadType(t *testing.T) {
	// A string in the elements slot should count as 0, not panic — a
	// malformed upload must not take the handler down.
	doc := model.JSONMap{"elements": "nope"}
	if got := ElementCountFromDocument(doc); got != 0 {
		t.Errorf("bad type → %d, want 0", got)
	}
}

func TestApplyElementPatches_UpdatesMatchingID(t *testing.T) {
	doc := model.JSONMap{
		"elements": []interface{}{
			map[string]interface{}{"id": "e1", "x": 10.0},
			map[string]interface{}{"id": "e2", "x": 20.0},
		},
	}
	patches := []map[string]interface{}{{"id": "e1", "x": 99.0}}
	next, changed, err := ApplyElementPatches(doc, patches)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}
	elems := next["elements"].([]interface{})
	if got := elems[0].(map[string]interface{})["x"]; got != 99.0 {
		t.Errorf("e1.x = %v, want 99.0", got)
	}
	if got := elems[1].(map[string]interface{})["x"]; got != 20.0 {
		t.Errorf("e2.x = %v, want 20.0 (untouched)", got)
	}
}

func TestApplyElementPatches_IgnoresIDMutation(t *testing.T) {
	// Element IDs are the join key for everything downstream (bindings,
	// audit, shot-link). Letting a patch rewrite them would silently
	// corrupt the whole graph.
	doc := model.JSONMap{
		"elements": []interface{}{map[string]interface{}{"id": "e1", "x": 10.0}},
	}
	patches := []map[string]interface{}{{"id": "e1", "x": 99.0, "idAttempt": "rename"}}
	// The ApplyElementPatches contract only reads `id` for matching;
	// any other key — including a stray "id" inside a different slot —
	// must pass through as a regular field mutation.
	next, changed, err := ApplyElementPatches(doc, patches)
	if err != nil || !changed {
		t.Fatalf("unexpected err=%v changed=%v", err, changed)
	}
	el := next["elements"].([]interface{})[0].(map[string]interface{})
	if el["id"] != "e1" {
		t.Errorf("id mutated to %v, want e1", el["id"])
	}
	if el["idAttempt"] != "rename" {
		t.Errorf("expected idAttempt=rename to land as a normal field, got %v", el["idAttempt"])
	}
}

func TestApplyElementPatches_NoChangeIfValueEqual(t *testing.T) {
	// DeepEqual-guarded short-circuit — otherwise every idle autosave
	// would produce a new CanvasVersion row.
	doc := model.JSONMap{
		"elements": []interface{}{map[string]interface{}{"id": "e1", "x": 10.0}},
	}
	patches := []map[string]interface{}{{"id": "e1", "x": 10.0}}
	got, changed, err := ApplyElementPatches(doc, patches)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if changed {
		t.Error("expected changed=false for no-op patch")
	}
	if !reflect.DeepEqual(got, doc) {
		t.Error("doc mutated even though changed=false")
	}
}

func TestApplyElementPatches_UnmatchedPatchLeavesDocUntouched(t *testing.T) {
	doc := model.JSONMap{
		"elements": []interface{}{map[string]interface{}{"id": "e1", "x": 10.0}},
	}
	patches := []map[string]interface{}{{"id": "ghost", "x": 99.0}}
	got, changed, err := ApplyElementPatches(doc, patches)
	if err != nil || changed {
		t.Fatalf("unexpected err=%v changed=%v", err, changed)
	}
	if !reflect.DeepEqual(got, doc) {
		t.Error("doc mutated despite no matching id")
	}
}

func TestApplyElementPatches_EmptyPatchesAreNoOp(t *testing.T) {
	doc := model.JSONMap{"elements": []interface{}{}}
	got, changed, err := ApplyElementPatches(doc, nil)
	if err != nil || changed {
		t.Fatalf("unexpected err=%v changed=%v", err, changed)
	}
	if !reflect.DeepEqual(got, doc) {
		t.Error("doc mutated for empty patch list")
	}
}

func TestApplyElementPatches_PatchWithoutIDIsSkipped(t *testing.T) {
	// Patch entries without an `id` are dropped — otherwise we'd have
	// no way to know which element they target.
	doc := model.JSONMap{
		"elements": []interface{}{map[string]interface{}{"id": "e1", "x": 10.0}},
	}
	patches := []map[string]interface{}{{"x": 99.0}}
	got, changed, err := ApplyElementPatches(doc, patches)
	if err != nil || changed {
		t.Fatalf("unexpected err=%v changed=%v", err, changed)
	}
	if !reflect.DeepEqual(got, doc) {
		t.Error("doc mutated despite patch missing id")
	}
}

func TestApplyElementPatches_ReturnsErrWhenElementsMissing(t *testing.T) {
	_, _, err := ApplyElementPatches(model.JSONMap{}, []map[string]interface{}{{"id": "e1"}})
	if !errors.Is(err, ErrCanvasElementsDocument) {
		t.Errorf("err = %v, want ErrCanvasElementsDocument", err)
	}
}

func TestApplyElementPatches_ReturnsErrWhenElementsMalformed(t *testing.T) {
	doc := model.JSONMap{"elements": 42}
	_, _, err := ApplyElementPatches(doc, []map[string]interface{}{{"id": "e1"}})
	if !errors.Is(err, ErrCanvasElementsDocument) {
		t.Errorf("err = %v, want ErrCanvasElementsDocument", err)
	}
}

func TestApplyElementPatches_MapSliceInputIsAccepted(t *testing.T) {
	// Parity with ElementCountFromDocument — GORM may hand us elements
	// as []map[string]interface{} after a JSON round-trip, and the
	// patch path has to cope without a panic.
	doc := model.JSONMap{
		"elements": []map[string]interface{}{{"id": "e1", "x": 1.0}},
	}
	patches := []map[string]interface{}{{"id": "e1", "x": 2.0}}
	next, changed, err := ApplyElementPatches(doc, patches)
	if err != nil || !changed {
		t.Fatalf("unexpected err=%v changed=%v", err, changed)
	}
	el := next["elements"].([]interface{})[0].(map[string]interface{})
	if el["x"] != 2.0 {
		t.Errorf("x = %v, want 2.0", el["x"])
	}
}

func TestApplyElementPatches_NonMapElementsArePreserved(t *testing.T) {
	// The handler's original implementation silently skipped elements
	// that weren't maps (e.g. sparse array holes). Pin that behaviour
	// so a corrupt row doesn't lose unrelated elements.
	doc := model.JSONMap{
		"elements": []interface{}{
			"nope", // not a map
			map[string]interface{}{"id": "e1", "x": 10.0},
		},
	}
	patches := []map[string]interface{}{{"id": "e1", "x": 11.0}}
	next, changed, err := ApplyElementPatches(doc, patches)
	if err != nil || !changed {
		t.Fatalf("unexpected err=%v changed=%v", err, changed)
	}
	elems := next["elements"].([]interface{})
	if elems[0] != "nope" {
		t.Errorf("non-map element mutated: %v", elems[0])
	}
}
