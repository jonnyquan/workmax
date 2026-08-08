package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"server/globals"
	"server/model"
	"server/service"
	toolsService "server/service/tools"
	"server/utils"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// randomVideoSeed returns a positive int63 suitable for downstream provider seed parameters.
func randomVideoSeed() int64 {
	// Int63n(1<<31) keeps within common 32-bit seed ranges providers accept.
	return rand.Int63n(1<<31-1) + 1
}

type VideoGeneratorApi struct{}

const (
	maxVideoGenerateRequestBodyBytes = 1 << 20
	maxVideoCoverRequestBodyBytes    = 4096
	maxVideoPromptRunes              = 5000
	maxVideoNegativePromptRunes      = 2000
	maxVideoReferenceMediaItems      = 3
	maxVideoReferenceMediaURLLength  = 2048
	maxVideoReferenceMediaFieldRunes = 128
	maxVideoCoverTimestampSeconds    = 30 * 60
)

type VideoGenerateRequest struct {
	Model             string                `json:"model" binding:"required"`
	Prompt            string                `json:"prompt" binding:"required"`
	NegativePrompt    string                `json:"negativePrompt,omitempty"`
	AspectRatio       string                `json:"aspectRatio"`
	Resolution        string                `json:"resolution"`
	Duration          int                   `json:"duration"`
	GenerationMethod  string                `json:"generationMethod"`
	MotionMode        string                `json:"motionMode"`
	MotionOrientation string                `json:"motionOrientation"`
	StartFrame        string                `json:"startFrame,omitempty"`
	EndFrame          string                `json:"endFrame,omitempty"`
	ReferenceImages   []ReferenceImageInput `json:"referenceImages,omitempty"`
	GenerateAudio     *bool                 `json:"generateAudio,omitempty"`
	Seed              *int64                `json:"seed,omitempty"`
	ReferenceVideos   []VideoReferenceMedia `json:"referenceVideos,omitempty"`
	ReferenceAudios   []VideoReferenceMedia `json:"referenceAudios,omitempty"`
	// ParentRecordID is the upstream w_generation_record this submission
	// is continuing from. Set by the "continue from end frame" UI flow.
	// Server validates ownership (uid + tool_id match) before persisting
	// to prevent users from forging lineage to other people's clips.
	ParentRecordID *uint `json:"parentRecordId,omitempty"`
}

type VideoReferenceMedia struct {
	ID       string `json:"id,omitempty"`
	URL      string `json:"url"`
	MimeType string `json:"mimeType,omitempty"`
	FileName string `json:"fileName,omitempty"`
}

type VideoHistoryItem struct {
	ID           string                 `json:"id"`
	VideoURL     string                 `json:"videoUrl"`
	ThumbnailURL string                 `json:"thumbnailUrl,omitempty"`
	Params       VideoHistoryItemParams `json:"params"`
	CreatedAt    string                 `json:"createdAt"`
}

type VideoHistoryItemParams struct {
	Prompt            string              `json:"prompt"`
	NegativePrompt    string              `json:"negativePrompt,omitempty"`
	Model             string              `json:"model"`
	AspectRatio       string              `json:"aspectRatio,omitempty"`
	Resolution        string              `json:"resolution,omitempty"`
	Duration          int                 `json:"duration"`
	GenerationMethod  string              `json:"generationMethod,omitempty"`
	MotionMode        string              `json:"motionMode,omitempty"`
	MotionOrientation string              `json:"motionOrientation,omitempty"`
	GenerateAudio     *bool               `json:"generateAudio,omitempty"`
	Seed              *int64              `json:"seed,omitempty"`
	StartFrame        *VideoHistoryFrame  `json:"startFrame,omitempty"`
	EndFrame          *VideoHistoryFrame  `json:"endFrame,omitempty"`
	ReferenceImages   []VideoHistoryFrame `json:"referenceImages,omitempty"`
	ReferenceVideos   []VideoHistoryMedia `json:"referenceVideos,omitempty"`
	ReferenceAudios   []VideoHistoryMedia `json:"referenceAudios,omitempty"`
	// ParentRecordID, when present, marks this clip as a continuation of
	// another w_generation_record row. Frontend uses string ids
	// throughout; we format the uint as a decimal string for parity
	// with VideoHistoryItem.ID. Empty means organic (non-continuation)
	// generation, which is the default.
	ParentRecordID string `json:"parentRecordId,omitempty"`
}

type VideoHistoryFrame struct {
	ID         string `json:"id"`
	URL        string `json:"url"`
	PreviewURL string `json:"previewUrl"`
}

type VideoHistoryMedia struct {
	ID       string `json:"id,omitempty"`
	URL      string `json:"url"`
	MimeType string `json:"mimeType,omitempty"`
	FileName string `json:"fileName,omitempty"`
}

type storedVideoRecordParams struct {
	Model             string                   `json:"model,omitempty"`
	Prompt            string                   `json:"prompt,omitempty"`
	AspectRatio       string                   `json:"aspectRatio,omitempty"`
	Duration          json.RawMessage          `json:"duration,omitempty"`
	Resolution        string                   `json:"resolution,omitempty"`
	GenerationMethod  string                   `json:"generationMethod,omitempty"`
	MotionMode        string                   `json:"motionMode,omitempty"`
	MotionOrientation string                   `json:"motionOrientation,omitempty"`
	GenerateAudio     *bool                    `json:"generateAudio,omitempty"`
	Seed              *int64                   `json:"seed,omitempty"`
	StartFrame        json.RawMessage          `json:"startFrame,omitempty"`
	EndFrame          json.RawMessage          `json:"endFrame,omitempty"`
	Params            *storedVideoRecordParams `json:"params,omitempty"`
}

type storedVideoInputFiles struct {
	ReferenceImages []storedVideoReferenceImage `json:"referenceImages,omitempty"`
	ReferenceVideos []storedVideoReferenceMedia `json:"referenceVideos,omitempty"`
	ReferenceAudios []storedVideoReferenceMedia `json:"referenceAudios,omitempty"`
}

type storedVideoReferenceImage struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

type storedVideoReferenceMedia struct {
	ID       string `json:"id,omitempty"`
	URL      string `json:"url"`
	MimeType string `json:"mimeType,omitempty"`
	FileName string `json:"fileName,omitempty"`
}

