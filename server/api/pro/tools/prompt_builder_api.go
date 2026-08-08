package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"server/globals"
	"server/model"
	"server/model/common/response"
	"server/service/account"
	llmService "server/service/llm"
	toolsService "server/service/tools"
	"server/utils"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errPromptBuilderQuotaExceeded = errors.New("prompt builder quota exceeded")

type PromptBuilderApi struct{}

// writePromptBuilderHTTPError writes an error response matching the shape
// used by sibling tools (upscaler/vectorizer/remover): {code, message, [data]}.
// The duplicated `error` field that used to mirror `message` was dropped —
// clients read `message` off the envelope via ensureSuccess, so no caller
// depended on it.
func writePromptBuilderHTTPError(c *gin.Context, status int, message string, data interface{}) {
	payload := gin.H{
		"code":    status,
		"message": message,
	}
	if data != nil {
		payload["data"] = data
	}
	c.JSON(status, payload)
}

func bindPromptBuilderJSON(c *gin.Context, req interface{}) error {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxPromptBuilderJSONBodyBytes)
	return c.ShouldBindJSON(req)
}

func normalizePromptBuilderRequestText(c *gin.Context, value string, field string, maxBytes int, required bool) (string, bool) {
	text := strings.TrimSpace(value)
	if required && text == "" {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, field+" is required", nil)
		return "", false
	}
	if len(text) > maxBytes {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, field+" is too long", nil)
		return "", false
	}
	return text, true
}

// reservePromptBuilderCredits reserves creditCost for a prompt-builder tool
// action. Returns the reservation ID on success. On insufficient credits or a
// terminal-retry, writes the 402 response and returns ok=false so the caller
// can abort. The idempotency key is scoped by tool so a client-supplied
// X-Idempotency-Key cannot reuse a reservation across different tool
// actions (e.g. translate-1cr reused to satisfy enhance-2cr). Accepts
// `Idempotency-Key` as a fallback to match sibling tools.
func reservePromptBuilderCredits(c *gin.Context, uid int, tool string, creditCost int) (uint, bool) {
	db := globals.GraDBs["system"]
	svc := account.NewCreditReservationService()
	clientKey := normalizeIdempotencyKey(c.GetHeader("X-Idempotency-Key"))
	if clientKey == "" {
		clientKey = normalizeIdempotencyKey(c.GetHeader("Idempotency-Key"))
	}
	if clientKey == "" {
		clientKey = fmt.Sprintf("auto_%d_%d", uid, time.Now().UnixNano())
	}
	key := fmt.Sprintf("%s:%s", tool, clientKey)
	var reservationID uint
	err := db.Transaction(func(tx *gorm.DB) error {
		res, err := svc.Reserve(tx, account.ReservationRequest{
			UID:            uid,
			Tool:           tool,
			IdempotencyKey: key,
			Reserved:       creditCost,
			Remark:         tool,
		})
		if err != nil {
			return err
		}
		if !res.Created {
			if res.Reservation.IsTerminal() {
				return account.ErrReservationAlreadyProcessed
			}
			return account.ErrReservationInProgress
		}
		reservationID = res.Reservation.Id
		return nil
	})
	if err != nil {
		if errors.Is(err, account.ErrInsufficientCredits) {
			writePromptBuilderHTTPError(c, http.StatusPaymentRequired, "Insufficient credits", map[string]interface{}{"creditsRequired": creditCost})
			return 0, false
		}
		if errors.Is(err, account.ErrReservationInProgress) {
			writePromptBuilderHTTPError(c, http.StatusConflict, "Request already in progress", map[string]interface{}{
				"errorCode": "RESERVATION_IN_PROGRESS", "retryable": true,
			})
			return 0, false
		}
		if errors.Is(err, account.ErrReservationAlreadyProcessed) || errors.Is(err, account.ErrReservationReplayConflict) {
			writePromptBuilderHTTPError(c, http.StatusConflict, "Idempotency key already used", map[string]interface{}{
				"errorCode": "RESERVATION_REPLAY_CONFLICT", "retryable": false,
			})
			return 0, false
		}
		globals.Error(fmt.Sprintf("[PromptBuilder] Reserve failed for user %d (cost %d): %v", uid, creditCost, err))
		writePromptBuilderHTTPError(c, http.StatusInternalServerError, "Server error", nil)
		return 0, false
	}
	return reservationID, true
}

func releasePromptBuilderReservation(reservationID uint) {
	if reservationID == 0 {
		return
	}
	err := globals.GraDBs["system"].Transaction(func(tx *gorm.DB) error {
		return account.NewCreditReservationService().Release(tx, reservationID)
	})
	if err != nil {
		globals.Error(fmt.Sprintf("[PromptBuilder] Failed to release reservation %d: %v", reservationID, err))
	}
}

func finalizePromptBuilderReservation(reservationID uint, used int) {
	if reservationID == 0 {
		return
	}
	err := globals.GraDBs["system"].Transaction(func(tx *gorm.DB) error {
		return account.NewCreditReservationService().Finalize(tx, reservationID, used)
	})
	if err != nil {
		globals.Error(fmt.Sprintf("[PromptBuilder] Failed to finalize reservation %d: %v", reservationID, err))
	}
}

