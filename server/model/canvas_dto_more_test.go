package model

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// canvas_dto_test.go covers the headline contract (CanvasElementDTO
// omitempty absence, pointer-bool Visible/Locked false emission,
// ViewportDTO always-emits x/y/scale, AssetBindingScope unmarshals
// valid strings, AssetBinding {scope} bare form, PaginatedResult
// shape, round-trip of some CanvasElementDTO fields). These fill the
// quieter wire-format gate invariants a silent regression would slip
// past:
//
//   • CanvasElementDTO.IsGenerating *bool emission when set to false —
//     base covers Visible/Locked pointer-false but not the two AI-flow
//     pointer bools (IsGenerating / ClipContent). Pin so a refactor to
//     plain bool would silently collapse "not set" and "explicitly
//     false" into one for the frontend's generating-spinner logic.
//   • CanvasElementDTO.ClipContent *bool emission (mirror case).
//   • CanvasElementDTO numeric zero omits — Width/Height/Rotation/
//     StrokeWidth/FontSize all have omitempty. A refactor that removed
//     omitempty would silently start emitting `"width":0` for text
//     elements that have no width.
//   • CanvasElementDTO.X/Y NEVER omit (no omitempty) — a text element
//     positioned at (0,0) must still emit x:0, y:0 so the frontend can
//     distinguish "at origin" from "undefined".
//   • CanvasProject.UUID always emits (no omitempty) — if empty it
//     serialises as `"uuid":""`. Pin so a refactor adding omitempty
//     would break API clients doing `if (!payload.uuid)` which would
//     suddenly see undefined instead of "".
//   • GlobalAsset.SizeBytes int64: large values (> 2^53) round-trip
//     — encoding/json treats int64 as raw number so 9_000_000_000
//     bytes (9 GB) survives marshal/unmarshal precisely. Pin the
//     int64-not-float64 serialisation contract.
//   • ViewportDTO zero-value: emits `"x":0,"y":0,"scale":0` in field
//     declaration order. Pin the per-field presence (base pins keys
//     exist but not zero-value emission specifically).
//   • AssetBindingScope unknown-string passthrough: `"flibble"` round-
//     trips via json.Unmarshal into AssetBindingScope("flibble") — no
//     validation. Pin so a refactor that added custom UnmarshalJSON
//     with rejection would surface (any API client sending a typo
//     currently silently works).
//   • AssetBinding with ONLY CharacterIDs: weights omitted via
//     omitempty. Pin partial-binding shape.
//   • AssetBinding with empty-but-non-nil slice [] still OMITS (Go's
//     encoding/json omitempty treats len(slice)==0 as omit, even for
//     non-nil []int{}). Pin this counter-intuitive behaviour — a
//     caller who built `[]int{}` to signal "empty binding" would be
//     silently indistinguishable from "no binding" on the wire.
//   • PaginatedResult with nil Items slice: emits `"items":null` (not
//     "items":[]). Pin the nil-vs-empty distinction — a list endpoint
//     returning an empty slice vs nil slice has visibly different
//     wire output.
//   • PaginatedResult with empty []T{} slice: emits `"items":[]`. Pin
//     the empty-slice path.
//   • PaginatedResult[string]: generic type erased in JSON. Pin that
//     the type parameter doesn't leak into the output.
//   • CanvasTaskBinding JSON shape: uid / projectId / taskId /
//     elementId — the recovery endpoint reads these verbatim to
//     restore placeholder state after refresh.
//   • Shot JSON shape + OMITEMPTY-tagged TimelineStartMs / Duration:
//     when nil, these keys disappear from the payload. Pin so a
//     refactor to `int64` (non-pointer) would silently start emitting
//     `"timelineStartMs":0` and the frontend would treat every shot as
//     starting at ms 0.

func TestCanvasElementDTO_IsGenerating_ClipContent_PointerFalseEmit(t *testing.T) {
	// Pointer-bool symmetric twins to the base test's Visible/Locked
	// — the AI-flow fields and the frame-clip field have the same
	// "unset vs explicit false" requirement.
	gen := false
	clip := false
	el := CanvasElementDTO{
		ID:           "e-1",
		Type:         "image",
		IsGenerating: &gen,
		ClipContent:  &clip,
	}
	b, err := json.Marshal(el)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(b)
	if !strings.Contains(out, `"isGenerating":false`) {
		t.Errorf("expected isGenerating:false in %s", out)
	}
	if !strings.Contains(out, `"clipContent":false`) {
		t.Errorf("expected clipContent:false in %s", out)
	}
}

func TestCanvasElementDTO_NumericZeroFieldsOmit(t *testing.T) {
	// Width/Height/Rotation/StrokeWidth/FontSize all have omitempty.
	// A refactor that dropped any of them would start emitting `:0`
	// and the frontend's truthy-branch UI defaults would silently flip.
	el := CanvasElementDTO{ID: "e", Type: "text", X: 0, Y: 0}
	b, err := json.Marshal(el)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(b)
	for _, absent := range []string{"width", "height", "rotation", "strokeWidth", "fontSize"} {
		if strings.Contains(out, `"`+absent+`":`) {
			t.Errorf("expected %q to be omitted (omitempty) when zero; got %s", absent, out)
		}
	}
}

