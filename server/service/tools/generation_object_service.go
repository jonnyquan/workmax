package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"server/config"
	assetLedgerService "server/service/assetledger"
	"strconv"
	"strings"
	"time"

	"server/globals"
	"server/model"
	globalAssetService "server/service/globalasset"
	storageService "server/service/storage"

	"gorm.io/gorm"
)

type GenerationObjectService struct{}

type RegisterGenerationObjectParams struct {
	UID         int
	TaskID      string
	RecordID    uint
	ToolID      string
	Provider    string
	Bucket      string
	ObjectKey   string
	AssetKind   string
	ContentType string
	SizeBytes   int64
	ETag        string
	PublicURL   string
	SourceURL   string
}

type CleanupGenerationObjectsResult struct {
	Scanned int
	Deleted int
	Skipped int
}

type BackfillGenerationObjectsResult struct {
	ScannedRecords  int  `json:"scannedRecords"`
	MatchedURLs     int  `json:"matchedUrls"`
	CreatedObjects  int  `json:"createdObjects"`
	ExistingObjects int  `json:"existingObjects"`
	SkippedURLs     int  `json:"skippedUrls"`
	LastRecordID    uint `json:"lastRecordId"`
	DryRun          bool `json:"dryRun"`
}

type GenerationObjectCoverageAuditIssue struct {
	RecordID  uint   `json:"recordId"`
	ToolID    string `json:"toolId"`
	URL       string `json:"url"`
	Category  string `json:"category"`
	ObjectKey string `json:"objectKey,omitempty"`
}

type GenerationObjectCoverageAuditResult struct {
	ScannedRecords int                                  `json:"scannedRecords"`
	LastRecordID   uint                                 `json:"lastRecordId"`
	ManagedURLs    int                                  `json:"managedUrls"`
	RegisteredURLs int                                  `json:"registeredUrls"`
	MissingObjects int                                  `json:"missingObjects"`
	ExternalURLs   int                                  `json:"externalUrls"`
	LocalURLs      int                                  `json:"localUrls"`
	InvalidURLs    int                                  `json:"invalidUrls"`
	SampledIssues  []GenerationObjectCoverageAuditIssue `json:"sampledIssues"`
}

type generationObjectBackfillCandidate struct {
	Provider    string
	Bucket      string
	PublicURL   string
	ObjectKey   string
	AssetKind   string
	ContentType string
}

func (s *GenerationObjectService) Register(p *RegisterGenerationObjectParams) error {
	if p == nil {
		return nil
	}
	if strings.TrimSpace(p.Bucket) == "" || strings.TrimSpace(p.ObjectKey) == "" {
		return nil
	}

	record := model.GenerationObject{
		UID:         p.UID,
		TaskID:      strings.TrimSpace(p.TaskID),
		RecordID:    p.RecordID,
		ToolID:      strings.TrimSpace(p.ToolID),
		Provider:    strings.TrimSpace(p.Provider),
		Bucket:      strings.TrimSpace(p.Bucket),
		ObjectKey:   strings.TrimSpace(p.ObjectKey),
		AssetKind:   strings.TrimSpace(p.AssetKind),
		ContentType: strings.TrimSpace(p.ContentType),
		SizeBytes:   p.SizeBytes,
		ETag:        strings.TrimSpace(p.ETag),
		PublicURL:   strings.TrimSpace(p.PublicURL),
		SourceURL:   strings.TrimSpace(p.SourceURL),
		Status:      model.GenerationObjectStatusActive,
	}

	db := globals.GraDBs["system"]
	if err := db.Where("bucket = ? AND object_key = ?", record.Bucket, record.ObjectKey).Assign(record).FirstOrCreate(&record).Error; err != nil {
		return err
	}
	if global, err := globalAssetService.NewRepository(db).CreateFromGenerationObject(&record); err != nil {
		globals.Warn(fmt.Sprintf("[generation_object] global asset dual-write failed for object=%d: %v", record.Id, err))
	} else if global != nil {
		record.GlobalAssetID = global.Id
	}
	return assetLedgerService.New().UpsertGeneratedObjectWithDB(db, &record)
}

func (s *GenerationObjectService) AttachTaskObjectsToRecord(taskID string, recordID uint) error {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" || recordID == 0 {
		return nil
	}

	db := globals.GraDBs["system"]
	if err := db.
		Model(&model.GenerationObject{}).
		Where("task_id = ? AND record_id = 0", taskID).
		Update("record_id", recordID).Error; err != nil {
		return err
	}
	return assetLedgerService.New().UpdateGeneratedRecordIDByTaskWithDB(db, 0, taskID, recordID)
}

func (s *GenerationObjectService) MarkTaskObjectsOrphan(taskIDs []string) error {
	return s.markTaskObjectsOrphanWithDB(globals.GraDBs["system"], taskIDs)
}

func (s *GenerationObjectService) MarkRecordObjectsDeleted(recordIDs []uint) error {
	return s.markRecordObjectsDeletedWithDB(globals.GraDBs["system"], recordIDs)
}

func (s *GenerationObjectService) DeleteGenerationRecordsWithAssets(ctx context.Context, tx *gorm.DB, records []model.GenerationRecord) error {
	if tx == nil {
		return nil
	}

	normalizedRecords := normalizeGenerationRecords(records)
	if len(normalizedRecords) == 0 {
		return nil
	}

	recordIDs := make([]uint, 0, len(normalizedRecords))
	for _, record := range normalizedRecords {
		recordIDs = append(recordIDs, record.Id)
	}

	var objects []model.GenerationObject
	if err := tx.Where("record_id IN ?", recordIDs).Find(&objects).Error; err != nil {
		return err
	}

	if err := s.deletePhysicalAssetsForRecords(ctx, normalizedRecords, objects); err != nil {
		return err
	}

	if len(objects) > 0 {
		objectIDs := make([]uint, 0, len(objects))
		for _, object := range objects {
			objectIDs = append(objectIDs, object.Id)
		}
		if err := tx.Model(&model.GlobalAsset{}).
			Where("source_table = ? AND source_id IN ? AND deleted_at IS NULL", model.GenerationObject{}.TableName(), objectIDs).
			Updates(map[string]interface{}{
				"status":     model.GlobalAssetStatusDeleted,
				"deleted_at": time.Now(),
			}).Error; err != nil {
			return err
		}
		if err := tx.Where("id IN ?", objectIDs).Delete(&model.GenerationObject{}).Error; err != nil {
			return err
		}
	}

	if err := assetLedgerService.New().DeleteGeneratedByRecordIDsWithDB(tx, 0, recordIDs); err != nil {
		return err
	}
	if err := tx.Where("source = ? AND record_id IN ?", "generation_input", recordIDs).
		Delete(&model.UserAssetLedger{}).Error; err != nil {
		return err
	}
	if err := tx.Where("record_id IN ?", recordIDs).Delete(&model.GenerationTask{}).Error; err != nil {
		return err
	}
	if err := tx.Where("id IN ?", recordIDs).Delete(&model.GenerationRecord{}).Error; err != nil {
		return err
	}

	return nil
}

