package workagent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"server/globals"
	"server/model/workagent"
	assetLedgerService "server/service/assetledger"
	desktopsync "server/service/desktop/sync"
	globalAssetService "server/service/globalasset"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// confinedToWorkspaceRoot reports whether path resolves to workspaceRoot
// or one of its descendants. Used as a defence-in-depth guard before
// destructive os.RemoveAll calls — resolveThreadWorkspacePath already
// constructs paths under workspaceRoot via Glob, but a future bug
// (cache poisoning, symlink shenanigans, refactor that drops the
// pattern-prefix invariant) could return something outside, and
// RemoveAll on the wrong path is catastrophic. Cheap insurance.
//
// Both inputs are EvalSymlinks'd so:
//
//   - macOS /var → /private/var doesn't false-negative when the root
//     is configured as /var/lib/<app>/workspace and the candidate
//     resolved through the symlink to /private/var/lib/<app>/...
//   - a planted symlink AT the candidate pointing outside the root
//     resolves to its target and fails the prefix check (we WANT this
//     case to refuse, not transparently follow the symlink and
//     RemoveAll something on the other end).
//
// EvalSymlinks failure on the candidate falls back to Clean+Abs so
// "candidate doesn't exist yet" cases (rare here — callers Stat
// first) still produce a sensible answer.
func confinedToWorkspaceRoot(workspaceRoot, candidate string) bool {
	if workspaceRoot == "" || candidate == "" {
		return false
	}
	absRoot, err := filepath.Abs(filepath.Clean(workspaceRoot))
	if err != nil {
		return false
	}
	if eval, err := filepath.EvalSymlinks(absRoot); err == nil {
		absRoot = eval
	}
	absCandidate, err := filepath.Abs(filepath.Clean(candidate))
	if err != nil {
		return false
	}
	if eval, err := filepath.EvalSymlinks(absCandidate); err == nil {
		absCandidate = eval
	}
	sep := string(filepath.Separator)
	if absCandidate == absRoot {
		// Refuse to nuke the workspace root itself.
		return false
	}
	return strings.HasPrefix(absCandidate+sep, absRoot+sep)
}

// ThreadLifecycleService manages the lifecycle of chat threads (simplified - no vector/embedding)
type ThreadLifecycleService struct {
	db          *gorm.DB
	fileService *FileService
}

// ErrThreadUUIDOwnedByAnotherUser is returned when an idempotent Desktop
// create collides with a thread UUID owned by another account. Callers must
// surface this as a generic conflict and must not reveal the existing owner.
var ErrThreadUUIDOwnedByAnotherUser = errors.New("thread uuid belongs to another user")

// NewThreadLifecycleService creates a new thread lifecycle service
func NewThreadLifecycleService(db *gorm.DB) *ThreadLifecycleService {
	return &ThreadLifecycleService{
		db:          db,
		fileService: GetFileService(),
	}
}

// CreateThreadNew creates a new chat thread. projectID=0 leaves
// the thread project-unbound (legacy uid-only scope); a non-zero
// value MUST belong to the same user — the caller is expected to
// have already validated ownership via project.ExistsForOwner. We
// don't re-validate inside the service because the project repo
// import would cause a cycle (project → workagent → project).
func (tls *ThreadLifecycleService) CreateThreadNew(userID int, title string, agentMode string, initialFiles []workagent.ThreadFileRequest, projectID uint) (*workagent.ChatThread, error) {
	// Create thread in database
	thread := workagent.CreateAIChatThread(userID, title, agentMode)
	thread.ProjectID = projectID
	thread.CreatedAt = time.Now()
	thread.UpdatedAt = time.Now()
	if err := tls.db.Create(thread).Error; err != nil {
		return nil, fmt.Errorf("failed to create thread: %w", err)
	}

	// Process initial files if any
	if len(initialFiles) > 0 {
		for _, fileReq := range initialFiles {
			fileReq.UID = userID
			fileReq.ThreadID = thread.Id

			if _, err := tls.fileService.AddFileToThread(fileReq); err != nil {
				globals.Warn(fmt.Sprintf("Failed to add initial file %s: %v", fileReq.FileName, err))
			}
		}
	}

	globals.Info(fmt.Sprintf("Created thread %d for user %d", thread.Id, userID))
	return thread, nil
}

