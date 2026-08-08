package tools

import (
	"io"
	"net/http"
	"os"
	"server/globals"
	"server/model"
	storageService "server/service/storage"
	toolsService "server/service/tools"
	"server/utils"
	"strings"

	"github.com/gin-gonic/gin"
)

type VectorizerApi struct{}

type vectorizerCreditQuoteResponse struct {
	Model   string `json:"model"`
	Credits int    `json:"credits"`
}

// vectorizerFallbackCredits is the last-resort credit cost used only when
// the pricing registry returns <=0 for the vectorizer tool. Keep in sync
// with the baseline cost in credit_cost.go.
const vectorizerFallbackCredits = 3

// vectorizerMaxReferenceImages bounds how many reference URLs a single
// Generate request may carry. The vectorizer UI enforces maxImages=1 and
// the server only consumes referenceImages[0] — any surplus is dropped
// rather than downloaded, so the cap also defends against payload-driven
// worker time amplification.
const vectorizerMaxReferenceImages = 1

const (
	maxVectorizerGenerateRequestBodyBytes = 1 << 20
	maxVectorizerSVGSourceBytes           = 5 << 20
)

func vectorizerToolIDs() []string {
	return []string{
		model.TOOL_IMAGE_VECTORIZER,
		"image-vectorizer",
		"vectorizer",
	}
}

// vectorizerHandlers wires the shared CRUD + ownership helpers (see
// tool_handler_helpers.go) for the Image Vectorizer tool.
var vectorizerHandlers = &ToolHandlerConfig{
	ToolIDs:                vectorizerToolIDs(),
	FeatureType:            model.TOOL_IMAGE_VECTORIZER,
	CancelForbiddenMessage: "Task does not belong to vectorizer",
}

func vectorizerError(c *gin.Context, statusCode int, code int, message string, errorCode string) {
	writeToolError(c, statusCode, code, message, errorCode)
}

func (a *VectorizerApi) GetCredits(c *gin.Context) {
	credits := toolsService.GetCreditCostByToolID(toolsService.CreditCostParams{
		ToolID: model.TOOL_IMAGE_VECTORIZER,
		Model:  "vectorizer",
	})
	if credits <= 0 {
		credits = vectorizerFallbackCredits
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data": vectorizerCreditQuoteResponse{
			Model:   "vectorizer",
			Credits: credits,
		},
	})
}

func (a *VectorizerApi) GetSource(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		vectorizerError(c, http.StatusUnauthorized, 401, "Unauthorized", "UNAUTHORIZED")
		return
	}

	rawURL := strings.TrimSpace(c.Query("url"))
	if rawURL == "" {
		vectorizerError(c, http.StatusBadRequest, 400, "SVG URL is required", "INVALID_REQUEST")
		return
	}
	if !strings.HasSuffix(strings.ToLower(rawURL), ".svg") && !strings.Contains(strings.ToLower(rawURL), ".svg?") {
		vectorizerError(c, http.StatusBadRequest, 400, "Only SVG assets are supported", "INVALID_REQUEST")
		return
	}

	if fullPath, ok, err := toolsService.ResolveLocalGeneratedResultPathForUID(rawURL, int(uid)); err != nil {
		vectorizerError(c, http.StatusBadRequest, 400, "Invalid SVG path", "INVALID_SVG_PATH")
		return
	} else if ok {
		if stat, statErr := os.Stat(fullPath); statErr == nil && stat.Size() > maxVectorizerSVGSourceBytes {
			vectorizerError(c, http.StatusRequestEntityTooLarge, 413, "SVG source is too large", "SVG_SOURCE_TOO_LARGE")
			return
		}
		data, readErr := os.ReadFile(fullPath)
		if readErr != nil {
			vectorizerError(c, http.StatusNotFound, 404, "SVG file not found", "NOT_FOUND")
			return
		}
		if len(data) > maxVectorizerSVGSourceBytes {
			vectorizerError(c, http.StatusRequestEntityTooLarge, 413, "SVG source is too large", "SVG_SOURCE_TOO_LARGE")
			return
		}
		writeVectorizerSVG(c, data)
		return
	}

	var object model.GenerationObject
	if err := globals.GraDBs["system"].
		Where("uid = ? AND status = ? AND tool_id IN ? AND public_url = ?", uid, model.GenerationObjectStatusActive, vectorizerToolIDs(), rawURL).
		Take(&object).Error; err != nil {
		vectorizerError(c, http.StatusNotFound, 404, "SVG object not found", "NOT_FOUND")
		return
	}

	store, ok, err := storageService.NewObjectStoreForProviderBucket(globals.GraConf.Generator.Storage, object.Provider, object.Bucket)
	if err != nil || !ok || store == nil {
		vectorizerError(c, http.StatusInternalServerError, 500, "SVG storage unavailable", "SVG_STORAGE_UNAVAILABLE")
		return
	}

	reader, _, err := store.Get(c.Request.Context(), object.ObjectKey)
	if err != nil {
		vectorizerError(c, http.StatusNotFound, 404, "SVG object not found", "NOT_FOUND")
		return
	}
	defer reader.Close()

	limitedReader := io.LimitReader(reader, maxVectorizerSVGSourceBytes+1)
	data, err := io.ReadAll(limitedReader)
	if err != nil {
		vectorizerError(c, http.StatusInternalServerError, 500, "Failed to read SVG source", "INTERNAL_ERROR")
		return
	}
	if len(data) > maxVectorizerSVGSourceBytes {
		vectorizerError(c, http.StatusRequestEntityTooLarge, 413, "SVG source is too large", "SVG_SOURCE_TOO_LARGE")
		return
	}
	writeVectorizerSVG(c, data)
}

