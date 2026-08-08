package tools

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"server/constants"
	"server/globals"
	"server/model"
	toolsService "server/service/tools"
	"server/utils"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type LoraApi struct{}

// loraFallbackCredits is the last-resort credit cost used only when the
// pricing registry returns <=0 for a LoRA preset. Keep in sync with the
// baseline cost in credit_cost.go (TOOL_LORA_STUDIO).
const loraFallbackCredits = 4

const (
	maxLoraGenerateRequestBodyBytes = 1 << 20
	maxLoraPromptRunes              = 5000
	maxLoraNegativePromptRunes      = 5000
	maxLoraParamsJSONBytes          = 64 << 10
)

// loraError thinly wraps the shared writeToolError so existing call sites
// keep working. Behaves identically to upscaler/avatars/remover/vectorizer
// error envelopes.
func loraError(c *gin.Context, statusCode int, code int, message string, errorCode string) {
	writeToolError(c, statusCode, code, message, errorCode)
}

// LoraGenerateRequest Lora生成请求
type LoraGenerateRequest struct {
	Slug            string                 `json:"slug" binding:"required"`
	Prompt          string                 `json:"prompt"`
	ReferenceImages []ReferenceImageInput  `json:"referenceImages"`
	AspectRatio     string                 `json:"aspectRatio"`
	NegativePrompt  string                 `json:"negativePrompt"`
	Params          map[string]interface{} `json:"params"`
}

type loraCreditQuoteResponse struct {
	Slug    string `json:"slug"`
	Model   string `json:"model"`
	Credits int    `json:"credits"`
}

// loraHistoryRecordDto is the stable GetHistory contract consumed by Desktop
// and keeps internal ORM fields (UID, ToolID, UpdatedAt, CreditsUsed,
// Liked, Downloaded, BatchID, Origin, ...) out of the public payload.
type loraHistoryRecordDto struct {
	ID             uint      `json:"id"`
	CreatedAt      time.Time `json:"createdAt"`
	Model          string    `json:"model"`
	Prompt         string    `json:"prompt"`
	NegativePrompt string    `json:"negativePrompt"`
	AspectRatio    string    `json:"aspectRatio"`
	StylePreset    string    `json:"stylePreset"`
	Params         string    `json:"params"`
	InputFiles     string    `json:"inputFiles"`
	ResultImages   string    `json:"resultImages"`
	ResultMetadata string    `json:"resultMetadata"`
	Status         int8      `json:"status"`
}

func toLoraHistoryRecordDto(record *model.GenerationRecord) loraHistoryRecordDto {
	if record == nil {
		return loraHistoryRecordDto{}
	}
	return loraHistoryRecordDto{
		ID:             record.Id,
		CreatedAt:      record.CreatedAt,
		Model:          record.Model,
		Prompt:         record.Prompt,
		NegativePrompt: record.NegativePrompt,
		AspectRatio:    record.AspectRatio,
		StylePreset:    record.StylePreset,
		Params:         record.Params,
		InputFiles:     record.InputFiles,
		ResultImages:   record.ResultImages,
		ResultMetadata: record.ResultMetadata,
		Status:         record.Status,
	}
}

func buildLoraToolID(slug string) string {
	cleaned := strings.TrimSpace(strings.ToLower(slug))
	cleaned = strings.ReplaceAll(cleaned, ":", "-")
	if cleaned == "" {
		return model.TOOL_LORA_STUDIO
	}
	return fmt.Sprintf("%s:%s", model.TOOL_LORA_STUDIO, cleaned)
}

func loraSlugFromToolID(toolID string) string {
	prefix := model.TOOL_LORA_STUDIO + ":"
	if !strings.HasPrefix(toolID, prefix) {
		return ""
	}
	slug := strings.TrimSpace(strings.TrimPrefix(toolID, prefix))
	if slug == "" {
		return ""
	}
	return slug
}

func isLoraGenerationTask(task *model.GenerationTask) bool {
	if task == nil {
		return false
	}
	return loraSlugFromToolID(task.ToolID) != ""
}