type VideoCreditQuoteResponse struct {
	Model            string                               `json:"model"`
	Duration         int                                  `json:"duration"`
	Resolution       string                               `json:"resolution,omitempty"`
	Credits          int                                  `json:"credits"`
	PricingStatus    toolsService.VideoModelPricingStatus `json:"pricingStatus"`
	BillableDuration int                                  `json:"billableDuration,omitempty"`
	BillableRate     string                               `json:"billableRate,omitempty"`
}

type VideoCoverUpdateRequest struct {
	Timestamp float64 `json:"timestamp"`
	Reset     bool    `json:"reset"`
}

type VideoPricingCatalogItemResponse struct {
	Model            string                               `json:"model"`
	PricingStatus    toolsService.VideoModelPricingStatus `json:"pricingStatus"`
	Duration         int                                  `json:"duration"`
	Resolution       string                               `json:"resolution,omitempty"`
	Credits          int                                  `json:"credits"`
	BillableDuration int                                  `json:"billableDuration,omitempty"`
	BillableRate     string                               `json:"billableRate,omitempty"`
}

type VideoPricingCatalogResponse struct {
	FeaturedModel      string                            `json:"featuredModel"`
	FeaturedDuration   int                               `json:"featuredDuration"`
	FeaturedResolution string                            `json:"featuredResolution,omitempty"`
	FeaturedCredits    int                               `json:"featuredCredits"`
	Items              []VideoPricingCatalogItemResponse `json:"items"`
}

func videoToolIDs() []string {
	return []string{model.TOOL_VIDEO_GENERATOR}
}

func isAllowedVideoModel(modelID string) bool {
	switch modelID {
	case model.MINIMAX_VIDEO, model.VEO_31, model.VEO_31_FAST, model.KLING_2_6, model.SORA_2, model.SEEDANCE, model.SEEDANCE_2, model.SEEDANCE_2_FAST:
		return true
	default:
		return false
	}
}

func normalizeVideoDuration(raw int) int {
	if raw <= 0 {
		return 5
	}
	if raw > 30 {
		return 30
	}
	return raw
}

func (a *VideoGeneratorApi) GetModels(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data":    toolsService.ListVideoModelCapabilities(),
	})
}

func (a *VideoGeneratorApi) GetPricingCatalog(c *gin.Context) {
	catalog := toolsService.GetVideoPricingCatalog()
	items := make([]VideoPricingCatalogItemResponse, 0, len(catalog.Items))
	for _, item := range catalog.Items {
		items = append(items, VideoPricingCatalogItemResponse{
			Model:            item.Model,
			PricingStatus:    item.PricingStatus,
			Duration:         item.Duration,
			Resolution:       item.Resolution,
			Credits:          item.Credits,
			BillableDuration: item.BillableDuration,
			BillableRate:     item.BillableRate,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data": VideoPricingCatalogResponse{
			FeaturedModel:      catalog.FeaturedModel,
			FeaturedDuration:   catalog.FeaturedDuration,
			FeaturedResolution: catalog.FeaturedResolution,
			FeaturedCredits:    catalog.FeaturedCredits,
			Items:              items,
		},
	})
}

func (a *VideoGeneratorApi) GetCreditQuote(c *gin.Context) {
	modelID := strings.TrimSpace(c.DefaultQuery("model", model.KLING_2_6))
	if !isAllowedVideoModel(modelID) {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "Invalid video model", "INVALID_VIDEO_MODEL", false)
		return
	}

	duration, _ := strconv.Atoi(strings.TrimSpace(c.DefaultQuery("duration", "5")))
	resolution := strings.TrimSpace(c.Query("resolution"))
	aspectRatio := strings.TrimSpace(c.Query("aspectRatio"))
	motionMode := strings.TrimSpace(c.Query("motionMode"))
	motionOrientation := strings.TrimSpace(c.Query("motionOrientation"))
	hasStartFrame := c.Query("hasStartFrame") == "1" || strings.EqualFold(c.Query("hasStartFrame"), "true")
	hasEndFrame := c.Query("hasEndFrame") == "1" || strings.EqualFold(c.Query("hasEndFrame"), "true")

	quote, err := toolsService.QuoteVideoCredits(modelID, toolsService.VideoQuoteOptions{
		Duration:          duration,
		Resolution:        resolution,
		AspectRatio:       aspectRatio,
		MotionMode:        motionMode,
		MotionOrientation: motionOrientation,
		HasStartFrame:     hasStartFrame,
		HasEndFrame:       hasEndFrame,
	})
	if err != nil {
		respondGeneratorHTTPError(c, http.StatusBadRequest, err.Error(), "INVALID_VIDEO_QUOTE", false)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data": VideoCreditQuoteResponse{
			Model:            quote.Model,
			Duration:         quote.Duration,
			Resolution:       quote.Resolution,
			Credits:          quote.Credits,
			PricingStatus:    quote.PricingStatus,
			BillableDuration: quote.BillableDuration,
			BillableRate:     quote.BillableRate,
		},
	})
}