func (s *GenerationObjectService) markTaskObjectsOrphanWithDB(db *gorm.DB, taskIDs []string) error {
	if db == nil {
		return nil
	}
	normalized := normalizeGenerationObjectTaskIDs(taskIDs)
	if len(normalized) == 0 {
		return nil
	}

	if err := db.Model(&model.GenerationObject{}).
		Where("task_id IN ? AND record_id = 0 AND status = ?", normalized, model.GenerationObjectStatusActive).
		Update("status", model.GenerationObjectStatusOrphan).Error; err != nil {
		return err
	}
	if _, err := globalAssetService.NewRepository(db).SyncFromGenerationObjectsByTaskIDs(context.Background(), normalized); err != nil {
		return err
	}

	if err := assetLedgerService.New().DeleteGeneratedByTaskIDsWithDB(db, 0, normalized); err != nil {
		return err
	}
	return nil
}

func (s *GenerationObjectService) markRecordObjectsDeletedWithDB(db *gorm.DB, recordIDs []uint) error {
	if db == nil {
		return nil
	}
	normalized := normalizeGenerationObjectRecordIDs(recordIDs)
	if len(normalized) == 0 {
		return nil
	}

	if err := db.Model(&model.GenerationObject{}).
		Where("record_id IN ? AND status <> ?", normalized, model.GenerationObjectStatusDeleted).
		Update("status", model.GenerationObjectStatusDeleted).Error; err != nil {
		return err
	}
	if _, err := globalAssetService.NewRepository(db).SyncFromGenerationObjectsByRecordIDs(context.Background(), normalized); err != nil {
		return err
	}

	if err := assetLedgerService.New().DeleteGeneratedByRecordIDsWithDB(db, 0, normalized); err != nil {
		return err
	}
	return nil
}

func normalizeGenerationObjectTaskIDs(taskIDs []string) []string {
	if len(taskIDs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(taskIDs))
	normalized := make([]string, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		trimmed := strings.TrimSpace(taskID)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		normalized = append(normalized, trimmed)
	}
	return normalized
}

func normalizeGenerationObjectRecordIDs(recordIDs []uint) []uint {
	if len(recordIDs) == 0 {
		return nil
	}
	seen := make(map[uint]struct{}, len(recordIDs))
	normalized := make([]uint, 0, len(recordIDs))
	for _, recordID := range recordIDs {
		if recordID == 0 {
			continue
		}
		if _, ok := seen[recordID]; ok {
			continue
		}
		seen[recordID] = struct{}{}
		normalized = append(normalized, recordID)
	}
	return normalized
}

func normalizeGenerationRecords(records []model.GenerationRecord) []model.GenerationRecord {
	if len(records) == 0 {
		return nil
	}
	seen := make(map[uint]struct{}, len(records))
	normalized := make([]model.GenerationRecord, 0, len(records))
	for _, record := range records {
		if record.Id == 0 {
			continue
		}
		if _, ok := seen[record.Id]; ok {
			continue
		}
		seen[record.Id] = struct{}{}
		normalized = append(normalized, record)
	}
	return normalized
}

func (s *GenerationObjectService) deletePhysicalAssetsForRecords(ctx context.Context, records []model.GenerationRecord, objects []model.GenerationObject) error {
	hasManagedObjects := false
	for _, object := range objects {
		if strings.TrimSpace(object.ObjectKey) != "" && !isLocalGenerationObjectProvider(object.Provider) {
			hasManagedObjects = true
			break
		}
	}

	var (
		store storageService.ObjectStore
	)
	if hasManagedObjects {
		initializedStore, ok, err := storageService.NewObjectStoreFromGeneratorConfig(globals.GraConf.Generator.Storage)
		if err != nil {
			return err
		}
		if !ok || initializedStore == nil {
			return fmt.Errorf("object store not available for deleting managed generation assets")
		}
		store = initializedStore
	}

	deletedObjectKeys := make(map[string]struct{}, len(objects))
	deletedLocalPaths := map[string]struct{}{}
	for _, object := range objects {
		objectKey := strings.TrimSpace(object.ObjectKey)
		if objectKey == "" {
			continue
		}
		if isLocalGenerationObjectProvider(object.Provider) {
			fullPath, ok, err := resolveLocalGeneratedObjectPath(&object)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			if _, seen := deletedLocalPaths[fullPath]; seen {
				continue
			}
			if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("delete local generated asset failed: %w", err)
			}
			deletedLocalPaths[fullPath] = struct{}{}
			continue
		}
		if _, seen := deletedObjectKeys[objectKey]; seen {
			continue
		}
		if !matchesConfiguredObjectStore(store, &object) {
			return fmt.Errorf("managed object store mismatch for %s/%s", strings.TrimSpace(object.Bucket), objectKey)
		}
		if err := deleteGenerationObjectFromStore(ctx, store, objectKey); err != nil {
			return err
		}
		deletedObjectKeys[objectKey] = struct{}{}
	}

	for _, record := range records {
		for _, rawURL := range collectGenerationRecordResultURLs(record) {
			if candidate, ok := buildGenerationObjectBackfillCandidate(globals.GraConf.Generator.Storage, store, store != nil, rawURL); ok {
				if isLocalGenerationObjectProvider(candidate.Provider) {
					fullPath, ok, err := resolveLocalGeneratedResultPath(candidate.PublicURL)
					if err != nil {
						return err
					}
					if ok {
						if _, seen := deletedLocalPaths[fullPath]; !seen {
							if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
								return fmt.Errorf("delete local generated asset failed: %w", err)
							}
							deletedLocalPaths[fullPath] = struct{}{}
						}
						continue
					}
				} else if store != nil {
					if _, seen := deletedObjectKeys[candidate.ObjectKey]; !seen {
						if err := deleteGenerationObjectFromStore(ctx, store, candidate.ObjectKey); err != nil {
							return err
						}
						deletedObjectKeys[candidate.ObjectKey] = struct{}{}
					}
					continue
				}
			}

			fullPath, ok, err := resolveLocalGeneratedResultPath(rawURL)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			if _, seen := deletedLocalPaths[fullPath]; seen {
				continue
			}
			if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("delete local generated asset failed: %w", err)
			}
			deletedLocalPaths[fullPath] = struct{}{}
		}
	}

	return nil
}

