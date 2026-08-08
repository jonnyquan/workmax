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
)

type UpscalerApi struct{}

type upscalerCreditQuoteResponse struct {
	Model       string `json:"model"`
	Credits     int    `json:"credits"`
	Scale       int    `json:"scale"`
	EnhanceFace bool   `json:"enhanceFace"`
}

const (
	// upscalerToolMetaID is the tool-level identifier persisted as metadata
	// (taskModel) and returned in the credit quote response. It is NOT a
	// real provider model — provider routing is driven by upscalerPrimaryModelID.
	upscalerToolMetaID = "upscaler"
	// upscalerPrimaryModelID is the model the task is enqueued under, which
	// determines the first attempt in task_queue.buildTaskModelCandidates.
	// gpt-image-2 is preferred for upscale because (a) its image-edit
	// endpoint produces sharper, less hallucinated upscales than the Gemini
	// preview channel and (b) it is async/poll-based with its own timeout
	// budget, so a single stalled job doesn't burn the task ctx the way
	// a sync Gemini call does. nanobanana-2 / nanobanana stay as fallbacks
	// (see modelCandidates in Generate) so a missing gpt-image-2 provider
	// or a transient failure still produces an image.
	upscalerPrimaryModelID     = model.GPT_IMAGE_2
	upscalerDefaultAspectRatio = "auto"

	// Fallback credit costs used only when the pricing registry (GetCreditCostByToolID)
	// returns 0 — i.e. when a row is missing for this tool/model combination.
	// Values match the registry defaults at the time of writing; keep them in sync
	// with server/service/tools pricing if the registry is updated.
	upscalerFallbackCreditsEnhanced = 5 // 4x scale OR enhanceFace on
	upscalerFallbackCreditsStandard = 3 // 2x scale, no enhancement

	maxUpscalerGenerateRequestBodyBytes = 1 << 20
	maxUpscalerPromptRunes              = 5000
	maxUpscalerNegativePromptRunes      = 5000
	maxUpscalerParamsJSONBytes          = 64 << 10
)

func upscalerToolIDs() []string {
	return []string{
		model.TOOL_IMAGE_UPSCALER,
		"image-upscaler",
		"upscaler",
	}
}

// upscalerHandlers wires the shared CRUD + ownership helpers (see
// tool_handler_helpers.go) for the Image Upscaler tool.
var upscalerHandlers = &ToolHandlerConfig{
	ToolIDs:                upscalerToolIDs(),
	FeatureType:            model.TOOL_IMAGE_UPSCALER,
	CancelForbiddenMessage: "Task does not belong to upscaler",
}

func upscalerError(c *gin.Context, statusCode int, code int, message string, errorCode string) {
	writeToolError(c, statusCode, code, message, errorCode)
}