func (a *VideoGeneratorApi) Generate(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondGeneratorHTTPError(c, http.StatusUnauthorized, "Unauthorized", "UNAUTHORIZED", true)
		return
	}

	var req VideoGenerateRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxVideoGenerateRequestBodyBytes)
	if err := c.ShouldBindJSON(&req); err != nil {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "Invalid request", "INVALID_REQUEST", false)
		return
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "Prompt is required", "PROMPT_REQUIRED", false)
		return
	}
	if len([]rune(req.Prompt)) > maxVideoPromptRunes {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "Prompt is too long", "PROMPT_TOO_LONG", false)
		return
	}
	negativePrompt := strings.TrimSpace(req.NegativePrompt)
	if len([]rune(negativePrompt)) > maxVideoNegativePromptRunes {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "Negative prompt is too long", "NEGATIVE_PROMPT_TOO_LONG", false)
		return
	}

	modelID := strings.TrimSpace(req.Model)
	if !isAllowedVideoModel(modelID) {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "Invalid video model", "INVALID_VIDEO_MODEL", false)
		return
	}

	aspectRatio := strings.TrimSpace(req.AspectRatio)
	if aspectRatio == "" {
		aspectRatio = "16:9"
	}
	if _, ok := allowedAspectRatios[aspectRatio]; !ok {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "Invalid aspectRatio", "INVALID_ASPECT_RATIO", false)
		return
	}

	duration := normalizeVideoDuration(req.Duration)

	generationMethod := strings.TrimSpace(req.GenerationMethod)
	if generationMethod == "" {
		generationMethod = "frames"
	}

	motionMode := strings.TrimSpace(req.MotionMode)
	if motionMode == "" {
		motionMode = "std"
	}
	motionOrientation := strings.TrimSpace(req.MotionOrientation)
	if motionOrientation == "" {
		motionOrientation = "image"
	}
	resolution := strings.TrimSpace(req.Resolution)

	hasStartFrame := strings.TrimSpace(req.StartFrame) != ""
	hasEndFrame := strings.TrimSpace(req.EndFrame) != ""
	frameReferenceImages := make([]ReferenceImageInput, 0, 2)
	if startFrame := strings.TrimSpace(req.StartFrame); startFrame != "" {
		frameReferenceImages = append(frameReferenceImages, ReferenceImageInput{ID: "start-frame", URL: startFrame, Weight: 1})
	}
	if endFrame := strings.TrimSpace(req.EndFrame); endFrame != "" {
		frameReferenceImages = append(frameReferenceImages, ReferenceImageInput{ID: "end-frame", URL: endFrame, Weight: 0.8})
	}
	frameReferenceImages, err := sanitizeVideoGeneratorReferenceImages(frameReferenceImages, uid)
	if err != nil {
		respondGeneratorHTTPError(c, http.StatusBadRequest, err.Error(), "INVALID_REFERENCE_IMAGE", false)
		return
	}
	genericReferenceImages, err := sanitizeVideoGeneratorReferenceImages(req.ReferenceImages, uid)
	if err != nil {
		respondGeneratorHTTPError(c, http.StatusBadRequest, err.Error(), "INVALID_REFERENCE_IMAGE", false)
		return
	}
	normalizeVideoGenericReferenceImageIDs(genericReferenceImages)

	hasGenericReferenceImages := len(genericReferenceImages) > 0
	if hasGenericReferenceImages && !toolsService.SupportsVideoReferenceImages(modelID) {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "reference images are not supported for this model", "REFERENCE_IMAGE_NOT_SUPPORTED", false)
		return
	}
	maxGenericReferenceImages := toolsService.MaxGenericVideoReferenceImages(modelID)
	if hasGenericReferenceImages && maxGenericReferenceImages > 0 && len(genericReferenceImages) > maxGenericReferenceImages {
		respondGeneratorHTTPError(c, http.StatusBadRequest, fmt.Sprintf("this model supports at most %d generic reference images", maxGenericReferenceImages), "TOO_MANY_REFERENCE_IMAGES", false)
		return
	}
	if hasGenericReferenceImages && (hasStartFrame || hasEndFrame) && !supportsGenericReferencesWithExplicitFrames(modelID) {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "generic reference images cannot be combined with explicit start or end frames for this model", "REFERENCE_IMAGE_ROLE_UNSUPPORTED", false)
		return
	}
	hasBillableImageReference := hasStartFrame || hasEndFrame || hasGenericReferenceImages
	if err := toolsService.ValidateVideoRequest(modelID, toolsService.VideoQuoteOptions{
		Duration:          duration,
		Resolution:        resolution,
		AspectRatio:       aspectRatio,
		MotionMode:        motionMode,
		MotionOrientation: motionOrientation,
		HasStartFrame:     hasBillableImageReference,
		HasEndFrame:       hasEndFrame,
	}, generationMethod, hasBillableImageReference, hasEndFrame); err != nil {
		code := "INVALID_VIDEO_REQUEST"
		switch err.Error() {
		case "invalid duration":
			code = "INVALID_VIDEO_DURATION"
		case "invalid resolution":
			code = "INVALID_VIDEO_RESOLUTION"
		case "invalid aspectRatio":
			code = "INVALID_ASPECT_RATIO"
		case "invalid generationMethod":
			code = "INVALID_GENERATION_METHOD"
		case "invalid motionOrientation":
			code = "INVALID_MOTION_ORIENTATION"
		}
		respondGeneratorHTTPError(c, http.StatusBadRequest, err.Error(), code, false)
		return
	}

	normalized := toolsService.NormalizeVideoQuoteOptions(modelID, toolsService.VideoQuoteOptions{
		Duration:          duration,
		Resolution:        resolution,
		AspectRatio:       aspectRatio,
		MotionMode:        motionMode,
		MotionOrientation: motionOrientation,
		HasStartFrame:     hasBillableImageReference,
		HasEndFrame:       hasEndFrame,
	})
	duration = normalized.Duration
	resolution = normalized.Resolution
	aspectRatio = normalized.AspectRatio
	motionMode = normalized.MotionMode
	motionOrientation = normalized.MotionOrientation

	referenceImages := make([]ReferenceImageInput, 0, len(frameReferenceImages)+len(genericReferenceImages))
	referenceImages = append(referenceImages, frameReferenceImages...)
	referenceImages = append(referenceImages, genericReferenceImages...)

	referenceVideos, err := sanitizeVideoReferenceMedia(req.ReferenceVideos, "reference-videos", allowedReferenceVideoTypes, uid)
	if err != nil {
		respondGeneratorHTTPError(c, http.StatusBadRequest, err.Error(), "INVALID_REFERENCE_VIDEO", false)
		return
	}
	referenceAudios, err := sanitizeVideoReferenceMedia(req.ReferenceAudios, "reference-audios", allowedReferenceAudioTypes, uid)
	if err != nil {
		respondGeneratorHTTPError(c, http.StatusBadRequest, err.Error(), "INVALID_REFERENCE_AUDIO", false)
		return
	}
	if len(referenceVideos) > 0 && !toolsService.SupportsVideoReferenceMedia(modelID) {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "reference videos are not supported for this model", "REFERENCE_VIDEO_NOT_SUPPORTED", false)
		return
	}
	if len(referenceAudios) > 0 && !toolsService.SupportsAudioReferenceMedia(modelID) {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "reference audios are not supported for this model", "REFERENCE_AUDIO_NOT_SUPPORTED", false)
		return
	}

	capability, _ := toolsService.GetVideoModelCapabilities(modelID)
	nestedParams := model.JSONMap{
		"model":             modelID,
		"mediaType":         model.MediaTypeVideo,
		"duration":          strconv.Itoa(duration) + "s",
		"resolution":        resolution,
		"generationMethod":  generationMethod,
		"motionMode":        motionMode,
		"motionOrientation": motionOrientation,
		"startFrame":        strings.TrimSpace(req.StartFrame),
		"endFrame":          strings.TrimSpace(req.EndFrame),
	}
	if capability.SupportsAudioGeneration && req.GenerateAudio != nil {
		nestedParams["generateAudio"] = *req.GenerateAudio
	}
	var effectiveSeed int64
	if capability.SupportsSeed {
		if req.Seed != nil && *req.Seed > 0 {
			effectiveSeed = *req.Seed
		} else {
			effectiveSeed = randomVideoSeed()
			req.Seed = &effectiveSeed
		}
		nestedParams["seed"] = effectiveSeed
	}
	requestData := model.JSONMap{
		"model":           modelID,
		"prompt":          req.Prompt,
		"aspectRatio":     aspectRatio,
		"resolution":      resolution,
		"referenceImages": referenceImages,
		"params":          nestedParams,
	}
	if negativePrompt != "" {
		requestData["negativePrompt"] = negativePrompt
	}
	if len(referenceImages) > 0 {
		requestData["referenceImages"] = referenceImages
	}
	if len(referenceVideos) > 0 {
		requestData["referenceVideos"] = referenceVideos
	}
	if len(referenceAudios) > 0 {
		requestData["referenceAudios"] = referenceAudios
	}

	creditCost := toolsService.GetCreditCostByToolID(toolsService.CreditCostParams{
		ToolID: model.TOOL_VIDEO_GENERATOR,
		Model:  modelID,
		Params: requestData,
	})

	idempotencyKey := normalizeIdempotencyKey(c.GetHeader("X-Idempotency-Key"))
	if idempotencyKey == "" {
		idempotencyKey = normalizeIdempotencyKey(c.GetHeader("Idempotency-Key"))
	}

	// Validate continuation lineage: the parent must exist, belong to
	// this user, and be a video generation. Cross-tool lineage doesn't
	// make sense (image→video, drama→video) for the end-frame flow.
	var parentRecordID *uint
	if req.ParentRecordID != nil && *req.ParentRecordID > 0 {
		parentID := *req.ParentRecordID
		var parent model.GenerationRecord
		if err := globals.GraDBs["system"].
			Select("id, uid, tool_id").
			Where("id = ?", parentID).
			First(&parent).Error; err != nil {
			respondGeneratorHTTPError(c, http.StatusBadRequest, "Parent record not found", "INVALID_PARENT_RECORD_ID", false)
			return
		}
		if parent.UID != int(uid) || parent.ToolID != model.TOOL_VIDEO_GENERATOR {
			// Treat foreign / cross-tool parents as a 400 too — distinct
			// 403 would leak the existence of someone else's record.
			respondGeneratorHTTPError(c, http.StatusBadRequest, "Parent record not found", "INVALID_PARENT_RECORD_ID", false)
			return
		}
		parentRecordID = &parentID
	}

	inputFiles := map[string]interface{}{"referenceImages": referenceImages}
	if len(referenceVideos) > 0 {
		inputFiles["referenceVideos"] = referenceVideos
	}
	if len(referenceAudios) > 0 {
		inputFiles["referenceAudios"] = referenceAudios
	}

	(&GeneratorApi{}).submitGenerationTask(c, &submitTaskParams{
		UID:            uid,
		ModelID:        modelID,
		ToolID:         model.TOOL_VIDEO_GENERATOR,
		CreditCost:     creditCost,
		RequestData:    requestData,
		InputFiles:     inputFiles,
		IdempotencyKey: idempotencyKey,
		EffectiveSeed:  effectiveSeed,
		Origin:         model.GENERATION_ORIGIN_VIDEO_GEN,
		ParentRecordID: parentRecordID,
	})
}