// Per-user cap on concurrent SSE streams. A single user opening many tabs or
// scripting the endpoint can otherwise pin multiple goroutines + upstream LLM
// connections and burn credits in parallel. In-memory is fine: the limit is a
// per-pod soft ceiling, and a pod restart simply clears the counters.
const maxPromptBuilderSSEStreamsPerUser = 2

var (
	promptBuilderSSEStreams   = make(map[uint]int)
	promptBuilderSSEStreamsMu sync.Mutex
)

// acquirePromptBuilderSSESlot returns false if the user already has
// maxPromptBuilderSSEStreamsPerUser streams in flight; caller should 429.
func acquirePromptBuilderSSESlot(uid uint) bool {
	promptBuilderSSEStreamsMu.Lock()
	defer promptBuilderSSEStreamsMu.Unlock()
	if promptBuilderSSEStreams[uid] >= maxPromptBuilderSSEStreamsPerUser {
		return false
	}
	promptBuilderSSEStreams[uid]++
	return true
}

func releasePromptBuilderSSESlot(uid uint) {
	promptBuilderSSEStreamsMu.Lock()
	defer promptBuilderSSEStreamsMu.Unlock()
	if promptBuilderSSEStreams[uid] <= 1 {
		delete(promptBuilderSSEStreams, uid)
		return
	}
	promptBuilderSSEStreams[uid]--
}

// --- CRUD Endpoints ---
//
// Request/response DTOs and allowed-values tables live in
// prompt_builder_schema.go. This file keeps HTTP helpers and handlers.

// sanitizePromptBuilderMetaHint strips newlines and caps length. Used for
// short hint strings that get inlined into LLM user messages outside of
// delimited tags (e.g. "Mode: ..."). Prevents trivial prompt-injection via
// newline-inserted fake headers.
func sanitizePromptBuilderMetaHint(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	replacer := strings.NewReplacer("\n", " ", "\r", " ", "<", "", ">", "")
	s = replacer.Replace(s)
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

// isSafeHTTPURL returns true only for http(s) URLs with a host. Rejects
// data:, javascript:, blob:, file:, relative URLs, and anything that could
// yield stored-XSS or exfiltration when rendered in <img src>.
func isSafeHTTPURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return false
	}
	if parsed.Host == "" {
		return false
	}
	return true
}

// Allowed-values tables (promptBuilderAllowedModes, promptBuilderAllowedBlockTypes,
// promptBuilderBlockFields, promptBuilderFieldKinds, promptBuilderFieldAliases,
// promptBuilderCharacterArrayFields), DTO types (promptBuilderConfigSummary,
// PublicConfigResponse, request structs), and pagination constants all live in
// prompt_builder_schema.go.

func (a *PromptBuilderApi) ListConfigs(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		writePromptBuilderHTTPError(c, http.StatusUnauthorized, "Unauthorized", nil)
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", strconv.Itoa(promptBuilderListDefaultPageSize)))
	if pageSize < 1 || pageSize > promptBuilderListMaxPageSize {
		pageSize = promptBuilderListDefaultPageSize
	}

	db := globals.GraDBs["system"].Model(&model.ImagePromptConfig{}).Where("uid = ?", uid)

	var total int64
	if err := db.Count(&total).Error; err != nil {
		writePromptBuilderHTTPError(c, http.StatusInternalServerError, "Failed to count configs", nil)
		return
	}

	var configs []promptBuilderConfigSummary
	if err := db.
		Select("id, uid, name, mode, thumbnail, is_favorite, created_at, updated_at").
		Order("updated_at DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&configs).Error; err != nil {
		writePromptBuilderHTTPError(c, http.StatusInternalServerError, "Failed to list configs", nil)
		return
	}

	response.OkWithData(map[string]interface{}{
		"list":     configs,
		"total":    total,
		"page":     page,
		"pageSize": pageSize,
	}, c)
}

func (a *PromptBuilderApi) GetConfig(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		writePromptBuilderHTTPError(c, http.StatusUnauthorized, "Unauthorized", nil)
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Invalid config id", nil)
		return
	}

	var config model.ImagePromptConfig
	if err := globals.GraDBs["system"].
		Where("id = ? AND uid = ?", id, uid).
		First(&config).Error; err != nil {
		writePromptBuilderHTTPError(c, http.StatusNotFound, "Config not found", nil)
		return
	}

	response.OkWithData(config, c)
}

