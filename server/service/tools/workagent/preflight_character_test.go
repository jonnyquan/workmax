package workagent

import (
	"strings"
	"testing"

	"server/model"
	"server/service/asset_library"
	"server/service/character"
	"server/utils/testutil"

	"gorm.io/gorm"
)

// seedCharacterAsset seeds a *model.Character into w_global_character
// for the preflight test suite. Lifecycle gated by `confirmed`:
// true = ready / vocalized, false = M4 draft. Mirrors the brand +
// director-style fixture shape across the asset-library trio.
func seedCharacterAsset(t *testing.T, db *gorm.DB, uid int, confirmed bool) *model.Character {
	t.Helper()
	c := &model.Character{
		UID:             uid,
		Name:            "Lin Mei",
		Slug:            "lin-mei",
		RoleType:        model.CharacterRoleProtagonist,
		Gender:          "female",
		AgeRange:        "25-30",
		Status:          model.CharacterStatusActive,
		Confirmed:       confirmed,
		SourceKind:      model.CharacterSourceManual,
		Lang:            "en",
		Appearance:      "A detail-oriented architect with a quiet smile.",
		Personality:     "Calm, observant; speaks in short measured sentences.",
		IdentityAnchors: model.JSONMap{"hair": "black bob", "build": "slim"},
		AvatarImageURL:  "uid/ref.png",
		PromptSuffix:    "wearing navy linen suit",
		NegativePrompt:  "no glasses",
	}
	if err := character.Default().Create(c); err != nil {
		t.Fatalf("seed character: %v", err)
	}
	return c
}

func TestLoadCharacterContextForOwner_NoUID(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	if got := loadActiveAssetXML(0, asset_library.AssetKindCharacter); got != "" {
		t.Errorf("uid=0 should return empty, got %q", got)
	}
}

func TestLoadCharacterContextForOwner_NoActiveCharacter(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	seedCharacterAsset(t, db, 42, false)
	if got := loadActiveAssetXML(42, asset_library.AssetKindCharacter); got != "" {
		t.Errorf("draft-only uid should return empty, got %q", got)
	}
}

func TestLoadCharacterContextForOwner_RendersConfirmedCharacter(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	seedCharacterAsset(t, db, 42, true)

	got := loadActiveAssetXML(42, asset_library.AssetKindCharacter)
	if got == "" {
		t.Fatal("confirmed character should produce non-empty output")
	}
	if !strings.HasPrefix(got, "<character-context>") || !strings.HasSuffix(got, "</character-context>") {
		t.Errorf("missing XML wrapper: %q", got)
	}
	for _, want := range []string{
		"asset_kind: character",
		"contract_schema: creative_asset_contract.v1",
		"contract_status: confirmed",
		"name: Lin Mei",
		"slug: lin-mei",
		"role: protagonist",
		"gender: female",
		"age_range: 25-30",
		"description: A detail-oriented architect with a quiet smile.",
		"personality: Calm, observant; speaks in short measured sentences.",
		"traits:",
		"black bob",
		"reference_image: ",
		"prompt_suffix: wearing navy linen suit",
		"negative_prompt: no glasses",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q: %q", want, got)
		}
	}
}

// TestFormatCharacterContextXML_DropsEmptyFields — partial rows
// render cleanly without empty placeholder lines.
func TestFormatCharacterContextXML_DropsEmptyFields(t *testing.T) {
	c := &model.Character{
		Name:       "Sparse",
		Confirmed:  true,
		Appearance: "Only a name and description.",
		// Slug / RoleType / Gender / AgeRange / Personality /
		// IdentityAnchors / AvatarImageURL / PromptSuffix /
		// NegativePrompt left empty
	}
	got := formatCharacterContextXML(c)
	if !strings.Contains(got, "name: Sparse") {
		t.Errorf("populated field missing: %q", got)
	}
	if !strings.Contains(got, "description: Only a name and description.") {
		t.Errorf("description missing: %q", got)
	}
	for _, dropped := range []string{
		"slug:", "role:", "gender:", "age_range:",
		"personality:",
		"traits:", "reference_image:", "prompt_suffix:", "negative_prompt:",
	} {
		if strings.Contains(got, dropped) {
			t.Errorf("unset field should drop, but found %q in: %q", dropped, got)
		}
	}
}

// TestFormatCharacterContextXML_PersonalityWithoutAppearance —
// personality emits independently of appearance: a row with
// Personality but no Appearance gets the personality: line and
// no description: line.
func TestFormatCharacterContextXML_PersonalityWithoutAppearance(t *testing.T) {
	c := &model.Character{
		Name:        "Voice",
		Confirmed:   true,
		Personality: "Wry, precise; favours one-syllable verbs.",
	}
	got := formatCharacterContextXML(c)
	if !strings.Contains(got, "personality: Wry, precise; favours one-syllable verbs.") {
		t.Errorf("personality line missing: %q", got)
	}
	if strings.Contains(got, "description:") {
		t.Errorf("appearance-less row should drop description: line, got: %q", got)
	}
}

// TestFormatCharacterContextXML_DropsEmptyTraits — empty
// IdentityAnchors drops the traits: line.
func TestFormatCharacterContextXML_DropsEmptyTraits(t *testing.T) {
	for _, traits := range []model.JSONMap{
		nil,
		{},
	} {
		c := &model.Character{
			Name:            "x",
			Confirmed:       true,
			IdentityAnchors: traits,
		}
		got := formatCharacterContextXML(c)
		if strings.Contains(got, "traits:") {
			t.Errorf("empty traits should drop, but found in: %q", got)
		}
	}
}

func TestFormatCharacterContextXML_DraftWatermark(t *testing.T) {
	draft := &model.Character{Name: "Draft", Confirmed: false}
	if got := formatCharacterContextXML(draft); !strings.Contains(got, "[待品牌方确认]") {
		t.Errorf("draft missing watermark: %q", got)
	}
	confirmed := &model.Character{Name: "Confirmed", Confirmed: true}
	if got := formatCharacterContextXML(confirmed); strings.Contains(got, "[待品牌方确认]") {
		t.Errorf("confirmed should NOT carry watermark: %q", got)
	}
}

// TestBuildPreflightAdditions_InjectsCharacterContext — end-to-end:
// confirmed character lands in composed SystemAdditions.
func TestBuildPreflightAdditions_InjectsCharacterContext(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	seedCharacterAsset(t, db, 42, true)

	got := BuildPreflightAdditions(42, "ppt")
	if !strings.Contains(got, "<character-context>") {
		t.Errorf("character-context should ride into preflight, got %q",
			got[:min(500, len(got))])
	}
	if !strings.Contains(got, "name: Lin Mei") {
		t.Errorf("character name missing: %q", got[:min(500, len(got))])
	}
}

// TestBuildPreflightAdditions_BrandAndCharacterCoexist — both can
// fire on the same turn; brand block precedes character block.
func TestBuildPreflightAdditions_BrandAndCharacterCoexist(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	seedBrandAsset(t, db, 42, true)
	seedCharacterAsset(t, db, 42, true)

	got := BuildPreflightAdditions(42, "ppt")
	brandIdx := strings.Index(got, "<brand-spec>")
	charIdx := strings.Index(got, "<character-context>")
	if brandIdx < 0 {
		t.Errorf("brand block missing: %q", got[:min(500, len(got))])
	}
	if charIdx < 0 {
		t.Errorf("character block missing: %q", got[:min(500, len(got))])
	}
	if brandIdx > charIdx {
		t.Errorf("brand should appear before character (composer ordering); got brand@%d character@%d",
			brandIdx, charIdx)
	}
}
