package canvas

import (
	"testing"

	"server/globals"
	"server/model"
)

// asset_injector_test.go covers ApplyAssetContext — the pure merge.
// The DB-backed wrapper InjectAssetContext is exercised by the
// canvas integration tests where a gorm in-memory DB is available;
// keeping these tests pure avoids pulling GORM + sqlite here.

func mkChar(id int, slug, suffix, neg, avatar string) model.Character {
	c := model.Character{
		Slug:           slug,
		PromptSuffix:   suffix,
		NegativePrompt: neg,
		AvatarImageURL: avatar,
	}
	c.GraMODEL = globals.GraMODEL{Id: uint(id)}
	return c
}

func TestApplyAssetContext_NilBindingsReturnsBase(t *testing.T) {
	got := ApplyAssetContext("cat on a hat", "blurry", nil, nil)
	if got.EnrichedPrompt != "cat on a hat" {
		t.Fatalf("prompt mutated: %q", got.EnrichedPrompt)
	}
	if got.EnrichedNegativePrompt != "blurry" {
		t.Fatalf("negative mutated: %q", got.EnrichedNegativePrompt)
	}
	if len(got.ReferenceImages) != 0 {
		t.Fatalf("unexpected refs: %+v", got.ReferenceImages)
	}
}

func TestApplyAssetContext_EmptyBindingsTrimsBasePrompt(t *testing.T) {
	bindings := &AssetBinding{Scope: AssetScopeElement}
	got := ApplyAssetContext("  cat on a hat  ", "", bindings, nil)
	if got.EnrichedPrompt != "cat on a hat" {
		t.Fatalf("expected trimmed base, got %q", got.EnrichedPrompt)
	}
}

func TestApplyAssetContext_SingleCharacterAppendsSuffixAndRef(t *testing.T) {
	bindings := &AssetBinding{
		Scope:        AssetScopeElement,
		CharacterIDs: []int{42},
	}
	chars := []model.Character{
		mkChar(42, "linxia", "tall woman, black hair", "cartoon", "https://cdn/lx.png"),
	}
	got := ApplyAssetContext("runs in the rain", "", bindings, chars)
	want := "runs in the rain, tall woman, black hair"
	if got.EnrichedPrompt != want {
		t.Fatalf("prompt = %q, want %q", got.EnrichedPrompt, want)
	}
	if got.EnrichedNegativePrompt != "cartoon" {
		t.Fatalf("negative = %q", got.EnrichedNegativePrompt)
	}
	if len(got.ReferenceImages) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(got.ReferenceImages))
	}
	ref := got.ReferenceImages[0]
	if ref.URL != "https://cdn/lx.png" || ref.Label != "character:linxia" || ref.Weight != 1 {
		t.Fatalf("ref = %+v", ref)
	}
}

func TestApplyAssetContext_MultipleCharactersPreserveBindingOrder(t *testing.T) {
	bindings := &AssetBinding{
		Scope:        AssetScopeShot,
		CharacterIDs: []int{7, 3},
	}
	// Fetched order deliberately reversed vs bindings to prove the
	// injector follows bindings, not DB order.
	chars := []model.Character{
		mkChar(3, "max", "short man", "", "https://cdn/max.png"),
		mkChar(7, "jane", "tall woman", "", "https://cdn/jane.png"),
	}
	got := ApplyAssetContext("stand back-to-back", "", bindings, chars)
	want := "stand back-to-back, tall woman, short man"
	if got.EnrichedPrompt != want {
		t.Fatalf("prompt = %q, want %q", got.EnrichedPrompt, want)
	}
	if len(got.ReferenceImages) != 2 {
		t.Fatalf("expected 2 refs, got %d", len(got.ReferenceImages))
	}
	if got.ReferenceImages[0].Label != "character:jane" {
		t.Fatalf("first ref = %+v", got.ReferenceImages[0])
	}
	if got.ReferenceImages[1].Label != "character:max" {
		t.Fatalf("second ref = %+v", got.ReferenceImages[1])
	}
}

func TestApplyAssetContext_DedupsReferenceURLs(t *testing.T) {
	bindings := &AssetBinding{
		Scope:        AssetScopeElement,
		CharacterIDs: []int{1, 2},
	}
	shared := "https://cdn/shared.png"
	chars := []model.Character{
		mkChar(1, "a", "desc a", "", shared),
		mkChar(2, "b", "desc b", "", shared),
	}
	got := ApplyAssetContext("scene", "", bindings, chars)
	if len(got.ReferenceImages) != 1 {
		t.Fatalf("expected 1 deduped ref, got %d: %+v", len(got.ReferenceImages), got.ReferenceImages)
	}
	if got.ReferenceImages[0].Label != "character:a" {
		t.Fatalf("expected first-wins label, got %q", got.ReferenceImages[0].Label)
	}
}

