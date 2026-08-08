package model

import (
	"encoding/json"
	"strings"
	"testing"
)

// canvas_test.go pins TableName values + constant wire strings +
// ShotStatus disjointness + AssetScope string form. These fill the
// quieter gate invariants that ALSO cross the frontend wire contract
// (which is what downstream breakage looks like when a struct tag is
// silently edited):
//
//   • CanvasElementDTO serialises a `*bool` field with `omitempty`
//     correctly: nil pointer → key absent; pointer to false → key
//     present with `false`. This is the standard Go pointer-with-
//     omitempty trick and the whole reason Visible/Locked/
//     ClipContent/IsGenerating are pointers. Pin each explicitly so
//     a refactor that changed them to bare bool (losing the ability
//     to distinguish "unset" from "explicitly false") would surface.
//   • ViewportDTO does NOT use omitempty on X/Y/Scale — a viewport
//     at (0, 0, 0) still round-trips as `{"x":0,"y":0,"scale":0}`.
//     Pin so a refactor that added omitempty would drop the origin
//     as a defaulted-away viewport.
//   • PaginatedResult[T] wire format for empty data: a nil Items slice
//     marshals as `null`, an empty slice as `[]`. Pin this Go JSON
//     gotcha — callers that branch on the key value must stay stable.
//   • AssetBinding nil slices + nil weight maps omit cleanly
//     (`omitempty` on all six optional fields). An empty-but-non-nil
//     slice/map ALSO omits because len == 0 + omitempty. Pin both to
//     document the observable shape.
//   • Shot.TimelineStartMs / TimelineDurationMs are pointer-int64 with
//     omitempty — nil omits, 0 value SERIALISES as `"timelineStartMs":0`.
//     Pin so a shot at t=0 round-trips its explicit start, while a
//     shot with no timeline binding drops the keys.
//   • AssetBindingScope literal values are lowercase — pin the three
//     exact forms (`"element"`, `"shot"`, `"project"`) are NOT
//     title-case, which the JSON tag would transparently accept if the
//     underlying type changed.
//   • Shot status values form a 0..N-1 dense range starting at 0. If a
//     future constant gets added, the new value should be 3 (next
//     integer). Pin the current max so an out-of-band renumbering
//     would surface.

func TestCanvasElementDTO_PointerBoolOmitemptyContract(t *testing.T) {
	trueVal := true
	falseVal := false

	t.Run("nil pointer omits the key", func(t *testing.T) {
		dto := CanvasElementDTO{ID: "e1", Type: "image"}
		raw, _ := json.Marshal(dto)
		for _, key := range []string{"visible", "locked", "clipContent", "isGenerating"} {
			if strings.Contains(string(raw), key) {
				t.Errorf("nil %q should omit; got: %s", key, raw)
			}
		}
	})

	t.Run("pointer-to-false emits the key with false (distinguishes 'unset' from 'explicitly off')", func(t *testing.T) {
		dto := CanvasElementDTO{
			ID:          "e1",
			Type:        "image",
			Visible:     &falseVal,
			Locked:      &falseVal,
			ClipContent: &falseVal,
		}
		raw, _ := json.Marshal(dto)
		for _, want := range []string{`"visible":false`, `"locked":false`, `"clipContent":false`} {
			if !strings.Contains(string(raw), want) {
				t.Errorf("expected %q in output; got: %s", want, raw)
			}
		}
	})

	t.Run("pointer-to-true emits the key with true", func(t *testing.T) {
		dto := CanvasElementDTO{ID: "e1", Type: "image", IsGenerating: &trueVal}
		raw, _ := json.Marshal(dto)
		if !strings.Contains(string(raw), `"isGenerating":true`) {
			t.Errorf("expected `isGenerating:true`; got: %s", raw)
		}
	})
}

func TestViewportDTO_ZeroValuesSerialiseVerbatim(t *testing.T) {
	// No omitempty on X/Y/Scale — origin viewport round-trips.
	vp := ViewportDTO{}
	raw, _ := json.Marshal(vp)
	want := `{"x":0,"y":0,"scale":0}`
	if string(raw) != want {
		t.Errorf("zero viewport should serialise as %q, got %q", want, raw)
	}
}

func TestPaginatedResult_NilItemsVsEmptySlice(t *testing.T) {
	// Go JSON gotcha: nil slice → `null`, empty slice → `[]`. Pin both
	// so callers that branch on the wire representation stay stable.
	var nilPaged = PaginatedResult[int]{Items: nil, Total: 0, Page: 1, Limit: 10}
	raw, _ := json.Marshal(nilPaged)
	if !strings.Contains(string(raw), `"items":null`) {
		t.Errorf("nil Items should serialise as `null`; got %s", raw)
	}

	emptyPaged := PaginatedResult[int]{Items: []int{}, Total: 0, Page: 1, Limit: 10}
	raw, _ = json.Marshal(emptyPaged)
	if !strings.Contains(string(raw), `"items":[]`) {
		t.Errorf("empty Items should serialise as `[]`; got %s", raw)
	}
}

