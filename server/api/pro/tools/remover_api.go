package tools

import (
	"net/http"
	"server/model"
	toolsService "server/service/tools"
	"server/utils"
	"strings"

	"github.com/gin-gonic/gin"
)

type RemoverApi struct{}

type removerCreditQuoteResponse struct {
	Model   string `json:"model"`
	Credits int    `json:"credits"`
}

const (
	// removerDefaultPrompt is the canonical instruction sent to the underlying
	// image-edit model. English-only by design — the prompt is operational
	// (it describes the editing operation, not user-facing content), and
	// instruction-following degrades on translated variants. There is no UI
	// control to override it; the request struct deliberately omits a prompt
	// field so the server is the single source of truth.
	removerDefaultPrompt         = "remove the background from this image, preserve the original subject, composition, and details"
	removerDefaultNegativePrompt = "new subject, extra object, altered face, changed product, different pose, cropped subject, illustration"
	// removerOutputAspectRatio is "auto" so generator_service.inferRemoverAspectRatio
	// matches the source image's dimensions — preserving subject framing is
	// the entire point of background removal.
	removerOutputAspectRatio = "auto"
	// removerPrimaryModelID is the model the task is enqueued under, which
	// determines the first attempt in task_queue.buildTaskModelCandidates.
	// gpt-image-2 leads because its image-edit endpoint produces sharper
	// foreground preservation than the Gemini preview channel and is
	// async/poll-based, so a stalled job doesn't burn the task ctx the way
	// a sync Gemini call does. nanobanana-2 / nanobanana stay as fallbacks
	// (see modelCandidates in Generate) so a missing gpt-image-2 provider
	// or a transient failure still produces an image.
	removerPrimaryModelID = model.GPT_IMAGE_2
	// removerFallbackCredits is the last-resort credit cost used only when the
	// pricing registry returns <=0 for the remover tool. Keep in sync with the
	// baseline cost in credit_cost.go.
	removerFallbackCredits = 4

	maxRemoverGenerateRequestBodyBytes = 1 << 20
)

func removerToolIDs() []string {
	return []string{
		model.TOOL_BACKGROUND_REMOVER,
		"background-remover",
		"remover",
	}
}

// removerHandlers wires the shared CRUD + ownership helpers (see
// tool_handler_helpers.go) for the Background Remover tool.
var removerHandlers = &ToolHandlerConfig{
	ToolIDs:                removerToolIDs(),
	FeatureType:            model.TOOL_BACKGROUND_REMOVER,
	CancelForbiddenMessage: "Task does not belong to remover",
}

func removerError(c *gin.Context, statusCode int, code int, message string, errorCode string) {
	writeToolError(c, statusCode, code, message, errorCode)
}

func (a *RemoverApi) GetCredits(c *gin.Context) {
	credits := toolsService.GetCreditCostByToolID(toolsService.CreditCostParams{
		ToolID: model.TOOL_BACKGROUND_REMOVER,
		Model:  removerPrimaryModelID,
	})
	if credits <= 0 {
		credits = removerFallbackCredits
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data": removerCreditQuoteResponse{
			Model:   "remover",
			Credits: credits,
		},
	})
}

// RemoverGenerateRequest is the request body for /api/tools/remover/generate.
//
// Background removal has no user-controllable knobs: the operation is fixed
// (remove the background, preserve everything else), the prompt is server-set
// (see removerDefaultPrompt), and the output aspect ratio is always inferred
// from the source image. So the contract is just "here is the source image" —
// everything else is server-authoritative. Older clients may still send
// `prompt` / `negativePrompt` / `aspectRatio` / `params` in the body; the
// JSON decoder silently drops unknown fields.
type RemoverGenerateRequest struct {
	ImageURL        string                `json:"imageUrl"`
	ReferenceImages []ReferenceImageInput `json:"referenceImages"`
}

// Generate 提交背景移除任务
func (a *RemoverApi) Generate(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		removerError(c, http.StatusUnauthorized, 401, "Unauthorized", "UNAUTHORIZED")
		return
	}

	var req RemoverGenerateRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxRemoverGenerateRequestBodyBytes)
	if err := c.ShouldBindJSON(&req); err != nil {
		removerError(c, http.StatusBadRequest, 400, "Invalid request: "+err.Error(), "INVALID_REQUEST")
		return
	}

	sourceImageURL := strings.TrimSpace(req.ImageURL)
	rawReferenceImages := req.ReferenceImages
	if len(rawReferenceImages) == 0 && sourceImageURL != "" {
		rawReferenceImages = []ReferenceImageInput{{
			ID:     "remover-source-1",
			URL:    sourceImageURL,
			Weight: 1,
		}}
	}
	referenceImages, err := resolveGeneratorReferenceImages(rawReferenceImages, uid)
	if err != nil {
		removerError(c, http.StatusBadRequest, 400, err.Error(), "INVALID_REFERENCE_IMAGE")
		return
	}
	if len(referenceImages) == 0 {
		removerError(c, http.StatusBadRequest, 400, "Reference image is required", "REFERENCE_IMAGE_REQUIRED")
		return
	}
	if sourceImageURL == "" {
		sourceImageURL = strings.TrimSpace(referenceImages[0].URL)
	}

	creditCost := toolsService.GetCreditCostByToolID(toolsService.CreditCostParams{
		ToolID: model.TOOL_BACKGROUND_REMOVER,
		Model:  removerPrimaryModelID,
	})
	if creditCost <= 0 {
		creditCost = removerFallbackCredits
	}

	// Provider preference, in order. gpt-image-2 is primary (see
	// removerPrimaryModelID for rationale); nanobanana-2 / nanobanana are
	// fallbacks so a missing gpt-image-2 provider, a transient failure, or
	// a stalled job still produces an image. task_queue's
	// buildTaskModelCandidates walks this list in order — the first entry
	// is always task.Model (= removerPrimaryModelID), then dedup'd
	// continuations.
	requestData := model.JSONMap{
		"model":           removerPrimaryModelID,
		"modelCandidates": []string{model.GPT_IMAGE_2, model.NANO_BANANA_2, model.NANO_BANANA},
		"prompt":          removerDefaultPrompt,
		"negativePrompt":  removerDefaultNegativePrompt,
		"aspectRatio":     removerOutputAspectRatio,
		"referenceImages": referenceImages,
		"numberOfImages":  1,
		"imageUrl":        sourceImageURL,
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
		ModelID:        removerPrimaryModelID,
		ToolID:         model.TOOL_BACKGROUND_REMOVER,
		CreditCost:     creditCost,
		RequestData:    requestData,
		InputFiles:     inputFiles,
		IdempotencyKey: idempotencyKey,
		Origin:         model.GENERATION_ORIGIN_IMAGE_GEN,
	})
}

func (a *RemoverApi) GetTasks(c *gin.Context)       { removerHandlers.HandleGetTasks(c) }
func (a *RemoverApi) GetActiveTasks(c *gin.Context) { removerHandlers.HandleGetActiveTasks(c) }
func (a *RemoverApi) GetHistory(c *gin.Context)     { removerHandlers.HandleGetHistory(c) }
func (a *RemoverApi) DeleteHistoryRecord(c *gin.Context) {
	removerHandlers.HandleDeleteHistoryRecord(c)
}
func (a *RemoverApi) CancelTask(c *gin.Context) { removerHandlers.HandleCancelTask(c) }
func (a *RemoverApi) RetryTask(c *gin.Context)  { removerHandlers.HandleRetryTask(c) }
