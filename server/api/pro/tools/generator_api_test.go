package tools

import (
	"server/model"
	"testing"
)

func TestNormalizeGenerateVideoPayload_NormalizesCanvasVideoParams(t *testing.T) {
	aspectRatio, resolution, params, errorCode, err := normalizeGenerateVideoPayload(
		model.VEO_31_FAST,
		"",
		"1080p",
		map[string]interface{}{
			"duration":    8.0,
			"resolution":  "1080p",
			"startFrame":  "https://example.com/start.png",
			"endFrameUrl": "https://example.com/end.png",
		},
		nil,
	)
	if err != nil {
		t.Fatalf("expected normalization success, got error: %v", err)
	}
	if errorCode != "" {
		t.Fatalf("expected empty errorCode, got %q", errorCode)
	}
	if aspectRatio != "16:9" {
		t.Fatalf("expected normalized aspectRatio 16:9, got %q", aspectRatio)
	}
	if resolution != "1080p" {
		t.Fatalf("expected normalized resolution 1080p, got %q", resolution)
	}
	if params["duration"] != "8" {
		t.Fatalf("expected numeric duration to be serialized as string 8, got %#v", params["duration"])
	}
	if params["startFrame"] != "https://example.com/start.png" {
		t.Fatalf("expected startFrame to be preserved, got %#v", params["startFrame"])
	}
	if params["endFrame"] != "https://example.com/end.png" {
		t.Fatalf("expected endFrame to be normalized from endFrameUrl, got %#v", params["endFrame"])
	}
	if params["motionMode"] != "" {
		t.Fatalf("expected unsupported Veo motionMode to be cleared, got %#v", params["motionMode"])
	}
	if params["generationMethod"] != "frames" {
		t.Fatalf("expected default generationMethod frames, got %#v", params["generationMethod"])
	}
}

func TestNormalizeGenerateVideoPayload_RejectsInvalidVeo1080pDuration(t *testing.T) {
	_, _, _, errorCode, err := normalizeGenerateVideoPayload(
		model.VEO_31_FAST,
		"16:9",
		"1080p",
		map[string]interface{}{
			"duration": 6,
		},
		nil,
	)
	if err == nil {
		t.Fatal("expected invalid duration error")
	}
	if err.Error() != "invalid duration" {
		t.Fatalf("expected invalid duration error, got %v", err)
	}
	if errorCode != "INVALID_VIDEO_DURATION" {
		t.Fatalf("expected INVALID_VIDEO_DURATION errorCode, got %q", errorCode)
	}
}

func TestNormalizeGenerateVideoPayload_RejectsInvalidGenerationMethod(t *testing.T) {
	_, _, _, errorCode, err := normalizeGenerateVideoPayload(
		model.KLING_2_6,
		"16:9",
		"",
		map[string]interface{}{
			"duration":         5,
			"generationMethod": "invalid",
		},
		nil,
	)
	if err == nil {
		t.Fatal("expected invalid generationMethod error")
	}
	if err.Error() != "invalid generationMethod" {
		t.Fatalf("expected invalid generationMethod error, got %v", err)
	}
	if errorCode != "INVALID_GENERATION_METHOD" {
		t.Fatalf("expected INVALID_GENERATION_METHOD errorCode, got %q", errorCode)
	}
}

func TestStripSplitRetryMetadata_StripsAggregationByDefault(t *testing.T) {
	requestData := model.JSONMap{
		"skipRecord":     true,
		"splitBatchId":   "batch-a",
		"aggregateMode":  "generic_aggregate_mode",
		"parentRecordId": 42,
		"params": model.JSONMap{
			"skipRecord":     true,
			"splitBatchId":   "batch-a",
			"aggregateMode":  "generic_aggregate_mode",
			"parentRecordId": 42,
		},
	}

	stripSplitRetryMetadata(requestData, false)

	if _, ok := requestData["skipRecord"]; ok {
		t.Fatalf("expected top-level skipRecord to be stripped")
	}
	if _, ok := requestData["aggregateMode"]; ok {
		t.Fatalf("expected top-level aggregateMode to be stripped")
	}
	if _, ok := requestData["parentRecordId"]; ok {
		t.Fatalf("expected top-level parentRecordId to be stripped")
	}

	params := requestData["params"].(model.JSONMap)
	if _, ok := params["skipRecord"]; ok {
		t.Fatalf("expected params.skipRecord to be stripped")
	}
	if _, ok := params["aggregateMode"]; ok {
		t.Fatalf("expected params.aggregateMode to be stripped")
	}
	if _, ok := params["parentRecordId"]; ok {
		t.Fatalf("expected params.parentRecordId to be stripped")
	}
}

