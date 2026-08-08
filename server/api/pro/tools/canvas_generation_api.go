package tools

// canvas_generation_api.go · Generation 子域 (B1 · §10.1)
//
// Split out of canvas_api.go as part of the M2 收口 拆分 plan. Contains
// the canvas editing pipeline — img2img / outpaint / inpaint / mockup /
// edit-text / split-layers / upscale / remove-bg — plus the shared
// helpers that normalize models, authorize Pro access, build reference
// images, tag canvas-operation metadata, and hand off to the shared
// generator task queue via GeneratorApi.submitGenerationTask.
//
// Every handler here delegates to CanvasApi.submitCanvasGenerationTask
// (or directly to GeneratorApi.submitGenerationTask for the pre-M3
// upscale/remove-bg paths). That keeps the Origin=canvas stamp + the
// CanvasTaskBinding creation on a single choke point.

import (
	"fmt"
	"net/http"
	"strings"

	"server/globals"
	"server/model"
	toolsService "server/service/tools"
	canvasService "server/service/tools/canvas"
	"server/utils"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ============================================================================
// Canvas AI Operations
// ============================================================================

// canvasGenerationCreditFloor is the credit cost we charge when the
// catalog + legacy fallbacks both fail to produce a number. Pinned at
// 3 to match the historical default. Surfaces in
// submitCanvasGenerationTask only.
const canvasGenerationCreditFloor = 3

// These handlers normalize canvas editing requests and delegate them to the
// shared generator task queue so the frontend can poll /api/tools/task/:taskId.
// Pure helpers (normalize / filter / authorize / bind / set-meta) live in
// canvas_generation_helpers.go; the JSON-body cap moved there as well.

type canvasImg2ImgRequest struct {
	ImageURL        string   `json:"imageUrl" binding:"required"`
	Prompt          string   `json:"prompt" binding:"required"`
	Strength        float64  `json:"strength"`
	Model           string   `json:"model"`
	ModelCandidates []string `json:"modelCandidates,omitempty"`
	StylePreset     string   `json:"stylePreset"`
	AspectRatio     string   `json:"aspectRatio"`
	// Seed axis value for batch-matrix variants. Pointer so 0 and absent
	// are distinguishable; generator_service.go currently only honors
	// non-zero seeds, but persisting the caller's intent keeps retries
	// faithful even for seed==0.
	Seed        *int64 `json:"seed,omitempty"`
	TargetPoint *struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	} `json:"targetPoint,omitempty"`
}

type canvasOutpaintRequest struct {
	ImageURL        string   `json:"imageUrl" binding:"required"`
	Direction       string   `json:"direction" binding:"required"`
	ExpandRatio     *float64 `json:"expandRatio"`
	Model           string   `json:"model"`
	ModelCandidates []string `json:"modelCandidates,omitempty"`
	StylePreset     string   `json:"stylePreset"`
}

type canvasInpaintRequest struct {
	ImageURL        string   `json:"imageUrl" binding:"required"`
	MaskURL         string   `json:"maskUrl"`
	Prompt          string   `json:"prompt" binding:"required"`
	Model           string   `json:"model"`
	ModelCandidates []string `json:"modelCandidates,omitempty"`
	StylePreset     string   `json:"stylePreset"`
}

type canvasMockupRequest struct {
	ImageURL        string   `json:"imageUrl" binding:"required"`
	TemplateID      string   `json:"templateId"`
	Prompt          string   `json:"prompt"`
	Model           string   `json:"model"`
	ModelCandidates []string `json:"modelCandidates,omitempty"`
	StylePreset     string   `json:"stylePreset"`
}

type canvasEditTextRequest struct {
	ImageURL        string   `json:"imageUrl" binding:"required"`
	TargetText      string   `json:"targetText"`
	ReplacementText string   `json:"replacementText" binding:"required"`
	Model           string   `json:"model"`
	ModelCandidates []string `json:"modelCandidates,omitempty"`
}

type canvasSplitLayersRequest struct {
	ImageURL        string   `json:"imageUrl" binding:"required"`
	Model           string   `json:"model"`
	ModelCandidates []string `json:"modelCandidates,omitempty"`
}

