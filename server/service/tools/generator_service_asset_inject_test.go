package tools

import (
	"testing"

	"server/model"
	"server/service/tools/canvas"
)

// generator_service_asset_inject_test.go covers the merge helper that
// stitches canvas.InjectAssetContext output into the provider request.
// Injected references must come first (so clamp-by-count providers
// still see the character sheet) and must dedupe by URL against the
// user's explicit list.

func TestMergeInjectedReferenceImages_EmptyInjectedKeepsUser(t *testing.T) {
	user := []model.ReferenceImageParam{
		{ID: "user-1", URL: "https://cdn/u1.png", Weight: 0.5},
	}
	got := mergeInjectedReferenceImages(user, nil)
	if len(got) != 1 || got[0].ID != "user-1" {
		t.Fatalf("empty injected should pass user through, got %+v", got)
	}
}

func TestMergeInjectedReferenceImages_InjectedPrepended(t *testing.T) {
	user := []model.ReferenceImageParam{
		{ID: "user-1", URL: "https://cdn/u1.png", Weight: 0.5},
	}
	injected := []canvas.ReferenceImage{
		{URL: "https://cdn/jane.png", Label: "character:jane", Weight: 1},
	}
	got := mergeInjectedReferenceImages(user, injected)
	if len(got) != 2 {
		t.Fatalf("expected 2 merged refs, got %d", len(got))
	}
	if got[0].URL != "https://cdn/jane.png" {
		t.Fatalf("injected must be first, got %+v", got)
	}
	if got[0].ID != "character:jane" || got[0].Weight != 1 {
		t.Fatalf("injected fields not preserved: %+v", got[0])
	}
	if got[1].ID != "user-1" {
		t.Fatalf("user ref must follow, got %+v", got[1])
	}
}

func TestMergeInjectedReferenceImages_DedupesByURL(t *testing.T) {
	user := []model.ReferenceImageParam{
		{ID: "user-1", URL: "https://cdn/jane.png", Weight: 0.8},
	}
	injected := []canvas.ReferenceImage{
		{URL: "https://cdn/jane.png", Label: "character:jane", Weight: 1},
	}
	got := mergeInjectedReferenceImages(user, injected)
	if len(got) != 1 {
		t.Fatalf("expected 1 deduped ref, got %d: %+v", len(got), got)
	}
	if got[0].ID != "character:jane" {
		t.Fatalf("injected must win on URL collision, got %+v", got[0])
	}
}

func TestMergeInjectedReferenceImages_DropsEmptyInjectedURL(t *testing.T) {
	injected := []canvas.ReferenceImage{
		{URL: "   ", Label: "bogus", Weight: 1},
		{URL: "https://cdn/a.png", Label: "character:a", Weight: 1},
	}
	got := mergeInjectedReferenceImages(nil, injected)
	if len(got) != 1 || got[0].URL != "https://cdn/a.png" {
		t.Fatalf("empty-URL injected entry must be skipped, got %+v", got)
	}
}

func TestMergeInjectedReferenceImages_ZeroWeightBecomesOne(t *testing.T) {
	injected := []canvas.ReferenceImage{
		{URL: "https://cdn/a.png", Label: "character:a", Weight: 0},
	}
	got := mergeInjectedReferenceImages(nil, injected)
	if len(got) != 1 || got[0].Weight != 1 {
		t.Fatalf("zero weight should default to 1, got %+v", got)
	}
}
