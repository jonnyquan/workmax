package workagent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"server/globals"
	workagentModel "server/model/workagent"
	assetLedgerService "server/service/assetledger"
	globalAssetService "server/service/globalasset"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// mysqlDuplicateKey is MySQL's "Duplicate entry" error number; raised
// when an INSERT collides with a UNIQUE constraint. Used by AddFile to
// recover when two concurrent inserts race past the existence check
// and one of them loses the unique-index race against
// uk_uid_thread_dedup. Lifted to a const so the magic number doesn't
// drift across files.
const mysqlDuplicateKey = 1062

// isDuplicateKeyError unwraps GORM's wrapping of the driver error and
// reports whether it's a UNIQUE-violation. Returns false for any other
// MySQL error or non-MySQL error.
func isDuplicateKeyError(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == mysqlDuplicateKey
}

// FileService provides unified file management for threads
// Combines database operations with filesystem synchronization.
//
// threadMutexes is a per-threadID lock keyspace: SyncOutputFiles needs
// to serialize concurrent invocations against the same thread (so two
// scans don't race on the existing-files map and double-insert), but
// users syncing different threads have no shared state to protect.
// The previous global s.mu serialized everyone behind whoever was
// running at the time — a 200-file PPT export blocked every other
// user's sync until it finished. Per-thread locking keeps the
// safety guarantee while removing the cross-user shoulder bump.
type FileService struct {
	threadMutexes sync.Map // map[uint]*sync.Mutex
}

