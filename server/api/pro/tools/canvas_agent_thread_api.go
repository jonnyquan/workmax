package tools

// Canvas Agent thread CRUD — list / read / append for the work_agent
// thread surface used by the Canvas Agent (canvas-mode threads in the
// shared w_workagent_thread table) and direct image/video generation
// turns.
//
// Sibling files: canvas_shared.go (canvasChatMaxMessageChars + the
// idempotency / prompt-guard helpers preserved from the now-retired
// canvas_chat_api.go), canvas_optimize_prompt_api.go, canvas_agent_api.go
// (the SSE AgentChat handler itself, kept separate because it owns the
// canvas-specific PromptGuard + SDK-driven per-turn loop; R3 phase 4 will
// fold it into workagent's shared dispatcher).

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"server/globals"
	workagentModel "server/model/workagent"
	canvas "server/service/tools/workagent/surfaces/canvas"
	"server/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	// canvasDirectMessageMaxBodyBytes caps AppendAgentMessage payloads.
	// 1 MiB is generous for a single chat turn — bigger payloads usually
	// indicate an attachment that should have gone through the upload
	// endpoint instead.
	canvasDirectMessageMaxBodyBytes = 1 * 1024 * 1024

	// canvasAgentMaxPaginationOffset mirrors the workagent api/ cap.
	// Keeps the OFFSET scan bounded; clients past this depth should
	// switch to a future cursor-mode endpoint or trim their pagination.
	canvasAgentMaxPaginationOffset = 100_000

	// canvasAgentMessageCap caps how many messages AgentMessages returns
	// per call. Without it, a long thread (1000+ messages) ships
	// multi-MB on every poll. 500 is well above any realistic chat
	// session and matches the public share endpoint cap.
	canvasAgentMessageCap = 500
)