// Error-code constants (canvasAIError*) and respondCanvasAIError live
// in canvas_ai_error.go — they form their own contract surface with
// dedicated tests in canvas_ai_error_test.go / _more_test.go.

func (a *CanvasApi) submitCanvasGenerationTask(
	c *gin.Context,
	uid uint,
	toolID string,
	modelID string,
	requestData model.JSONMap,
	inputFiles map[string]interface{},
) {
	// Catalog-first pricing (post 2026-05-15 A2 migration). Falls
	// back to legacy GetCreditCostByToolID for non-canvas tool IDs
	// (lora / avatar / prompt-builder) and for unrecognised payload
	// shapes — see settle_credit.go header for the rationale.
	//
	// Quote endpoint (canvas_quote_api.go) and this settle path
	// now share ONE pricing source of truth via canvasService.
	creditCost, ok := canvasService.SettleCreditCost(toolID, modelID, requestData)
	if !ok {
		creditCost = toolsService.GetCreditCostByToolID(toolsService.CreditCostParams{
			ToolID: toolID,
			Model:  modelID,
			Params: requestData,
		})
	}
	if creditCost <= 0 {
		creditCost = canvasGenerationCreditFloor
	}

	// M2-W4-01: attach the task to a Canvas project via the lightweight
	// CanvasTaskBinding row. Project + element are carried as headers
	// so we don't have to touch every canvas request struct. Missing
	// headers mean no binding is written (backward compatible).
	bindingCtx, status, message, errorCode := resolveCanvasTaskBindingContext(c, uid)
	if status != 0 {
		respondCanvasAIError(c, status, message, errorCode)
		return
	}

	// M3-W8-01b: if the element carries an AssetBinding, forward it via
	// requestData so the task processor can hand it to GenerateImage's
	// AssetBindings field. The unmarshal into model.TaskRequestData will
	// pick up the "assetBindings" key automatically.
	//
	// Task #13: stamp the canvas project id onto the binding when
	// we have a binding context. The injector reads it to scope the
	// continuity-tracked-references lookup (most recent per-character
	// render in this project). Setting it via the trusted binding
	// ctx (which is uid-validated above) prevents a malicious client
	// from forging a header to leak rows from another project.
	if binding := parseCanvasAssetBindingsHeader(c); binding != nil && requestData != nil {
		if bindingCtx != nil && bindingCtx.ProjectID > 0 {
			pid := uint64(bindingCtx.ProjectID)
			binding.CanvasProjectID = &pid
		}
		requestData["assetBindings"] = binding
	}
	var postCreate func(tx *gorm.DB, tasks []*model.GenerationTask) error
	if bindingCtx != nil {
		postCreate = bindingCtx.PostCreate
	}

	(&GeneratorApi{}).submitGenerationTask(c, &submitTaskParams{
		UID:         uid,
		ModelID:     modelID,
		ToolID:      toolID,
		CreditCost:  creditCost,
		RequestData: requestData,
		InputFiles:  inputFiles,
		PostCreate:  postCreate,
		Origin:      model.GENERATION_ORIGIN_CANVAS,
	})
}

// Header parsers (parseCanvasProjectHeader, resolveCanvasTaskBindingContext,
// parseCanvasAssetBindingsHeader + canvasTaskBindingContext) live in
// canvas_headers.go — they form their own contract surface with dedicated
// tests in canvas_headers_test.go / _more_test.go / _parse_test.go.