// lockForThread returns an unlock fn that the caller must invoke
// (typically `defer unlock()`). LoadOrStore handles the race where
// two callers see no entry — only one's mutex makes it into the map,
// both end up using the same mutex.
func (s *FileService) lockForThread(threadID uint) func() {
	actual, _ := s.threadMutexes.LoadOrStore(threadID, &sync.Mutex{})
	mu := actual.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

var fileServiceInstance *FileService
var fileServiceOnce sync.Once

// NewFileService creates a new FileService instance (for dependency injection)
func NewFileService() *FileService {
	return GetFileService()
}

// GetFileService returns the singleton instance
func GetFileService() *FileService {
	fileServiceOnce.Do(func() {
		fileServiceInstance = &FileService{}
	})
	return fileServiceInstance
}

// SyncResult represents the result of a sync operation
type SyncResult struct {
	Added    int      `json:"added"`
	Updated  int      `json:"updated"`
	Removed  int      `json:"removed"`
	Errors   []string `json:"errors,omitempty"`
	Duration string   `json:"duration"`
}

// =============================================================================
// File CRUD Operations
// =============================================================================

// AddFile adds a file record to the database.
//
// Wraps the full check-then-create plus asset-ledger upsert in a single
// transaction so a partial failure (row created, ledger failed) can't
// leave an orphan ThreadFile behind. Concurrent inserts that race past
// the existence check are now safe at the DB level — migration
// 20260567 added a unique constraint on (uid, thread_id, dedup_key)
// where dedup_key is a SHA2-256 of (file_name + '|' + file_path). The
// loser of the race lands here as a MySQL #1062 duplicate-key error,
// which we map to a fresh fetch + existing-record return so the caller
// gets the same response shape as the slow-path winner.
func (s *FileService) AddFile(req workagentModel.ThreadFileRequest) (*workagentModel.ThreadFileResponse, error) {
	db := globals.GraDBs["system"]

	var response workagentModel.ThreadFileResponse
	txErr := db.Transaction(func(tx *gorm.DB) error {
		var thread workagentModel.ChatThread
		if err := tx.First(&thread, req.ThreadID).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return fmt.Errorf("thread not found: %d", req.ThreadID)
			}
			return fmt.Errorf("failed to check thread: %w", err)
		}
		if thread.UID != req.UID {
			return fmt.Errorf("thread not found: %d", req.ThreadID)
		}

		// Fast path: a row at the same (uid, thread_id, file_name, file_path)
		// tuple already exists. Two sub-cases:
		//
		//   (a) Same row, same bytes — caller is just re-registering the
		//       same upload. Return the existing response.
		//   (b) Same row, NEW bytes — user re-uploaded the file in place.
		//       The dedup_key collides because (file_name, file_path) is
		//       unchanged, but the contents shifted. Refresh size/hash on
		//       the existing row so downstream cache fingerprints (which
		//       key off FileHash) bust correctly. Without this branch the
		//       row carries stale metadata forever and the agent keeps
		//       seeing the prior version.
		var existingFile workagentModel.ThreadFile
		lookupErr := tx.Where("uid = ? AND thread_id = ? AND file_name = ? AND file_path = ?",
			req.UID, req.ThreadID, req.FileName, req.FilePath).First(&existingFile).Error
		if lookupErr == nil {
			replaced := false
			updates := map[string]interface{}{}
			if req.FileSize != 0 && req.FileSize != existingFile.FileSize {
				updates["file_size"] = req.FileSize
				replaced = true
			}
			if req.FileHash != "" && req.FileHash != existingFile.FileHash {
				updates["file_hash"] = req.FileHash
				replaced = true
			}
			if replaced {
				if err := tx.Model(&existingFile).Updates(updates).Error; err != nil {
					return fmt.Errorf("failed to refresh replaced file row: %w", err)
				}
				// Refetch so ToResponse reflects the just-written values
				// (Updates doesn't refresh the struct fields).
				if err := tx.First(&existingFile, existingFile.Id).Error; err != nil {
					return fmt.Errorf("failed to reload refreshed file row: %w", err)
				}
				globals.Info(fmt.Sprintf("[FileService] Refreshed replaced file row id=%d (%s)", existingFile.Id, existingFile.FileName))
			}
			if _, err := NewArtifactRegistryRepository(tx).UpsertFromThreadFile(&existingFile); err != nil {
				return fmt.Errorf("failed to sync artifact registry: %w", err)
			}
			response = existingFile.ToResponse()
			return nil
		}
		if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
			return fmt.Errorf("failed to check existing file: %w", lookupErr)
		}

		fileSource := req.FileSource
		if fileSource == "" {
			fileSource = workagentModel.FileSourceUpload
		}

		threadFile := workagentModel.ThreadFile{
			UID:          req.UID,
			ThreadID:     req.ThreadID,
			MessageID:    req.MessageID,
			FileName:     req.FileName,
			DisplayName:  req.DisplayName,
			FileSize:     req.FileSize,
			FileType:     req.FileType,
			MimeType:     req.MimeType,
			FilePath:     req.FilePath,
			FileHash:     req.FileHash,
			FileSource:   fileSource,
			Description:  req.Description,
			ExistsOnDisk: true,
		}

		if createErr := tx.Create(&threadFile).Error; createErr != nil {
			// Concurrent insert won the unique-index race. Re-fetch the
			// winning row and return its response — same outcome as the
			// existence-check fast path above. Any other create error is
			// fatal.
			if isDuplicateKeyError(createErr) {
				if fetchErr := tx.Where("uid = ? AND thread_id = ? AND file_name = ? AND file_path = ?",
					req.UID, req.ThreadID, req.FileName, req.FilePath).First(&existingFile).Error; fetchErr != nil {
					return fmt.Errorf("duplicate-key insert but failed to refetch winner: %w", fetchErr)
				}
				if _, err := NewArtifactRegistryRepository(tx).UpsertFromThreadFile(&existingFile); err != nil {
					return fmt.Errorf("failed to sync artifact registry: %w", err)
				}
				response = existingFile.ToResponse()
				return nil
			}
			return fmt.Errorf("failed to create file record: %w", createErr)
		}
		if err := assetLedgerService.New().UpsertThreadFileWithDB(tx, &threadFile, &thread); err != nil {
			return fmt.Errorf("failed to sync asset ledger: %w", err)
		}
		if _, err := NewArtifactRegistryRepository(tx).UpsertFromThreadFile(&threadFile); err != nil {
			return fmt.Errorf("failed to sync artifact registry: %w", err)
		}

		globals.Info(fmt.Sprintf("[FileService] Added file to thread %d: %s (source: %s)",
			req.ThreadID, req.FileName, fileSource))

		response = threadFile.ToResponse()
		return nil
	})
	if txErr != nil {
		return nil, txErr
	}
	if response.Id != 0 {
		if file, err := DefaultFileRepository().LoadByIDForOwner(response.Id, uint(req.UID)); err == nil {
			if err := syncThreadFileGlobalAssetAndLedger(globals.GraDBs["system"], file, nil); err != nil {
				globals.Warn(fmt.Sprintf("[FileService] global asset/ledger bridge sync failed for thread_file=%d: %v", response.Id, err))
			}
		}
	}
	return &response, nil
}

