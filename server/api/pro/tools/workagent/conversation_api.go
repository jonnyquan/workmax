package workagent

import (
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	agentv1 "server/contracts/agent/v1"
	"server/globals"
	"server/model/common/response"
	"server/service/project"
	workagentService "server/service/tools/workagent"
	"server/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// maxSystemPromptBytes caps the user-supplied system prompt accepted
// by UpdateConversationSettings. Without it, a 100 MB POST would land
// in the DB and forward to the SDK every turn, ballooning context
// usage and per-turn cost. 32 KiB is generous (the official skill
// system prompts run 5–10 KB) but bounded enough that an upload
// abuse can't drive cost or latency open-ended.
const maxSystemPromptBytes = 32 * 1024

// maxPaginationOffset caps `(page-1)*limit` for legacy page-mode
// endpoints (GetConversations, GetChatHistory). Without it,
// page=999999&limit=200 issues an OFFSET 199.8M scan: the DB has to
// count through 200M rows before discarding them, latching for
// seconds while every other query waits behind it. The covered
// indexes mitigate but don't eliminate the cost. A request past
// this offset returns an empty page; clients that need deeper data
// should switch to cursor pagination (GetConversations supports it).
const maxPaginationOffset = 100_000

var allowedAgentModes = agentv1.OfficialAgentModeSet()

var allowedModelTiers = map[string]struct{}{
	"work-pro":  {},
	"work-plus": {},
}

const onlySupportedAgentType = "general_agent"

func normalizeAgentMode(mode string) string {
	normalized := strings.TrimSpace(mode)
	if normalized == "" {
		return ""
	}
	if _, ok := allowedAgentModes[normalized]; !ok {
		return ""
	}
	return normalized
}

func normalizeModelTier(model string) string {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if normalized == "chatexcel" {
		return "work-pro"
	}
	if _, ok := allowedModelTiers[normalized]; ok {
		return normalized
	}
	return ""
}

// CreateConversation creates a new conversation
func (api *AIChatApiNew) CreateConversation(c *gin.Context) {
	uid := utils.GetUserID(c)
	var request ConversationCreateRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, workAgentSmallJSONMaxBytes)
	if err := c.ShouldBindJSON(&request); err != nil {
		response.FailWithDetailed(err.Error(), "Failed to retrieve input", c)
		return
	}

	// Use ThreadLifecycleService to create thread with vector space
	title := strings.TrimSpace(request.Title)
	if title == "" {
		title = "Untitled"
	}
	// Mirror UpdateConversationSettings: 200-byte cap stops a long-
	// blob title from landing in the DB and the conversation list
	// (where the column is rendered, truncated, and used in JSON
	// payloads forwarded to the frontend on every list query).
	if len(title) > 200 {
		response.FailWithMessage("Conversation name too long", c)
		return
	}

	agentMode := normalizeAgentMode(request.AgentMode)
	if strings.TrimSpace(request.AgentMode) != "" && agentMode == "" {
		response.FailWithMessage("Invalid agentMode", c)
		return
	}

	// Plan-A Phase A3: optional project binding. Validate
	// ownership via project.ExistsForOwner so cross-tenant /
	// nonexistent / soft-deleted project ids are rejected with
	// a 4xx instead of being silently persisted. Skipping the
	// lookup on the empty case keeps legacy clients (omitted
	// projectId) byte-identical.
	if request.ProjectID > 0 {
		ok, err := project.Default().ExistsForOwner(request.ProjectID, uid)
		if err != nil {
			globals.Error(fmt.Sprintf("Failed to validate project %d for user %d: %v", request.ProjectID, uid, err))
			response.FailWithMessage("Failed to validate project", c)
			return
		}
		if !ok {
			response.FailWithMessage("Project not found", c)
			return
		}
	}

	chatThread, err := api.threadLifecycleService.CreateThreadNew(int(uid), title, agentMode, nil, request.ProjectID)
	if err != nil {
		globals.Error(fmt.Sprintf("Failed to create conversation: %v", err))
		response.FailWithMessage("Failed to create conversation", c)
		return
	}

	globals.Info(fmt.Sprintf("Created conversation %d for user %d", chatThread.Id, uid))
	response.OkWithData(ConversationResponse{
		Id:        strconv.Itoa(int(chatThread.Id)),
		ThreadID:  int(chatThread.Id),
		UUID:      chatThread.UUID,
		ProjectID: chatThread.ProjectID,
	}, c)
}

