package workagent

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"server/globals"
	"server/model"
	workagentModel "server/model/workagent"
	toolsService "server/service/tools"
	workagentService "server/service/tools/workagent"
	"server/utils"
)

// resolveAgentInputFiles converts request-level FileInfo entries into
// agent workspace paths. The three-tier source-of-truth fallback is:
//
//  1. fileInfo.Path  — already a workspace-relative path supplied by
//     the frontend (the conversation's own file list).
//  2. fileInfo.Id    — DB lookup, scoped by uid so a user can't
//     reference another user's file row by guessing IDs (CWE-639).
//  3. fileInfo.FilePath — last-resort raw path. Historically this was
//     accepted unchecked, which let a crafted request point it at
//     /etc/passwd and have the agent symlink it into the workspace
//     (CWE-22). Funneled through ResolveInsideWorkspace so any escape
//     is rejected uniformly.
//
// Files that don't resolve inside threadWorkspace fail the turn with a
// user-visible diagnostic. Silent partial attachment loss is worse than a
// hard stop here: the model would answer without the user-selected context
// while the turn still consumes credits.
//
// Returns the agent-ready slice and a non-nil error when any requested file
// cannot be prepared. The caller's contract is "if err != nil, the agent will
// see no files; abort and refund". The previous behaviour of swallowing
// PrepareFilesForAgent's error or silently dropping individual files charged
// the user for a turn where the model never saw the expected attachments.
func resolveAgentInputFiles(files []FileInfo, uid uint, workspaceRoot, threadWorkspace string) ([]workagentService.AgentFileInfo, error) {
	if len(files) == 0 {
		return nil, nil
	}

	globals.Info(fmt.Sprintf("[Agent] Preparing %d files for agent workspace", len(files)))

	// Single batch lookup for every file ID in the request. The previous
	// per-file getFileByID was N+1: a 10-attachment send paid 10 DB
	// round-trips on top of the per-turn reservation transaction.
	// Build the ID slice in declaration order, look them all up at
	// once, then assemble against the resulting map.
	fileIDs := make([]string, 0, len(files))
	for _, fileInfo := range files {
		if fileInfo.Id != "" {
			fileIDs = append(fileIDs, fileInfo.Id)
		}
	}
	fileRecords := getFilesByIDs(fileIDs, uid)

	agentFiles := make([]workagentService.AgentFileInfo, 0, len(files))
	skippedFiles := make([]string, 0)
	for _, fileInfo := range files {
		filePath := ""
		fileHash := ""
		displayName := strings.TrimSpace(fileInfo.Name)
		if displayName == "" {
			displayName = fileInfo.Id
		}
		if displayName == "" {
			displayName = "unnamed file"
		}

		// Pull from the batched map. The row carries the authoritative
		// FileHash that the filesContext cache uses to detect in-place
		// re-uploads — without it, replacing a file keeps the same
		// (id, name, size) tuple and the cache silently reuses the
		// stale prompt. IDs that don't belong to uid (or don't exist)
		// are absent from the map; treat that as a hard reference
		// failure rather than falling back to caller-provided paths.
		if fileInfo.Id == "" {
			globals.Warn(fmt.Sprintf("[Agent] Skipping file %s: missing file id", displayName))
			skippedFiles = append(skippedFiles, fmt.Sprintf("%s (missing file id)", displayName))
			continue
		}
		fileRecord := fileRecords[fileInfo.Id]
		if fileRecord == nil {
			globals.Warn(fmt.Sprintf("[Agent] Skipping file %s: file id %s not found for uid=%d", displayName, fileInfo.Id, uid))
			skippedFiles = append(skippedFiles, fmt.Sprintf("%s (not found or not owned by this user)", displayName))
			continue
		}
		filePath = fileRecord.FilePath
		fileHash = fileRecord.FileHash

		resolvedPath := ""
		if filePath != "" {
			resolvedPath = workagentService.ResolveInsideWorkspace(workspaceRoot, filePath)
		}

		if resolvedPath == "" {
			globals.Warn(fmt.Sprintf("[Agent] Skipping file %s: path could not be resolved inside workspace", displayName))
			skippedFiles = append(skippedFiles, fmt.Sprintf("%s (not available in this workspace)", displayName))
			continue
		}

		// Fall back to mtime when the row has no hash — newly uploaded
		// rows get a hash, but legacy rows and outputs of in-process
		// pipelines may not. Stat is cheap and we're already touching
		// these paths in PrepareFilesForAgent right after.
		var modTime int64
		if fileHash == "" {
			if st, err := os.Stat(resolvedPath); err == nil {
				modTime = st.ModTime().UnixNano()
			}
		}

		agentFiles = append(agentFiles, workagentService.AgentFileInfo{
			ID:      fileInfo.Id,
			Name:    fileInfo.Name,
			Path:    resolvedPath,
			Size:    fileInfo.Size,
			Type:    fileInfo.Type,
			Hash:    fileHash,
			ModTime: modTime,
		})
		globals.Info(fmt.Sprintf("[Agent] Adding file to workspace: %s (path: %s)", fileInfo.Name, resolvedPath))
	}
	if len(skippedFiles) > 0 {
		return nil, fmt.Errorf("some selected files are no longer available: %s", strings.Join(skippedFiles, "; "))
	}

	preparedFiles, err := workagentService.PrepareFilesForAgent(agentFiles, threadWorkspace)
	if err != nil {
		// Workspace-level failure (e.g. uploads/ mkdir denied) — the
		// agent will see zero files. Surface so the caller can refund
		// the reservation and emit a user-visible error instead of
		// silently charging for a turn with no attachments.
		globals.Error(fmt.Sprintf("[Agent] Failed to prepare files: %v", err))
		return agentFiles, fmt.Errorf("prepare files: %w", err)
	}
	globals.Info(fmt.Sprintf("[Agent] Successfully prepared %d files: %v", len(preparedFiles), preparedFiles))

	return agentFiles, nil
}