func syncThreadFileGlobalAssetAndLedger(db *gorm.DB, file *workagentModel.ThreadFile, thread *workagentModel.ChatThread) error {
	if db == nil || file == nil || file.Id == 0 || file.UID == 0 {
		return nil
	}
	if thread == nil {
		var loaded workagentModel.ChatThread
		if err := db.Where("id = ? AND uid = ?", file.ThreadID, file.UID).First(&loaded).Error; err == nil {
			thread = &loaded
		}
	}
	global, err := globalAssetService.NewRepository(db).CreateFromThreadFileForThread(file, thread)
	if err != nil {
		return fmt.Errorf("global asset dual-write failed: %w", err)
	}
	if global != nil {
		file.GlobalAssetID = global.Id
	}
	if err := assetLedgerService.New().UpsertThreadFileWithDB(db, file, thread); err != nil {
		return fmt.Errorf("asset ledger sync failed: %w", err)
	}
	return nil
}

// RemoveFile removes a file record from the database
func (s *FileService) RemoveFile(uid int, threadID uint, fileID uint) error {
	tx := globals.GraDBs["system"].Begin()
	if tx.Error != nil {
		return fmt.Errorf("failed to start transaction: %w", tx.Error)
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	// Check if file exists
	var threadFile workagentModel.ThreadFile
	if err := tx.Where("uid = ? AND thread_id = ? AND id = ?", uid, threadID, fileID).First(&threadFile).Error; err != nil {
		tx.Rollback()
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("file not found: uid=%d, threadID=%d, fileID=%d", uid, threadID, fileID)
		}
		return fmt.Errorf("failed to check file: %w", err)
	}

	// Capture the thread UUID inside the transaction so we can find
	// the workspace symlink after commit. ChatThread row is read-only
	// here, no concurrency hazard.
	var thread workagentModel.ChatThread
	threadFetchErr := tx.Select("id, uuid").First(&thread, threadID).Error

	// Delete record
	if err := tx.Delete(&threadFile).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to remove file: %w", err)
	}
	if err := globalAssetService.NewRepository(tx).MarkWorkAgentThreadFilesDeletedWithDB(tx, uid, []uint{threadFile.Id}); err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to mark global asset deleted: %w", err)
	}
	if err := assetLedgerService.New().DeleteThreadByIDWithDB(tx, uid, mapThreadLedgerSource(threadFile.FileSource), threadFile.Id); err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to remove asset ledger row: %w", err)
	}

	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	// Best-effort: clean the workspace symlink that PrepareFilesForAgent
	// installed at <thread_workspace>/uploads/<file_name>. Without
	// this, the next agent turn either still sees a "ghost" file the
	// user thought they removed (symlink resolves to a stale source)
	// or PrepareFilesForAgent's EvalSymlinks fails and the file is
	// silently dropped. Both paths are surprising; cleanup keeps the
	// workspace honest. Failures here are logged-warn, not fatal —
	// the DB is the source of truth and is already correct.
	if threadFetchErr == nil && thread.UUID != "" {
		if manager := GetAgentClientManager(); manager != nil {
			if threadWorkspace, wsErr := manager.EnsureThreadWorkspace(uid, thread.UUID); wsErr == nil {
				symlinkName := filepath.Base(filepath.Clean(threadFile.FileName))
				if symlinkName != "" && symlinkName != "." && symlinkName != ".." {
					symlinkPath := filepath.Join(threadWorkspace, "uploads", symlinkName)
					if rmErr := os.Remove(symlinkPath); rmErr != nil && !os.IsNotExist(rmErr) {
						globals.Warn(fmt.Sprintf("[FileService] Failed to remove workspace symlink %s: %v", symlinkPath, rmErr))
					}
				}
			}
		}
	}

	globals.Info(fmt.Sprintf("[FileService] Removed file %d from thread %d", fileID, threadID))
	return nil
}

// GetFiles returns files for a thread, optionally filtered by source
// threadFileListLimit caps how many ThreadFile rows GetFiles will
// return in a single call. A long ppt-mode thread can accumulate
// hundreds of generated `output` rows over time; loading all of them
// inflates the dock-panel payload, slows the JSON marshal, and ends
// up flooding the React tree with row components most users will
// never scroll to. 500 is generous for the dock view (recent +
// frequently-touched) and bounds the unbounded growth case.
//
// If a higher cap or paged listing is ever needed, switch this to a
// cursor-based query keyed on (created_at DESC, id DESC); the
// (thread_id, file_source, created_at) composite index is sized for
// it.
const threadFileListLimit = 500

// GetFiles delegates to FileRepository.ListByThread. Kept as a
// FileService method (rather than removed in favour of the repo) so
// existing callers don't need to switch — B2 partial introduces the
// repo without breaking the service contract.
func (s *FileService) GetFiles(uid int, threadID uint, source *workagentModel.FileSource) ([]workagentModel.ThreadFileResponse, error) {
	return DefaultFileRepository().ListByThread(uid, threadID, source)
}