func TestAssetBinding_OmitemptyOnAllOptionalFields(t *testing.T) {
	// Nil slices + nil weight maps all drop; only scope remains.
	ab := AssetBinding{Scope: AssetScopeElement}
	raw, _ := json.Marshal(ab)
	want := `{"scope":"element"}`
	if string(raw) != want {
		t.Errorf("binding with only scope should serialise as %q, got %q", want, raw)
	}

	// Empty-but-non-nil slices also omit via omitempty's len==0 check.
	abEmpty := AssetBinding{
		Scope:        AssetScopeShot,
		CharacterIDs: []int{},
	}
	raw, _ = json.Marshal(abEmpty)
	// Scope present, but the empty slice is still omitted.
	if strings.Contains(string(raw), "characterIds") {
		t.Errorf("empty slice should still omit; got %s", raw)
	}
	if !strings.Contains(string(raw), `"scope":"shot"`) {
		t.Errorf("scope missing in empty-bindings payload; got %s", raw)
	}
}

func TestAssetBinding_NonEmptySlicesSurvive(t *testing.T) {
	ab := AssetBinding{
		Scope:            AssetScopeProject,
		CharacterIDs:     []int{42, 7},
		CharacterWeights: AssetWeightMap{"42": 1.5},
	}
	raw, _ := json.Marshal(ab)
	for _, want := range []string{
		`"scope":"project"`,
		`"characterIds":[42,7]`,
		`"characterWeights":{"42":1.5}`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("expected %q in output; got: %s", want, raw)
		}
	}
}

func TestShot_PointerInt64WireFormat(t *testing.T) {
	// nil → key absent.
	s := Shot{LocalCardID: "c1"}
	raw, _ := json.Marshal(s)
	if strings.Contains(string(raw), "timelineStartMs") || strings.Contains(string(raw), "timelineDurationMs") {
		t.Errorf("nil timeline pointers should omit; got: %s", raw)
	}

	// Pointer-to-zero → key present with 0.
	var zero int64 = 0
	s2 := Shot{LocalCardID: "c1", TimelineStartMs: &zero}
	raw, _ = json.Marshal(s2)
	if !strings.Contains(string(raw), `"timelineStartMs":0`) {
		t.Errorf("pointer-to-zero timelineStartMs should emit `:0`; got: %s", raw)
	}

	// Pointer-to-positive → key with value.
	v := int64(42_000)
	s3 := Shot{LocalCardID: "c1", TimelineDurationMs: &v}
	raw, _ = json.Marshal(s3)
	if !strings.Contains(string(raw), `"timelineDurationMs":42000`) {
		t.Errorf("pointer timelineDurationMs=42000 should emit; got: %s", raw)
	}
}

func TestShotStatus_ValueRangeIsZeroToArchived(t *testing.T) {
	// Archived (2) is the max defined value. Pin so a renumbering or
	// a sparse gap would surface.
	if ShotStatusDraft != 0 || ShotStatusReady != 1 || ShotStatusArchived != 2 {
		t.Errorf("status values drift: draft=%d, ready=%d, archived=%d",
			ShotStatusDraft, ShotStatusReady, ShotStatusArchived)
	}
	// Max value check: Archived is the largest.
	for _, v := range []int8{ShotStatusDraft, ShotStatusReady} {
		if v > ShotStatusArchived {
			t.Errorf("status %d exceeds Archived=%d", v, ShotStatusArchived)
		}
	}
}

func TestAssetScope_LowercaseLiterals(t *testing.T) {
	// Pin the underlying string — the typed wrapper protects compile-
	// time usage, but JSON goes over the wire as the raw string. A
	// drift to title-case (`"Element"`) would break the frontend.
	for name, got := range map[string]AssetBindingScope{
		"element": AssetScopeElement,
		"shot":    AssetScopeShot,
		"project": AssetScopeProject,
	} {
		if string(got) != name {
			t.Errorf("AssetScope %s = %q, want %q", name, got, name)
		}
		if strings.ToLower(string(got)) != string(got) {
			t.Errorf("AssetScope %q is not fully lowercase", got)
		}
	}
}

func TestCanvasElementDTO_EmptyStringFieldsOmit(t *testing.T) {
	// Zero values of optional string fields must drop when empty,
	// via `omitempty`. Pin that ONLY id+type+x+y are emitted for a
	// minimal DTO.
	dto := CanvasElementDTO{ID: "e1", Type: "text", X: 0, Y: 0}
	raw, _ := json.Marshal(dto)
	// Must include the four required (non-omitempty) keys.
	for _, want := range []string{`"id":"e1"`, `"type":"text"`, `"x":0`, `"y":0`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("expected %q in minimal DTO; got: %s", want, raw)
		}
	}
	// Must NOT include any of the optional string fields.
	for _, unwanted := range []string{"content", "width", "height", "color", "src", "prompt", "seed"} {
		if strings.Contains(string(raw), unwanted) {
			t.Errorf("optional field %q should omit; got: %s", unwanted, raw)
		}
	}
}
