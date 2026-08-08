package tools

import (
	"encoding/json"
	"testing"
)

func TestSanitizePromptBuilderCharacters_PreservesExpandedSubjectFields(t *testing.T) {
	raw := []interface{}{
		map[string]interface{}{
			"id":          "char-1",
			"type":        "woman",
			"skin":        []interface{}{"fair skin"},
			"eyes":        []interface{}{"blue eyes"},
			"hair":        []interface{}{"long hair"},
			"expression":  []interface{}{"gentle smile"},
			"clothing":    []interface{}{"blazer"},
			"texture":     []interface{}{"wool"},
			"accessories": []interface{}{"glasses"},
			"makeup":      []interface{}{"natural makeup"},
			"description": "studio portrait",
		},
	}

	sanitized, ok := sanitizePromptBuilderObjectArrayField("characters", raw)
	if !ok {
		t.Fatal("expected characters payload to be accepted")
	}
	if len(sanitized) != 1 {
		t.Fatalf("expected 1 character, got %d", len(sanitized))
	}

	character := sanitized[0]
	for _, field := range []string{"skin", "eyes", "hair", "expression", "clothing", "texture", "accessories", "makeup"} {
		if _, exists := character[field]; !exists {
			t.Fatalf("expected field %q to be preserved, got %#v", field, character)
		}
	}
}

func TestValidateAndNormalizePromptBuilderBlocksJSON_PreservesEmptyBlocks(t *testing.T) {
	payload := map[string]interface{}{
		"schema_version": 2,
		"blocks": []interface{}{
			map[string]interface{}{
				"id":        "subject-1",
				"type":      "subject",
				"data":      map[string]interface{}{},
				"collapsed": false,
			},
			map[string]interface{}{
				"id":        "meta-1",
				"type":      "meta",
				"data":      map[string]interface{}{},
				"collapsed": true,
			},
		},
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	normalized, err := validateAndNormalizePromptBuilderBlocksJSON(string(raw))
	if err != nil {
		t.Fatalf("expected payload to remain valid, got error: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(normalized), &decoded); err != nil {
		t.Fatalf("unmarshal normalized payload: %v", err)
	}

	blocks, ok := decoded["blocks"].([]interface{})
	if !ok {
		t.Fatalf("expected normalized blocks array, got %#v", decoded["blocks"])
	}
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks to be preserved, got %d", len(blocks))
	}
}