func (a *PromptBuilderApi) SaveConfig(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		writePromptBuilderHTTPError(c, http.StatusUnauthorized, "Unauthorized", nil)
		return
	}

	var req saveConfigRequest
	if err := bindPromptBuilderJSON(c, &req); err != nil {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Invalid request: "+err.Error(), nil)
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Config name is required", nil)
		return
	}
	if len(name) > maxPromptBuilderNameLength {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Config name is too long", nil)
		return
	}
	thumbnail := ""
	if req.Thumbnail != nil {
		thumbnail = strings.TrimSpace(*req.Thumbnail)
	}
	if len(thumbnail) > maxPromptBuilderThumbnailLength {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Thumbnail is too long", nil)
		return
	}
	if thumbnail != "" && !isSafeHTTPURL(thumbnail) {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Thumbnail must be an http(s) URL", nil)
		return
	}

	// Validate mode
	req.Mode = strings.TrimSpace(req.Mode)
	if !isPromptBuilderMode(req.Mode) {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Invalid mode", nil)
		return
	}

	normalizedBlocksJSON, err := validateAndNormalizePromptBuilderBlocksJSON(req.BlocksJSON)
	if err != nil {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Invalid blocks JSON: "+err.Error(), nil)
		return
	}

	config := model.ImagePromptConfig{
		UID:        uid,
		Name:       name,
		Mode:       req.Mode,
		BlocksJSON: normalizedBlocksJSON,
		Thumbnail:  thumbnail,
		IsFavorite: req.IsFavorite != nil && *req.IsFavorite,
	}

	// Quota check + insert in one transaction with a next-key lock on the
	// `uid = ?` range of idx_ip_uid. The lock blocks concurrent inserts for
	// the same uid until this tx commits, so two parallel saves can't both
	// see count == maxConfigsPerUser-1 and both insert to reach maxConfigsPerUser+1.
	err = globals.GraDBs["system"].Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&model.ImagePromptConfig{}).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("uid = ?", uid).
			Count(&count).Error; err != nil {
			return err
		}
		if count >= maxConfigsPerUser {
			return errPromptBuilderQuotaExceeded
		}
		return tx.Create(&config).Error
	})
	if err != nil {
		if errors.Is(err, errPromptBuilderQuotaExceeded) {
			writePromptBuilderHTTPError(c, http.StatusTooManyRequests, "Maximum saved configurations reached (50)", nil)
			return
		}
		writePromptBuilderHTTPError(c, http.StatusInternalServerError, "Failed to save config", nil)
		return
	}

	response.OkWithData(config, c)
}

func (a *PromptBuilderApi) UpdateConfig(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		writePromptBuilderHTTPError(c, http.StatusUnauthorized, "Unauthorized", nil)
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Invalid config id", nil)
		return
	}

	var req saveConfigRequest
	if err := bindPromptBuilderJSON(c, &req); err != nil {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Invalid request: "+err.Error(), nil)
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Config name is required", nil)
		return
	}
	if len(name) > maxPromptBuilderNameLength {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Config name is too long", nil)
		return
	}
	thumbnail := ""
	if req.Thumbnail != nil {
		thumbnail = strings.TrimSpace(*req.Thumbnail)
	}
	if len(thumbnail) > maxPromptBuilderThumbnailLength {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Thumbnail is too long", nil)
		return
	}
	if thumbnail != "" && !isSafeHTTPURL(thumbnail) {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Thumbnail must be an http(s) URL", nil)
		return
	}

	req.Mode = strings.TrimSpace(req.Mode)
	if !isPromptBuilderMode(req.Mode) {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Invalid mode", nil)
		return
	}

	normalizedBlocksJSON, err := validateAndNormalizePromptBuilderBlocksJSON(req.BlocksJSON)
	if err != nil {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Invalid blocks JSON: "+err.Error(), nil)
		return
	}

	// Lock → update → re-read in a single transaction so the returned row
	// reflects this request's write, not a concurrent writer's. Without the
	// row lock two racing updates could interleave Update/First and each
	// return the other's values.
	var updated model.ImagePromptConfig
	err = globals.GraDBs["system"].Transaction(func(tx *gorm.DB) error {
		var existing model.ImagePromptConfig
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND uid = ?", id, uid).
			First(&existing).Error; err != nil {
			return err
		}
		updates := map[string]interface{}{
			"name":        name,
			"mode":        req.Mode,
			"blocks_json": normalizedBlocksJSON,
		}
		if req.Thumbnail != nil {
			updates["thumbnail"] = thumbnail
		}
		if req.IsFavorite != nil {
			updates["is_favorite"] = *req.IsFavorite
		}
		if err := tx.Model(&existing).Updates(updates).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).First(&updated).Error
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writePromptBuilderHTTPError(c, http.StatusNotFound, "Config not found", nil)
			return
		}
		writePromptBuilderHTTPError(c, http.StatusInternalServerError, "Failed to update config", nil)
		return
	}
	response.OkWithData(updated, c)
}

func (a *PromptBuilderApi) DeleteConfig(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		writePromptBuilderHTTPError(c, http.StatusUnauthorized, "Unauthorized", nil)
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Invalid config id", nil)
		return
	}

	res := globals.GraDBs["system"].
		Where("id = ? AND uid = ?", id, uid).
		Delete(&model.ImagePromptConfig{})
	if res.Error != nil {
		writePromptBuilderHTTPError(c, http.StatusInternalServerError, "Failed to delete config", nil)
		return
	}
	if res.RowsAffected == 0 {
		writePromptBuilderHTTPError(c, http.StatusNotFound, "Config not found", nil)
		return
	}

	response.Ok(c)
}

