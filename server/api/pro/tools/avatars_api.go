package tools

import (
	"encoding/json"
	"net/http"
	"server/model"
	toolsService "server/service/tools"
	"server/utils"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type AvatarsApi struct{}

// avatarDefaultUnitCredits is the fallback per-image credit cost used when the
// tool cost registry returns no positive value for Avatar Studio (NANO_BANANA_2).
const avatarDefaultUnitCredits = 4

const (
	maxAvatarGenerateRequestBodyBytes = 1 << 20
	maxAvatarPromptRunes              = 5000
	maxAvatarNegativePromptRunes      = 5000
	maxAvatarParamsJSONBytes          = 64 << 10
	maxAvatarSeedValue                = 2147483647
)

func getAvatarIntParam(params map[string]interface{}, key string) (int, bool) {
	if params == nil {
		return 0, false
	}
	raw, ok := params[key]
	if !ok {
		return 0, false
	}
	switch value := raw.(type) {
	case int:
		return value, true
	case int8:
		return int(value), true
	case int16:
		return int(value), true
	case int32:
		return int(value), true
	case int64:
		return int(value), true
	case float32:
		return int(value), true
	case float64:
		return int(value), true
	default:
		return 0, false
	}
}

func getAvatarStringParam(params map[string]interface{}, key string) (string, bool) {
	if params == nil {
		return "", false
	}
	raw, ok := params[key]
	if !ok {
		return "", false
	}
	value, ok := raw.(string)
	if !ok {
		return "", false
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	return value, true
}

func getAvatarMapValue(raw interface{}) (map[string]interface{}, bool) {
	switch value := raw.(type) {
	case map[string]interface{}:
		return value, true
	case model.JSONMap:
		return value, true
	case string:
		if strings.TrimSpace(value) == "" {
			return nil, false
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(value), &parsed); err != nil {
			return nil, false
		}
		return parsed, true
	default:
		return nil, false
	}
}

func buildAvatarInputSnapshot(prompt, negativePrompt, aspectRatio string, numberOfImages int, scenePresetKey string, seed *int, refs []ReferenceImageInput) model.JSONMap {
	snapshot := model.JSONMap{
		"toolId":         "avatars",
		"prompt":         prompt,
		"negativePrompt": negativePrompt,
		"aspectRatio":    aspectRatio,
		"numberOfImages": numberOfImages,
	}
	if scenePresetKey != "" {
		snapshot["scenePresetKey"] = scenePresetKey
	}
	if seed != nil && *seed >= 0 {
		snapshot["seed"] = *seed
	}
	if len(refs) > 0 {
		snapshot["referenceImages"] = refs
	}
	return snapshot
}

func cloneAvatarRequestDataForSnapshot(input model.JSONMap) model.JSONMap {
	if input == nil {
		return model.JSONMap{}
	}
	cloned := cloneJSONMap(input)
	delete(cloned, "inputSnapshot")
	delete(cloned, "executionSnapshot")
	delete(cloned, "billingSnapshot")
	delete(cloned, "idempotencyKey")
	return cloned
}

func buildAvatarExecutionSnapshot(requestData model.JSONMap, modelID string) model.JSONMap {
	snapshot := model.JSONMap{
		"featureKey":  "avatars",
		"modelId":     modelID,
		"requestData": cloneAvatarRequestDataForSnapshot(requestData),
		"promptFinal": strings.TrimSpace(readStringFromAvatarRequestData(requestData, "prompt")),
		"aspectRatio": strings.TrimSpace(readStringFromAvatarRequestData(requestData, "aspectRatio")),
	}
	if negativePrompt := strings.TrimSpace(readStringFromAvatarRequestData(requestData, "negativePrompt")); negativePrompt != "" {
		snapshot["negativePromptFinal"] = negativePrompt
	}
	if scenePresetKey := strings.TrimSpace(readStringFromAvatarRequestData(requestData, "scenePresetKey")); scenePresetKey != "" {
		snapshot["scenePresetKey"] = scenePresetKey
	}
	if seed := readIntFromAvatarRequestData(requestData, "seed"); seed >= 0 {
		snapshot["seed"] = seed
	}
	return snapshot
}

func buildAvatarBillingSnapshot(unitCreditCost, requestedCount, reservedCredits int, billingStatus string) model.JSONMap {
	if unitCreditCost <= 0 {
		unitCreditCost = 1
	}
	if requestedCount <= 0 {
		requestedCount = 1
	}
	if reservedCredits <= 0 {
		reservedCredits = unitCreditCost * requestedCount
	}
	if strings.TrimSpace(billingStatus) == "" {
		billingStatus = "reserved"
	}
	return model.JSONMap{
		"unitCreditCost":      unitCreditCost,
		"requestedCount":      requestedCount,
		"reservedCredits":     reservedCredits,
		"successCount":        0,
		"failedCount":         0,
		"finalChargedCredits": 0,
		"refundCredits":       0,
		"billingStatus":       billingStatus,
	}
}

func readStringFromAvatarRequestData(raw model.JSONMap, key string) string {
	if raw == nil {
		return ""
	}
	if value, ok := raw[key]; ok {
		if str, ok := value.(string); ok {
			return str
		}
	}
	if nested, ok := getAvatarMapValue(raw["params"]); ok {
		if value, ok := nested[key]; ok {
			if str, ok := value.(string); ok {
				return str
			}
		}
	}
	return ""
}

func readIntFromAvatarRequestData(raw model.JSONMap, key string) int {
	if raw == nil {
		return -1
	}
	if value, ok := raw[key]; ok {
		switch casted := value.(type) {
		case int:
			return casted
		case int32:
			return int(casted)
		case int64:
			return int(casted)
		case float32:
			return int(casted)
		case float64:
			return int(casted)
		}
	}
	if nested, ok := getAvatarMapValue(raw["params"]); ok {
		if value, ok := nested[key]; ok {
			switch casted := value.(type) {
			case int:
				return casted
			case int32:
				return int(casted)
			case int64:
				return int(casted)
			case float32:
				return int(casted)
			case float64:
				return int(casted)
			}
		}
	}
	return -1
}

func avatarToolIDs() []string {
	return []string{
		model.TOOL_AVATAR_STUDIO,
		"avatar-studio",
		"avatars",
	}
}

// avatarHandlers wires the shared CRUD + ownership helpers (see
// tool_handler_helpers.go) for the Avatar Studio tool.
var avatarHandlers = &ToolHandlerConfig{
	ToolIDs:                avatarToolIDs(),
	FeatureType:            model.TOOL_AVATAR_STUDIO,
	CancelForbiddenMessage: "Task does not belong to avatars",
}

// avatarError is retained as a thin wrapper around the shared
// writeToolError so existing call sites in this file (Generate, etc.)
// keep working.
func avatarError(c *gin.Context, statusCode int, code int, message string, errorCode string) {
	writeToolError(c, statusCode, code, message, errorCode)
}

// AvatarsGenerateRequest 头像生成请求（默认使用 Nano Banana 2）
type AvatarsGenerateRequest struct {
	Prompt          string                 `json:"prompt" binding:"required"`
	NegativePrompt  string                 `json:"negativePrompt"`
	AspectRatio     string                 `json:"aspectRatio"`
	Params          map[string]interface{} `json:"params"`
	ReferenceImages []ReferenceImageInput  `json:"referenceImages"`
	NumberOfImages  int                    `json:"numberOfImages,omitempty"`
}

type avatarCreditQuoteResponse struct {
	Model          string `json:"model"`
	UnitCredits    int    `json:"unitCredits"`
	NumberOfImages int    `json:"numberOfImages"`
	Credits        int    `json:"credits"`
}

func (a *AvatarsApi) GetCredits(c *gin.Context) {
	numberOfImages, err := strconv.Atoi(strings.TrimSpace(c.DefaultQuery("numberOfImages", "1")))
	if err != nil {
		numberOfImages = 1
	}
	if numberOfImages < 1 {
		numberOfImages = 1
	}
	if numberOfImages > 4 {
		numberOfImages = 4
	}

	const providerModelID = model.NANO_BANANA_2
	unitCredits := toolsService.GetCreditCostByToolID(toolsService.CreditCostParams{
		ToolID:         model.TOOL_AVATAR_STUDIO,
		Model:          providerModelID,
		NumberOfImages: 1,
		Params: map[string]interface{}{
			"numberOfImages": 1,
		},
	})
	if unitCredits <= 0 {
		unitCredits = avatarDefaultUnitCredits
	}

	credits := toolsService.GetCreditCostByToolID(toolsService.CreditCostParams{
		ToolID:         model.TOOL_AVATAR_STUDIO,
		Model:          providerModelID,
		NumberOfImages: numberOfImages,
		Params: map[string]interface{}{
			"numberOfImages": numberOfImages,
		},
	})
	if credits <= 0 {
		credits = unitCredits * numberOfImages
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data": avatarCreditQuoteResponse{
			Model:          model.NANO_BANANA_2,
			UnitCredits:    unitCredits,
			NumberOfImages: numberOfImages,
			Credits:        credits,
		},
	})
}

