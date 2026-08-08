package tools

import (
	"math"
	"server/constants"
	"server/globals"
	"server/model"
	"testing"
	"time"
)

// Locks the invariant that LoraPrompts and LoraNegativePrompts cover the
// same set of preset keys. Catches the "added a new preset to one map but
// forgot the other" drift before it ships and 400s real users at the
// generate endpoint. The Desktop catalog is validated at its own boundary;
// this test keeps the Server registry internally consistent.
func TestLoraPromptRegistriesAreInSync(t *testing.T) {
	missingNegative := make([]string, 0)
	for slug := range constants.LoraPrompts {
		if _, ok := constants.LoraNegativePrompts[slug]; !ok {
			missingNegative = append(missingNegative, slug)
		}
	}
	if len(missingNegative) > 0 {
		t.Errorf("LoraPrompts has slugs missing from LoraNegativePrompts: %v", missingNegative)
	}

	orphanNegative := make([]string, 0)
	for slug := range constants.LoraNegativePrompts {
		if _, ok := constants.LoraPrompts[slug]; !ok {
			orphanNegative = append(orphanNegative, slug)
		}
	}
	if len(orphanNegative) > 0 {
		t.Errorf("LoraNegativePrompts has slugs missing from LoraPrompts: %v", orphanNegative)
	}

	for slug, prompt := range constants.LoraPrompts {
		if prompt == "" {
			t.Errorf("LoraPrompts[%q] is empty", slug)
		}
	}
	for slug, prompt := range constants.LoraNegativePrompts {
		if prompt == "" {
			t.Errorf("LoraNegativePrompts[%q] is empty", slug)
		}
	}
}

func TestBuildLoraToolID(t *testing.T) {
	cases := []struct {
		name string
		slug string
		want string
	}{
		{"empty", "", model.TOOL_LORA_STUDIO},
		{"whitespace", "   ", model.TOOL_LORA_STUDIO},
		{"simple", "anime", model.TOOL_LORA_STUDIO + ":anime"},
		{"uppercase lowercased", "Anime", model.TOOL_LORA_STUDIO + ":anime"},
		{"trims surrounding spaces", "  anime  ", model.TOOL_LORA_STUDIO + ":anime"},
		{"colon replaced with dash", "Foo:Bar", model.TOOL_LORA_STUDIO + ":foo-bar"},
	}
	for _, tc := range cases {
		if got := buildLoraToolID(tc.slug); got != tc.want {
			t.Errorf("%s: buildLoraToolID(%q) = %q, want %q", tc.name, tc.slug, got, tc.want)
		}
	}
}

func TestLoraSlugFromToolID(t *testing.T) {
	cases := []struct {
		name   string
		toolID string
		want   string
	}{
		{"empty", "", ""},
		{"prefix only (no colon)", model.TOOL_LORA_STUDIO, ""},
		{"prefix with empty slug", model.TOOL_LORA_STUDIO + ":", ""},
		{"prefix with whitespace slug", model.TOOL_LORA_STUDIO + ":   ", ""},
		{"valid slug", model.TOOL_LORA_STUDIO + ":anime", "anime"},
		{"slug with dash", model.TOOL_LORA_STUDIO + ":foo-bar", "foo-bar"},
		{"unrelated tool", "other_tool:anime", ""},
	}
	for _, tc := range cases {
		if got := loraSlugFromToolID(tc.toolID); got != tc.want {
			t.Errorf("%s: loraSlugFromToolID(%q) = %q, want %q", tc.name, tc.toolID, got, tc.want)
		}
	}
}

func TestBuildAndLoraSlugRoundTrip(t *testing.T) {
	slugs := []string{"anime", "foo-bar", "studio_one"}
	for _, slug := range slugs {
		toolID := buildLoraToolID(slug)
		if got := loraSlugFromToolID(toolID); got != slug {
			t.Errorf("round-trip %q: got %q via toolID %q", slug, got, toolID)
		}
	}
}

func TestMergePromptSegments(t *testing.T) {
	cases := []struct {
		name  string
		parts []string
		want  string
	}{
		{"empty inputs", []string{"", ""}, ""},
		{"single segment", []string{"lowres"}, "lowres"},
		{"comma split + trim", []string{" lowres ,  blurry  "}, "lowres, blurry"},
		{"case-insensitive dedup keeps first casing",
			[]string{"Lowres, blurry", "LOWRES, ugly"},
			"Lowres, blurry, ugly"},
		{"empty fragments dropped", []string{"a,,b, ,c"}, "a, b, c"},
		{"dedup across parts", []string{"a, b", "b, c"}, "a, b, c"},
	}
	for _, tc := range cases {
		if got := mergePromptSegments(tc.parts...); got != tc.want {
			t.Errorf("%s: mergePromptSegments(%v) = %q, want %q", tc.name, tc.parts, got, tc.want)
		}
	}
}

