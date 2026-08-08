package workagent

// file_service_workspace_sync.go owns the filesystem-side concerns:
// scan-and-sync the outputs/ directory, register one file from disk,
// verify on-disk presence, and the orphan/source-bulk-delete paths.
// Plus the small filename-MIME helper that those flows use.
//
// Pulled out of file_service.go (B2 chunk 2). All methods stay on
// FileService receiver, same package, so existing call sites work
// unchanged. The asset-ledger upserts that ride alongside these
// flows are preserved as-is — moving them to AfterCreate hooks is
// the next chunk of B2 (not done in this file-organisation pass).

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"server/globals"
	workagentModel "server/model/workagent"
	assetLedgerService "server/service/assetledger"
	globalAssetService "server/service/globalasset"

	"gorm.io/gorm"
)

func (s *FileService) SyncOutputFiles(uid int, threadID uint, threadWorkspace string, threadUUID string) (*SyncResult, error) {
	defer s.lockForThread(threadID)()

	startTime := time.Now()
	result := &SyncResult{}

	mgr := GetAgentClientManager()
	workspaceRoot := ""
	if mgr != nil {
		workspaceRoot = mgr.WorkspaceRoot()
	}

	// Defence-in-depth: refuse to walk a threadWorkspace that isn't
	// inside the configured workspace root. The api/ caller derives
	// it from the manager + thread metadata, so this should always
	// be true today — but a future code path or admin endpoint that
	// accepts an arbitrary path would otherwise let filepath.Walk
	// scan, batch-insert, and asset-ledger every file under the
	// supplied directory. With workspaceRoot empty (init-not-done)
	// we stay permissive — better than blocking legit chats during
	// startup races — and rely on downstream ResolveInsideWorkspace
	// to refuse path leakage at read time.
	if workspaceRoot != "" && !confinedToWorkspaceRoot(workspaceRoot, threadWorkspace) {
		return nil, fmt.Errorf("threadWorkspace not inside workspaceRoot: %s", threadWorkspace)
	}

	outputsPath := filepath.Join(threadWorkspace, "outputs")
	if _, err := os.Stat(outputsPath); os.IsNotExist(err) {
		result.Duration = time.Since(startTime).String()
		return result, nil
	}
	variationManifest := loadVariationDraftManifest(outputsPath)
	variationDrafts := variationManifest.Drafts
	for _, msg := range variationManifest.Errors {
		result.Errors = append(result.Errors, msg)
	}

	// Get existing output files from database
	var existingFiles []workagentModel.ThreadFile
	if err := globals.GraDBs["system"].
		Where("uid = ? AND thread_id = ? AND file_source = ?", uid, threadID, workagentModel.FileSourceOutput).
		Find(&existingFiles).Error; err != nil {
		return nil, fmt.Errorf("failed to get existing files: %w", err)
	}
	var thread workagentModel.ChatThread
	// Surface DB issues. If the lookup fails (transient DB error,
	// thread genuinely missing), the asset-ledger upsert below still
	// runs but with an empty thread → ledger rows land with no UUID
	// or name and are awkward to reconcile after the fact. Keep the
	// non-fatal posture (ledger is best-effort) but log so the
	// degradation isn't invisible.
	if err := globals.GraDBs["system"].Where("id = ? AND uid = ?", threadID, uid).First(&thread).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		globals.Warn(fmt.Sprintf("[FileService] SyncOutputFiles thread lookup failed for thread=%d uid=%d: %v (asset ledger entries will lack thread UUID/name)", threadID, uid, err))
	}

	existingFileMap := make(map[string]*workagentModel.ThreadFile)
	for i := range existingFiles {
		existingFileMap[existingFiles[i].FilePath] = &existingFiles[i]
	}

	// Walk-and-collect: never write to DB inside the closure. New rows
	// go into newFiles for a single CreateInBatches call after the
	// walk; existing rows whose size/exists-flag changed go into the
	// updates slice for sequential Save().
	filesOnDisk := make(map[string]bool)
	outputFilesOnDisk := make(map[string]bool)
	newFiles := make([]workagentModel.ThreadFile, 0)
	updates := make([]*workagentModel.ThreadFile, 0)

	err := filepath.Walk(outputsPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("walk error: %v", err))
			return nil
		}
		if info.IsDir() {
			if path != outputsPath && strings.HasPrefix(filepath.Base(path), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(filepath.Base(path), ".") {
			return nil
		}

		var relPath string
		if workspaceRoot != "" {
			var relErr error
			relPath, relErr = filepath.Rel(workspaceRoot, path)
			if relErr != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("rel path error: %v", relErr))
				return nil
			}
		} else {
			relToThreadWorkspace, relErr := filepath.Rel(threadWorkspace, path)
			if relErr != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("rel path error: %v", relErr))
				return nil
			}
			relPath = filepath.Join(fmt.Sprintf("thread_%s", threadUUID), relToThreadWorkspace)
		}
		relPath = filepath.ToSlash(relPath)
		outputRel, outputRelErr := filepath.Rel(outputsPath, path)
		if outputRelErr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("output rel path error: %v", outputRelErr))
			return nil
		}
		outputRel = filepath.ToSlash(outputRel)
		variationDescription := variationDraftDescription(variationDrafts[outputRel])
		filesOnDisk[relPath] = true
		outputFilesOnDisk[outputRel] = true

		if existingFile, exists := existingFileMap[relPath]; exists {
			if existingFile.FileSize != uint64(info.Size()) || !existingFile.ExistsOnDisk || (variationDescription != "" && existingFile.Description != variationDescription) {
				existingFile.FileSize = uint64(info.Size())
				existingFile.ExistsOnDisk = true
				if variationDescription != "" {
					existingFile.Description = variationDescription
				}
				updates = append(updates, existingFile)
			}
			return nil
		}

		newFiles = append(newFiles, workagentModel.ThreadFile{
			UID:          uid,
			ThreadID:     threadID,
			FileName:     info.Name(),
			DisplayName:  info.Name(),
			FileSize:     uint64(info.Size()),
			FileType:     string(workagentModel.FileTypeWriteagentOutput),
			MimeType:     getMimeTypeFromFilename(info.Name()),
			FilePath:     relPath,
			FileSource:   workagentModel.FileSourceOutput,
			Description:  variationDescription,
			ExistsOnDisk: true,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to walk outputs directory: %w", err)
	}
	for _, msg := range validateVariationDraftManifest(variationDrafts, outputFilesOnDisk) {
		result.Errors = append(result.Errors, msg)
	}
	if !variationManifest.Exists && len(outputFilesOnDisk) > 0 && loadPassModeState(threadID).Mode == WorkAgentPassModeDraft {
		result.Errors = append(result.Errors, "draft pass missing outputs/.workagent/pass_1_variations.json manifest")
	}

	// Batch-insert new rows. CreateInBatches(50) keeps the SQL packet
	// size under MySQL's max_allowed_packet for any reasonable burst.
	if len(newFiles) > 0 {
		if err := globals.GraDBs["system"].CreateInBatches(&newFiles, 50).Error; err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("batch create error: %v", err))
		} else {
			result.Added = len(newFiles)
			// Ledger/global-asset sync needs the committed ID, so it runs
			// after CreateInBatches has populated newFiles[i].Id.
			for i := range newFiles {
				if err := syncThreadFileGlobalAssetAndLedger(globals.GraDBs["system"], &newFiles[i], &thread); err != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("asset bridge sync error for %s: %v", newFiles[i].FilePath, err))
				}
				if _, err := NewArtifactRegistryRepository(globals.GraDBs["system"]).UpsertFromThreadFile(&newFiles[i]); err != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("artifact registry sync error for %s: %v", newFiles[i].FilePath, err))
				}
			}
		}
	}

	// Apply queued updates. Save remains row-by-row because we need
	// per-row error reporting, and updates are typically rare relative
	// to inserts during a fresh sync.
	for _, existingFile := range updates {
		if err := globals.GraDBs["system"].Save(existingFile).Error; err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("update error for %s: %v", existingFile.FilePath, err))
		} else {
			if _, err := NewArtifactRegistryRepository(globals.GraDBs["system"]).UpsertFromThreadFile(existingFile); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("artifact registry sync error for %s: %v", existingFile.FilePath, err))
			}
			result.Updated++
		}
	}

	// Mark files as not existing if they're not on disk
	for relPath, existingFile := range existingFileMap {
		if !filesOnDisk[relPath] && existingFile.ExistsOnDisk {
			existingFile.ExistsOnDisk = false
			if err := globals.GraDBs["system"].Save(existingFile).Error; err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("mark removed error for %s: %v", relPath, err))
			} else {
				result.Removed++
			}
		}
	}

	result.Duration = time.Since(startTime).String()
	globals.Info(fmt.Sprintf("[FileService] Sync completed for thread %d: added=%d, updated=%d, removed=%d",
		threadID, result.Added, result.Updated, result.Removed))

	return result, nil
}