// PutThreadNew creates one thread at a caller-supplied stable UUID or returns
// the existing thread when the same owner replays the request. The UUID is the
// idempotency identity: a lost HTTP response, a safe 401 retry, or a failed
// Desktop local-cache commit can repeat the PUT without creating another
// cloud conversation.
//
// Title and agentMode apply only when the resource is first created. A replay
// returns the current stored row, including any later user edits, rather than
// treating those edits as an idempotency conflict. A UUID owned by another
// user fails closed via ErrThreadUUIDOwnedByAnotherUser.
func (tls *ThreadLifecycleService) PutThreadNew(userID int, threadUUID, title, agentMode string, projectID uint) (*workagent.ChatThread, bool, error) {
	if tls == nil || tls.db == nil {
		return nil, false, fmt.Errorf("put thread: database is not configured")
	}
	if userID <= 0 {
		return nil, false, fmt.Errorf("put thread: user id must be positive")
	}
	if threadUUID == "" {
		return nil, false, fmt.Errorf("put thread: uuid is required")
	}

	// Keep ordinary retries read-only and return the currently stored resource.
	// The unique index below remains the authority for callers that race after
	// both observe a miss.
	var stored workagent.ChatThread
	if err := tls.db.Where("uuid = ?", threadUUID).First(&stored).Error; err == nil {
		if stored.UID != userID {
			return nil, false, ErrThreadUUIDOwnedByAnotherUser
		}
		return &stored, false, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, fmt.Errorf("put thread: load existing resource: %w", err)
	}

	thread := workagent.CreateAIChatThread(userID, title, agentMode)
	thread.UUID = threadUUID
	thread.ProjectID = projectID
	now := time.Now().UTC()
	thread.CreatedAt = now
	thread.UpdatedAt = now

	// One globally-unique UUID is the create gate. On MySQL, use a plain INSERT:
	// success unambiguously means this caller created the row, while #1062 means
	// another caller won. GORM's MySQL rendering of DoNothing is a no-op UPDATE,
	// whose affected-row and last-insert-id results vary with connection options
	// and server versions, so it cannot reliably decide between HTTP 201 and 200.
	createdByInsert := false
	if tls.db.Dialector.Name() == "mysql" {
		result := tls.db.Create(thread)
		if result.Error == nil {
			createdByInsert = true
		} else if !isDuplicateKeyError(result.Error) {
			return nil, false, fmt.Errorf("put thread: create: %w", result.Error)
		}
	} else {
		// SQLite is used by the isolated tests; other GORM dialects also expose
		// native conflict-ignore semantics with a trustworthy RowsAffected value.
		result := tls.db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "uuid"}},
			DoNothing: true,
		}).Create(thread)
		if result.Error != nil {
			return nil, false, fmt.Errorf("put thread: create: %w", result.Error)
		}
		createdByInsert = result.RowsAffected == 1
	}
	insertedID := thread.Id

	if err := tls.db.Where("uuid = ?", threadUUID).First(&stored).Error; err != nil {
		return nil, false, fmt.Errorf("put thread: load resource: %w", err)
	}
	if stored.UID != userID {
		return nil, false, ErrThreadUUIDOwnedByAnotherUser
	}

	// Match the generated ID to the loaded winner as a final integrity check.
	// This keeps HTTP 201 exclusive to the caller that inserted this exact row.
	created := createdByInsert && insertedID != 0 && stored.Id == insertedID
	return &stored, created, nil
}

// AddFileToThreadNew adds a file to thread
func (tls *ThreadLifecycleService) AddFileToThreadNew(threadID uint, userID int, fileReq workagent.ThreadFileRequest) error {
	var thread workagent.ChatThread
	if err := tls.db.First(&thread, threadID).Error; err != nil {
		return fmt.Errorf("thread not found: %w", err)
	}

	if thread.UID != userID {
		return fmt.Errorf("user %d does not have access to thread %d", userID, threadID)
	}

	fileReq.UID = userID
	fileReq.ThreadID = threadID

	if _, err := tls.fileService.AddFileToThread(fileReq); err != nil {
		return fmt.Errorf("failed to add file: %w", err)
	}

	return nil
}

