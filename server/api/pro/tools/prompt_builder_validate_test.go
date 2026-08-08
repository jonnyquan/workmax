package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseLLMJSONContent(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantKey string
		wantErr bool
	}{
		{"plain", `{"mode":"portrait"}`, "mode", false},
		{"fenced", "```json\n{\"mode\":\"portrait\"}\n```", "mode", false},
		{"prose prefix", "Here is the result:\n{\"mode\":\"scene\"}", "mode", false},
		{"empty", "", "", true},
		{"invalid", "not json at all", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseLLMJSONContent(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %#v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if _, ok := got[tc.wantKey]; !ok {
				t.Fatalf("expected key %q in parsed result, got %#v", tc.wantKey, got)
			}
		})
	}
}

func TestIsPromptBuilderMode(t *testing.T) {
	if !isPromptBuilderMode("portrait") {
		t.Fatal("portrait should be an allowed mode")
	}
	if !isPromptBuilderMode("  freeform  ") {
		t.Fatal("whitespace around mode should be tolerated")
	}
	if isPromptBuilderMode("bogus") {
		t.Fatal("bogus mode should be rejected")
	}
}

func TestIsPromptBuilderFieldAllowed(t *testing.T) {
	if !isPromptBuilderFieldAllowed("subject", "characters") {
		t.Fatal("subject.characters should be allowed")
	}
	if isPromptBuilderFieldAllowed("subject", "shot_type") {
		t.Fatal("shot_type belongs to camera, not subject")
	}
	if isPromptBuilderFieldAllowed("unknown_block", "anything") {
		t.Fatal("unknown block types should reject all fields")
	}
}

