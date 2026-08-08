package tools

import (
	"strings"
	"testing"

	"server/globals"
	"server/model"
	"server/utils/testutil"

	"gorm.io/gorm"
)

func installVideoPromptConfigTestDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	previous := globals.GraDBs
	globals.GraDBs = map[string]*gorm.DB{"system": db}
	t.Cleanup(func() { globals.GraDBs = previous })
}

func TestNormalizeVideoPromptAssetList_ResolvesAccessibleAssetID(t *testing.T) {
	db := testutil.NewTestDB(t)
	installVideoPromptConfigTestDB(t, db)

	global := model.GlobalAsset{
		UID:           42,
		UUID:          "video-prompt-asset",
		Kind:          model.GlobalAssetKindImage,
		Source:        model.GlobalAssetSourceUpload,
		SourceTable:   "reference_upload",
		SourceID:      1,
		SourceItemKey: "upload-1",
		URL:           "/uploads/references/uid/42/ref.png",
		Status:        model.GlobalAssetStatusActive,
		Visibility:    model.GlobalAssetVisibilityPrivate,
		VariantType:   model.GlobalAssetVariantOriginal,
	}
	if err := db.Create(&global).Error; err != nil {
		t.Fatalf("create global asset: %v", err)
	}

	got, err := normalizeVideoPromptAssetList([]any{
		map[string]any{
			"id":      "hero-ref",
			"kind":    "image",
			"role":    "character-look",
			"assetId": float64(global.Id),
		},
	}, 4, 42)
	if err != nil {
		t.Fatalf("normalizeVideoPromptAssetList: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0]["uri"] != global.URL {
		t.Fatalf("uri = %#v, want %q", got[0]["uri"], global.URL)
	}
	if got[0]["assetId"] != global.Id || got[0]["globalAssetId"] != global.Id {
		t.Fatalf("asset ids = %#v/%#v, want %d", got[0]["assetId"], got[0]["globalAssetId"], global.Id)
	}
}

func TestNormalizeVideoPromptAssetList_RejectsForeignOrWrongKindAssetID(t *testing.T) {
	db := testutil.NewTestDB(t)
	installVideoPromptConfigTestDB(t, db)

	global := model.GlobalAsset{
		UID:           99,
		UUID:          "foreign-video-prompt-asset",
		Kind:          model.GlobalAssetKindVideo,
		Source:        model.GlobalAssetSourceUpload,
		SourceTable:   "reference_upload",
		SourceID:      2,
		SourceItemKey: "upload-2",
		URL:           "/uploads/reference-videos/uid/99/ref.mp4",
		Status:        model.GlobalAssetStatusActive,
		Visibility:    model.GlobalAssetVisibilityPrivate,
		VariantType:   model.GlobalAssetVariantOriginal,
	}
	if err := db.Create(&global).Error; err != nil {
		t.Fatalf("create global asset: %v", err)
	}

	_, err := normalizeVideoPromptAssetList([]any{
		map[string]any{
			"id":      "foreign-ref",
			"kind":    "video",
			"role":    "camera-movement",
			"assetId": float64(global.Id),
		},
	}, 4, 42)
	if err == nil || !strings.Contains(err.Error(), "not accessible") {
		t.Fatalf("foreign asset err = %v, want not accessible", err)
	}

	global.UID = 42
	if err := db.Save(&global).Error; err != nil {
		t.Fatalf("move asset to owner: %v", err)
	}
	_, err = normalizeVideoPromptAssetList([]any{
		map[string]any{
			"id":      "wrong-kind",
			"kind":    "image",
			"role":    "style-ref",
			"assetId": float64(global.Id),
		},
	}, 4, 42)
	if err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("wrong kind err = %v, want kind mismatch", err)
	}
}