type canvasAppendMessageRequest struct {
	ConversationID    string          `json:"conversationId"`
	ProjectID         string          `json:"projectId,omitempty"`
	UserText          string          `json:"userText,omitempty"`
	AIText            string          `json:"aiText,omitempty"`
	ChatMode          string          `json:"chatMode,omitempty"`
	Model             string          `json:"model,omitempty"`
	ContentType       string          `json:"contentType,omitempty"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
}

// AgentThreads lists Canvas Agent conversation threads for the current user.
// Filters by agent_type=canvas to only return canvas-specific threads.
// @Summary List Canvas Agent Threads
// @Tags Canvas
// @Produce json
// @Router /api/tools/canvas/agent/threads [get]
func (a *CanvasApi) AgentThreads(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	projectID, err := parseAndAuthorizeCanvasThreadProjectID(c.Query("projectId"), uid)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid projectId"})
		return
	}

	// Optional pagination. strconv.Atoi (not fmt.Sscanf — the
	// latter silently leaves p/ps at zero on bad input, which
	// the floors below then quietly correct to defaults; cleaner
	// to ignore the parse error here and let the floors do their
	// job explicitly).
	page := c.DefaultQuery("page", "1")
	pageSize := c.DefaultQuery("pageSize", "20")
	p, _ := strconv.Atoi(page)
	ps, _ := strconv.Atoi(pageSize)
	if p < 1 {
		p = 1
	}
	if ps < 1 || ps > 100 {
		ps = 20
	}

	// Cap effective offset before issuing the query. page=999999&pageSize=100
	// would otherwise OFFSET 99.9M and lock the table for seconds; same
	// fix as conversation_api.GetConversations / GetChatHistory.
	offset := (p - 1) * ps
	if offset > canvasAgentMaxPaginationOffset {
		c.JSON(http.StatusOK, gin.H{
			"threads":  []workagentModel.ChatThreadResponse{},
			"total":    int64(0),
			"page":     p,
			"pageSize": ps,
		})
		return
	}

	query := globals.GraDBs["system"].WithContext(c.Request.Context()).
		Where("agent_type = ?", canvas.CanvasAgentType).
		Order("updated_at DESC")
	if projectID > 0 {
		query = query.Where("project_id = ?", projectID)
	} else {
		query = query.Where("uid = ?", uid)
	}

	var total int64
	if err := query.Model(&workagentModel.ChatThread{}).Count(&total).Error; err != nil {
		globals.Warn(fmt.Sprintf("[CanvasAgent] Failed to count threads (uid=%d, project=%d): %v", uid, projectID, err))
		// Don't fail the whole list call — the threads page still
		// renders without total; total=0 just means the pager
		// shows "no more pages" which is a benign degradation.
	}

	var threads []workagentModel.ChatThread
	if err := query.Offset(offset).Limit(ps).Find(&threads).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch threads"})
		return
	}

	// Convert to response format
	responses := make([]workagentModel.ChatThreadResponse, len(threads))
	for i, t := range threads {
		responses[i] = t.ToResponse()
	}

	c.JSON(http.StatusOK, gin.H{
		"threads":  responses,
		"total":    total,
		"page":     p,
		"pageSize": ps,
	})
}

// AgentMessages returns messages for a specific Canvas Agent thread.
// @Summary Get Canvas Agent Thread Messages
// @Tags Canvas
// @Produce json
// @Param id path int true "Thread ID"
// @Router /api/tools/canvas/agent/threads/{id}/messages [get]
func (a *CanvasApi) AgentMessages(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	threadIDStr := c.Param("threadId")
	threadID, _ := strconv.Atoi(threadIDStr)
	if threadID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid thread ID"})
		return
	}
	projectID, err := parseAndAuthorizeCanvasThreadProjectID(c.Query("projectId"), uid)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid projectId"})
		return
	}

	// Verify thread ownership and type.
	//
	// When projectID > 0, the upstream parseAndAuthorizeCanvasThreadProjectID
	// has already confirmed the user can view that project via
	// project.Repository.CanViewProject (which covers owner +
	// shared-with viewers). The OR clause below intentionally
	// relaxes the per-uid filter in that case so any project
	// viewer can read its canvas threads — that's the documented
	// sharing semantic, not an IDOR oversight.
	var thread workagentModel.ChatThread
	threadQuery := globals.GraDBs["system"].WithContext(c.Request.Context()).
		Where("id = ? AND agent_type = ?", threadID, canvas.CanvasAgentType)
	if projectID > 0 {
		threadQuery = threadQuery.Where("project_id = ? OR (project_id = 0 AND uid = ?)", projectID, uid)
	} else {
		threadQuery = threadQuery.Where("uid = ?", uid)
	}
	if err := threadQuery.First(&thread).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Thread not found"})
		return
	}

	beforeID := uint64(0)
	if raw := strings.TrimSpace(c.Query("beforeId")); raw != "" {
		if parsed, err := strconv.ParseUint(raw, 10, 64); err == nil {
			beforeID = parsed
		}
	}

	// Fetch messages with a cursor cap. The previous unbounded query
	// would ship every message in a long-running thread on every
	// poll — multi-MB responses, plus the JSON marshal cost on the
	// server side. `hasMore/nextBeforeId` lets the client explicitly
	// load older history instead of silently losing it.
	var messages []workagentModel.ChatMessage
	query := globals.GraDBs["system"].WithContext(c.Request.Context()).
		Where("thread_id = ?", threadID).
		Order("created_at DESC, id DESC").
		Limit(canvasAgentMessageCap)
	if projectID == 0 {
		query = query.Where("uid = ?", uid)
	}
	if beforeID > 0 {
		query = query.Where("id < ?", beforeID)
	}
	if err := query.Find(&messages).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch messages"})
		return
	}
	hasMore := len(messages) >= canvasAgentMessageCap
	nextBeforeID := uint64(0)
	if hasMore && len(messages) > 0 {
		nextBeforeID = uint64(messages[len(messages)-1].Id)
	}
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	// Convert to response format
	responses := make([]workagentModel.ChatMessageResponse, len(messages))
	for i, m := range messages {
		responses[i] = m.ToResponse()
	}

	c.JSON(http.StatusOK, gin.H{
		"thread":    thread.ToResponse(),
		"messages":  responses,
		"truncated": hasMore,
		"hasMore":   hasMore,
		"nextBeforeId": func() any {
			if nextBeforeID == 0 {
				return nil
			}
			return nextBeforeID
		}(),
	})
}

// AppendAgentMessage persists a non-SDK Canvas chat turn into the same
// workagent thread used by Canvas Agent. Direct image/video generation uses
// this so refresh/reopen shows a single unified message timeline.
// @Summary Append Canvas Chat Message
// @Tags Canvas
// @Accept json
// @Produce json
// @Router /api/tools/canvas/agent/messages [post]
func (a *CanvasApi) AppendAgentMessage(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, canvasDirectMessageMaxBodyBytes)
	var req canvasAppendMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}

	userText := strings.TrimSpace(req.UserText)
	aiText := strings.TrimSpace(req.AIText)
	if userText == "" && aiText == "" && len(req.StructuredContent) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "message content is required"})
		return
	}
	if utf8.RuneCountInString(userText) > canvasChatMaxMessageChars ||
		utf8.RuneCountInString(aiText) > canvasChatMaxMessageChars {
		c.JSON(http.StatusBadRequest, gin.H{"error": "message is too long"})
		return
	}
	if len(req.StructuredContent) > 256*1024 || len(req.Metadata) > 64*1024 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "message metadata is too large"})
		return
	}
	projectID, err := parseAndAuthorizeCanvasThreadProjectIDForEdit(req.ProjectID, uid)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid projectId"})
		return
	}

	chatMode := strings.TrimSpace(req.ChatMode)
	if chatMode == "" {
		chatMode = string(workagentModel.ChatModeAgent)
	}
	if chatMode != "agent" && chatMode != "image" && chatMode != "video" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "chatMode is invalid"})
		return
	}

	contentType := strings.TrimSpace(req.ContentType)
	if contentType != "" && utf8.RuneCountInString(contentType) > 50 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "contentType is too long"})
		return
	}
	modelName := strings.TrimSpace(req.Model)
	if utf8.RuneCountInString(modelName) > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model is too long"})
		return
	}

	db := globals.GraDBs["system"]
	threadUUID := strings.TrimSpace(req.ConversationID)
	if threadUUID == "" {
		threadUUID = uuid.New().String()
	}

	var thread workagentModel.ChatThread
	var message workagentModel.ChatMessage
	var existingMessage *workagentModel.ChatMessage
	if err := db.Transaction(func(tx *gorm.DB) error {
		threadQuery := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("uuid = ? AND agent_type = ?", threadUUID, canvas.CanvasAgentType)
		if projectID > 0 {
			threadQuery = threadQuery.Where("project_id = ? OR (project_id = 0 AND uid = ?)", projectID, int(uid))
		} else {
			threadQuery = threadQuery.Where("uid = ?", int(uid))
		}
		if err := threadQuery.First(&thread).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("load thread: %w", err)
			}
			titleSource := userText
			if titleSource == "" {
				titleSource = aiText
			}
			if titleSource == "" {
				titleSource = "Canvas chat"
			}
			thread = workagentModel.ChatThread{
				UID:       int(uid),
				UUID:      threadUUID,
				ProjectID: projectID,
				Name:      canvas.TruncateString(titleSource, 50),
				AgentMode: canvas.CanvasAgentMode,
				AgentType: canvas.CanvasAgentType,
				Model:     "canvas-chat",
			}
			if err := tx.Create(&thread).Error; err != nil {
				// Direct user/result messages can arrive close together for a new
				// generated UUID. If another request won the insert race, reuse it
				// under a row lock so the message append and preview/count update
				// stay ordered.
				fetchQuery := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
					Where("uuid = ? AND agent_type = ?", threadUUID, canvas.CanvasAgentType)
				if projectID > 0 {
					fetchQuery = fetchQuery.Where("project_id = ? OR (project_id = 0 AND uid = ?)", projectID, int(uid))
				} else {
					fetchQuery = fetchQuery.Where("uid = ?", int(uid))
				}
				if fetchErr := fetchQuery.First(&thread).Error; fetchErr != nil {
					return fmt.Errorf("create thread: %w", err)
				}
			}
		}
		scopedProjectID, err := ensureCanvasThreadProjectScope(thread.ProjectID, projectID)
		if err != nil {
			return err
		}
		if scopedProjectID > 0 && thread.ProjectID == 0 {
			if err := tx.Model(&workagentModel.ChatThread{}).
				Where("id = ?", thread.Id).
				Update("project_id", scopedProjectID).Error; err != nil {
				return fmt.Errorf("scope thread: %w", err)
			}
			thread.ProjectID = scopedProjectID
		}

		metadata := string(req.Metadata)
		var messageIdempotencyKey *string
		if key, nextMetadata := buildCanvasDirectMessageIdempotencyKey(req, chatMode, contentType); key != "" {
			metadata = nextMetadata
			var existing workagentModel.ChatMessage
			if err := tx.Where("thread_id = ? AND uid = ? AND message_idempotency_key = ?", int(thread.Id), int(uid), key).
				Order("id DESC").
				First(&existing).Error; err == nil {
				existingMessage = &existing
				return nil
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("find idempotent message: %w", err)
			}
			messageIdempotencyKey = &key
		}

		message = workagentModel.ChatMessage{
			UUID:                  uuid.New().String(),
			ThreadID:              int(thread.Id),
			UID:                   int(uid),
			UserText:              userText,
			AIText:                aiText,
			ChatMode:              chatMode,
			Model:                 modelName,
			ContentType:           contentType,
			StructuredContent:     string(req.StructuredContent),
			Metadata:              metadata,
			MessageIdempotencyKey: messageIdempotencyKey,
			IP:                    c.ClientIP(),
		}
		if err := tx.Create(&message).Error; err != nil {
			if messageIdempotencyKey != nil {
				var existing workagentModel.ChatMessage
				if findErr := tx.Where("thread_id = ? AND uid = ? AND message_idempotency_key = ?", int(thread.Id), int(uid), *messageIdempotencyKey).
					Order("id DESC").
					First(&existing).Error; findErr == nil {
					existingMessage = &existing
					return nil
				}
			}
			return fmt.Errorf("save message: %w", err)
		}

		preview := userText
		if preview == "" {
			preview = aiText
		}
		if preview == "" {
			preview = contentType
		}
		if err := tx.Model(&workagentModel.ChatThread{}).
			Where("id = ?", thread.Id).
			Updates(map[string]interface{}{
				"updated_at":    time.Now(),
				"message_count": gorm.Expr("message_count + ?", 1),
				"msg_preview":   canvas.TruncateString(preview, 200),
			}).Error; err != nil {
			return fmt.Errorf("update thread preview: %w", err)
		}
		return nil
	}); err != nil {
		if strings.Contains(err.Error(), "canvas thread belongs to a different project") {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save message"})
		return
	}
	if existingMessage != nil {
		c.JSON(http.StatusOK, gin.H{
			"thread":  thread.ToResponse(),
			"message": existingMessage.ToResponse(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"thread":  thread.ToResponse(),
		"message": message.ToResponse(),
	})
}

func buildCanvasDirectMessageIdempotencyKey(req canvasAppendMessageRequest, chatMode, contentType string) (string, string) {
	metadata := decodeCanvasDirectMessageJSON(req.Metadata)
	structured := decodeCanvasDirectMessageJSON(req.StructuredContent)
	clientRunID := firstCanvasDirectMessageString(metadata["clientRunId"], structured["clientRunId"])
	if !isSafeCanvasDirectMessageToken(clientRunID) {
		return "", string(req.Metadata)
	}

	stage := firstCanvasDirectMessageString(structured["status"], metadata["status"])
	if stage == "" {
		stage = firstCanvasDirectMessageString(structured["operation"], metadata["operation"])
	}
	if stage == "" {
		if strings.TrimSpace(req.UserText) != "" {
			stage = "user"
		} else {
			stage = "assistant"
		}
	}
	role := "assistant"
	if strings.TrimSpace(req.UserText) != "" {
		role = "user"
	}
	key := strings.Join([]string{
		clientRunID,
		cleanCanvasDirectMessageKeyPart(chatMode),
		cleanCanvasDirectMessageKeyPart(contentType),
		cleanCanvasDirectMessageKeyPart(stage),
		role,
	}, ":")
	if !isSafeCanvasDirectMessageToken(key) {
		return "", string(req.Metadata)
	}
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	metadata["clientRunId"] = clientRunID
	metadata["messageIdempotencyKey"] = key
	encoded, err := json.Marshal(metadata)
	if err != nil || len(encoded) > 64*1024 {
		return "", string(req.Metadata)
	}
	return key, string(encoded)
}

func decodeCanvasDirectMessageJSON(raw json.RawMessage) map[string]interface{} {
	if len(raw) == 0 {
		return nil
	}
	var out map[string]interface{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func firstCanvasDirectMessageString(values ...interface{}) string {
	for _, value := range values {
		if text, ok := value.(string); ok {
			trimmed := strings.TrimSpace(text)
			if trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func isSafeCanvasDirectMessageToken(value string) bool {
	if value == "" || len(value) > 180 {
		return false
	}
	return !strings.ContainsFunc(value, func(r rune) bool {
		return r < 32 || r == 127 || r == '%' || r == '_' || r == '\\'
	})
}

func cleanCanvasDirectMessageKeyPart(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "-"
	}
	replacer := strings.NewReplacer("%", "-", "_", "-", "\\", "-", ":", "-")
	return replacer.Replace(trimmed)
}