// DeleteThread removes a thread and cleans up associated data.
//
// Ordering matters for crash/partial-failure safety:
//  1. Gather file paths while the rows still exist.
//  2. Commit the DB transaction (thread + messages + files + asset ledger).
//  3. Only after commit: remove files from disk, delete the workspace
//     directory, and invalidate the in-memory workspace cache.
//
// If the DB commit fails, no filesystem changes have happened and the
// thread remains consistent. If the filesystem cleanup partially fails,
// the result is an orphan file (a janitor can reclaim) rather than a
// dangling DB row pointing at a deleted artifact.
//
// The userID parameter is the caller's user id; we refuse to delete
// when it doesn't match the thread's owner. The single API caller
// (conversation_api.DeleteConversation) already checks ownership, but
// keeping the guard inside the service is defence in depth — same
// CWE-639 hardening pattern as commit c16714ae and the matching
// owner check in ClearMessages / ClearThread / AddFileToThreadNew.
func (tls *ThreadLifecycleService) DeleteThread(threadID uint, userID int) error {
	var thread workagent.ChatThread
	if err := tls.db.First(&thread, threadID).Error; err != nil {
		return fmt.Errorf("thread not found: %w", err)
	}

	if thread.UID != userID {
		// Mirror the ClearMessages / ClearThread error string so a future
		// audit greps the same shape across the service. Do not reveal
		// the owning UID in the message.
		return fmt.Errorf("user %d does not have access to thread %d", userID, threadID)
	}

	// Gather file paths up front; we'll delete them only after the DB commit.
	threadFiles, err := tls.fileService.GetThreadFiles(thread.UID, threadID)
	if err != nil {
		globals.Warn(fmt.Sprintf("Failed to get thread files for thread %d: %v", threadID, err))
	} else {
		globals.Info(fmt.Sprintf("Found %d files to delete for thread %d", len(threadFiles), threadID))
	}

	tx := tls.db.Begin()
	if tx.Error != nil {
		return fmt.Errorf("failed to begin transaction: %w", tx.Error)
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	// userID is the parameter, already verified to equal thread.UID above.
	if err := tx.Where("thread_id = ? AND uid = ?", threadID, userID).Delete(&workagent.ChatMessage{}).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete chat messages: %w", err)
	}

	threadFileIDs := make([]uint, 0, len(threadFiles))
	for _, file := range threadFiles {
		if file.Id != 0 {
			threadFileIDs = append(threadFileIDs, file.Id)
		}
	}
	if err := globalAssetService.NewRepository(tx).MarkWorkAgentThreadFilesDeletedWithDB(tx, userID, threadFileIDs); err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to mark thread global assets deleted: %w", err)
	}

	if err := tx.Where("thread_id = ? AND uid = ?", threadID, userID).Delete(&workagent.ThreadFile{}).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete thread files: %w", err)
	}
	if err := assetLedgerService.New().DeleteThreadByThreadIDWithDB(tx, userID, threadID); err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete thread asset ledger: %w", err)
	}

	if err := tx.Delete(&thread).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete thread: %w", err)
	}

	// P1.A.5b: insert tombstone INSIDE the same transaction so the
	// delete + tombstone land atomically. Desktop sync (cloud-sync.md
	// §5) reads this row + emits action="delete" so the sidecar's
	// UpsertThreads (P1.A.5a) can remove its local row + cascade
	// messages.
	if err := desktopsync.InsertTombstone(tx, userID,
		workagent.TombstoneEntityTypeThread, thread.Id, thread.UUID); err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to insert thread tombstone: %w", err)
	}

	if err := tx.Commit().Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	// Invalidate the ThreadCache after the row is gone. Without this,
	// a getChatThread call within the 5-minute TTL would still serve
	// the deleted-but-cached row as if the thread existed — and any
	// authorization check based on cachedThread.UID would pass for
	// a thread that no longer has a real row backing it. Same posture
	// as ce4796ee / b37e13a8 / eb4f682a (task #90 follow-ups).
	GetThreadCache().Invalidate(fmt.Sprint(threadID))

	// DB is clean. Now remove filesystem artifacts — failures here become
	// warnings, not user-visible errors, because the DB is the source of truth.
	deletedCount := 0
	for _, file := range threadFiles {
		fullPath := tls.resolveStoredThreadFilePath(file.FilePath)
		// resolveStoredThreadFilePath returns "" when the stored path
		// would escape workspaceRoot (absolute path, ".." segments,
		// other traversal). Without this skip, os.Stat("")/os.Remove("")
		// each fail with confusing messages and the warn log fills up
		// for every escaping row. ClearThread already does this skip
		// — keep the two loops symmetric.
		if fullPath == "" {
			continue
		}
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			continue
		}
		if err := os.Remove(fullPath); err != nil {
			globals.Warn(fmt.Sprintf("Post-commit: failed to delete file %s: %v", fullPath, err))
		} else {
			deletedCount++
		}
	}
	globals.Info(fmt.Sprintf("Post-commit: removed %d/%d physical files for thread %d", deletedCount, len(threadFiles), threadID))

	if thread.UUID != "" {
		if err := tls.deleteThreadWorkspace(thread.UID, thread.UUID); err != nil {
			globals.Warn(fmt.Sprintf("Failed to delete workspace for thread %d: %v", threadID, err))
		}
		// Always invalidate the cached workspace path so a later lookup does
		// not return a stale pointer to a removed directory.
		if manager := GetAgentClientManager(); manager != nil {
			manager.InvalidateWorkspace(thread.UID, thread.UUID)
		}
	}

	globals.Info(fmt.Sprintf("Deleted thread %d and all associated records", threadID))
	return nil
}

