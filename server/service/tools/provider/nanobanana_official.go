package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"server/globals"
	"server/model"
	"strings"
	"time"
)

// NanoBananaProvider 官方 Google Gemini API 提供商
// 使用 generativelanguage.googleapis.com/v1beta/models/{model}:generateContent
type NanoBananaProvider struct {
	name         string
	providerType string
	endpoint     string
	apiKey       string
	model        string
	enabled      bool
	timeout      time.Duration
}

// NewNanoBananaProvider 创建官方 Nano Banana 提供商
func NewNanoBananaProvider(cfg *ProviderConfig) *NanoBananaProvider {
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = "https://generativelanguage.googleapis.com"
	}
	providerType := model.ProviderTypeGemini
	if cfg != nil && cfg.Type != "" {
		providerType = model.NormalizeProviderType(cfg.Type)
	}

	// Keep provider timeout aligned with task queue timeout by default,
	// so provider HTTP timeout won't fire earlier than task-level timeout.
	timeout := 300 * time.Second
	if taskTimeoutSec := globals.GraConf.Generator.TaskQueue.TaskTimeout; taskTimeoutSec > 0 {
		timeout = time.Duration(taskTimeoutSec) * time.Second
	}
	if cfg.ExtraConfig != nil && cfg.ExtraConfig.Timeout > 0 {
		timeout = time.Duration(cfg.ExtraConfig.Timeout) * time.Second
	}

	return &NanoBananaProvider{
		name:         cfg.Name,
		providerType: providerType,
		endpoint:     endpoint,
		apiKey:       cfg.APIKey,
		model:        cfg.Model,
		enabled:      cfg.Enabled,
		timeout:      timeout,
	}
}

func (p *NanoBananaProvider) Name() string {
	return p.name
}

func (p *NanoBananaProvider) Type() string {
	if p.providerType != "" {
		return p.providerType
	}
	return model.ProviderTypeGemini
}

func (p *NanoBananaProvider) IsEnabled() bool {
	return p.enabled && p.apiKey != ""
}