// writeVectorizerSVG streams an SVG response with hardening headers. The
// endpoint gates on a .svg URL suffix, so we pin the content-type to SVG
// regardless of what local storage / object storage returns, and apply
// nosniff + a strict CSP + sandbox so embedded <script> tags can't execute
// in a direct navigation context (SVG is XML and allows inline scripting).
func writeVectorizerSVG(c *gin.Context, data []byte) {
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:; sandbox")
	c.Header("Content-Disposition", `inline; filename="source.svg"`)
	c.Data(http.StatusOK, "image/svg+xml; charset=utf-8", data)
}

// VectorizerGenerateRequest is the request body for /api/tools/vectorizer/generate.
//
// Vectorization is a deterministic raster→SVG conversion that runs the
// vtracer backend with hard-coded optimal settings (detailLevel=high,
// colorMode=color, colors=64, highFidelity=true). It does not consult a
// prompt, negativePrompt, aspect ratio, or model — those fields were
// historically accepted but never actually wired to the converter. So the
// contract is just "here is the source image". Older clients may still
// send extra fields in the body; the JSON decoder silently drops them.
type VectorizerGenerateRequest struct {
	ImageURL        string                `json:"imageUrl"`
	ReferenceImages []ReferenceImageInput `json:"referenceImages"`
}

// Generate 提交矢量化任务
func (a *VectorizerApi) Generate(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		vectorizerError(c, http.StatusUnauthorized, 401, "Unauthorized", "UNAUTHORIZED")
		return
	}

	var req VectorizerGenerateRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxVectorizerGenerateRequestBodyBytes)
	if err := c.ShouldBindJSON(&req); err != nil {
		vectorizerError(c, http.StatusBadRequest, 400, "Invalid request: "+err.Error(), "INVALID_REQUEST")
		return
	}

	// The UI allows exactly one reference image; the generator only ever
	// consumes the first entry (sourceImageURL falls back to it below).
	// Cap here so a crafted payload can't force the worker to download and
	// decode N unrelated URLs before the task ever starts.
	if len(req.ReferenceImages) > vectorizerMaxReferenceImages {
		vectorizerError(c, http.StatusBadRequest, 400, "Too many reference images", "TOO_MANY_REFERENCE_IMAGES")
		return
	}

	sourceImageURL := strings.TrimSpace(req.ImageURL)
	rawReferenceImages := req.ReferenceImages
	if len(rawReferenceImages) == 0 && sourceImageURL != "" {
		rawReferenceImages = []ReferenceImageInput{{
			ID:     "vectorizer-source-1",
			URL:    sourceImageURL,
			Weight: 1,
		}}
	}
	referenceImages, err := resolveGeneratorReferenceImages(rawReferenceImages, uid)
	if err != nil {
		vectorizerError(c, http.StatusBadRequest, 400, err.Error(), "INVALID_REFERENCE_IMAGE")
		return
	}
	if len(referenceImages) == 0 {
		vectorizerError(c, http.StatusBadRequest, 400, "Reference image is required", "REFERENCE_IMAGE_REQUIRED")
		return
	}
	if len(referenceImages) > vectorizerMaxReferenceImages {
		vectorizerError(c, http.StatusBadRequest, 400, "Too many reference images", "TOO_MANY_REFERENCE_IMAGES")
		return
	}
	sourceImageURL = strings.TrimSpace(referenceImages[0].URL)

	creditCost := toolsService.GetCreditCostByToolID(toolsService.CreditCostParams{
		ToolID: model.TOOL_IMAGE_VECTORIZER,
		Model:  "vectorizer",
	})
	if creditCost <= 0 {
		creditCost = vectorizerFallbackCredits
	}

	requestData := model.JSONMap{
		"model":           "vectorizer",
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
		ModelID:        "vectorizer",
		ToolID:         model.TOOL_IMAGE_VECTORIZER,
		CreditCost:     creditCost,
		RequestData:    requestData,
		InputFiles:     inputFiles,
		IdempotencyKey: idempotencyKey,
		Origin:         model.GENERATION_ORIGIN_IMAGE_GEN,
	})
}

func (a *VectorizerApi) GetTasks(c *gin.Context)       { vectorizerHandlers.HandleGetTasks(c) }
func (a *VectorizerApi) GetActiveTasks(c *gin.Context) { vectorizerHandlers.HandleGetActiveTasks(c) }
func (a *VectorizerApi) GetHistory(c *gin.Context)     { vectorizerHandlers.HandleGetHistory(c) }
func (a *VectorizerApi) DeleteHistoryRecord(c *gin.Context) {
	vectorizerHandlers.HandleDeleteHistoryRecord(c)
}
func (a *VectorizerApi) CancelTask(c *gin.Context) { vectorizerHandlers.HandleCancelTask(c) }
func (a *VectorizerApi) RetryTask(c *gin.Context)  { vectorizerHandlers.HandleRetryTask(c) }