func (a *CanvasApi) Img2Img(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondCanvasAIError(c, http.StatusUnauthorized, "Unauthorized", canvasAIErrorUnauthorized)
		return
	}

	var req canvasImg2ImgRequest
	if err := bindCanvasGenerationJSON(c, &req); err != nil {
		respondCanvasAIError(c, http.StatusBadRequest, "Invalid request: "+err.Error(), canvasAIErrorInvalidReq)
		return
	}

	if req.Strength <= 0 || req.Strength > 1 {
		req.Strength = 0.5
	}
	sourceImageURL, err := normalizeCanvasGenerationSourceURL(c, uid, req.ImageURL)
	if err != nil || sourceImageURL == "" {
		respondCanvasAIError(c, http.StatusBadRequest, "imageUrl is invalid", canvasAIErrorInvalidParams)
		return
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		respondCanvasAIError(c, http.StatusBadRequest, "prompt is required", canvasAIErrorInvalidParams)
		return
	}

	modelID := normalizeCanvasImageModel(req.Model)
	modelCandidates, ok := authorizeCanvasModelCandidates(c, uid, modelID, req.ModelCandidates)
	if !ok {
		return
	}
	modelID = modelCandidates[0]
	referenceImages := buildCanvasReferenceImages(sourceImageURL)
	if req.TargetPoint != nil {
		prompt = fmt.Sprintf("%s Focus the edit around the marked target area while preserving the rest of the composition.", prompt)
	}
	requestData := model.JSONMap{
		"model":           modelID,
		"prompt":          prompt,
		"referenceImages": referenceImages,
		"numberOfImages":  1,
		"imageUrl":        sourceImageURL,
		"strength":        req.Strength,
	}
	meta := model.JSONMap{
		"strength": req.Strength,
	}
	if len(modelCandidates) > 1 {
		requestData["modelCandidates"] = modelCandidates
	}
	if stylePreset := strings.TrimSpace(req.StylePreset); stylePreset != "" {
		requestData["stylePreset"] = stylePreset
	}
	if aspectRatio := strings.TrimSpace(req.AspectRatio); aspectRatio != "" {
		requestData["aspectRatio"] = aspectRatio
	}
	applyOptionalCanvasSeed(requestData, req.Seed)
	if req.TargetPoint != nil {
		meta["targetPoint"] = map[string]float64{
			"x": req.TargetPoint.X,
			"y": req.TargetPoint.Y,
		}
	}
	setCanvasOperationMeta(requestData, "img2img", meta)

	inputFiles := map[string]interface{}{
		"imageUrl":        sourceImageURL,
		"referenceImages": referenceImages,
	}

	globals.Info(fmt.Sprintf("[Canvas AI] delegate img2img task, uid: %d, strength: %.2f", uid, req.Strength))
	a.submitCanvasGenerationTask(c, uid, model.TOOL_IMAGE_GENERATOR, modelID, requestData, inputFiles)
}

func (a *CanvasApi) Outpaint(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondCanvasAIError(c, http.StatusUnauthorized, "Unauthorized", canvasAIErrorUnauthorized)
		return
	}

	var req canvasOutpaintRequest
	if err := bindCanvasGenerationJSON(c, &req); err != nil {
		respondCanvasAIError(c, http.StatusBadRequest, "Invalid request: "+err.Error(), canvasAIErrorInvalidReq)
		return
	}

	direction := strings.ToLower(strings.TrimSpace(req.Direction))
	validDirections := map[string]bool{"up": true, "down": true, "left": true, "right": true}
	if !validDirections[direction] {
		respondCanvasAIError(c, http.StatusBadRequest, "Invalid direction, must be: up, down, left, right", canvasAIErrorInvalidDir)
		return
	}

	expandRatio := 1.5
	if req.ExpandRatio != nil {
		expandRatio = *req.ExpandRatio
		if expandRatio <= 1 || expandRatio > 3 {
			respondCanvasAIError(c, http.StatusBadRequest, "expandRatio must be greater than 1 and at most 3", canvasAIErrorInvalidExpand)
			return
		}
	}
	sourceImageURL, err := normalizeCanvasGenerationSourceURL(c, uid, req.ImageURL)
	if err != nil || sourceImageURL == "" {
		respondCanvasAIError(c, http.StatusBadRequest, "imageUrl is invalid", canvasAIErrorInvalidParams)
		return
	}
	modelID := normalizeCanvasImageModel(req.Model)
	modelCandidates, ok := authorizeCanvasModelCandidates(c, uid, modelID, req.ModelCandidates)
	if !ok {
		return
	}
	modelID = modelCandidates[0]
	referenceImages := buildCanvasReferenceImages(sourceImageURL)

	prompt := fmt.Sprintf("Outpaint the %s side of the reference image by %.2fx while preserving style, subject identity, and lighting continuity.", direction, expandRatio)
	requestData := model.JSONMap{
		"model":           modelID,
		"prompt":          prompt,
		"referenceImages": referenceImages,
		"numberOfImages":  1,
		"imageUrl":        sourceImageURL,
	}
	setCanvasOperationMeta(requestData, "outpaint", model.JSONMap{
		"direction":   direction,
		"expandRatio": expandRatio,
	})
	if len(modelCandidates) > 1 {
		requestData["modelCandidates"] = modelCandidates
	}
	if stylePreset := strings.TrimSpace(req.StylePreset); stylePreset != "" {
		requestData["stylePreset"] = stylePreset
	}

	inputFiles := map[string]interface{}{
		"imageUrl":        sourceImageURL,
		"referenceImages": referenceImages,
	}

	globals.Info(fmt.Sprintf("[Canvas AI] delegate outpaint task, uid: %d, direction: %s, ratio: %.2f", uid, direction, expandRatio))
	a.submitCanvasGenerationTask(c, uid, model.TOOL_IMAGE_GENERATOR, modelID, requestData, inputFiles)
}