// --- AI Endpoints ---
//
// Request bodies (enhanceRequest, translatePromptStreamRequest, parseRequest,
// suggestRequest) live in prompt_builder_schema.go.

func (a *PromptBuilderApi) EnhancePrompt(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		writePromptBuilderHTTPError(c, http.StatusUnauthorized, "Unauthorized", nil)
		return
	}

	var req enhanceRequest
	if err := bindPromptBuilderJSON(c, &req); err != nil {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Invalid request", nil)
		return
	}
	var ok bool
	if req.Prompt, ok = normalizePromptBuilderRequestText(c, req.Prompt, "Prompt", maxPromptBuilderPromptTextBytes, true); !ok {
		return
	}
	if req.Negative, ok = normalizePromptBuilderRequestText(c, req.Negative, "Negative prompt", maxPromptBuilderPromptTextBytes, false); !ok {
		return
	}
	if req.Mode, ok = normalizePromptBuilderRequestText(c, req.Mode, "Mode", maxPromptBuilderMetaTextBytes, false); !ok {
		return
	}
	if req.Style, ok = normalizePromptBuilderRequestText(c, req.Style, "Style", maxPromptBuilderMetaTextBytes, false); !ok {
		return
	}

	if !acquirePromptBuilderSSESlot(uid) {
		writePromptBuilderHTTPError(c, http.StatusTooManyRequests, "Too many concurrent streams", nil)
		return
	}
	defer releasePromptBuilderSSESlot(uid)

	creditCost := 2

	reservationID, ok := reservePromptBuilderCredits(c, int(uid), "prompt_builder_enhance", creditCost)
	if !ok {
		return
	}
	db := globals.GraDBs["system"]

	// SSE headers
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		releasePromptBuilderReservation(reservationID)
		writePromptBuilderHTTPError(c, http.StatusInternalServerError, "Streaming not supported", nil)
		return
	}

	writeEvent := func(payload interface{}) error {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if _, err = c.Writer.Write([]byte("data: ")); err != nil {
			return err
		}
		if _, err = c.Writer.Write(b); err != nil {
			return err
		}
		if _, err = c.Writer.Write([]byte("\n\n")); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
	defer cancel()

	// Build user message. User-controlled text is wrapped in a delimited tag
	// so the system prompt can treat the contents as data rather than
	// instructions (mitigates prompt-injection like "ignore previous…").
	mode := sanitizePromptBuilderMetaHint(req.Mode)
	style := sanitizePromptBuilderMetaHint(req.Style)
	userMsg := fmt.Sprintf("Mode: %s\nStyle hint: %s\n<user_prompt>\n%s\n</user_prompt>", mode, style, req.Prompt)

	llmReq := llmService.UniversalLLMRequest{
		SystemPrompt: toolsService.PromptBuilderEnhanceSystemPrompt,
		UserMessage:  userMsg,
		Temperature:  0.7,
		MaxTokens:    1500,
		UserID:       uid,
		Service:      "prompt_builder_enhance",
		RequestID:    fmt.Sprintf("pb_enhance_%d_%d", uid, time.Now().UnixNano()),
	}

	llmSvc := llmService.GetService()
	var fullText strings.Builder

	err := llmSvc.ProcessStreamingRequest(ctx, llmReq, func(chunk string) error {
		if chunk == "" {
			return nil
		}
		fullText.WriteString(chunk)
		return writeEvent(gin.H{"type": "text", "content": chunk})
	})

	switch {
	case err == nil:
		if err := writeEvent(gin.H{"type": "done", "content": fullText.String()}); err != nil {
			globals.Error(fmt.Sprintf("[PromptBuilder] Enhance client write failed uid=%d: %v", uid, err))
			releasePromptBuilderReservation(reservationID)
			return
		}
		finalizePromptBuilderReservation(reservationID, creditCost)
		_ = db.Transaction(func(tx *gorm.DB) error {
			return toolsService.CreateUsageRecordTx(tx, int(uid), model.TOOL_PROMPT_BUILDER, 0, creditCost, model.STATUS_SUCCESS, 0, &toolsService.UsageRecordMeta{
				ToolParams: map[string]interface{}{"action": "enhance", "mode": req.Mode},
			})
		})
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// Client disconnected or stream timed out — don't charge for incomplete output.
		globals.Error(fmt.Sprintf("[PromptBuilder] Enhance canceled/timeout uid=%d: %v", uid, err))
		releasePromptBuilderReservation(reservationID)
	default:
		globals.Error(fmt.Sprintf("[PromptBuilder] Enhance failed uid=%d: %v", uid, err))
		_ = writeEvent(gin.H{"type": "error", "error": "Enhance failed"})
		releasePromptBuilderReservation(reservationID)
	}
}