func TestApplyAssetContext_MissingCharacterSilentlySkipped(t *testing.T) {
	bindings := &AssetBinding{
		Scope:        AssetScopeElement,
		CharacterIDs: []int{1, 999}, // 999 not in loaded set
	}
	chars := []model.Character{
		mkChar(1, "a", "desc a", "", ""),
	}
	got := ApplyAssetContext("scene", "", bindings, chars)
	if got.EnrichedPrompt != "scene, desc a" {
		t.Fatalf("missing char must not break merge, got %q", got.EnrichedPrompt)
	}
}

func TestApplyAssetContext_EmptyBasePromptGetsJustSuffix(t *testing.T) {
	bindings := &AssetBinding{
		Scope:        AssetScopeElement,
		CharacterIDs: []int{1},
	}
	chars := []model.Character{mkChar(1, "a", "tall woman", "", "")}
	got := ApplyAssetContext("", "", bindings, chars)
	if got.EnrichedPrompt != "tall woman" {
		t.Fatalf("empty base must not leave leading comma, got %q", got.EnrichedPrompt)
	}
}

func TestApplyAssetContext_CharacterWithNoSuffixOnlyAddsRef(t *testing.T) {
	bindings := &AssetBinding{
		Scope:        AssetScopeElement,
		CharacterIDs: []int{1},
	}
	chars := []model.Character{mkChar(1, "a", "", "", "https://cdn/a.png")}
	got := ApplyAssetContext("scene", "", bindings, chars)
	if got.EnrichedPrompt != "scene" {
		t.Fatalf("no suffix must leave prompt alone, got %q", got.EnrichedPrompt)
	}
	if len(got.ReferenceImages) != 1 {
		t.Fatalf("avatar must still produce a ref, got %d", len(got.ReferenceImages))
	}
}

func TestApplyAssetContext_WeightMapThreadsToReferenceImage(t *testing.T) {
	bindings := &AssetBinding{
		Scope:            AssetScopeElement,
		CharacterIDs:     []int{1, 2},
		CharacterWeights: model.AssetWeightMap{"1": 1.5, "2": 0.4},
	}
	chars := []model.Character{
		mkChar(1, "a", "desc a", "", "https://cdn/a.png"),
		mkChar(2, "b", "desc b", "", "https://cdn/b.png"),
	}
	got := ApplyAssetContext("scene", "", bindings, chars)
	if len(got.ReferenceImages) != 2 {
		t.Fatalf("expected 2 refs, got %d", len(got.ReferenceImages))
	}
	if got.ReferenceImages[0].Weight != 1.5 {
		t.Fatalf("first ref weight = %v, want 1.5", got.ReferenceImages[0].Weight)
	}
	if got.ReferenceImages[1].Weight != 0.4 {
		t.Fatalf("second ref weight = %v, want 0.4", got.ReferenceImages[1].Weight)
	}
}

func TestApplyAssetContext_WeightMapMissingKeysDefaultToOne(t *testing.T) {
	bindings := &AssetBinding{
		Scope:            AssetScopeElement,
		CharacterIDs:     []int{1, 2},
		CharacterWeights: model.AssetWeightMap{"1": 1.5}, // no entry for 2
	}
	chars := []model.Character{
		mkChar(1, "a", "desc a", "", "https://cdn/a.png"),
		mkChar(2, "b", "desc b", "", "https://cdn/b.png"),
	}
	got := ApplyAssetContext("scene", "", bindings, chars)
	if got.ReferenceImages[1].Weight != 1 {
		t.Fatalf("missing weight must default to 1, got %v", got.ReferenceImages[1].Weight)
	}
}

func TestWeightForClampsOutOfRange(t *testing.T) {
	weights := model.AssetWeightMap{"1": 5, "2": -3, "3": 1.2}
	if got := weightFor(weights, 1); got != assetWeightMax {
		t.Fatalf("clamp high: got %v, want %v", got, assetWeightMax)
	}
	if got := weightFor(weights, 2); got != assetWeightMin {
		t.Fatalf("clamp low: got %v, want %v", got, assetWeightMin)
	}
	if got := weightFor(weights, 3); got != 1.2 {
		t.Fatalf("in-range: got %v, want 1.2", got)
	}
	if got := weightFor(weights, 999); got != 1 {
		t.Fatalf("missing key: got %v, want 1", got)
	}
	if got := weightFor(nil, 1); got != 1 {
		t.Fatalf("nil map: got %v, want 1", got)
	}
}

func TestDedupIDs(t *testing.T) {
	got := dedupIDs([]int{3, 0, 3, 7, -1, 7, 1})
	want := []int{3, 7, 1}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (%+v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pos %d: got %d, want %d", i, got[i], want[i])
		}
	}
}