func (a *CanvasApi) Inpaint(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondCanvasAIError(c, http.StatusUnauthorized, "Unauthorized", canvasAIErrorUnauthorized)
		return
	}

	var req canvasInpaintRequest
	if err := bindCanvasGenerationJSON(c, &req); err != nil {
		respondCanvasAIError(c, http.StatusBadRequest, "Invalid request: "+err.Error(), canvasAIErrorInvalidReq)
		return
	}
	prompt := strings.TrimSpace(req.Prompt)
	rawMaskURL := strings.TrimSpace(req.MaskURL)
	if rawMaskURL == "" {
		respondCanvasAIError(c, http.StatusBadRequest, "maskUrl is required for inpaint", canvasAIErrorMaskRequired)
		return
	}
	sourceImageURL, err := normalizeCanvasGenerationSourceURL(c, uid, req.ImageURL)
	if err != nil || sourceImageURL == "" {
		respondCanvasAIError(c, http.StatusBadRequest, "imageUrl is invalid", canvasAIErrorInvalidParams)
		return
	}
	maskURL, err := normalizeCanvasGenerationSourceURL(c, uid, rawMaskURL)
	if err != nil || maskURL == "" {
		respondCanvasAIError(c, http.StatusBadRequest, "maskUrl is invalid", canvasAIErrorInvalidParams)
		return
	}
	if prompt == "" {
		respondCanvasAIError(c, http.StatusBadRequest, "prompt is required", canvasAIErrorInvalidParams)
		return
	}

	modelID := normalizeCanvasImageModel(req.Model)
	modelCandidates, ok := authorizeCanvasModelCandidates(c, uid, modelID, req.ModelCandidates)
	if !ok {
		return
	}
	modelID = modelCandidates[0]
	referenceImages := buildCanvasReferenceImages(sourceImageURL)
	requestData := model.JSONMap{
		"model":           modelID,
		"prompt":          prompt + " Only modify the masked area and preserve everything outside the mask.",
		"referenceImages": referenceImages,
		"numberOfImages":  1,
		"imageUrl":        sourceImageURL,
	}
	if len(modelCandidates) > 1 {
		requestData["modelCandidates"] = modelCandidates
	}
	if stylePreset := strings.TrimSpace(req.StylePreset); stylePreset != "" {
		requestData["stylePreset"] = stylePreset
	}
	setCanvasOperationMeta(requestData, "inpaint", model.JSONMap{
		"maskUrl": maskURL,
	})

	inputFiles := map[string]interface{}{
		"imageUrl":        sourceImageURL,
		"referenceImages": referenceImages,
	}
	inputFiles["maskUrl"] = maskURL

	globals.Info(fmt.Sprintf("[Canvas AI] delegate inpaint task, uid: %d", uid))
	a.submitCanvasGenerationTask(c, uid, model.TOOL_IMAGE_GENERATOR, modelID, requestData, inputFiles)
}