func TestCanvasElementDTO_X_Y_AlwaysEmit_EvenAtZero(t *testing.T) {
	// X and Y have no omitempty — origin (0,0) must still emit so
	// the frontend distinguishes "at origin" from "undefined".
	el := CanvasElementDTO{ID: "e", Type: "text"} // X=0, Y=0
	b, err := json.Marshal(el)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(b)
	if !strings.Contains(out, `"x":0`) {
		t.Errorf("expected x:0 in %s", out)
	}
	if !strings.Contains(out, `"y":0`) {
		t.Errorf("expected y:0 in %s", out)
	}
}

func TestCanvasProject_UUID_AlwaysEmits(t *testing.T) {
	// No omitempty on UUID — empty string emits `"uuid":""`.
	p := CanvasProject{}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(b)
	if !strings.Contains(out, `"uuid":""`) {
		t.Errorf("expected uuid:\"\" in %s", out)
	}
}

func TestGlobalAsset_SizeBytes_LargeInt64RoundTrips(t *testing.T) {
	// int64 values above 2^53 still round-trip exactly through
	// encoding/json (Go emits the raw integer, unmarshals back to
	// int64). Pin the no-precision-loss contract for 9GB assets.
	orig := GlobalAsset{SizeBytes: 9_000_000_000}
	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"sizeBytes":9000000000`) {
		t.Errorf("expected sizeBytes:9000000000 in %s", string(b))
	}
	var back GlobalAsset
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.SizeBytes != orig.SizeBytes {
		t.Errorf("SizeBytes round-trip: got %d want %d", back.SizeBytes, orig.SizeBytes)
	}
	// Also pin math.MaxInt64 precision — this is the ceiling a storage
	// backend could legitimately report.
	big := GlobalAsset{SizeBytes: math.MaxInt64}
	b2, _ := json.Marshal(big)
	var back2 GlobalAsset
	if err := json.Unmarshal(b2, &back2); err != nil {
		t.Fatalf("unmarshal MaxInt64: %v", err)
	}
	if back2.SizeBytes != math.MaxInt64 {
		t.Errorf("MaxInt64 lost precision: got %d", back2.SizeBytes)
	}
}

func TestViewportDTO_ZeroValuesAllEmit(t *testing.T) {
	// Explicit pin: all three fields emit their zero value (no
	// omitempty). Base test checks presence of keys but not that
	// each key actually carries `:0` on the wire.
	vp := ViewportDTO{}
	b, err := json.Marshal(vp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(b)
	for _, want := range []string{`"x":0`, `"y":0`, `"scale":0`} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %s in %s", want, out)
		}
	}
}

func TestAssetBindingScope_UnknownStringPassthroughNoValidation(t *testing.T) {
	// AssetBindingScope is `type = string` with no custom Unmarshal.
	// Any string round-trips. Pin so a refactor that added validation
	// would deliberately tighten the contract (and break old clients).
	var got AssetBindingScope
	if err := json.Unmarshal([]byte(`"flibble"`), &got); err != nil {
		t.Fatalf("unmarshal unknown: %v", err)
	}
	if string(got) != "flibble" {
		t.Errorf("unknown scope passthrough = %q, want %q", string(got), "flibble")
	}
	if got == AssetScopeElement || got == AssetScopeShot || got == AssetScopeProject {
		t.Errorf("unknown scope should not equal a defined constant, got %q", got)
	}
}

func TestAssetBinding_PartialAndEmptySliceShape(t *testing.T) {
	// With only CharacterIDs set, other id/weight keys stay absent.
	ab := AssetBinding{Scope: AssetScopeShot, CharacterIDs: []int{1, 2}}
	b, err := json.Marshal(ab)
	if err != nil {
		t.Fatalf("marshal partial: %v", err)
	}
	out := string(b)
	if !strings.Contains(out, `"characterIds":[1,2]`) {
		t.Errorf("expected characterIds in %s", out)
	}
	for _, absent := range []string{"brandIds", "productIds", "characterWeights", "brandWeights", "productWeights"} {
		if strings.Contains(out, `"`+absent+`":`) {
			t.Errorf("expected %q to be omitted; got %s", absent, out)
		}
	}

	// Empty-but-non-nil []int{} still OMITS under encoding/json's
	// omitempty. Pin the len==0 rule so callers stop trying to signal
	// "empty binding" with an explicit []int{}.
	emptyExplicit := AssetBinding{Scope: AssetScopeProject, CharacterIDs: []int{}}
	b2, _ := json.Marshal(emptyExplicit)
	if string(b2) != `{"scope":"project"}` {
		t.Errorf("empty []int{} should still omit, got %q", string(b2))
	}
}

func TestPaginatedResult_NilVsEmptyItems(t *testing.T) {
	// nil slice → null; empty slice → []. Two observably different
	// wire shapes; pin the distinction.
	nilCase := PaginatedResult[string]{Items: nil, Total: 0, Page: 1, Limit: 10}
	b, err := json.Marshal(nilCase)
	if err != nil {
		t.Fatalf("marshal nil: %v", err)
	}
	if !strings.Contains(string(b), `"items":null`) {
		t.Errorf("expected items:null in %s", string(b))
	}

	emptyCase := PaginatedResult[string]{Items: []string{}, Total: 0, Page: 1, Limit: 10}
	b2, err := json.Marshal(emptyCase)
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	if !strings.Contains(string(b2), `"items":[]`) {
		t.Errorf("expected items:[] in %s", string(b2))
	}
}

func TestPaginatedResult_GenericTypeErasedInOutput(t *testing.T) {
	// The JSON shape is identical regardless of T — pin the erasure so
	// a refactor that added a type discriminator wouldn't silently
	// reshape the wire format.
	strCase := PaginatedResult[string]{Items: []string{"a"}, Total: 1, Page: 1, Limit: 10}
	intCase := PaginatedResult[int]{Items: []int{1}, Total: 1, Page: 1, Limit: 10}

	bs, _ := json.Marshal(strCase)
	bi, _ := json.Marshal(intCase)
	// Keys must be identical set.
	var pm map[string]json.RawMessage
	if err := json.Unmarshal(bs, &pm); err != nil {
		t.Fatalf("unmarshal string: %v", err)
	}
	for _, key := range []string{"items", "total", "page", "limit"} {
		if _, ok := pm[key]; !ok {
			t.Errorf("PaginatedResult[string] missing %q in %s", key, string(bs))
		}
	}
	var pm2 map[string]json.RawMessage
	if err := json.Unmarshal(bi, &pm2); err != nil {
		t.Fatalf("unmarshal int: %v", err)
	}
	if len(pm) != len(pm2) {
		t.Errorf("key-set size differs: %d vs %d", len(pm), len(pm2))
	}
}

func TestCanvasTaskBinding_JSONShape(t *testing.T) {
	// The recovery endpoint reads taskId + elementId verbatim to
	// restore placeholder state after page refresh.
	binding := CanvasTaskBinding{UID: 3, ProjectID: 11, TaskID: "task-abc", ElementID: "el-1"}
	b, err := json.Marshal(binding)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(b)
	for _, want := range []string{
		`"uid":3`, `"projectId":11`, `"taskId":"task-abc"`, `"elementId":"el-1"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %s in %s", want, out)
		}
	}
}