func sanitizeVideoReferenceMedia(items []VideoReferenceMedia, kind string, allowedTypes map[string]string, uid uint) ([]VideoReferenceMedia, error) {
	if len(items) == 0 {
		return nil, nil
	}
	if len(items) > maxVideoReferenceMediaItems {
		return nil, fmt.Errorf("too many reference media items")
	}
	out := make([]VideoReferenceMedia, 0, len(items))
	for _, item := range items {
		mediaURL, err := sanitizeVideoReferenceURL(item.URL, kind, uid)
		if err != nil {
			return nil, err
		}
		if mediaURL == "" {
			continue
		}
		mimeType := strings.ToLower(strings.TrimSpace(item.MimeType))
		if mimeType != "" {
			if len([]rune(mimeType)) > maxVideoReferenceMediaFieldRunes {
				return nil, fmt.Errorf("reference media mimeType is too long")
			}
			if _, ok := allowedTypes[mimeType]; !ok {
				return nil, fmt.Errorf("invalid reference media mimeType")
			}
		}
		id, err := sanitizeVideoReferenceField(item.ID, true)
		if err != nil {
			return nil, fmt.Errorf("invalid reference media id")
		}
		fileName, err := sanitizeVideoReferenceField(item.FileName, false)
		if err != nil {
			return nil, fmt.Errorf("invalid reference media fileName")
		}
		out = append(out, VideoReferenceMedia{
			ID:       id,
			URL:      mediaURL,
			MimeType: mimeType,
			FileName: fileName,
		})
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func sanitizeVideoGeneratorReferenceImages(items []ReferenceImageInput, uid uint) ([]ReferenceImageInput, error) {
	images, err := resolveGeneratorReferenceImages(items, uid)
	if err != nil {
		return nil, err
	}
	out := make([]ReferenceImageInput, 0, len(images))
	for _, item := range images {
		if item.AssetID == 0 {
			imageURL, err := sanitizeVideoReferenceImageURL(item.URL, uid)
			if err != nil {
				return nil, err
			}
			item.URL = imageURL
		}
		out = append(out, item)
	}
	return out, nil
}

func normalizeVideoGenericReferenceImageIDs(items []ReferenceImageInput) {
	for i := range items {
		id := strings.TrimSpace(items[i].ID)
		if id == "" || isReservedVideoFrameReferenceID(id) {
			items[i].ID = fmt.Sprintf("reference-image-%d", i+1)
		}
	}
}

func isReservedVideoFrameReferenceID(id string) bool {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "start-frame", "video-start-frame", "end-frame", "video-end-frame":
		return true
	default:
		return false
	}
}

func supportsGenericReferencesWithExplicitFrames(modelID string) bool {
	return toolsService.SupportsGenericVideoReferencesWithFrames(strings.TrimSpace(modelID))
}

func sanitizeVideoReferenceImageURL(raw string, uid uint) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid reference image url")
	}
	if parsed.IsAbs() && !isAllowedVideoReferenceMediaHost(parsed.Hostname()) {
		return "", fmt.Errorf("reference image must come from uploaded assets")
	}
	pathValue := parsed.Path
	if pathValue == "" {
		pathValue = parsed.EscapedPath()
	}
	if !isVideoGeneratorReferenceImagePath(pathValue) {
		return "", fmt.Errorf("reference image must come from uploaded assets")
	}
	if !referenceURLPathMatchesUID(pathValue, uid) {
		return "", fmt.Errorf("reference image does not belong to the current user")
	}
	return trimmed, nil
}

