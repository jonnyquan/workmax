package tools

// Pins the pure helpers at the top of canvas_generation_api.go:
//
//   normalizeCanvasImageModel         — blank → NANO_BANANA_2 default
//   normalizeCanvasModelCandidates    — primary + list dedup / normalize
//   filterCanvasModelCandidates       — blocklist removal
//   buildCanvasReferenceImages        — request-body reference shape
//   setCanvasOperationMeta            — source/canvasOperation/meta stamp
//
// These feed authorizeCanvasModelCandidates and submitCanvasGenerationTask,
// both of which are exercised in the big canvas_api_test.go. The leaf
// tests here make contract changes (new model id, a 2nd reference slot,
// a different meta key) fail with a sharp message instead of a diffuse
// generation-path failure.

import (
	"reflect"
	"testing"

	"server/model"
)

func TestNormalizeCanvasImageModel(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", model.NANO_BANANA_2},
		{"   ", model.NANO_BANANA_2},
		{"nano banana 2", model.NANO_BANANA_2},
		{"NANO-BANANA-2", model.NANO_BANANA_2},
		{"NanoBanana2", model.NANO_BANANA_2},
		{model.NANO_BANANA_PRO, model.NANO_BANANA_PRO},
		{"nano banana pro", model.NANO_BANANA_PRO},
		// NormalizeModelID returns the lower-cased input for unknown ids,
		// which the caller is allowed to use — normalization isn't an
		// allow-list, it's a canonical-form map.
		{"custom-model-id", "custom-model-id"},
	}
	for _, tc := range cases {
		if got := normalizeCanvasImageModel(tc.in); got != tc.want {
			t.Errorf("normalizeCanvasImageModel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeCanvasModelCandidates(t *testing.T) {
	t.Run("empty primary and list returns empty", func(t *testing.T) {
		got := normalizeCanvasModelCandidates("", nil)
		if len(got) != 0 {
			t.Errorf("got %v, want empty", got)
		}
	})

	t.Run("primary is prepended and deduped against the list", func(t *testing.T) {
		got := normalizeCanvasModelCandidates(
			"NANO-BANANA-2",
			[]string{"nano banana 2", model.NANO_BANANA_PRO, "nano-banana-pro"},
		)
		want := []string{model.NANO_BANANA_2, model.NANO_BANANA_PRO}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("blank entries are dropped", func(t *testing.T) {
		got := normalizeCanvasModelCandidates(
			"",
			[]string{"   ", "", model.NANO_BANANA_2, ""},
		)
		want := []string{model.NANO_BANANA_2}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("unknown ids pass through lower-cased", func(t *testing.T) {
		got := normalizeCanvasModelCandidates("Custom-ID-A", []string{"custom-id-a", "Custom-ID-B"})
		want := []string{"custom-id-a", "custom-id-b"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
}

func TestFilterCanvasModelCandidates(t *testing.T) {
	t.Run("returns input unchanged when blocklist is empty", func(t *testing.T) {
		in := []string{model.NANO_BANANA_2, model.NANO_BANANA_PRO}
		got := filterCanvasModelCandidates(in, nil)
		if !reflect.DeepEqual(got, in) {
			t.Errorf("got %v, want %v", got, in)
		}
	})

	t.Run("returns input unchanged when input is empty", func(t *testing.T) {
		got := filterCanvasModelCandidates(nil, map[string]struct{}{model.NANO_BANANA_PRO: {}})
		if len(got) != 0 {
			t.Errorf("got %v, want empty", got)
		}
	})

	t.Run("drops blocklisted entries and preserves order", func(t *testing.T) {
		got := filterCanvasModelCandidates(
			[]string{model.NANO_BANANA_2, model.NANO_BANANA_PRO, "other-model"},
			map[string]struct{}{model.NANO_BANANA_PRO: {}},
		)
		want := []string{model.NANO_BANANA_2, "other-model"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
}

func TestBuildCanvasReferenceImages(t *testing.T) {
	// The shape is consumed by GenerateImage — a drift in key names or
	// weight would silently break canvas → generator handoff.
	got := buildCanvasReferenceImages("https://cdn.example.com/a.png")
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	entry := got[0]
	if entry["id"] != "canvas-source-1" {
		t.Errorf("id = %#v, want canvas-source-1", entry["id"])
	}
	if entry["url"] != "https://cdn.example.com/a.png" {
		t.Errorf("url = %#v", entry["url"])
	}
	if entry["weight"] != 1 {
		t.Errorf("weight = %#v, want 1", entry["weight"])
	}
}

func TestSetCanvasOperationMeta(t *testing.T) {
	t.Run("no-ops on nil map", func(t *testing.T) {
		// Should not panic and should be observably a no-op.
		setCanvasOperationMeta(nil, "img2img", model.JSONMap{"k": "v"})
	})

	t.Run("stamps source + operation; omits meta when empty", func(t *testing.T) {
		req := model.JSONMap{"prompt": "p"}
		setCanvasOperationMeta(req, "img2img", nil)
		if req["source"] != "canvas" {
			t.Errorf("source = %#v, want canvas", req["source"])
		}
		if req["canvasOperation"] != "img2img" {
			t.Errorf("canvasOperation = %#v, want img2img", req["canvasOperation"])
		}
		if _, present := req["canvasOperationMeta"]; present {
			t.Errorf("canvasOperationMeta should be omitted when meta is empty: %#v", req["canvasOperationMeta"])
		}
	})

	t.Run("stamps meta when non-empty", func(t *testing.T) {
		req := model.JSONMap{}
		meta := model.JSONMap{"strength": 0.5}
		setCanvasOperationMeta(req, "edit-text", meta)
		if req["canvasOperation"] != "edit-text" {
			t.Errorf("canvasOperation = %#v", req["canvasOperation"])
		}
		if !reflect.DeepEqual(req["canvasOperationMeta"], meta) {
			t.Errorf("canvasOperationMeta = %#v, want %#v", req["canvasOperationMeta"], meta)
		}
	})
}