// safeFloatFromJSONNumber extracts a finite float64 from an untyped JSON
// value. Returns (0, false) for non-numbers, NaN, or ±Inf so callers can
// treat unsafe inputs as "absent" without smuggling poisoned values into
// requestData.
func safeFloatFromJSONNumber(raw interface{}) (float64, bool) {
	f, ok := raw.(float64)
	if !ok {
		return 0, false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// parseLoraRequestParams whitelists the small set of generation knobs
// the LoRA surface forwards through req.Params. Top-level fields
// (prompt, negativePrompt, aspectRatio, referenceImages) are handled
// separately on the request struct; every other key in the params map
// is dropped so stray client-side inputs cannot ride into requestData
// or the persisted Params JSON column.
func parseLoraRequestParams(params map[string]interface{}) model.JSONMap {
	result := model.JSONMap{}
	if params == nil {
		return result
	}

	// numberOfImages: read by task_queue when splitting a generation into
	// multiple backend tasks. Clamped to [1, 4] to match the UI contract.
	if f, ok := safeFloatFromJSONNumber(params["numberOfImages"]); ok {
		n := int(f)
		if n < 1 {
			n = 1
		}
		if n > 4 {
			n = 4
		}
		result["numberOfImages"] = n
	}
	// seed: optional determinism hint. Accept any finite integer; downstream
	// providers decide whether to honor it.
	if f, ok := safeFloatFromJSONNumber(params["seed"]); ok {
		result["seed"] = int64(f)
	}

	return result
}

func clampListLimit(raw int, fallback int) int {
	limit := raw
	if limit <= 0 {
		limit = fallback
	}
	if limit > 100 {
		limit = 100
	}
	return limit
}

func mergePromptSegments(parts ...string) string {
	seen := make(map[string]struct{})
	segments := make([]string, 0, 16)

	for _, part := range parts {
		for _, raw := range strings.Split(part, ",") {
			segment := strings.TrimSpace(raw)
			if segment == "" {
				continue
			}
			key := strings.ToLower(segment)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			segments = append(segments, segment)
		}
	}

	return strings.Join(segments, ", ")
}

// Generate 处理Lora工具生成请求
func (a *LoraApi) Generate(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		loraError(c, http.StatusUnauthorized, 401, "Unauthorized", "UNAUTHORIZED")
		return
	}

	var req LoraGenerateRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxLoraGenerateRequestBodyBytes)
	if err := c.ShouldBindJSON(&req); err != nil {
		loraError(c, http.StatusBadRequest, 400, "Invalid request: "+err.Error(), "INVALID_REQUEST")
		return
	}
	if req.Params != nil {
		paramsJSON, err := json.Marshal(req.Params)
		if err != nil || len(paramsJSON) > maxLoraParamsJSONBytes {
			loraError(c, http.StatusBadRequest, 400, "Invalid params", "INVALID_PARAMS")
			return
		}
	}

	slug := strings.TrimSpace(req.Slug)
	defaultPrompt, ok := constants.LoraPrompts[slug]
	if !ok {
		loraError(c, http.StatusBadRequest, 400, "Invalid tool slug", "INVALID_SLUG")
		return
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		prompt = defaultPrompt
	}
	if len([]rune(prompt)) > maxLoraPromptRunes {
		loraError(c, http.StatusBadRequest, 400, "Prompt is too long", "PROMPT_TOO_LONG")
		return
	}
	baseNegativePrompt := strings.TrimSpace(constants.LoraGlobalNegativePrompt)
	defaultNegativePrompt := strings.TrimSpace(constants.LoraNegativePrompts[slug])
	requestNegativePrompt := strings.TrimSpace(req.NegativePrompt)
	finalNegativePrompt := mergePromptSegments(baseNegativePrompt, defaultNegativePrompt, requestNegativePrompt)
	if len([]rune(finalNegativePrompt)) > maxLoraNegativePromptRunes {
		loraError(c, http.StatusBadRequest, 400, "Negative prompt is too long", "NEGATIVE_PROMPT_TOO_LONG")
		return
	}

	referenceImages, err := sanitizeVideoGeneratorReferenceImages(req.ReferenceImages, uid)
	if err != nil {
		loraError(c, http.StatusBadRequest, 400, err.Error(), "INVALID_REFERENCE_IMAGE")
		return
	}
	if len(referenceImages) == 0 {
		loraError(c, http.StatusBadRequest, 400, "At least one reference image is required", "REFERENCE_IMAGE_REQUIRED")
		return
	}

	aspectRatio := strings.TrimSpace(req.AspectRatio)
	if aspectRatio == "" {
		aspectRatio = "1:1"
	}
	if _, ok := allowedAspectRatios[aspectRatio]; !ok {
		loraError(c, http.StatusBadRequest, 400, "Invalid aspectRatio", "INVALID_ASPECT_RATIO")
		return
	}

	// negativePrompt carries the merged string (global + per-slug default +
	// user input) used by the model provider. userNegativePrompt carries
	// only what the user actually typed, so the form can round-trip back
	// without leaking internal defaults on retry / load-from-history.
	// Fallback to nanobanana (gemini-2.5-flash-image) when nanobanana-2
	// (gemini-3.1-flash-image-preview) times out or is overloaded. The task
	// queue walks this list in order on transient failures (see
	// task_queue.go:buildTaskModelCandidates).
	requestData := model.JSONMap{
		"model":              model.NANO_BANANA_2,
		"modelCandidates":    []string{model.NANO_BANANA_2, model.NANO_BANANA},
		"prompt":             prompt,
		"negativePrompt":     finalNegativePrompt,
		"userNegativePrompt": requestNegativePrompt,
		"aspectRatio":        aspectRatio,
		"referenceImages":    referenceImages,
		"loraSlug":           slug,
	}
	for k, v := range parseLoraRequestParams(req.Params) {
		requestData[k] = v
	}

	inputFiles := map[string]interface{}{
		"referenceImages": referenceImages,
	}

	creditCost := toolsService.GetCreditCostByToolID(toolsService.CreditCostParams{
		ToolID: buildLoraToolID(slug),
		Model:  model.NANO_BANANA_2,
		Params: req.Params,
	})
	if creditCost <= 0 {
		creditCost = loraFallbackCredits
	}
	idempotencyKey := normalizeIdempotencyKey(c.GetHeader("X-Idempotency-Key"))
	if idempotencyKey == "" {
		idempotencyKey = normalizeIdempotencyKey(c.GetHeader("Idempotency-Key"))
	}

	(&GeneratorApi{}).submitGenerationTask(c, &submitTaskParams{
		UID:            uid,
		ModelID:        model.NANO_BANANA_2,
		ToolID:         buildLoraToolID(slug),
		CreditCost:     creditCost,
		RequestData:    requestData,
		InputFiles:     inputFiles,
		IdempotencyKey: idempotencyKey,
		Origin:         model.GENERATION_ORIGIN_IMAGE_GEN,
	})
}