func deleteGenerationObjectFromStore(ctx context.Context, store storageService.ObjectStore, objectKey string) error {
	if store == nil {
		return fmt.Errorf("object store not initialized")
	}
	if err := store.Delete(ctx, objectKey); err != nil {
		if isIgnorableObjectDeleteError(err) {
			return nil
		}
		return err
	}
	return nil
}

func isIgnorableObjectDeleteError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "not found") ||
		strings.Contains(lower, "no such key") ||
		strings.Contains(lower, "nosuchkey") ||
		errors.Is(err, os.ErrNotExist)
}

func resolveLocalGeneratedResultPath(rawURL string) (string, bool, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", false, nil
	}

	var candidatePath string
	if strings.HasPrefix(trimmed, "/") {
		candidatePath = trimmed
	} else if parsed, err := url.Parse(trimmed); err == nil && parsed.Path != "" {
		candidatePath = parsed.Path
	}
	if candidatePath == "" {
		return "", false, nil
	}

	localCfg := globals.GraConf.Generator.Storage.Local
	urlPrefix := strings.TrimSpace(localCfg.URLPrefix)
	if urlPrefix == "" {
		urlPrefix = "/uploads/generations"
	}
	if !strings.HasPrefix(candidatePath, urlPrefix) {
		return "", false, nil
	}

	rel := strings.TrimPrefix(candidatePath, urlPrefix)
	rel = strings.TrimPrefix(rel, "/")
	if strings.Contains(rel, "..") || strings.Contains(rel, "~") {
		return "", false, fmt.Errorf("invalid generated asset path")
	}

	cleanRel := path.Clean("/" + rel)
	if strings.HasPrefix(cleanRel, "/..") || cleanRel == "/.." {
		return "", false, fmt.Errorf("invalid generated asset path")
	}

	storagePath := strings.TrimSpace(localCfg.Path)
	if storagePath == "" {
		storagePath = "./uploads/generations"
	}
	fullPath := filepath.Join(storagePath, strings.TrimPrefix(cleanRel, "/"))
	baseAbs, err := filepath.Abs(storagePath)
	if err != nil {
		return "", false, err
	}
	fullAbs, err := filepath.Abs(fullPath)
	if err != nil {
		return "", false, err
	}
	if fullAbs != baseAbs && !strings.HasPrefix(fullAbs, baseAbs+string(os.PathSeparator)) {
		return "", false, fmt.Errorf("invalid generated asset path")
	}

	return fullAbs, true, nil
}

func ResolveLocalGeneratedResultPathForRead(rawURL string) (string, bool, error) {
	return resolveLocalGeneratedResultPath(rawURL)
}

// ResolveLocalGeneratedResultPathForUID resolves a public generated URL to its
// absolute on-disk path only when it lives inside the per-uid jail
// (<storage>/uid/<uid>/...). A cross-uid URL reports ok=false with a nil
// error so callers fall through to the DB lookup (which is uid-scoped and
// returns 404 — avoiding existence disclosure).
func ResolveLocalGeneratedResultPathForUID(rawURL string, uid int) (string, bool, error) {
	fullPath, ok, err := resolveLocalGeneratedResultPath(rawURL)
	if err != nil || !ok {
		return fullPath, ok, err
	}
	if uid <= 0 {
		return "", false, nil
	}

	storagePath := strings.TrimSpace(globals.GraConf.Generator.Storage.Local.Path)
	if storagePath == "" {
		storagePath = "./uploads/generations"
	}
	userRootAbs, err := filepath.Abs(filepath.Join(storagePath, "uid", strconv.Itoa(uid)))
	if err != nil {
		return "", false, err
	}
	if !strings.HasPrefix(fullPath, userRootAbs+string(os.PathSeparator)) {
		return "", false, nil
	}
	return fullPath, true, nil
}

func (s *GenerationObjectService) CleanupOrphanGenerationObjects(ctx context.Context, olderThan time.Duration, limit int) (*CleanupGenerationObjectsResult, error) {
	db := globals.GraDBs["system"]
	if db == nil {
		return &CleanupGenerationObjectsResult{}, nil
	}
	if limit <= 0 {
		limit = 100
	}

	store, ok, err := storageService.NewObjectStoreFromGeneratorConfig(globals.GraConf.Generator.Storage)
	if err != nil {
		return nil, err
	}
	hasManagedStore := ok && store != nil

	query := db.Model(&model.GenerationObject{}).
		Where("status = ?", model.GenerationObjectStatusOrphan).
		Order("created_at ASC").
		Limit(limit)

	if olderThan > 0 {
		query = query.Where("created_at < ?", time.Now().Add(-olderThan))
	}

	var objects []model.GenerationObject
	if err := query.Find(&objects).Error; err != nil {
		return nil, err
	}

	result := &CleanupGenerationObjectsResult{Scanned: len(objects)}
	for _, object := range objects {
		if isLocalGenerationObjectProvider(object.Provider) {
			fullPath, ok, err := resolveLocalGeneratedObjectPath(&object)
			if err != nil {
				return result, err
			}
			if !ok {
				result.Skipped++
				continue
			}
			if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
				return result, fmt.Errorf("delete local orphan object %s failed: %w", object.ObjectKey, err)
			}
		} else {
			if !hasManagedStore || !matchesConfiguredObjectStore(store, &object) {
				result.Skipped++
				continue
			}

			if err := store.Delete(ctx, object.ObjectKey); err != nil {
				return result, fmt.Errorf("delete object %s/%s failed: %w", object.Bucket, object.ObjectKey, err)
			}
		}

		if err := db.Model(&model.GenerationObject{}).
			Where("id = ?", object.Id).
			Update("status", model.GenerationObjectStatusDeleted).Error; err != nil {
			return result, err
		}

		result.Deleted++
	}

	return result, nil
}

