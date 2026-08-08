package tools

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"server/config"
	"server/globals"
	"server/model"
	assetLedgerService "server/service/assetledger"
	globalAssetService "server/service/globalasset"
	storageService "server/service/storage"
	"server/service/tools/canvas"
	"server/service/tools/provider"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "golang.org/x/image/webp"
	"gorm.io/gorm"
)

type GeneratorService struct{}

type remoteAssetBody struct {
	io.Reader
	closer io.Closer
}

func (r *remoteAssetBody) Close() error {
	if r == nil || r.closer == nil {
		return nil
	}
	return r.closer.Close()
}

type closeFunc func() error

func (f closeFunc) Close() error {
	return f()
}

type remoteAssetStream struct {
	Body        io.ReadCloser
	ContentType string
	Size        int64
}

type maxBytesReadCloser struct {
	reader    io.Reader
	closer    io.Closer
	remaining int64
	err       error
}

func newMaxBytesReadCloser(reader io.Reader, closer io.Closer, maxSize int64) io.ReadCloser {
	if maxSize <= 0 {
		return &remoteAssetBody{Reader: reader, closer: closer}
	}
	return &maxBytesReadCloser{
		reader:    reader,
		closer:    closer,
		remaining: maxSize,
	}
}

func (r *maxBytesReadCloser) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	if len(p) == 0 {
		return 0, nil
	}

	limit := len(p)
	if int64(limit) > r.remaining+1 {
		limit = int(r.remaining + 1)
	}
	n, err := r.reader.Read(p[:limit])
	if n > 0 {
		r.remaining -= int64(n)
		if r.remaining < 0 {
			r.err = fmt.Errorf("remote asset exceeds max allowed size")
			return n, r.err
		}
	}
	if err != nil {
		r.err = err
	}
	return n, err
}

func (r *maxBytesReadCloser) Close() error {
	if r == nil || r.closer == nil {
		return nil
	}
	return r.closer.Close()
}

func exceedsConfiguredRemoteAssetSize(contentLength int64, maxSize int64) bool {
	return maxSize > 0 && contentLength > maxSize
}

func providerTypeByModel(modelID string) string {
	switch model.NormalizeModelID(modelID) {
	case model.NANO_BANANA_PRO:
		return model.ProviderTypeGemini
	case model.NANO_BANANA_2:
		return model.ProviderTypeGemini
	case model.NANO_BANANA:
		return model.ProviderTypeGemini
	case model.GPT_IMAGE_2:
		return model.ProviderTypeOpenAI
	case model.KLING_2_6:
		return model.ProviderTypeKling
	case model.SORA_2:
		return model.ProviderTypeSora
	case model.VEO_31, model.VEO_31_FAST:
		return model.ProviderTypeGemini
	case model.SEEDANCE:
		return model.ProviderTypeSeedance
	case model.SEEDANCE_2, model.SEEDANCE_2_FAST:
		return model.ProviderTypeSeedance
	case model.MINIMAX_VIDEO:
		return model.ProviderTypeMinimax
	default:
		return ""
	}
}

// ErrCancelUnsupported signals that the caller must not assume the upstream
// job was stopped — either we have no provider job id yet, or the resolved
// provider does not implement CancelableProvider. Callers should refuse the
// cancel (e.g. respond 409) instead of silently marking the task Cancelled
// and refunding credits while the provider keeps burning GPU.
var ErrCancelUnsupported = errors.New("remote cancel unsupported")

func CancelRemoteGenerationTask(ctx context.Context, task *model.GenerationTask) error {
	if task == nil {
		return fmt.Errorf("task is nil")
	}
	jobID := ""
	if task.ResultData != nil {
		if v, ok := task.ResultData["providerJobId"].(string); ok {
			jobID = strings.TrimSpace(v)
		}
	}
	if jobID == "" {
		return ErrCancelUnsupported
	}

	mediaType := model.MediaTypeImage
	if task.RequestData != nil {
		if v := strings.TrimSpace(readStringFromJSONMapWithParams(task.RequestData, "mediaType")); v != "" {
			mediaType = v
		}
	}
	targetProviderType := providerTypeByModel(task.Model)
	p := provider.GetDefaultProviderForModel(mediaType, targetProviderType, task.Model, "")
	if p == nil && targetProviderType != "" {
		p = provider.GetDefaultProviderByMediaType(mediaType, "", "")
	}
	if p == nil {
		return fmt.Errorf("provider not available for cancel")
	}
	cancelable, ok := p.(provider.CancelableProvider)
	if !ok {
		return ErrCancelUnsupported
	}
	return cancelable.Cancel(ctx, jobID)
}

// GenerateImageRequest 生成图片请求
type GenerateImageRequest struct {
	UID             uint
	ToolID          string
	Model           string
	Lora            string
	Prompt          string
	NegativePrompt  string
	AspectRatio     string
	StylePreset     string
	Resolution      string
	NumberOfImages  int
	Steps           int
	CFGScale        float64
	Sampler         string
	Seed            int64
	Upscale         bool
	ReferenceImages []model.ReferenceImageParam
	ReferenceVideos []model.ReferenceMediaParam
	ReferenceAudios []model.ReferenceMediaParam
	RawRequestData  model.JSONMap
	CreditCost      int // 由调用方预先计算的费用，>0 时直接使用
	SkipRecord      bool
	TaskID          string
	OnProgress      func(progress int, meta map[string]interface{})
	OnProviderJob   func(jobID string, meta map[string]interface{})
	// AssetBindings, when set, feeds canvas.InjectAssetContext so
	// character prompt suffixes and avatar reference images get merged
	// into the provider request. Nil for non-canvas callers.
	AssetBindings *canvas.AssetBinding
	// Origin identifies which product surface produced the request so
	// saveGenerationRecord can stamp the record for analytics / audit.
	// See canvas-tool-design.md §8.8.
	Origin string
	// LineageParentRecordID, when non-nil, marks this generation as a
	// continuation of an existing w_generation_record. saveGenerationRecord
	// stamps it onto the new row's parent_record_id column.
	LineageParentRecordID *uint
}

// GenerateImageResponse 生成图片响应
type GenerateImageResponse struct {
	Success          bool                   `json:"success"`
	TaskID           string                 `json:"taskId"`
	ImageURLs        []string               `json:"imageUrls"`
	VideoURLs        []string               `json:"videoUrls,omitempty"`
	ThumbnailURL     string                 `json:"thumbnailUrl,omitempty"`
	ProviderJobID    string                 `json:"providerJobId,omitempty"`
	CreditsUsed      int                    `json:"creditsUsed"`
	CreditsRemaining int                    `json:"creditsRemaining"`
	Duration         int64                  `json:"duration"`
	ResultMetadata   map[string]interface{} `json:"resultMetadata,omitempty"`
	Error            string                 `json:"error,omitempty"`
}

func firstVectorizerSourceURL(req *GenerateImageRequest) string {
	if req == nil {
		return ""
	}
	if len(req.ReferenceImages) > 0 {
		for _, img := range req.ReferenceImages {
			if url := strings.TrimSpace(img.URL); url != "" {
				return url
			}
		}
	}
	return strings.TrimSpace(readStringFromJSONMapWithParams(req.RawRequestData, "imageUrl"))
}

func firstRemoverSourceURL(req *GenerateImageRequest) string {
	if req == nil {
		return ""
	}
	if len(req.ReferenceImages) > 0 {
		for _, img := range req.ReferenceImages {
			if url := strings.TrimSpace(img.URL); url != "" {
				return url
			}
		}
	}
	return strings.TrimSpace(readStringFromJSONMapWithParams(req.RawRequestData, "imageUrl"))
}

func firstUpscalerSourceURL(req *GenerateImageRequest) string {
	if req == nil {
		return ""
	}
	if len(req.ReferenceImages) > 0 {
		for _, img := range req.ReferenceImages {
			if url := strings.TrimSpace(img.URL); url != "" {
				return url
			}
		}
	}
	return strings.TrimSpace(readStringFromJSONMapWithParams(req.RawRequestData, "imageUrl"))
}

func nearestAspectRatioForDimensions(width, height int) string {
	if width <= 0 || height <= 0 {
		return "1:1"
	}

	ratio := float64(width) / float64(height)
	candidates := map[string]float64{
		"1:1":  1.0,
		"16:9": 1.778,
		"9:16": 0.5625,
		"3:2":  1.5,
		"2:3":  0.667,
		"4:5":  0.8,
		"5:4":  1.25,
		"4:3":  1.333,
		"3:4":  0.75,
		"21:9": 2.333,
		"9:21": 0.429,
	}

	closest := "1:1"
	minDiff := math.MaxFloat64
	for candidate, candidateRatio := range candidates {
		diff := math.Abs(ratio - candidateRatio)
		if diff < minDiff {
			minDiff = diff
			closest = candidate
		}
	}
	return closest
}

func (s *GeneratorService) inferRemoverAspectRatio(ctx context.Context, req *GenerateImageRequest) string {
	sourceImageURL := firstRemoverSourceURL(req)
	if sourceImageURL == "" {
		return "1:1"
	}

	_, sourceImageData, err := s.loadReferenceImageBytes(ctx, sourceImageURL)
	if err != nil {
		return "1:1"
	}

	bounds, _, err := image.DecodeConfig(bytes.NewReader(sourceImageData))
	if err != nil {
		return "1:1"
	}

	return nearestAspectRatioForDimensions(bounds.Width, bounds.Height)
}

func (s *GeneratorService) inferUpscalerAspectRatio(ctx context.Context, req *GenerateImageRequest) string {
	sourceImageURL := firstUpscalerSourceURL(req)
	if sourceImageURL == "" {
		return "1:1"
	}

	_, sourceImageData, err := s.loadReferenceImageBytes(ctx, sourceImageURL)
	if err != nil {
		return "1:1"
	}

	bounds, _, err := image.DecodeConfig(bytes.NewReader(sourceImageData))
	if err != nil {
		return "1:1"
	}

	return nearestAspectRatioForDimensions(bounds.Width, bounds.Height)
}

func (s *GeneratorService) generateRemoverResult(ctx context.Context, taskID string, req *GenerateImageRequest, creditCost int, startTime time.Time) (*GenerateImageResponse, error) {
	aspectRatio := strings.TrimSpace(req.AspectRatio)
	if aspectRatio == "" || strings.EqualFold(aspectRatio, "auto") {
		aspectRatio = s.inferRemoverAspectRatio(ctx, req)
	}
	req.AspectRatio = aspectRatio

	targetProviderType := providerTypeByModel(req.Model)
	removerProvider := provider.GetDefaultBackgroundRemoverProvider(targetProviderType, "")
	if removerProvider == nil {
		return &GenerateImageResponse{
			Success: false,
			TaskID:  taskID,
			Error:   "No available background remover provider. Ask the Server operator to configure one.",
		}, nil
	}

	width, height := provider.AspectRatioToDimensions(aspectRatio)
	refImages, err := s.buildProviderReferenceImages(ctx, req.ReferenceImages)
	if err != nil {
		globals.Warn(fmt.Sprintf("[GenerateImage] Remover reference image validation failed: %v", err))
		return &GenerateImageResponse{
			Success: false,
			TaskID:  taskID,
			Error:   err.Error(),
		}, nil
	}

	result, err := removerProvider.RemoveBackground(ctx, &provider.BackgroundRemovalRequest{
		Model:           req.Model,
		Prompt:          req.Prompt,
		NegativePrompt:  req.NegativePrompt,
		AspectRatio:     aspectRatio,
		Width:           width,
		Height:          height,
		SourceImageURL:  firstRemoverSourceURL(req),
		ReferenceImages: refImages,
		TaskID:          req.TaskID,
		OnProgress:      req.OnProgress,
		OnProviderJob:   req.OnProviderJob,
	})
	if err != nil {
		globals.Warn(fmt.Sprintf("[GenerateImage] Remover provider failed (%s): %v", removerProvider.Name(), err))
		return &GenerateImageResponse{
			Success: false,
			TaskID:  taskID,
			Error:   err.Error(),
		}, nil
	}
	if result == nil {
		return &GenerateImageResponse{
			Success: false,
			TaskID:  taskID,
			Error:   "Background remover provider returned no result",
		}, nil
	}
	if !result.Success {
		return &GenerateImageResponse{
			Success: false,
			TaskID:  taskID,
			Error:   result.Error,
		}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	imageURLs, err := s.saveImages(ctx, taskID, result.ImageData, result.ImageURLs)
	if err != nil {
		globals.Warn(fmt.Sprintf("[GenerateImage] Save remover images failed (%s): %v", result.Provider, err))
		return &GenerateImageResponse{
			Success: false,
			TaskID:  taskID,
			Error:   fmt.Sprintf("Failed to save images: %v", err),
		}, nil
	}

	resultMeta := map[string]interface{}{
		"provider":       result.Provider,
		"width":          width,
		"height":         height,
		"format":         detectFirstImageFormat(result.ImageData, imageURLs),
		"providerJobId":  result.ProviderJobID,
		"mode":           "background_remover",
		"providerType":   removerProvider.Type(),
		"sourceImageUrl": firstRemoverSourceURL(req),
		"hasAlpha":       detectFirstImageHasAlpha(result.ImageData),
	}

	if !req.SkipRecord {
		if err := s.saveGenerationRecord(req, taskID, imageURLs, 1, result.Duration, creditCost, resultMeta); err != nil {
			globals.Warn(fmt.Sprintf("[GenerateImage] Failed to save remover record: %v", err))
		}
	}

	return &GenerateImageResponse{
		Success:        true,
		TaskID:         taskID,
		ImageURLs:      imageURLs,
		ProviderJobID:  result.ProviderJobID,
		CreditsUsed:    creditCost,
		Duration:       time.Since(startTime).Milliseconds(),
		ResultMetadata: resultMeta,
	}, nil
}

func (s *GeneratorService) generateUpscalerResult(ctx context.Context, taskID string, req *GenerateImageRequest, creditCost int, startTime time.Time) (*GenerateImageResponse, error) {
	sourceImageURL := firstUpscalerSourceURL(req)
	aspectRatio := strings.TrimSpace(req.AspectRatio)
	if aspectRatio == "" || strings.EqualFold(aspectRatio, "auto") {
		aspectRatio = s.inferUpscalerAspectRatio(ctx, req)
	}
	req.AspectRatio = aspectRatio

	targetProviderType := providerTypeByModel(req.Model)
	upscalerProvider := provider.GetDefaultUpscalerProvider(targetProviderType, "")
	if upscalerProvider == nil {
		return &GenerateImageResponse{
			Success: false,
			TaskID:  taskID,
			Error:   "No available upscaler provider. Ask the Server operator to configure one.",
		}, nil
	}

	width, height := provider.AspectRatioToDimensions(aspectRatio)
	sourceWidth := 0
	sourceHeight := 0
	if sourceImageURL != "" {
		if _, sourceImageData, err := s.loadReferenceImageBytes(ctx, sourceImageURL); err == nil {
			if bounds, _, decodeErr := image.DecodeConfig(bytes.NewReader(sourceImageData)); decodeErr == nil {
				sourceWidth = bounds.Width
				sourceHeight = bounds.Height
			}
		}
	}
	refImages, err := s.buildProviderReferenceImages(ctx, req.ReferenceImages)
	if err != nil {
		globals.Warn(fmt.Sprintf("[GenerateImage] Upscaler reference image validation failed: %v", err))
		return &GenerateImageResponse{
			Success: false,
			TaskID:  taskID,
			Error:   err.Error(),
		}, nil
	}

	scale := readIntFromJSONMapWithParams(req.RawRequestData, "scale")
	if scale != 4 {
		scale = 2
	}
	enhanceFace, ok := readBoolFromJSONMapWithParams(req.RawRequestData, "enhanceFace")
	if !ok {
		enhanceFace, _ = readBoolFromJSONMapWithParams(req.RawRequestData, "enhanceQuality")
	}

	result, err := upscalerProvider.Upscale(ctx, &provider.UpscaleRequest{
		Model:           req.Model,
		Prompt:          req.Prompt,
		NegativePrompt:  req.NegativePrompt,
		AspectRatio:     aspectRatio,
		Width:           width,
		Height:          height,
		Scale:           scale,
		EnhanceFace:     enhanceFace,
		SourceImageURL:  sourceImageURL,
		ReferenceImages: refImages,
		TaskID:          req.TaskID,
		OnProgress:      req.OnProgress,
		OnProviderJob:   req.OnProviderJob,
	})
	if err != nil {
		globals.Warn(fmt.Sprintf("[GenerateImage] Upscaler provider failed (%s): %v", upscalerProvider.Name(), err))
		return &GenerateImageResponse{
			Success: false,
			TaskID:  taskID,
			Error:   err.Error(),
		}, nil
	}
	if result == nil {
		return &GenerateImageResponse{
			Success: false,
			TaskID:  taskID,
			Error:   "Upscaler provider returned no result",
		}, nil
	}
	if !result.Success {
		return &GenerateImageResponse{
			Success: false,
			TaskID:  taskID,
			Error:   result.Error,
		}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	imageURLs, err := s.saveImages(ctx, taskID, result.ImageData, result.ImageURLs)
	if err != nil {
		globals.Warn(fmt.Sprintf("[GenerateImage] Save upscaler images failed (%s): %v", result.Provider, err))
		return &GenerateImageResponse{
			Success: false,
			TaskID:  taskID,
			Error:   fmt.Sprintf("Failed to save images: %v", err),
		}, nil
	}

	outputWidth := width
	outputHeight := height
	if decodedWidth, decodedHeight, ok := detectFirstImageDimensions(result.ImageData); ok {
		outputWidth = decodedWidth
		outputHeight = decodedHeight
	}

	resultMeta := map[string]interface{}{
		"provider":       result.Provider,
		"width":          width,
		"height":         height,
		"sourceWidth":    sourceWidth,
		"sourceHeight":   sourceHeight,
		"outputWidth":    outputWidth,
		"outputHeight":   outputHeight,
		"format":         detectFirstImageFormat(result.ImageData, imageURLs),
		"providerJobId":  result.ProviderJobID,
		"mode":           "upscaler",
		"providerType":   upscalerProvider.Type(),
		"sourceImageUrl": sourceImageURL,
		"scale":          scale,
		"enhanceFace":    enhanceFace,
	}

	if !req.SkipRecord {
		if err := s.saveGenerationRecord(req, taskID, imageURLs, 1, result.Duration, creditCost, resultMeta); err != nil {
			globals.Warn(fmt.Sprintf("[GenerateImage] Failed to save upscaler record: %v", err))
		}
	}

	return &GenerateImageResponse{
		Success:        true,
		TaskID:         taskID,
		ImageURLs:      imageURLs,
		ProviderJobID:  result.ProviderJobID,
		CreditsUsed:    creditCost,
		Duration:       time.Since(startTime).Milliseconds(),
		ResultMetadata: resultMeta,
	}, nil
}

func detectFirstImageFormat(imageData [][]byte, imageURLs []string) string {
	if len(imageData) > 0 && len(imageData[0]) > 0 {
		switch strings.ToLower(strings.TrimSpace(http.DetectContentType(imageData[0]))) {
		case "image/png":
			return "png"
		case "image/webp":
			return "webp"
		case "image/jpeg":
			return "jpg"
		case "image/gif":
			return "gif"
		}
	}
	if len(imageURLs) > 0 {
		cleanURL := strings.Split(strings.Split(imageURLs[0], "#")[0], "?")[0]
		if idx := strings.LastIndex(cleanURL, "."); idx >= 0 && idx < len(cleanURL)-1 {
			ext := strings.ToLower(strings.TrimSpace(cleanURL[idx+1:]))
			switch ext {
			case "png", "webp", "jpg", "jpeg", "gif":
				if ext == "jpeg" {
					return "jpg"
				}
				return ext
			}
		}
	}
	return ""
}

func detectFirstImageHasAlpha(imageData [][]byte) bool {
	if len(imageData) == 0 || len(imageData[0]) == 0 {
		return false
	}
	img, _, err := image.Decode(bytes.NewReader(imageData[0]))
	if err != nil {
		return false
	}
	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			_, _, _, a := img.At(x, y).RGBA()
			if a != 0xffff {
				return true
			}
		}
	}
	return false
}