// ClearMessages clears only messages from a thread but keeps files and suggestions
func (tls *ThreadLifecycleService) ClearMessages(threadID uint, userID int) error {
	// First verify thread exists and user has access
	var thread workagent.ChatThread
	if err := tls.db.First(&thread, threadID).Error; err != nil {
		return fmt.Errorf("thread not found: %w", err)
	}

	if thread.UID != userID {
		return fmt.Errorf("user %d does not have access to thread %d", userID, threadID)
	}

	// Begin transaction for data consistency
	tx := tls.db.Begin()
	if tx.Error != nil {
		return fmt.Errorf("failed to begin transaction: %w", tx.Error)
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	// Delete only chat messages for this thread
	if err := tx.Where("thread_id = ? AND uid = ?", threadID, userID).Delete(&workagent.ChatMessage{}).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete chat messages: %w", err)
	}

	// Reset the SDK session along with the message rows. Without this
	// the next turn calls WithResume(agent_session_id) and the SDK
	// replays every prior turn from its own state — UI looks empty,
	// model answers as if the cleared messages were still in context.
	// (DeleteThread doesn't need this; the row is gone.)
	if err := tx.Model(&workagent.ChatThread{}).
		Where("id = ?", threadID).
		Updates(map[string]interface{}{
			"agent_session_id":         "",
			"agent_session_created_at": nil,
		}).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to reset agent session: %w", err)
	}

	// Commit transaction
	if err := tx.Commit().Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	// Invalidate the ThreadCache so the next chat turn re-reads the
	// just-cleared agent_session_id. Without this, the next request
	// inside the 5-minute TTL still sees the OLD agent_session_id and
	// SDK.WithResume replays the cleared turns from the SDK's own
	// session state — UI looks empty, model answers as if nothing was
	// cleared. Same posture as ce4796ee / b37e13a8 / eb4f682a; the
	// conversation_api caller's updateThreadStatistics also flushes
	// the cache as a side effect, but doing it at the service layer
	// means any future caller (admin tools, scripts, tests) inherits
	// the right behaviour.
	GetThreadCache().Invalidate(fmt.Sprint(threadID))

	globals.Info(fmt.Sprintf("Cleared messages for thread %d (user %d)", threadID, userID))
	return nil
}

