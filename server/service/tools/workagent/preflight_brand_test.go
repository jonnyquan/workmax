package workagent

import (
	"strings"
	"testing"

	"server/globals"
	"server/model"
	"server/service/asset_library"
	"server/utils/testutil"

	"gorm.io/gorm"
)

// installSystemDBForPreflight swaps globals.GraDBs["system"] for
// the duration of one test so the asset_library descriptors (and
// the rest of preflight's Default* lookups) read from the in-memory
// SQLite fixture instead of trying the production DB. Cleanup
// restores the previous map so test ordering doesn't leak.
func installSystemDBForPreflight(t *testing.T, db *gorm.DB) {
	t.Helper()
	previous := globals.GraDBs
	globals.GraDBs = map[string]*gorm.DB{"system": db}
	t.Cleanup(func() { globals.GraDBs = previous })
}

// seedBrandAsset — thin alias around seedBrand kept for naming
// symmetry with seedCharacterAsset / seedDirectorStyleAsset (one
// seed function name per asset kind). Production code never sees
// these; callers are tests in this package + the library-index
// cross-cutting tests.
func seedBrandAsset(t *testing.T, db *gorm.DB, uid int, confirmed bool) *model.Brand {
	t.Helper()
	return seedBrand(t, db, uid, confirmed)
}

// seedBrand seeds a row into the platform w_brand table for tests.
// Sprint-E migration: the test fixtures now produce *model.Brand
// rows because the brand descriptor reads/writes the platform
// table. The lifecycle helper takes Confirmed bool directly (no
// status enum string mapping) — matches the platform's flat
// Status int8 + Confirmed bool model.
func seedBrand(t *testing.T, db *gorm.DB, uid int, confirmed bool) *model.Brand {
	t.Helper()
	b := &model.Brand{
		UID:        uid,
		Name:       "Test Brand",
		Slug:       "test-brand",
		Status:     model.BrandStatusActive,
		Confirmed:  confirmed,
		SourceKind: model.BrandSourceExtracted,
		Lang:       "en",
		Colors:     model.JSONMap{"primary": "#3151c4", "accent": "#b75240"},
		Typography: model.JSONMap{"display": "Inter Display", "body": "Inter"},
	}
	// Select("*") forces GORM to include zero-value fields in the
	// INSERT — without it, Confirmed=false would be skipped and the
	// DDL DEFAULT 1 would override (yielding confirmed=true rows
	// where the test asked for a draft).
	if err := db.Select("*").Create(b).Error; err != nil {
		t.Fatalf("seed brand: %v", err)
	}
	return b
}

// TestLoadBrandSpecForOwner_NoUID — uid=0 short-circuits before
// hitting the DB. Defensive: a system-path caller that loses its
// uid context shouldn't accidentally surface a brand from a
// random user.
func TestLoadBrandSpecForOwner_NoUID(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	if got := loadActiveAssetXML(0, asset_library.AssetKindBrand); got != "" {
		t.Errorf("uid=0 should return empty, got %q", got)
	}
}

// TestLoadBrandSpecForOwner_NoActiveBrand — a uid with only
// drafts gets empty (preflight only injects fully-vetted brands).
func TestLoadBrandSpecForOwner_NoActiveBrand(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	seedBrand(t, db, 42, false) // draft (Confirmed=false)
	if got := loadActiveAssetXML(42, asset_library.AssetKindBrand); got != "" {
		t.Errorf("draft-only uid should return empty, got %q", got)
	}
}

// TestLoadBrandSpecForOwner_RendersConfirmedBrand — happy path.
// Confirmed brand → <brand-spec> XML carrying name + slug + JSON
// sections.
func TestLoadBrandSpecForOwner_RendersConfirmedBrand(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	seedBrand(t, db, 42, true)

	got := loadActiveAssetXML(42, asset_library.AssetKindBrand)
	if got == "" {
		t.Fatal("confirmed brand should produce non-empty output")
	}
	if !strings.HasPrefix(got, "<brand-spec>") || !strings.HasSuffix(got, "</brand-spec>") {
		t.Errorf("missing XML wrapper: %q", got)
	}
	for _, want := range []string{
		"asset_kind: brand",
		"contract_schema: creative_asset_contract.v1",
		"contract_status: confirmed",
		"name: Test Brand",
		"slug: test-brand",
		`#3151c4`, // colors JSON content survives
		`Inter`,   // typography JSON content survives
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q: %q", want, got)
		}
	}
}