func detectFirstImageDimensions(imageData [][]byte) (int, int, bool) {
	if len(imageData) == 0 || len(imageData[0]) == 0 {
		return 0, 0, false
	}
	bounds, _, err := image.DecodeConfig(bytes.NewReader(imageData[0]))
	if err != nil {
		return 0, 0, false
	}
	if bounds.Width <= 0 || bounds.Height <= 0 {
		return 0, 0, false
	}
	return bounds.Width, bounds.Height, true
}

func encodeRasterPreviewPNG(imageData []byte) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(imageData))
	if err != nil {
		return nil, fmt.Errorf("failed to decode source image for preview: %w", err)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("failed to encode preview png: %w", err)
	}
	return buf.Bytes(), nil
}

func (s *GeneratorService) generateVectorizerResult(ctx context.Context, taskID string, req *GenerateImageRequest, creditCost int, startTime time.Time) (*GenerateImageResponse, error) {
	sourceImageURL := firstVectorizerSourceURL(req)
	if sourceImageURL == "" {
		return nil, fmt.Errorf("reference image is required")
	}

	if req.OnProgress != nil {
		req.OnProgress(10, nil)
	}

	mimeType, sourceImageData, err := s.loadReferenceImageBytes(ctx, sourceImageURL)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(strings.TrimSpace(mimeType), "image/svg+xml") || isSVGURL(sourceImageURL) {
		return nil, fmt.Errorf("vectorizer requires a raster image source")
	}

	if req.OnProgress != nil {
		req.OnProgress(35, nil)
	}

	previewPNG, err := encodeRasterPreviewPNG(sourceImageData)
	if err != nil {
		return nil, err
	}
	previewURLs, err := s.saveGeneratedBinaryAssetsLocalOnly(taskID, [][]byte{previewPNG}, []string{"image/png"}, "preview")
	if err != nil {
		return nil, err
	}

	if req.OnProgress != nil {
		req.OnProgress(70, nil)
	}

	vectorCfg := parseVectorizerPostProcessConfig(req)
	svgData, vectorEngine, err := vectorizeImageDataWithEngine(sourceImageData, vectorCfg)
	if err != nil {
		return nil, err
	}
	dateDir := time.Now().Format("2006/01/02")
	svgURL, err := s.saveVectorizedSVG(ctx, taskID, dateDir, svgData)
	if err != nil {
		return nil, err
	}

	if req.OnProgress != nil {
		req.OnProgress(90, nil)
	}

	imageURLs := make([]string, 0, len(previewURLs)+1)
	imageURLs = append(imageURLs, previewURLs...)
	if strings.TrimSpace(svgURL) != "" {
		imageURLs = append(imageURLs, svgURL)
	}

	if !req.SkipRecord {
		bounds, _, decodeErr := image.DecodeConfig(bytes.NewReader(sourceImageData))
		resultMeta := map[string]interface{}{
			"provider":       "local_vectorizer",
			"providerName":   vectorEngine,
			"providerModel":  vectorEngine,
			"format":         "svg",
			"sourceMimeType": mimeType,
			"svgUrl":         svgURL,
		}
		if decodeErr == nil {
			resultMeta["width"] = bounds.Width
			resultMeta["height"] = bounds.Height
		}
		if err := s.saveGenerationRecord(req, taskID, imageURLs, 1, time.Since(startTime).Milliseconds(), creditCost, resultMeta); err != nil {
			globals.Warn(fmt.Sprintf("[GenerateImage] Failed to save vectorizer record: %v", err))
		}
	}

	return &GenerateImageResponse{
		Success:       true,
		TaskID:        taskID,
		ImageURLs:     imageURLs,
		CreditsUsed:   creditCost,
		Duration:      time.Since(startTime).Milliseconds(),
		ProviderJobID: "",
		ResultMetadata: map[string]interface{}{
			"provider": "local_vectorizer",
			"engine":   vectorEngine,
			"svgUrl":   svgURL,
		},
	}, nil
}

