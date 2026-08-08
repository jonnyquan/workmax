package tools

import (
	"context"
	"fmt"
	"time"

	"server/globals"
	"server/model"

	"gorm.io/gorm"
)

// CleanOrphanCanvasAssets tombstones global Canvas project-file assets that
// have no associated project (project_id IS NULL) and were created more than
// `maxAge` ago. The name is kept for scheduler compatibility; the legacy
// canvas-asset table (dropped by migration 20260625) is no longer touched.
//
// This function only updates database records. If the assets reference files
// stored in object storage, run a separate storage GC pass after deleting the
// legacy rows.
//
// Usage:
//
//	// In a cron job or periodic task:
//	deleted, err := tools.CleanOrphanCanvasAssets(ctx, 7*24*time.Hour)
func CleanOrphanCanvasAssets(ctx context.Context, maxAge time.Duration) (int64, error) {
	if maxAge <= 0 {
		return 0, fmt.Errorf("maxAge must be positive")
	}

	cutoff := time.Now().Add(-maxAge)
	db := globals.GraDBs["system"]
	tx := db.WithContext(ctx)

	// source_table list covers every canvas-originating CreateManagedUpload
	// (globalasset/repository.go): canvas_project_file for project assets,
	// reference_upload for composer / @mention reference uploads. Both can
	// strand at project_id IS NULL when the binding step fails after upload.
	canvasSourceTables := []string{"canvas_project_file", "reference_upload"}
	var assets []model.GlobalAsset
	if err := tx.Model(&model.GlobalAsset{}).
		Where("source_table IN ?", canvasSourceTables).
		Where("project_id IS NULL AND deleted_at IS NULL AND status <> ?", model.GlobalAssetStatusDeleted).
		Where("created_at < ?", cutoff).
		Find(&assets).Error; err != nil {
		globals.Error(fmt.Sprintf("[Canvas] CleanOrphanCanvasAssets failed: %s", err.Error()))
		return 0, err
	}
	if len(assets) == 0 {
		return 0, nil
	}

	globalIDs := make([]uint, 0, len(assets))
	for _, asset := range assets {
		globalIDs = append(globalIDs, asset.Id)
	}

	var deleted int64
	err := tx.Transaction(func(ttx *gorm.DB) error {
		now := time.Now()
		result := ttx.Model(&model.GlobalAsset{}).
			Where("id IN ? AND deleted_at IS NULL", globalIDs).
			Updates(map[string]interface{}{
				"status":     model.GlobalAssetStatusDeleted,
				"deleted_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		deleted = result.RowsAffected
		return nil
	})
	if err != nil {
		globals.Error(fmt.Sprintf("[Canvas] CleanOrphanCanvasAssets failed: %s", err.Error()))
		return 0, err
	}

	if deleted > 0 {
		globals.Info(fmt.Sprintf("[Canvas] Cleaned %d orphan assets older than %s", deleted, maxAge))
	}

	return deleted, nil
}

// ListOrphanCanvasAssets returns orphan global Canvas file assets created
// before the given cutoff time. Useful for previewing what would be cleaned,
// or for pre-cleaning file storage before deleting records.
func ListOrphanCanvasAssets(ctx context.Context, maxAge time.Duration, limit int) ([]model.GlobalAsset, error) {
	if limit <= 0 {
		limit = 100
	}

	cutoff := time.Now().Add(-maxAge)
	db := globals.GraDBs["system"]

	canvasSourceTables := []string{"canvas_project_file", "reference_upload"}
	var assets []model.GlobalAsset
	err := db.WithContext(ctx).Model(&model.GlobalAsset{}).
		Where("source_table IN ?", canvasSourceTables).
		Where("project_id IS NULL AND deleted_at IS NULL AND status <> ?", model.GlobalAssetStatusDeleted).
		Where("created_at < ?", cutoff).
		Order("created_at ASC").
		Limit(limit).
		Find(&assets).Error

	return assets, err
}