// TestFormatBrandSpecXML_DropsEmptySections — only populated
// JSONMap sections render. nil / empty-map sections drop cleanly
// so the composer doesn't spam the prompt with empty placeholders.
func TestFormatBrandSpecXML_DropsEmptySections(t *testing.T) {
	b := &model.Brand{
		Name:       "Sparse",
		Slug:       "sparse",
		Confirmed:  true,
		Colors:     model.JSONMap{"x": 1}, // populated
		Typography: model.JSONMap{},       // empty map → drop
		Spacing:    nil,                   // nil → drop
		// layout / components / motion / voice — nil
	}
	got := formatBrandSpecXML(b)
	if !strings.Contains(got, "colors:") {
		t.Errorf("populated section missing: %q", got)
	}
	for _, dropped := range []string{"typography:", "spacing:", "layout:", "components:", "motion:", "voice:"} {
		if strings.Contains(got, dropped) {
			t.Errorf("unset/empty section should drop, but found %q in: %q", dropped, got)
		}
	}
}

// TestFormatBrandSpecXML_DraftWatermark — drafts ride with the
// [待品牌方确认] watermark per M4 protocol; confirmed brands don't.
func TestFormatBrandSpecXML_DraftWatermark(t *testing.T) {
	draft := &model.Brand{Name: "Draft", Confirmed: false}
	if got := formatBrandSpecXML(draft); !strings.Contains(got, "[待品牌方确认]") {
		t.Errorf("draft missing watermark: %q", got)
	}
	confirmed := &model.Brand{Name: "Confirmed", Confirmed: true}
	if got := formatBrandSpecXML(confirmed); strings.Contains(got, "[待品牌方确认]") {
		t.Errorf("confirmed brand should NOT carry watermark: %q", got)
	}
}

// TestBuildPreflightAdditions_InjectsBrandSpec — end-to-end
// integration: a confirmed brand for uid lands in the composed
// SystemAdditions output as <brand-spec>.
func TestBuildPreflightAdditions_InjectsBrandSpec(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	seedBrand(t, db, 42, true)

	got := BuildPreflightAdditions(42, "ppt")
	if !strings.Contains(got, "<brand-spec>") {
		t.Errorf("brand-spec should ride into preflight additions, got %q",
			got[:min(500, len(got))])
	}
	if !strings.Contains(got, "name: Test Brand") {
		t.Errorf("brand name missing from additions: %q", got[:min(500, len(got))])
	}
}

// TestHasBrandContext_DBPath — uid with confirmed brand returns
// true regardless of threadID. Sprint-E Phase 7: HasBrandContext
// now reads the platform w_brand table; fixture seeds *model.Brand.
func TestHasBrandContext_DBPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	seedBrand(t, db, 42, true)

	if !HasBrandContext(42, 0) {
		t.Error("uid with confirmed brand should report true even with threadID=0")
	}
	if !HasBrandContext(42, 999) {
		t.Error("uid with confirmed brand should report true regardless of threadID")
	}
	// Different uid sees no brand.
	if HasBrandContext(99, 0) {
		t.Error("other uid should NOT inherit brand context")
	}
}

// TestHasBrandContext_DBPath_DraftSurfaces — FindLatestForOwner
// surfaces both draft + confirmed; the M4 watermark path requires
// drafts to register as "has context" so the picker stays
// suppressed even before Vocalize.
func TestHasBrandContext_DBPath_DraftSurfaces(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	seedBrand(t, db, 42, false) // draft (Confirmed=false)
	if !HasBrandContext(42, 0) {
		t.Error("draft brand should still register as 'has context'")
	}
}

// TestHasBrandContext_NoUID_NoThread — both signals empty → false.
func TestHasBrandContext_NoUID_NoThread(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	if HasBrandContext(0, 0) {
		t.Error("no uid + no threadID should be false (default-deny)")
	}
}

// TestHasBrandContext_LegacyMetadataIgnored — Sprint-E close-out:
// HasBrandContext no longer scans chat_message metadata for the
// pre-Backlog-#11 "brand_spec_extracted" / "brand_asset_picked"
// markers. A thread carrying only those legacy markers (and no
// brand_asset row for the uid) should now report false.
func TestHasBrandContext_LegacyMetadataIgnored(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	if HasBrandContext(42, 99) {
		t.Error("uid with no brand_asset row should report false, even when a thread is supplied")
	}
}