type variationDraftManifest struct {
	Variations []variationDraftManifestEntry `json:"variations"`
}

type variationDraftManifestEntry struct {
	ID                   string `json:"id"`
	Label                string `json:"label"`
	Stance               string `json:"stance"`
	FilePath             string `json:"file_path"`
	DesignSystemBasename string `json:"design_system_basename"`
	AssetContract        string `json:"asset_contract"`
}

type variationDraftManifestLoadResult struct {
	Drafts map[string]variationDraftManifestEntry
	Exists bool
	Errors []string
}

func loadVariationDraftManifest(outputsPath string) variationDraftManifestLoadResult {
	result := variationDraftManifestLoadResult{Drafts: map[string]variationDraftManifestEntry{}}
	if strings.TrimSpace(outputsPath) == "" {
		return result
	}
	manifestPath := filepath.Join(outputsPath, ".workagent", "pass_1_variations.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return result
	}
	result.Exists = true
	var manifest variationDraftManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		msg := fmt.Sprintf("invalid pass_1_variations manifest: %v", err)
		globals.Warn(fmt.Sprintf("[FileService] %s", msg))
		result.Errors = append(result.Errors, msg)
		return result
	}
	for _, item := range manifest.Variations {
		id := strings.TrimSpace(item.ID)
		filePath := cleanVariationDraftManifestPath(item.FilePath)
		if id == "" || filePath == "" {
			result.Errors = append(result.Errors, "variation draft manifest entry requires id and file_path")
			continue
		}
		item.ID = id
		item.Label = strings.TrimSpace(item.Label)
		item.Stance = strings.TrimSpace(item.Stance)
		item.FilePath = filePath
		item.DesignSystemBasename = strings.TrimSpace(item.DesignSystemBasename)
		item.AssetContract = strings.TrimSpace(item.AssetContract)
		if previous, exists := result.Drafts[filePath]; exists {
			result.Errors = append(result.Errors, fmt.Sprintf("variation draft manifest repeats file_path %q for variation %q and %q", filePath, previous.ID, id))
			continue
		}
		result.Drafts[filePath] = item
	}
	return result
}