// Gemini API 请求结构
type geminiRequest struct {
	Contents         []geminiContent         `json:"contents"`
	GenerationConfig *geminiGenerationConfig `json:"generationConfig,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text            string      `json:"text,omitempty"`
	InlineData      *geminiBlob `json:"inline_data,omitempty"`
	InlineDataCamel *geminiBlob `json:"inlineData,omitempty"`
}

type geminiBlob struct {
	MimeType string `json:"mime_type"`
	Data     string `json:"data"`
}

type geminiGenerationConfig struct {
	ResponseModalities []string           `json:"responseModalities,omitempty"`
	ImageConfig        *geminiImageConfig `json:"imageConfig,omitempty"`
}

type geminiImageConfig struct {
	AspectRatio string `json:"aspectRatio,omitempty"`
	ImageSize   string `json:"imageSize,omitempty"`
}

// Gemini API 响应结构
type geminiResponse struct {
	Candidates     []geminiCandidate     `json:"candidates"`
	PromptFeedback *geminiPromptFeedback `json:"promptFeedback,omitempty"`
	Error          *geminiError          `json:"error,omitempty"`
}

type geminiCandidate struct {
	Content       geminiContent `json:"content"`
	FinishReason  string        `json:"finishReason,omitempty"`
	SafetyRatings []interface{} `json:"safetyRatings,omitempty"`
}

type geminiPromptFeedback struct {
	BlockReason   string        `json:"blockReason,omitempty"`
	SafetyRatings []interface{} `json:"safetyRatings,omitempty"`
}

type geminiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

// Generate 调用 Google Gemini API 生成图片
func (p *NanoBananaProvider) Generate(ctx context.Context, req *GenerationRequest) (*GenerationResult, error) {
	startTime := time.Now()

	if !p.IsEnabled() {
		return &GenerationResult{
			Success:  false,
			Error:    "Nano Banana provider is not enabled or API key not configured",
			Provider: p.Name(),
		}, nil
	}

	// Synthetic progress: Gemini's generateContent is a single sync HTTP
	// call with no intermediate signal, so the task row would otherwise
	// stay at 0% the entire wait and snap to 100% on completion. Ramp
	// 5 → 90% on a ticker for the duration of the call so the UI shows
	// motion; the caller bumps to 100% after we return successfully.
	progressDone := make(chan struct{})
	if req.OnProgress != nil {
		req.OnProgress(5, nil)
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			pct := 5
			for {
				select {
				case <-progressDone:
					return
				case <-ctx.Done():
					return
				case <-ticker.C:
					if pct < 90 {
						pct += 5
						if pct > 90 {
							pct = 90
						}
						req.OnProgress(pct, nil)
					}
				}
			}
		}()
	}
	defer close(progressDone)

	// 构建提示词
	prompt := req.Prompt
	if req.StylePreset != "" {
		prompt += StylePresetToPromptSuffix(req.StylePreset)
	}
	if strings.TrimSpace(req.NegativePrompt) != "" {
		prompt += "\n\nAvoid: " + strings.TrimSpace(req.NegativePrompt)
	}

	// 确定使用的模型
	modelCode := p.resolveGeminiModelCode(req.Model)

	// 构建请求内容
	parts := []geminiPart{
		{Text: prompt},
	}

	// 如果有参考图片，添加到请求中
	for _, img := range req.ReferenceImages {
		parts = append(parts, geminiPart{
			InlineData: &geminiBlob{
				MimeType: img.MimeType,
				Data:     img.Data,
			},
		})
	}

	// 构建生成配置
	genConfig := &geminiGenerationConfig{
		ResponseModalities: []string{"TEXT", "IMAGE"},
	}

	// 根据 Width/Height 设置宽高比
	imageConfig := &geminiImageConfig{}
	if req.Width > 0 && req.Height > 0 {
		aspectRatio := dimensionsToAspectRatio(req.Width, req.Height)
		if aspectRatio != "" {
			imageConfig.AspectRatio = aspectRatio
		}
	}
	if imageSize := resolutionToImageSize(req.Resolution); imageSize != "" {
		imageConfig.ImageSize = imageSize
	}
	if imageConfig.AspectRatio != "" || imageConfig.ImageSize != "" {
		genConfig.ImageConfig = imageConfig
	}

	// 构建请求
	geminiReq := geminiRequest{
		Contents: []geminiContent{
			{Role: "user", Parts: parts},
		},
		GenerationConfig: genConfig,
	}

	// 序列化请求
	jsonData, err := json.Marshal(geminiReq)
	if err != nil {
		return &GenerationResult{
			Success:  false,
			Error:    fmt.Sprintf("Failed to marshal request: %v", err),
			Provider: p.Name(),
		}, nil
	}

	// 构建 API URL
	apiURL := fmt.Sprintf("%s/v1beta/models/%s:generateContent",
		strings.TrimSuffix(p.endpoint, "/"), modelCode)

	globals.Info(fmt.Sprintf("[NanoBanana Official] Sending request to %s", apiURL))

	// 创建 HTTP 请求
	httpReq, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return &GenerationResult{
			Success:  false,
			Error:    fmt.Sprintf("Failed to create request: %v", err),
			Provider: p.Name(),
		}, nil
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-goog-api-key", p.apiKey)

	// 发送请求 (图片生成可能需要较长时间)
	client := &http.Client{Timeout: p.timeout}
	resp, err := client.Do(httpReq)
	if err != nil {
		return &GenerationResult{
			Success:  false,
			Error:    fmt.Sprintf("Request failed: %v", err),
			Provider: p.Name(),
		}, nil
	}
	defer resp.Body.Close()

	// 读取响应
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &GenerationResult{
			Success:  false,
			Error:    fmt.Sprintf("Failed to read response: %v", err),
			Provider: p.Name(),
		}, nil
	}

	// 检查 HTTP 状态码
	if resp.StatusCode != http.StatusOK {
		bodyPreview := string(body[:min(len(body), 500)])
		globals.Error(fmt.Sprintf("[NanoBanana Official] HTTP %d: %s", resp.StatusCode, bodyPreview))
		var geminiErrResp geminiResponse
		if err := json.Unmarshal(body, &geminiErrResp); err == nil && geminiErrResp.Error != nil && geminiErrResp.Error.Message != "" {
			return &GenerationResult{
				Success:  false,
				Error:    geminiErrResp.Error.Message,
				Provider: p.Name(),
			}, nil
		}
		return &GenerationResult{
			Success:  false,
			Error:    fmt.Sprintf("API returned HTTP %d: %s", resp.StatusCode, bodyPreview),
			Provider: p.Name(),
		}, nil
	}

	// 解析响应
	var geminiResp geminiResponse
	if err := json.Unmarshal(body, &geminiResp); err != nil {
		globals.Error(fmt.Sprintf("[NanoBanana Official] Response parse error: %s", string(body[:min(len(body), 500)])))
		return &GenerationResult{
			Success:  false,
			Error:    fmt.Sprintf("Failed to parse response: %v", err),
			Provider: p.Name(),
		}, nil
	}

	// 检查 API 错误
	if geminiResp.Error != nil {
		return &GenerationResult{
			Success:  false,
			Error:    geminiResp.Error.Message,
			Provider: p.Name(),
		}, nil
	}

	// 检查是否被内容过滤阻止
	if geminiResp.PromptFeedback != nil && geminiResp.PromptFeedback.BlockReason != "" {
		return &GenerationResult{
			Success:  false,
			Error:    fmt.Sprintf("Content blocked: %s", geminiResp.PromptFeedback.BlockReason),
			Provider: p.Name(),
		}, nil
	}

	// 提取图片数据
	var imageData [][]byte
	finishReasons := make([]string, 0, len(geminiResp.Candidates))
	textParts := make([]string, 0)
	for _, candidate := range geminiResp.Candidates {
		if candidate.FinishReason != "" {
			finishReasons = append(finishReasons, candidate.FinishReason)
		}
		for _, part := range candidate.Content.Parts {
			blob := part.InlineData
			if blob == nil {
				blob = part.InlineDataCamel
			}
			if blob != nil && blob.Data != "" {
				// 解码 base64 图片数据
				imgBytes, err := base64.StdEncoding.DecodeString(blob.Data)
				if err != nil {
					globals.Warn(fmt.Sprintf("[NanoBanana Official] Failed to decode image: %v", err))
					continue
				}
				imageData = append(imageData, imgBytes)
			}
			if part.Text != "" {
				textParts = append(textParts, part.Text)
			}
		}
	}

	if len(imageData) == 0 {
		// 打印有限上下文帮助定位结构差异，避免长日志
		respPreview := string(body[:min(len(body), 500)])
		finishReasonSummary := strings.Join(finishReasons, ",")
		textSummary := strings.Join(textParts, " | ")
		if len(textSummary) > 300 {
			textSummary = textSummary[:300] + "..."
		}
		globals.Warn(fmt.Sprintf("[NanoBanana Official] No image parts found, finishReason=%s, textParts=%s, response preview: %s", finishReasonSummary, textSummary, respPreview))
		return &GenerationResult{
			Success:  false,
			Error:    buildNoImageFriendlyError(finishReasonSummary, textSummary),
			Provider: p.Name(),
		}, nil
	}

	duration := time.Since(startTime).Milliseconds()
	globals.Info(fmt.Sprintf("[NanoBanana Official] Generated %d image(s) in %dms", len(imageData), duration))

	return &GenerationResult{
		Success:   true,
		ImageData: imageData,
		ImageURLs: []string{},
		Seed:      req.Seed,
		Duration:  duration,
		Provider:  p.Name(),
	}, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func buildNoImageFriendlyError(finishReason, textSummary string) string {
	base := "Image generation failed because the model returned text instead of image output. Please revise the prompt to request one final image only, then try again."
	lowerText := strings.ToLower(textSummary)

	// 模型要求二选一/多选提示时，给出更明确引导
	if strings.Contains(lowerText, "which prompt variation") ||
		strings.Contains(lowerText, "would you like to try both") ||
		strings.Contains(lowerText, "which one") {
		base = "Image generation failed because the model asked a follow-up question instead of returning an image. Please pick one clear style/variation in your prompt and request one final image."
	}

	if strings.TrimSpace(finishReason) != "" {
		return fmt.Sprintf("%s (provider finish reason: %s)", base, finishReason)
	}
	return base
}

// dimensionsToAspectRatio 将宽高尺寸转换为 Gemini API 支持的宽高比
func dimensionsToAspectRatio(width, height int) string {
	if width == 0 || height == 0 {
		return ""
	}
	ratio := float64(width) / float64(height)

	// Gemini 支持的宽高比: 1:1, 2:3, 3:2, 3:4, 4:3, 4:5, 5:4, 9:16, 16:9, 21:9
	ratios := map[string]float64{
		"1:1":  1.0,
		"2:3":  0.667,
		"3:2":  1.5,
		"3:4":  0.75,
		"4:3":  1.333,
		"4:5":  0.8,
		"5:4":  1.25,
		"9:16": 0.5625,
		"16:9": 1.778,
		"21:9": 2.333,
	}

	// 找到最接近的宽高比
	closestRatio := "1:1"
	minDiff := 999.0
	for name, r := range ratios {
		diff := abs(ratio - r)
		if diff < minDiff {
			minDiff = diff
			closestRatio = name
		}
	}
	return closestRatio
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// resolutionToImageSize 映射业务分辨率参数到 Gemini imageConfig.imageSize
func resolutionToImageSize(resolution string) string {
	switch strings.ToLower(strings.TrimSpace(resolution)) {
	case "1k":
		return "1K"
	case "2k":
		return "2K"
	case "4k":
		return "4K"
	default:
		return ""
	}
}

func (p *NanoBananaProvider) resolveGeminiModelCode(requestModel string) string {
	normalizedReqModel := model.NormalizeModelID(requestModel)

	if configured := strings.TrimSpace(p.model); configured != "" {
		return configured
	}

	switch normalizedReqModel {
	case model.NANO_BANANA_2:
		return "gemini-3.1-flash-image-preview"
	case model.NANO_BANANA_PRO:
		return "gemini-3-pro-image-preview"
	default:
		if model.NormalizeModelID(p.model) == model.NANO_BANANA_PRO {
			return "gemini-3-pro-image-preview"
		}
		return "gemini-2.5-flash-image"
	}
}