func sanitizeVideoReferenceURL(raw string, kind string, uid uint) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if len(trimmed) > maxVideoReferenceMediaURLLength || hasControlRune(trimmed) {
		return "", fmt.Errorf("invalid reference media url")
	}
	if strings.HasPrefix(trimmed, "//") {
		return "", fmt.Errorf("invalid reference media url")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid reference media url")
	}
	if parsed.IsAbs() {
		scheme := strings.ToLower(parsed.Scheme)
		if scheme != "http" && scheme != "https" {
			return "", fmt.Errorf("invalid reference media url scheme")
		}
		if parsed.Host == "" || parsed.User != nil {
			return "", fmt.Errorf("invalid reference media url")
		}
		if !isAllowedVideoReferenceMediaHost(parsed.Hostname()) {
			return "", fmt.Errorf("reference media must come from uploaded assets")
		}
	} else if !strings.HasPrefix(trimmed, "/") {
		return "", fmt.Errorf("invalid reference media url")
	}

	pathValue := parsed.Path
	if pathValue == "" {
		pathValue = parsed.EscapedPath()
	}
	if !strings.Contains(pathValue, "/"+kind+"/uid/") || hasUnsafeURLPath(pathValue) {
		return "", fmt.Errorf("reference media must come from uploaded assets")
	}
	if !referenceURLPathMatchesUID(pathValue, uid) {
		return "", fmt.Errorf("reference media does not belong to the current user")
	}
	return trimmed, nil
}

func isVideoGeneratorReferenceImagePath(pathValue string) bool {
	if hasUnsafeURLPath(pathValue) {
		return false
	}
	return strings.HasPrefix(pathValue, "/uploads/references/uid/") ||
		strings.HasPrefix(pathValue, "/uploads/generations/reference-images/") ||
		strings.HasPrefix(pathValue, "/uploads/reference-images/") ||
		strings.HasPrefix(pathValue, "/uploads/canvas/uid/")
}

func referenceURLPathMatchesUID(pathValue string, uid uint) bool {
	if uid == 0 {
		return false
	}
	segments := strings.Split(pathValue, "/")
	want := strconv.FormatUint(uint64(uid), 10)
	for i := 0; i < len(segments)-1; i++ {
		if segments[i] == "uid" {
			return segments[i+1] == want
		}
	}
	// New generation submissions must use modern, owner-scoped upload
	// paths. Legacy history rows are read-only and should not be reused as
	// fresh reference input without first re-uploading under the caller uid.
	return false
}

func isAllowedVideoReferenceMediaHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	for _, rawBase := range []string{
		globals.GraConf.System.BackendURL,
		globals.GraConf.System.FrontendURL,
		globals.GraConf.Generator.Storage.Local.URLPrefix,
		globals.GraConf.Generator.Storage.R2.CDNUrl,
		globals.GraConf.Generator.Storage.R2.PublicURL,
	} {
		parsed, err := url.Parse(strings.TrimSpace(rawBase))
		if err != nil || parsed == nil {
			continue
		}
		if strings.EqualFold(host, strings.TrimSpace(parsed.Hostname())) {
			return true
		}
	}
	return false
}

