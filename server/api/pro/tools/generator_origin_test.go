package tools

import (
	"encoding/json"
	"server/model"
	"testing"
)

// Origin plumbing for canvas × video-generator 合流.
//
// The pipeline is: submitTaskParams.Origin → requestData["origin"]
// → GenerationTask.request_data → TaskProcessor lifts onto
// GenerateImageRequest.Origin → saveGenerationRecord stamps
// w_generation_record.origin.
//
// These tests cover the pure helper at the head of the pipeline and
// the on-wire TaskRequestData round-trip that keeps origin alive
// across the queue boundary.

func TestResolveTaskItemOrigin_PrefersItemOverBatch(t *testing.T) {
	got := resolveTaskItemOrigin(model.GENERATION_ORIGIN_CANVAS, model.GENERATION_ORIGIN_VIDEO_GEN, nil)
	if got != model.GENERATION_ORIGIN_CANVAS {
		t.Fatalf("expected item-level origin to win, got %q", got)
	}
}

func TestResolveTaskItemOrigin_FallsBackToBatch(t *testing.T) {
	got := resolveTaskItemOrigin("", model.GENERATION_ORIGIN_VIDEO_GEN, nil)
	if got != model.GENERATION_ORIGIN_VIDEO_GEN {
		t.Fatalf("expected batch-level origin as fallback, got %q", got)
	}
}

func TestResolveTaskItemOrigin_FallsBackToRequestData(t *testing.T) {
	// Retry path: the submit site doesn't stamp Origin, but the cloned
	// request_data from the original task carries it — we must preserve it.
	rd := model.JSONMap{"origin": model.GENERATION_ORIGIN_CANVAS}
	got := resolveTaskItemOrigin("", "", rd)
	if got != model.GENERATION_ORIGIN_CANVAS {
		t.Fatalf("expected retry to preserve origin from requestData, got %q", got)
	}
}

func TestResolveTaskItemOrigin_TrimsWhitespace(t *testing.T) {
	got := resolveTaskItemOrigin("  canvas  ", "", nil)
	if got != "canvas" {
		t.Fatalf("expected whitespace-trimmed origin, got %q", got)
	}
}

func TestResolveTaskItemOrigin_NoOriginAnywhere(t *testing.T) {
	if got := resolveTaskItemOrigin("", "", nil); got != "" {
		t.Fatalf("expected empty origin when nothing is set, got %q", got)
	}
	if got := resolveTaskItemOrigin("", "", model.JSONMap{"origin": "   "}); got != "" {
		t.Fatalf("expected empty origin for whitespace-only requestData origin, got %q", got)
	}
}

func TestTaskRequestData_OriginRoundTripsThroughJSON(t *testing.T) {
	// TaskRequestData is what the queue persists + hands to TaskProcessor.
	// If origin didn't survive JSON marshalling, every downstream consumer
	// would have to read the raw map — regress detector.
	src := model.TaskRequestData{
		Model:  model.SEEDANCE_2,
		Prompt: "a cat walking on the moon",
		Origin: model.GENERATION_ORIGIN_VIDEO_GEN,
	}
	buf, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var got model.TaskRequestData
	if err := json.Unmarshal(buf, &got); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if got.Origin != model.GENERATION_ORIGIN_VIDEO_GEN {
		t.Fatalf("expected origin to round-trip, got %q", got.Origin)
	}
}

func TestGenerationOriginConstants_StableValues(t *testing.T) {
	// The string values land in w_generation_record.origin and are
	// expected to be stable — downstream analytics queries filter
	// on them. Flag any accidental rename.
	cases := map[string]string{
		"canvas":    model.GENERATION_ORIGIN_CANVAS,
		"video-gen": model.GENERATION_ORIGIN_VIDEO_GEN,
		"image-gen": model.GENERATION_ORIGIN_IMAGE_GEN,
	}
	for want, got := range cases {
		if got != want {
			t.Fatalf("expected origin constant %q, got %q", want, got)
		}
	}
}