func TestShot_TimelineFields_NilOmitsNonNilEmits(t *testing.T) {
	// TimelineStartMs / TimelineDurationMs are *int64 with omitempty.
	// Nil → absent; set → emitted. A refactor to int64 (non-pointer)
	// would remove omitempty effectiveness and every shot would emit
	// timelineStartMs:0, which the frontend would misread as "starts
	// at ms 0" instead of "timeline not set".
	shot := Shot{UID: 1, CanvasProjectID: 2, LocalCardID: "card-1", OrderIndex: 0, Title: "Scene 1"}
	b, err := json.Marshal(shot)
	if err != nil {
		t.Fatalf("marshal nil timeline: %v", err)
	}
	out := string(b)
	if strings.Contains(out, "timelineStartMs") {
		t.Errorf("nil TimelineStartMs should be omitted; got %s", out)
	}
	if strings.Contains(out, "timelineDurationMs") {
		t.Errorf("nil TimelineDurationMs should be omitted; got %s", out)
	}

	start := int64(500)
	dur := int64(2000)
	shot.TimelineStartMs = &start
	shot.TimelineDurationMs = &dur
	b2, err := json.Marshal(shot)
	if err != nil {
		t.Fatalf("marshal set timeline: %v", err)
	}
	out2 := string(b2)
	if !strings.Contains(out2, `"timelineStartMs":500`) {
		t.Errorf("expected timelineStartMs:500 in %s", out2)
	}
	if !strings.Contains(out2, `"timelineDurationMs":2000`) {
		t.Errorf("expected timelineDurationMs:2000 in %s", out2)
	}
}

func TestAssetBindingScope_MarshalsAsRawString(t *testing.T) {
	// The type alias to string means marshal emits the raw string
	// literal (no JSON object wrapping). Pin so a refactor to a
	// struct type would surface as a wire-format change.
	for _, c := range []struct {
		scope AssetBindingScope
		want  string
	}{
		{AssetScopeElement, `"element"`},
		{AssetScopeShot, `"shot"`},
		{AssetScopeProject, `"project"`},
	} {
		b, err := json.Marshal(c.scope)
		if err != nil {
			t.Fatalf("marshal %q: %v", c.scope, err)
		}
		if string(b) != c.want {
			t.Errorf("marshal %q = %s, want %s", c.scope, string(b), c.want)
		}
	}
}