func sanitizeVideoReferenceField(raw string, allowTokenOnly bool) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if len([]rune(trimmed)) > maxVideoReferenceMediaFieldRunes || hasControlRune(trimmed) {
		return "", fmt.Errorf("invalid reference media field")
	}
	if strings.ContainsAny(trimmed, `/\`) {
		return "", fmt.Errorf("invalid reference media field")
	}
	if !allowTokenOnly {
		return trimmed, nil
	}
	for _, r := range trimmed {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			continue
		}
		return "", fmt.Errorf("invalid reference media field")
	}
	return trimmed, nil
}

func hasUnsafeURLPath(pathValue string) bool {
	for _, segment := range strings.Split(pathValue, "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

func hasControlRune(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func (a *VideoGeneratorApi) GetTaskStatus(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondGeneratorHTTPError(c, http.StatusUnauthorized, "Unauthorized", "UNAUTHORIZED", true)
		return
	}
	taskID := c.Param("taskId")
	if taskID == "" {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "Task ID is required", "TASK_ID_REQUIRED", false)
		return
	}
	task, err := toolsService.GetTaskByID(taskID)
	if err != nil {
		respondGeneratorHTTPError(c, http.StatusNotFound, "Task not found", "TASK_NOT_FOUND", false)
		return
	}
	if task.UID != int(uid) {
		respondGeneratorHTTPError(c, http.StatusForbidden, "Access denied", "TASK_ACCESS_DENIED", false)
		return
	}
	if task.ToolID != model.TOOL_VIDEO_GENERATOR {
		respondGeneratorHTTPError(c, http.StatusNotFound, "Task not found", "TASK_NOT_FOUND", false)
		return
	}
	(&GeneratorApi{}).GetTaskStatus(c)
}

func (a *VideoGeneratorApi) CancelTask(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondGeneratorHTTPError(c, http.StatusUnauthorized, "Unauthorized", "UNAUTHORIZED", true)
		return
	}
	taskID := c.Param("taskId")
	if taskID == "" {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "Task ID is required", "TASK_ID_REQUIRED", false)
		return
	}
	task, err := toolsService.GetTaskByID(taskID)
	if err != nil || task.UID != int(uid) || task.ToolID != model.TOOL_VIDEO_GENERATOR {
		respondGeneratorHTTPError(c, http.StatusNotFound, "Task not found", "TASK_NOT_FOUND", false)
		return
	}
	(&GeneratorApi{}).CancelTask(c)
}

func (a *VideoGeneratorApi) GetActiveTasks(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondGeneratorHTTPError(c, http.StatusUnauthorized, "Unauthorized", "UNAUTHORIZED", true)
		return
	}

	tasks, err := toolsService.GetActiveTasksByUIDAndTool(int(uid), videoToolIDs())
	if err != nil {
		respondGeneratorHTTPError(c, http.StatusInternalServerError, "Failed to get active tasks", "ACTIVE_TASKS_FETCH_FAILED", false)
		return
	}

	result := make([]map[string]interface{}, 0, len(tasks))
	for _, task := range tasks {
		result = append(result, toolsService.TaskToResponseDTOWithContext(c.Request.Context(), &task))
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "success", "data": result})
}

func (a *VideoGeneratorApi) GetTasks(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondGeneratorHTTPError(c, http.StatusUnauthorized, "Unauthorized", "UNAUTHORIZED", true)
		return
	}

	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	tasks, err := toolsService.GetRecentTasksByUIDAndTool(int(uid), videoToolIDs(), limit)
	if err != nil {
		respondGeneratorHTTPError(c, http.StatusInternalServerError, "Failed to get tasks", "TASKS_FETCH_FAILED", false)
		return
	}

	result := make([]map[string]interface{}, 0, len(tasks))
	for _, task := range tasks {
		result = append(result, toolsService.TaskToResponseDTOWithContext(c.Request.Context(), &task))
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "success", "data": result})
}

func (a *VideoGeneratorApi) GetHistory(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondGeneratorHTTPError(c, http.StatusUnauthorized, "Unauthorized", "UNAUTHORIZED", true)
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	modelFilter := c.Query("model")
	withTotal := c.DefaultQuery("withTotal", "1") != "0"
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}

	genService := service.GroupServiceApp.ToolServiceGroup.GeneratorService
	records, total, err := genService.GetGenerationHistoryByToolIDAndModel(uid, page, limit, videoToolIDs(), modelFilter, withTotal)
	if err != nil {
		respondGeneratorHTTPError(c, http.StatusInternalServerError, "Failed to get history", "HISTORY_FETCH_FAILED", false)
		return
	}

	items := make([]VideoHistoryItem, 0, len(records))
	for _, record := range records {
		item := buildVideoHistoryItem(c.Request.Context(), record)
		if item.VideoURL == "" {
			continue
		}
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].CreatedAt > items[j].CreatedAt })

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

func (a *VideoGeneratorApi) DeleteHistoryRecord(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondGeneratorHTTPError(c, http.StatusUnauthorized, "Unauthorized", "UNAUTHORIZED", true)
		return
	}

	recordID, err := strconv.Atoi(strings.TrimSpace(c.Param("id")))
	if err != nil || recordID <= 0 {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "Record ID is required", "RECORD_ID_REQUIRED", false)
		return
	}

	var record model.GenerationRecord
	if err := globals.GraDBs["system"].
		Where("id = ? AND uid = ? AND tool_id = ?", recordID, uid, model.TOOL_VIDEO_GENERATOR).
		First(&record).Error; err != nil {
		respondGeneratorHTTPError(c, http.StatusNotFound, "Record not found", "RECORD_NOT_FOUND", false)
		return
	}

	if err := globals.GraDBs["system"].Transaction(func(tx *gorm.DB) error {
		return (&toolsService.GenerationObjectService{}).DeleteGenerationRecordsWithAssets(c.Request.Context(), tx, []model.GenerationRecord{record})
	}); err != nil {
		respondGeneratorHTTPError(c, http.StatusInternalServerError, "Failed to delete record", "RECORD_DELETE_FAILED", false)
		return
	}

	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "success", "data": gin.H{"id": recordID}})
}

func (a *VideoGeneratorApi) UpdateHistoryCover(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondGeneratorHTTPError(c, http.StatusUnauthorized, "Unauthorized", "UNAUTHORIZED", true)
		return
	}

	recordID, err := strconv.Atoi(strings.TrimSpace(c.Param("id")))
	if err != nil || recordID <= 0 {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "Record ID is required", "RECORD_ID_REQUIRED", false)
		return
	}

	var req VideoCoverUpdateRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxVideoCoverRequestBodyBytes)
	if err := c.ShouldBindJSON(&req); err != nil {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "Invalid request", "INVALID_REQUEST", false)
		return
	}

	var record model.GenerationRecord
	if err := globals.GraDBs["system"].
		Where("id = ? AND uid = ? AND tool_id = ?", recordID, uid, model.TOOL_VIDEO_GENERATOR).
		First(&record).Error; err != nil {
		respondGeneratorHTTPError(c, http.StatusNotFound, "Record not found", "RECORD_NOT_FOUND", false)
		return
	}

	item := buildVideoHistoryItem(c.Request.Context(), record)
	if strings.TrimSpace(item.VideoURL) == "" {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "Video result is unavailable", "VIDEO_NOT_FOUND", false)
		return
	}

	var task model.GenerationTask
	if err := globals.GraDBs["system"].
		Where("record_id = ? AND uid = ? AND tool_id = ?", record.Id, uid, model.TOOL_VIDEO_GENERATOR).
		Order("id DESC").
		First(&task).Error; err != nil {
		respondGeneratorHTTPError(c, http.StatusNotFound, "Task not found", "TASK_NOT_FOUND", false)
		return
	}

	genService := service.GroupServiceApp.ToolServiceGroup.GeneratorService
	var thumbnailURL string
	if req.Reset {
		thumbnailURL, err = genService.GenerateAndSaveVideoThumbnail(c.Request.Context(), task.TaskID, item.VideoURL)
	} else {
		if math.IsNaN(req.Timestamp) || math.IsInf(req.Timestamp, 0) || req.Timestamp < 0 || req.Timestamp > maxVideoCoverTimestampSeconds {
			respondGeneratorHTTPError(c, http.StatusBadRequest, "Invalid timestamp", "INVALID_TIMESTAMP", false)
			return
		}
		thumbnailURL, err = genService.GenerateAndSaveVideoThumbnailAtTimestamp(c.Request.Context(), task.TaskID, item.VideoURL, req.Timestamp)
	}
	if err != nil {
		respondGeneratorHTTPError(c, http.StatusInternalServerError, err.Error(), "COVER_UPDATE_FAILED", false)
		return
	}

	if err := globals.GraDBs["system"].Transaction(func(tx *gorm.DB) error {
		recordMetadata := parseJSONStringMap(record.ResultMetadata)
		recordMetadata["thumbnailUrl"] = thumbnailURL
		recordMetadataJSON, err := json.Marshal(recordMetadata)
		if err != nil {
			return err
		}
		if err := tx.Model(&model.GenerationRecord{}).
			Where("id = ?", record.Id).
			Update("result_metadata", string(recordMetadataJSON)).Error; err != nil {
			return err
		}

		var relatedTasks []model.GenerationTask
		if err := tx.
			Where("record_id = ? AND uid = ? AND tool_id = ?", record.Id, uid, model.TOOL_VIDEO_GENERATOR).
			Find(&relatedTasks).Error; err != nil {
			return err
		}
		for _, relatedTask := range relatedTasks {
			resultData := relatedTask.ResultData
			if resultData == nil {
				resultData = model.JSONMap{}
			}
			resultData["thumbnailUrl"] = thumbnailURL
			resultMetadata := extractResultMetadataMap(resultData["resultMetadata"])
			resultMetadata["thumbnailUrl"] = thumbnailURL
			resultData["resultMetadata"] = resultMetadata
			if err := tx.Model(&model.GenerationTask{}).
				Where("id = ?", relatedTask.Id).
				Update("result_data", resultData).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		respondGeneratorHTTPError(c, http.StatusInternalServerError, "Failed to persist cover", "COVER_PERSIST_FAILED", false)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data": gin.H{
			"id":           strconv.Itoa(recordID),
			"thumbnailUrl": thumbnailURL,
		},
	})
}

// DownloadHistoryRecord resolves a short-lived, presigned download URL for the
// requested history record's video asset and returns it as JSON. Presigned URLs
// include a Content-Disposition header that instructs the browser to save the
// file with a stable filename instead of playing it inline.
func (a *VideoGeneratorApi) DownloadHistoryRecord(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondGeneratorHTTPError(c, http.StatusUnauthorized, "Unauthorized", "UNAUTHORIZED", true)
		return
	}

	recordID, err := strconv.Atoi(strings.TrimSpace(c.Param("id")))
	if err != nil || recordID <= 0 {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "Record ID is required", "RECORD_ID_REQUIRED", false)
		return
	}

	var record model.GenerationRecord
	if err := globals.GraDBs["system"].
		Where("id = ? AND uid = ? AND tool_id = ?", recordID, uid, model.TOOL_VIDEO_GENERATOR).
		First(&record).Error; err != nil {
		respondGeneratorHTTPError(c, http.StatusNotFound, "Record not found", "RECORD_NOT_FOUND", false)
		return
	}

	urls := parseJSONStringSlice(record.ResultImages)
	videoURL := pickVideoURL(urls)
	if videoURL == "" && len(urls) > 0 {
		videoURL = strings.TrimSpace(urls[0])
	}
	if videoURL == "" {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "Video result is unavailable", "VIDEO_NOT_FOUND", false)
		return
	}

	filename := buildVideoDownloadFilename(&record, videoURL)
	resolved, err := (&toolsService.GenerationObjectService{}).ResolveSignedDownloadURL(
		c.Request.Context(),
		record.Id,
		"",
		"video",
		videoURL,
		filename,
		0,
	)
	if err != nil || strings.TrimSpace(resolved) == "" {
		resolved = videoURL
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data": gin.H{
			"url":      resolved,
			"filename": filename,
		},
	})
}

// buildVideoDownloadFilename produces a stable, filesystem-safe filename for a
// video download, preserving the original extension when possible.
func buildVideoDownloadFilename(record *model.GenerationRecord, videoURL string) string {
	ext := ".mp4"
	trimmed := strings.TrimSpace(videoURL)
	if trimmed != "" {
		if idx := strings.IndexAny(trimmed, "?#"); idx >= 0 {
			trimmed = trimmed[:idx]
		}
		if slash := strings.LastIndex(trimmed, "/"); slash >= 0 {
			trimmed = trimmed[slash+1:]
		}
		if dot := strings.LastIndex(trimmed, "."); dot > 0 && dot < len(trimmed)-1 {
			candidate := strings.ToLower(trimmed[dot:])
			switch candidate {
			case ".mp4", ".mov", ".webm", ".mkv", ".m4v":
				ext = candidate
			}
		}
	}

	recordID := uint(0)
	if record != nil {
		recordID = record.Id
	}
	if recordID > 0 {
		return "workmax-video-" + strconv.FormatUint(uint64(recordID), 10) + ext
	}
	return "workmax-video-" + strconv.FormatInt(time.Now().Unix(), 10) + ext
}

func buildVideoHistoryItem(ctx context.Context, record model.GenerationRecord) VideoHistoryItem {
	(&toolsService.GenerationObjectService{}).ResolveRecordDownloadURLs(ctx, &record)

	stored := decodeStoredVideoParams(record.Params)
	if stored.Params != nil {
		stored.mergeFallback(*stored.Params)
	}

	metadata := parseJSONStringMap(record.ResultMetadata)
	urls := parseJSONStringSlice(record.ResultImages)
	videoURL := pickVideoURL(urls)
	if videoURL == "" && len(urls) > 0 {
		videoURL = strings.TrimSpace(urls[0])
	}
	thumbnailURL := extractStringMapValue(metadata, "thumbnailUrl")

	out := VideoHistoryItemParams{
		Prompt:            record.Prompt,
		NegativePrompt:    record.NegativePrompt,
		Model:             record.Model,
		AspectRatio:       record.AspectRatio,
		Duration:          parseDurationRawMessage(stored.Duration),
		Resolution:        stored.Resolution,
		GenerationMethod:  stored.GenerationMethod,
		MotionMode:        stored.MotionMode,
		MotionOrientation: stored.MotionOrientation,
		GenerateAudio:     stored.GenerateAudio,
		Seed:              stored.Seed,
		StartFrame:        decodeVideoFrame(stored.StartFrame, "start-frame"),
		EndFrame:          decodeVideoFrame(stored.EndFrame, "end-frame"),
	}
	if record.ParentRecordID != nil && *record.ParentRecordID > 0 {
		out.ParentRecordID = strconv.FormatUint(uint64(*record.ParentRecordID), 10)
	}
	if out.Model == "" {
		out.Model = stored.Model
	}

	storedFiles := decodeStoredInputFiles(record.InputFiles)
	for _, ref := range storedFiles.ReferenceImages {
		id := strings.TrimSpace(ref.ID)
		urlValue := strings.TrimSpace(ref.URL)
		if urlValue == "" {
			continue
		}
		frame := &VideoHistoryFrame{ID: id, URL: urlValue, PreviewURL: urlValue}
		switch id {
		case "start-frame", "video-start-frame":
			out.StartFrame = frame
		case "end-frame", "video-end-frame":
			out.EndFrame = frame
		default:
			out.ReferenceImages = append(out.ReferenceImages, *frame)
		}
	}
	if media := convertStoredReferenceMedia(storedFiles.ReferenceVideos); len(media) > 0 {
		out.ReferenceVideos = media
	}
	if media := convertStoredReferenceMedia(storedFiles.ReferenceAudios); len(media) > 0 {
		out.ReferenceAudios = media
	}

	return VideoHistoryItem{
		ID:           strconv.FormatUint(uint64(record.Id), 10),
		VideoURL:     videoURL,
		ThumbnailURL: thumbnailURL,
		Params:       out,
		CreatedAt:    record.CreatedAt.Format(time.RFC3339),
	}
}

func decodeStoredVideoParams(raw string) storedVideoRecordParams {
	var stored storedVideoRecordParams
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return stored
	}
	_ = json.Unmarshal([]byte(trimmed), &stored)
	return stored
}

func decodeStoredInputFiles(raw string) storedVideoInputFiles {
	var files storedVideoInputFiles
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return files
	}
	_ = json.Unmarshal([]byte(trimmed), &files)
	return files
}

func convertStoredReferenceMedia(items []storedVideoReferenceMedia) []VideoHistoryMedia {
	if len(items) == 0 {
		return nil
	}
	out := make([]VideoHistoryMedia, 0, len(items))
	for _, item := range items {
		urlValue := strings.TrimSpace(item.URL)
		if urlValue == "" {
			continue
		}
		out = append(out, VideoHistoryMedia{
			ID:       strings.TrimSpace(item.ID),
			URL:      urlValue,
			MimeType: strings.TrimSpace(item.MimeType),
			FileName: strings.TrimSpace(item.FileName),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// mergeFallback fills unset fields on s with values from fallback.
func (s *storedVideoRecordParams) mergeFallback(fallback storedVideoRecordParams) {
	if s.Model == "" {
		s.Model = fallback.Model
	}
	if len(s.Duration) == 0 {
		s.Duration = fallback.Duration
	}
	if s.Resolution == "" {
		s.Resolution = fallback.Resolution
	}
	if s.GenerationMethod == "" {
		s.GenerationMethod = fallback.GenerationMethod
	}
	if s.MotionMode == "" {
		s.MotionMode = fallback.MotionMode
	}
	if s.MotionOrientation == "" {
		s.MotionOrientation = fallback.MotionOrientation
	}
	if s.GenerateAudio == nil {
		s.GenerateAudio = fallback.GenerateAudio
	}
	if s.Seed == nil {
		s.Seed = fallback.Seed
	}
	if len(s.StartFrame) == 0 {
		s.StartFrame = fallback.StartFrame
	}
	if len(s.EndFrame) == 0 {
		s.EndFrame = fallback.EndFrame
	}
}

func decodeVideoFrame(raw json.RawMessage, defaultID string) *VideoHistoryFrame {
	if len(raw) == 0 {
		return nil
	}
	// Try object shape first.
	var obj struct {
		ID         string `json:"id"`
		URL        string `json:"url"`
		PreviewURL string `json:"previewUrl"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		urlValue := strings.TrimSpace(obj.URL)
		if urlValue == "" {
			return nil
		}
		id := obj.ID
		if id == "" {
			id = defaultID
		}
		previewURL := obj.PreviewURL
		if previewURL == "" {
			previewURL = urlValue
		}
		return &VideoHistoryFrame{ID: id, URL: urlValue, PreviewURL: previewURL}
	}
	// Fall back to plain string URL.
	var urlStr string
	if err := json.Unmarshal(raw, &urlStr); err == nil {
		urlValue := strings.TrimSpace(urlStr)
		if urlValue == "" {
			return nil
		}
		return &VideoHistoryFrame{ID: defaultID, URL: urlValue, PreviewURL: urlValue}
	}
	return nil
}