// GetConversations gets all conversations for the user with pagination support - Simplified version
func (api *AIChatApiNew) GetConversations(c *gin.Context) {
	uid := utils.GetUserID(c)

	// Pagination accepts two modes:
	//
	//   - Legacy page+limit (page=N, limit=M). The `idx_uid_agent_type_updated_at`
	//     index covers the WHERE + ORDER BY, but MySQL still scans+discards
	//     `offset` rows before returning. At a few hundred threads it's fine;
	//     at 10k+ (team plans) page 100+ becomes seconds.
	//
	//   - Cursor (cursor=<base64>, limit=M). Decoded into (updated_at, id);
	//     the WHERE adds `(updated_at, id) < (?, ?)` which the same composite
	//     index walks in order without offset scan. Cursor mode is what
	//     long lists should use; the response always includes nextCursor
	//     so clients can migrate.
	//
	// When `cursor` is supplied we ignore `page` (cursor wins). When neither
	// is supplied we serve page=1 in legacy mode for back-compat.
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "10"))
	if limit < 1 || limit > 100 {
		limit = 10
	}

	requestedAgentType := strings.TrimSpace(c.DefaultQuery("agent_type", ""))
	if requestedAgentType != "" && requestedAgentType != onlySupportedAgentType {
		globals.Warn(fmt.Sprintf("Unsupported agent_type '%s', fallback to '%s'", requestedAgentType, onlySupportedAgentType))
	}
	agentType := onlySupportedAgentType

	cursor := strings.TrimSpace(c.DefaultQuery("cursor", ""))
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}

	// §1 Project/Workspace Stage 2: optional project_id scope. 0 (or
	// missing / unparseable) means "all of the user's threads" — the
	// pre-Stage-2 behaviour. >0 filters via the
	// idx_workagent_thread_uid_project composite index (added in
	// migration 20260605).
	var projectIDScope uint
	if raw := strings.TrimSpace(c.Query("project_id")); raw != "" {
		if parsed, err := strconv.ParseUint(raw, 10, 64); err == nil && parsed > 0 {
			projectIDScope = uint(parsed)
		}
	}

	// Decode the wire-format cursor at the API boundary; the repo
	// takes pre-decoded (time, id) values so it doesn't have to know
	// about base64 / pipe-delimited formats. An invalid cursor is a
	// 4xx-shaped client error, distinct from the 5xx surface for DB
	// failures inside the repo.
	useCursor := cursor != ""
	var cursorTime time.Time
	var cursorID uint
	if useCursor {
		t, id, decodeErr := decodeConversationsCursor(cursor)
		if decodeErr != nil {
			response.FailWithMessage("Invalid cursor", c)
			return
		}
		cursorTime, cursorID = t, id
	}

	listResult, err := workagentService.DefaultThreadRepository().ListByOwner(workagentService.ListThreadsOptions{
		UID:            uid,
		AgentType:      agentType,
		ProjectID:      projectIDScope,
		CursorTime:     cursorTime,
		CursorID:       cursorID,
		Page:           page,
		Limit:          limit,
		MaxOffsetGuard: maxPaginationOffset,
	})
	if err != nil {
		globals.Error(fmt.Sprintf("Failed to get conversations: %v", err))
		response.FailWithMessage("ErrDatabaseOperation", c)
		return
	}

	// Offset-cap short-circuit: the repo refused a deep OFFSET that
	// would otherwise lock the DB. Mirror the original empty-page
	// response shape so legacy page-mode clients keep working without
	// the slow seek; cursor mode has its own continuation path past
	// this depth.
	if listResult.OffsetCapped {
		response.OkWithData(map[string]interface{}{
			"items": []map[string]interface{}{},
			"pagination": map[string]interface{}{
				"limit":      limit,
				"hasMore":    false,
				"nextCursor": "",
				"page":       page,
				"total":      listResult.TotalCount,
				"totalPages": 0,
			},
		}, c)
		return
	}

	threads := listResult.Threads
	hasMore := listResult.HasMore
	totalCount := listResult.TotalCount

	// Format response - Using new thread table fields for better performance
	conversations := make([]map[string]interface{}, 0, len(threads))
	for _, thread := range threads {
		// Truncate preview to 50 chars for consistency
		preview := thread.MsgPreview
		if len([]rune(preview)) > 50 {
			preview = string([]rune(preview)[:50]) + "..."
		}

		conversation := map[string]interface{}{
			"thread_id":     utils.ToString(thread.Id),
			"uuid":          thread.UUID,
			"projectId":     thread.ProjectID,
			"name":          thread.Name,
			"created_at":    thread.CreatedAt,
			"updated_at":    thread.UpdatedAt,
			"message_count": thread.MessageCount,
			"last_message":  preview,
			"config": map[string]interface{}{"model": func() string {
				if tier := normalizeModelTier(thread.Model); tier != "" {
					return tier
				}
				return "work-pro"
			}()},
			"hasFile":  thread.FileCount > 0,
			"fileInfo": map[string]interface{}{"count": thread.FileCount},
		}

		conversations = append(conversations, conversation)
	}

	// nextCursor is always populated when there's more data, regardless
	// of which mode the caller used — lets legacy page-mode clients
	// migrate one call site at a time. Repo surfaces the (time, id)
	// anchor; encoding stays at the API boundary.
	nextCursor := ""
	if hasMore && !listResult.NextCursorTime.IsZero() {
		nextCursor = encodeConversationsCursor(listResult.NextCursorTime, listResult.NextCursorID)
	}

	pagination := map[string]interface{}{
		"limit":      limit,
		"hasMore":    hasMore,
		"nextCursor": nextCursor,
	}
	if !useCursor {
		// Legacy fields for clients still doing page+totalPages math.
		// Cursor-mode responses skip these (totalCount is unknown by
		// design; sending a fake page/totalPages would mislead).
		totalPages := int((totalCount + int64(limit) - 1) / int64(limit))
		pagination["page"] = page
		pagination["total"] = totalCount
		pagination["totalPages"] = totalPages
	}

	response.OkWithData(map[string]interface{}{
		"items":      conversations,
		"pagination": pagination,
	}, c)
}

