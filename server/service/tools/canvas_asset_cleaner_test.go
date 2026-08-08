package tools

import (
	"context"
	"testing"
	"time"

	"server/globals"
	"server/model"
	"server/utils/testutil"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

func TestCleanOrphanCanvasAssets_TombstonesGlobalBridge(t *testing.T) {
	db := testutil.NewTestDB(t)
	prevDBs := globals.GraDBs
	prevLogger := globals.Logger
	globals.GraDBs = map[string]*gorm.DB{"system": db}
	globals.Logger = logrus.New()
	t.Cleanup(func() {
		globals.GraDBs = prevDBs
		globals.Logger = prevLogger
	})

	old := time.Now().Add(-48 * time.Hour)
	if err := db.Create(&model.GlobalAsset{
		GraMODEL:      globals.GraMODEL{Id: 20, CreatedAt: old, UpdatedAt: old},
		UID:           42,
		UUID:          "orphan-global",
		Kind:          model.GlobalAssetKindImage,
		Source:        model.GlobalAssetSourceUpload,
		SourceTable:   "canvas_project_file",
		SourceID:      0,
		SourceItemKey: "asset",
		URL:           "/uploads/canvas/orphan.png",
		Status:        model.GlobalAssetStatusActive,
		Visibility:    model.GlobalAssetVisibilityPrivate,
		VariantType:   model.GlobalAssetVariantOriginal,
	}).Error; err != nil {
		t.Fatalf("seed global asset: %v", err)
	}

	deleted, err := CleanOrphanCanvasAssets(context.Background(), time.Hour)
	if err != nil {
		t.Fatalf("CleanOrphanCanvasAssets: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}

	var global model.GlobalAsset
	if err := db.Unscoped().First(&global, 20).Error; err != nil {
		t.Fatalf("load global asset: %v", err)
	}
	if global.Status != model.GlobalAssetStatusDeleted || global.DeletedAt == nil {
		t.Fatalf("global asset deletion state = status %d deleted_at %v, want tombstone", global.Status, global.DeletedAt)
	}
}

// TestCleanOrphanCanvasAssets_CoversReferenceUploadAndSparesGenerator
// pins the source_table scope: both canvas_project_file and
// reference_upload (composer / @mention reference image uploads) are
// canvas-origin and get swept; non-canvas source_tables stay alive so
// the generator surface's own cleanup can own its data (audit 2026-05-16
// / P0-②).
func TestCleanOrphanCanvasAssets_CoversReferenceUploadAndSparesGenerator(t *testing.T) {
	db := testutil.NewTestDB(t)
	prevDBs := globals.GraDBs
	prevLogger := globals.Logger
	globals.GraDBs = map[string]*gorm.DB{"system": db}
	globals.Logger = logrus.New()
	t.Cleanup(func() {
		globals.GraDBs = prevDBs
		globals.Logger = prevLogger
	})

	old := time.Now().Add(-48 * time.Hour)
	seeds := []struct {
		id          uint
		uuid        string
		sourceTable string
		url         string
		wantDeleted bool
	}{
		{30, "orphan-project", "canvas_project_file", "/uploads/canvas/p.png", true},
		{31, "orphan-reference", "reference_upload", "/uploads/canvas/r.png", true},
		{32, "orphan-generator", "w_generation_object", "/uploads/gen/g.png", false},
	}
	for _, s := range seeds {
		if err := db.Create(&model.GlobalAsset{
			GraMODEL:      globals.GraMODEL{Id: s.id, CreatedAt: old, UpdatedAt: old},
			UID:           7,
			UUID:          s.uuid,
			Kind:          model.GlobalAssetKindImage,
			Source:        model.GlobalAssetSourceUpload,
			SourceTable:   s.sourceTable,
			SourceID:      0,
			SourceItemKey: "asset-" + s.uuid,
			URL:           s.url,
			Status:        model.GlobalAssetStatusActive,
			Visibility:    model.GlobalAssetVisibilityPrivate,
			VariantType:   model.GlobalAssetVariantOriginal,
		}).Error; err != nil {
			t.Fatalf("seed %s: %v", s.uuid, err)
		}
	}

	deleted, err := CleanOrphanCanvasAssets(context.Background(), time.Hour)
	if err != nil {
		t.Fatalf("CleanOrphanCanvasAssets: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted = %d, want 2 (canvas_project_file + reference_upload; generator out of scope)", deleted)
	}

	for _, s := range seeds {
		var got model.GlobalAsset
		if err := db.Unscoped().First(&got, s.id).Error; err != nil {
			t.Fatalf("load %s: %v", s.uuid, err)
		}
		isTomb := got.Status == model.GlobalAssetStatusDeleted
		if isTomb != s.wantDeleted {
			t.Errorf("%s (source_table=%s): tombstoned=%v want=%v", s.uuid, s.sourceTable, isTomb, s.wantDeleted)
		}
	}
}