// TranslateStream provides a standalone streaming prompt translation endpoint.
func (a *PromptBuilderApi) TranslateStream(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		writePromptBuilderHTTPError(c, http.StatusUnauthorized, "Unauthorized", nil)
		return
	}

	var req translatePromptStreamRequest
	if err := bindPromptBuilderJSON(c, &req); err != nil {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Invalid request: "+err.Error(), nil)
		return
	}
	var ok bool
	if req.Prompt, ok = normalizePromptBuilderRequestText(c, req.Prompt, "Prompt", maxPromptBuilderPromptTextBytes, true); !ok {
		return
	}
	if req.TargetLanguage, ok = normalizePromptBuilderRequestText(c, req.TargetLanguage, "Target language", maxPromptBuilderMetaTextBytes, false); !ok {
		return
	}

	if !acquirePromptBuilderSSESlot(uid) {
		writePromptBuilderHTTPError(c, http.StatusTooManyRequests, "Too many concurrent streams", nil)
		return
	}
	defer releasePromptBuilderSSESlot(uid)

	const creditCost = 1
	db := globals.GraDBs["system"]
	reservationID, ok := reservePromptBuilderCredits(c, int(uid), "prompt_builder_translate", creditCost)
	if !ok {
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		releasePromptBuilderReservation(reservationID)
		writePromptBuilderHTTPError(c, http.StatusInternalServerError, "Streaming not supported", nil)
		return
	}

	writeEvent := func(payload interface{}) error {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if _, err = c.Writer.Write([]byte("data: ")); err != nil {
			return err
		}
		if _, err = c.Writer.Write(b); err != nil {
			return err
		}
		if _, err = c.Writer.Write([]byte("\n\n")); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	start := time.Now()
	finalText, err := a.translatePromptStreamWithLLM(
		c.Request.Context(),
		req.Prompt,
		req.TargetLanguage,
		uid,
		func(chunk string) error {
			return writeEvent(gin.H{"type": "text", "content": chunk})
		},
	)
	if err != nil {
		globals.Error(fmt.Sprintf("[PromptBuilder] TranslateStream failed uid=%d: %v", uid, err))
		releasePromptBuilderReservation(reservationID)
		_ = writeEvent(gin.H{"type": "error", "error": "Translate failed"})
		_ = writeEvent(gin.H{"type": "done"})
		return
	}

	fixedText := preservePromptLineLayout(req.Prompt, finalText)
	if strings.TrimSpace(fixedText) == "" {
		fixedText = req.Prompt
	}
	if fixedText != finalText {
		_ = writeEvent(gin.H{"type": "replace", "content": fixedText})
	}

	finalizePromptBuilderReservation(reservationID, creditCost)
	meta := &toolsService.UsageRecordMeta{
		IP:         utils.GetClientIP(c.Request),
		DeviceInfo: c.GetHeader("User-Agent"),
		ToolParams: map[string]interface{}{
			"action":         "translate_stream",
			"promptLength":   len(req.Prompt),
			"targetLanguage": req.TargetLanguage,
		},
	}
	_ = toolsService.CreateUsageRecordTx(db, int(uid), model.TOOL_PROMPT_BUILDER, 0, creditCost, model.STATUS_SUCCESS, int(time.Since(start).Seconds()), meta)
	_ = writeEvent(gin.H{"type": "done"})
}

func (a *PromptBuilderApi) ParsePrompt(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		writePromptBuilderHTTPError(c, http.StatusUnauthorized, "Unauthorized", nil)
		return
	}

	var req parseRequest
	if err := bindPromptBuilderJSON(c, &req); err != nil {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Invalid request: "+err.Error(), nil)
		return
	}
	var ok bool
	if req.Text, ok = normalizePromptBuilderRequestText(c, req.Text, "Text", maxPromptBuilderPromptTextBytes, true); !ok {
		return
	}

	creditCost := 2

	reservationID, ok := reservePromptBuilderCredits(c, int(uid), "prompt_builder_parse", creditCost)
	if !ok {
		return
	}
	db := globals.GraDBs["system"]

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	llmReq := llmService.UniversalLLMRequest{
		SystemPrompt: toolsService.PromptBuilderParseSystemPrompt,
		UserMessage:  req.Text,
		Temperature:  0.3,
		MaxTokens:    2000,
		JSONMode:     true,
		UserID:       uid,
		Service:      "prompt_builder_parse",
		RequestID:    fmt.Sprintf("pb_parse_%d_%d", uid, time.Now().UnixNano()),
	}

	llmSvc := llmService.GetService()
	resp, err := llmSvc.ProcessRequest(ctx, llmReq)
	if err != nil || !resp.Success {
		errMsg := "Parse failed"
		if err != nil {
			errMsg = err.Error()
		} else if resp.Error != "" {
			errMsg = resp.Error
		}
		globals.Error(fmt.Sprintf("[PromptBuilder] Parse failed: %s", errMsg))
		releasePromptBuilderReservation(reservationID)
		writePromptBuilderHTTPError(c, http.StatusBadGateway, "Failed to parse prompt", nil)
		return
	}

	parsed, err := parseLLMJSONContent(resp.Content)
	if err != nil {
		releasePromptBuilderReservation(reservationID)
		writePromptBuilderHTTPError(c, http.StatusBadGateway, "Failed to parse AI response", nil)
		return
	}

	sanitized, err := sanitizePromptBuilderParsedResult(parsed)
	if err != nil {
		releasePromptBuilderReservation(reservationID)
		writePromptBuilderHTTPError(c, http.StatusBadGateway, "Failed to parse AI response", nil)
		return
	}

	finalizePromptBuilderReservation(reservationID, creditCost)
	_ = db.Transaction(func(tx *gorm.DB) error {
		return toolsService.CreateUsageRecordTx(tx, int(uid), model.TOOL_PROMPT_BUILDER, 0, creditCost, model.STATUS_SUCCESS, 0, &toolsService.UsageRecordMeta{
			ToolParams: map[string]interface{}{"action": "parse"},
		})
	})

	response.OkWithData(sanitized, c)
}