func matchesConfiguredObjectStore(store storageService.ObjectStore, object *model.GenerationObject) bool {
	if store == nil || object == nil {
		return false
	}
	if strings.TrimSpace(object.Provider) != "" && strings.TrimSpace(object.Provider) != strings.TrimSpace(store.Provider()) {
		return false
	}
	if strings.TrimSpace(object.Bucket) != "" && strings.TrimSpace(object.Bucket) != strings.TrimSpace(store.Bucket()) {
		return false
	}
	return strings.TrimSpace(object.ObjectKey) != ""
}

func MatchesConfiguredObjectStore(store storageService.ObjectStore, object *model.GenerationObject) bool {
	return matchesConfiguredObjectStore(store, object)
}

func (s *GenerationObjectService) BackfillGenerationObjects(ctx context.Context, afterRecordID uint, limit int, dryRun bool) (*BackfillGenerationObjectsResult, error) {
	db := globals.GraDBs["system"]
	if db == nil {
		return &BackfillGenerationObjectsResult{DryRun: dryRun}, nil
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	store, ok, err := storageService.NewObjectStoreFromGeneratorConfig(globals.GraConf.Generator.Storage)
	if err != nil {
		return nil, err
	}
	hasManagedStore := ok && store != nil

	var records []model.GenerationRecord
	query := db.Model(&model.GenerationRecord{}).
		Where("status = ?", model.STATUS_SUCCESS).
		Order("id ASC").
		Limit(limit)
	if afterRecordID > 0 {
		query = query.Where("id > ?", afterRecordID)
	}
	if err := query.Find(&records).Error; err != nil {
		return nil, err
	}

	result := &BackfillGenerationObjectsResult{
		ScannedRecords: len(records),
		DryRun:         dryRun,
	}
	for _, record := range records {
		result.LastRecordID = record.Id
		candidates := collectGenerationObjectBackfillCandidates(globals.GraConf.Generator.Storage, store, hasManagedStore, record)
		if len(candidates) == 0 {
			continue
		}
		for _, candidate := range candidates {
			result.MatchedURLs++
			exists, err := generationObjectExists(db, candidate.Bucket, candidate.ObjectKey)
			if err != nil {
				return result, err
			}
			if exists {
				result.ExistingObjects++
				continue
			}
			if dryRun {
				result.CreatedObjects++
				continue
			}
			if err := s.Register(&RegisterGenerationObjectParams{
				UID:         record.UID,
				TaskID:      strings.TrimSpace(record.BatchID),
				RecordID:    record.Id,
				ToolID:      strings.TrimSpace(record.ToolID),
				Provider:    strings.TrimSpace(candidate.Provider),
				Bucket:      strings.TrimSpace(candidate.Bucket),
				ObjectKey:   candidate.ObjectKey,
				AssetKind:   candidate.AssetKind,
				ContentType: candidate.ContentType,
				PublicURL:   candidate.PublicURL,
			}); err != nil {
				return result, err
			}
			result.CreatedObjects++
		}
	}
	result.SkippedURLs = result.MatchedURLs - result.CreatedObjects - result.ExistingObjects
	if result.SkippedURLs < 0 {
		result.SkippedURLs = 0
	}
	return result, nil
}

func (s *GenerationObjectService) AuditGenerationObjectCoverage(ctx context.Context, afterRecordID uint, limit int) (*GenerationObjectCoverageAuditResult, error) {
	db := globals.GraDBs["system"]
	if db == nil {
		return &GenerationObjectCoverageAuditResult{}, nil
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	store, ok, err := storageService.NewObjectStoreFromGeneratorConfig(globals.GraConf.Generator.Storage)
	if err != nil {
		return nil, err
	}
	hasManagedStore := ok && store != nil

	var records []model.GenerationRecord
	query := db.Model(&model.GenerationRecord{}).
		Where("status = ?", model.STATUS_SUCCESS).
		Order("id ASC").
		Limit(limit)
	if afterRecordID > 0 {
		query = query.Where("id > ?", afterRecordID)
	}
	if err := query.Find(&records).Error; err != nil {
		return nil, err
	}

	result := &GenerationObjectCoverageAuditResult{
		ScannedRecords: len(records),
		SampledIssues:  make([]GenerationObjectCoverageAuditIssue, 0, 20),
	}

	appendIssue := func(issue GenerationObjectCoverageAuditIssue) {
		if len(result.SampledIssues) >= 20 {
			return
		}
		result.SampledIssues = append(result.SampledIssues, issue)
	}

	for _, record := range records {
		result.LastRecordID = record.Id
		urls := collectGenerationObjectRecordURLs(record)
		for _, rawURL := range urls {
			trimmedURL := strings.TrimSpace(rawURL)
			if trimmedURL == "" {
				continue
			}

			candidate, managed := buildGenerationObjectBackfillCandidate(globals.GraConf.Generator.Storage, store, hasManagedStore, trimmedURL)
			if managed {
				if isLocalGenerationObjectProvider(candidate.Provider) {
					result.LocalURLs++
				} else {
					result.ManagedURLs++
				}
				exists, existsErr := generationObjectExists(db, candidate.Bucket, candidate.ObjectKey)
				if existsErr != nil {
					return result, existsErr
				}
				if exists {
					result.RegisteredURLs++
				} else {
					result.MissingObjects++
					appendIssue(GenerationObjectCoverageAuditIssue{
						RecordID:  record.Id,
						ToolID:    strings.TrimSpace(record.ToolID),
						URL:       candidate.PublicURL,
						Category:  "missing_object",
						ObjectKey: candidate.ObjectKey,
					})
				}
				continue
			}

			if normalized := normalizeGenerationObjectURL(trimmedURL); normalized != "" {
				result.ExternalURLs++
				appendIssue(GenerationObjectCoverageAuditIssue{
					RecordID: record.Id,
					ToolID:   strings.TrimSpace(record.ToolID),
					URL:      normalized,
					Category: "external",
				})
				continue
			}

			result.InvalidURLs++
			appendIssue(GenerationObjectCoverageAuditIssue{
				RecordID: record.Id,
				ToolID:   strings.TrimSpace(record.ToolID),
				URL:      trimmedURL,
				Category: "invalid",
			})
		}
	}

	return result, nil
}

func (s *GenerationObjectService) ResolveTaskDownloadURLs(ctx context.Context, task *model.GenerationTask) {
	if task == nil || task.ResultData == nil {
		return
	}

	featureType := FeatureTypeForToolID(task.ToolID)
	switch featureType {
	case model.TOOL_VIDEO_GENERATOR:
		resolveVideoResultDataURLs(ctx, s, task.RecordID, task.TaskID, task.ResultData)
	}
}

func (s *GenerationObjectService) ResolveRecordDownloadURLs(ctx context.Context, record *model.GenerationRecord) {
	if record == nil {
		return
	}

	featureType := FeatureTypeForToolID(record.ToolID)
	switch featureType {
	case model.TOOL_VIDEO_GENERATOR:
		imageURLs := parseGenerationObjectJSONStringSlice(record.ResultImages)
		resolvedURLs := s.ResolveURLs(ctx, record.Id, record.BatchID, "video", imageURLs)
		record.ResultImages = marshalGenerationObjectJSONStringSlice(resolvedURLs, record.ResultImages)

		metadata := parseGenerationObjectJSONStringMap(record.ResultMetadata)
		resolveVideoMetadataURLs(ctx, s, record.Id, record.BatchID, metadata)
		record.ResultMetadata = marshalGenerationObjectJSONStringMap(metadata, record.ResultMetadata)
	}
}

func (s *GenerationObjectService) ResolveURLs(ctx context.Context, recordID uint, taskID, assetKind string, urls []string) []string {
	if len(urls) == 0 {
		return nil
	}

	objects, err := s.findActiveObjects(assetKind, recordID, taskID)
	if err != nil || len(objects) == 0 {
		if err != nil {
			globals.Warn(fmt.Sprintf("[GenerationObjectService] Resolve %s URLs failed: %v", assetKind, err))
		}
		return append([]string(nil), urls...)
	}

	return resolveGenerationObjectURLs(ctx, objects, urls)
}

func resolveGenerationObjectURLs(ctx context.Context, objects []model.GenerationObject, urls []string) []string {
	if len(urls) == 0 {
		return nil
	}
	if len(objects) == 0 {
		return append([]string(nil), urls...)
	}

	urlByObjectID := make(map[uint]string, len(objects))
	publicURLIndex := make(map[string]*model.GenerationObject, len(objects))
	sourceURLIndex := make(map[string]*model.GenerationObject, len(objects))
	for index := range objects {
		object := &objects[index]
		if trimmed := strings.TrimSpace(object.PublicURL); trimmed != "" {
			publicURLIndex[trimmed] = object
		}
		if trimmed := strings.TrimSpace(object.SourceURL); trimmed != "" {
			sourceURLIndex[trimmed] = object
		}
	}

	resolved := make([]string, 0, len(urls))
	for _, item := range urls {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			resolved = append(resolved, trimmed)
			continue
		}

		matched := publicURLIndex[trimmed]
		if matched == nil {
			matched = sourceURLIndex[trimmed]
		}
		if matched == nil && len(urls) == 1 && len(objects) == 1 {
			matched = &objects[0]
		}
		if matched == nil {
			resolved = append(resolved, trimmed)
			continue
		}

		if cached, ok := urlByObjectID[matched.Id]; ok {
			resolved = append(resolved, cached)
			continue
		}

		downloadURL, err := ResolveGenerationObjectDeliveryURL(ctx, matched, 0)
		if err != nil {
			globals.Warn(fmt.Sprintf("[GenerationObjectService] Resolve download URL for %s/%s failed: %v", matched.Bucket, matched.ObjectKey, err))
			if fallback := strings.TrimSpace(matched.PublicURL); fallback != "" {
				urlByObjectID[matched.Id] = fallback
				resolved = append(resolved, fallback)
				continue
			}
			resolved = append(resolved, trimmed)
			continue
		}
		urlByObjectID[matched.Id] = downloadURL
		resolved = append(resolved, downloadURL)
	}

	return resolved
}

func (s *GenerationObjectService) ResolveURL(ctx context.Context, recordID uint, taskID, assetKind, rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return ""
	}
	resolved := s.ResolveURLs(ctx, recordID, taskID, assetKind, []string{trimmed})
	if len(resolved) == 0 || strings.TrimSpace(resolved[0]) == "" {
		return trimmed
	}
	return strings.TrimSpace(resolved[0])
}

