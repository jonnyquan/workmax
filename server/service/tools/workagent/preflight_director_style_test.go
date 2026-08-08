package workagent

import (
	"strings"
	"testing"

	"server/model"
	"server/service/asset_library"
	"server/service/director_style"
	"server/utils/testutil"

	"gorm.io/gorm"
)

// seedDirectorStyleAsset seeds a *model.DirectorStyle into
// w_global_director_style for the preflight test suite. Lifecycle
// gated by `confirmed`: true = ready / vocalized, false = M4 draft.
// Mirrors the brand + character fixture shape across the asset-
// library trio.
func seedDirectorStyleAsset(t *testing.T, db *gorm.DB, uid int, confirmed bool) *model.DirectorStyle {
	t.Helper()
	d := &model.DirectorStyle{
		UID:            uid,
		Name:           "Wes Anderson",
		Slug:           "wes-anderson",
		Era:            "modern",
		Genre:          "comedy-drama",
		Status:         model.DirectorStyleStatusActive,
		Confirmed:      confirmed,
		SourceKind:     model.DirectorStyleSourceExtracted,
		Lang:           "en",
		Composition:    model.JSONMap{"symmetry": "strict", "framing": "central"},
		Color:          model.JSONMap{"palette": []string{"#f4d6a8", "#3a4f3c"}},
		Lighting:       model.JSONMap{"key": "soft", "time": "golden_hour"},
		Motion:         model.JSONMap{"camera": []string{"whip_pan"}},
		PromptSuffix:   "shot on 35mm, soft golden hour",
		NegativePrompt: "no handheld",
	}
	if err := director_style.Default().Create(d); err != nil {
		t.Fatalf("seed director_style: %v", err)
	}
	return d
}

func TestLoadDirectorStyleContextForOwner_NoUID(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	if got := loadActiveAssetXML(0, asset_library.AssetKindDirectorStyle); got != "" {
		t.Errorf("uid=0 should return empty, got %q", got)
	}
}

func TestLoadDirectorStyleContextForOwner_NoActive(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	seedDirectorStyleAsset(t, db, 42, false)
	if got := loadActiveAssetXML(42, asset_library.AssetKindDirectorStyle); got != "" {
		t.Errorf("draft-only uid should return empty, got %q", got)
	}
}

func TestLoadDirectorStyleContextForOwner_RendersConfirmed(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	seedDirectorStyleAsset(t, db, 42, true)

	got := loadActiveAssetXML(42, asset_library.AssetKindDirectorStyle)
	if !strings.HasPrefix(got, "<director-style>") || !strings.HasSuffix(got, "</director-style>") {
		t.Errorf("missing XML wrapper: %q", got)
	}
	// Sprint-E: description is no longer surfaced (platform table
	// has it as RawSpecMD; preflight emission focused on identity +
	// axes). references: line also removed (relational ref table
	// not yet joined into the formatter).
	for _, want := range []string{
		"asset_kind: director_style",
		"contract_schema: creative_asset_contract.v1",
		"contract_status: confirmed",
		"name: Wes Anderson",
		"slug: wes-anderson",
		"era: modern",
		"genre: comedy-drama",
		"composition:",
		`"symmetry":"strict"`,
		"color:",
		`#f4d6a8`,
		"lighting:",
		"motion:",
		"prompt_suffix: shot on 35mm, soft golden hour",
		"negative_prompt: no handheld",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q: %q", want, got)
		}
	}
}

func TestLoadDirectorStyleContextForOwner_RendersReferences(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	style := seedDirectorStyleAsset(t, db, 42, true)
	if err := db.Create(&model.DirectorStyleReference{
		DirectorStyleID: uint64(style.Id),
		UID:             42,
		ImageURL:        "/uploads/ref-1.png",
		ReferenceType:   model.DirectorStyleReferenceTypeStill,
		Label:           "centered hallway",
		SortOrder:       1,
	}).Error; err != nil {
		t.Fatalf("seed director reference: %v", err)
	}

	got := loadActiveAssetXML(42, asset_library.AssetKindDirectorStyle)
	for _, want := range []string{
		"reference: /uploads/ref-1.png",
		"type=still",
		"label=centered hallway",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q: %q", want, got)
		}
	}
}

// TestFormatDirectorStyleXML_DropsEmptyAxes — only populated axes
// ride; nil / empty-map axes drop cleanly.
func TestFormatDirectorStyleXML_DropsEmptyAxes(t *testing.T) {
	d := &model.DirectorStyle{
		Name:        "Sparse",
		Confirmed:   true,
		Composition: model.JSONMap{"x": 1},
		Color:       model.JSONMap{}, // empty → drop
		Lighting:    nil,             // nil → drop
		// motion / texture left nil
	}
	got := formatDirectorStyleXML(d)
	if !strings.Contains(got, "composition:") {
		t.Errorf("populated axis missing: %q", got)
	}
	for _, dropped := range []string{"color:", "lighting:", "motion:", "texture:"} {
		if strings.Contains(got, dropped) {
			t.Errorf("empty/missing axis %q should drop, got: %q", dropped, got)
		}
	}
}

func TestFormatDirectorStyleXML_DraftWatermark(t *testing.T) {
	draft := &model.DirectorStyle{Name: "Draft", Confirmed: false}
	if got := formatDirectorStyleXML(draft); !strings.Contains(got, "[待品牌方确认]") {
		t.Errorf("draft missing watermark: %q", got)
	}
}

// TestBuildPreflightAdditions_InjectsDirectorStyle — end-to-end
// integration: confirmed director_style rides into composed output.
func TestBuildPreflightAdditions_InjectsDirectorStyle(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	seedDirectorStyleAsset(t, db, 42, true)

	got := BuildPreflightAdditions(42, "ppt")
	if !strings.Contains(got, "<director-style>") {
		t.Errorf("director-style should ride into preflight: %q",
			got[:min(500, len(got))])
	}
	if !strings.Contains(got, "name: Wes Anderson") {
		t.Errorf("director name missing: %q", got[:min(500, len(got))])
	}
}

// TestBuildPreflightAdditions_AllThreeAssetsCoexist — brand +
// director_style + character all on one turn. Verifies the
// composer's layer ordering: brand → director-style → character.
//
// Note: character still uses workagent table until Phase 3b.
func TestBuildPreflightAdditions_AllThreeAssetsCoexist(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	seedBrandAsset(t, db, 42, true)
	seedDirectorStyleAsset(t, db, 42, true)
	seedCharacterAsset(t, db, 42, true)

	got := BuildPreflightAdditions(42, "ppt")
	brandIdx := strings.Index(got, "<brand-spec>")
	directorIdx := strings.Index(got, "<director-style>")
	charIdx := strings.Index(got, "<character-context>")

	if brandIdx < 0 {
		t.Errorf("brand block missing: %q", got[:min(500, len(got))])
	}
	if directorIdx < 0 {
		t.Errorf("director-style block missing: %q", got[:min(500, len(got))])
	}
	if charIdx < 0 {
		t.Errorf("character block missing: %q", got[:min(500, len(got))])
	}

	// Layer order: brand → director-style → character.
	if !(brandIdx < directorIdx && directorIdx < charIdx) {
		t.Errorf("layer ordering wrong: brand@%d director@%d character@%d (want ascending)",
			brandIdx, directorIdx, charIdx)
	}
}