func (a *PromptBuilderApi) ParsePromptImage(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		writePromptBuilderHTTPError(c, http.StatusUnauthorized, "Unauthorized", nil)
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxPromptBuilderImageSize+maxPromptBuilderMultipartOverheadBytes)

	fileHeader, err := c.FormFile("file")
	if err != nil {
		fileHeader, err = c.FormFile("image")
		if err != nil {
			writePromptBuilderHTTPError(c, http.StatusBadRequest, "Missing image file", nil)
			return
		}
	}

	if fileHeader.Size <= 0 || fileHeader.Size > maxPromptBuilderImageSize {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Invalid image size", nil)
		return
	}

	file, err := fileHeader.Open()
	if err != nil {
		writePromptBuilderHTTPError(c, http.StatusInternalServerError, "Failed to open image", nil)
		return
	}
	defer file.Close()

	imageBytes, err := io.ReadAll(io.LimitReader(file, maxPromptBuilderImageSize+1))
	if err != nil {
		writePromptBuilderHTTPError(c, http.StatusInternalServerError, "Failed to read image", nil)
		return
	}
	if int64(len(imageBytes)) == 0 || int64(len(imageBytes)) > maxPromptBuilderImageSize {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Invalid image size", nil)
		return
	}

	// Always validate against magic bytes. A client-supplied Content-Type
	// alone can be forged (e.g. a text file labeled image/png). If both the
	// declared header and the detected type are valid but disagree, we trust
	// the detected type to avoid passing unexpected content to the vision model.
	detected := http.DetectContentType(imageBytes)
	headerType := strings.ToLower(strings.TrimSpace(fileHeader.Header.Get("Content-Type")))
	if idx := strings.Index(headerType, ";"); idx >= 0 {
		headerType = strings.TrimSpace(headerType[:idx])
	}
	if _, ok := promptBuilderAllowedImageTypes[detected]; !ok {
		writePromptBuilderHTTPError(c, http.StatusUnsupportedMediaType, "Unsupported image type", nil)
		return
	}
	if headerType != "" && headerType != detected {
		if _, ok := promptBuilderAllowedImageTypes[headerType]; !ok || headerType != detected {
			writePromptBuilderHTTPError(c, http.StatusUnsupportedMediaType, "Image content does not match declared type", nil)
			return
		}
	}
	contentType := detected

	creditCost := 2
	reservationID, ok := reservePromptBuilderCredits(c, int(uid), "prompt_builder_parse_image", creditCost)
	if !ok {
		return
	}
	db := globals.GraDBs["system"]

	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
	defer cancel()

	resp, err := llmService.ProcessImageAnalysisRequest(ctx, llmService.ImageAnalysisRequest{
		SystemPrompt: toolsService.PromptBuilderImageParseSystemPrompt,
		UserMessage:  "Analyze this image and return prompt-builder blocks JSON.",
		MimeType:     contentType,
		ImageBytes:   imageBytes,
		Temperature:  0.2,
		MaxTokens:    2000,
		JSONMode:     true,
		Service:      "prompt_builder_parse_image",
		RequestID:    fmt.Sprintf("pb_parse_image_%d_%d", uid, time.Now().UnixNano()),
	})
	if err != nil || resp == nil || !resp.Success {
		errMsg := "image analysis client not available"
		if err != nil {
			errMsg = err.Error()
		} else if resp != nil && resp.Error != "" {
			errMsg = resp.Error
		}
		globals.Error(fmt.Sprintf("[PromptBuilder] Parse image failed: %s", errMsg))
		releasePromptBuilderReservation(reservationID)
		writePromptBuilderHTTPError(c, http.StatusBadGateway, "Failed to parse image prompt", nil)
		return
	}

	parsed, err := parseLLMJSONContent(resp.Content)
	if err != nil {
		releasePromptBuilderReservation(reservationID)
		writePromptBuilderHTTPError(c, http.StatusBadGateway, "Failed to parse AI response", nil)
		return
	}

	sanitized, err := sanitizePromptBuilderParsedResult(parsed)
	if err != nil {
		releasePromptBuilderReservation(reservationID)
		writePromptBuilderHTTPError(c, http.StatusBadGateway, "Failed to parse AI response", nil)
		return
	}

	finalizePromptBuilderReservation(reservationID, creditCost)
	_ = db.Transaction(func(tx *gorm.DB) error {
		return toolsService.CreateUsageRecordTx(tx, int(uid), model.TOOL_PROMPT_BUILDER, 0, creditCost, model.STATUS_SUCCESS, 0, &toolsService.UsageRecordMeta{
			ToolParams: map[string]interface{}{
				"action": "parse_image",
				"model":  llmService.GetImageAnalysisModel(),
			},
		})
	})

	response.OkWithData(sanitized, c)
}

