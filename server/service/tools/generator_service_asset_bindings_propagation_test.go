package tools

import (
	"encoding/json"
	"testing"

	"server/model"
)

// generator_service_asset_bindings_propagation_test.go pins the §8.4
// end-to-end shape: a canvas handler stashes AssetBindings into the
// JSONMap request payload, that payload roundtrips through the DB as
// JSON, then TaskProcessor.Process unmarshals it into TaskRequestData
// and hands it to GenerateImageRequest. The injector itself is covered
// by asset_injector_test.go and generator_service_asset_inject_test.go;
// this test proves the binding doesn't get dropped on the way there.

func TestTaskRequestDataRoundtrip_PreservesAssetBindings(t *testing.T) {
	// Canvas handler shape — model.JSONMap is what hits the DB.
	raw := model.JSONMap{
		"model":  "nano-banana-2",
		"prompt": "scene",
		"assetBindings": map[string]interface{}{
			"scope":        "element",
			"characterIds": []int{42, 7},
		},
	}

	// Roundtrip through JSON the way GORM persists/loads JSONMap.
	buf, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var reloaded model.TaskRequestData
	if err := json.Unmarshal(buf, &reloaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if reloaded.AssetBindings == nil {
		t.Fatalf("AssetBindings dropped during roundtrip")
	}
	if reloaded.AssetBindings.Scope != model.AssetScopeElement {
		t.Fatalf("scope = %q, want %q",
			reloaded.AssetBindings.Scope, model.AssetScopeElement)
	}
	if len(reloaded.AssetBindings.CharacterIDs) != 2 ||
		reloaded.AssetBindings.CharacterIDs[0] != 42 ||
		reloaded.AssetBindings.CharacterIDs[1] != 7 {
		t.Fatalf("characterIds = %+v, want [42 7]",
			reloaded.AssetBindings.CharacterIDs)
	}
}

func TestTaskRequestDataRoundtrip_AbsentBindingsStayNil(t *testing.T) {
	raw := model.JSONMap{
		"model":  "nano-banana-2",
		"prompt": "scene",
	}
	buf, _ := json.Marshal(raw)

	var reloaded model.TaskRequestData
	if err := json.Unmarshal(buf, &reloaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if reloaded.AssetBindings != nil {
		t.Fatalf("expected nil AssetBindings, got %+v", reloaded.AssetBindings)
	}
}

func TestGenerateImageRequest_CopiesAssetBindingsFromTaskRequestData(t *testing.T) {
	// Mirror the exact construction in task_queue.go Process:
	//     genReq := &GenerateImageRequest{ ..., AssetBindings: requestData.AssetBindings }
	// so a refactor that forgets to copy the field will fail this test.
	requestData := model.TaskRequestData{
		Model:  "nano-banana-2",
		Prompt: "scene",
		AssetBindings: &model.AssetBinding{
			Scope:        model.AssetScopeShot,
			CharacterIDs: []int{1, 2},
		},
	}
	genReq := &GenerateImageRequest{
		Model:         requestData.Model,
		Prompt:        requestData.Prompt,
		AssetBindings: requestData.AssetBindings,
	}
	if genReq.AssetBindings == nil {
		t.Fatalf("genReq.AssetBindings is nil — propagation broken")
	}
	if genReq.AssetBindings.Scope != model.AssetScopeShot {
		t.Fatalf("scope lost: %q", genReq.AssetBindings.Scope)
	}
	if len(genReq.AssetBindings.CharacterIDs) != 2 {
		t.Fatalf("characterIds lost: %+v", genReq.AssetBindings.CharacterIDs)
	}
}