// Generate 提交头像生成任务（独立入口，task 流程共用）
func (a *AvatarsApi) Generate(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		avatarError(c, http.StatusUnauthorized, 401, "Unauthorized", "UNAUTHORIZED")
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxAvatarGenerateRequestBodyBytes)

	var req AvatarsGenerateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		avatarError(c, http.StatusBadRequest, 400, "Invalid request: "+err.Error(), "INVALID_REQUEST")
		return
	}

	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		avatarError(c, http.StatusBadRequest, 400, "Prompt is required", "PROMPT_REQUIRED")
		return
	}
	if len([]rune(prompt)) > maxAvatarPromptRunes {
		avatarError(c, http.StatusBadRequest, 400, "Prompt is too long", "PROMPT_TOO_LONG")
		return
	}
	negativePrompt := strings.TrimSpace(req.NegativePrompt)
	if len([]rune(negativePrompt)) > maxAvatarNegativePromptRunes {
		avatarError(c, http.StatusBadRequest, 400, "Negative prompt is too long", "NEGATIVE_PROMPT_TOO_LONG")
		return
	}
	if req.Params != nil {
		paramsJSON, err := json.Marshal(req.Params)
		if err != nil || len(paramsJSON) > maxAvatarParamsJSONBytes {
			avatarError(c, http.StatusBadRequest, 400, "Invalid params", "INVALID_PARAMS")
			return
		}
	}
	referenceImages, err := resolveGeneratorReferenceImages(req.ReferenceImages, uid)
	if err != nil {
		avatarError(c, http.StatusBadRequest, 400, err.Error(), "INVALID_REFERENCE_IMAGE")
		return
	}
	req.ReferenceImages = referenceImages

	aspectRatio := strings.TrimSpace(req.AspectRatio)
	if aspectRatio == "" {
		aspectRatio = "1:1"
	}
	if _, ok := allowedAspectRatios[aspectRatio]; !ok {
		avatarError(c, http.StatusBadRequest, 400, "Invalid aspectRatio", "INVALID_ASPECT_RATIO")
		return
	}

	numberOfImages := req.NumberOfImages
	if req.Params != nil && numberOfImages == 0 {
		if v, ok := req.Params["numberOfImages"].(float64); ok {
			numberOfImages = int(v)
		}
	}
	if numberOfImages <= 0 {
		numberOfImages = 1
	}
	if numberOfImages < 1 || numberOfImages > 4 {
		avatarError(c, http.StatusBadRequest, 400, "Invalid numberOfImages", "INVALID_NUMBER_OF_IMAGES")
		return
	}
	scenePresetKey, _ := getAvatarStringParam(req.Params, "scenePresetKey")
	if !toolsService.IsKnownAvatarScenePresetKey(scenePresetKey) {
		avatarError(c, http.StatusBadRequest, 400, "Unknown scene preset", "UNKNOWN_SCENE_PRESET")
		return
	}
	seedValue, hasSeed := getAvatarIntParam(req.Params, "seed")
	var seedPtr *int
	if hasSeed && seedValue >= 0 {
		if seedValue > maxAvatarSeedValue {
			avatarError(c, http.StatusBadRequest, 400, "Invalid seed", "INVALID_SEED")
			return
		}
		seedCopy := seedValue
		seedPtr = &seedCopy
	}
	idempotencyKey := normalizeIdempotencyKey(c.GetHeader("X-Idempotency-Key"))
	if idempotencyKey == "" {
		idempotencyKey = normalizeIdempotencyKey(c.GetHeader("Idempotency-Key"))
	}

	const providerModelID = model.NANO_BANANA_2
	const taskModelID = model.NANO_BANANA_2
	// Fallback to nanobanana (gemini-2.5-flash-image) when nanobanana-2
	// (gemini-3.1-flash-image-preview) times out or is overloaded. The task
	// queue walks this list in order on transient failures (see
	// task_queue.go:buildTaskModelCandidates).
	modelCandidates := []string{model.NANO_BANANA_2, model.NANO_BANANA}
	requestData := model.JSONMap{
		"model":           providerModelID,
		"modelCandidates": modelCandidates,
		"prompt":          prompt,
		"negativePrompt":  negativePrompt,
		"aspectRatio":     aspectRatio,
		"referenceImages": req.ReferenceImages,
		"numberOfImages":  numberOfImages,
	}
	if scenePresetKey != "" {
		requestData["scenePresetKey"] = scenePresetKey
	}
	if len(req.Params) > 0 {
		requestData["params"] = req.Params
		if seedPtr != nil {
			requestData["seed"] = *seedPtr
		}
	}

	var inputFiles interface{}
	if len(req.ReferenceImages) > 0 {
		inputFiles = map[string]interface{}{
			"referenceImages": req.ReferenceImages,
		}
	}

	creditCost := toolsService.GetCreditCostByToolID(toolsService.CreditCostParams{
		ToolID:         model.TOOL_AVATAR_STUDIO,
		Model:          providerModelID,
		NumberOfImages: numberOfImages,
		Params:         req.Params,
	})
	singleCreditCost := toolsService.GetCreditCostByToolID(toolsService.CreditCostParams{
		ToolID:         model.TOOL_AVATAR_STUDIO,
		Model:          providerModelID,
		NumberOfImages: 1,
		Params:         req.Params,
	})
	if singleCreditCost <= 0 {
		singleCreditCost = 1
	}

	requestData["inputSnapshot"] = buildAvatarInputSnapshot(prompt, negativePrompt, aspectRatio, numberOfImages, scenePresetKey, seedPtr, req.ReferenceImages)
	requestData["executionSnapshot"] = buildAvatarExecutionSnapshot(requestData, providerModelID)
	requestData["billingSnapshot"] = buildAvatarBillingSnapshot(singleCreditCost, 1, singleCreditCost, "reserved")

	submitParams := &submitTaskParams{
		UID:            uid,
		ModelID:        taskModelID,
		ToolID:         model.TOOL_AVATAR_STUDIO,
		CreditCost:     creditCost,
		UnitCreditCost: singleCreditCost,
		RequestedCount: numberOfImages,
		IdempotencyKey: idempotencyKey,
		Origin:         model.GENERATION_ORIGIN_IMAGE_GEN,
	}

	if numberOfImages > 1 {
		// Split into N single-image tasks under one batch. The aggregator
		// (handled downstream) merges their results into a single
		// GenerationRecord; that's why each task carries skipRecord=true.
		batchID := "batch_" + uuid.New().String()
		items := make([]submitTaskItem, 0, numberOfImages)
		for i := 0; i < numberOfImages; i++ {
			taskRequestData := cloneJSONMap(requestData)
			taskRequestData["numberOfImages"] = 1
			taskRequestData["skipRecord"] = true
			taskRequestData["splitBatchId"] = batchID
			taskRequestData["splitBatchSize"] = numberOfImages
			taskRequestData["splitBatchIndex"] = i + 1
			taskRequestData["executionSnapshot"] = buildAvatarExecutionSnapshot(taskRequestData, providerModelID)
			taskRequestData["billingSnapshot"] = buildAvatarBillingSnapshot(singleCreditCost, 1, singleCreditCost, "reserved")

			items = append(items, submitTaskItem{
				ModelID:     taskModelID,
				ToolID:      model.TOOL_AVATAR_STUDIO,
				CreditCost:  singleCreditCost,
				RequestData: taskRequestData,
				InputFiles:  inputFiles,
			})
		}
		submitParams.TaskItems = items
		submitParams.BatchID = batchID
	} else {
		submitParams.RequestData = requestData
		submitParams.InputFiles = inputFiles
	}

	(&GeneratorApi{}).submitGenerationTask(c, submitParams)
}

func (a *AvatarsApi) GetTasks(c *gin.Context)            { avatarHandlers.HandleGetTasks(c) }
func (a *AvatarsApi) GetActiveTasks(c *gin.Context)      { avatarHandlers.HandleGetActiveTasks(c) }
func (a *AvatarsApi) GetHistory(c *gin.Context)          { avatarHandlers.HandleGetHistory(c) }
func (a *AvatarsApi) DeleteHistoryRecord(c *gin.Context) { avatarHandlers.HandleDeleteHistoryRecord(c) }
func (a *AvatarsApi) CancelTask(c *gin.Context)          { avatarHandlers.HandleCancelTask(c) }
func (a *AvatarsApi) RetryTask(c *gin.Context)           { avatarHandlers.HandleRetryTask(c) }

// GetScenePresets returns the canonical Avatar Studio scene presets. Labels and
// descriptions are resolved client-side via i18n keyed by Key.
func (a *AvatarsApi) GetScenePresets(c *gin.Context) {
	presets := toolsService.GetAvatarScenePresets()
	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data": gin.H{
			"presets": presets,
		},
	})
}