func parseDurationRawMessage(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 5
	}
	var asInt int
	if err := json.Unmarshal(raw, &asInt); err == nil && asInt > 0 {
		return asInt
	}
	var asFloat float64
	if err := json.Unmarshal(raw, &asFloat); err == nil && asFloat > 0 {
		return int(asFloat)
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return parseDurationSeconds(asString)
	}
	return 5
}

func parseJSONStringMap(raw string) map[string]interface{} {
	result := map[string]interface{}{}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return result
	}
	_ = json.Unmarshal([]byte(trimmed), &result)
	return result
}

func parseJSONStringSlice(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	var result []string
	if err := json.Unmarshal([]byte(trimmed), &result); err == nil {
		return result
	}
	return nil
}

func extractStringMapValue(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	value, ok := m[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		return strconv.Itoa(typed)
	default:
		return ""
	}
}

func extractResultMetadataMap(raw interface{}) map[string]interface{} {
	switch typed := raw.(type) {
	case map[string]interface{}:
		return typed
	case model.JSONMap:
		return map[string]interface{}(typed)
	case string:
		return parseJSONStringMap(typed)
	case []byte:
		return parseJSONStringMap(string(typed))
	default:
		return map[string]interface{}{}
	}
}

func pickVideoURL(urls []string) string {
	for _, item := range urls {
		trimmed := strings.TrimSpace(item)
		lower := strings.ToLower(trimmed)
		if strings.HasSuffix(lower, ".mp4") || strings.HasSuffix(lower, ".webm") || strings.HasSuffix(lower, ".mov") {
			return trimmed
		}
	}
	return ""
}

func parseDurationSeconds(raw string) int {
	trimmed := strings.TrimSpace(strings.TrimSuffix(raw, "s"))
	if trimmed == "" {
		return 5
	}
	value, err := strconv.Atoi(trimmed)
	if err != nil || value <= 0 {
		return 5
	}
	return value
}