func readIntParam(params map[string]interface{}, key string) int {
	if params == nil {
		return 0
	}
	raw, ok := params[key]
	if !ok {
		return 0
	}
	switch v := raw.(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float32:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

func readBoolParam(params map[string]interface{}, key string) bool {
	if params == nil {
		return false
	}
	raw, ok := params[key]
	if !ok {
		return false
	}
	value, ok := raw.(bool)
	if !ok {
		return false
	}
	return value
}

func readStringParam(params map[string]interface{}, key string) string {
	if params == nil {
		return ""
	}
	raw, ok := params[key]
	if !ok {
		return ""
	}
	value, ok := raw.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

// mergeUpscalerEnhanceFlags ORs the canonical enhanceFace flag with two legacy
// fallbacks: nested params.enhanceFace (older client builds) and
// params.enhanceQuality (the original platform-wide pricing alias). Any truthy
// input enables enhancement. New code must use req.EnhanceFace exclusively.
func mergeUpscalerEnhanceFlags(req *UpscalerGenerateRequest) bool {
	if req == nil {
		return false
	}
	return req.EnhanceFace ||
		readBoolParam(req.Params, "enhanceFace") ||
		readBoolParam(req.Params, "enhanceQuality")
}

func normalizeUpscalerAspectRatio(aspectRatio string) string {
	normalized := strings.TrimSpace(aspectRatio)
	if normalized == "" {
		return upscalerDefaultAspectRatio
	}
	return normalized
}

func (a *UpscalerApi) GetCredits(c *gin.Context) {
	scale, _ := strconv.Atoi(strings.TrimSpace(c.DefaultQuery("scale", "2")))
	if scale != 4 {
		scale = 2
	}
	parseTruthy := func(value string) bool {
		v := strings.TrimSpace(value)
		return v == "1" || strings.EqualFold(v, "true")
	}
	// Accept the legacy "enhanceQuality" query param too — old client
	// builds may still be cached against this endpoint.
	enhanceFace := parseTruthy(c.Query("enhanceFace")) || parseTruthy(c.Query("enhanceQuality"))

	credits := toolsService.GetCreditCostByToolID(toolsService.CreditCostParams{
		ToolID:      model.TOOL_IMAGE_UPSCALER,
		Model:       upscalerPrimaryModelID,
		Scale:       scale,
		EnhanceFace: enhanceFace,
		Params: map[string]interface{}{
			"scale":       scale,
			"enhanceFace": enhanceFace,
		},
	})
	if credits <= 0 {
		if scale == 4 || enhanceFace {
			credits = upscalerFallbackCreditsEnhanced
		} else {
			credits = upscalerFallbackCreditsStandard
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data": upscalerCreditQuoteResponse{
			Model:       upscalerToolMetaID,
			Credits:     credits,
			Scale:       scale,
			EnhanceFace: enhanceFace,
		},
	})
}

// UpscalerGenerateRequest 图像超分请求
//
// `enhanceFace` is the canonical name everywhere — UI label, persisted column,
// pricing input. The legacy `enhanceQuality` alias is no longer accepted at the
// top level; if older clients still send it, they need to put it inside
// `params` (see mergeUpscalerEnhanceFlags for the legacy fallback).
type UpscalerGenerateRequest struct {
	ImageURL        string                 `json:"imageUrl"`
	ReferenceImages []ReferenceImageInput  `json:"referenceImages"`
	Prompt          string                 `json:"prompt"`
	NegativePrompt  string                 `json:"negativePrompt"`
	AspectRatio     string                 `json:"aspectRatio"`
	Scale           int                    `json:"scale"` // 2, 4
	EnhanceFace     bool                   `json:"enhanceFace"`
	Params          map[string]interface{} `json:"params"`
}

// Generate 处理图像放大请求
func (a *UpscalerApi) Generate(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		upscalerError(c, http.StatusUnauthorized, 401, "Unauthorized", "UNAUTHORIZED")
		return
	}

	var req UpscalerGenerateRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxUpscalerGenerateRequestBodyBytes)
	if err := c.ShouldBindJSON(&req); err != nil {
		upscalerError(c, http.StatusBadRequest, 400, "Invalid request: "+err.Error(), "INVALID_REQUEST")
		return
	}

	sourceImageURL := strings.TrimSpace(req.ImageURL)
	rawReferenceImages := req.ReferenceImages
	if len(rawReferenceImages) == 0 && sourceImageURL != "" {
		rawReferenceImages = []ReferenceImageInput{{
			ID:     "upscale-source-1",
			URL:    sourceImageURL,
			Weight: 1,
		}}
	}
	referenceImages, err := resolveGeneratorReferenceImages(rawReferenceImages, uid)
	if err != nil {
		upscalerError(c, http.StatusBadRequest, 400, err.Error(), "INVALID_REFERENCE_IMAGE")
		return
	}
	if len(referenceImages) == 0 {
		upscalerError(c, http.StatusBadRequest, 400, "Reference image is required", "REFERENCE_IMAGE_REQUIRED")
		return
	}
	if sourceImageURL == "" {
		sourceImageURL = strings.TrimSpace(referenceImages[0].URL)
	}

	scale := req.Scale
	if scale == 0 {
		scale = readIntParam(req.Params, "scale")
	}
	if scale == 0 {
		scale = 2
	}
	if scale != 2 && scale != 4 {
		upscalerError(c, http.StatusBadRequest, 400, "Invalid scale. Must be 2 or 4", "INVALID_SCALE")
		return
	}

	enhanceFace := mergeUpscalerEnhanceFlags(&req)

	extraParams := model.JSONMap{}
	for key, value := range req.Params {
		extraParams[key] = value
	}
	paramsBytes, err := json.Marshal(req.Params)
	if err != nil {
		upscalerError(c, http.StatusBadRequest, 400, "Invalid params", "INVALID_PARAMS")
		return
	}
	if len(paramsBytes) > maxUpscalerParamsJSONBytes {
		upscalerError(c, http.StatusBadRequest, 400, "Params are too large", "INVALID_PARAMS")
		return
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		prompt = readStringParam(req.Params, "prompt")
	}
	if prompt == "" {
		prompt = "Upscale the reference image to " + strconv.Itoa(scale) + "x resolution, preserve original composition, improve clarity and details"
	}
	if len([]rune(prompt)) > maxUpscalerPromptRunes {
		upscalerError(c, http.StatusBadRequest, 400, "Prompt is too long", "PROMPT_TOO_LONG")
		return
	}

	negativePrompt := strings.TrimSpace(req.NegativePrompt)
	if negativePrompt == "" {
		negativePrompt = readStringParam(req.Params, "negativePrompt")
	}
	if len([]rune(negativePrompt)) > maxUpscalerNegativePromptRunes {
		upscalerError(c, http.StatusBadRequest, 400, "Negative prompt is too long", "NEGATIVE_PROMPT_TOO_LONG")
		return
	}

	aspectRatio := strings.TrimSpace(req.AspectRatio)
	if aspectRatio == "" {
		aspectRatio = readStringParam(req.Params, "aspectRatio")
	}
	aspectRatio = normalizeUpscalerAspectRatio(aspectRatio)
	if _, ok := allowedAspectRatios[aspectRatio]; !ok {
		upscalerError(c, http.StatusBadRequest, 400, "Invalid aspectRatio", "INVALID_ASPECT_RATIO")
		return
	}

	extraParams["scale"] = scale
	extraParams["enhanceFace"] = enhanceFace
	extraParams["prompt"] = prompt
	extraParams["negativePrompt"] = negativePrompt
	extraParams["aspectRatio"] = aspectRatio
	extraParams["imageUrl"] = sourceImageURL

	creditCost := toolsService.GetCreditCostByToolID(toolsService.CreditCostParams{
		ToolID:      model.TOOL_IMAGE_UPSCALER,
		Model:       upscalerPrimaryModelID,
		Scale:       scale,
		EnhanceFace: enhanceFace,
		Params:      extraParams,
	})
	if creditCost <= 0 {
		if scale == 4 || enhanceFace {
			creditCost = upscalerFallbackCreditsEnhanced
		} else {
			creditCost = upscalerFallbackCreditsStandard
		}
	}

	// Provider preference, in order. gpt-image-2 is primary (see
	// upscalerPrimaryModelID for rationale); nanobanana-2 / nanobanana are
	// fallbacks so a missing gpt-image-2 provider, a transient failure, or
	// a stalled job still produces an image. task_queue's
	// buildTaskModelCandidates walks this list in order — the first entry
	// is always task.Model (= upscalerPrimaryModelID), then dedup'd
	// continuations.
	requestData := model.JSONMap{
		"model":           upscalerPrimaryModelID,
		"modelCandidates": []string{model.GPT_IMAGE_2, model.NANO_BANANA_2, model.NANO_BANANA},
		"taskModel":       upscalerToolMetaID,
		"prompt":          prompt,
		"negativePrompt":  negativePrompt,
		"aspectRatio":     aspectRatio,
		"referenceImages": referenceImages,
		"numberOfImages":  1,
		"imageUrl":        sourceImageURL,
		"scale":           scale,
		"enhanceFace":     enhanceFace,
		"params":          extraParams,
	}

	inputFiles := map[string]interface{}{
		"imageUrl":        sourceImageURL,
		"referenceImages": referenceImages,
	}

	idempotencyKey := normalizeIdempotencyKey(c.GetHeader("X-Idempotency-Key"))
	if idempotencyKey == "" {
		idempotencyKey = normalizeIdempotencyKey(c.GetHeader("Idempotency-Key"))
	}

	(&GeneratorApi{}).submitGenerationTask(c, &submitTaskParams{
		UID:            uid,
		ModelID:        upscalerPrimaryModelID,
		ToolID:         model.TOOL_IMAGE_UPSCALER,
		CreditCost:     creditCost,
		RequestData:    requestData,
		InputFiles:     inputFiles,
		IdempotencyKey: idempotencyKey,
		Origin:         model.GENERATION_ORIGIN_IMAGE_GEN,
	})
}

func (a *UpscalerApi) GetTasks(c *gin.Context)       { upscalerHandlers.HandleGetTasks(c) }
func (a *UpscalerApi) GetActiveTasks(c *gin.Context) { upscalerHandlers.HandleGetActiveTasks(c) }
func (a *UpscalerApi) GetHistory(c *gin.Context)     { upscalerHandlers.HandleGetHistory(c) }
func (a *UpscalerApi) DeleteHistoryRecord(c *gin.Context) {
	upscalerHandlers.HandleDeleteHistoryRecord(c)
}
func (a *UpscalerApi) CancelTask(c *gin.Context) { upscalerHandlers.HandleCancelTask(c) }
func (a *UpscalerApi) RetryTask(c *gin.Context)  { upscalerHandlers.HandleRetryTask(c) }