// GenerateImage 生成图片核心逻辑
func (s *GeneratorService) GenerateImage(ctx context.Context, req *GenerateImageRequest) (*GenerateImageResponse, error) {
	startTime := time.Now()
	taskID := strings.TrimSpace(req.TaskID)
	if taskID == "" {
		taskID = uuid.New().String()
	}

	numberOfImages := req.NumberOfImages
	if numberOfImages <= 0 {
		numberOfImages = 1
	}
	resolution := req.Resolution

	// 使用调用方预先计算的费用（由 API 层统一计算）
	creditCost := req.CreditCost
	if creditCost <= 0 {
		// 回退：按 feature key 计算（兼容直接调用场景）
		creditCost = GetCreditCostByToolID(CreditCostParams{
			ToolID:         req.ToolID,
			Model:          req.Model,
			NumberOfImages: numberOfImages,
			Resolution:     resolution,
			Upscale:        req.Upscale,
		})
	}

	if req.ToolID == model.TOOL_IMAGE_VECTORIZER {
		return s.generateVectorizerResult(ctx, taskID, req, creditCost, startTime)
	}

	if req.ToolID == model.TOOL_BACKGROUND_REMOVER {
		return s.generateRemoverResult(ctx, taskID, req, creditCost, startTime)
	}

	if req.ToolID == model.TOOL_IMAGE_UPSCALER {
		return s.generateUpscalerResult(ctx, taskID, req, creditCost, startTime)
	}

	// 按模型 + 媒体类型路由 provider，避免视频任务误落到图片 provider。
	mediaType := strings.ToLower(strings.TrimSpace(readStringFromJSONMapWithParams(req.RawRequestData, "mediaType")))
	if mediaType == "" {
		mediaType = model.MediaTypeImage
	}

	// `type` 只代表 provider 实现；具体业务模型通过 `model` 参与候选过滤。
	targetProviderType := providerTypeByModel(req.Model)
	p := provider.GetDefaultProviderForModel(mediaType, targetProviderType, req.Model, "")

	// 严格模式：明确选了型号但未配置对应 provider，直接报错。
	// 不再静默回退到任意 image provider，否则用户选定模型后会被路由
	// 到同一 provider 实现下的其他业务模型。
	if p == nil && targetProviderType != "" {
		return &GenerateImageResponse{
			Success: false,
			TaskID:  taskID,
			Error:   fmt.Sprintf("No available provider for model %q (provider type=%q). Ask the Server operator to configure type=%s.", req.Model, targetProviderType, targetProviderType),
		}, nil
	}

	// 走到这里 targetProviderType == ""（未识别 model），按 mediaType 兜底。
	if p == nil && mediaType == model.MediaTypeVideo {
		return &GenerateImageResponse{
			Success: false,
			TaskID:  taskID,
			Error:   "No available video generation provider. Ask the Server operator to configure media_type=video.",
		}, nil
	}
	if p == nil {
		return &GenerateImageResponse{
			Success: false,
			TaskID:  taskID,
			Error:   fmt.Sprintf("No available %s generation provider. Ask the Server operator to configure one.", mediaType),
		}, nil
	}

	// 计算尺寸
	width, height := provider.AspectRatioToDimensions(req.AspectRatio)
	if model.NormalizeModelID(req.Model) == model.NANO_BANANA_PRO && req.Resolution == "4k" {
		width = width * 2
		height = height * 2
	}

	// Safety net: if a @character/<slug> token slipped past the frontend
	// resolver, expand it server-side so raw mention syntax never reaches
	// the provider. No-op when the prompt is already clean.
	resolvedPrompt, resolvedNegative := canvas.ResolvePromptSafely(ctx, req.UID, req.Prompt, req.NegativePrompt)

	// Canvas AssetBinding injection (§8.4): when the request carries a
	// character/brand/product binding, merge its prompt suffixes and
	// reference images into the outgoing request. Non-canvas callers
	// leave AssetBindings nil and this is a no-op.
	if req.AssetBindings != nil {
		injected, err := canvas.InjectAssetContext(
			ctx,
			globals.GraDBs["system"],
			int(req.UID),
			resolvedPrompt,
			resolvedNegative,
			req.AssetBindings,
		)
		if err != nil {
			globals.Warn(fmt.Sprintf("[GenerateImage] Asset injection failed (non-fatal): %v", err))
		} else {
			resolvedPrompt = injected.EnrichedPrompt
			resolvedNegative = injected.EnrichedNegativePrompt
			req.ReferenceImages = mergeInjectedReferenceImages(req.ReferenceImages, injected.ReferenceImages)
		}
	}

	// 构建生成请求
	genReq := &provider.GenerationRequest{
		Model:             req.Model,
		Prompt:            resolvedPrompt,
		NegativePrompt:    resolvedNegative,
		Resolution:        resolution,
		Width:             width,
		Height:            height,
		Steps:             req.Steps,
		CFGScale:          req.CFGScale,
		Seed:              req.Seed,
		StylePreset:       req.StylePreset,
		Sampler:           req.Sampler,
		NumImages:         1,
		AspectRatio:       req.AspectRatio,
		Duration:          readStringFromJSONMapWithParams(req.RawRequestData, "duration"),
		StartFrame:        readStringFromJSONMapWithParams(req.RawRequestData, "startFrame"),
		EndFrame:          readStringFromJSONMapWithParams(req.RawRequestData, "endFrame"),
		GenerationMethod:  readStringFromJSONMapWithParams(req.RawRequestData, "generationMethod"),
		MotionMode:        readStringFromJSONMapWithParams(req.RawRequestData, "motionMode"),
		MotionOrientation: readStringFromJSONMapWithParams(req.RawRequestData, "motionOrientation"),
		TaskID:            req.TaskID,
		OnProgress:        req.OnProgress,
		OnProviderJob:     req.OnProviderJob,
	}
	if req.Seed == 0 {
		if seedFromParams := readIntFromJSONMapWithParams(req.RawRequestData, "seed"); seedFromParams > 0 {
			genReq.Seed = int64(seedFromParams)
		}
	}
	if generateAudio, ok := readBoolFromJSONMapWithParams(req.RawRequestData, "generateAudio"); ok {
		value := generateAudio
		genReq.GenerateAudio = &value
	}
	if req.NumberOfImages > 0 {
		genReq.NumImages = req.NumberOfImages
	}
	// GPT Image 2 advanced knobs surfaced by /tools/image-generator. Other providers
	// ignore these fields, so passing them unconditionally is safe.
	genReq.Quality = readStringFromJSONMapWithParams(req.RawRequestData, "quality")
	genReq.OutputFormat = readStringFromJSONMapWithParams(req.RawRequestData, "outputFormat")
	genReq.OutputCompression = readIntFromJSONMapWithParams(req.RawRequestData, "outputCompression")

	refImages, err := s.buildProviderReferenceImages(ctx, req.ReferenceImages)
	if err != nil {
		globals.Warn(fmt.Sprintf("[GenerateImage] Reference image validation failed: %v", err))
		return &GenerateImageResponse{
			Success: false,
			TaskID:  taskID,
			Error:   err.Error(),
		}, nil
	}
	genReq.ReferenceImages = refImages
	genReq.ReferenceVideos = s.buildProviderReferenceMedia(req.ReferenceVideos)
	genReq.ReferenceAudios = s.buildProviderReferenceMedia(req.ReferenceAudios)

	// 调用提供商生成
	result, err := p.Generate(ctx, genReq)
	if err != nil {
		globals.Warn(fmt.Sprintf("[GenerateImage] Provider generate failed (%s): %v", p.Name(), err))
		return &GenerateImageResponse{
			Success: false,
			TaskID:  taskID,
			Error:   err.Error(),
		}, nil
	}

	if !result.Success {
		globals.Warn(fmt.Sprintf("[GenerateImage] Provider returned unsuccessful result (%s): %s", result.Provider, result.Error))
		return &GenerateImageResponse{
			Success: false,
			TaskID:  taskID,
			Error:   result.Error,
		}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// 保存图片到本地存储
	imageURLs, err := s.saveImages(ctx, taskID, result.ImageData, result.ImageURLs)
	if err != nil {
		globals.Warn(fmt.Sprintf("[GenerateImage] Save images failed (%s): %v", result.Provider, err))
		return &GenerateImageResponse{
			Success: false,
			TaskID:  taskID,
			Error:   fmt.Sprintf("Failed to save images: %v", err),
		}, nil
	}

	videoURLs := append([]string(nil), result.VideoURLs...)
	thumbnailURL := ""
	if mediaType == model.MediaTypeVideo {
		if len(result.VideoData) > 0 {
			videoURLs, err = s.saveGeneratedBinaryAssets(ctx, taskID, result.VideoData, result.VideoContentTypes, "video")
			if err != nil {
				globals.Warn(fmt.Sprintf("[GenerateImage] Save binary videos failed (%s): %v", result.Provider, err))
				return &GenerateImageResponse{
					Success: false,
					TaskID:  taskID,
					Error:   fmt.Sprintf("Failed to save videos: %v", err),
				}, nil
			}
		} else if result.DownloadVideos {
			videoURLs, err = s.saveRemoteFiles(ctx, taskID, result.VideoURLs, "video")
			if err != nil {
				globals.Warn(fmt.Sprintf("[GenerateImage] Save remote videos failed (%s): %v", result.Provider, err))
				return &GenerateImageResponse{
					Success: false,
					TaskID:  taskID,
					Error:   fmt.Sprintf("Failed to save videos: %v", err),
				}, nil
			}
		}
		if strings.TrimSpace(result.ThumbnailURL) != "" {
			thumbURLs, thumbErr := s.saveRemoteFiles(ctx, taskID, []string{result.ThumbnailURL}, "thumbnail")
			if thumbErr != nil {
				globals.Warn(fmt.Sprintf("[GenerateImage] Save remote video thumbnail failed (%s): %v", result.Provider, thumbErr))
			} else if len(thumbURLs) > 0 {
				thumbnailURL = thumbURLs[0]
			}
		}
		if thumbnailURL == "" && len(videoURLs) > 0 {
			fallbackThumbURL, thumbErr := s.generateAndSaveVideoThumbnail(ctx, taskID, videoURLs[0])
			if thumbErr != nil {
				globals.Warn(fmt.Sprintf("[GenerateImage] Generate video thumbnail fallback failed (%s): %v", result.Provider, thumbErr))
			} else {
				thumbnailURL = fallbackThumbURL
			}
		}
	} else if strings.TrimSpace(result.ThumbnailURL) != "" {
		thumbURLs, thumbErr := s.saveRemoteFiles(ctx, taskID, []string{result.ThumbnailURL}, "thumbnail")
		if thumbErr != nil {
			globals.Warn(fmt.Sprintf("[GenerateImage] Save remote thumbnail failed (%s): %v", result.Provider, thumbErr))
		} else if len(thumbURLs) > 0 {
			thumbnailURL = thumbURLs[0]
		}
	}
	svgURL := ""
	if req.ToolID == model.TOOL_IMAGE_VECTORIZER {
		vectorCfg := parseVectorizerPostProcessConfig(req)
		svgURL, err = s.generateVectorizerSVG(ctx, taskID, result.ImageData, imageURLs, vectorCfg)
		if err != nil {
			globals.Warn(fmt.Sprintf("[GenerateImage] Vectorizer SVG post-process failed: %v", err))
		} else if strings.TrimSpace(svgURL) != "" {
			imageURLs = append(imageURLs, svgURL)
		}
	}

	if !req.SkipRecord {
		resultMeta := map[string]interface{}{
			"provider":      result.Provider,
			"width":         width,
			"height":        height,
			"format":        "png",
			"seed":          result.Seed,
			"steps":         req.Steps,
			"thumbnailUrl":  thumbnailURL,
			"providerJobId": result.ProviderJobID,
			"mediaType":     mediaType,
		}
		if strings.TrimSpace(svgURL) != "" {
			resultMeta["svgUrl"] = svgURL
		}
		// 保存生成记录, 视频URL与图片URL合并传给 file_urls 数组
		allUrls := append(imageURLs, videoURLs...)
		if err := s.saveGenerationRecord(req, taskID, allUrls, 1, result.Duration, creditCost, resultMeta); err != nil {
			globals.Warn(fmt.Sprintf("[GenerateImage] Failed to save generation record: %v", err))
			// 不影响返回结果，图片已成功生成
		}
	}

	duration := time.Since(startTime).Milliseconds()

	return &GenerateImageResponse{
		Success:       true,
		TaskID:        taskID,
		ImageURLs:     imageURLs,
		VideoURLs:     videoURLs,
		ThumbnailURL:  thumbnailURL,
		ProviderJobID: result.ProviderJobID,
		CreditsUsed:   creditCost,
		Duration:      duration,
	}, nil
}

type vectorizerPostProcessConfig struct {
	DetailLevel  string
	ColorMode    string
	Colors       int
	HighFidelity bool
}

func getValueFromJSONMapWithParams(raw model.JSONMap, key string) (interface{}, bool) {
	if raw == nil {
		return nil, false
	}
	if value, ok := raw[key]; ok {
		return value, true
	}
	nestedRaw, ok := raw["params"]
	if !ok {
		return nil, false
	}
	switch nested := nestedRaw.(type) {
	case map[string]interface{}:
		value, ok := nested[key]
		return value, ok
	case model.JSONMap:
		value, ok := nested[key]
		return value, ok
	default:
		return nil, false
	}
}

func readStringFromJSONMapWithParams(raw model.JSONMap, key string) string {
	value, ok := getValueFromJSONMapWithParams(raw, key)
	if !ok {
		return ""
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	default:
		return ""
	}
}

func readIntFromJSONMapWithParams(raw model.JSONMap, key string) int {
	value, ok := getValueFromJSONMapWithParams(raw, key)
	if !ok {
		return 0
	}
	switch v := value.(type) {
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

func readBoolFromJSONMapWithParams(raw model.JSONMap, key string) (bool, bool) {
	value, ok := getValueFromJSONMapWithParams(raw, key)
	if !ok {
		return false, false
	}
	switch v := value.(type) {
	case bool:
		return v, true
	case int:
		return v != 0, true
	case int32:
		return v != 0, true
	case int64:
		return v != 0, true
	case float32:
		return v != 0, true
	case float64:
		return v != 0, true
	case string:
		normalized := strings.ToLower(strings.TrimSpace(v))
		if normalized == "1" || normalized == "yes" || normalized == "on" {
			return true, true
		}
		if normalized == "0" || normalized == "no" || normalized == "off" {
			return false, true
		}
		parsed, err := strconv.ParseBool(normalized)
		if err == nil {
			return parsed, true
		}
		return false, false
	default:
		return false, false
	}
}

func readOptionalBoolFromJSONMapWithParams(raw model.JSONMap, key string, defaultValue bool) bool {
	if value, ok := readBoolFromJSONMapWithParams(raw, key); ok {
		return value
	}
	return defaultValue
}

func mergeJSONStringMap(target map[string]interface{}, raw interface{}) {
	if target == nil || raw == nil {
		return
	}
	switch value := raw.(type) {
	case map[string]interface{}:
		for key, item := range value {
			target[key] = item
		}
	case model.JSONMap:
		for key, item := range value {
			target[key] = item
		}
	}
}

func getAvatarBillingSnapshotForRecord(raw model.JSONMap, requestedCount, creditsUsed int) map[string]interface{} {
	unitCreditCost := creditsUsed
	if requestedCount <= 0 {
		requestedCount = readIntFromJSONMapWithParams(raw, "numberOfImages")
	}
	if requestedCount <= 0 {
		requestedCount = 1
	}
	if snapshotRaw, ok := getValueFromJSONMapWithParams(raw, "billingSnapshot"); ok {
		if snapshot, ok := snapshotRaw.(map[string]interface{}); ok {
			if value := readIntFromMap(snapshot, "unitCreditCost"); value > 0 {
				unitCreditCost = value
			}
		}
		if snapshot, ok := snapshotRaw.(model.JSONMap); ok {
			if value := readIntFromMap(snapshot, "unitCreditCost"); value > 0 {
				unitCreditCost = value
			}
		}
	}
	if unitCreditCost <= 0 {
		if requestedCount > 0 && creditsUsed > 0 {
			unitCreditCost = creditsUsed / requestedCount
		}
		if unitCreditCost <= 0 {
			unitCreditCost = creditsUsed
		}
		if unitCreditCost <= 0 {
			unitCreditCost = 1
		}
	}
	reservedCredits := unitCreditCost * requestedCount
	finalChargedCredits := creditsUsed
	if finalChargedCredits <= 0 {
		finalChargedCredits = reservedCredits
	}
	refundCredits := reservedCredits - finalChargedCredits
	if refundCredits < 0 {
		refundCredits = 0
	}
	return map[string]interface{}{
		"unitCreditCost":      unitCreditCost,
		"requestedCount":      requestedCount,
		"reservedCredits":     reservedCredits,
		"successCount":        requestedCount,
		"failedCount":         0,
		"finalChargedCredits": finalChargedCredits,
		"refundCredits":       refundCredits,
		"billingStatus":       "settled",
	}
}

func readIntFromMap(raw interface{}, key string) int {
	var source map[string]interface{}
	switch casted := raw.(type) {
	case map[string]interface{}:
		source = casted
	case model.JSONMap:
		source = map[string]interface{}(casted)
	default:
		return 0
	}
	value, ok := source[key]
	if !ok {
		return 0
	}
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
	default:
		return 0
	}
}

func normalizeVectorizerDetailLevel(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "low":
		return "low"
	case "high":
		return "high"
	default:
		return "medium"
	}
}

func normalizeVectorizerColorMode(value string) string {
	_ = value
	return "color"
}

func clampVectorizerColorCount(value int) int {
	if value < 2 {
		return 2
	}
	if value > 64 {
		return 64
	}
	return value
}

func parseVectorizerPostProcessConfig(req *GenerateImageRequest) vectorizerPostProcessConfig {
	return vectorizerPostProcessConfig{
		DetailLevel:  "high",
		ColorMode:    "color",
		Colors:       64,
		HighFidelity: true,
	}
}

func isSVGURL(url string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(url))
	if strings.HasPrefix(trimmed, "data:image/svg") {
		return true
	}
	if strings.Contains(trimmed, ".svg") {
		idx := strings.Index(trimmed, ".svg")
		if idx >= 0 {
			suffix := trimmed[idx+4:]
			return suffix == "" || strings.HasPrefix(suffix, "?") || strings.HasPrefix(suffix, "#")
		}
	}
	return false
}

func vectorizerTargetDimension(detailLevel string, highFidelity bool) int {
	if highFidelity {
		switch normalizeVectorizerDetailLevel(detailLevel) {
		case "low":
			return 320
		case "high":
			return 960
		default:
			return 640
		}
	}

	switch normalizeVectorizerDetailLevel(detailLevel) {
	case "low":
		return 192
	case "high":
		return 640
	default:
		return 384
	}
}

type rgbaKey struct {
	R uint8
	G uint8
	B uint8
	A uint8
}

func quantizeVectorizerColor(r, g, b, a uint8, cfg vectorizerPostProcessConfig) rgbaKey {
	alphaCutoff := uint8(16)
	if cfg.HighFidelity {
		alphaCutoff = 8
	}
	if a < alphaCutoff {
		return rgbaKey{}
	}

	levels := int(math.Ceil(math.Cbrt(float64(cfg.Colors * 12))))
	if cfg.HighFidelity {
		boosted := int(math.Ceil(math.Cbrt(float64(cfg.Colors * 20))))
		if boosted > levels {
			levels = boosted
		}
	}
	if levels < 2 {
		levels = 2
	}
	maxLevels := 12
	if cfg.HighFidelity {
		maxLevels = 20
	}
	if levels > maxLevels {
		levels = maxLevels
	}

	quantizeChannel := func(ch uint8) uint8 {
		idx := int(ch) * (levels - 1) / 255
		return uint8((idx*255 + (levels-1)/2) / (levels - 1))
	}

	return rgbaKey{
		R: quantizeChannel(r),
		G: quantizeChannel(g),
		B: quantizeChannel(b),
		A: a,
	}
}

type vectorGridPoint struct {
	X int
	Y int
}

type vectorOrientedEdge struct {
	From vectorGridPoint
	To   vectorGridPoint
}

type vectorDirection struct {
	DX int
	DY int
}

func vectorEdgeDirection(edge vectorOrientedEdge) vectorDirection {
	return vectorDirection{
		DX: edge.To.X - edge.From.X,
		DY: edge.To.Y - edge.From.Y,
	}
}

func vectorTurnPriority(prev, next vectorDirection) int {
	cross := prev.DX*next.DY - prev.DY*next.DX
	dot := prev.DX*next.DX + prev.DY*next.DY
	switch {
	case cross > 0:
		return 3
	case cross == 0 && dot > 0:
		return 2
	case cross < 0:
		return 1
	default:
		return 0
	}
}

func buildVectorMaskEdges(mask []bool, width, height int) []vectorOrientedEdge {
	edges := make([]vectorOrientedEdge, 0, width*height)
	isFilled := func(x, y int) bool {
		if x < 0 || x >= width || y < 0 || y >= height {
			return false
		}
		return mask[y*width+x]
	}

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if !isFilled(x, y) {
				continue
			}

			if !isFilled(x, y-1) {
				edges = append(edges, vectorOrientedEdge{
					From: vectorGridPoint{X: x, Y: y},
					To:   vectorGridPoint{X: x + 1, Y: y},
				})
			}
			if !isFilled(x+1, y) {
				edges = append(edges, vectorOrientedEdge{
					From: vectorGridPoint{X: x + 1, Y: y},
					To:   vectorGridPoint{X: x + 1, Y: y + 1},
				})
			}
			if !isFilled(x, y+1) {
				edges = append(edges, vectorOrientedEdge{
					From: vectorGridPoint{X: x + 1, Y: y + 1},
					To:   vectorGridPoint{X: x, Y: y + 1},
				})
			}
			if !isFilled(x-1, y) {
				edges = append(edges, vectorOrientedEdge{
					From: vectorGridPoint{X: x, Y: y + 1},
					To:   vectorGridPoint{X: x, Y: y},
				})
			}
		}
	}

	return edges
}

func chooseNextVectorEdge(
	currentPoint vectorGridPoint,
	prevDirection vectorDirection,
	edges []vectorOrientedEdge,
	outgoing map[vectorGridPoint][]int,
	visited map[int]bool,
	localVisited map[int]bool,
) (int, bool) {
	candidates := outgoing[currentPoint]
	bestEdge := -1
	bestPriority := -1
	bestTie := 1 << 30

	for _, edgeIdx := range candidates {
		if visited[edgeIdx] || localVisited[edgeIdx] {
			continue
		}
		dir := vectorEdgeDirection(edges[edgeIdx])
		priority := vectorTurnPriority(prevDirection, dir)
		tie := dir.DX*10 + dir.DY
		if priority > bestPriority || (priority == bestPriority && tie < bestTie) {
			bestPriority = priority
			bestTie = tie
			bestEdge = edgeIdx
		}
	}

	if bestEdge < 0 {
		return 0, false
	}
	return bestEdge, true
}

func traceVectorLoops(edges []vectorOrientedEdge) [][]vectorGridPoint {
	outgoing := make(map[vectorGridPoint][]int, len(edges))
	for idx, edge := range edges {
		outgoing[edge.From] = append(outgoing[edge.From], idx)
	}

	visited := make(map[int]bool, len(edges))
	loops := make([][]vectorGridPoint, 0)
	maxSteps := len(edges) + 5

	for idx := range edges {
		if visited[idx] {
			continue
		}

		startEdge := edges[idx]
		startPoint := startEdge.From
		currentIdx := idx
		currentDir := vectorEdgeDirection(startEdge)
		localVisited := map[int]bool{}
		localTrace := make([]int, 0, 64)
		points := []vectorGridPoint{startPoint}
		success := false

		for step := 0; step < maxSteps; step++ {
			if visited[currentIdx] || localVisited[currentIdx] {
				break
			}
			localVisited[currentIdx] = true
			localTrace = append(localTrace, currentIdx)

			edge := edges[currentIdx]
			points = append(points, edge.To)
			if edge.To == startPoint {
				success = true
				break
			}

			nextIdx, ok := chooseNextVectorEdge(edge.To, currentDir, edges, outgoing, visited, localVisited)
			if !ok {
				break
			}
			currentIdx = nextIdx
			currentDir = vectorEdgeDirection(edges[currentIdx])
		}

		for _, traced := range localTrace {
			visited[traced] = true
		}

		if !success || len(points) < 4 {
			continue
		}
		loops = append(loops, points)
	}

	return loops
}

