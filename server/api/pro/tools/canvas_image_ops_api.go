package tools

// canvas_image_ops_api.go · pre-M3 image-ops handlers extracted from
// canvas_generation_api.go on 2026-05-15.
//
// Upscale and RemoveBg sit at a different conceptual layer from the
// §10.1 editing pipeline (img2img / outpaint / inpaint / mockup /
// edit-text / split-layers):
//
//   - They hard-code their own primary→fallback modelCandidates chain
//     (gpt-image-2 → nanobanana-2 → nanobanana) and intentionally
//     bypass authorizeCanvasModelCandidates because the chain itself
//     has no Pro-gated entries to authorize.
//   - They mirror the standalone /tools/upscaler and /tools/remover
//     surfaces' behaviour 1:1 — the canvas variants exist so canvas
//     elements can call into the same job queue while carrying the
//     Origin=canvas stamp + CanvasTaskBinding. They are *not* part of
//     the canvas editing flow's catalog-pricing path.
//   - Both still flow through submitCanvasGenerationTask in
//     canvas_generation_api.go so credit reservation + binding creation
//     stays on one choke point.
//
// Keeping them in canvas_generation_api.go conflated two different
// dispatch shapes; this file isolates the legacy / sibling-surface
// mirror handlers from the editing pipeline proper.

import (
	"fmt"
	"net/http"

	"server/globals"
	"server/model"
	"server/utils"

	"github.com/gin-gonic/gin"
)

// Upscale godoc
// @Summary Canvas Image Upscale (delegated to GeneratorApi task queue)
// @Description Upscales an image by submitting a real job to the global task queue.
// @Tags Tools:Canvas (Pro)
func (a *CanvasApi) Upscale(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondCanvasAIError(c, http.StatusUnauthorized, "Unauthorized", canvasAIErrorUnauthorized)
		return
	}

	var req struct {
		ImageURL string   `json:"imageUrl" binding:"required"`
		Scale    *float64 `json:"scale"`
	}
	if err := bindCanvasGenerationJSON(c, &req); err != nil {
		respondCanvasAIError(c, http.StatusBadRequest, "Invalid request: "+err.Error(), canvasAIErrorInvalidReq)
		return
	}

	scale := 2
	if req.Scale != nil && *req.Scale > 0 {
		scale = int(*req.Scale)
	}
	if scale != 2 && scale != 4 {
		scale = 2
	}

	sourceImageURL, err := normalizeCanvasGenerationSourceURL(c, uid, req.ImageURL)
	if err != nil || sourceImageURL == "" {
		respondCanvasAIError(c, http.StatusBadRequest, "imageUrl is invalid", canvasAIErrorInvalidParams)
		return
	}
	referenceImages := []map[string]interface{}{
		{"id": "upscale-source-1", "url": sourceImageURL, "weight": 1},
	}

	prompt := fmt.Sprintf("Upscale the reference image to %dx resolution, preserve original composition, improve clarity and details", scale)

	// Mirror /tools/upscaler's primary→fallback chain so canvas upscale gets
	// the same behavior: gpt-image-2 first (sharper image-edit output, async
	// poll budget), then nanobanana-2 / nanobanana as fallbacks.
	requestData := model.JSONMap{
		"model":           model.GPT_IMAGE_2,
		"modelCandidates": []string{model.GPT_IMAGE_2, model.NANO_BANANA_2, model.NANO_BANANA},
		"prompt":          prompt,
		"referenceImages": referenceImages,
		"numberOfImages":  1,
		"imageUrl":        sourceImageURL,
		"scale":           scale,
		"enhanceFace":     false,
		"source":          "canvas",
	}

	inputFiles := map[string]interface{}{
		"imageUrl":        sourceImageURL,
		"referenceImages": referenceImages,
	}

	globals.Info(fmt.Sprintf("[Canvas AI] upscale task delegated to queue, uid: %d, scale: %d, source: %s", uid, scale, sourceImageURL))
	a.submitCanvasGenerationTask(c, uid, model.TOOL_IMAGE_UPSCALER, model.GPT_IMAGE_2, requestData, inputFiles)
}

// RemoveBg godoc
// @Summary Canvas Image Remove Background (delegated to GeneratorApi task queue)
// @Description Removes background from an image by submitting a real job to the global task queue.
// @Tags Tools:Canvas (Pro)
func (a *CanvasApi) RemoveBg(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondCanvasAIError(c, http.StatusUnauthorized, "Unauthorized", canvasAIErrorUnauthorized)
		return
	}

	var req struct {
		ImageURL string `json:"imageUrl" binding:"required"`
	}
	if err := bindCanvasGenerationJSON(c, &req); err != nil {
		respondCanvasAIError(c, http.StatusBadRequest, "Invalid request: "+err.Error(), canvasAIErrorInvalidReq)
		return
	}

	sourceImageURL, err := normalizeCanvasGenerationSourceURL(c, uid, req.ImageURL)
	if err != nil || sourceImageURL == "" {
		respondCanvasAIError(c, http.StatusBadRequest, "imageUrl is invalid", canvasAIErrorInvalidParams)
		return
	}
	referenceImages := []map[string]interface{}{
		{"id": "removebg-source-1", "url": sourceImageURL, "weight": 1},
	}

	// Mirror /tools/remover's primary→fallback chain so canvas remove-bg gets
	// the same behavior: gpt-image-2 first (sharper foreground preservation,
	// async poll budget), then nanobanana-2 / nanobanana as fallbacks.
	requestData := model.JSONMap{
		"model":           removerPrimaryModelID,
		"modelCandidates": []string{model.GPT_IMAGE_2, model.NANO_BANANA_2, model.NANO_BANANA},
		"prompt":          removerDefaultPrompt,
		"negativePrompt":  removerDefaultNegativePrompt,
		"aspectRatio":     removerOutputAspectRatio,
		"referenceImages": referenceImages,
		"numberOfImages":  1,
		"imageUrl":        sourceImageURL,
		"source":          "canvas",
	}

	inputFiles := map[string]interface{}{
		"imageUrl":        sourceImageURL,
		"referenceImages": referenceImages,
	}

	globals.Info(fmt.Sprintf("[Canvas AI] remove-bg task delegated to queue, uid: %d, source: %s", uid, sourceImageURL))
	a.submitCanvasGenerationTask(c, uid, model.TOOL_BACKGROUND_REMOVER, removerPrimaryModelID, requestData, inputFiles)
}