func (a *CanvasApi) Mockup(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondCanvasAIError(c, http.StatusUnauthorized, "Unauthorized", canvasAIErrorUnauthorized)
		return
	}

	var req canvasMockupRequest
	if err := bindCanvasGenerationJSON(c, &req); err != nil {
		respondCanvasAIError(c, http.StatusBadRequest, "Invalid request: "+err.Error(), canvasAIErrorInvalidReq)
		return
	}
	sourceImageURL, err := normalizeCanvasGenerationSourceURL(c, uid, req.ImageURL)
	if err != nil || sourceImageURL == "" {
		respondCanvasAIError(c, http.StatusBadRequest, "imageUrl is invalid", canvasAIErrorInvalidParams)
		return
	}
	templateID := strings.TrimSpace(req.TemplateID)
	if templateID == "" {
		templateID = "generic-product"
	}

	modelID := normalizeCanvasImageModel(req.Model)
	modelCandidates, ok := authorizeCanvasModelCandidates(c, uid, modelID, req.ModelCandidates)
	if !ok {
		return
	}
	modelID = modelCandidates[0]
	referenceImages := buildCanvasReferenceImages(sourceImageURL)
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		prompt = fmt.Sprintf("Place the reference design into a realistic %s mockup. Preserve the original graphic details and use natural perspective and lighting.", templateID)
	}

	requestData := model.JSONMap{
		"model":           modelID,
		"prompt":          prompt,
		"referenceImages": referenceImages,
		"numberOfImages":  1,
		"imageUrl":        sourceImageURL,
	}
	setCanvasOperationMeta(requestData, "mockup", model.JSONMap{
		"templateId": templateID,
	})
	if len(modelCandidates) > 1 {
		requestData["modelCandidates"] = modelCandidates
	}
	if stylePreset := strings.TrimSpace(req.StylePreset); stylePreset != "" {
		requestData["stylePreset"] = stylePreset
	}

	inputFiles := map[string]interface{}{
		"imageUrl":        sourceImageURL,
		"referenceImages": referenceImages,
	}

	globals.Info(fmt.Sprintf("[Canvas AI] delegate mockup task, uid: %d, template: %s", uid, templateID))
	a.submitCanvasGenerationTask(c, uid, model.TOOL_IMAGE_GENERATOR, modelID, requestData, inputFiles)
}

func (a *CanvasApi) GetTaskStatus(c *gin.Context) {
	(&GeneratorApi{}).GetTaskStatus(c)
}

// EditText godoc
// @Summary Canvas AI Edit Text in Image
// @Description Replaces text within an image layout while preserving background. Returns a task ID for polling.
// @Tags Tools:Canvas (Pro)
// @Accept json
// @Produce json
// @Param request body struct{ImageURL string `json:"imageUrl" binding:"required"`; TargetText string `json:"targetText" binding:"required"`; ReplacementText string `json:"replacementText" binding:"required"`} true "Edit Text options"
// @Success 200 {object} response.Response{data=TaskSubmitResponse}
// @Router /api/tools/canvas/edit-text [post]
func (a *CanvasApi) EditText(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondCanvasAIError(c, http.StatusUnauthorized, "Unauthorized", canvasAIErrorUnauthorized)
		return
	}

	var req canvasEditTextRequest
	if err := bindCanvasGenerationJSON(c, &req); err != nil {
		respondCanvasAIError(c, http.StatusBadRequest, "Invalid request: "+err.Error(), canvasAIErrorInvalidReq)
		return
	}
	replacement := strings.TrimSpace(req.ReplacementText)
	target := strings.TrimSpace(req.TargetText)
	if strings.TrimSpace(req.ImageURL) == "" || target == "" || replacement == "" {
		respondCanvasAIError(c, http.StatusBadRequest, "imageUrl, targetText and replacementText are required", canvasAIErrorTextRequired)
		return
	}
	sourceImageURL, err := normalizeCanvasGenerationSourceURL(c, uid, req.ImageURL)
	if err != nil || sourceImageURL == "" {
		respondCanvasAIError(c, http.StatusBadRequest, "imageUrl is invalid", canvasAIErrorInvalidParams)
		return
	}

	modelID := normalizeCanvasImageModel(req.Model)
	modelCandidates, ok := authorizeCanvasModelCandidates(c, uid, modelID, req.ModelCandidates)
	if !ok {
		return
	}
	modelID = modelCandidates[0]
	referenceImages := buildCanvasReferenceImages(sourceImageURL)
	prompt := fmt.Sprintf("Replace the text '%s' with '%s' in the reference image. Keep typography style and layout consistency.", target, replacement)

	requestData := model.JSONMap{
		"model":           modelID,
		"prompt":          prompt,
		"referenceImages": referenceImages,
		"numberOfImages":  1,
		"imageUrl":        sourceImageURL,
	}
	setCanvasOperationMeta(requestData, "edit-text", model.JSONMap{
		"targetText":      target,
		"replacementText": replacement,
	})
	if len(modelCandidates) > 1 {
		requestData["modelCandidates"] = modelCandidates
	}
	inputFiles := map[string]interface{}{
		"imageUrl":        sourceImageURL,
		"referenceImages": referenceImages,
	}

	globals.Info(fmt.Sprintf("[Canvas AI] delegate edit-text task, uid: %d, target: %s", uid, target))
	a.submitCanvasGenerationTask(c, uid, model.TOOL_IMAGE_GENERATOR, modelID, requestData, inputFiles)
}

