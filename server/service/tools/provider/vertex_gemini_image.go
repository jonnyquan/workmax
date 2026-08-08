package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"server/globals"
	"server/model"
	"strings"
	"time"
)

// VertexGeminiImageProvider calls Gemini image-capable generateContent models
// through Vertex AI publisher endpoints. It covers Gemini 3 Pro Image
// (Nano Banana Pro) and keeps the existing NanoBanana request/response parser.
type VertexGeminiImageProvider struct {
	name         string
	providerType string
	enabled      bool
	vertex       vertexConfig
	timeout      time.Duration
}

func NewVertexGeminiImageProvider(cfg *ProviderConfig) *VertexGeminiImageProvider {
	timeout := 300 * time.Second
	if taskTimeoutSec := globals.GraConf.Generator.TaskQueue.TaskTimeout; taskTimeoutSec > 0 {
		timeout = time.Duration(taskTimeoutSec) * time.Second
	}
	if cfg != nil && cfg.ExtraConfig != nil && cfg.ExtraConfig.Timeout > 0 {
		timeout = time.Duration(cfg.ExtraConfig.Timeout) * time.Second
	}
	return &VertexGeminiImageProvider{
		name:         cfg.Name,
		providerType: cfg.Type,
		enabled:      cfg.Enabled,
		vertex:       newVertexConfig(cfg),
		timeout:      timeout,
	}
}

func (p *VertexGeminiImageProvider) Name() string { return p.name }

func (p *VertexGeminiImageProvider) Type() string {
	if p.providerType != "" {
		return p.providerType
	}
	return model.ProviderTypeVertex
}

func (p *VertexGeminiImageProvider) IsEnabled() bool {
	return p.enabled && (p.vertex.apiKeyMode() || strings.TrimSpace(p.vertex.projectID) != "")
}

func (p *VertexGeminiImageProvider) Generate(ctx context.Context, req *GenerationRequest) (*GenerationResult, error) {
	startTime := time.Now()
	if !p.IsEnabled() {
		return &GenerationResult{Success: false, Error: "Vertex Gemini Image provider is not enabled or project id not configured", Provider: p.Name()}, nil
	}

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

	payload, err := p.buildGenerateContentPayload(req)
	if err != nil {
		return &GenerationResult{Success: false, Error: err.Error(), Provider: p.Name()}, nil
	}

	var out geminiResponse
	modelCode := p.resolveModelCode(req.Model)
	requestCtx := ctx
	if p.timeout > 0 {
		var cancel context.CancelFunc
		requestCtx, cancel = context.WithTimeout(ctx, p.timeout)
		defer cancel()
	}
	requestPath := p.vertex.modelPath(modelCode) + ":generateContent"
	doRequest := p.vertex.doJSONRequest
	if p.vertex.apiKeyMode() {
		// Vertex AI API-key mode for Gemini uses the publisher-model endpoint
		// without a project/location segment:
		// /v1/publishers/google/models/{model}:generateContent?key=...
		requestPath = p.vertex.publisherModelPath(modelCode) + ":generateContent"
		doRequest = p.vertex.doAPIKeyJSONRequest
	}
	if err := doRequest(requestCtx, http.MethodPost, requestPath, payload, &out); err != nil {
		return &GenerationResult{Success: false, Error: err.Error(), Provider: p.Name()}, nil
	}
	if out.Error != nil {
		return &GenerationResult{Success: false, Error: out.Error.Message, Provider: p.Name()}, nil
	}
	if out.PromptFeedback != nil && out.PromptFeedback.BlockReason != "" {
		return &GenerationResult{Success: false, Error: fmt.Sprintf("Content blocked: %s", out.PromptFeedback.BlockReason), Provider: p.Name()}, nil
	}

	imageData, err := extractGeminiImageData(out)
	if err != nil {
		return &GenerationResult{Success: false, Error: err.Error(), Provider: p.Name()}, nil
	}
	return &GenerationResult{
		Success:   true,
		ImageData: imageData,
		ImageURLs: []string{},
		Seed:      req.Seed,
		Duration:  time.Since(startTime).Milliseconds(),
		Provider:  p.Name(),
	}, nil
}

func (p *VertexGeminiImageProvider) resolveModelCode(requestModel string) string {
	normalizedReqModel := model.NormalizeModelID(requestModel)
	if configured := strings.TrimSpace(p.vertex.model); configured != "" {
		return configured
	}
	switch normalizedReqModel {
	case model.NANO_BANANA_2:
		return "gemini-3.1-flash-image-preview"
	case model.NANO_BANANA_PRO:
		return "gemini-3-pro-image-preview"
	default:
		if model.NormalizeModelID(p.vertex.model) == model.NANO_BANANA_PRO {
			return "gemini-3-pro-image-preview"
		}
		return "gemini-2.5-flash-image"
	}
}

func (p *VertexGeminiImageProvider) buildGenerateContentPayload(req *GenerationRequest) ([]byte, error) {
	prompt := req.Prompt
	if req.StylePreset != "" {
		prompt += StylePresetToPromptSuffix(req.StylePreset)
	}
	if strings.TrimSpace(req.NegativePrompt) != "" {
		prompt += "\n\nAvoid: " + strings.TrimSpace(req.NegativePrompt)
	}
	parts := []geminiPart{{Text: prompt}}
	for _, img := range req.ReferenceImages {
		parts = append(parts, geminiPart{
			InlineDataCamel: &geminiBlob{
				MimeType: img.MimeType,
				Data:     img.Data,
			},
		})
	}
	genConfig := &geminiGenerationConfig{
		ResponseModalities: []string{"TEXT", "IMAGE"},
	}
	imageConfig := &geminiImageConfig{}
	if req.Width > 0 && req.Height > 0 {
		if aspectRatio := dimensionsToAspectRatio(req.Width, req.Height); aspectRatio != "" {
			imageConfig.AspectRatio = aspectRatio
		}
	}
	if imageSize := resolutionToImageSize(req.Resolution); imageSize != "" {
		imageConfig.ImageSize = imageSize
	}
	if imageConfig.AspectRatio != "" || imageConfig.ImageSize != "" {
		genConfig.ImageConfig = imageConfig
	}
	return json.Marshal(geminiRequest{
		Contents: []geminiContent{
			{Role: "user", Parts: parts},
		},
		GenerationConfig: genConfig,
	})
}

func extractGeminiImageData(resp geminiResponse) ([][]byte, error) {
	var imageData [][]byte
	finishReasons := make([]string, 0, len(resp.Candidates))
	textParts := make([]string, 0)
	for _, candidate := range resp.Candidates {
		if candidate.FinishReason != "" {
			finishReasons = append(finishReasons, candidate.FinishReason)
		}
		for _, part := range candidate.Content.Parts {
			blob := part.InlineData
			if blob == nil {
				blob = part.InlineDataCamel
			}
			if blob != nil && blob.Data != "" {
				imgBytes, err := base64.StdEncoding.DecodeString(blob.Data)
				if err != nil {
					globals.Warn(fmt.Sprintf("[Vertex Gemini Image] Failed to decode image: %v", err))
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
		textSummary := strings.Join(textParts, " | ")
		if len(textSummary) > 300 {
			textSummary = textSummary[:300] + "..."
		}
		return nil, fmt.Errorf("%s", buildNoImageFriendlyError(strings.Join(finishReasons, ","), textSummary))
	}
	return imageData, nil
}