func TestValidateAndNormalizeBlocksJSON_RawArray(t *testing.T) {
	raw := `[{"type":"subject","data":{"characters":[{"name":"a"}]},"collapsed":true}]`
	out, err := validateAndNormalizePromptBuilderBlocksJSON(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var decoded struct {
		SchemaVersion int `json:"schema_version"`
		Blocks        []struct {
			Type      string                 `json:"type"`
			Data      map[string]interface{} `json:"data"`
			Collapsed bool                   `json:"collapsed"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("unmarshal normalized: %v", err)
	}
	if decoded.SchemaVersion != promptBuilderSchemaVersion {
		t.Fatalf("expected schema_version=%d, got %d", promptBuilderSchemaVersion, decoded.SchemaVersion)
	}
	if len(decoded.Blocks) != 1 || decoded.Blocks[0].Type != "subject" {
		t.Fatalf("unexpected blocks: %#v", decoded.Blocks)
	}
	if !decoded.Blocks[0].Collapsed {
		t.Fatal("collapsed flag should be preserved")
	}
}

func TestValidateAndNormalizeBlocksJSON_FiltersUnknownType(t *testing.T) {
	raw := `{"schema_version":2,"blocks":[{"type":"subject","data":{}},{"type":"totally_invalid","data":{}}]}`
	out, err := validateAndNormalizePromptBuilderBlocksJSON(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(out, "totally_invalid") {
		t.Fatalf("unknown block types should be filtered out, got: %s", out)
	}
}

func TestValidateAndNormalizeBlocksJSON_RejectsOversized(t *testing.T) {
	big := strings.Repeat("x", maxPromptBuilderBlocksJSONSize+1)
	if _, err := validateAndNormalizePromptBuilderBlocksJSON(big); err == nil {
		t.Fatal("expected oversized payload to be rejected")
	}
}

func TestValidateAndNormalizeBlocksJSON_RejectsAllUnknownBlocks(t *testing.T) {
	raw := `[{"type":"totally_invalid","data":{}}]`
	if _, err := validateAndNormalizePromptBuilderBlocksJSON(raw); err == nil {
		t.Fatal("expected error when no valid blocks remain")
	}
}

func TestValidateAndNormalizeBlocksJSON_InvalidJSON(t *testing.T) {
	if _, err := validateAndNormalizePromptBuilderBlocksJSON("not-json"); err == nil {
		t.Fatal("expected error for invalid JSON input")
	}
}

func TestSanitizePromptBuilderParsedResult_DefaultsFreeformMode(t *testing.T) {
	parsed := map[string]interface{}{
		"blocks": []interface{}{
			map[string]interface{}{
				"type": "subject",
				"data": map[string]interface{}{
					"characters": []interface{}{map[string]interface{}{"name": "a"}},
				},
			},
		},
	}
	out, err := sanitizePromptBuilderParsedResult(parsed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["mode"] != "freeform" {
		t.Fatalf("expected mode to default to freeform, got %v", out["mode"])
	}
}

func TestSanitizePromptBuilderParsedResult_RejectsEmpty(t *testing.T) {
	if _, err := sanitizePromptBuilderParsedResult(nil); err == nil {
		t.Fatal("expected nil parsed to error")
	}
	if _, err := sanitizePromptBuilderParsedResult(map[string]interface{}{"blocks": []interface{}{}}); err == nil {
		t.Fatal("expected empty blocks to error")
	}
}

func TestSanitizeBlockData_NumberClampingAndDefaultSkip(t *testing.T) {
	// intensity=50 is the semantic default and should be stripped.
	raw := map[string]interface{}{
		"intensity": 50,
	}
	sanitized := sanitizePromptBuilderBlockData("sensory", raw)
	if _, exists := sanitized["intensity"]; exists {
		t.Fatalf("default intensity (50) should be dropped, got %#v", sanitized)
	}

	// Out-of-range clamps to [0,100].
	raw = map[string]interface{}{"intensity": 250}
	sanitized = sanitizePromptBuilderBlockData("sensory", raw)
	if v, ok := sanitized["intensity"].(float64); !ok || v != 100 {
		t.Fatalf("expected intensity clamped to 100, got %#v", sanitized["intensity"])
	}
}

func TestSanitizeBlockData_BooleanFalseDropped(t *testing.T) {
	raw := map[string]interface{}{"single_subject": false}
	sanitized := sanitizePromptBuilderBlockData("subject", raw)
	if _, exists := sanitized["single_subject"]; exists {
		t.Fatalf("false boolean should be dropped, got %#v", sanitized)
	}
	raw = map[string]interface{}{"single_subject": true}
	sanitized = sanitizePromptBuilderBlockData("subject", raw)
	if sanitized["single_subject"] != true {
		t.Fatalf("true boolean should be kept, got %#v", sanitized)
	}
}

func TestSanitizeBlockData_WeightClamping(t *testing.T) {
	// Default weight 1.0 is dropped.
	raw := map[string]interface{}{"weight": 1.0}
	sanitized := sanitizePromptBuilderBlockData("style", raw)
	if _, exists := sanitized["weight"]; exists {
		t.Fatalf("default weight (1.0) should be dropped, got %#v", sanitized)
	}

	// Non-default weight is preserved and clamped.
	raw = map[string]interface{}{"weight": 5.0}
	sanitized = sanitizePromptBuilderBlockData("style", raw)
	if v, ok := sanitized["weight"].(float64); !ok || v != 2.0 {
		t.Fatalf("expected weight clamped to 2.0, got %#v", sanitized["weight"])
	}
}

func TestSanitizeBlockData_CamelCaseAlias(t *testing.T) {
	// Frontend sends camelCase; backend expects snake_case after aliasing.
	raw := map[string]interface{}{"artStyle": []interface{}{"anime", "watercolor"}}
	sanitized := sanitizePromptBuilderBlockData("style", raw)
	got, ok := sanitized["art_style"].([]string)
	if !ok || len(got) != 2 {
		t.Fatalf("expected art_style from camelCase alias, got %#v", sanitized["art_style"])
	}
}

func TestNormalizePromptBuilderBoolean(t *testing.T) {
	cases := []struct {
		in   interface{}
		want bool
		ok   bool
	}{
		{true, true, true},
		{false, false, true},
		{"true", true, true},
		{"YES", true, true},
		{"off", false, true},
		{"bogus", false, false},
		{1, true, true},
		{0, false, true},
		{nil, false, false},
	}
	for _, tc := range cases {
		got, ok := normalizePromptBuilderBoolean(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("normalizeBool(%v): got (%v,%v) want (%v,%v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestNormalizePromptBuilderNumber(t *testing.T) {
	cases := []struct {
		in   interface{}
		want float64
		ok   bool
	}{
		{1.5, 1.5, true},
		{2, 2.0, true},
		{int64(3), 3.0, true},
		{"4.25", 4.25, true},
		{"   ", 0, false},
		{"nope", 0, false},
		{nil, 0, false},
		{json.Number("7.5"), 7.5, true},
	}
	for _, tc := range cases {
		got, ok := normalizePromptBuilderNumber(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("normalizeNumber(%v): got (%v,%v) want (%v,%v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestClampPromptBuilderNumber(t *testing.T) {
	if got := clampPromptBuilderNumber(-1, 0, 10); got != 0 {
		t.Fatalf("expected 0, got %v", got)
	}
	if got := clampPromptBuilderNumber(11, 0, 10); got != 10 {
		t.Fatalf("expected 10, got %v", got)
	}
	if got := clampPromptBuilderNumber(5, 0, 10); got != 5 {
		t.Fatalf("expected 5, got %v", got)
	}
}

func TestNormalizePromptBuilderStringArray(t *testing.T) {
	// slice of interfaces (typical JSON decode)
	got := normalizePromptBuilderStringArray([]interface{}{"a", " b ", "", "c"})
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("unexpected slice result: %#v", got)
	}
	// comma-separated string
	got = normalizePromptBuilderStringArray("foo, bar ,,baz")
	if len(got) != 3 || got[0] != "foo" || got[1] != "bar" || got[2] != "baz" {
		t.Fatalf("unexpected string-split result: %#v", got)
	}
	// unsupported type falls back to empty slice
	if got := normalizePromptBuilderStringArray(42); len(got) != 0 {
		t.Fatalf("expected empty slice for int input, got %#v", got)
	}
}

func TestNormalizePromptBuilderText(t *testing.T) {
	if text, ok := normalizePromptBuilderText("  hello  "); !ok || text != "hello" {
		t.Fatalf("expected trimmed hello, got (%q,%v)", text, ok)
	}
	if _, ok := normalizePromptBuilderText("   "); ok {
		t.Fatal("whitespace-only should not be accepted")
	}
	if _, ok := normalizePromptBuilderText(123); ok {
		t.Fatal("non-string/non-Stringer should be rejected")
	}
}

func TestPickPromptBuilderFieldValue_UsesAlias(t *testing.T) {
	raw := map[string]interface{}{"artStyle": []interface{}{"anime"}}
	value, ok := pickPromptBuilderFieldValue(raw, "art_style")
	if !ok {
		t.Fatal("expected alias lookup to succeed")
	}
	arr, _ := value.([]interface{})
	if len(arr) != 1 {
		t.Fatalf("expected aliased array to carry through, got %#v", value)
	}
}

func TestSanitizeStyleFusionBlend_KeepsKnownKeysOnly(t *testing.T) {
	value := map[string]interface{}{
		"style1":   "anime",
		"style2":   "watercolor",
		"weight":   75,
		"ignored":  "drop-me",
	}
	out, ok := sanitizePromptBuilderObjectField("style_fusion_blend", value)
	if !ok {
		t.Fatal("expected style_fusion_blend to be accepted")
	}
	if out["style1"] != "anime" || out["style2"] != "watercolor" {
		t.Fatalf("expected style1/style2 preserved, got %#v", out)
	}
	if _, exists := out["ignored"]; exists {
		t.Fatalf("unknown key should be dropped, got %#v", out)
	}
	if w, _ := out["weight"].(float64); w != 75 {
		t.Fatalf("expected weight=75, got %#v", out["weight"])
	}
}

func TestSanitizeUploadedImages_AcceptsStringOrObject(t *testing.T) {
	value := []interface{}{
		"https://example.com/a.jpg",
		map[string]interface{}{
			"id":   "img-1",
			"url":  "https://example.com/b.jpg",
			"name": "photo",
		},
	}
	out, ok := sanitizePromptBuilderObjectArrayField("uploaded_images", value)
	if !ok || len(out) != 2 {
		t.Fatalf("expected 2 normalized entries, got %#v", out)
	}
	if out[0]["url"] != "https://example.com/a.jpg" {
		t.Fatalf("expected string entry to keep url, got %#v", out[0])
	}
	if out[1]["id"] != "img-1" || out[1]["name"] != "photo" {
		t.Fatalf("expected object entry fields preserved, got %#v", out[1])
	}
}
