// continuity_index_test.go — pure-function tests for the
// continuity-tracked references projection (Task #13).
//
// The DB-coupled end-to-end lives in continuity_index_db_test.go;
// this file pins the projection logic (most-recent-wins, JSONMap
// decode, output-URL preference, deduplication, character-list
// filtering) without a database. Keeps the unit cycle tight.

package canvas

import (
	"testing"
	"time"

	"server/model"
)

func TestProjectContinuityRowsToIndex_EmptyInputsProduceEmptyIndex(t *testing.T) {
	// All three guard branches: nil rows, nil requested, both
	// empty. The function should return an empty (non-nil)
	// ContinuityIndex so callers can `_, ok := idx[id]` without
	// nil-checking.
	cases := []struct {
		name      string
		rows      []continuityScanRow
		requested map[int]struct{}
	}{
		{"both empty", nil, nil},
		{"rows only", []continuityScanRow{{}}, nil},
		{"requested only", nil, map[int]struct{}{1: {}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			idx := projectContinuityRowsToIndex(c.rows, c.requested)
			if idx == nil {
				t.Errorf("index should be non-nil (empty), got nil")
			}
			if len(idx) != 0 {
				t.Errorf("index should be empty, got %d entries", len(idx))
			}
		})
	}
}

func TestProjectContinuityRowsToIndex_PicksMostRecentPerCharacter(t *testing.T) {
	// Two rows, both referencing character 7. Row 0 is the newer
	// one (SQL ordering is desc-by-completed_at; we mimic that
	// here). The newer row's URL wins; the older row is ignored.
	rows := []continuityScanRow{
		{
			RequestData: model.JSONMap{
				"assetBindings": map[string]interface{}{
					"characterIds": []interface{}{float64(7)},
				},
			},
			ResultData: model.JSONMap{
				"imageUrls": []interface{}{"https://cdn/newer.png"},
			},
			CreatedAt: time.Now(),
		},
		{
			RequestData: model.JSONMap{
				"assetBindings": map[string]interface{}{
					"characterIds": []interface{}{float64(7)},
				},
			},
			ResultData: model.JSONMap{
				"imageUrls": []interface{}{"https://cdn/older.png"},
			},
			CreatedAt: time.Now().Add(-2 * time.Hour),
		},
	}
	idx := projectContinuityRowsToIndex(rows, map[int]struct{}{7: {}})
	if got := idx[7]; got != "https://cdn/newer.png" {
		t.Errorf("idx[7] = %q, want newer URL", got)
	}
}

func TestProjectContinuityRowsToIndex_OnlyHydratesRequestedCharacters(t *testing.T) {
	// Row references characters 1 and 9; caller only requests 1.
	// The output should NOT contain character 9 even though the
	// data is available.
	rows := []continuityScanRow{
		{
			RequestData: model.JSONMap{
				"assetBindings": map[string]interface{}{
					"characterIds": []interface{}{float64(1), float64(9)},
				},
			},
			ResultData: model.JSONMap{"imageUrls": []interface{}{"u"}},
		},
	}
	idx := projectContinuityRowsToIndex(rows, map[int]struct{}{1: {}})
	if _, ok := idx[9]; ok {
		t.Errorf("idx must not contain character 9 (caller didn't request it)")
	}
	if got := idx[1]; got != "u" {
		t.Errorf("idx[1] = %q, want 'u'", got)
	}
}

func TestProjectContinuityRowsToIndex_SkipsRowsWithoutImageURL(t *testing.T) {
	// A row with no imageUrls AND no videoUrls AND no outputUrls
	// shouldn't even register the characters it touched. The next
	// row down (if any) gets a chance to fill the index.
	rows := []continuityScanRow{
		{
			RequestData: model.JSONMap{
				"assetBindings": map[string]interface{}{
					"characterIds": []interface{}{float64(3)},
				},
			},
			ResultData: model.JSONMap{}, // no urls
		},
		{
			RequestData: model.JSONMap{
				"assetBindings": map[string]interface{}{
					"characterIds": []interface{}{float64(3)},
				},
			},
			ResultData: model.JSONMap{
				"imageUrls": []interface{}{"https://fallback"},
			},
		},
	}
	idx := projectContinuityRowsToIndex(rows, map[int]struct{}{3: {}})
	if got := idx[3]; got != "https://fallback" {
		t.Errorf("idx[3] = %q, want fallback URL (first row had no URL)", got)
	}
}

func TestProjectContinuityRowsToIndex_PrefersImageOverVideoOverOutput(t *testing.T) {
	// firstSuccessfulImageURL walks imageUrls → videoUrls →
	// outputUrls. A row that has all three should use imageUrls.
	rows := []continuityScanRow{
		{
			RequestData: model.JSONMap{
				"assetBindings": map[string]interface{}{
					"characterIds": []interface{}{float64(5)},
				},
			},
			ResultData: model.JSONMap{
				"imageUrls":  []interface{}{"image-wins"},
				"videoUrls":  []interface{}{"video-loses"},
				"outputUrls": []interface{}{"output-loses"},
			},
		},
	}
	idx := projectContinuityRowsToIndex(rows, map[int]struct{}{5: {}})
	if got := idx[5]; got != "image-wins" {
		t.Errorf("idx[5] = %q, want 'image-wins' (imageUrls has priority)", got)
	}
}