func ResolveGenerationObjectDeliveryURL(ctx context.Context, object *model.GenerationObject, ttl time.Duration) (string, error) {
	return ResolveGenerationObjectDeliveryURLWithDisposition(ctx, object, ttl, "")
}

func ResolveGenerationObjectDeliveryURLWithDisposition(ctx context.Context, object *model.GenerationObject, ttl time.Duration, downloadFilename string) (string, error) {
	if object == nil {
		return "", fmt.Errorf("object is nil")
	}
	if isLocalGenerationObjectProvider(object.Provider) {
		if fallback := strings.TrimSpace(object.PublicURL); fallback != "" {
			return fallback, nil
		}
		return "", fmt.Errorf("local generation object missing public url")
	}
	store, ok, err := storageService.NewObjectStoreForProviderBucket(
		globals.GraConf.Generator.Storage,
		strings.TrimSpace(object.Provider),
		strings.TrimSpace(object.Bucket),
	)
	if err != nil {
		return "", err
	}
	if !ok || store == nil {
		if fallback := strings.TrimSpace(object.PublicURL); fallback != "" {
			return fallback, nil
		}
		return "", fmt.Errorf("object store unavailable for provider=%s bucket=%s", object.Provider, object.Bucket)
	}
	disposition := ""
	if filename := strings.TrimSpace(downloadFilename); filename != "" {
		disposition = storageService.BuildAttachmentContentDisposition(filename)
	}
	return store.DownloadURLWithDisposition(ctx, object.ObjectKey, ttl, disposition)
}