func (a *PromptBuilderApi) SuggestTags(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		writePromptBuilderHTTPError(c, http.StatusUnauthorized, "Unauthorized", nil)
		return
	}

	var req suggestRequest
	if err := bindPromptBuilderJSON(c, &req); err != nil {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Invalid request: "+err.Error(), nil)
		return
	}
	req.BlockType = strings.TrimSpace(req.BlockType)
	req.Field = strings.TrimSpace(req.Field)
	if _, ok := promptBuilderAllowedBlockTypes[req.BlockType]; !ok {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Invalid block type", nil)
		return
	}
	if !isPromptBuilderFieldAllowed(req.BlockType, req.Field) {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Invalid field", nil)
		return
	}
	if len(req.Existing) > 0 {
		normalizedExisting := make([]string, 0, len(req.Existing))
		for _, item := range req.Existing {
			if text, ok := normalizePromptBuilderText(item); ok {
				if len(text) > maxPromptBuilderSuggestExistingTextBytes {
					writePromptBuilderHTTPError(c, http.StatusBadRequest, "Existing tags are too long", nil)
					return
				}
				normalizedExisting = append(normalizedExisting, text)
			}
			if len(normalizedExisting) >= 50 {
				break
			}
		}
		req.Existing = normalizedExisting
	}
	if len(req.AllBlocksSummary) > maxPromptBuilderAllBlocksSummaryBytes {
		req.AllBlocksSummary = req.AllBlocksSummary[:maxPromptBuilderAllBlocksSummaryBytes]
	}
	sanitizedContext := sanitizePromptBuilderBlockData(req.BlockType, req.Context)
	contextJSON, err := json.Marshal(sanitizedContext)
	if err != nil || len(contextJSON) > maxPromptBuilderSuggestContextJSONBytes {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Context is too large", nil)
		return
	}

	creditCost := 1

	reservationID, ok := reservePromptBuilderCredits(c, int(uid), "prompt_builder_suggest", creditCost)
	if !ok {
		return
	}
	db := globals.GraDBs["system"]

	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()

	// Build user message. User-controlled text is wrapped in delimited tags
	// so the system prompt can treat the contents as data rather than
	// instructions. Length caps apply before injection into the prompt.
	existingJSON, _ := json.Marshal(req.Existing)
	crossBlockCtx := ""
	if summary := strings.TrimSpace(req.AllBlocksSummary); summary != "" {
		crossBlockCtx = fmt.Sprintf("\n<other_blocks_summary>\n%s\n</other_blocks_summary>", summary)
	}
	userMsg := fmt.Sprintf(
		"Block type: %s\nField: %s\n<current_block_data>\n%s\n</current_block_data>\n<already_selected>\n%s\n</already_selected>%s\nSuggest complementary tags that harmonize with the full prompt context.",
		req.BlockType, req.Field, string(contextJSON), string(existingJSON), crossBlockCtx,
	)

	llmReq := llmService.UniversalLLMRequest{
		SystemPrompt: toolsService.PromptBuilderSuggestSystemPrompt,
		UserMessage:  userMsg,
		Temperature:  0.8,
		MaxTokens:    500,
		JSONMode:     true,
		UserID:       uid,
		Service:      "prompt_builder_suggest",
		RequestID:    fmt.Sprintf("pb_suggest_%d_%d", uid, time.Now().UnixNano()),
	}

	llmSvc := llmService.GetService()
	resp, err := llmSvc.ProcessRequest(ctx, llmReq)
	if err != nil || !resp.Success {
		releasePromptBuilderReservation(reservationID)
		writePromptBuilderHTTPError(c, http.StatusBadGateway, "Failed to generate suggestions", nil)
		return
	}

	parsed, err := parseLLMJSONContent(resp.Content)
	if err != nil {
		releasePromptBuilderReservation(reservationID)
		writePromptBuilderHTTPError(c, http.StatusBadGateway, "Failed to parse suggestions", nil)
		return
	}
	suggestions := normalizePromptBuilderStringArray(parsed["suggestions"])
	parsed = map[string]interface{}{
		"suggestions": suggestions,
	}

	finalizePromptBuilderReservation(reservationID, creditCost)
	_ = db.Transaction(func(tx *gorm.DB) error {
		return toolsService.CreateUsageRecordTx(tx, int(uid), model.TOOL_PROMPT_BUILDER, 0, creditCost, model.STATUS_SUCCESS, 0, &toolsService.UsageRecordMeta{
			ToolParams: map[string]interface{}{"action": "suggest", "blockType": req.BlockType, "field": req.Field},
		})
	})

	response.OkWithData(parsed, c)
}