// conversationInBandError detects in-band error results — the agent
// SDK returns success-the-protocol but tags the result with is_error
// (e.g. GLM coding plan expired, rate-limited 429). When true, the
// caller must skip persistence + usage recording so a failed turn
// doesn't charge credits or leave a STATUS_SUCCESS row.
//
// Returns the raw inner error string so the caller can route an admin
// email — the user-facing SSE was already emitted in the OnDone callback
// before this runs.
func conversationInBandError(conversation *workagentModel.AgentConversation) (string, bool) {
	if conversation == nil || len(conversation.Result) == 0 {
		return "", false
	}
	var check struct {
		IsError bool   `json:"is_error"`
		Result  string `json:"result"`
	}
	if json.Unmarshal(conversation.Result, &check) != nil || !check.IsError {
		return "", false
	}

	globals.Error("[Agent API] ❌ Agent execution returned error: is_error=true")
	globals.Error(fmt.Sprintf("[Agent API] Error content: %s", check.Result))
	globals.Warn("[Agent API] Skipping conversation save, thread update, and usage recording")
	return check.Result, true
}

// persistAgentSessionID writes a fresh agent SDK session ID back to the
// thread row when the SDK rotated it. Empty newSessionID is logged but
// never written — empty would clobber a still-valid session. A DB
// failure here is best-effort: the agent already streamed its answer,
// so we surface the warning rather than fail the request.
func persistAgentSessionID(chatThread *workagentModel.ChatThread, newSessionID string) {
	globals.Info(fmt.Sprintf("[Agent API] Session ID check - old: '%s', new: '%s'",
		chatThread.AgentSessionID, newSessionID))

	if newSessionID == "" {
		globals.Warn("[Agent API] ⚠️ newSessionID is empty, cannot update database")
		return
	}
	if newSessionID == chatThread.AgentSessionID {
		globals.Info("[Agent API] Session ID unchanged, no database update needed")
		return
	}

	now := time.Now()
	globals.Info(fmt.Sprintf("[Agent API] Updating session ID: '%s' -> '%s'",
		chatThread.AgentSessionID, newSessionID))

	// Repo write bakes in the ThreadCache invalidation so the next
	// getChatThread re-reads the fresh agent_session_id instead of
	// serving the cached pre-update value for up to 5 minutes. Without
	// this, a fast back-to-back turn would call SDK.WithResume on the
	// SDK's just-rotated old session id and either error or replay
	// stale context. The previous inline shape carried the
	// invalidation alongside the UPDATE manually, two separate
	// concerns at one call site — moving it into the repo makes it
	// impossible for a future caller to forget.
	if err := workagentService.DefaultThreadRepository().
		PersistAgentSessionID(chatThread.Id, newSessionID, now); err != nil {
		globals.Error(fmt.Sprintf("[Agent API] Failed to update session ID: %v", err))
		return
	}
	// Keep the in-process struct in sync too — agent_api.go uses
	// chatThread.AgentSessionID after this call (e.g. error logging
	// branches that include the session id), so without this update
	// they'd still report the pre-rotation value.
	chatThread.AgentSessionID = newSessionID
	chatThread.AgentSessionCreatedAt = &now
	globals.Info("[Agent API] ✅ Session ID updated successfully in database")
}

// recordAgentUsageInput collects the per-request fields the usage
// record needs without forcing the caller to thread eight positional
// args through. Renamed/added fields here propagate to the record
// without shuffling argument order at the call site.
type recordAgentUsageInput struct {
	uid                 uint
	creditCost          int
	threadID            string
	chatThreadID        uint
	messageID           uint
	agentMode           string
	modelTier           string
	duration            int
	generatedFilesCount int
}

// recordAgentUsage writes a STATUS_SUCCESS usage row for a completed
// agent turn. Failure is logged-and-swallowed: the user already saw
// their answer streamed, refunding credits for a missing usage row
// would be more confusing than the missing row itself.
//
// creditCost mirrors the amount the reservation finalizes for this
// turn so admin dashboards (which sum credits_used) match what the
// user was actually charged.
func recordAgentUsage(c *gin.Context, in recordAgentUsageInput) {
	err := toolsService.CreateUsageRecordTx(
		globals.GraDBs["system"],
		int(in.uid),
		model.TOOL_AGENT,
		in.messageID,
		in.creditCost,
		model.STATUS_SUCCESS,
		in.duration,
		&toolsService.UsageRecordMeta{
			IP:         utils.GetClientIP(c.Request),
			DeviceInfo: c.GetHeader("User-Agent"),
			ToolParams: map[string]interface{}{
				"agentMode":      in.agentMode,
				"modelTier":      in.modelTier,
				"conversationId": in.threadID,
				"threadId":       in.chatThreadID,
			},
			ResultMetadata: map[string]interface{}{
				"generatedFilesCount": in.generatedFilesCount,
			},
		},
	)
	if err != nil {
		globals.Error(fmt.Sprintf("[Agent API] Failed to create usage record: %v", err))
	}
	globals.Info(fmt.Sprintf("[Agent API] Usage: uid=%d, duration=%ds, credits=%d, messageID=%d",
		in.uid, in.duration, in.creditCost, in.messageID))
}
