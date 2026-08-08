package globalasset

import (
	"context"
	"fmt"
	"strings"

	"server/model"
	workagentModel "server/model/workagent"

	"gorm.io/gorm"
)

type BackfillOptions struct {
	BatchSize int
}

type BackfillReport struct {
	GenerationObjects int
	WorkAgentFiles    int
}

type CoverageReport struct {
	MissingGenerationObjects  int64
	MissingWorkAgentFiles     int64
	MissingLedgerGlobalBridge int64
	GlobalAssetsMissingSource int64
	GlobalAssetsDeletedSource int64
}

func AuditCoverage(ctx context.Context, db *gorm.DB) (CoverageReport, error) {
	if db == nil {
		return CoverageReport{}, gorm.ErrInvalidDB
	}
	tx := db.WithContext(ctx)
	var report CoverageReport
	if err := tx.Model(&model.GenerationObject{}).
		Where("global_asset_id = 0").
		Count(&report.MissingGenerationObjects).Error; err != nil {
		return report, fmt.Errorf("count generation objects missing global bridge: %w", err)
	}
	if err := tx.Model(&workagentModel.ThreadFile{}).
		Where("global_asset_id = 0").
		Count(&report.MissingWorkAgentFiles).Error; err != nil {
		return report, fmt.Errorf("count workagent files missing global bridge: %w", err)
	}
	if err := tx.Model(&model.UserAssetLedger{}).
		Where("source IN ? AND global_asset_id = 0", []string{"generated", "canvas", "thread_upload", "thread_output", "reference_upload"}).
		Count(&report.MissingLedgerGlobalBridge).Error; err != nil {
		return report, fmt.Errorf("count ledger rows missing global bridge: %w", err)
	}
	if err := tx.Raw(`
		SELECT COUNT(*)
		FROM w_global_asset ga
		WHERE ga.deleted_at IS NULL
		  AND (
		    (ga.source_table = 'canvas_project_file' AND NOT EXISTS (SELECT 1 FROM w_global_project gp WHERE gp.id = ga.source_id))
		    OR (ga.source_table = 'w_generation_object' AND NOT EXISTS (SELECT 1 FROM w_generation_object go WHERE go.id = ga.source_id))
		    OR (ga.source_table = 'w_workagent_thread_file' AND NOT EXISTS (SELECT 1 FROM w_workagent_thread_file wf WHERE wf.id = ga.source_id))
		  )
	`).Scan(&report.GlobalAssetsMissingSource).Error; err != nil {
		return report, fmt.Errorf("count global assets missing source rows: %w", err)
	}
	if err := tx.Raw(`
		SELECT COUNT(*)
		FROM w_global_asset ga
		WHERE ga.deleted_at IS NULL
		  AND ga.status <> ?
		  AND (
		    (ga.source_table = 'canvas_project_file' AND EXISTS (SELECT 1 FROM w_global_project gp WHERE gp.id = ga.source_id AND gp.deleted_at IS NOT NULL))
		    OR (ga.source_table = 'w_generation_object' AND EXISTS (SELECT 1 FROM w_generation_object go WHERE go.id = ga.source_id AND go.status = ?))
		    OR (ga.source_table = 'w_workagent_thread_file' AND EXISTS (SELECT 1 FROM w_workagent_thread_file wf WHERE wf.id = ga.source_id AND wf.deleted_at IS NOT NULL))
		  )
	`, model.GlobalAssetStatusDeleted, model.GenerationObjectStatusDeleted).Scan(&report.GlobalAssetsDeletedSource).Error; err != nil {
		return report, fmt.Errorf("count global assets with deleted source rows: %w", err)
	}
	return report, nil
}

func BackfillExistingSources(ctx context.Context, db *gorm.DB, opts BackfillOptions) (BackfillReport, error) {
	if db == nil {
		return BackfillReport{}, gorm.ErrInvalidDB
	}
	batchSize := opts.BatchSize
	if batchSize <= 0 || batchSize > 1000 {
		batchSize = 500
	}

	repo := NewRepository(db.WithContext(ctx))
	var report BackfillReport
	for {
		count, err := backfillGenerationObjects(ctx, db, repo, batchSize)
		if err != nil {
			return report, err
		}
		report.GenerationObjects += count
		if count < batchSize {
			break
		}
	}
	for {
		count, err := backfillWorkAgentFiles(ctx, db, repo, batchSize)
		if err != nil {
			return report, err
		}
		report.WorkAgentFiles += count
		if count < batchSize {
			break
		}
	}
	return report, nil
}