func (a *LoraApi) GetCredits(c *gin.Context) {
	slug := strings.TrimSpace(c.Query("slug"))
	if slug == "" {
		loraError(c, http.StatusBadRequest, 400, "Tool slug is required", "INVALID_REQUEST")
		return
	}
	if _, ok := constants.LoraPrompts[slug]; !ok {
		loraError(c, http.StatusBadRequest, 400, "Invalid tool slug", "INVALID_SLUG")
		return
	}

	credits := toolsService.GetCreditCostByToolID(toolsService.CreditCostParams{
		ToolID: buildLoraToolID(slug),
		Model:  model.NANO_BANANA_2,
	})
	if credits <= 0 {
		credits = loraFallbackCredits
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data": loraCreditQuoteResponse{
			Slug:    slug,
			Model:   model.NANO_BANANA_2,
			Credits: credits,
		},
	})
}

func (a *LoraApi) GetTasks(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		loraError(c, http.StatusUnauthorized, 401, "Unauthorized", "UNAUTHORIZED")
		return
	}

	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	limit = clampListLimit(limit, 20)
	slug := strings.TrimSpace(c.Query("slug"))

	var tasks []model.GenerationTask
	var err error
	if slug != "" {
		tasks, err = toolsService.GetRecentTasksByUIDAndTool(int(uid), []string{buildLoraToolID(slug)}, limit)
	} else {
		tasks, err = toolsService.GetRecentTasksByUIDAndToolPrefix(int(uid), model.TOOL_LORA_STUDIO+":", limit)
	}
	if err != nil {
		loraError(c, http.StatusInternalServerError, 500, "Failed to get tasks", "INTERNAL_ERROR")
		return
	}

	result := make([]map[string]interface{}, 0, len(tasks))
	for i := range tasks {
		task := &tasks[i]
		dto := toolsService.TaskToResponseDTOWithContext(c.Request.Context(), task)
		if task.RequestData != nil {
			dto["requestData"] = task.RequestData
		}
		result = append(result, dto)
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data":    result,
	})
}

func (a *LoraApi) GetActiveTasks(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		loraError(c, http.StatusUnauthorized, 401, "Unauthorized", "UNAUTHORIZED")
		return
	}

	slug := strings.TrimSpace(c.Query("slug"))

	var tasks []model.GenerationTask
	var err error
	if slug != "" {
		tasks, err = toolsService.GetActiveTasksByUIDAndTool(int(uid), []string{buildLoraToolID(slug)})
	} else {
		tasks, err = toolsService.GetActiveTasksByUIDAndToolPrefix(int(uid), model.TOOL_LORA_STUDIO+":")
	}
	if err != nil {
		loraError(c, http.StatusInternalServerError, 500, "Failed to get active tasks", "INTERNAL_ERROR")
		return
	}

	result := make([]map[string]interface{}, 0, len(tasks))
	for i := range tasks {
		result = append(result, toolsService.TaskToResponseDTOWithContext(c.Request.Context(), &tasks[i]))
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data":    result,
	})
}