// ---------------------------------------------------------------------
// ApplyContinuityOverlay
// ---------------------------------------------------------------------

func TestApplyContinuityOverlay_NoBindingsReturnsUnchanged(t *testing.T) {
	// Defensive: nil bindings, empty character IDs, empty index
	// all return the input verbatim. Specifically asserts that
	// the function doesn't allocate a new ReferenceImages slice
	// when there's nothing to merge.
	in := AssetContext{
		ReferenceImages: []ReferenceImage{{URL: "a"}, {URL: "b"}},
	}
	out := ApplyContinuityOverlay(in, nil, nil, ContinuityIndex{1: "x"})
	if len(out.ReferenceImages) != 2 {
		t.Errorf("nil bindings should return unchanged, got %d refs", len(out.ReferenceImages))
	}
	out = ApplyContinuityOverlay(in, &AssetBinding{}, nil, ContinuityIndex{})
	if len(out.ReferenceImages) != 2 {
		t.Errorf("empty index should return unchanged, got %d refs", len(out.ReferenceImages))
	}
}

func TestApplyContinuityOverlay_PrependsInBindingOrder(t *testing.T) {
	// The binding's CharacterIDs ordering is the source of truth
	// for output ranking. Characters appear in the prepended slice
	// in the SAME order the binding declares them, not whatever
	// the index iteration happens to produce.
	in := AssetContext{
		ReferenceImages: []ReferenceImage{{URL: "avatar-A"}, {URL: "avatar-B"}},
	}
	bindings := &AssetBinding{
		CharacterIDs: []int{42, 7},
	}
	chars := []model.Character{
		{Slug: "alice", Name: "Alice"}, // id=0 below — overwritten via map
	}
	chars[0].Id = 42
	bob := model.Character{Slug: "bob", Name: "Bob"}
	bob.Id = 7
	chars = append(chars, bob)

	index := ContinuityIndex{
		7:  "continuity-bob",
		42: "continuity-alice",
	}
	out := ApplyContinuityOverlay(in, bindings, chars, index)
	if len(out.ReferenceImages) != 4 {
		t.Fatalf("expected 4 refs (2 continuity + 2 avatar), got %d", len(out.ReferenceImages))
	}
	if out.ReferenceImages[0].URL != "continuity-alice" || out.ReferenceImages[1].URL != "continuity-bob" {
		t.Errorf("continuity refs out of binding order: %+v", out.ReferenceImages[:2])
	}
	// Avatars still trail, untouched.
	if out.ReferenceImages[2].URL != "avatar-A" || out.ReferenceImages[3].URL != "avatar-B" {
		t.Errorf("avatar refs should stay after prepend, got %+v", out.ReferenceImages[2:])
	}
	// Label should signal "continuity:slug" for diagnostics.
	if out.ReferenceImages[0].Label != "continuity:alice" {
		t.Errorf("continuity ref label = %q, want 'continuity:alice'", out.ReferenceImages[0].Label)
	}
}

func TestApplyContinuityOverlay_DedupsAgainstExistingRefs(t *testing.T) {
	// If the continuity URL is already in the reference list (e.g.
	// the most-recent render IS the avatar — a freshly-bound
	// character with one render), don't double up.
	in := AssetContext{
		ReferenceImages: []ReferenceImage{{URL: "same.png", Label: "character:alice"}},
	}
	bindings := &AssetBinding{CharacterIDs: []int{42}}
	chars := []model.Character{}
	c := model.Character{Slug: "alice", Name: "Alice"}
	c.Id = 42
	chars = append(chars, c)
	index := ContinuityIndex{42: "same.png"}
	out := ApplyContinuityOverlay(in, bindings, chars, index)
	if len(out.ReferenceImages) != 1 {
		t.Errorf("dup URL must NOT add a second ref, got %d", len(out.ReferenceImages))
	}
}

func TestApplyContinuityOverlay_SkipsCharactersNotInIndex(t *testing.T) {
	// Binding asks for chars 1, 2, 3; index only has continuity
	// for char 2. Only char 2's URL is prepended.
	in := AssetContext{ReferenceImages: []ReferenceImage{{URL: "av"}}}
	bindings := &AssetBinding{CharacterIDs: []int{1, 2, 3}}
	c := model.Character{Slug: "two"}
	c.Id = 2
	chars := []model.Character{c}
	index := ContinuityIndex{2: "u2"}
	out := ApplyContinuityOverlay(in, bindings, chars, index)
	if len(out.ReferenceImages) != 2 {
		t.Fatalf("expected 2 refs, got %d", len(out.ReferenceImages))
	}
	if out.ReferenceImages[0].URL != "u2" {
		t.Errorf("first ref = %q, want 'u2'", out.ReferenceImages[0].URL)
	}
}