// ResolveSignedDownloadURL returns a download URL for the first matching asset of
// the given recordID/assetKind pair, applying a Content-Disposition attachment
// header keyed by the provided filename when a presigned URL is used.
func (s *GenerationObjectService) ResolveSignedDownloadURL(ctx context.Context, recordID uint, taskID, assetKind, publicURL, filename string, ttl time.Duration) (string, error) {
	trimmedURL := strings.TrimSpace(publicURL)
	objects, err := s.findActiveObjects(assetKind, recordID, taskID)
	if err != nil {
		return "", err
	}
	if len(objects) == 0 {
		return trimmedURL, nil
	}

	var matched *model.GenerationObject
	for index := range objects {
		candidate := &objects[index]
		if trimmedURL != "" {
			if strings.TrimSpace(candidate.PublicURL) == trimmedURL || strings.TrimSpace(candidate.SourceURL) == trimmedURL {
				matched = candidate
				break
			}
		}
	}
	if matched == nil {
		matched = &objects[0]
	}

	resolved, err := ResolveGenerationObjectDeliveryURLWithDisposition(ctx, matched, ttl, filename)
	if err != nil {
		if trimmedURL != "" {
			return trimmedURL, nil
		}
		return "", err
	}
	if strings.TrimSpace(resolved) == "" && trimmedURL != "" {
		return trimmedURL, nil
	}
	return resolved, nil
}

func resolveLocalGeneratedObjectPath(object *model.GenerationObject) (string, bool, error) {
	if object == nil {
		return "", false, nil
	}

	cfg := globals.GraConf.Generator.Storage.Local
	storagePath := strings.TrimSpace(cfg.Path)
	if storagePath == "" {
		storagePath = "./uploads/generations"
	}
	baseAbs, err := filepath.Abs(storagePath)
	if err != nil {
		return "", false, err
	}

	objectKey := strings.Trim(strings.TrimSpace(object.ObjectKey), "/")
	if objectKey != "" {
		if strings.Contains(objectKey, "..") || strings.Contains(objectKey, "~") {
			return "", false, fmt.Errorf("invalid local generation object path")
		}
		fullPath := filepath.Join(storagePath, filepath.FromSlash(objectKey))
		fullAbs, err := filepath.Abs(fullPath)
		if err != nil {
			return "", false, err
		}
		if fullAbs != baseAbs && !strings.HasPrefix(fullAbs, baseAbs+string(os.PathSeparator)) {
			return "", false, fmt.Errorf("invalid local generation object path")
		}
		return fullAbs, true, nil
	}

	return resolveLocalGeneratedResultPath(strings.TrimSpace(object.PublicURL))
}

func isLocalGenerationObjectProvider(provider string) bool {
	return strings.EqualFold(strings.TrimSpace(provider), "local")
}

func (s *GenerationObjectService) findActiveObjects(assetKind string, recordID uint, taskID string) ([]model.GenerationObject, error) {
	db := globals.GraDBs["system"]
	if db == nil {
		return nil, nil
	}

	assetKind = strings.TrimSpace(assetKind)
	taskID = strings.TrimSpace(taskID)
	if assetKind == "" || (recordID == 0 && taskID == "") {
		return nil, nil
	}

	query := db.Model(&model.GenerationObject{}).
		Where("asset_kind = ? AND status IN ?", assetKind, []int8{model.GenerationObjectStatusActive, model.GenerationObjectStatusHidden}).
		Order("id ASC")

	var objects []model.GenerationObject
	if recordID > 0 {
		if err := query.Where("record_id = ?", recordID).Find(&objects).Error; err != nil {
			return nil, err
		}
		if len(objects) > 0 || taskID == "" {
			return objects, nil
		}
	}

	if taskID == "" {
		return objects, nil
	}
	if err := query.Where("task_id = ?", taskID).Find(&objects).Error; err != nil {
		return nil, err
	}
	return objects, nil
}

func resolveVideoResultDataURLs(ctx context.Context, service *GenerationObjectService, recordID uint, taskID string, resultData model.JSONMap) {
	if resultData == nil {
		return
	}
	if videoURLs := ParseVideoURLs(resultData); len(videoURLs) > 0 {
		resultData["videoUrls"] = service.ResolveURLs(ctx, recordID, taskID, "video", videoURLs)
	}
	if metadata := extractGenerationObjectMetadataMap(resultData["resultMetadata"]); metadata != nil {
		resolveVideoMetadataURLs(ctx, service, recordID, taskID, metadata)
		resultData["resultMetadata"] = metadata
	}
}

func resolveVideoMetadataURLs(ctx context.Context, service *GenerationObjectService, recordID uint, taskID string, metadata map[string]interface{}) {
	if metadata == nil {
		return
	}
	if value := extractGenerationObjectStringMapValue(metadata, "videoUrl"); value != "" {
		metadata["videoUrl"] = service.ResolveURL(ctx, recordID, taskID, "video", value)
	}
	if urls := extractGenerationObjectStringSlice(metadata["videoUrls"]); len(urls) > 0 {
		metadata["videoUrls"] = service.ResolveURLs(ctx, recordID, taskID, "video", urls)
	}
}