// GetUploadFiles returns uploaded files for a thread
func (s *FileService) GetUploadFiles(uid int, threadID uint) ([]workagentModel.ThreadFileResponse, error) {
	source := workagentModel.FileSourceUpload
	return s.GetFiles(uid, threadID, &source)
}

// GetOutputFiles returns output files for a thread
func (s *FileService) GetOutputFiles(uid int, threadID uint) ([]workagentModel.ThreadFileResponse, error) {
	source := workagentModel.FileSourceOutput
	return s.GetFiles(uid, threadID, &source)
}

// GetAllFiles returns all files for a thread
func (s *FileService) GetAllFiles(uid int, threadID uint) ([]workagentModel.ThreadFileResponse, error) {
	return s.GetFiles(uid, threadID, nil)
}

// The five read-side methods below now delegate to FileRepository
// (B2 partial) so the SELECT/COUNT shapes live in one place. Kept as
// FileService methods rather than deleted because existing call
// sites use them; future migrations can switch to the repo directly.

// GetFileCount returns the number of files for a thread, uid-scoped.
func (s *FileService) GetFileCount(uid int, threadID uint) (int, error) {
	return DefaultFileRepository().CountForOwner(uid, threadID)
}

// CountFilesByThread returns the file count without uid scoping (per-
// turn statistics path).
func (s *FileService) CountFilesByThread(threadID uint) (int64, error) {
	return DefaultFileRepository().CountByThread(threadID)
}

// LoadFileForOwner reads one ThreadFile by id, scoped to uid.
// Returns gorm.ErrRecordNotFound for both missing and cross-tenant
// cases (CWE-639 defence baked into the repo).
func (s *FileService) LoadFileForOwner(fileID uint, uid uint) (*workagentModel.ThreadFile, error) {
	return DefaultFileRepository().LoadByIDForOwner(fileID, uid)
}

// LoadFileForOwnerByStringID is the string-id variant.
func (s *FileService) LoadFileForOwnerByStringID(fileID string, uid uint) (*workagentModel.ThreadFile, error) {
	return DefaultFileRepository().LoadByStringIDForOwner(fileID, uid)
}

// LoadFilesByIDsForOwner is the bulk variant — N+1 prefetch helper.
func (s *FileService) LoadFilesByIDsForOwner(fileIDs []string, uid uint) map[string]*workagentModel.ThreadFile {
	return DefaultFileRepository().LoadByIDsForOwner(fileIDs, uid)
}

// =============================================================================
// Filesystem Synchronization
// =============================================================================

// SyncOutputFiles scans the outputs directory and syncs with database.
// Per-thread locked: concurrent calls for the SAME thread serialize so
// the existing-files map can't be raced into double-inserts; concurrent
// calls for DIFFERENT threads run in parallel, since they touch
// disjoint thread_id row sets.
//
// New rows go through CreateInBatches instead of N×Create — a 200-file
// PPT export drops from ~200 INSERT round-trips to ~4. Asset-ledger
// upserts still run per-file (UpsertThreadFileWithDB needs the
// committed ID), but they're at least no longer fighting the global
// mutex.
func mapThreadLedgerSource(source workagentModel.FileSource) string {
	if source == workagentModel.FileSourceOutput {
		return "thread_output"
	}
	return "thread_upload"
}

// =============================================================================
// Legacy Compatibility (for ThreadFileService migration)
// =============================================================================

// AddFileToThread is a compatibility method for ThreadFileService
func (s *FileService) AddFileToThread(req workagentModel.ThreadFileRequest) (*workagentModel.ThreadFileResponse, error) {
	return s.AddFile(req)
}

// RemoveFileFromThread is a compatibility method for ThreadFileService
func (s *FileService) RemoveFileFromThread(uid int, threadID uint, fileID uint) error {
	return s.RemoveFile(uid, threadID, fileID)
}

// GetThreadFiles is a compatibility method for ThreadFileService
func (s *FileService) GetThreadFiles(uid int, threadID uint) ([]workagentModel.ThreadFileResponse, error) {
	return s.GetAllFiles(uid, threadID)
}

// GetThreadFileCount is a compatibility method for ThreadFileService
func (s *FileService) GetThreadFileCount(uid int, threadID uint) (int, error) {
	return s.GetFileCount(uid, threadID)
}

// =============================================================================
// File Upload Processing
// =============================================================================