func normalizeVectorLoop(points []vectorGridPoint) []vectorGridPoint {
	if len(points) < 4 {
		return nil
	}

	compact := make([]vectorGridPoint, 0, len(points))
	for _, p := range points {
		if len(compact) == 0 || compact[len(compact)-1] != p {
			compact = append(compact, p)
		}
	}
	if len(compact) < 4 {
		return nil
	}
	if compact[0] == compact[len(compact)-1] {
		compact = compact[:len(compact)-1]
	}
	if len(compact) < 3 {
		return nil
	}
	return compact
}

func removeCollinearVectorPoints(points []vectorGridPoint) []vectorGridPoint {
	if len(points) < 3 {
		return points
	}

	out := append([]vectorGridPoint(nil), points...)
	for {
		if len(out) < 3 {
			return out
		}

		n := len(out)
		next := make([]vectorGridPoint, 0, n)
		removed := false
		for i := 0; i < n; i++ {
			prev := out[(i-1+n)%n]
			curr := out[i]
			nextPoint := out[(i+1)%n]
			dx1 := curr.X - prev.X
			dy1 := curr.Y - prev.Y
			dx2 := nextPoint.X - curr.X
			dy2 := nextPoint.Y - curr.Y
			if dx1*dy2-dy1*dx2 == 0 {
				removed = true
				continue
			}
			next = append(next, curr)
		}

		if !removed || len(next) < 3 {
			return out
		}
		out = next
	}
}

func vectorPointToSegmentDistance(p, a, b vectorGridPoint) float64 {
	ax := float64(a.X)
	ay := float64(a.Y)
	bx := float64(b.X)
	by := float64(b.Y)
	px := float64(p.X)
	py := float64(p.Y)

	dx := bx - ax
	dy := by - ay
	if dx == 0 && dy == 0 {
		return math.Hypot(px-ax, py-ay)
	}

	t := ((px-ax)*dx + (py-ay)*dy) / (dx*dx + dy*dy)
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	projX := ax + t*dx
	projY := ay + t*dy
	return math.Hypot(px-projX, py-projY)
}

func simplifyVectorLoopByTolerance(points []vectorGridPoint, tolerance float64, minPoints int) []vectorGridPoint {
	if len(points) <= minPoints || tolerance <= 0 {
		return points
	}

	out := append([]vectorGridPoint(nil), points...)
	for len(out) > minPoints {
		bestIdx := -1
		bestDist := tolerance
		n := len(out)

		for i := 0; i < n; i++ {
			prev := out[(i-1+n)%n]
			curr := out[i]
			next := out[(i+1)%n]
			dist := vectorPointToSegmentDistance(curr, prev, next)
			if dist <= bestDist {
				bestDist = dist
				bestIdx = i
			}
		}

		if bestIdx < 0 {
			break
		}
		out = append(out[:bestIdx], out[bestIdx+1:]...)
	}

	return out
}

func downsampleVectorLoop(points []vectorGridPoint, maxPoints int) []vectorGridPoint {
	if maxPoints < 3 || len(points) <= maxPoints {
		return points
	}

	sampled := make([]vectorGridPoint, 0, maxPoints)
	step := float64(len(points)) / float64(maxPoints)
	for i := 0; i < maxPoints; i++ {
		idx := int(math.Floor(float64(i) * step))
		if idx >= len(points) {
			idx = len(points) - 1
		}
		p := points[idx]
		if len(sampled) == 0 || sampled[len(sampled)-1] != p {
			sampled = append(sampled, p)
		}
	}
	if len(sampled) < 3 {
		return points
	}
	return sampled
}

func closeVectorLoop(points []vectorGridPoint) []vectorGridPoint {
	if len(points) == 0 {
		return nil
	}
	closed := make([]vectorGridPoint, 0, len(points)+1)
	closed = append(closed, points...)
	if closed[0] != closed[len(closed)-1] {
		closed = append(closed, closed[0])
	}
	return closed
}

func vectorizerLoopMaxPoints(detailLevel string, highFidelity bool) int {
	maxPoints := 720
	switch normalizeVectorizerDetailLevel(detailLevel) {
	case "low":
		maxPoints = 240
	case "high":
		maxPoints = 2400
	}
	if highFidelity {
		maxPoints = maxPoints * 2
		if maxPoints > 6000 {
			maxPoints = 6000
		}
	}
	return maxPoints
}

func vectorizerLoopTolerance(detailLevel string, highFidelity bool) float64 {
	tolerance := 0.45
	switch normalizeVectorizerDetailLevel(detailLevel) {
	case "low":
		tolerance = 0.8
	case "high":
		tolerance = 0.18
	}
	if highFidelity {
		return math.Max(0.08, tolerance*0.45)
	}
	return tolerance
}

func vectorizerLoopMinArea(detailLevel string, width, height int, highFidelity bool) float64 {
	totalArea := float64(width * height)
	minArea := math.Max(1, totalArea*0.00008)
	switch normalizeVectorizerDetailLevel(detailLevel) {
	case "low":
		minArea = math.Max(4, totalArea*0.0002)
	case "high":
		minArea = math.Max(0.5, totalArea*0.00003)
	}
	if highFidelity {
		return math.Max(0.2, minArea*0.22)
	}
	return minArea
}

func vectorizerUseSmoothPath(detailLevel string) bool {
	return normalizeVectorizerDetailLevel(detailLevel) != "low"
}

func vectorLoopArea(points []vectorGridPoint) float64 {
	n := len(points)
	if n < 3 {
		return 0
	}
	if points[0] == points[n-1] {
		n--
	}
	if n < 3 {
		return 0
	}

	var sum float64
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		sum += float64(points[i].X*points[j].Y - points[j].X*points[i].Y)
	}
	if sum < 0 {
		sum = -sum
	}
	return sum / 2
}

func formatVectorPathCoord(v float64) string {
	formatted := strconv.FormatFloat(v, 'f', 2, 64)
	formatted = strings.TrimRight(formatted, "0")
	formatted = strings.TrimRight(formatted, ".")
	if formatted == "" || formatted == "-0" {
		return "0"
	}
	return formatted
}

func reduceVectorLoopPoints(points []vectorGridPoint, detailLevel string, highFidelity bool) []vectorGridPoint {
	openLoop := normalizeVectorLoop(points)
	if len(openLoop) < 3 {
		return nil
	}

	openLoop = removeCollinearVectorPoints(openLoop)
	if len(openLoop) < 3 {
		return nil
	}

	tolerance := vectorizerLoopTolerance(detailLevel, highFidelity)
	openLoop = simplifyVectorLoopByTolerance(openLoop, tolerance, 8)
	openLoop = downsampleVectorLoop(openLoop, vectorizerLoopMaxPoints(detailLevel, highFidelity))
	openLoop = removeCollinearVectorPoints(openLoop)
	if len(openLoop) < 3 {
		return nil
	}

	return closeVectorLoop(openLoop)
}

func appendLinearVectorLoopPath(path *strings.Builder, openLoop []vectorGridPoint) {
	path.WriteString(fmt.Sprintf("M%d %d", openLoop[0].X, openLoop[0].Y))
	for i := 1; i < len(openLoop); i++ {
		path.WriteString(fmt.Sprintf("L%d %d", openLoop[i].X, openLoop[i].Y))
	}
	path.WriteString("Z")
}

func appendSmoothVectorLoopPath(path *strings.Builder, openLoop []vectorGridPoint) {
	n := len(openLoop)
	startX := (float64(openLoop[n-1].X) + float64(openLoop[0].X)) / 2
	startY := (float64(openLoop[n-1].Y) + float64(openLoop[0].Y)) / 2
	path.WriteString("M")
	path.WriteString(formatVectorPathCoord(startX))
	path.WriteByte(' ')
	path.WriteString(formatVectorPathCoord(startY))

	for i := 0; i < n; i++ {
		curr := openLoop[i]
		next := openLoop[(i+1)%n]
		endX := (float64(curr.X) + float64(next.X)) / 2
		endY := (float64(curr.Y) + float64(next.Y)) / 2
		path.WriteString("Q")
		path.WriteString(formatVectorPathCoord(float64(curr.X)))
		path.WriteByte(' ')
		path.WriteString(formatVectorPathCoord(float64(curr.Y)))
		path.WriteByte(' ')
		path.WriteString(formatVectorPathCoord(endX))
		path.WriteByte(' ')
		path.WriteString(formatVectorPathCoord(endY))
	}
	path.WriteString("Z")
}

func buildVectorPathData(loops [][]vectorGridPoint, detailLevel string, minLoopArea float64, highFidelity bool) string {
	useSmoothPath := vectorizerUseSmoothPath(detailLevel)
	var path strings.Builder
	for _, rawLoop := range loops {
		loop := reduceVectorLoopPoints(rawLoop, detailLevel, highFidelity)
		if len(loop) < 4 {
			continue
		}
		if vectorLoopArea(loop) < minLoopArea {
			continue
		}
		openLoop := loop[:len(loop)-1]
		if len(openLoop) < 3 {
			continue
		}
		if path.Len() > 0 {
			path.WriteByte(' ')
		}

		if useSmoothPath && len(openLoop) >= 4 {
			appendSmoothVectorLoopPath(&path, openLoop)
		} else {
			appendLinearVectorLoopPath(&path, openLoop)
		}
	}
	return path.String()
}