func parseGenerationObjectJSONStringMap(raw string) map[string]interface{} {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]interface{}{}
	}
	result := map[string]interface{}{}
	if err := json.Unmarshal([]byte(trimmed), &result); err != nil {
		return map[string]interface{}{}
	}
	return result
}

func parseGenerationObjectJSONStringSlice(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	var result []string
	if err := json.Unmarshal([]byte(trimmed), &result); err != nil {
		return nil
	}
	return result
}

func marshalGenerationObjectJSONStringMap(value map[string]interface{}, fallback string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fallback
	}
	return string(encoded)
}

func marshalGenerationObjectJSONStringSlice(value []string, fallback string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fallback
	}
	return string(encoded)
}

func extractGenerationObjectMetadataMap(raw interface{}) map[string]interface{} {
	switch typed := raw.(type) {
	case map[string]interface{}:
		return typed
	case model.JSONMap:
		return map[string]interface{}(typed)
	default:
		return nil
	}
}

func extractGenerationObjectNestedMap(container map[string]interface{}, key string) map[string]interface{} {
	if container == nil {
		return nil
	}
	return extractGenerationObjectMetadataMap(container[key])
}

func extractGenerationObjectStringMapValue(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	value, ok := m[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return ""
	}
}

func extractGenerationObjectStringSlice(raw interface{}) []string {
	switch typed := raw.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []interface{}:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if value, ok := item.(string); ok {
				trimmed := strings.TrimSpace(value)
				if trimmed != "" {
					result = append(result, trimmed)
				}
			}
		}
		return result
	default:
		return nil
	}
}

func extractGenerationObjectMapSlice(raw interface{}) []map[string]interface{} {
	items, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	result := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		if mapped, ok := item.(map[string]interface{}); ok {
			result = append(result, mapped)
		}
	}
	return result
}

func collectGenerationObjectBackfillCandidates(storageCfg config.StorageConfig, store storageService.ObjectStore, hasManagedStore bool, record model.GenerationRecord) []generationObjectBackfillCandidate {
	seen := map[string]struct{}{}
	candidates := make([]generationObjectBackfillCandidate, 0)
	for _, rawURL := range collectGenerationObjectRecordURLs(record) {
		candidate, ok := buildGenerationObjectBackfillCandidate(storageCfg, store, hasManagedStore, rawURL)
		if !ok {
			continue
		}
		seenKey := strings.TrimSpace(candidate.Provider) + "|" + strings.TrimSpace(candidate.Bucket) + "|" + strings.TrimSpace(candidate.ObjectKey)
		if _, exists := seen[seenKey]; exists {
			continue
		}
		seen[seenKey] = struct{}{}
		candidates = append(candidates, candidate)
	}

	return candidates
}

func collectGenerationObjectRecordURLs(record model.GenerationRecord) []string {
	seen := map[string]struct{}{}
	urls := make([]string, 0)
	appendURL := func(rawURL string) {
		trimmed := strings.TrimSpace(rawURL)
		if trimmed == "" {
			return
		}
		if _, exists := seen[trimmed]; exists {
			return
		}
		seen[trimmed] = struct{}{}
		urls = append(urls, trimmed)
	}

	for _, item := range parseGenerationObjectJSONStringSlice(record.ResultImages) {
		appendURL(item)
	}

	metadata := parseGenerationObjectJSONStringMap(record.ResultMetadata)
	appendGenerationObjectURLsFromMap(metadata, appendURL)

	inputFiles := parseGenerationObjectJSONStringMap(record.InputFiles)
	appendGenerationObjectURLsFromMap(inputFiles, appendURL)
	appendGenerationObjectReferenceURLs(inputFiles["referenceImages"], appendURL)

	return urls
}

func collectGenerationRecordResultURLs(record model.GenerationRecord) []string {
	seen := map[string]struct{}{}
	urls := make([]string, 0)
	appendURL := func(rawURL string) {
		trimmed := strings.TrimSpace(rawURL)
		if trimmed == "" {
			return
		}
		if _, exists := seen[trimmed]; exists {
			return
		}
		seen[trimmed] = struct{}{}
		urls = append(urls, trimmed)
	}

	for _, item := range parseGenerationObjectJSONStringSlice(record.ResultImages) {
		appendURL(item)
	}

	var metadata interface{}
	if trimmed := strings.TrimSpace(record.ResultMetadata); trimmed != "" {
		_ = json.Unmarshal([]byte(trimmed), &metadata)
		appendURLStringsRecursively(metadata, appendURL)
	}

	return urls
}

func appendURLStringsRecursively(value interface{}, appendURL func(string)) {
	switch typed := value.(type) {
	case string:
		if looksLikeGeneratedAssetURL(typed) {
			appendURL(typed)
		}
	case map[string]interface{}:
		for _, item := range typed {
			appendURLStringsRecursively(item, appendURL)
		}
	case []interface{}:
		for _, item := range typed {
			appendURLStringsRecursively(item, appendURL)
		}
	}
}

func looksLikeGeneratedAssetURL(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "/uploads/") {
		return true
	}
	normalized := normalizeGenerationObjectURL(trimmed)
	if normalized == "" {
		return false
	}
	parsed, err := url.Parse(normalized)
	if err != nil {
		return false
	}
	return strings.HasPrefix(parsed.Path, "/uploads/") || strings.Contains(parsed.Path, "/generations/")
}

func generationObjectURLPrefixes(store storageService.ObjectStore) []string {
	marker := "__codex_storage_probe__"
	base := strings.TrimSuffix(strings.TrimSpace(store.PublicURL(marker)), marker)
	seen := map[string]struct{}{}
	prefixes := make([]string, 0, 3)
	appendPrefix := func(value string) {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return
		}
		trimmed = strings.TrimRight(trimmed, "/") + "/"
		if _, ok := seen[trimmed]; ok {
			return
		}
		seen[trimmed] = struct{}{}
		prefixes = append(prefixes, trimmed)
	}
	appendPrefix(base)
	return prefixes
}