func (r *Repository) SyncFromGenerationObjectsByRecordIDs(ctx context.Context, recordIDs []uint) (int, error) {
	if r == nil || r.db == nil {
		return 0, gorm.ErrInvalidDB
	}
	normalized := make([]uint, 0, len(recordIDs))
	seen := map[uint]struct{}{}
	for _, id := range recordIDs {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	if len(normalized) == 0 {
		return 0, nil
	}
	var rows []model.GenerationObject
	if err := r.db.WithContext(ctx).Where("record_id IN ?", normalized).Find(&rows).Error; err != nil {
		return 0, fmt.Errorf("load generation objects by record: %w", err)
	}
	for i := range rows {
		if _, err := r.CreateFromGenerationObject(&rows[i]); err != nil {
			return i, err
		}
	}
	return len(rows), nil
}

func (r *Repository) SyncFromGenerationObjectsByTaskIDs(ctx context.Context, taskIDs []string) (int, error) {
	if r == nil || r.db == nil {
		return 0, gorm.ErrInvalidDB
	}
	normalized := make([]string, 0, len(taskIDs))
	seen := map[string]struct{}{}
	for _, id := range taskIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	if len(normalized) == 0 {
		return 0, nil
	}
	var rows []model.GenerationObject
	if err := r.db.WithContext(ctx).Where("task_id IN ?", normalized).Find(&rows).Error; err != nil {
		return 0, fmt.Errorf("load generation objects by task: %w", err)
	}
	for i := range rows {
		if _, err := r.CreateFromGenerationObject(&rows[i]); err != nil {
			return i, err
		}
	}
	return len(rows), nil
}

func backfillGenerationObjects(ctx context.Context, db *gorm.DB, repo *Repository, limit int) (int, error) {
	var rows []model.GenerationObject
	if err := db.WithContext(ctx).
		Where("global_asset_id = 0").
		Order("id ASC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return 0, fmt.Errorf("load generation objects for global backfill: %w", err)
	}
	for i := range rows {
		global, err := repo.CreateFromGenerationObject(&rows[i])
		if err != nil {
			return i, fmt.Errorf("backfill generation object %d: %w", rows[i].Id, err)
		}
		if global != nil && global.Id != 0 {
			if err := db.WithContext(ctx).Model(&model.UserAssetLedger{}).
				Where("uid = ? AND source = ? AND source_id = ?", rows[i].UID, "generated", uint64(rows[i].Id)).
				Update("global_asset_id", global.Id).Error; err != nil {
				return i, fmt.Errorf("backfill generation ledger %d: %w", rows[i].Id, err)
			}
		}
	}
	return len(rows), nil
}

func backfillWorkAgentFiles(ctx context.Context, db *gorm.DB, repo *Repository, limit int) (int, error) {
	var rows []workagentModel.ThreadFile
	if err := db.WithContext(ctx).
		Where("global_asset_id = 0").
		Order("id ASC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return 0, fmt.Errorf("load workagent files for global backfill: %w", err)
	}
	for i := range rows {
		var thread workagentModel.ChatThread
		threadPtr := (*workagentModel.ChatThread)(nil)
		if rows[i].ThreadID != 0 {
			if err := db.WithContext(ctx).
				Where("id = ? AND uid = ?", rows[i].ThreadID, rows[i].UID).
				First(&thread).Error; err == nil {
				threadPtr = &thread
			} else if err != gorm.ErrRecordNotFound {
				return i, fmt.Errorf("load workagent thread %d: %w", rows[i].ThreadID, err)
			}
		}
		global, err := repo.CreateFromThreadFileForThread(&rows[i], threadPtr)
		if err != nil {
			return i, fmt.Errorf("backfill workagent file %d: %w", rows[i].Id, err)
		}
		if global != nil && global.Id != 0 {
			source := "thread_upload"
			if rows[i].FileSource == workagentModel.FileSourceOutput {
				source = "thread_output"
			}
			if err := db.WithContext(ctx).Model(&model.UserAssetLedger{}).
				Where("uid = ? AND source = ? AND source_id = ?", rows[i].UID, source, uint64(rows[i].Id)).
				Update("global_asset_id", global.Id).Error; err != nil {
				return i, fmt.Errorf("backfill workagent ledger %d: %w", rows[i].Id, err)
			}
		}
	}
	return len(rows), nil
}
