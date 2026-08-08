package workagent

import (
	"strings"
	"testing"

	"server/model"
	"server/service/brand"
	"server/utils/testutil"
)

// TestLoadAssetLibraryIndex_NoUID — uid=0 short-circuits without
// hitting the DB. Same posture as the other loadFor* helpers.
func TestLoadAssetLibraryIndex_NoUID(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	if got := loadAssetLibraryIndex(0); got != "" {
		t.Errorf("uid=0 should return empty, got %q", got)
	}
}

// TestLoadAssetLibraryIndex_FreshUser — a uid with no assets at all
// returns "" so the composer doesn't surface an empty <asset-library-
// index> block. Distinct from the per-section "(none)" placeholder
// which only fires when AT LEAST ONE other type has assets.
func TestLoadAssetLibraryIndex_FreshUser(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	if got := loadAssetLibraryIndex(42); got != "" {
		t.Errorf("fresh user should return empty, got %q", got)
	}
}

// TestLoadAssetLibraryIndex_ListsAllThree — when the user has assets
// across all three types, the index renders all three subsections
// with slug + status per entry.
func TestLoadAssetLibraryIndex_ListsAllThree(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	seedBrandAsset(t, db, 42, true)
	seedCharacterAsset(t, db, 42, false)
	seedDirectorStyleAsset(t, db, 42, true)

	got := loadAssetLibraryIndex(42)
	if got == "" {
		t.Fatal("expected non-empty index when user has assets")
	}
	if !strings.HasPrefix(got, "<asset-library-index>") || !strings.HasSuffix(got, "</asset-library-index>") {
		t.Errorf("missing XML wrapper: %q", got)
	}
	for _, want := range []string{
		"brands:",
		"test-brand (confirmed)",
		"characters:",
		"lin-mei (draft)",
		"director_styles:",
		"wes-anderson (confirmed)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q: %q", want, got)
		}
	}
}

// TestLoadAssetLibraryIndex_OneTypeEmptyShowsNone — when only
// brands exist (no characters / director-styles), the empty
// subsections render "(none)" rather than absent so the agent
// can distinguish the missing case from a list cut-off.
func TestLoadAssetLibraryIndex_OneTypeEmptyShowsNone(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	seedBrandAsset(t, db, 42, true)

	got := loadAssetLibraryIndex(42)
	if !strings.Contains(got, "characters:\n  (none)") {
		t.Errorf("empty characters subsection should show (none): %q", got)
	}
	if !strings.Contains(got, "director_styles:\n  (none)") {
		t.Errorf("empty director_styles subsection should show (none): %q", got)
	}
	if !strings.Contains(got, "test-brand (confirmed)") {
		t.Errorf("populated brand subsection should still surface: %q", got)
	}
}

// TestLoadAssetLibraryIndex_FallsBackToNameWhenSlugEmpty — if a
// row has Name but no Slug, the index uses the name. If both are
// empty, falls back to "#<id>". Defends against future imports
// that drop the slug field.
func TestLoadAssetLibraryIndex_FallsBackToNameWhenSlugEmpty(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	// Sprint-E retarget: seed *model.Brand into w_brand. Slug
	// intentionally empty to exercise the librarySlug name-fallback.
	b := &model.Brand{
		UID:        42,
		Name:       "Fallback Name",
		Status:     model.BrandStatusActive,
		Confirmed:  true,
		SourceKind: model.BrandSourceManual,
		Lang:       "en",
		// Slug intentionally empty
	}
	if err := brand.Default().Create(b); err != nil {
		t.Fatal(err)
	}

	got := loadAssetLibraryIndex(42)
	if !strings.Contains(got, "Fallback Name (confirmed)") {
		t.Errorf("expected name fallback, got %q", got)
	}
}

// TestBuildPreflightAdditions_InjectsLibraryIndex — end-to-end:
// the asset library index rides into the composed SystemAdditions
// alongside any active brand/character.
func TestBuildPreflightAdditions_InjectsLibraryIndex(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	seedBrandAsset(t, db, 42, true)
	seedCharacterAsset(t, db, 42, true)

	got := BuildPreflightAdditions(42, "ppt")
	if !strings.Contains(got, "<asset-library-index>") {
		t.Errorf("asset-library-index should ride into preflight additions: %q",
			got[:min(800, len(got))])
	}
	// Verifies the index sits AFTER brand-spec and BEFORE character-context
	// per the composer's documented layer order.
	brandIdx := strings.Index(got, "<brand-spec>")
	indexIdx := strings.Index(got, "<asset-library-index>")
	charIdx := strings.Index(got, "<character-context>")
	if brandIdx < 0 || indexIdx < 0 || charIdx < 0 {
		t.Fatalf("layer order test missing one of the expected blocks: brand@%d index@%d char@%d",
			brandIdx, indexIdx, charIdx)
	}
	if !(brandIdx < indexIdx && indexIdx < charIdx) {
		t.Errorf("layer ordering wrong: brand@%d index@%d char@%d (want brand < index < char)",
			brandIdx, indexIdx, charIdx)
	}
}
