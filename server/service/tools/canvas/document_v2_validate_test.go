package canvas

import (
	"strings"
	"testing"

	"server/model"
)

func baseV2Doc() model.JSONMap {
	return model.JSONMap{
		"schemaVersion": float64(2),
		"viewMode":      string(ViewModeFreeform),
		"elements":      []interface{}{},
		"viewport": map[string]interface{}{
			"x":     float64(0),
			"y":     float64(0),
			"scale": float64(1),
		},
	}
}

func TestValidateV2_AcceptsMinimalDocument(t *testing.T) {
	if err := ValidateCanvasDocumentV2(baseV2Doc()); err != nil {
		t.Fatalf("minimal v2 doc should validate, got %v", err)
	}
}

func TestValidateV2_AcceptsPersistedUIState(t *testing.T) {
	doc := baseV2Doc()
	doc["selectedIds"] = []interface{}{"el-1"}
	doc["settings"] = map[string]interface{}{
		"activeMode":       "agent",
		"showGrid":         true,
		"imageModelPool":   []interface{}{"nanobanana-2"},
		"canvasBackground": "#0a0a1a",
	}
	if err := ValidateCanvasDocumentV2(doc); err != nil {
		t.Fatalf("persisted UI state should pass, got %v", err)
	}
}

func TestValidateV2_RejectsInvalidPersistedUIState(t *testing.T) {
	t.Run("selectedIds must be strings", func(t *testing.T) {
		doc := baseV2Doc()
		doc["selectedIds"] = []interface{}{float64(1)}
		err := ValidateCanvasDocumentV2(doc)
		if err == nil || !strings.Contains(err.Error(), "selectedIds[0]") {
			t.Fatalf("expected selectedIds error, got %v", err)
		}
	})

	t.Run("settings must be object", func(t *testing.T) {
		doc := baseV2Doc()
		doc["settings"] = "bad"
		err := ValidateCanvasDocumentV2(doc)
		if err == nil || !strings.Contains(err.Error(), "settings must be an object") {
			t.Fatalf("expected settings error, got %v", err)
		}
	})
}

func TestValidateV2_RejectsWrongSchemaVersion(t *testing.T) {
	doc := baseV2Doc()
	doc["schemaVersion"] = float64(1)
	err := ValidateCanvasDocumentV2(doc)
	if err == nil {
		t.Fatal("expected rejection for schemaVersion=1")
	}
	se, ok := IsSchemaError(err)
	if !ok || se.Code != ErrCodeSchemaInvalid {
		t.Fatalf("expected ErrCodeSchemaInvalid, got %v", err)
	}
}

func TestValidateV2_RejectsUnsupportedViewMode(t *testing.T) {
	doc := baseV2Doc()
	doc["viewMode"] = "gallery"
	err := ValidateCanvasDocumentV2(doc)
	if err == nil {
		t.Fatal("expected rejection for unknown viewMode")
	}
	if !strings.Contains(err.Error(), "gallery") {
		t.Fatalf("error should mention bad viewMode, got %q", err.Error())
	}
}

func TestValidateV2_RejectsElementWithoutTypeOrKind(t *testing.T) {
	doc := baseV2Doc()
	doc["elements"] = []interface{}{
		map[string]interface{}{"id": "bad"},
	}
	err := ValidateCanvasDocumentV2(doc)
	if err == nil {
		t.Fatal("element missing type/kind should be rejected")
	}
	if !strings.Contains(err.Error(), "type or kind") {
		t.Fatalf("unexpected error: %q", err.Error())
	}
}

func TestValidateV2_AcceptsV1ElementShape(t *testing.T) {
	doc := baseV2Doc()
	doc["elements"] = []interface{}{
		map[string]interface{}{
			"id":   "el-1",
			"type": "shape",
			"x":    float64(10),
			"y":    float64(20),
		},
	}
	if err := ValidateCanvasDocumentV2(doc); err != nil {
		t.Fatalf("v1-shape element should pass (§8.3 compat), got %v", err)
	}
}

func TestValidateV2_RejectsBadShotLink(t *testing.T) {
	doc := baseV2Doc()
	doc["shots"] = []interface{}{
		map[string]interface{}{
			"id":          "shot-1",
			"localCardId": "card-1",
			// shotId missing → must be a number
			"orderIndex": float64(0),
		},
	}
	err := ValidateCanvasDocumentV2(doc)
	if err == nil {
		t.Fatal("shot without shotId should be rejected")
	}
}

func TestValidateV2_AcceptsFullShotLink(t *testing.T) {
	doc := baseV2Doc()
	doc["shots"] = []interface{}{
		map[string]interface{}{
			"id":          "shot-1",
			"typeName":    string(WorkMaxTypeShot),
			"shotId":      float64(1001),
			"localCardId": "card-1",
			"orderIndex":  float64(0),
		},
	}
	if err := ValidateCanvasDocumentV2(doc); err != nil {
		t.Fatalf("full shot should pass, got %v", err)
	}
}

func TestValidateV2_RejectsBadTimelineTrackKind(t *testing.T) {
	doc := baseV2Doc()
	doc["timeline"] = map[string]interface{}{
		"fps":           float64(30),
		"totalDuration": float64(60000),
		"tracks": []interface{}{
			map[string]interface{}{
				"id":    "track-1",
				"kind":  "hologram",
				"clips": []interface{}{},
			},
		},
	}
	err := ValidateCanvasDocumentV2(doc)
	if err == nil {
		t.Fatal("unknown track kind should be rejected")
	}
}

func TestValidateV2_AcceptsValidTimeline(t *testing.T) {
	doc := baseV2Doc()
	doc["timeline"] = map[string]interface{}{
		"fps":           float64(30),
		"totalDuration": float64(60000),
		"tracks": []interface{}{
			map[string]interface{}{
				"id":    "track-1",
				"kind":  string(TimelineTrackVideo),
				"clips": []interface{}{},
			},
		},
	}
	if err := ValidateCanvasDocumentV2(doc); err != nil {
		t.Fatalf("valid timeline should pass, got %v", err)
	}
}

func TestValidateV2_RejectsAssetBindingBadScope(t *testing.T) {
	doc := baseV2Doc()
	doc["projectAssetBindings"] = map[string]interface{}{
		"scope":        "global",
		"characterIds": []interface{}{float64(1)},
	}
	err := ValidateCanvasDocumentV2(doc)
	if err == nil {
		t.Fatal("asset binding with unknown scope should be rejected")
	}
}

func TestValidateV2_AcceptsAssetBinding(t *testing.T) {
	doc := baseV2Doc()
	doc["projectAssetBindings"] = map[string]interface{}{
		"scope":        string(AssetScopeProject),
		"characterIds": []interface{}{float64(1), float64(2)},
		"brandIds":     []interface{}{float64(10)},
	}
	if err := ValidateCanvasDocumentV2(doc); err != nil {
		t.Fatalf("valid asset binding should pass, got %v", err)
	}
}

func TestValidateV2_RejectsMissingViewport(t *testing.T) {
	doc := baseV2Doc()
	delete(doc, "viewport")
	err := ValidateCanvasDocumentV2(doc)
	if err == nil {
		t.Fatal("missing viewport should be rejected")
	}
}

func TestValidateV2_RejectsNil(t *testing.T) {
	err := ValidateCanvasDocumentV2(nil)
	if err == nil {
		t.Fatal("nil doc should be rejected")
	}
}
