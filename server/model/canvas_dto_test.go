package model

import (
	"encoding/json"
	"strings"
	"testing"
)

// canvas_test.go pins the table names + enum constants. These fill in
// the JSON wire-format contracts on the DTO side — any field that
// crosses the HTTP boundary to the React client, where a drift in
// omitempty / key name / type would silently break a frontend parser.

func TestCanvasElementDTO_OmitsAbsentOptionalFields(t *testing.T) {
	// A minimal element (id/type/x/y set, everything else zero) must
	// marshal WITHOUT the optional fields — omitempty is load-bearing
	// because the frontend uses `field in element` to branch. A missing
	// omitempty would make every optional key show up as "" / 0, and
	// the UI would treat a blank `color` as "color override = white".
	el := CanvasElementDTO{ID: "el-1", Type: "text", X: 10, Y: 20}
	b, err := json.Marshal(el)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(b)

	// x / y must always appear (no omitempty) — the canvas coordinate
	// system treats missing = undefined, not zero.
	for _, want := range []string{`"id":"el-1"`, `"type":"text"`, `"x":10`, `"y":20`} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %s in %s", want, out)
		}
	}
	// omitempty keys that are zero-valued must NOT appear.
	for _, absent := range []string{"content", "color", "src", "path", "visible", "locked", "rotation", "taskId", "prompt"} {
		if strings.Contains(out, `"`+absent+`":`) {
			t.Errorf("expected %q to be omitted (omitempty); got %s", absent, out)
		}
	}
}

func TestCanvasElementDTO_PointerBoolsEmitFalseWhenSet(t *testing.T) {
	// `Visible`/`Locked`/`IsGenerating`/`ClipContent` are *bool so that
	// the frontend can distinguish "unset" from "explicitly false".
	// A nil pointer omits; a *bool pointing to false MUST serialize as
	// "visible":false — otherwise a user toggling visibility off would
	// round-trip as "not set" and the element would flash back on.
	visFalse := false
	lockFalse := false
	el := CanvasElementDTO{
		ID:      "el-2",
		Type:    "image",
		Visible: &visFalse,
		Locked:  &lockFalse,
	}
	b, err := json.Marshal(el)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(b)
	if !strings.Contains(out, `"visible":false`) {
		t.Errorf("expected visible:false in %s", out)
	}
	if !strings.Contains(out, `"locked":false`) {
		t.Errorf("expected locked:false in %s", out)
	}
}

func TestViewportDTO_AlwaysEmitsXYScale(t *testing.T) {
	// Viewport fields have no omitempty — the frontend hydrates a
	// Zustand slice directly from the payload and expects all three
	// keys present. A field going omitempty would leave `scale`
	// undefined on the client and zoom would jump to 1 on reload.
	vp := ViewportDTO{} // all zero values
	b, err := json.Marshal(vp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"x", "y", "scale"} {
		if _, ok := parsed[key]; !ok {
			t.Errorf("ViewportDTO missing key %q in output %s", key, string(b))
		}
	}
}

func TestAssetBindingScope_UnmarshalsFromStringLiteral(t *testing.T) {
	// Frontend sends the raw string `"element"` / `"shot"` / `"project"`.
	// AssetBindingScope is a `type AssetBindingScope string`, so it must
	// round-trip through json.Unmarshal and compare equal to the matching
	// constant — the service uses == against the constants to pick a
	// code path, and a custom UnmarshalJSON that bypassed this comparison
	// would silently ignore client-specified scopes.
	cases := []struct {
		raw  string
		want AssetBindingScope
	}{
		{`"element"`, AssetScopeElement},
		{`"shot"`, AssetScopeShot},
		{`"project"`, AssetScopeProject},
	}
	for _, c := range cases {
		var got AssetBindingScope
		if err := json.Unmarshal([]byte(c.raw), &got); err != nil {
			t.Errorf("unmarshal %s: %v", c.raw, err)
			continue
		}
		if got != c.want {
			t.Errorf("unmarshal %s = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestAssetBinding_OmitsEmptySliceAndMap(t *testing.T) {
	// A scope-only binding (no ids, no weights) must flatten to just
	// `{"scope":"project"}` — the frontend treats a present `characterIds`
	// key, even as `[]`, as "binding explicitly empty" vs. omitted
	// meaning "no binding specified". Pin the difference.
	b, err := json.Marshal(AssetBinding{Scope: AssetScopeProject})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(b)
	if out != `{"scope":"project"}` {
		t.Errorf("bare binding should be %q, got %q", `{"scope":"project"}`, out)
	}
}

func TestPaginatedResult_MarshalShape(t *testing.T) {
	// PaginatedResult[T] crosses into the HTTP layer with keys
	// items/total/page/limit. The generic type parameter must not leak
	// into the JSON (encoding/json erases it) — if it ever did, the
	// frontend pager would break on every list endpoint.
	p := PaginatedResult[int]{Items: []int{1, 2, 3}, Total: 3, Page: 1, Limit: 20}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(b)
	for _, want := range []string{`"items":[1,2,3]`, `"total":3`, `"page":1`, `"limit":20`} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %s in %s", want, out)
		}
	}
}

func TestCanvasElementDTO_RoundTripPreservesKnownFields(t *testing.T) {
	// Full-fidelity round trip — marshal then unmarshal — must reproduce
	// the value exactly for the subset of fields the Chat API sends
	// round-tripped back from a prompt response. A regression in a field
	// tag (wrong json name, accidental omitempty on a required field)
	// would show up here as a non-equal deserialisation.
	clip := true
	gen := false
	orig := CanvasElementDTO{
		ID:           "hero-1",
		Type:         "image",
		X:            100, Y: 50,
		Width:        300, Height: 200,
		Src:          "/u/hero.png",
		Rotation:     45.5,
		FrameId:      "frame-a",
		ClipContent:  &clip,
		IsGenerating: &gen,
		TaskId:       "task-123",
		Prompt:       "a cat",
		Model:        "sdxl",
		AspectRatio:  "16:9",
	}
	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back CanvasElementDTO
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.ID != orig.ID || back.Type != orig.Type || back.Src != orig.Src {
		t.Errorf("round-trip mismatch on basic fields:\n orig=%+v\n back=%+v", orig, back)
	}
	if back.ClipContent == nil || *back.ClipContent != true {
		t.Errorf("ClipContent lost through round-trip: %+v", back.ClipContent)
	}
	if back.IsGenerating == nil || *back.IsGenerating != false {
		t.Errorf("IsGenerating lost through round-trip: %+v", back.IsGenerating)
	}
	if back.AspectRatio != "16:9" {
		t.Errorf("AspectRatio = %q, want %q", back.AspectRatio, "16:9")
	}
}