// ClearThread clears all content from a thread but keeps the thread itself.
//
// Ordering mirrors DeleteThread: gather file paths, commit the DB transaction,
// then remove filesystem artifacts. A pre-commit filesystem wipe could leave
// orphan DB rows pointing at vanished files if the transaction then fails.
func (tls *ThreadLifecycleService) ClearThread(threadID uint, userID int) error {
	var thread workagent.ChatThread
	if err := tls.db.First(&thread, threadID).Error; err != nil {
		return fmt.Errorf("thread not found: %w", err)
	}

	if thread.UID != userID {
		return fmt.Errorf("user %d does not have access to thread %d", userID, threadID)
	}

	// Gather file paths up front while the rows still exist; we delete them
	// only after the DB commit succeeds.
	threadFiles, err := tls.fileService.GetThreadFiles(userID, threadID)
	if err != nil {
		globals.Warn(fmt.Sprintf("Failed to get thread files for thread %d: %v", threadID, err))
	} else {
		globals.Info(fmt.Sprintf("Found %d files to delete for thread %d", len(threadFiles), threadID))
	}

	tx := tls.db.Begin()
	if tx.Error != nil {
		return fmt.Errorf("failed to begin transaction: %w", tx.Error)
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	if err := tx.Where("thread_id = ? AND uid = ?", threadID, userID).Delete(&workagent.ChatMessage{}).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete chat messages: %w", err)
	}

	threadFileIDs := make([]uint, 0, len(threadFiles))
	for _, file := range threadFiles {
		if file.Id != 0 {
			threadFileIDs = append(threadFileIDs, file.Id)
		}
	}
	if err := globalAssetService.NewRepository(tx).MarkWorkAgentThreadFilesDeletedWithDB(tx, userID, threadFileIDs); err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to mark thread global assets deleted: %w", err)
	}

	if err := tx.Where("thread_id = ? AND uid = ?", threadID, userID).Delete(&workagent.ThreadFile{}).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete thread files: %w", err)
	}
	if err := assetLedgerService.New().DeleteThreadByThreadIDWithDB(tx, userID, threadID); err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to delete thread asset ledger: %w", err)
	}

	if thread.Name != "Untitled" {
		if err := tx.Model(&thread).Update("name", "Untitled").Error; err != nil {
			globals.Warn(fmt.Sprintf("Failed to reset thread name: %v", err))
		}
	}

	// Reset the SDK session — see matching note in ClearMessages. The
	// SDK keeps its own copy of conversation history keyed by
	// session_id, so wiping rows without resetting the session leaves
	// the model answering from invisible context after a Clear.
	if err := tx.Model(&thread).Updates(map[string]interface{}{
		"agent_session_id":         "",
		"agent_session_created_at": nil,
	}).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to reset agent session: %w", err)
	}

	if err := tx.Commit().Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	// Invalidate the ThreadCache after the clear commits. Same
	// rationale as the matching ClearMessages call above: the row's
	// name was reset to "Untitled" and agent_session_id was wiped;
	// without this, the cached row keeps the pre-clear name + session
	// id for up to 5 minutes.
	GetThreadCache().Invalidate(fmt.Sprint(threadID))

	// DB is clean. Now remove filesystem artifacts — failures become warnings.
	deletedCount := 0
	for _, file := range threadFiles {
		fullPath := tls.resolveStoredThreadFilePath(file.FilePath)
		if fullPath == "" {
			continue
		}
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			continue
		}
		if err := os.Remove(fullPath); err != nil {
			globals.Warn(fmt.Sprintf("Post-commit: failed to delete file %s: %v", fullPath, err))
		} else {
			deletedCount++
		}
	}
	globals.Info(fmt.Sprintf("Post-commit: removed %d/%d physical files for thread %d", deletedCount, len(threadFiles), threadID))

	if thread.UUID != "" {
		if err := tls.clearThreadWorkspaceContent(thread.UID, thread.UUID); err != nil {
			globals.Warn(fmt.Sprintf("Failed to clear workspace content for thread %d: %v", threadID, err))
		} else {
			globals.Info(fmt.Sprintf("Cleared workspace content for thread %d (UUID: %s)", threadID, thread.UUID))
		}
	}

	globals.Info(fmt.Sprintf("Cleared thread %d for user %d", threadID, userID))
	return nil
}

// RemoveFileFromThreadNew removes a file from thread
func (tls *ThreadLifecycleService) RemoveFileFromThreadNew(threadID uint, userID int, fileID uint) error {
	var thread workagent.ChatThread
	if err := tls.db.First(&thread, threadID).Error; err != nil {
		return fmt.Errorf("thread not found: %w", err)
	}

	if thread.UID != userID {
		return fmt.Errorf("user does not have access to thread")
	}

	if err := tls.fileService.RemoveFileFromThread(userID, threadID, fileID); err != nil {
		return fmt.Errorf("failed to remove file: %w", err)
	}

	return nil
}

// resolveStoredThreadFilePath returns the absolute on-disk path for a stored
// workspace-relative file, or "" when the input would escape workspaceRoot
// (absolute path, ".." segments, or other traversal). Callers must treat ""
// as "file not found" — the DB row exists but the artifact is unreachable.
func (tls *ThreadLifecycleService) resolveStoredThreadFilePath(path string) string {
	manager := GetAgentClientManager()
	root := ""
	if manager != nil {
		root = manager.WorkspaceRoot()
	}
	return ResolveInsideWorkspace(root, path)
}