// encodeConversationsCursor + decodeConversationsCursor wrap the
// (updated_at, id) tuple in an opaque base64 string so clients
// can't reason about its internals. The server can change the
// shape (e.g. switch to id-only when ts collisions become an
// issue) without a coordinated client migration. Format is
// "<unixNano>|<id>" pre-encoding — chosen for round-trip
// stability across Go's time.Time location handling.
func encodeConversationsCursor(updatedAt time.Time, id uint) string {
	raw := fmt.Sprintf("%d|%d", updatedAt.UnixNano(), id)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeConversationsCursor(cursor string) (time.Time, uint, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, 0, err
	}
	parts := strings.SplitN(string(decoded), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, 0, fmt.Errorf("malformed cursor")
	}
	unixNano, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return time.Time{}, 0, err
	}
	id, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return time.Time{}, 0, err
	}
	return time.Unix(0, unixNano), uint(id), nil
}

func acquireThreadLifecycleLock(c *gin.Context, threadID uint64) (func(), bool) {
	threadIDStr := strconv.FormatUint(threadID, 10)
	turnLock := workagentService.GetAgentTurnLock(threadIDStr)
	if turnLock.TryLock() {
		return turnLock.Unlock, true
	}
	c.JSON(http.StatusConflict, gin.H{
		"code":    "THREAD_BUSY",
		"message": "An agent turn is still running in this conversation. Stop it before clearing or deleting the conversation.",
	})
	return nil, false
}

// DeleteConversation deletes a conversation owned by the caller.
// Access is scoped by uid so users cannot delete other users' threads.
func (api *AIChatApiNew) DeleteConversation(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}

	threadId := c.Param("id")
	if threadId == "" {
		response.FailWithMessage("threadId is required", c)
		return
	}

	threadIdUint, err := strconv.ParseUint(threadId, 10, 32)
	if err != nil {
		response.FailWithMessage("Invalid thread ID", c)
		return
	}

	// Verify the caller owns this thread before touching anything.
	// LoadByIDForOwner returns the same generic "not found" for both
	// missing-row and uid-mismatch cases (CWE-639 defence) — drops
	// the prior load-then-compare pattern which was the
	// existence-oracle shape we keep deliberately closing across this
	// file. Slight efficiency loss vs. the previous Select("id","uid")
	// narrow (we now load the full row), but the row is small and the
	// IDOR-shape consolidation is the more important win.
	if _, err := workagentService.DefaultThreadRepository().
		LoadByIDForOwner(uint(threadIdUint), uid); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.FailWithMessage("Conversation not found", c)
		} else {
			globals.Error(fmt.Sprintf("Failed to load thread %d: %v", threadIdUint, err))
			response.FailWithMessage("Failed to delete conversation", c)
		}
		return
	}
	releaseTurnLock, ok := acquireThreadLifecycleLock(c, threadIdUint)
	if !ok {
		return
	}
	defer releaseTurnLock()

	// Tear down any in-flight SSE streams for this thread BEFORE the
	// row + workspace get deleted. Without this, a long-running agent
	// turn (10-30 min) keeps streaming after the user clicks "Delete"
	// from another tab — its persist callbacks UPDATE against the
	// missing thread row (RowsAffected=0 silent), SyncOutputFiles can
	// re-create ThreadFile rows that point at a deleted workspace, and
	// the SDK keeps writing to the now-orphaned workspace path until
	// the OS finally tears it down. Closing the connection first lets
	// the handler's defer chain (release reservation, log) run cleanly
	// while the row still exists.
	threadIDStr := strconv.FormatUint(threadIdUint, 10)
	for _, conn := range workagentService.GetGlobalSSEManager().GetConnectionsByThread(threadIDStr) {
		conn.Close()
	}

	if err := api.threadLifecycleService.DeleteThread(uint(threadIdUint), int(uid)); err != nil {
		globals.Error(fmt.Sprintf("Failed to delete conversation and vector space: %v", err))
		response.FailWithMessage("Failed to delete conversation", c)
		return
	}

	globals.Info(fmt.Sprintf("Deleted conversation %d (uid=%d) and cleaned up vector space", threadIdUint, uid))
	response.Ok(c)
}