func validateVariationDraftManifest(manifest map[string]variationDraftManifestEntry, outputFilesOnDisk map[string]bool) []string {
	if len(manifest) == 0 {
		return nil
	}
	var errs []string
	seenIDs := map[string]string{}
	for filePath, item := range manifest {
		id := strings.TrimSpace(item.ID)
		if id != "" {
			if previousPath := seenIDs[id]; previousPath != "" {
				errs = append(errs, fmt.Sprintf("variation draft manifest repeats variation id %q for %s and %s", id, previousPath, filePath))
			} else {
				seenIDs[id] = filePath
			}
		}
		if !outputFilesOnDisk[filePath] {
			errs = append(errs, fmt.Sprintf("variation draft manifest references missing file %q for variation %q", filePath, id))
		}
	}
	return errs
}

func cleanVariationDraftManifestPath(raw string) string {
	clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(raw)))
	clean = strings.TrimPrefix(clean, "./")
	clean = strings.TrimPrefix(clean, "outputs/")
	if clean == "." || clean == "" || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return ""
	}
	return clean
}

func variationDraftDescription(item variationDraftManifestEntry) string {
	if strings.TrimSpace(item.ID) == "" {
		return ""
	}
	payload := map[string]string{
		"kind":         "workagent_variation_draft",
		"variation_id": strings.TrimSpace(item.ID),
	}
	if strings.TrimSpace(item.Label) != "" {
		payload["label"] = strings.TrimSpace(item.Label)
	}
	if strings.TrimSpace(item.Stance) != "" {
		payload["stance"] = strings.TrimSpace(item.Stance)
	}
	if strings.TrimSpace(item.DesignSystemBasename) != "" {
		payload["design_system_basename"] = strings.TrimSpace(item.DesignSystemBasename)
	}
	if strings.TrimSpace(item.AssetContract) != "" {
		payload["asset_contract"] = strings.TrimSpace(item.AssetContract)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(data)
}

func VariationDraftDescriptionForOutput(threadWorkspace string, outputPath string) string {
	outputsPath := filepath.Join(strings.TrimSpace(threadWorkspace), "outputs")
	if strings.TrimSpace(threadWorkspace) == "" || strings.TrimSpace(outputPath) == "" {
		return ""
	}
	rel, err := filepath.Rel(outputsPath, outputPath)
	if err != nil {
		return ""
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || strings.HasPrefix(rel, "../") || strings.HasPrefix(rel, "/") {
		return ""
	}
	manifest := loadVariationDraftManifest(outputsPath)
	return variationDraftDescription(manifest.Drafts[rel])
}

func VariationDraftManifestIssuesForOutputScan(threadWorkspace string, threadID uint, generatedOutputPaths []string) []string {
	outputsPath := filepath.Join(strings.TrimSpace(threadWorkspace), "outputs")
	if strings.TrimSpace(threadWorkspace) == "" || threadID == 0 || len(generatedOutputPaths) == 0 {
		return nil
	}
	manifest := loadVariationDraftManifest(outputsPath)
	outputFilesOnDisk := collectVariationOutputFilesOnDisk(outputsPath)
	issues := append([]string{}, manifest.Errors...)
	issues = append(issues, validateVariationDraftManifest(manifest.Drafts, outputFilesOnDisk)...)
	if !manifest.Exists && loadPassModeState(threadID).Mode == WorkAgentPassModeDraft {
		issues = append(issues, "draft pass missing outputs/.workagent/pass_1_variations.json manifest")
	}
	return issues
}

func collectVariationOutputFilesOnDisk(outputsPath string) map[string]bool {
	files := map[string]bool{}
	if strings.TrimSpace(outputsPath) == "" {
		return files
	}
	_ = filepath.Walk(outputsPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if path != outputsPath && strings.HasPrefix(filepath.Base(path), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(filepath.Base(path), ".") {
			return nil
		}
		rel, err := filepath.Rel(outputsPath, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel != "." && !strings.HasPrefix(rel, "../") && !strings.HasPrefix(rel, "/") {
			files[rel] = true
		}
		return nil
	})
	return files
}

// RegisterOutputFile registers a new output file in the database.
//
// absolutePath MUST resolve inside the configured workspace root.
// filepath.Rel doesn't error on out-of-tree paths — it just produces
// a "../../foo" relative path — so the only thing stopping a caller
// from registering /etc/passwd as a "thread output" is an explicit
// confinement check. Without that, downstream readers like
// ResolveInsideWorkspace would refuse to serve the row, but the DB +
// asset ledger would still carry the poisoned record.
func (s *FileService) RegisterOutputFile(uid int, threadID uint, absolutePath string, description string) (*workagentModel.ThreadFile, error) {
	info, err := os.Stat(absolutePath)
	if err != nil {
		return nil, fmt.Errorf("file not found: %w", err)
	}

	// Get workspace base path and refuse out-of-root targets.
	manager := GetAgentClientManager()
	workspaceBase := manager.WorkspaceRoot()
	if workspaceBase == "" {
		return nil, fmt.Errorf("workspace root not configured; refusing to register %s", absolutePath)
	}
	if !confinedToWorkspaceRoot(workspaceBase, absolutePath) {
		return nil, fmt.Errorf("refusing to register output file outside workspace root: %s", absolutePath)
	}
	relPath, err := filepath.Rel(workspaceBase, absolutePath)
	if err != nil {
		relPath = absolutePath
	}

	mimeType := getMimeTypeFromFilename(info.Name())

	// Check if file already exists. Surface DB lookup failures —
	// silent swallow here meant ledger upserts later carried empty
	// UUID/Name and were hard to reconcile. Same posture as
	// SyncOutputFiles: warn but don't abort.
	var thread workagentModel.ChatThread
	if err := globals.GraDBs["system"].Where("id = ? AND uid = ?", threadID, uid).First(&thread).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		globals.Warn(fmt.Sprintf("[FileService] RegisterOutputFile thread lookup failed for thread=%d uid=%d: %v (asset ledger will lack thread UUID/name)", threadID, uid, err))
	}
	var existingFile workagentModel.ThreadFile
	err = globals.GraDBs["system"].
		Where("uid = ? AND thread_id = ? AND file_path = ?", uid, threadID, relPath).
		First(&existingFile).Error

	if err == nil {
		// Update existing file
		existingFile.FileSize = uint64(info.Size())
		existingFile.ExistsOnDisk = true
		if description != "" {
			existingFile.Description = description
		}
		if err := globals.GraDBs["system"].Save(&existingFile).Error; err != nil {
			return nil, fmt.Errorf("failed to update file: %w", err)
		}
		if err := syncThreadFileGlobalAssetAndLedger(globals.GraDBs["system"], &existingFile, &thread); err != nil {
			return nil, fmt.Errorf("failed to sync asset bridge: %w", err)
		}
		if _, err := NewArtifactRegistryRepository(globals.GraDBs["system"]).UpsertFromThreadFile(&existingFile); err != nil {
			return nil, fmt.Errorf("failed to sync artifact registry: %w", err)
		}
		return &existingFile, nil
	}

	if err != gorm.ErrRecordNotFound {
		return nil, fmt.Errorf("failed to check existing file: %w", err)
	}

	// Create new file record
	newFile := workagentModel.ThreadFile{
		UID:          uid,
		ThreadID:     threadID,
		FileName:     info.Name(),
		DisplayName:  info.Name(),
		FileSize:     uint64(info.Size()),
		FileType:     string(workagentModel.FileTypeWriteagentOutput),
		MimeType:     mimeType,
		FilePath:     relPath,
		FileSource:   workagentModel.FileSourceOutput,
		Description:  description,
		ExistsOnDisk: true,
	}

	if err := globals.GraDBs["system"].Create(&newFile).Error; err != nil {
		return nil, fmt.Errorf("failed to create file record: %w", err)
	}
	if err := syncThreadFileGlobalAssetAndLedger(globals.GraDBs["system"], &newFile, &thread); err != nil {
		return nil, fmt.Errorf("failed to sync asset bridge: %w", err)
	}
	if _, err := NewArtifactRegistryRepository(globals.GraDBs["system"]).UpsertFromThreadFile(&newFile); err != nil {
		return nil, fmt.Errorf("failed to sync artifact registry: %w", err)
	}

	globals.Info(fmt.Sprintf("[FileService] Registered output file: %s for thread %d", info.Name(), threadID))
	return &newFile, nil
}

// =============================================================================
// File Verification and Cleanup
// =============================================================================

// VerifyFileExists checks if a file exists on disk and updates the database.
//
// The constructed pathToCheck = workspacePath + file.FilePath must
// stay inside workspacePath. file.FilePath comes from a DB row that
// could in theory carry a "../../foo" relative path (legacy data,
// future bug, partial migration). Without confinement, the os.Stat
// here would silently report on system files and the result would
// leak existence via the boolean return. Refuse out-of-root paths
// up front and treat them as "not exists" — the DB's exists_on_disk
// flag flips to false and the row gets cleaned up on the next sweep.
func (s *FileService) VerifyFileExists(fileID uint, workspacePath string) (bool, error) {
	var file workagentModel.ThreadFile
	if err := globals.GraDBs["system"].First(&file, fileID).Error; err != nil {
		return false, fmt.Errorf("file not found in database: %w", err)
	}

	// Construct absolute path from workspace + relative FilePath
	pathToCheck := ""
	if file.FilePath != "" && workspacePath != "" {
		pathToCheck = filepath.Join(workspacePath, file.FilePath)
	}

	exists := true
	if pathToCheck == "" {
		exists = false
	} else if !confinedToWorkspaceRoot(workspacePath, pathToCheck) {
		// Path traversal would escape — treat as "doesn't exist"
		// rather than stat'ing a system file.
		exists = false
	} else if _, err := os.Stat(pathToCheck); os.IsNotExist(err) {
		exists = false
	}

	if file.ExistsOnDisk != exists {
		file.ExistsOnDisk = exists
		if err := globals.GraDBs["system"].Save(&file).Error; err != nil {
			return exists, fmt.Errorf("failed to update file status: %w", err)
		}
	}

	return exists, nil
}

// DeleteOrphanedRecords removes database records for files that no longer
// exist on disk. The previous implementation ran SELECT, DELETE, and the
// per-file ledger cleanup as three independent statements: a midway
// failure on row N of M left rows 1..N-1's ThreadFile gone but their
// ledger entries deleted, and rows N..M's ThreadFile rows ALSO gone
// (the bulk DELETE already ran) but with their ledger entries still
// alive — pointing at file_ids that no longer exist. Wrap the whole
// flow in a single transaction so an error rolls back atomically.
func (s *FileService) DeleteOrphanedRecords(uid int, threadID uint) (int, error) {
	var rowsAffected int64
	err := globals.GraDBs["system"].Transaction(func(tx *gorm.DB) error {
		var files []workagentModel.ThreadFile
		if err := tx.
			Where("uid = ? AND thread_id = ? AND exists_on_disk = ?", uid, threadID, false).
			Find(&files).Error; err != nil {
			return fmt.Errorf("failed to list orphaned records: %w", err)
		}
		if len(files) == 0 {
			return nil
		}
		fileIDs := make([]uint, 0, len(files))
		for _, file := range files {
			if file.Id != 0 {
				fileIDs = append(fileIDs, file.Id)
			}
		}
		if err := globalAssetService.NewRepository(tx).MarkWorkAgentThreadFilesDeletedWithDB(tx, uid, fileIDs); err != nil {
			return fmt.Errorf("failed to mark global assets deleted: %w", err)
		}
		result := tx.
			Where("uid = ? AND thread_id = ? AND exists_on_disk = ?", uid, threadID, false).
			Delete(&workagentModel.ThreadFile{})

		if result.Error != nil {
			return fmt.Errorf("failed to delete orphaned records: %w", result.Error)
		}
		ledger := assetLedgerService.New()
		for _, file := range files {
			if err := ledger.DeleteThreadByIDWithDB(tx, uid, mapThreadLedgerSource(file.FileSource), file.Id); err != nil {
				return fmt.Errorf("failed to cleanup asset ledger: %w", err)
			}
		}
		rowsAffected = result.RowsAffected
		return nil
	})
	if err != nil {
		return 0, err
	}
	return int(rowsAffected), nil
}

// DeleteFilesBySource deletes all files of a specific source type. Same
// transactional shape as DeleteOrphanedRecords above — without the
// transaction a partial failure splits the delete from the ledger
// cleanup and leaves orphaned ledger rows.
func (s *FileService) DeleteFilesBySource(uid int, threadID uint, source workagentModel.FileSource) (int, error) {
	var rowsAffected int64
	err := globals.GraDBs["system"].Transaction(func(tx *gorm.DB) error {
		var files []workagentModel.ThreadFile
		if err := tx.
			Where("uid = ? AND thread_id = ? AND file_source = ?", uid, threadID, source).
			Find(&files).Error; err != nil {
			return fmt.Errorf("failed to list files: %w", err)
		}
		if len(files) == 0 {
			return nil
		}
		fileIDs := make([]uint, 0, len(files))
		for _, file := range files {
			if file.Id != 0 {
				fileIDs = append(fileIDs, file.Id)
			}
		}
		if err := globalAssetService.NewRepository(tx).MarkWorkAgentThreadFilesDeletedWithDB(tx, uid, fileIDs); err != nil {
			return fmt.Errorf("failed to mark global assets deleted: %w", err)
		}
		result := tx.
			Where("uid = ? AND thread_id = ? AND file_source = ?", uid, threadID, source).
			Delete(&workagentModel.ThreadFile{})

		if result.Error != nil {
			return fmt.Errorf("failed to delete files: %w", result.Error)
		}
		ledger := assetLedgerService.New()
		for _, file := range files {
			if err := ledger.DeleteThreadByIDWithDB(tx, uid, mapThreadLedgerSource(file.FileSource), file.Id); err != nil {
				return fmt.Errorf("failed to cleanup asset ledger: %w", err)
			}
		}
		rowsAffected = result.RowsAffected
		return nil
	})
	if err != nil {
		return 0, err
	}
	return int(rowsAffected), nil
}

// =============================================================================
// Utility Functions
// =============================================================================

// ComputeFileHash computes MD5 hash of a file
func ComputeFileHash(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := md5.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

func getMimeTypeFromFilename(filename string) string {
	ext := filepath.Ext(filename)
	mimeType := mime.TypeByExtension(ext)
	if mimeType == "" {
		return "application/octet-stream"
	}
	return mimeType
}
