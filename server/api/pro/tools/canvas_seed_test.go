package tools

import (
	"server/model"
	"testing"
)

func ptrInt64(v int64) *int64 { return &v }

func TestApplyOptionalCanvasSeed_NilLeavesRequestUntouched(t *testing.T) {
	// Baseline: a caller who never touched the seed field shouldn't
	// end up writing any "seed" key into the downstream request.
	req := model.JSONMap{"strength": 0.75}
	applyOptionalCanvasSeed(req, nil)
	if _, ok := req["seed"]; ok {
		t.Fatalf("nil seed should not inject a seed key, got %v", req)
	}
}

func TestApplyOptionalCanvasSeed_NegativeSeedIsIgnored(t *testing.T) {
	// Defensive: a negative value signals an unintended/garbage value
	// (e.g. missing field coerced from JSON) and should be dropped
	// rather than passed through to the provider.
	req := model.JSONMap{}
	applyOptionalCanvasSeed(req, ptrInt64(-1))
	if _, ok := req["seed"]; ok {
		t.Fatalf("negative seed should be ignored, got %v", req)
	}
}

func TestApplyOptionalCanvasSeed_ZeroIsPreserved(t *testing.T) {
	// Locks in the "seed=0 is a valid intent" semantic shared with
	// the frontend manifest-persistence + retry-plan tests. Dropping
	// zero here would silently fall back to a provider-chosen seed
	// and break deterministic retries.
	req := model.JSONMap{}
	applyOptionalCanvasSeed(req, ptrInt64(0))
	got, ok := req["seed"]
	if !ok {
		t.Fatalf("seed=0 should survive propagation, got %v", req)
	}
	if got != int64(0) {
		t.Fatalf("expected seed=0, got %T %v", got, got)
	}
}

func TestApplyOptionalCanvasSeed_PositiveSeedPassesThrough(t *testing.T) {
	req := model.JSONMap{}
	applyOptionalCanvasSeed(req, ptrInt64(42))
	got, ok := req["seed"]
	if !ok {
		t.Fatalf("positive seed should be written, got %v", req)
	}
	if got != int64(42) {
		t.Fatalf("expected seed=42, got %T %v", got, got)
	}
}

func TestApplyOptionalCanvasSeed_OverwritesExistingSeedKey(t *testing.T) {
	// Defensive: if an earlier helper stashed a placeholder "seed"
	// the caller's explicit value must win. Surfaces any accidental
	// split-brain where two helpers both write `seed`.
	req := model.JSONMap{"seed": int64(1)}
	applyOptionalCanvasSeed(req, ptrInt64(99))
	if req["seed"] != int64(99) {
		t.Fatalf("expected seed=99 to overwrite, got %v", req["seed"])
	}
}