func TestClampListLimit(t *testing.T) {
	cases := []struct {
		name     string
		raw      int
		fallback int
		want     int
	}{
		{"zero uses fallback", 0, 20, 20},
		{"negative uses fallback", -5, 20, 20},
		{"positive below cap", 50, 20, 50},
		{"at cap", 100, 20, 100},
		{"above cap clamped", 250, 20, 100},
		{"fallback above cap still capped", 0, 500, 100},
	}
	for _, tc := range cases {
		if got := clampListLimit(tc.raw, tc.fallback); got != tc.want {
			t.Errorf("%s: clampListLimit(%d, %d) = %d, want %d",
				tc.name, tc.raw, tc.fallback, got, tc.want)
		}
	}
}

func TestSafeFloatFromJSONNumber(t *testing.T) {
	if got, ok := safeFloatFromJSONNumber(3.14); !ok || got != 3.14 {
		t.Errorf("finite: got (%v, %v), want (3.14, true)", got, ok)
	}
	if got, ok := safeFloatFromJSONNumber(0.0); !ok || got != 0.0 {
		t.Errorf("zero: got (%v, %v), want (0, true)", got, ok)
	}
	if _, ok := safeFloatFromJSONNumber(math.NaN()); ok {
		t.Errorf("NaN: ok = true, want false")
	}
	if _, ok := safeFloatFromJSONNumber(math.Inf(1)); ok {
		t.Errorf("+Inf: ok = true, want false")
	}
	if _, ok := safeFloatFromJSONNumber(math.Inf(-1)); ok {
		t.Errorf("-Inf: ok = true, want false")
	}
	if _, ok := safeFloatFromJSONNumber("not a number"); ok {
		t.Errorf("string: ok = true, want false")
	}
	if _, ok := safeFloatFromJSONNumber(nil); ok {
		t.Errorf("nil: ok = true, want false")
	}
	// JSON unmarshal always yields float64, but a raw int from other
	// paths must not sneak through without the explicit type assertion.
	if _, ok := safeFloatFromJSONNumber(42); ok {
		t.Errorf("int (not float64): ok = true, want false")
	}
}

func TestParseLoraRequestParams(t *testing.T) {
	if out := parseLoraRequestParams(nil); len(out) != 0 {
		t.Errorf("nil input: got %v, want empty", out)
	}
	if out := parseLoraRequestParams(map[string]interface{}{}); len(out) != 0 {
		t.Errorf("empty input: got %v, want empty", out)
	}

	// numberOfImages clamping
	clampCases := []struct {
		in   float64
		want int
	}{
		{1, 1},
		{2, 2},
		{4, 4},
		{0, 1},   // below min clamps up
		{-5, 1},  // negative clamps up
		{7, 4},   // above max clamps down
		{100, 4}, // way above clamps down
	}
	for _, tc := range clampCases {
		out := parseLoraRequestParams(map[string]interface{}{"numberOfImages": tc.in})
		got, ok := out["numberOfImages"].(int)
		if !ok {
			t.Errorf("numberOfImages=%v: missing or wrong type in %v", tc.in, out)
			continue
		}
		if got != tc.want {
			t.Errorf("numberOfImages=%v: got %d, want %d", tc.in, got, tc.want)
		}
	}

	// seed passes through as int64
	out := parseLoraRequestParams(map[string]interface{}{"seed": 12345.0})
	if got, ok := out["seed"].(int64); !ok || got != 12345 {
		t.Errorf("seed: got %v, want int64(12345)", out["seed"])
	}

	// NaN / Inf silently dropped
	out = parseLoraRequestParams(map[string]interface{}{
		"numberOfImages": math.NaN(),
		"seed":           math.Inf(1),
	})
	if _, present := out["numberOfImages"]; present {
		t.Errorf("NaN numberOfImages leaked into result: %v", out)
	}
	if _, present := out["seed"]; present {
		t.Errorf("Inf seed leaked into result: %v", out)
	}

	// Unknown keys dropped
	out = parseLoraRequestParams(map[string]interface{}{
		"steps":     20.0,
		"cfgScale":  7.5,
		"sampler":   "euler",
		"upscale":   true,
		"arbitrary": "value",
	})
	if len(out) != 0 {
		t.Errorf("unknown keys leaked: %v", out)
	}

	// Mixed: valid + unknown
	out = parseLoraRequestParams(map[string]interface{}{
		"numberOfImages": 3.0,
		"seed":           99.0,
		"steps":          20.0, // dropped
	})
	if len(out) != 2 {
		t.Errorf("mixed: len = %d, want 2 (got %v)", len(out), out)
	}
	if got, _ := out["numberOfImages"].(int); got != 3 {
		t.Errorf("mixed numberOfImages: got %v, want 3", out["numberOfImages"])
	}
	if got, _ := out["seed"].(int64); got != 99 {
		t.Errorf("mixed seed: got %v, want int64(99)", out["seed"])
	}
}

