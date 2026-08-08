package tools

import (
	"encoding/json"
	"server/globals"
	"strings"
	"testing"
)

const testVideoPromptUID = uint(42)

func configureVideoPromptAssetHostForTest() {
	globals.GraConf.System.FrontendURL = "https://app.example.com"
}

func minimalVideoPromptIRJSON(overrides string) string {
	base := `{
		"meta":{"title":"Demo","durationSec":8,"aspectRatio":"9:16","resolution":"1080p","productionLevel":"professional"},
		"subject":{"entities":[],"consistency":{"lockAppearance":false}},
		"action":{"beats":[],"intensity":"subtle"},
		"environment":{"setting":"studio","timeOfDay":"day","atmosphere":"clean"},
		"cinematography":{"shotType":"medium","angle":"eye-level","movement":"static","lighting":{"type":"studio","quality":"soft","direction":"key-front"}},
		"style":{"preset":"cinematic-realism"}
	}`
	if strings.TrimSpace(overrides) == "" {
		return base
	}
	return strings.TrimSuffix(base, "}") + "," + overrides + "}"
}

func TestValidateAndNormalizeVideoPromptIRJSONNormalizesTargetAndTimelineAssets(t *testing.T) {
	configureVideoPromptAssetHostForTest()
	raw := minimalVideoPromptIRJSON(`"timeline":[{
		"start":0,
		"end":99,
		"content":"hero reveal",
		"assets":[{"id":" asset_1 ","kind":"image","role":"first-frame","uri":" https://app.example.com/uploads/references/uid/42/reference.png "}]
	}]`)

	normalized, err := validateAndNormalizeVideoPromptIRJSON(raw, "seedance-2", testVideoPromptUID)
	if err != nil {
		t.Fatalf("expected valid IR, got error: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal([]byte(normalized), &out); err != nil {
		t.Fatalf("normalized IR is invalid JSON: %v", err)
	}
	meta := out["meta"].(map[string]any)
	if meta["targetModel"] != "seedance-2" {
		t.Fatalf("expected targetModel to match adapter, got %#v", meta["targetModel"])
	}
	timeline := out["timeline"].([]any)
	segment := timeline[0].(map[string]any)
	if segment["end"] != float64(8) {
		t.Fatalf("expected timeline end to clamp to duration, got %#v", segment["end"])
	}
	assets := segment["assets"].([]any)
	asset := assets[0].(map[string]any)
	if asset["id"] != "asset_1" || asset["uri"] != "https://app.example.com/uploads/references/uid/42/reference.png" {
		t.Fatalf("expected normalized timeline asset, got %#v", asset)
	}
}

func TestValidateAndNormalizeVideoPromptIRJSONAllowsIncompleteDraftText(t *testing.T) {
	raw := minimalVideoPromptIRJSON(`"subject":{"entities":[{"id":"char_1","descriptor":"   "}],"consistency":{"lockAppearance":false}},
		"action":{"beats":[{"id":"beat_1","description":"   "}],"intensity":"subtle"},
		"environment":{"setting":"","timeOfDay":"day","atmosphere":" "},
		"audio":{"dialogue":[{"speaker":"","text":"  "}]},
		"timeline":[{"start":0,"end":1,"content":""}]`)

	if _, err := validateAndNormalizeVideoPromptIRJSON(raw, "seedance-2", testVideoPromptUID); err != nil {
		t.Fatalf("expected incomplete draft text to be valid, got error: %v", err)
	}
}

func TestValidateAndNormalizeVideoPromptIRJSONDropsLegacyEditMode(t *testing.T) {
	raw := minimalVideoPromptIRJSON(`"edit":{
		"baseClipId":" clip_123 ",
		"operation":"partial-edit",
		"keepAspects":["style","audio","style",""],
		"details":" replace the jacket "
	}`)

	normalized, err := validateAndNormalizeVideoPromptIRJSON(raw, "seedance-2", testVideoPromptUID)
	if err != nil {
		t.Fatalf("expected edit mode to be valid, got error: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal([]byte(normalized), &out); err != nil {
		t.Fatalf("normalized IR is invalid JSON: %v", err)
	}
	if _, exists := out["edit"]; exists {
		t.Fatalf("expected legacy edit mode to be dropped, got %#v", out["edit"])
	}
}

func TestValidateAndNormalizeVideoPromptIRJSONRejectsUnsafeTimelineAsset(t *testing.T) {
	configureVideoPromptAssetHostForTest()
	raw := minimalVideoPromptIRJSON(`"timeline":[{
		"start":0,
		"end":1,
		"content":"hero reveal",
		"assets":[{"id":"asset_1","kind":"image","role":"first-frame","uri":"javascript:alert(1)"}]
	}]`)

	if _, err := validateAndNormalizeVideoPromptIRJSON(raw, "seedance-2", testVideoPromptUID); err == nil {
		t.Fatal("expected unsafe timeline asset URI to be rejected")
	}
}

func TestValidateAndNormalizeVideoPromptIRJSONRejectsInvalidAssetKind(t *testing.T) {
	configureVideoPromptAssetHostForTest()
	raw := minimalVideoPromptIRJSON(`"assets":[{"id":"asset_1","kind":"iframe","role":"first-frame","uri":"https://app.example.com/uploads/generations/reference-images/42/reference.png"}]`)

	if _, err := validateAndNormalizeVideoPromptIRJSON(raw, "seedance-2", testVideoPromptUID); err == nil {
		t.Fatal("expected invalid asset kind to be rejected")
	}
}

func TestValidateAndNormalizeVideoPromptIRJSONRejectsExternalAssetHost(t *testing.T) {
	configureVideoPromptAssetHostForTest()
	raw := minimalVideoPromptIRJSON(`"assets":[{"id":"asset_1","kind":"image","role":"first-frame","uri":"https://evil.example.com/uploads/generations/reference-images/42/reference.png"}]`)

	if _, err := validateAndNormalizeVideoPromptIRJSON(raw, "seedance-2", testVideoPromptUID); err == nil {
		t.Fatal("expected external asset host to be rejected")
	}
}