func (a *LoraApi) GetHistory(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		loraError(c, http.StatusUnauthorized, 401, "Unauthorized", "UNAUTHORIZED")
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	limit = clampListLimit(limit, 20)
	withTotal := c.DefaultQuery("withTotal", "1") != "0"
	slug := strings.TrimSpace(c.Query("slug"))

	// `lora-studio:_%` requires at least one character after the colon,
	// so rows with a malformed `tool_id == "lora-studio:"` (no slug) are
	// excluded at the SQL layer — the same invariant the old Go-side
	// isLoraGenerationRecord filter enforced.
	baseQuery := globals.GraDBs["system"].Model(&model.GenerationRecord{}).
		Where("uid = ? AND status = ?", uid, model.STATUS_SUCCESS).
		Where("tool_id LIKE ?", model.TOOL_LORA_STUDIO+":_%")
	if slug != "" {
		baseQuery = baseQuery.Where("tool_id = ?", buildLoraToolID(slug))
	}

	var total int64
	if withTotal {
		if err := baseQuery.Session(&gorm.Session{}).Count(&total).Error; err != nil {
			loraError(c, http.StatusInternalServerError, 500, "Failed to get history", "INTERNAL_ERROR")
			return
		}
	}

	offset := (page - 1) * limit
	var records []model.GenerationRecord
	if err := baseQuery.
		Order("id desc").
		Offset(offset).
		Limit(limit).
		Find(&records).Error; err != nil {
		loraError(c, http.StatusInternalServerError, 500, "Failed to get history", "INTERNAL_ERROR")
		return
	}

	items := make([]loraHistoryRecordDto, 0, len(records))
	for i := range records {
		items = append(items, toLoraHistoryRecordDto(&records[i]))
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data": gin.H{
			"items": items,
			"total": total,
			"page":  page,
			"limit": limit,
		},
	})
}

func (a *LoraApi) DeleteHistoryRecord(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		loraError(c, http.StatusUnauthorized, 401, "Unauthorized", "UNAUTHORIZED")
		return
	}

	recordID, err := strconv.Atoi(strings.TrimSpace(c.Param("id")))
	if err != nil || recordID <= 0 {
		loraError(c, http.StatusBadRequest, 400, "Record ID is required", "INVALID_REQUEST")
		return
	}

	var record model.GenerationRecord
	if err := globals.GraDBs["system"].
		Where("id = ? AND uid = ? AND tool_id LIKE ?", recordID, uid, model.TOOL_LORA_STUDIO+":%").
		First(&record).Error; err != nil {
		loraError(c, http.StatusNotFound, 404, "Record not found", "NOT_FOUND")
		return
	}

	if err := globals.GraDBs["system"].Transaction(func(tx *gorm.DB) error {
		return (&toolsService.GenerationObjectService{}).DeleteGenerationRecordsWithAssets(c.Request.Context(), tx, []model.GenerationRecord{record})
	}); err != nil {
		loraError(c, http.StatusInternalServerError, 500, "Failed to delete record", "INTERNAL_ERROR")
		return
	}

	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "success", "data": gin.H{"id": recordID}})
}

func (a *LoraApi) CancelTask(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		loraError(c, http.StatusUnauthorized, 401, "Unauthorized", "UNAUTHORIZED")
		return
	}

	taskID := strings.TrimSpace(c.Param("taskId"))
	if taskID == "" {
		loraError(c, http.StatusBadRequest, 400, "Task ID is required", "INVALID_REQUEST")
		return
	}

	task, err := toolsService.GetTaskByID(taskID)
	if err != nil {
		loraError(c, http.StatusNotFound, 404, "Task not found", "NOT_FOUND")
		return
	}
	if task.UID != int(uid) {
		loraError(c, http.StatusForbidden, 403, "Access denied", "FORBIDDEN")
		return
	}
	if !isLoraGenerationTask(task) {
		loraError(c, http.StatusForbidden, 403, "Task does not belong to lora", "WRONG_TOOL")
		return
	}

	(&GeneratorApi{}).CancelTask(c)
}

func (a *LoraApi) RetryTask(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		loraError(c, http.StatusUnauthorized, 401, "Unauthorized", "UNAUTHORIZED")
		return
	}

	taskID := strings.TrimSpace(c.Param("taskId"))
	if taskID == "" {
		loraError(c, http.StatusBadRequest, 400, "Task ID is required", "INVALID_REQUEST")
		return
	}

	task, err := toolsService.GetTaskByID(taskID)
	if err != nil {
		loraError(c, http.StatusNotFound, 404, "Task not found", "NOT_FOUND")
		return
	}
	if task.UID != int(uid) {
		loraError(c, http.StatusForbidden, 403, "Access denied", "FORBIDDEN")
		return
	}
	if !isLoraGenerationTask(task) {
		loraError(c, http.StatusForbidden, 403, "Task does not belong to lora", "WRONG_TOOL")
		return
	}

	// Delegate to GeneratorApi.RetryTask — it already enforces the
	// "only failed tasks can be retried" invariant. We keep the LoRA
	// ownership/tool filter above so a LoRA slug endpoint never touches
	// a task that belongs to a sibling tool.
	(&GeneratorApi{}).RetryTask(c)
}