func TestLoraReferenceImagesRejectOtherUserURL(t *testing.T) {
	_, err := sanitizeVideoGeneratorReferenceImages([]ReferenceImageInput{{
		ID:     "source",
		URL:    "/uploads/references/uid/43/2026/05/05/ref.png",
		Weight: 1,
	}}, 42)
	if err == nil {
		t.Fatal("expected owner mismatch error")
	}
}

func TestLoraReferenceImagesAcceptOwnedURL(t *testing.T) {
	out, err := sanitizeVideoGeneratorReferenceImages([]ReferenceImageInput{{
		ID:     "source",
		URL:    "/uploads/references/uid/42/2026/05/05/ref.png",
		Weight: 1,
	}}, 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
}

func TestToLoraHistoryRecordDto(t *testing.T) {
	if got := toLoraHistoryRecordDto(nil); got != (loraHistoryRecordDto{}) {
		t.Errorf("nil input: got %+v, want zero value", got)
	}

	created := time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)
	record := &model.GenerationRecord{
		GraMODEL: globals.GraMODEL{
			Id:        42,
			CreatedAt: created,
		},
		Model:          model.NANO_BANANA_2,
		Prompt:         "a cat",
		NegativePrompt: "blurry",
		AspectRatio:    "1:1",
		StylePreset:    "anime",
		Params:         `{"numberOfImages":2}`,
		InputFiles:     `{"referenceImages":[]}`,
		ResultImages:   `["https://a/1.png"]`,
		ResultMetadata: `{}`,
		Status:         model.STATUS_SUCCESS,
	}

	dto := toLoraHistoryRecordDto(record)
	if dto.ID != 42 {
		t.Errorf("ID = %d, want 42", dto.ID)
	}
	if !dto.CreatedAt.Equal(created) {
		t.Errorf("CreatedAt = %v, want %v", dto.CreatedAt, created)
	}
	if dto.Model != model.NANO_BANANA_2 {
		t.Errorf("Model = %q, want %q", dto.Model, model.NANO_BANANA_2)
	}
	if dto.Prompt != "a cat" {
		t.Errorf("Prompt = %q, want %q", dto.Prompt, "a cat")
	}
	if dto.NegativePrompt != "blurry" {
		t.Errorf("NegativePrompt = %q", dto.NegativePrompt)
	}
	if dto.AspectRatio != "1:1" {
		t.Errorf("AspectRatio = %q", dto.AspectRatio)
	}
	if dto.StylePreset != "anime" {
		t.Errorf("StylePreset = %q", dto.StylePreset)
	}
	if dto.Params != `{"numberOfImages":2}` {
		t.Errorf("Params = %q", dto.Params)
	}
	if dto.InputFiles != `{"referenceImages":[]}` {
		t.Errorf("InputFiles = %q", dto.InputFiles)
	}
	if dto.ResultImages != `["https://a/1.png"]` {
		t.Errorf("ResultImages = %q", dto.ResultImages)
	}
	if dto.ResultMetadata != `{}` {
		t.Errorf("ResultMetadata = %q", dto.ResultMetadata)
	}
	if dto.Status != model.STATUS_SUCCESS {
		t.Errorf("Status = %d, want %d", dto.Status, model.STATUS_SUCCESS)
	}
}

func TestIsLoraGenerationTask(t *testing.T) {
	cases := []struct {
		name string
		task *model.GenerationTask
		want bool
	}{
		{"nil task", nil, false},
		{"empty toolID", &model.GenerationTask{ToolID: ""}, false},
		{"prefix without slug", &model.GenerationTask{ToolID: model.TOOL_LORA_STUDIO + ":"}, false},
		{"prefix only no colon", &model.GenerationTask{ToolID: model.TOOL_LORA_STUDIO}, false},
		{"unrelated tool", &model.GenerationTask{ToolID: "other_tool:foo"}, false},
		{"valid lora task", &model.GenerationTask{ToolID: model.TOOL_LORA_STUDIO + ":anime"}, true},
	}
	for _, tc := range cases {
		if got := isLoraGenerationTask(tc.task); got != tc.want {
			t.Errorf("%s: isLoraGenerationTask = %v, want %v", tc.name, got, tc.want)
		}
	}
}