// UpdateConversationSettings updates conversation settings
func (api *AIChatApiNew) UpdateConversationSettings(c *gin.Context) {
	uid := utils.GetUserID(c)
	conversationId := c.Param("id")
	if conversationId == "" {
		response.FailWithMessage("Conversation ID is required", c)
		return
	}

	// Convert conversationId to uint
	conversationIdUint, err := strconv.ParseUint(conversationId, 10, 32)
	if err != nil {
		response.FailWithMessage("Invalid conversation ID", c)
		return
	}

	// Parse request body
	var updateRequest struct {
		Name         *string  `json:"name"`
		ContextCount *int     `json:"contextCount"`
		SystemPrompt *string  `json:"systemPrompt"`
		Temperature  *float64 `json:"temperature"`
		MaxTokens    *int     `json:"maxTokens"`
		AgentMode    *string  `json:"agentMode"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, workAgentSmallJSONMaxBytes)
	if err := c.ShouldBindJSON(&updateRequest); err != nil {
		response.FailWithMessage("Invalid request format", c)
		return
	}

	// Get the conversation to verify ownership. LoadByIDForOwner bakes
	// the uid predicate into the query — a forgotten ownership check
	// at this call site can't accidentally serve another user's row.
	threadPtr, err := workagentService.DefaultThreadRepository().
		LoadByIDForOwner(uint(conversationIdUint), uid)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.FailWithMessage("Conversation not found", c)
		} else {
			response.FailWithMessage("Database error", c)
		}
		return
	}
	thread := *threadPtr

	// Acquire the same AgentTurnLock the agent itself holds during a
	// turn. Settings updates that race a live turn would otherwise be
	// able to (a) clear agent_session_id / agent_session_created_at
	// from under the SDK call when AgentMode changes (lines 552-555
	// below), or (b) rewrite system_prompt / context_count that the
	// preflight composer reads on the same row. The lock makes
	// "settings save while a turn is in flight" deterministic — the
	// save waits or surfaces 409 to the user, matching the just-
	// shipped Delete / Clear / ClearMessages pattern at
	// acquireThreadLifecycleLock (line 333-345).
	releaseTurnLock, ok := acquireThreadLifecycleLock(c, conversationIdUint)
	if !ok {
		return
	}
	defer releaseTurnLock()

	// Update fields that are provided
	updates := make(map[string]interface{})

	if updateRequest.Name != nil {
		// Validate name length
		if len(strings.TrimSpace(*updateRequest.Name)) == 0 {
			response.FailWithMessage("Conversation name cannot be empty", c)
			return
		}
		if len(*updateRequest.Name) > 200 {
			response.FailWithMessage("Conversation name too long", c)
			return
		}
		updates["name"] = strings.TrimSpace(*updateRequest.Name)
	}

	if updateRequest.ContextCount != nil {
		if *updateRequest.ContextCount < 0 || *updateRequest.ContextCount > 50 {
			response.FailWithMessage("Invalid context count", c)
			return
		}
		updates["context_count"] = *updateRequest.ContextCount
	}

	if updateRequest.SystemPrompt != nil {
		if len(*updateRequest.SystemPrompt) > maxSystemPromptBytes {
			response.FailWithMessage(fmt.Sprintf("System prompt exceeds %d bytes", maxSystemPromptBytes), c)
			return
		}
		updates["prompt"] = *updateRequest.SystemPrompt
	}

	// ⚠ WIRE STATUS (2026-05-14 audit): Temperature + MaxTokens are
	// persisted but NEVER read by the agent runtime. Both fields are
	// validated, saved to chat_thread, and returned by
	// GetConversationSettings for legacy rows/clients.
	// No production code path passes them into the Claude Agent SDK call
	// (see ProcessAgentConversationWithRetry's signature — accepts
	// agentMode + customPrompt + systemAdditions, no temperature /
	// max_tokens). The fixed values used by sub-agents (script /
	// shotboard / shot-prompt generators in service/tools/workagent/
	// production_tools_*.go) are HARDCODED per generator.
	//
	// The web ConversationSettings modal no longer renders these
	// controls because user-visible no-op knobs are misleading.
	// Keep accepting them for old clients so persisted values aren't
	// lost during rollout.
	if updateRequest.Temperature != nil {
		// math.IsNaN / IsInf BEFORE the range check — NaN comparisons
		// return false, so a NaN payload would silently slip through
		// `< 0 || > 2` and land in the DB, corrupting every
		// downstream LLM call that reads the row.
		if math.IsNaN(*updateRequest.Temperature) || math.IsInf(*updateRequest.Temperature, 0) {
			response.FailWithMessage("Invalid temperature", c)
			return
		}
		if *updateRequest.Temperature < 0 || *updateRequest.Temperature > 2 {
			response.FailWithMessage("Invalid temperature (must be between 0 and 2)", c)
			return
		}
		updates["temperature"] = *updateRequest.Temperature
	}

	if updateRequest.MaxTokens != nil {
		if *updateRequest.MaxTokens < 100 || *updateRequest.MaxTokens > 8000 {
			response.FailWithMessage("Invalid maxTokens (must be between 100 and 8000)", c)
			return
		}
		updates["max_tokens"] = *updateRequest.MaxTokens
	}

	if updateRequest.AgentMode != nil {
		mode := normalizeAgentMode(*updateRequest.AgentMode)
		if mode == "" {
			response.FailWithMessage("Invalid agentMode", c)
			return
		}
		updates["agent_mode"] = mode
		// Mode change MUST reset the SDK session — the session was
		// created against the previous mode's system prompt; reusing
		// it (WithResume on the next turn) replays old-mode context
		// and silently ignores the user's mode switch. The chat-path
		// equivalent (agent_api.go HandleAgentChat lines 388-403) does
		// this same reset; if only one of the two reset paths is in
		// place, the Settings-modal switch silently lags behind chat-
		// driven switches by exactly one turn (or longer if the user
		// doesn't re-send). Mirror it here.
		if mode != thread.AgentMode {
			updates["agent_session_id"] = ""
			updates["agent_session_created_at"] = nil
		}
	}

	// Update the record. ApplyUpdatesForOwner bakes BOTH the uid
	// ownership check AND the ThreadCache invalidation into the same
	// path — the previous shape did the load/update/invalidate as
	// three separate steps, and the cache bust was a manual call site
	// that twice shipped without it (see ce4796ee history). Going
	// forward, "row updated, cache stale for 5 minutes" is impossible
	// at this surface unless someone reaches around the repo.
	if len(updates) > 0 {
		updates["updated_at"] = time.Now()
		if err := workagentService.DefaultThreadRepository().
			ApplyUpdatesForOwner(uint(conversationIdUint), uid, updates); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				response.FailWithMessage("Conversation not found", c)
				return
			}
			globals.Error(fmt.Sprintf("Failed to update conversation settings: %v", err))
			response.FailWithMessage("Failed to update conversation settings", c)
			return
		}
	}

	globals.Info(fmt.Sprintf("Updated conversation %d settings for user %d", conversationIdUint, uid))
	response.OkWithMessage("Conversation settings updated successfully", c)
}

// GetConversationByUuid fetches one conversation by UUID for the
// authenticated owner. Used by the SPA to land on a deep-history
// thread URL without paging through the conversation list — the
// pre-existing flow walked up to 10 pages serially when the URL UUID
// wasn't on the first page (10 sequential GETs on cold load).
//
// Same response shape as one row inside GetConversations so the
// frontend can normalize via the same conversationAdapter helper.
//
// Auth model mirrors the rest of the private CRUD surface: the WHERE
// clause carries `uid = ?` so a cross-tenant lookup is indistinguishable
// from "not found" — no oracle for thread enumeration. UUIDs are v4
// random so guessing is impractical, but defense in depth.
func (api *AIChatApiNew) GetConversationByUuid(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}

	threadUUID := strings.TrimSpace(c.Param("uuid"))
	if threadUUID == "" {
		response.FailWithMessage("UUID is required", c)
		return
	}

	// Validate UUID shape before issuing the query — a non-UUID input
	// turns into a string-comparison scan otherwise. Rejecting at the
	// edge keeps the DB query simple and avoids leaking parser quirks
	// as a probe channel.
	if _, parseErr := uuid.Parse(threadUUID); parseErr != nil {
		response.FailWithMessage("Conversation not found", c)
		return
	}

	threadPtr, err := workagentService.DefaultThreadRepository().
		LoadByUUIDForOwner(threadUUID, uid, onlySupportedAgentType)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.FailWithMessage("Conversation not found", c)
			return
		}
		globals.Error(fmt.Sprintf("[GetConversationByUuid] DB lookup failed for uuid %s: %v", threadUUID, err))
		response.FailWithMessage("Database error", c)
		return
	}
	thread := *threadPtr

	preview := thread.MsgPreview
	if len([]rune(preview)) > 50 {
		preview = string([]rune(preview)[:50]) + "..."
	}

	// Same shape as one row from GetConversations.items — the SPA
	// normaliser doesn't care whether the row came from a list or a
	// single-fetch endpoint, so reusing the shape lets the new path
	// drop straight in.
	conversation := map[string]interface{}{
		"thread_id":     utils.ToString(thread.Id),
		"uuid":          thread.UUID,
		"projectId":     thread.ProjectID,
		"name":          thread.Name,
		"created_at":    thread.CreatedAt,
		"updated_at":    thread.UpdatedAt,
		"message_count": thread.MessageCount,
		"last_message":  preview,
		"config": map[string]interface{}{"model": func() string {
			if tier := normalizeModelTier(thread.Model); tier != "" {
				return tier
			}
			return "work-pro"
		}()},
		"hasFile":  thread.FileCount > 0,
		"fileInfo": map[string]interface{}{"count": thread.FileCount},
	}

	response.OkWithData(conversation, c)
}

// GetConversationSettings gets conversation settings
func (api *AIChatApiNew) GetConversationSettings(c *gin.Context) {
	uid := utils.GetUserID(c)
	conversationId := c.Param("id")
	if conversationId == "" {
		response.FailWithMessage("Conversation ID is required", c)
		return
	}

	// Convert conversationId to uint
	conversationIdUint, err := strconv.ParseUint(conversationId, 10, 32)
	if err != nil {
		response.FailWithMessage("Invalid conversation ID", c)
		return
	}

	// Get the conversation via the repo's owner-scoped load — uid
	// predicate is in the WHERE so a forgotten ownership check can't
	// silently serve another user's settings.
	threadPtr, err := workagentService.DefaultThreadRepository().
		LoadByIDForOwner(uint(conversationIdUint), uid)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.FailWithMessage("Conversation not found", c)
		} else {
			response.FailWithMessage("Database error", c)
		}
		return
	}
	thread := *threadPtr

	// Create response with current settings
	settingsResponse := map[string]interface{}{
		"id":           thread.Id,
		"name":         thread.Name,
		"contextCount": thread.ContextCount,
		"systemPrompt": thread.Prompt,
		"temperature":  thread.Temperature,
		"maxTokens":    thread.MaxTokens,
		"agentMode":    thread.AgentMode,
		"isPublic":     thread.IsPublic,
		"model": func() string {
			if tier := normalizeModelTier(thread.Model); tier != "" {
				return tier
			}
			return "work-pro"
		}(),
		"createdAt": thread.CreatedAt,
		"updatedAt": thread.UpdatedAt,
	}

	response.OkWithData(settingsResponse, c)
}

// SetConversationVisibility flips a thread's is_public flag. Owner-only.
//
// The public share endpoints (GetConversationDetail, GetMessageDetail
// in conversation_share.go) check this column and 404 when false, so
// flipping is_public=false is the canonical way to revoke a previously
// shared URL without rotating the thread UUID. The UUID stays stable
// so the owner can re-share later by flipping back.
func (api *AIChatApiNew) SetConversationVisibility(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}

	conversationId := c.Param("id")
	if conversationId == "" {
		response.FailWithMessage("Conversation ID is required", c)
		return
	}
	conversationIdUint, err := strconv.ParseUint(conversationId, 10, 32)
	if err != nil {
		response.FailWithMessage("Invalid conversation ID", c)
		return
	}

	// Pointer so the caller has to send the field explicitly — a
	// missing key won't silently coerce to false and unshare a thread
	// the user only meant to re-fetch.
	var body struct {
		IsPublic *bool `json:"isPublic"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, workAgentSmallJSONMaxBytes)
	if err := c.ShouldBindJSON(&body); err != nil || body.IsPublic == nil {
		response.FailWithMessage("isPublic is required", c)
		return
	}

	// Defensive symmetry with UpdateConversationSettings / Delete /
	// Clear: even though `is_public` is metadata the agent turn
	// doesn't read mid-execution, locking here means "settings panel
	// can never race a turn" is a uniform contract across every
	// thread-state mutator. Cheap, prevents the "user toggles share
	// at the exact moment the agent finishes" edge case from
	// surfacing a public-share read of a half-written final message.
	releaseTurnLock, ok := acquireThreadLifecycleLock(c, conversationIdUint)
	if !ok {
		return
	}
	defer releaseTurnLock()

	// SetVisibility scopes the UPDATE by uid in the same WHERE and
	// invalidates ThreadCache on commit — both protections come from
	// the repo so a future call site can't accidentally regress
	// either. The cache bust is defence-in-depth: current readers
	// don't gate on IsPublic, but a future reader shouldn't be
	// silently broken by a stale hit.
	if err := workagentService.DefaultThreadRepository().
		SetVisibility(uint(conversationIdUint), uid, *body.IsPublic); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Same generic message as DeleteConversation's ownership
			// check — don't leak that the row exists under a
			// different uid.
			response.FailWithMessage("Conversation not found", c)
			return
		}
		globals.Error(fmt.Sprintf("Failed to update is_public on thread %d: %v", conversationIdUint, err))
		response.FailWithMessage("Failed to update visibility", c)
		return
	}

	response.OkWithData(map[string]interface{}{"isPublic": *body.IsPublic}, c)
}