// SplitLayers godoc
// @Summary Canvas AI Edit Elements
// @Description Separates subjects from their background into multiple layers. Returns a task ID for polling.
// @Tags Tools:Canvas (Pro)
// @Accept json
// @Produce json
// @Param request body struct{ImageURL string `json:"imageUrl" binding:"required"`} true "Split Layers Options"
// @Success 200 {object} response.Response{data=TaskSubmitResponse}
// @Router /api/tools/canvas/split-layers [post]
func (a *CanvasApi) SplitLayers(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondCanvasAIError(c, http.StatusUnauthorized, "Unauthorized", canvasAIErrorUnauthorized)
		return
	}

	var req canvasSplitLayersRequest
	if err := bindCanvasGenerationJSON(c, &req); err != nil {
		respondCanvasAIError(c, http.StatusBadRequest, "Invalid request: "+err.Error(), canvasAIErrorInvalidReq)
		return
	}
	sourceImageURL, err := normalizeCanvasGenerationSourceURL(c, uid, req.ImageURL)
	if err != nil || sourceImageURL == "" {
		respondCanvasAIError(c, http.StatusBadRequest, "imageUrl is invalid", canvasAIErrorInvalidParams)
		return
	}

	modelID := normalizeCanvasImageModel(req.Model)
	modelCandidates, ok := authorizeCanvasModelCandidates(c, uid, modelID, req.ModelCandidates)
	if !ok {
		return
	}
	modelID = modelCandidates[0]
	referenceImages := buildCanvasReferenceImages(sourceImageURL)
	requestData := model.JSONMap{
		"model":           modelID,
		"prompt":          "Remove the background and keep the main foreground subject with clean edges.",
		"referenceImages": referenceImages,
		"numberOfImages":  1,
		"imageUrl":        sourceImageURL,
	}
	setCanvasOperationMeta(requestData, "split-layers", nil)
	if len(modelCandidates) > 1 {
		requestData["modelCandidates"] = modelCandidates
	}
	inputFiles := map[string]interface{}{
		"imageUrl":        sourceImageURL,
		"referenceImages": referenceImages,
	}

	globals.Info(fmt.Sprintf("[Canvas AI] delegate split-layers task, uid: %d", uid))
	a.submitCanvasGenerationTask(c, uid, model.TOOL_BACKGROUND_REMOVER, modelID, requestData, inputFiles)
}

// Upscale and RemoveBg — the pre-M3 image-ops handlers — live in
// canvas_image_ops_api.go. They flow through submitCanvasGenerationTask
// (above) but bypass the §10.1 editing pipeline's catalog-pricing path
// and authorizeCanvasModelCandidates because their primary→fallback
// model chain is hard-coded.