func buildGenerationObjectBackfillCandidate(storageCfg config.StorageConfig, store storageService.ObjectStore, hasManagedStore bool, rawURL string) (generationObjectBackfillCandidate, bool) {
	if fullPath, ok, err := resolveLocalGeneratedResultPath(rawURL); err == nil && ok {
		storagePath := strings.TrimSpace(storageCfg.Local.Path)
		if storagePath == "" {
			storagePath = "./uploads/generations"
		}
		baseAbs, absErr := filepath.Abs(storagePath)
		if absErr == nil {
			relPath, relErr := filepath.Rel(baseAbs, fullPath)
			if relErr == nil {
				objectKey := filepath.ToSlash(strings.Trim(strings.TrimSpace(relPath), "/"))
				if objectKey != "" && !strings.HasPrefix(objectKey, "..") {
					assetKind := inferGenerationObjectAssetKind(objectKey)
					return generationObjectBackfillCandidate{
						Provider:    "local",
						Bucket:      "local",
						PublicURL:   strings.TrimSpace(rawURL),
						ObjectKey:   objectKey,
						AssetKind:   assetKind,
						ContentType: inferGenerationObjectContentType(objectKey, assetKind),
					}, true
				}
			}
		}
	}

	if !hasManagedStore || store == nil {
		return generationObjectBackfillCandidate{}, false
	}

	prefixes := generationObjectURLPrefixes(store)
	normalizedURL := normalizeGenerationObjectURL(rawURL)
	if normalizedURL == "" {
		return generationObjectBackfillCandidate{}, false
	}
	for _, prefix := range prefixes {
		if !strings.HasPrefix(normalizedURL, prefix) {
			continue
		}
		objectKey := strings.Trim(strings.TrimPrefix(normalizedURL, prefix), "/")
		if objectKey == "" {
			return generationObjectBackfillCandidate{}, false
		}
		assetKind := inferGenerationObjectAssetKind(objectKey)
		return generationObjectBackfillCandidate{
			Provider:    strings.TrimSpace(store.Provider()),
			Bucket:      strings.TrimSpace(store.Bucket()),
			PublicURL:   normalizedURL,
			ObjectKey:   objectKey,
			AssetKind:   assetKind,
			ContentType: inferGenerationObjectContentType(objectKey, assetKind),
		}, true
	}
	return generationObjectBackfillCandidate{}, false
}

func appendGenerationObjectURLsFromMap(data map[string]interface{}, appendCandidate func(string)) {
	if data == nil {
		return
	}
	for _, key := range []string{"videoUrl", "thumbnailUrl", "panoramaUrl", "preferredAssetUrl", "imageUrl", "sourceImageUrl", "url"} {
		if value := extractGenerationObjectStringMapValue(data, key); value != "" {
			appendCandidate(value)
		}
	}
	for _, key := range []string{"videoUrls", "assetUrls", "imageUrls"} {
		for _, item := range extractGenerationObjectStringSlice(data[key]) {
			appendCandidate(item)
		}
	}
	for _, nestedKey := range []string{"video", "panorama"} {
		if nested := extractGenerationObjectNestedMap(data, nestedKey); nested != nil {
			appendGenerationObjectURLsFromMap(nested, appendCandidate)
		}
	}
	for _, historyKey := range []string{"videoHistory", "panoramaHistory", "refinements"} {
		for _, item := range extractGenerationObjectMapSlice(data[historyKey]) {
			appendGenerationObjectURLsFromMap(item, appendCandidate)
		}
	}
}

func appendGenerationObjectReferenceURLs(raw interface{}, appendCandidate func(string)) {
	items, ok := raw.([]interface{})
	if !ok {
		return
	}
	for _, item := range items {
		mapped, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if value := extractGenerationObjectStringMapValue(mapped, "url"); value != "" {
			appendCandidate(value)
		}
	}
}

func normalizeGenerationObjectURL(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func inferGenerationObjectAssetKind(objectKey string) string {
	key := strings.Trim(strings.ToLower(strings.TrimSpace(objectKey)), "/")
	switch {
	case strings.HasPrefix(key, "reference-images/") || strings.Contains(key, "/reference-images/"):
		return "reference"
	case strings.HasPrefix(key, "assets/") || strings.Contains(key, "/assets/"):
		return "asset"
	case strings.HasPrefix(key, "videos/") || strings.Contains(key, "/videos/"):
		base := path.Base(key)
		if strings.Contains(base, "thumb") || strings.Contains(base, "thumbnail") {
			return "thumbnail"
		}
		ext := strings.ToLower(path.Ext(base))
		switch ext {
		case ".png", ".jpg", ".jpeg", ".webp", ".gif":
			return "thumbnail"
		default:
			return "video"
		}
	default:
		return "image"
	}
}

func inferGenerationObjectContentType(objectKey, assetKind string) string {
	ext := strings.ToLower(path.Ext(strings.TrimSpace(objectKey)))
	if ext != "" {
		if contentType := mime.TypeByExtension(ext); contentType != "" {
			return contentType
		}
	}
	switch strings.TrimSpace(assetKind) {
	case "video":
		return "video/mp4"
	case "thumbnail", "image", "reference":
		return "image/png"
	case "asset":
		switch ext {
		case ".glb":
			return "model/gltf-binary"
		case ".gltf":
			return "model/gltf+json"
		case ".obj":
			return "model/obj"
		case ".fbx":
			return "application/octet-stream"
		case ".stl":
			return "model/stl"
		case ".usdz":
			return "model/vnd.usdz+zip"
		case ".zip":
			return "application/zip"
		}
	}
	return "application/octet-stream"
}

func generationObjectExists(db *gorm.DB, bucket, objectKey string) (bool, error) {
	if db == nil || strings.TrimSpace(bucket) == "" || strings.TrimSpace(objectKey) == "" {
		return false, nil
	}
	var count int64
	if err := db.Model(&model.GenerationObject{}).
		Where("bucket = ? AND object_key = ?", strings.TrimSpace(bucket), strings.TrimSpace(objectKey)).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}