func TestStripSplitRetryMetadata_PreservesAggregationWhenRequested(t *testing.T) {
	requestData := model.JSONMap{
		"skipRecord":     true,
		"splitBatchId":   "batch-a",
		"aggregateMode":  "generic_aggregate_mode",
		"parentRecordId": 42,
		"params": model.JSONMap{
			"skipRecord":     true,
			"splitBatchId":   "batch-a",
			"aggregateMode":  "generic_aggregate_mode",
			"parentRecordId": 42,
		},
	}

	stripSplitRetryMetadata(requestData, true)

	if _, ok := requestData["splitBatchId"]; ok {
		t.Fatalf("expected splitBatchId to be stripped")
	}
	if requestData["aggregateMode"] != "generic_aggregate_mode" {
		t.Fatalf("expected top-level aggregateMode to be preserved")
	}
	if requestData["parentRecordId"] != 42 {
		t.Fatalf("expected top-level parentRecordId to be preserved")
	}
	if requestData["skipRecord"] != true {
		t.Fatalf("expected top-level skipRecord to be preserved")
	}

	params := requestData["params"].(model.JSONMap)
	if _, ok := params["splitBatchId"]; ok {
		t.Fatalf("expected params.splitBatchId to be stripped")
	}
	if params["aggregateMode"] != "generic_aggregate_mode" {
		t.Fatalf("expected params.aggregateMode to be preserved")
	}
	if params["parentRecordId"] != 42 {
		t.Fatalf("expected params.parentRecordId to be preserved")
	}
	if params["skipRecord"] != true {
		t.Fatalf("expected params.skipRecord to be preserved")
	}
}

func TestGetSubmitTaskRequiredSlots_DefaultsToOne(t *testing.T) {
	if got := getSubmitTaskRequiredSlots(nil); got != 1 {
		t.Fatalf("expected 1 slot for nil items, got %d", got)
	}
}

func TestGetSubmitTaskRequiredSlots_CountsStandaloneItems(t *testing.T) {
	items := []submitTaskItem{
		{RequestData: model.JSONMap{"prompt": "one"}},
		{RequestData: model.JSONMap{"prompt": "two"}},
		{RequestData: model.JSONMap{"prompt": "three"}},
	}

	if got := getSubmitTaskRequiredSlots(items); got != 3 {
		t.Fatalf("expected 3 slots for standalone items, got %d", got)
	}
}

func TestGetSubmitTaskRequiredSlots_CollapsesSplitBatchItems(t *testing.T) {
	items := []submitTaskItem{
		{RequestData: model.JSONMap{"splitBatchId": "batch-a", "splitBatchIndex": 1}},
		{RequestData: model.JSONMap{"splitBatchId": "batch-a", "splitBatchIndex": 2}},
		{RequestData: model.JSONMap{"splitBatchId": "batch-a", "splitBatchIndex": 3}},
		{RequestData: model.JSONMap{"splitBatchId": "batch-a", "splitBatchIndex": 4}},
		{RequestData: model.JSONMap{"splitBatchId": "batch-a", "splitBatchIndex": 5}},
		{RequestData: model.JSONMap{"splitBatchId": "batch-a", "splitBatchIndex": 6}},
		{RequestData: model.JSONMap{"splitBatchId": "batch-a", "splitBatchIndex": 7}},
		{RequestData: model.JSONMap{"splitBatchId": "batch-a", "splitBatchIndex": 8}},
	}

	if got := getSubmitTaskRequiredSlots(items); got != 1 {
		t.Fatalf("expected 1 slot for split batch items, got %d", got)
	}
}

func TestGetSubmitTaskRequiredSlots_CountsMixedItems(t *testing.T) {
	items := []submitTaskItem{
		{RequestData: model.JSONMap{"splitBatchId": "batch-a", "splitBatchIndex": 1}},
		{RequestData: model.JSONMap{"splitBatchId": "batch-a", "splitBatchIndex": 2}},
		{RequestData: model.JSONMap{"splitBatchId": "batch-b", "splitBatchIndex": 1}},
		{RequestData: model.JSONMap{"prompt": "standalone"}},
	}

	if got := getSubmitTaskRequiredSlots(items); got != 3 {
		t.Fatalf("expected 3 slots for 2 batches + 1 standalone item, got %d", got)
	}
}