// ClearMessages clears only messages from a conversation, keeping files and suggestions
func (api *AIChatApiNew) ClearMessages(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}
	threadId := c.Param("id")
	if threadId == "" {
		response.FailWithMessage("Conversation ID is required", c)
		return
	}

	// Convert threadId to uint
	threadIdUint, err := strconv.ParseUint(threadId, 10, 32)
	if err != nil {
		response.FailWithMessage("Invalid conversation ID", c)
		return
	}
	if _, err := workagentService.DefaultThreadRepository().
		LoadByIDForOwner(uint(threadIdUint), uid); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.FailWithMessage("Conversation not found", c)
		} else {
			globals.Error(fmt.Sprintf("Failed to load thread %d: %v", threadIdUint, err))
			response.FailWithMessage("Failed to clear conversation messages", c)
		}
		return
	}
	releaseTurnLock, ok := acquireThreadLifecycleLock(c, threadIdUint)
	if !ok {
		return
	}
	defer releaseTurnLock()

	// Use ThreadLifecycleService to clear only messages
	err = api.threadLifecycleService.ClearMessages(uint(threadIdUint), int(uid))
	if err != nil {
		globals.Error(fmt.Sprintf("Failed to clear messages for conversation %d (user %d): %v", threadIdUint, uid, err))
		response.FailWithMessage("Failed to clear conversation messages", c)
		return
	}

	globals.Info(fmt.Sprintf("Cleared messages for conversation %d (user %d)", threadIdUint, uid))

	// Update thread statistics after clearing messages
	if err := api.updateThreadStatistics(uint(threadIdUint)); err != nil {
		globals.Error(fmt.Sprintf("Failed to update thread statistics after clear: %v", err))
	}

	response.OkWithData(map[string]interface{}{
		"conversationId": threadId,
		"message":        "Conversation messages cleared successfully",
		"clearedAt":      time.Now(),
	}, c)
}