// rasterToVectorizedSVG is the original hand-written vectorization implementation,
// kept as a fallback when go-vtracer is unavailable or fails.
func rasterToVectorizedSVG(imageData []byte, cfg vectorizerPostProcessConfig) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(imageData))
	if err != nil {
		return nil, fmt.Errorf("failed to decode raster image: %w", err)
	}

	bounds := img.Bounds()
	srcW := bounds.Dx()
	srcH := bounds.Dy()
	if srcW <= 0 || srcH <= 0 {
		return nil, fmt.Errorf("invalid raster dimensions")
	}

	detailLevel := normalizeVectorizerDetailLevel(cfg.DetailLevel)
	if cfg.HighFidelity && detailLevel != "high" {
		detailLevel = "high"
	}

	targetMax := vectorizerTargetDimension(detailLevel, cfg.HighFidelity)
	outW, outH := srcW, srcH
	maxDim := srcW
	if srcH > maxDim {
		maxDim = srcH
	}
	if maxDim > targetMax {
		scale := float64(targetMax) / float64(maxDim)
		outW = int(math.Round(float64(srcW) * scale))
		outH = int(math.Round(float64(srcH) * scale))
		if outW < 1 {
			outW = 1
		}
		if outH < 1 {
			outH = 1
		}
	}

	pixels := make([]rgbaKey, outW*outH)
	colorCounts := make(map[rgbaKey]int)
	for y := 0; y < outH; y++ {
		srcY := bounds.Min.Y + y*srcH/outH
		for x := 0; x < outW; x++ {
			srcX := bounds.Min.X + x*srcW/outW
			r16, g16, b16, a16 := img.At(srcX, srcY).RGBA()
			key := quantizeVectorizerColor(
				uint8(r16>>8),
				uint8(g16>>8),
				uint8(b16>>8),
				uint8(a16>>8),
				cfg,
			)
			pixels[y*outW+x] = key
			if key.A > 0 {
				colorCounts[key]++
			}
		}
	}

	if len(colorCounts) == 0 {
		return nil, fmt.Errorf("no visible pixels for vectorization")
	}

	orderedColors := make([]rgbaKey, 0, len(colorCounts))
	for key := range colorCounts {
		orderedColors = append(orderedColors, key)
	}
	sort.Slice(orderedColors, func(i, j int) bool {
		left := orderedColors[i]
		right := orderedColors[j]
		if colorCounts[left] != colorCounts[right] {
			return colorCounts[left] > colorCounts[right]
		}
		if left.A != right.A {
			return left.A < right.A
		}
		if left.R != right.R {
			return left.R < right.R
		}
		if left.G != right.G {
			return left.G < right.G
		}
		return left.B < right.B
	})

	var svg strings.Builder
	svg.Grow(outW * outH * 6)
	svg.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	svg.WriteString(fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="geometricPrecision">`, outW, outH))

	mask := make([]bool, outW*outH)
	minLoopArea := vectorizerLoopMinArea(detailLevel, outW, outH, cfg.HighFidelity)
	for _, color := range orderedColors {
		for i := range mask {
			mask[i] = pixels[i] == color
		}

		edges := buildVectorMaskEdges(mask, outW, outH)
		if len(edges) == 0 {
			continue
		}
		loops := traceVectorLoops(edges)
		if len(loops) == 0 {
			continue
		}

		pathData := buildVectorPathData(loops, detailLevel, minLoopArea, cfg.HighFidelity)
		if strings.TrimSpace(pathData) == "" {
			continue
		}

		if color.A < 255 {
			svg.WriteString(fmt.Sprintf(
				`<path d="%s" fill="#%02x%02x%02x" fill-opacity="%.3f" fill-rule="evenodd"/>`,
				pathData, color.R, color.G, color.B, float64(color.A)/255,
			))
		} else {
			svg.WriteString(fmt.Sprintf(
				`<path d="%s" fill="#%02x%02x%02x" fill-rule="evenodd"/>`,
				pathData, color.R, color.G, color.B,
			))
		}
	}

	svg.WriteString(`</svg>`)
	return []byte(svg.String()), nil
}

func (s *GeneratorService) saveVectorizedSVG(ctx context.Context, taskID, dateDir string, svgData []byte) (string, error) {
	cfg := globals.GraConf.Generator.Storage
	filename := fmt.Sprintf("%s_vectorized.svg", taskID)
	uid, toolID, err := s.requireTaskStorageIdentity(taskID)
	if err != nil {
		return "", err
	}

	switch cfg.Type {
	case "r2":
		store, ok, err := storageService.NewObjectStoreFromGeneratorConfig(cfg)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("invalid object storage config")
		}
		key := buildGeneratedObjectKey(cfg, uid, "asset", dateDir, taskID, filename)
		result, err := store.Put(ctx, &storageService.PutObjectRequest{
			Key:          key,
			Body:         bytes.NewReader(svgData),
			Size:         int64(len(svgData)),
			ContentType:  "image/svg+xml",
			CacheControl: objectCacheControl("asset"),
			Metadata:     buildObjectMetadata(uid, taskID, toolID, "asset", ""),
		})
		if err != nil {
			return "", err
		}
		if err := (&GenerationObjectService{}).Register(&RegisterGenerationObjectParams{
			UID:         uid,
			TaskID:      taskID,
			ToolID:      toolID,
			Provider:    result.Provider,
			Bucket:      result.Bucket,
			ObjectKey:   result.Key,
			AssetKind:   "asset",
			ContentType: "image/svg+xml",
			SizeBytes:   int64(len(svgData)),
			ETag:        result.ETag,
			PublicURL:   result.PublicURL,
		}); err != nil {
			globals.Warn(fmt.Sprintf("[GeneratorService] Failed to register vectorized svg %s: %v", result.Key, err))
		}
		return result.PublicURL, nil
	default:
		localCfg := cfg.Local
		storagePath := localCfg.Path
		if storagePath == "" {
			storagePath = "./uploads/generations"
		}
		relativeDir := generatedLocalRelativeDir(uid, "asset", dateDir)
		fullPath := filepath.Join(storagePath, relativeDir)
		if err := os.MkdirAll(fullPath, 0755); err != nil {
			return "", fmt.Errorf("failed to create storage directory: %v", err)
		}
		filePath := filepath.Join(fullPath, filename)
		if err := os.WriteFile(filePath, svgData, 0644); err != nil {
			return "", fmt.Errorf("failed to save vectorized svg: %v", err)
		}

		urlPrefix := localCfg.URLPrefix
		if urlPrefix == "" {
			urlPrefix = "/uploads/generations"
		}
		localURL := fmt.Sprintf("%s/%s/%s", urlPrefix, filepath.ToSlash(relativeDir), filename)
		if err := s.registerLocalGenerationObject(uid, taskID, toolID, "asset", "image/svg+xml", int64(len(svgData)), filepath.ToSlash(filepath.Join(relativeDir, filename)), localURL); err != nil {
			globals.Warn(fmt.Sprintf("[GeneratorService] Failed to register local vectorized svg %s: %v", filename, err))
		}
		return localURL, nil
	}
}

func vectorizeImageDataWithEngine(imageData []byte, cfg vectorizerPostProcessConfig) ([]byte, string, error) {
	svgData, err := rasterToVectorizedSVGViaVTracer(imageData, cfg)
	if err == nil {
		return svgData, "vtracer", nil
	}
	globals.Warn(fmt.Sprintf("[Vectorizer] VTracer engine failed, falling back to legacy: %v", err))
	svgData, err = rasterToVectorizedSVG(imageData, cfg)
	if err != nil {
		return nil, "", err
	}
	return svgData, "legacy", nil
}

// vectorizeImageData attempts VTracer first, then falls back to the legacy engine.
func vectorizeImageData(imageData []byte, cfg vectorizerPostProcessConfig) ([]byte, error) {
	svgData, _, err := vectorizeImageDataWithEngine(imageData, cfg)
	return svgData, err
}

func (s *GeneratorService) generateVectorizerSVG(ctx context.Context, taskID string, sourceImageData [][]byte, sourceImageURLs []string, cfg vectorizerPostProcessConfig) (string, error) {
	for _, imageData := range sourceImageData {
		if len(imageData) == 0 {
			continue
		}
		svgData, err := vectorizeImageData(imageData, cfg)
		if err != nil {
			continue
		}
		dateDir := time.Now().Format("2006/01/02")
		return s.saveVectorizedSVG(ctx, taskID, dateDir, svgData)
	}

	if len(sourceImageURLs) == 0 {
		return "", fmt.Errorf("no source images available")
	}

	var lastErr error
	for _, imageURL := range sourceImageURLs {
		if strings.TrimSpace(imageURL) == "" || isSVGURL(imageURL) {
			continue
		}
		_, data, err := s.loadReferenceImageBytes(ctx, imageURL)
		if err != nil {
			lastErr = err
			continue
		}
		svgData, err := vectorizeImageData(data, cfg)
		if err != nil {
			lastErr = err
			continue
		}
		dateDir := time.Now().Format("2006/01/02")
		return s.saveVectorizedSVG(ctx, taskID, dateDir, svgData)
	}

	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("no raster image available for vectorization")
}

func (s *GeneratorService) buildProviderReferenceMedia(refs []model.ReferenceMediaParam) []provider.ReferenceMediaData {
	if len(refs) == 0 {
		return nil
	}
	out := make([]provider.ReferenceMediaData, 0, len(refs))
	for _, ref := range refs {
		url := strings.TrimSpace(ref.URL)
		if url == "" {
			continue
		}
		out = append(out, provider.ReferenceMediaData{
			URL:      url,
			MimeType: strings.TrimSpace(ref.MimeType),
		})
	}
	return out
}

// mergeInjectedReferenceImages prepends asset-injected references in
// front of the user-supplied ones, dedup-by-URL. Prepending keeps
// character sheets (the continuity signal) at the head of the list
// where providers that clamp the count will still see them.
func mergeInjectedReferenceImages(user []model.ReferenceImageParam, injected []canvas.ReferenceImage) []model.ReferenceImageParam {
	if len(injected) == 0 {
		return user
	}
	seen := make(map[string]struct{}, len(user)+len(injected))
	out := make([]model.ReferenceImageParam, 0, len(user)+len(injected))
	for _, ref := range injected {
		url := strings.TrimSpace(ref.URL)
		if url == "" {
			continue
		}
		if _, dup := seen[url]; dup {
			continue
		}
		seen[url] = struct{}{}
		weight := ref.Weight
		if weight <= 0 {
			weight = 1
		}
		out = append(out, model.ReferenceImageParam{
			ID:     ref.Label,
			URL:    url,
			Weight: float32(weight),
		})
	}
	for _, ref := range user {
		url := strings.TrimSpace(ref.URL)
		if url == "" {
			out = append(out, ref)
			continue
		}
		if _, dup := seen[url]; dup {
			continue
		}
		seen[url] = struct{}{}
		out = append(out, ref)
	}
	return out
}

func (s *GeneratorService) buildProviderReferenceImages(ctx context.Context, refImages []model.ReferenceImageParam) ([]provider.ReferenceImageData, error) {
	if len(refImages) == 0 {
		return nil, nil
	}

	out := make([]provider.ReferenceImageData, 0, len(refImages))
	for _, img := range refImages {
		url := strings.TrimSpace(img.URL)
		if url == "" {
			continue
		}

		mimeType, data, err := s.loadReferenceImageBytes(ctx, url)
		if err != nil {
			return nil, err
		}

		weight := img.Weight
		if math.IsNaN(float64(weight)) || weight <= 0 {
			weight = 1
		}
		if weight < 0.1 {
			weight = 0.1
		}
		if weight > 2 {
			weight = 2
		}
		out = append(out, provider.ReferenceImageData{
			ID:       strings.TrimSpace(img.ID),
			MimeType: mimeType,
			Data:     base64.StdEncoding.EncodeToString(data),
			Weight:   weight,
			URL:      url,
		})
	}

	return out, nil
}

func validateRemoteURLHostAllowlist(host string, allowlist []string) bool {
	if len(allowlist) == 0 {
		return true
	}
	host = strings.ToLower(strings.TrimSpace(host))
	for _, entry := range allowlist {
		normalized := strings.ToLower(strings.TrimSpace(entry))
		if normalized == "" {
			continue
		}
		if host == normalized || strings.HasSuffix(host, "."+normalized) {
			return true
		}
	}
	return false
}

func isUnsafeRemoteURLIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() ||
		ip.String() == "169.254.169.254" ||
		strings.HasPrefix(ip.String(), "169.254.")
}

func lookupSafeRemoteIPs(ctx context.Context, host, resourceLabel string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		if isUnsafeRemoteURLIP(ip) {
			globals.Warn(fmt.Sprintf("Blocked private/reserved IP for %s: %s", resourceLabel, ip.String()))
			return nil, fmt.Errorf("invalid %s url", resourceLabel)
		}
		return []net.IP{ip}, nil
	}

	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	addrs, err := net.DefaultResolver.LookupIPAddr(lookupCtx, host)
	if err != nil {
		globals.Warn(fmt.Sprintf("Failed to resolve %s host %s: %v", resourceLabel, host, err))
		return nil, fmt.Errorf("invalid %s url", resourceLabel)
	}
	if len(addrs) == 0 {
		globals.Warn(fmt.Sprintf("Resolved %s host %s to no addresses", resourceLabel, host))
		return nil, fmt.Errorf("invalid %s url", resourceLabel)
	}

	ips := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		ip := addr.IP
		if isUnsafeRemoteURLIP(ip) {
			ipString := "<nil>"
			if ip != nil {
				ipString = ip.String()
			}
			globals.Warn(fmt.Sprintf("DNS resolved %s to private/reserved IP: %s -> %s", resourceLabel, host, ipString))
			return nil, fmt.Errorf("invalid %s url", resourceLabel)
		}
		ips = append(ips, ip)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("invalid %s url", resourceLabel)
	}
	return ips, nil
}

func safeRemoteDialContext(ctx context.Context, network, address, resourceLabel string, allowlist []string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	normalizedHost := strings.ToLower(strings.TrimSpace(host))
	if !validateRemoteURLHostAllowlist(normalizedHost, allowlist) {
		globals.Warn(fmt.Sprintf("Blocked %s host outside allowlist during dial: %s", resourceLabel, normalizedHost))
		return nil, fmt.Errorf("invalid %s url", resourceLabel)
	}
	ips, err := lookupSafeRemoteIPs(ctx, normalizedHost, resourceLabel)
	if err != nil {
		return nil, err
	}
	dialer := net.Dialer{Timeout: 10 * time.Second}
	return dialer.DialContext(ctx, "tcp", net.JoinHostPort(ips[0].String(), port))
}

func (s *GeneratorService) validateRemoteHTTPURL(ctx context.Context, parsed *url.URL, resourceLabel string, allowlist []string) error {
	if parsed == nil || parsed.Hostname() == "" {
		return fmt.Errorf("invalid %s url", resourceLabel)
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		globals.Warn(fmt.Sprintf("Blocked non-HTTP(S) protocol for %s: %s", resourceLabel, parsed.Scheme))
		return fmt.Errorf("invalid %s url", resourceLabel)
	}

	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if !validateRemoteURLHostAllowlist(host, allowlist) {
		globals.Warn(fmt.Sprintf("Blocked %s host outside allowlist: %s", resourceLabel, host))
		return fmt.Errorf("invalid %s url", resourceLabel)
	}

	blockedHosts := []string{
		"localhost",
		"127.0.0.1",
		"0.0.0.0",
		"[::1]",
		"::1",
	}
	for _, blocked := range blockedHosts {
		if host == blocked {
			globals.Warn(fmt.Sprintf("Blocked localhost access attempt for %s: %s", resourceLabel, host))
			return fmt.Errorf("invalid %s url", resourceLabel)
		}
	}

	_, err := lookupSafeRemoteIPs(ctx, host, resourceLabel)
	return err
}

func (s *GeneratorService) validateRemoteReferenceImageURL(ctx context.Context, parsed *url.URL) error {
	return s.validateRemoteHTTPURL(ctx, parsed, "reference image", nil)
}

func (s *GeneratorService) validateRemoteAssetURL(ctx context.Context, parsed *url.URL) error {
	return s.validateRemoteHTTPURL(ctx, parsed, "remote asset", GetAllowedRemoteAssetHosts())
}

func resolveReferenceStorageBase(localCfg config.LocalStorage) string {
	storagePath := strings.TrimSpace(localCfg.Path)
	if storagePath == "" {
		storagePath = "./uploads/generations"
	}
	cleanStoragePath := filepath.Clean(storagePath)
	if filepath.Base(cleanStoragePath) == "generations" {
		return filepath.Dir(cleanStoragePath)
	}
	return cleanStoragePath
}

func resolveLegacyReferenceStorageBase(localCfg config.LocalStorage) string {
	storagePath := strings.TrimSpace(localCfg.Path)
	if storagePath == "" {
		storagePath = "./uploads/generations"
	}
	return filepath.Clean(storagePath)
}

func resolveReferenceURLBase(localCfg config.LocalStorage) string {
	urlPrefix := strings.TrimSpace(localCfg.URLPrefix)
	if urlPrefix == "" {
		urlPrefix = "/uploads/generations"
	}
	cleanURLPrefix := strings.TrimRight(urlPrefix, "/")
	if strings.HasSuffix(cleanURLPrefix, "/generations") {
		return strings.TrimSuffix(cleanURLPrefix, "/generations")
	}
	return cleanURLPrefix
}

func referenceImageDiskRoot(localCfg config.LocalStorage) string {
	return filepath.Join(resolveReferenceStorageBase(localCfg), "references")
}

func referenceImageLegacyDiskRoot(localCfg config.LocalStorage) string {
	return filepath.Join(resolveLegacyReferenceStorageBase(localCfg), "reference-images")
}

func referenceImageURLPrefix(localCfg config.LocalStorage) string {
	return resolveReferenceURLBase(localCfg) + "/references"
}

func referenceImageLegacyURLPrefix(localCfg config.LocalStorage) string {
	base := resolveReferenceURLBase(localCfg)
	if base == "" {
		base = "/uploads"
	}
	return base + "/generations/reference-images"
}

func resolveLocalReferenceRelativePath(rawURL string, prefixes ...string) (string, bool) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", false
	}

	var candidatePath string
	if strings.HasPrefix(trimmed, "/") {
		candidatePath = trimmed
	} else if parsed, err := url.Parse(trimmed); err == nil && parsed.Path != "" {
		candidatePath = parsed.Path
	}

	if candidatePath == "" {
		return "", false
	}

	for _, prefix := range prefixes {
		prefix = strings.TrimRight(strings.TrimSpace(prefix), "/")
		if prefix == "" ||
			(candidatePath != prefix && !strings.HasPrefix(candidatePath, prefix+"/")) {
			continue
		}

		rel := strings.TrimPrefix(candidatePath, prefix)
		rel = strings.TrimPrefix(rel, "/")
		return rel, true
	}
	return "", false
}

func decodeInlineReferenceImageData(rawURL string, maxSize int64) (string, []byte, bool, error) {
	trimmed := strings.TrimSpace(rawURL)
	if !strings.HasPrefix(strings.ToLower(trimmed), "data:image/") {
		return "", nil, false, nil
	}

	commaIndex := strings.Index(trimmed, ",")
	if commaIndex <= 0 {
		return "", nil, true, fmt.Errorf("invalid reference image data url")
	}
	meta := trimmed[:commaIndex]
	payload := trimmed[commaIndex+1:]
	if !strings.Contains(strings.ToLower(meta), ";base64") {
		return "", nil, true, fmt.Errorf("invalid reference image data url")
	}

	mimeType := strings.TrimSpace(strings.TrimPrefix(strings.SplitN(meta, ";", 2)[0], "data:"))
	if mimeType == "" || !strings.HasPrefix(strings.ToLower(mimeType), "image/") {
		return "", nil, true, fmt.Errorf("invalid reference image type")
	}

	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", nil, true, fmt.Errorf("invalid reference image data url")
	}
	if maxSize > 0 && int64(len(data)) > maxSize {
		return "", nil, true, fmt.Errorf("reference image too large")
	}
	return mimeType, data, true, nil
}

func readLocalReferenceImage(storagePath string, rel string, rawURL string, maxSize int64) (string, []byte, error) {
	if strings.Contains(rel, "..") || strings.Contains(rel, "~") {
		return "", nil, fmt.Errorf("invalid reference image path")
	}

	cleanRel := path.Clean("/" + rel)
	if strings.HasPrefix(cleanRel, "/..") || cleanRel == "/.." {
		return "", nil, fmt.Errorf("invalid reference image path")
	}

	fullPath := filepath.Join(storagePath, strings.TrimPrefix(cleanRel, "/"))
	baseAbs, err := filepath.Abs(storagePath)
	if err != nil {
		globals.Error(fmt.Sprintf("Failed to resolve base path: %v", err))
		return "", nil, fmt.Errorf("failed to resolve reference image base path")
	}
	fullAbs, err := filepath.Abs(fullPath)
	if err != nil {
		globals.Error(fmt.Sprintf("Failed to resolve file path: %v", err))
		return "", nil, fmt.Errorf("failed to resolve reference image path")
	}
	if fullAbs != baseAbs && !strings.HasPrefix(fullAbs, baseAbs+string(os.PathSeparator)) {
		globals.Warn(fmt.Sprintf("Path traversal attempt detected: %s -> %s", rawURL, fullAbs))
		return "", nil, fmt.Errorf("invalid reference image path")
	}

	f, err := os.Open(fullPath)
	if err != nil {
		return "", nil, err
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil {
		return "", nil, fmt.Errorf("failed to read reference image: %v", err)
	}
	if int64(len(data)) > maxSize {
		return "", nil, fmt.Errorf("reference image too large")
	}

	mimeType := http.DetectContentType(data)
	if !strings.HasPrefix(mimeType, "image/") {
		return "", nil, fmt.Errorf("invalid reference image type")
	}
	return mimeType, data, nil
}

func (s *GeneratorService) loadReferenceImageBytes(ctx context.Context, imageURL string) (string, []byte, error) {
	cfg := globals.GraConf.Generator.Storage
	localCfg := cfg.Local
	referenceURLPrefix := referenceImageURLPrefix(localCfg)
	legacyURLPrefix := referenceImageLegacyURLPrefix(localCfg)
	maxSize := GetMaxReferenceImageSize()

	if mimeType, data, ok, err := decodeInlineReferenceImageData(imageURL, maxSize); ok {
		if err != nil {
			return "", nil, err
		}
		return mimeType, data, nil
	}

	if rel, ok := resolveLocalReferenceRelativePath(imageURL, referenceURLPrefix, legacyURLPrefix); ok {
		candidates := []string{
			referenceImageDiskRoot(localCfg),
			referenceImageLegacyDiskRoot(localCfg),
		}
		for _, baseDir := range candidates {
			baseDir = filepath.Clean(baseDir)
			if baseDir == "." || baseDir == "" {
				continue
			}
			if mimeType, data, err := readLocalReferenceImage(baseDir, rel, imageURL, maxSize); err == nil {
				return mimeType, data, nil
			} else if !os.IsNotExist(err) {
				return "", nil, err
			}
		}
	}

	if rel, ok := resolveLocalReferenceRelativePath(imageURL, "/uploads"); ok {
		if mimeType, data, err := readLocalReferenceImage("./uploads", rel, imageURL, maxSize); err == nil {
			return mimeType, data, nil
		} else if !os.IsNotExist(err) {
			return "", nil, err
		}
	}

	normalizedURL := strings.TrimSpace(imageURL)
	if strings.HasPrefix(normalizedURL, "/") {
		backendURL := strings.TrimSuffix(strings.TrimSpace(globals.GraConf.System.BackendURL), "/")
		if backendURL != "" {
			normalizedURL = backendURL + normalizedURL
		}
	}

	parsed, err := url.Parse(normalizedURL)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", nil, fmt.Errorf("invalid reference image url")
	}

	backendParsed, backendParseErr := url.Parse(strings.TrimSpace(globals.GraConf.System.BackendURL))
	allowBackendReferencePath := backendParseErr == nil &&
		backendParsed != nil &&
		backendParsed.Hostname() != "" &&
		strings.EqualFold(parsed.Hostname(), backendParsed.Hostname()) &&
		(strings.HasPrefix(parsed.Path, referenceURLPrefix) || strings.HasPrefix(parsed.Path, legacyURLPrefix))

	if !allowBackendReferencePath {
		if err := s.validateRemoteReferenceImageURL(ctx, parsed); err != nil {
			return "", nil, err
		}
	}

	client := &http.Client{
		Timeout: 20 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			if allowBackendReferencePath {
				return nil
			}
			return s.validateRemoteReferenceImageURL(ctx, req.URL)
		},
	}
	if !allowBackendReferencePath {
		client.Transport = &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				return safeRemoteDialContext(ctx, network, address, "reference image", nil)
			},
		}
	}
	httpReq, err := http.NewRequestWithContext(ctx, "GET", normalizedURL, nil)
	if err != nil {
		return "", nil, fmt.Errorf("failed to create reference image request: %v", err)
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return "", nil, fmt.Errorf("failed to download reference image: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", nil, fmt.Errorf("failed to download reference image: status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return "", nil, fmt.Errorf("failed to read reference image: %v", err)
	}
	if int64(len(data)) > maxSize {
		return "", nil, fmt.Errorf("reference image too large")
	}

	mimeType := strings.TrimSpace(http.DetectContentType(data))
	if strings.Contains(mimeType, ";") {
		mimeType = strings.TrimSpace(strings.SplitN(mimeType, ";", 2)[0])
	}
	if !strings.HasPrefix(strings.ToLower(mimeType), "image/") {
		return "", nil, fmt.Errorf("invalid reference image type")
	}
	return mimeType, data, nil
}

// saveImages 保存图片到存储
func (s *GeneratorService) saveImages(ctx context.Context, taskID string, imageData [][]byte, imageURLs []string) ([]string, error) {
	cfg := globals.GraConf.Generator.Storage

	savedURLs := make([]string, 0)
	uid, toolID, err := s.requireTaskStorageIdentity(taskID)
	if err != nil {
		return nil, err
	}

	// 按日期组织目录
	dateDir := time.Now().Format("2006/01/02")

	switch cfg.Type {
	case "r2":
		store, ok, err := storageService.NewObjectStoreFromGeneratorConfig(cfg)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("invalid object storage config")
		}
		urls, err := s.saveToObjectStore(ctx, taskID, dateDir, imageData, store, uid, toolID)
		if err != nil {
			return nil, err
		}
		savedURLs = append(savedURLs, urls...)
	default: // local
		urls, err := s.saveToLocal(uid, taskID, dateDir, imageData)
		if err != nil {
			return nil, err
		}
		savedURLs = append(savedURLs, urls...)
	}

	if len(imageURLs) > 0 {
		urls, err := s.saveRemoteFiles(ctx, taskID, imageURLs, "image")
		if err != nil {
			return nil, err
		}
		savedURLs = append(savedURLs, urls...)
	}

	return savedURLs, nil
}

func (s *GeneratorService) saveGeneratedBinaryAssets(ctx context.Context, taskID string, assets [][]byte, contentTypes []string, assetKind string) ([]string, error) {
	if len(assets) == 0 {
		return nil, nil
	}

	cfg := globals.GraConf.Generator.Storage
	dateDir := time.Now().Format("2006/01/02")
	savedURLs := make([]string, 0, len(assets))
	uid, toolID, err := s.requireTaskStorageIdentity(taskID)
	if err != nil {
		return nil, err
	}
	var store storageService.ObjectStore
	if cfg.Type == "r2" {
		initializedStore, ok, err := storageService.NewObjectStoreFromGeneratorConfig(cfg)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("invalid object storage config")
		}
		store = initializedStore
	}

	for index, data := range assets {
		if len(data) == 0 {
			return savedURLs, fmt.Errorf("empty %s asset at index %d", assetKind, index)
		}
		contentType := ""
		if index < len(contentTypes) {
			contentType = strings.TrimSpace(contentTypes[index])
		}
		if contentType == "" {
			contentType = http.DetectContentType(data)
		}

		filename := buildStoredAssetFilename(taskID, index, "", contentType, assetKind)
		switch cfg.Type {
		case "r2":
			key := buildGeneratedObjectKey(cfg, uid, assetKind, dateDir, taskID, filename)
			result, err := store.Put(ctx, &storageService.PutObjectRequest{
				Key:          key,
				Body:         bytes.NewReader(data),
				Size:         int64(len(data)),
				ContentType:  contentType,
				CacheControl: objectCacheControl(assetKind),
				Metadata:     buildObjectMetadata(uid, taskID, toolID, assetKind, ""),
			})
			if err != nil {
				return savedURLs, err
			}
			savedURLs = append(savedURLs, result.PublicURL)
			if err := (&GenerationObjectService{}).Register(&RegisterGenerationObjectParams{
				UID:         uid,
				TaskID:      taskID,
				ToolID:      toolID,
				Provider:    result.Provider,
				Bucket:      result.Bucket,
				ObjectKey:   result.Key,
				AssetKind:   assetKind,
				ContentType: contentType,
				SizeBytes:   int64(len(data)),
				ETag:        result.ETag,
				PublicURL:   result.PublicURL,
			}); err != nil {
				globals.Warn(fmt.Sprintf("[GeneratorService] Failed to register %s object %s: %v", assetKind, result.Key, err))
			}
		default:
			localURL, err := s.saveBinaryToLocal(uid, taskID, toolID, assetKind, dateDir, filename, data, contentType)
			if err != nil {
				return savedURLs, err
			}
			savedURLs = append(savedURLs, localURL)
		}
	}

	return savedURLs, nil
}

func (s *GeneratorService) saveGeneratedBinaryAssetsLocalOnly(taskID string, assets [][]byte, contentTypes []string, assetKind string) ([]string, error) {
	if len(assets) == 0 {
		return nil, nil
	}

	dateDir := time.Now().Format("2006/01/02")
	savedURLs := make([]string, 0, len(assets))
	uid, _, err := s.requireTaskStorageIdentity(taskID)
	if err != nil {
		return nil, err
	}

	for index, data := range assets {
		if len(data) == 0 {
			return savedURLs, fmt.Errorf("empty %s asset at index %d", assetKind, index)
		}
		contentType := ""
		if index < len(contentTypes) {
			contentType = strings.TrimSpace(contentTypes[index])
		}
		if contentType == "" {
			contentType = http.DetectContentType(data)
		}

		filename := buildStoredAssetFilename(taskID, index, "", contentType, assetKind)
		localURL, err := s.saveBinaryToLocal(uid, taskID, "", assetKind, dateDir, filename, data, contentType)
		if err != nil {
			return savedURLs, err
		}
		savedURLs = append(savedURLs, localURL)
	}

	return savedURLs, nil
}

func (s *GeneratorService) saveRemoteFiles(ctx context.Context, taskID string, remoteURLs []string, assetKind string) ([]string, error) {
	if len(remoteURLs) == 0 {
		return nil, nil
	}

	cfg := globals.GraConf.Generator.Storage
	dateDir := time.Now().Format("2006/01/02")
	savedURLs := make([]string, 0, len(remoteURLs))
	failedIndexes := make([]int, 0)
	uid, toolID, err := s.requireTaskStorageIdentity(taskID)
	if err != nil {
		return nil, err
	}
	var store storageService.ObjectStore
	if cfg.Type == "r2" {
		initializedStore, ok, err := storageService.NewObjectStoreFromGeneratorConfig(cfg)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("invalid object storage config")
		}
		store = initializedStore
	}

	for index, remoteURL := range remoteURLs {
		trimmedURL := strings.TrimSpace(remoteURL)
		if trimmedURL == "" {
			failedIndexes = append(failedIndexes, index)
			continue
		}
		if strings.HasPrefix(trimmedURL, "/") || (!strings.HasPrefix(trimmedURL, "http://") && !strings.HasPrefix(trimmedURL, "https://")) {
			savedURLs = append(savedURLs, trimmedURL)
			continue
		}
		asset, err := s.openRemoteAsset(ctx, trimmedURL)
		if err != nil {
			globals.Warn(fmt.Sprintf("[GeneratorService] Failed to download %s asset %s: %v", assetKind, trimmedURL, err))
			failedIndexes = append(failedIndexes, index)
			continue
		}

		filename := buildStoredAssetFilename(taskID, index, trimmedURL, asset.ContentType, assetKind)
		switch cfg.Type {
		case "r2":
			key := buildGeneratedObjectKey(cfg, uid, assetKind, dateDir, taskID, filename)
			result, err := store.Put(ctx, &storageService.PutObjectRequest{
				Key:          key,
				Body:         asset.Body,
				Size:         asset.Size,
				ContentType:  asset.ContentType,
				CacheControl: objectCacheControl(assetKind),
				Metadata:     buildObjectMetadata(uid, taskID, toolID, assetKind, trimmedURL),
			})
			_ = asset.Body.Close()
			if err != nil {
				globals.Warn(fmt.Sprintf("[GeneratorService] Failed to upload remote %s asset %s: %v", assetKind, trimmedURL, err))
				failedIndexes = append(failedIndexes, index)
				continue
			}
			savedURLs = append(savedURLs, result.PublicURL)
			if err := (&GenerationObjectService{}).Register(&RegisterGenerationObjectParams{
				UID:         uid,
				TaskID:      taskID,
				ToolID:      toolID,
				Provider:    result.Provider,
				Bucket:      result.Bucket,
				ObjectKey:   result.Key,
				AssetKind:   assetKind,
				ContentType: asset.ContentType,
				SizeBytes:   asset.Size,
				ETag:        result.ETag,
				PublicURL:   result.PublicURL,
				SourceURL:   trimmedURL,
			}); err != nil {
				globals.Warn(fmt.Sprintf("[GeneratorService] Failed to register remote %s object %s: %v", assetKind, result.Key, err))
			}
		default:
			data, readErr := io.ReadAll(asset.Body)
			_ = asset.Body.Close()
			if readErr != nil {
				globals.Warn(fmt.Sprintf("[GeneratorService] Failed to read remote %s asset %s: %v", assetKind, trimmedURL, readErr))
				failedIndexes = append(failedIndexes, index)
				continue
			}
			localURL, err := s.saveBinaryToLocal(uid, taskID, toolID, assetKind, dateDir, filename, data, asset.ContentType)
			if err != nil {
				globals.Warn(fmt.Sprintf("[GeneratorService] Failed to save remote %s asset %s: %v", assetKind, trimmedURL, err))
				failedIndexes = append(failedIndexes, index)
				continue
			}
			savedURLs = append(savedURLs, localURL)
		}
	}

	if len(failedIndexes) > 0 {
		return savedURLs, fmt.Errorf("failed to save %d/%d remote assets", len(failedIndexes), len(remoteURLs))
	}

	return savedURLs, nil
}

func (s *GeneratorService) generateAndSaveVideoThumbnail(ctx context.Context, taskID, videoURL string) (string, error) {
	videoURL = strings.TrimSpace(videoURL)
	if videoURL == "" {
		return "", fmt.Errorf("video url is empty")
	}

	thumbnailData, err := s.extractVideoThumbnailPNG(ctx, videoURL, nil)
	if err != nil {
		return "", err
	}

	savedURLs, err := s.saveGeneratedBinaryAssets(ctx, taskID, [][]byte{thumbnailData}, []string{"image/png"}, "thumbnail")
	if err != nil {
		return "", err
	}
	if len(savedURLs) == 0 {
		return "", fmt.Errorf("thumbnail save returned no url")
	}
	return savedURLs[0], nil
}

func (s *GeneratorService) GenerateAndSaveVideoThumbnail(ctx context.Context, taskID, videoURL string) (string, error) {
	return s.generateAndSaveVideoThumbnail(ctx, taskID, videoURL)
}

func (s *GeneratorService) GenerateAndSaveVideoThumbnailAtTimestamp(ctx context.Context, taskID, videoURL string, timestampSeconds float64) (string, error) {
	videoURL = strings.TrimSpace(videoURL)
	if videoURL == "" {
		return "", fmt.Errorf("video url is empty")
	}

	if timestampSeconds < 0 {
		timestampSeconds = 0
	}

	thumbnailData, err := s.extractVideoThumbnailPNG(ctx, videoURL, &timestampSeconds)
	if err != nil {
		return "", err
	}

	savedURLs, err := s.saveGeneratedBinaryAssets(ctx, taskID, [][]byte{thumbnailData}, []string{"image/png"}, "thumbnail")
	if err != nil {
		return "", err
	}
	if len(savedURLs) == 0 {
		return "", fmt.Errorf("thumbnail save returned no url")
	}
	return savedURLs[0], nil
}

func (s *GeneratorService) extractVideoThumbnailPNG(ctx context.Context, videoURL string, timestampSeconds *float64) ([]byte, error) {
	inputPath, cleanup, err := s.prepareVideoThumbnailInput(ctx, videoURL)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	tempDir, err := os.MkdirTemp("", "nanobanana-video-thumb-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tempDir)

	outputPath := filepath.Join(tempDir, "thumbnail.png")
	args := []string{"-y"}
	if timestampSeconds != nil {
		args = append(args, "-ss", strconv.FormatFloat(*timestampSeconds, 'f', 3, 64))
	}
	args = append(args, "-i", inputPath)
	if timestampSeconds != nil {
		args = append(args,
			"-vf", "scale=1280:-1:force_original_aspect_ratio=decrease",
			"-frames:v", "1",
			outputPath,
		)
	} else {
		args = append(args,
			"-vf", "thumbnail,scale=1280:-1:force_original_aspect_ratio=decrease",
			"-frames:v", "1",
			outputPath,
		)
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		message := strings.TrimSpace(string(output))
		if len(message) > 240 {
			message = message[:240]
		}
		return nil, fmt.Errorf("ffmpeg thumbnail extraction failed: %s", message)
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("empty thumbnail output")
	}
	return data, nil
}

func (s *GeneratorService) prepareVideoThumbnailInput(ctx context.Context, videoURL string) (string, func(), error) {
	if localPath, ok, err := ResolveLocalGeneratedResultPathForRead(videoURL); err != nil {
		return "", nil, err
	} else if ok {
		return localPath, func() {}, nil
	}

	contentType := ""
	var body io.ReadCloser
	if stream, err := s.openGeneratedVideoStream(ctx, videoURL); err == nil {
		body = stream.Body
		contentType = stream.ContentType
	} else {
		return "", nil, err
	}

	tempDir, err := os.MkdirTemp("", "nanobanana-video-source-*")
	if err != nil {
		_ = body.Close()
		return "", nil, err
	}

	ext := detectStoredAssetExtension(videoURL, contentType, "video")
	inputPath := filepath.Join(tempDir, "input"+ext)
	file, err := os.Create(inputPath)
	if err != nil {
		_ = body.Close()
		_ = os.RemoveAll(tempDir)
		return "", nil, err
	}

	_, copyErr := io.Copy(file, body)
	closeErr := file.Close()
	bodyCloseErr := body.Close()
	if copyErr != nil {
		_ = os.RemoveAll(tempDir)
		return "", nil, copyErr
	}
	if closeErr != nil {
		_ = os.RemoveAll(tempDir)
		return "", nil, closeErr
	}
	if bodyCloseErr != nil {
		_ = os.RemoveAll(tempDir)
		return "", nil, bodyCloseErr
	}

	return inputPath, func() { _ = os.RemoveAll(tempDir) }, nil
}

func (s *GeneratorService) openGeneratedVideoStream(ctx context.Context, videoURL string) (*remoteAssetStream, error) {
	videoURL = strings.TrimSpace(videoURL)
	if videoURL == "" {
		return nil, fmt.Errorf("video url is empty")
	}

	var object model.GenerationObject
	err := globals.GraDBs["system"].
		Where("public_url = ? AND status IN ?", videoURL, []int8{model.GenerationObjectStatusActive, model.GenerationObjectStatusHidden}).
		First(&object).Error
	if err == nil {
		store, ok, storeErr := storageService.NewObjectStoreForProviderBucket(globals.GraConf.Generator.Storage, object.Provider, object.Bucket)
		if storeErr != nil {
			return nil, storeErr
		}
		if ok && store != nil {
			reader, contentType, getErr := store.Get(ctx, object.ObjectKey)
			if getErr != nil {
				return nil, getErr
			}
			return &remoteAssetStream{
				Body:        reader,
				ContentType: strings.TrimSpace(contentType),
				Size:        object.SizeBytes,
			}, nil
		}
	}

	return s.openRemoteAsset(ctx, videoURL)
}

// saveToLocal 保存到本地存储
func storageUIDPathSegment(uid int) string {
	return filepath.Join("uid", strconv.Itoa(uid))
}

func generatedLocalRelativeDir(uid int, assetKind, dateDir string) string {
	category := generatedObjectCategory(assetKind)
	return filepath.Join(storageUIDPathSegment(uid), category, dateDir)
}

func (s *GeneratorService) saveToLocal(uid int, taskID, dateDir string, imageData [][]byte) ([]string, error) {
	cfg := globals.GraConf.Generator.Storage.Local
	uidFromTask, toolID, err := s.requireTaskStorageIdentity(taskID)
	if err != nil {
		return nil, err
	}
	if uidFromTask != uid {
		uid = uidFromTask
	}

	storagePath := cfg.Path
	if storagePath == "" {
		storagePath = "./uploads/generations"
	}

	relativeDir := generatedLocalRelativeDir(uid, "image", dateDir)
	fullPath := filepath.Join(storagePath, relativeDir)
	if err := os.MkdirAll(fullPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create storage directory: %v", err)
	}

	savedURLs := make([]string, 0)

	for i, data := range imageData {
		filename := fmt.Sprintf("%s_%d.png", taskID, i)
		filePath := filepath.Join(fullPath, filename)

		if err := os.WriteFile(filePath, data, 0644); err != nil {
			globals.Error(fmt.Sprintf("Failed to save image %s: %v", filename, err))
			continue
		}

		urlPrefix := cfg.URLPrefix
		if urlPrefix == "" {
			urlPrefix = "/uploads/generations"
		}
		imageURL := fmt.Sprintf("%s/%s/%s", urlPrefix, filepath.ToSlash(relativeDir), filename)
		savedURLs = append(savedURLs, imageURL)
		if err := s.registerLocalGenerationObject(uid, taskID, toolID, "image", "image/png", int64(len(data)), filepath.ToSlash(filepath.Join(relativeDir, filename)), imageURL); err != nil {
			globals.Warn(fmt.Sprintf("[GeneratorService] Failed to register local image object %s: %v", filename, err))
		}
	}

	return savedURLs, nil
}

func (s *GeneratorService) saveBinaryToLocal(uid int, taskID, toolID, assetKind, dateDir, filename string, data []byte, contentType string) (string, error) {
	cfg := globals.GraConf.Generator.Storage.Local
	storagePath := cfg.Path
	if storagePath == "" {
		storagePath = "./uploads/generations"
	}

	relativeDir := generatedLocalRelativeDir(uid, assetKind, dateDir)
	fullPath := filepath.Join(storagePath, relativeDir)
	if err := os.MkdirAll(fullPath, 0755); err != nil {
		return "", fmt.Errorf("failed to create storage directory: %v", err)
	}

	filePath := filepath.Join(fullPath, filename)
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return "", fmt.Errorf("failed to save file: %v", err)
	}

	urlPrefix := cfg.URLPrefix
	if urlPrefix == "" {
		urlPrefix = "/uploads/generations"
	}
	localURL := fmt.Sprintf("%s/%s/%s", urlPrefix, filepath.ToSlash(relativeDir), filename)
	if strings.TrimSpace(contentType) == "" {
		contentType = http.DetectContentType(data)
	}
	if err := s.registerLocalGenerationObject(uid, taskID, toolID, assetKind, contentType, int64(len(data)), filepath.ToSlash(filepath.Join(relativeDir, filename)), localURL); err != nil {
		globals.Warn(fmt.Sprintf("[GeneratorService] Failed to register local %s object %s: %v", assetKind, filename, err))
	}
	return localURL, nil
}

func (s *GeneratorService) registerLocalGenerationObject(uid int, taskID, toolID, assetKind, contentType string, sizeBytes int64, objectKey, publicURL string) error {
	objectKey = strings.Trim(strings.TrimSpace(objectKey), "/")
	taskID = strings.TrimSpace(taskID)
	toolID = strings.TrimSpace(toolID)
	if taskID == "" || objectKey == "" || strings.TrimSpace(publicURL) == "" {
		return nil
	}
	if uid <= 0 || toolID == "" {
		resolvedUID, resolvedToolID, err := s.requireTaskStorageIdentity(taskID)
		if err != nil {
			return err
		}
		if uid <= 0 {
			uid = resolvedUID
		}
		if toolID == "" {
			toolID = resolvedToolID
		}
	}
	return (&GenerationObjectService{}).Register(&RegisterGenerationObjectParams{
		UID:         uid,
		TaskID:      taskID,
		ToolID:      toolID,
		Provider:    "local",
		Bucket:      "local",
		ObjectKey:   objectKey,
		AssetKind:   strings.TrimSpace(assetKind),
		ContentType: strings.TrimSpace(contentType),
		SizeBytes:   sizeBytes,
		PublicURL:   strings.TrimSpace(publicURL),
	})
}

func resolveObjectPathPrefix(cfg config.StorageConfig) string {
	switch strings.TrimSpace(cfg.Type) {
	case "r2":
		return strings.Trim(strings.TrimSpace(cfg.R2.PathPrefix), "/")
	default:
		return ""
	}
}

func generatedObjectCategory(assetKind string) string {
	switch strings.TrimSpace(assetKind) {
	case "video", "thumbnail":
		return "videos"
	case "asset":
		return "assets"
	case "reference":
		return "reference-images"
	default:
		return "images"
	}
}

func buildGeneratedObjectKey(cfg config.StorageConfig, uid int, assetKind, dateDir, taskID, filename string) string {
	category := generatedObjectCategory(assetKind)
	pathPrefix := resolveObjectPathPrefix(cfg)
	taskSegment := strings.TrimSpace(taskID)
	uidSegment := path.Join("uid", strconv.Itoa(uid))

	switch {
	case pathPrefix == "" && taskSegment == "":
		return path.Join(uidSegment, category, dateDir, filename)
	case pathPrefix == "":
		return path.Join(uidSegment, category, dateDir, taskSegment, filename)
	case taskSegment == "":
		return path.Join(pathPrefix, uidSegment, category, dateDir, filename)
	default:
		return path.Join(pathPrefix, uidSegment, category, dateDir, taskSegment, filename)
	}
}

func buildReferenceObjectKey(cfg config.StorageConfig, uidDir, dateDir, filename string) string {
	pathPrefix := resolveObjectPathPrefix(cfg)
	if pathPrefix == "" {
		return path.Join("reference-images", uidDir, dateDir, filename)
	}
	return path.Join(pathPrefix, "reference-images", uidDir, dateDir, filename)
}

func objectCacheControl(assetKind string) string {
	switch strings.TrimSpace(assetKind) {
	case "reference":
		return "private, max-age=86400"
	default:
		return "public, max-age=31536000, immutable"
	}
}

func buildObjectMetadata(uid int, taskID, toolID, assetKind, sourceURL string) map[string]string {
	metadata := map[string]string{
		"asset-kind": strings.TrimSpace(assetKind),
	}
	if uid > 0 {
		metadata["uid"] = strconv.Itoa(uid)
	}
	if trimmed := strings.TrimSpace(taskID); trimmed != "" {
		metadata["task-id"] = trimmed
	}
	if trimmed := strings.TrimSpace(toolID); trimmed != "" {
		metadata["tool-id"] = trimmed
	}
	if trimmed := strings.TrimSpace(sourceURL); trimmed != "" {
		metadata["source-url"] = trimmed
	}
	return metadata
}

func (s *GeneratorService) saveToObjectStore(ctx context.Context, taskID, dateDir string, imageData [][]byte, store storageService.ObjectStore, uid int, toolID string) ([]string, error) {
	savedURLs := make([]string, 0, len(imageData))
	failedIndexes := make([]int, 0)
	cfg := globals.GraConf.Generator.Storage

	for i, data := range imageData {
		filename := fmt.Sprintf("%s_%d.png", taskID, i)
		key := buildGeneratedObjectKey(cfg, uid, "image", dateDir, taskID, filename)

		result, err := store.Put(ctx, &storageService.PutObjectRequest{
			Key:          key,
			Body:         bytes.NewReader(data),
			Size:         int64(len(data)),
			ContentType:  "image/png",
			CacheControl: objectCacheControl("image"),
			Metadata:     buildObjectMetadata(uid, taskID, toolID, "image", ""),
		})
		if err != nil {
			globals.Error(fmt.Sprintf("Failed to upload to %s %s: %v", store.Provider(), filename, err))
			failedIndexes = append(failedIndexes, i)
			continue
		}

		savedURLs = append(savedURLs, result.PublicURL)
		if err := (&GenerationObjectService{}).Register(&RegisterGenerationObjectParams{
			UID:         uid,
			TaskID:      taskID,
			ToolID:      toolID,
			Provider:    result.Provider,
			Bucket:      result.Bucket,
			ObjectKey:   result.Key,
			AssetKind:   "image",
			ContentType: "image/png",
			SizeBytes:   int64(len(data)),
			ETag:        result.ETag,
			PublicURL:   result.PublicURL,
		}); err != nil {
			globals.Warn(fmt.Sprintf("[GeneratorService] Failed to register image object %s: %v", result.Key, err))
		}
	}

	// 如果有任何上传失败，返回错误
	if len(failedIndexes) > 0 {
		return savedURLs, fmt.Errorf("failed to upload %d/%d images (indexes: %v)",
			len(failedIndexes), len(imageData), failedIndexes)
	}

	return savedURLs, nil
}

func (s *GeneratorService) openRemoteAsset(ctx context.Context, sourceURL string) (*remoteAssetStream, error) {
	parsed, err := url.Parse(sourceURL)
	if err != nil || parsed.Hostname() == "" {
		return nil, fmt.Errorf("invalid remote asset url")
	}
	if err := s.validateRemoteAssetURL(ctx, parsed); err != nil {
		return nil, err
	}

	timeout := GetRemoteAssetTimeout()
	requestCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, timeout)
	}

	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			return s.validateRemoteAssetURL(ctx, req.URL)
		},
	}
	client.Transport = &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return safeRemoteDialContext(ctx, network, address, "remote asset", GetAllowedRemoteAssetHosts())
		},
	}

	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, sourceURL, nil)
	if err != nil {
		if cancel != nil {
			cancel()
		}
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		if cancel != nil {
			cancel()
		}
		return nil, err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		if cancel != nil {
			cancel()
		}
		_ = resp.Body.Close()
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	maxSize := GetMaxRemoteAssetSize()
	if exceedsConfiguredRemoteAssetSize(resp.ContentLength, maxSize) {
		if cancel != nil {
			cancel()
		}
		_ = resp.Body.Close()
		return nil, fmt.Errorf("remote asset too large")
	}
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	baseCloser := io.Closer(resp.Body)
	if cancel != nil {
		baseCloser = closeFunc(func() error {
			cancel()
			return resp.Body.Close()
		})
	}
	var body io.ReadCloser
	if contentType == "" {
		reader := bufio.NewReader(resp.Body)
		sniff, _ := reader.Peek(512)
		if len(sniff) > 0 {
			contentType = http.DetectContentType(sniff)
		}
		body = newMaxBytesReadCloser(reader, baseCloser, maxSize)
	} else {
		body = newMaxBytesReadCloser(resp.Body, baseCloser, maxSize)
	}
	if idx := strings.Index(contentType, ";"); idx >= 0 {
		contentType = strings.TrimSpace(contentType[:idx])
	}
	return &remoteAssetStream{
		Body:        body,
		ContentType: contentType,
		Size:        resp.ContentLength,
	}, nil
}

func (s *GeneratorService) resolveTaskStorageIdentity(taskID string) (int, string) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return 0, ""
	}

	var task model.GenerationTask
	if err := globals.GraDBs["system"].
		Select("uid", "tool_id").
		Where("task_id = ?", taskID).
		First(&task).Error; err != nil {
		globals.Warn(fmt.Sprintf("[GeneratorService] Failed to resolve storage identity for task %s: %v", taskID, err))
		return 0, ""
	}

	return task.UID, task.ToolID
}

func (s *GeneratorService) requireTaskStorageIdentity(taskID string) (int, string, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return 0, "", fmt.Errorf("missing task id for generated asset storage")
	}

	uid, toolID := s.resolveTaskStorageIdentity(taskID)
	if uid <= 0 {
		return 0, "", fmt.Errorf("failed to resolve storage uid for task %s", taskID)
	}
	if strings.TrimSpace(toolID) == "" {
		return 0, "", fmt.Errorf("failed to resolve storage tool id for task %s", taskID)
	}
	return uid, toolID, nil
}

func buildStoredAssetFilename(taskID string, index int, sourceURL, contentType, assetKind string) string {
	ext := detectStoredAssetExtension(sourceURL, contentType, assetKind)
	prefix := taskID
	if strings.TrimSpace(assetKind) != "" {
		prefix = fmt.Sprintf("%s_%s", taskID, assetKind)
	}
	return fmt.Sprintf("%s_%d%s", prefix, index, ext)
}

func detectStoredAssetExtension(sourceURL, contentType, assetKind string) string {
	normalizedType := strings.ToLower(strings.TrimSpace(contentType))
	switch normalizedType {
	case "video/mp4":
		return ".mp4"
	case "video/webm":
		return ".webm"
	case "video/quicktime":
		return ".mov"
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "model/gltf-binary":
		return ".glb"
	case "model/gltf+json":
		return ".gltf"
	case "model/obj", "text/plain":
		if assetKind == "asset" {
			return ".obj"
		}
	case "model/stl", "application/sla":
		return ".stl"
	case "application/zip", "application/x-zip-compressed":
		return ".zip"
	case "application/octet-stream":
		if assetKind == "asset" {
			if parsed, err := url.Parse(sourceURL); err == nil {
				if ext := strings.ToLower(filepath.Ext(parsed.Path)); ext != "" {
					return ext
				}
			}
		}
	}

	if parsed, err := url.Parse(sourceURL); err == nil {
		ext := strings.ToLower(filepath.Ext(parsed.Path))
		switch ext {
		case ".mp4", ".webm", ".mov", ".jpg", ".jpeg", ".png", ".webp", ".gif", ".glb", ".gltf", ".obj", ".fbx", ".stl", ".usdz", ".zip":
			if ext == ".jpeg" {
				return ".jpg"
			}
			return ext
		}
	}

	if assetKind == "video" {
		return ".mp4"
	}
	if assetKind == "asset" {
		return ".glb"
	}
	return ".png"
}

type UploadReferenceImageResult struct {
	ID            string
	URL           string
	GlobalAssetID uint
}

func (s *GeneratorService) UploadReferenceImage(ctx context.Context, uid uint, contentType string, data []byte, ext string) (*UploadReferenceImageResult, error) {
	_ = ctx
	cfg := globals.GraConf.Generator.Storage
	id := uuid.New().String()
	dateDir := time.Now().Format("2006/01/02")

	filename := id
	if ext != "" {
		if strings.HasPrefix(ext, ".") {
			filename += ext
		} else {
			filename += "." + ext
		}
	}

	localCfg := cfg.Local
	storagePath := referenceImageDiskRoot(localCfg)
	relativeDir := filepath.Join("uid", strconv.FormatUint(uint64(uid), 10), dateDir)

	fullPath := filepath.Join(storagePath, relativeDir)
	if err := os.MkdirAll(fullPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create storage directory: %v", err)
	}

	filePath := filepath.Join(fullPath, filename)
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return nil, fmt.Errorf("failed to save reference image: %v", err)
	}

	result := &UploadReferenceImageResult{
		ID:  id,
		URL: fmt.Sprintf("%s/%s/%s", referenceImageURLPrefix(localCfg), filepath.ToSlash(filepath.Join("uid", strconv.FormatUint(uint64(uid), 10), dateDir)), filename),
	}

	sum := sha256.Sum256(data)
	if asset, err := globalAssetService.NewRepository(globals.GraDBs["system"]).CreateManagedUpload(globalAssetService.ManagedUploadInput{
		UID:         uid,
		UploadID:    id,
		URL:         result.URL,
		MimeType:    contentType,
		SizeBytes:   int64(len(data)),
		ContentHash: hex.EncodeToString(sum[:]),
		Kind:        model.GlobalAssetKindImage,
	}); err != nil {
		globals.Warn(fmt.Sprintf("[GeneratorService] failed to create global asset for reference upload %s: %v", id, err))
	} else if asset != nil {
		result.GlobalAssetID = asset.Id
		if err := assetLedgerService.New().UpsertReferenceUploadWithDB(globals.GraDBs["system"], asset); err != nil {
			globals.Warn(fmt.Sprintf("[GeneratorService] failed to sync reference upload ledger %s: %v", id, err))
		}
	}
	return result, nil
}

// saveGenerationRecord 保存生成记录到数据库
func (s *GeneratorService) saveGenerationRecord(req *GenerateImageRequest, taskID string, imageURLs []string, status int8, duration int64, creditsUsed int, resultMetadata interface{}) error {
	// 序列化参数
	params := model.GenerationRecordParams{
		Seed:              int(req.Seed),
		NumberOfImages:    req.NumberOfImages,
		Resolution:        req.Resolution,
		MediaType:         readStringFromJSONMapWithParams(req.RawRequestData, "mediaType"),
		Duration:          readStringFromJSONMapWithParams(req.RawRequestData, "duration"),
		StartFrame:        readStringFromJSONMapWithParams(req.RawRequestData, "startFrame"),
		EndFrame:          readStringFromJSONMapWithParams(req.RawRequestData, "endFrame"),
		GenerationMethod:  readStringFromJSONMapWithParams(req.RawRequestData, "generationMethod"),
		MotionMode:        readStringFromJSONMapWithParams(req.RawRequestData, "motionMode"),
		MotionOrientation: readStringFromJSONMapWithParams(req.RawRequestData, "motionOrientation"),
		Steps:             req.Steps,
		CfgScale:          req.CFGScale,
		Sampler:           req.Sampler,
		Lora:              req.Lora,
		Upscale:           req.Upscale,
	}
	if req != nil && FeatureTypeForToolID(req.ToolID) == model.TOOL_IMAGE_VECTORIZER {
		vectorCfg := parseVectorizerPostProcessConfig(req)
		params.Colors = vectorCfg.Colors
		params.DetailLevel = vectorCfg.DetailLevel
		params.ColorMode = vectorCfg.ColorMode
		params.HighFidelity = vectorCfg.HighFidelity
		params.Mode = "color"
	}
	paramsMap := map[string]interface{}{}
	if paramsJSON, err := json.Marshal(params); err == nil {
		if err := json.Unmarshal(paramsJSON, &paramsMap); err != nil {
			paramsMap = map[string]interface{}{}
		}
	} else {
		globals.Error(fmt.Sprintf("[SaveRecord] Failed to marshal params: %v", err))
	}
	if req != nil && FeatureTypeForToolID(req.ToolID) == model.TOOL_AVATAR_STUDIO {
		mergeJSONStringMap(paramsMap, req.RawRequestData["params"])
		if scenePresetKey := readStringFromJSONMapWithParams(req.RawRequestData, "scenePresetKey"); scenePresetKey != "" {
			paramsMap["scenePresetKey"] = scenePresetKey
		}
		mergeJSONStringMap(paramsMap, map[string]interface{}{
			"inputSnapshot":     req.RawRequestData["inputSnapshot"],
			"executionSnapshot": req.RawRequestData["executionSnapshot"],
			"billingSnapshot":   getAvatarBillingSnapshotForRecord(req.RawRequestData, req.NumberOfImages, creditsUsed),
		})
		if len(req.ReferenceImages) > 0 {
			paramsMap["referenceImages"] = req.ReferenceImages
		}
	}
	paramsJSON, err := json.Marshal(paramsMap)
	if err != nil {
		globals.Error(fmt.Sprintf("[SaveRecord] Failed to marshal params map: %v", err))
		paramsJSON = []byte("{}")
	}

	// 序列化结果图片
	resultImagesJSON, err := json.Marshal(imageURLs)
	if err != nil {
		globals.Error(fmt.Sprintf("[SaveRecord] Failed to marshal result images: %v", err))
		resultImagesJSON = []byte("[]")
	}

	// 序列化参考图片
	refImagesJSON, err := json.Marshal(req.ReferenceImages)
	if err != nil {
		globals.Error(fmt.Sprintf("[SaveRecord] Failed to marshal reference images: %v", err))
		refImagesJSON = []byte("[]")
	}
	resultMetadataJSON, err := json.Marshal(resultMetadata)
	if err != nil {
		globals.Error(fmt.Sprintf("[SaveRecord] Failed to marshal result metadata: %v", err))
		resultMetadataJSON = []byte("{}")
	}

	toolID := req.ToolID
	if toolID == "" {
		toolID = model.GetToolIDFromModel(req.Model)
	}
	record := model.GenerationRecord{
		UID:            int(req.UID),
		ToolID:         toolID,
		Model:          req.Model,
		Prompt:         req.Prompt,
		NegativePrompt: req.NegativePrompt,
		StylePreset:    req.StylePreset,
		AspectRatio:    req.AspectRatio,
		Params:         string(paramsJSON),
		InputFiles:     string(refImagesJSON),
		ResultImages:   string(resultImagesJSON),
		ResultMetadata: string(resultMetadataJSON),
		Status:         status,
		DurationMs:     int(duration),
		CreditsUsed:    creditsUsed,
		BatchID:        taskID,
		Origin:         req.Origin,
		ParentRecordID: req.LineageParentRecordID,
	}

	if err := globals.GraDBs["system"].Create(&record).Error; err != nil {
		globals.Error(fmt.Sprintf("[SaveRecord] Failed to save generation record to database: %v", err))
		return fmt.Errorf("failed to save generation record: %w", err)
	}
	if err := assetLedgerService.New().SyncGenerationInputsWithDB(globals.GraDBs["system"], &record); err != nil {
		globals.Warn(fmt.Sprintf("[SaveRecord] Failed to sync generation input ledger for record %d: %v", record.Id, err))
	}

	if strings.TrimSpace(taskID) != "" {
		if err := globals.GraDBs["system"].
			Model(&model.GenerationTask{}).
			Where("task_id = ?", taskID).
			Update("record_id", record.Id).Error; err != nil {
			globals.Warn(fmt.Sprintf("[SaveRecord] Failed to link task %s with generation record %d: %v", taskID, record.Id, err))
		}
		if err := (&GenerationObjectService{}).AttachTaskObjectsToRecord(taskID, record.Id); err != nil {
			globals.Warn(fmt.Sprintf("[SaveRecord] Failed to link generation objects for task %s with generation record %d: %v", taskID, record.Id, err))
		}
	}

	return nil
}

// GetGenerationHistory 获取生成历史
func (s *GeneratorService) GetGenerationHistory(uid uint, page, limit int, modelFilter string, withTotal bool) ([]model.GenerationRecord, int64, error) {
	var records []model.GenerationRecord
	var total int64

	db := globals.GraDBs["system"].Model(&model.GenerationRecord{}).Where("uid = ? AND status = ?", uid, model.STATUS_SUCCESS)

	if modelFilter != "" {
		db = db.Where("model = ?", modelFilter)
	}

	if withTotal {
		if err := db.Count(&total).Error; err != nil {
			return nil, 0, err
		}
	}

	// 分页查询
	offset := (page - 1) * limit
	if err := db.
		Select("id", "created_at", "uid", "model", "prompt", "negative_prompt", "aspect_ratio", "style_preset", "params", "input_files", "result_images", "result_metadata", "status").
		Order("created_at DESC").
		Offset(offset).
		Limit(limit).
		Find(&records).Error; err != nil {
		return nil, 0, err
	}

	return records, total, nil
}

func (s *GeneratorService) GetGenerationHistoryByToolID(uid uint, page, limit int, toolIDs []string, withTotal bool) ([]model.GenerationRecord, int64, error) {
	var records []model.GenerationRecord
	var total int64

	db := globals.GraDBs["system"].Model(&model.GenerationRecord{}).Where("uid = ? AND status = ?", uid, model.STATUS_SUCCESS)
	if len(toolIDs) > 0 {
		db = db.Where("tool_id IN ?", toolIDs)
	}

	if withTotal {
		if err := db.Count(&total).Error; err != nil {
			return nil, 0, err
		}
	}

	var err error
	records, err = fetchGenerationHistoryRecords(db, page, limit)
	if err != nil {
		return nil, 0, err
	}

	return records, total, nil
}

func (s *GeneratorService) GetGenerationHistoryByToolIDAndModel(uid uint, page, limit int, toolIDs []string, modelFilter string, withTotal bool) ([]model.GenerationRecord, int64, error) {
	var records []model.GenerationRecord
	var total int64

	db := globals.GraDBs["system"].Model(&model.GenerationRecord{}).Where("uid = ? AND status = ?", uid, model.STATUS_SUCCESS)
	if len(toolIDs) > 0 {
		db = db.Where("tool_id IN ?", toolIDs)
	}
	if modelFilter != "" {
		db = db.Where("model = ?", modelFilter)
	}

	if withTotal {
		if err := db.Count(&total).Error; err != nil {
			return nil, 0, err
		}
	}

	var err error
	records, err = fetchGenerationHistoryRecords(db, page, limit)
	if err != nil {
		return nil, 0, err
	}

	return records, total, nil
}

func fetchGenerationHistoryRecords(db *gorm.DB, page, limit int) ([]model.GenerationRecord, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if page < 1 {
		page = 1
	}

	offset := (page - 1) * limit
	orderedIDs := make([]uint, 0, limit)
	if err := db.
		Session(&gorm.Session{}).
		Order("created_at DESC, id DESC").
		Offset(offset).
		Limit(limit).
		Pluck("id", &orderedIDs).Error; err != nil {
		return nil, err
	}
	if len(orderedIDs) == 0 {
		return []model.GenerationRecord{}, nil
	}

	var records []model.GenerationRecord
	if err := globals.GraDBs["system"].
		Model(&model.GenerationRecord{}).
		Select("id", "created_at", "uid", "tool_id", "model", "prompt", "negative_prompt", "aspect_ratio", "style_preset", "params", "input_files", "result_images", "result_metadata", "status").
		Where("id IN ?", orderedIDs).
		Find(&records).Error; err != nil {
		return nil, err
	}

	recordsByID := make(map[uint]model.GenerationRecord, len(records))
	for _, record := range records {
		recordsByID[uint(record.Id)] = record
	}

	orderedRecords := make([]model.GenerationRecord, 0, len(orderedIDs))
	for _, id := range orderedIDs {
		if record, ok := recordsByID[id]; ok {
			orderedRecords = append(orderedRecords, record)
		}
	}

	return orderedRecords, nil
}

// GetGenerationByID 根据 ID 获取生成记录
func (s *GeneratorService) GetGenerationByID(uid uint, id uint) (*model.GenerationRecord, error) {
	var record model.GenerationRecord
	if err := globals.GraDBs["system"].Where("id = ? AND uid = ?", id, uid).First(&record).Error; err != nil {
		return nil, err
	}
	return &record, nil
}

// GetMaxReferenceImageSize 获取参考图片最大大小（从配置读取，默认10MB）
func GetMaxReferenceImageSize() int64 {
	cfg := globals.GraConf.Generator.FileUpload
	if cfg.MaxReferenceImageSize > 0 {
		return cfg.MaxReferenceImageSize
	}
	return 10 * 1024 * 1024 // 默认10MB
}

// GetMaxReferenceVideoSize 参考视频最大大小，默认 100MB
func GetMaxReferenceVideoSize() int64 {
	return 100 * 1024 * 1024
}

// GetMaxReferenceAudioSize 参考音频最大大小，默认 20MB
func GetMaxReferenceAudioSize() int64 {
	return 20 * 1024 * 1024
}

// UploadReferenceMedia 保存视频/音频参考素材；kind 用于区分子目录（reference-videos / reference-audios）。
func (s *GeneratorService) UploadReferenceMedia(ctx context.Context, uid uint, kind string, contentType string, data []byte, ext string) (*UploadReferenceImageResult, error) {
	_ = ctx
	cfg := globals.GraConf.Generator.Storage
	id := uuid.New().String()
	dateDir := time.Now().Format("2006/01/02")

	filename := id
	if ext != "" {
		if strings.HasPrefix(ext, ".") {
			filename += ext
		} else {
			filename += "." + ext
		}
	}

	safeKind := kind
	switch safeKind {
	case "reference-videos", "reference-audios":
	default:
		safeKind = "reference-media"
	}

	localCfg := cfg.Local
	storagePath := filepath.Join(resolveReferenceStorageBase(localCfg), safeKind)
	relativeDir := filepath.Join("uid", strconv.FormatUint(uint64(uid), 10), dateDir)

	fullPath := filepath.Join(storagePath, relativeDir)
	if err := os.MkdirAll(fullPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create storage directory: %v", err)
	}

	filePath := filepath.Join(fullPath, filename)
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return nil, fmt.Errorf("failed to save reference media: %v", err)
	}

	result := &UploadReferenceImageResult{
		ID:  id,
		URL: fmt.Sprintf("%s/%s/%s/%s", resolveReferenceURLBase(localCfg), safeKind, filepath.ToSlash(relativeDir), filename),
	}

	assetKind := model.GlobalAssetKindVideo
	if safeKind == "reference-audios" {
		assetKind = model.GlobalAssetKindAudio
	}
	sum := sha256.Sum256(data)
	if asset, err := globalAssetService.NewRepository(globals.GraDBs["system"]).CreateManagedUpload(globalAssetService.ManagedUploadInput{
		UID:         uid,
		UploadID:    id,
		URL:         result.URL,
		MimeType:    contentType,
		SizeBytes:   int64(len(data)),
		ContentHash: hex.EncodeToString(sum[:]),
		Kind:        assetKind,
	}); err != nil {
		globals.Warn(fmt.Sprintf("[GeneratorService] failed to create global asset for reference media %s: %v", id, err))
	} else if asset != nil {
		result.GlobalAssetID = asset.Id
		if err := assetLedgerService.New().UpsertReferenceUploadWithDB(globals.GraDBs["system"], asset); err != nil {
			globals.Warn(fmt.Sprintf("[GeneratorService] failed to sync reference media ledger %s: %v", id, err))
		}
	}
	return result, nil
}

// GetMaxRemoteAssetSize 获取远程资产最大大小（从配置读取，默认200MB）
func GetMaxRemoteAssetSize() int64 {
	cfg := globals.GraConf.Generator.FileUpload
	if cfg.MaxRemoteAssetSize > 0 {
		return cfg.MaxRemoteAssetSize
	}
	return 200 * 1024 * 1024
}

// GetRemoteAssetTimeout 获取远程资产下载超时（从配置读取，默认2分钟）
func GetRemoteAssetTimeout() time.Duration {
	cfg := globals.GraConf.Generator.FileUpload
	if cfg.RemoteAssetTimeoutSeconds > 0 {
		return time.Duration(cfg.RemoteAssetTimeoutSeconds) * time.Second
	}
	return 2 * time.Minute
}

func GetAllowedRemoteAssetHosts() []string {
	cfg := globals.GraConf.Generator.FileUpload
	if len(cfg.AllowedRemoteAssetHosts) == 0 {
		return nil
	}
	hosts := make([]string, 0, len(cfg.AllowedRemoteAssetHosts))
	for _, host := range cfg.AllowedRemoteAssetHosts {
		trimmed := strings.ToLower(strings.TrimSpace(host))
		if trimmed == "" {
			continue
		}
		hosts = append(hosts, trimmed)
	}
	return hosts
}