func (tls *ThreadLifecycleService) resolveThreadWorkspacePath(uid int, threadUUID string) (string, error) {
	manager := GetAgentClientManager()
	if manager == nil {
		return "", fmt.Errorf("agent client manager not initialized")
	}
	normalizedThreadUUID, err := normalizeThreadWorkspaceUUID(threadUUID)
	if err != nil {
		return "", err
	}

	workspaceRoot := manager.WorkspaceRoot()
	if workspaceRoot == "" {
		return "", fmt.Errorf("workspace root not configured")
	}

	if cachedPath, ok := manager.workspaceCache.Load(userScopedThreadWorkspaceCacheKey(uid, normalizedThreadUUID)); ok {
		if path, _ := cachedPath.(string); path != "" {
			return path, nil
		}
	}

	pattern := filepath.Join(workspaceRoot, "uid", strconv.Itoa(uid), "*", fmt.Sprintf("thread_%s", normalizedThreadUUID))
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return "", fmt.Errorf("failed to search thread workspace: %w", err)
	}
	if len(matches) > 0 {
		return filepath.Clean(matches[0]), nil
	}

	return "", os.ErrNotExist
}

// deleteThreadWorkspace removes the agent workspace directory for a thread
func (tls *ThreadLifecycleService) deleteThreadWorkspace(uid int, threadUUID string) error {
	threadWorkspacePath, err := tls.resolveThreadWorkspacePath(uid, threadUUID)
	if err != nil {
		if os.IsNotExist(err) {
			globals.Info(fmt.Sprintf("Thread workspace does not exist (not created yet): uid=%d, uuid=%s", uid, threadUUID))
			return nil
		}
		return err
	}

	// Check if directory exists
	if _, err := os.Stat(threadWorkspacePath); os.IsNotExist(err) {
		globals.Info(fmt.Sprintf("Thread workspace does not exist (already deleted?): %s", threadWorkspacePath))
		return nil
	}

	// Defence-in-depth: never RemoveAll a path that isn't provably
	// inside the workspace root.
	manager := GetAgentClientManager()
	root := ""
	if manager != nil {
		root = manager.WorkspaceRoot()
	}
	if !confinedToWorkspaceRoot(root, threadWorkspacePath) {
		return fmt.Errorf("refusing to remove workspace path outside root: path=%s root=%s", threadWorkspacePath, root)
	}

	// Remove the entire workspace directory
	if err := os.RemoveAll(threadWorkspacePath); err != nil {
		return fmt.Errorf("failed to remove workspace directory %s: %w", threadWorkspacePath, err)
	}

	globals.Info(fmt.Sprintf("Successfully deleted workspace directory: %s", threadWorkspacePath))
	return nil
}

// clearThreadWorkspaceContent removes uploads and outputs subdirectories but keeps the workspace
func (tls *ThreadLifecycleService) clearThreadWorkspaceContent(uid int, threadUUID string) error {
	threadWorkspacePath, err := tls.resolveThreadWorkspacePath(uid, threadUUID)
	if err != nil {
		if os.IsNotExist(err) {
			globals.Info(fmt.Sprintf("Thread workspace does not exist (not created yet): uid=%d, uuid=%s", uid, threadUUID))
			return nil
		}
		return err
	}

	// Check if directory exists
	if _, err := os.Stat(threadWorkspacePath); os.IsNotExist(err) {
		globals.Info(fmt.Sprintf("Thread workspace does not exist: %s", threadWorkspacePath))
		return nil
	}

	// Defence-in-depth: confirm the resolved thread path is inside
	// workspaceRoot before any RemoveAll. The uploads/ and outputs/
	// targets below are derived from this path; if the parent
	// escaped, both children would too.
	manager := GetAgentClientManager()
	root := ""
	if manager != nil {
		root = manager.WorkspaceRoot()
	}
	if !confinedToWorkspaceRoot(root, threadWorkspacePath) {
		return fmt.Errorf("refusing to clear workspace path outside root: path=%s root=%s", threadWorkspacePath, root)
	}

	// Clear uploads directory
	uploadsPath := filepath.Join(threadWorkspacePath, "uploads")
	if _, err := os.Stat(uploadsPath); err == nil {
		if err := os.RemoveAll(uploadsPath); err != nil {
			globals.Warn(fmt.Sprintf("Failed to remove uploads directory: %v", err))
		} else {
			globals.Info(fmt.Sprintf("Cleared uploads directory: %s", uploadsPath))
		}
	}

	// Clear outputs directory
	outputsPath := filepath.Join(threadWorkspacePath, "outputs")
	if _, err := os.Stat(outputsPath); err == nil {
		if err := os.RemoveAll(outputsPath); err != nil {
			globals.Warn(fmt.Sprintf("Failed to remove outputs directory: %v", err))
		} else {
			globals.Info(fmt.Sprintf("Cleared outputs directory: %s", outputsPath))
		}
	}

	globals.Info(fmt.Sprintf("Successfully cleared workspace content for thread_%s", threadUUID))
	return nil
}