// ClearConversation clears all content from a conversation
func (api *AIChatApiNew) ClearConversation(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}
	threadId := c.Param("id")
	if threadId == "" {
		response.FailWithMessage("Conversation ID is required", c)
		return
	}

	// Convert threadId to uint
	threadIdUint, err := strconv.ParseUint(threadId, 10, 32)
	if err != nil {
		response.FailWithMessage("Invalid conversation ID", c)
		return
	}
	if _, err := workagentService.DefaultThreadRepository().
		LoadByIDForOwner(uint(threadIdUint), uid); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.FailWithMessage("Conversation not found", c)
		} else {
			globals.Error(fmt.Sprintf("Failed to load thread %d: %v", threadIdUint, err))
			response.FailWithMessage("Failed to clear conversation content", c)
		}
		return
	}
	releaseTurnLock, ok := acquireThreadLifecycleLock(c, threadIdUint)
	if !ok {
		return
	}
	defer releaseTurnLock()

	// Use ThreadLifecycleService to clear thread content
	err = api.threadLifecycleService.ClearThread(uint(threadIdUint), int(uid))
	if err != nil {
		globals.Error(fmt.Sprintf("Failed to clear conversation %d for user %d: %v", threadIdUint, uid, err))
		response.FailWithMessage("Failed to clear conversation content", c)
		return
	}

	// Mirror ClearMessages above — without this, message_count keeps
	// the pre-clear value while the message rows are gone, and the
	// conversation list shows stale counts forever.
	if err := api.updateThreadStatistics(uint(threadIdUint)); err != nil {
		globals.Error(fmt.Sprintf("Failed to update thread statistics after clear: %v", err))
	}

	globals.Info(fmt.Sprintf("Cleared conversation %d for user %d", threadIdUint, uid))
	response.OkWithData(map[string]interface{}{
		"conversationId": threadId,
		"message":        "Conversation cleared successfully",
		"clearedAt":      time.Now(),
	}, c)
}