func (a *PromptBuilderApi) translatePromptStreamWithLLM(
	ctx context.Context,
	prompt string,
	targetLanguage string,
	uid uint,
	onChunk func(chunk string) error,
) (string, error) {
	target := resolveTargetLanguage(targetLanguage)

	systemPrompt := `You are a professional translator specializing in AI image prompts.

Rules:
1. Translate only to the requested target language.
2. Preserve meaning and structure exactly.
3. Preserve line breaks, paragraph boundaries, list markers, punctuation style, and spacing layout.
4. Output only translated text. Do not output JSON, markdown fences, explanations, or notes.`

	userMessage := fmt.Sprintf(`Target language: %s

Translate the following source prompt to the target language while preserving original formatting exactly.

<source_prompt>
%s
</source_prompt>`, target, prompt)

	llmReq := llmService.UniversalLLMRequest{
		SystemPrompt: systemPrompt,
		UserMessage:  userMessage,
		Temperature:  0.2,
		MaxTokens:    2000,
		JSONMode:     false,
		UserID:       uid,
		Service:      "prompt_builder_translate_stream",
		RequestID:    fmt.Sprintf("pb_translate_stream_%d_%d", uid, time.Now().UnixNano()),
	}

	llmSvc := llmService.GetService()
	var builder strings.Builder
	err := llmSvc.ProcessStreamingRequest(ctx, llmReq, func(chunk string) error {
		if chunk == "" {
			return nil
		}
		builder.WriteString(chunk)
		if onChunk != nil {
			return onChunk(chunk)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("LLM streaming request failed: %w", err)
	}

	result := strings.TrimSpace(builder.String())
	if result == "" {
		return "", fmt.Errorf("empty translation response")
	}
	return result, nil
}

func preservePromptLineLayout(source, translated string) string {
	source = strings.ReplaceAll(source, "\r\n", "\n")
	translated = strings.ReplaceAll(translated, "\r\n", "\n")

	sourceLines := strings.Split(source, "\n")
	if len(sourceLines) <= 1 {
		return strings.TrimSpace(translated)
	}

	translatedLines := strings.Split(translated, "\n")
	if len(translatedLines) == len(sourceLines) {
		return strings.TrimSpace(translated)
	}

	flat := strings.Join(strings.Fields(strings.ReplaceAll(translated, "\n", " ")), " ")
	if flat == "" {
		return source
	}

	nonEmptyIndexes := make([]int, 0, len(sourceLines))
	weights := make([]int, 0, len(sourceLines))
	totalWeight := 0
	for idx, line := range sourceLines {
		w := len([]rune(strings.TrimSpace(line)))
		if w == 0 {
			continue
		}
		nonEmptyIndexes = append(nonEmptyIndexes, idx)
		weights = append(weights, w)
		totalWeight += w
	}
	if len(nonEmptyIndexes) == 0 {
		return source
	}

	runes := []rune(flat)
	if len(runes) == 0 {
		return source
	}

	segments := make([]string, len(nonEmptyIndexes))
	consumedWeight := 0
	lastPos := 0
	for i := range nonEmptyIndexes {
		if i == len(nonEmptyIndexes)-1 {
			segments[i] = strings.TrimSpace(string(runes[lastPos:]))
			break
		}

		consumedWeight += weights[i]
		targetPos := consumedWeight * len(runes) / totalWeight
		remainingSegments := len(nonEmptyIndexes) - i - 1
		maxPos := len(runes) - remainingSegments
		if targetPos <= lastPos {
			targetPos = lastPos + 1
		}
		if targetPos > maxPos {
			targetPos = maxPos
		}
		segments[i] = strings.TrimSpace(string(runes[lastPos:targetPos]))
		lastPos = targetPos
	}

	outLines := make([]string, len(sourceLines))
	for i, idx := range nonEmptyIndexes {
		outLines[idx] = segments[i]
	}

	return strings.Join(outLines, "\n")
}

func resolveTargetLanguage(targetLanguage string) string {
	target := strings.TrimSpace(strings.ToLower(targetLanguage))
	switch target {
	case "", "en", "en-us", "en-gb", "english":
		return "English"
	case "zh", "zh-cn", "zh-hans", "zh-tw", "zh-hk", "chinese":
		return "Chinese"
	case "ja", "japanese":
		return "Japanese"
	case "ko", "korean":
		return "Korean"
	case "fr", "french":
		return "French"
	case "de", "german":
		return "German"
	case "es", "spanish":
		return "Spanish"
	case "pt", "portuguese":
		return "Portuguese"
	case "ru", "russian":
		return "Russian"
	case "it", "italian":
		return "Italian"
	case "nl", "dutch":
		return "Dutch"
	default:
		if target == "" {
			return "English"
		}
		return strings.ToUpper(targetLanguage)
	}
}