// GetChatHistory gets chat history for a conversation
func (api *AIChatApiNew) GetChatHistory(c *gin.Context) {
	conversationID := c.Query("conversationId")
	if conversationID == "" {
		response.FailWithMessage("Missing conversation ID", c)
		return
	}

	uid := utils.GetUserID(c)
	threadID, err := strconv.Atoi(conversationID)
	if err != nil {
		response.FailWithMessage("Invalid conversation ID", c)
		return
	}

	// Get pagination parameters
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))

	// Validate pagination parameters
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	offset := (page - 1) * limit

	// Refuse out-of-bound offset before touching the DB. See
	// maxPaginationOffset rationale: a deep OFFSET scan locks the
	// table for seconds; cap it. Clients needing deeper history
	// should chunk via earlier pages or switch to message-id-cursor
	// (a future endpoint).
	if offset > maxPaginationOffset {
		response.OkWithData(map[string]interface{}{
			"messages": []map[string]interface{}{},
			"pagination": map[string]interface{}{
				"page":       page,
				"limit":      limit,
				"total":      0,
				"totalPages": 0,
				"hasMore":    false,
			},
		}, c)
		return
	}

	// Verify thread ownership via the repo. Same uid-in-WHERE
	// defence as the other handlers in this file — never load-then-
	// compare, the predicate is part of the query.
	threadPtr, err := workagentService.DefaultThreadRepository().
		LoadByIDForOwner(uint(threadID), uid)
	if err != nil {
		response.FailWithMessage("Conversation not found or access denied", c)
		return
	}
	thread := *threadPtr

	// Both reads now go through MessageRepository — the canonical
	// column-narrow lives there (chatHistoryColumns) so a future
	// caller can't accidentally pull the full row over the wire by
	// writing a list query inline. Same ~1MB-or-less envelope the
	// previous inline Select promised, just enforced at one place.
	msgRepo := workagentService.DefaultMessageRepository()
	totalCount, err := msgRepo.CountByThread(uint(threadID))
	if err != nil {
		globals.GraLog.Error(fmt.Sprintf("Failed to count messages: %v", err))
		response.FailWithMessage("Failed to get message count", c)
		return
	}

	messages, err := msgRepo.ListByThread(uint(threadID), page, limit)
	if err != nil {
		globals.GraLog.Error(fmt.Sprintf("Failed to fetch messages: %v", err))
		response.FailWithMessage("Failed to fetch messages", c)
		return
	}

	// Prepare workspace root for sanitizing structured content
	workspaceRoot := ""
	if manager := workagentService.GetAgentClientManager(); manager != nil {
		workspaceRoot = manager.WorkspaceRoot()
	}

	// Format messages for response - Use original format expected by frontend
	// Frontend expects: { id, userText, aiText, created, updated }
	formattedMessages := make([]map[string]interface{}, 0, len(messages))
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		sanitizedContent := msg.StructuredContent
		if sanitizedContent != "" {
			sanitizedContent = workagentService.ReplaceWorkspacePaths(sanitizedContent, workspaceRoot)
		}
		if msg.ContentType == "agent_conversation" && sanitizedContent != "" {
			preview := sanitizedContent
			// Rune-safe truncation — sanitizedContent flows through
			// JSON-encoded structured agent output that commonly
			// contains CJK and emoji from the model. byte-slice
			// [:500] cut multi-byte sequences mid-codepoint, producing
			// invalid UTF-8 in the log line and downstream tooling
			// that parses log JSON. Same fix applied in 1f73df4e
			// (GetContentSummary), fa2141f4 (RecordFailure),
			// 136dff3d (recordTestFailure), a9ae3791
			// (RecordSessionNotFound), 690f9b15 (test runner).
			runes := []rune(preview)
			if len(runes) > 500 {
				preview = string(runes[:500]) + "...(truncated)"
			}
			globals.Info(fmt.Sprintf("[Agent Debug] Thread %d message %d structured content: %s", threadID, msg.Id, preview))
		}
		// Return in the original format the frontend expects
		formattedMessages = append(formattedMessages, map[string]interface{}{
			"ID":         msg.Id,        // Frontend looks for msg.Id || msg.id
			"id":         msg.Id,        // Fallback for compatibility
			"uuid":       msg.UUID,      // UUID for sharing
			"userText":   msg.UserText,  // Frontend expects userText field
			"aiText":     msg.AIText,    // Frontend expects aiText field
			"created_at": msg.CreatedAt, // Frontend expects created timestamp
			"updated_at": msg.UpdatedAt, // Frontend expects updated timestamp
			"model":      msg.Model,     // Additional metadata
			"threadId":   msg.ThreadID,  // Thread relationship
			"chatMode":   msg.ChatMode,  // Chat mode: normal or agent
			// Agent Message System fields
			"contentType":       msg.ContentType,  // Message content type
			"structuredContent": sanitizedContent, // Structured content JSON
			"actions":           msg.Actions,      // Interactive actions JSON
			"metadata":          msg.Metadata,     // Metadata JSON
			// Critique-loop fields (P0 #3). Surface the persisted
			// rating + feedback so the bubble re-renders in its
			// already-rated state when the user reloads the
			// conversation; without these, every reload would show
			// the empty 👍/👎 buttons even though the row holds the
			// previous vote.
			"userRating":   msg.UserRating,
			"userFeedback": msg.UserFeedback,
		})
	}

	// Calculate pagination metadata
	totalPages := int((totalCount + int64(limit) - 1) / int64(limit))

	response.OkWithData(map[string]interface{}{
		"messages": formattedMessages,
		"pagination": map[string]interface{}{
			"page":       page,
			"limit":      limit,
			"total":      totalCount,
			"totalPages": totalPages,
			"hasMore":    page < totalPages,
		},
		"conversationInfo": map[string]interface{}{
			"id":         thread.Id,
			"name":       thread.Name,
			"created_at": thread.CreatedAt,
			"updated_at": thread.UpdatedAt,
			// latestPlan rehydrates the Progress section even when the
			// emitting message has been paginated out. Empty string
			// when the thread has never fired TodoWrite, in which case
			// the frontend falls back to the in-memory message walk.
			"latestPlan": thread.LatestPlan,
			// planHistory is the rolling N=10 snapshot array. When set
			// the frontend rebuilds the FULL timeline UI (active plan
			// + earlier phases) without paging back through messages.
			// Frontend prefers planHistory > latestPlan > message walk.
			"planHistory": thread.PlanHistory,
		},
	}, c)
}
