package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"server/model"
	"strings"
	"time"
)

const (
	defaultVeoEndpoint     = "https://generativelanguage.googleapis.com"
	defaultVeoPollInterval = 10 * time.Second
	defaultVeoMaxWait      = 15 * time.Minute
)

type VeoProvider struct {
	name         string
	providerType string
	endpoint     string
	apiKey       string
	model        string
	enabled      bool
	pollInterval time.Duration
	maxWait      time.Duration
}

func NewVeoProvider(cfg *ProviderConfig) *VeoProvider {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		endpoint = defaultVeoEndpoint
	}

	pollInterval := defaultVeoPollInterval
	maxWait := defaultVeoMaxWait
	if cfg != nil && cfg.ExtraConfig != nil {
		if cfg.ExtraConfig.PollInterval > 0 {
			pollInterval = time.Duration(cfg.ExtraConfig.PollInterval) * time.Second
		}
		if cfg.ExtraConfig.MaxWaitSeconds > 0 {
			maxWait = time.Duration(cfg.ExtraConfig.MaxWaitSeconds) * time.Second
		}
	}

	return &VeoProvider{
		name:         cfg.Name,
		providerType: cfg.Type,
		endpoint:     endpoint,
		apiKey:       cfg.APIKey,
		model:        cfg.Model,
		enabled:      cfg.Enabled,
		pollInterval: pollInterval,
		maxWait:      maxWait,
	}
}

func (p *VeoProvider) Name() string {
	return p.name
}

func (p *VeoProvider) Type() string {
	return p.providerType
}

func (p *VeoProvider) IsEnabled() bool {
	return p.enabled && strings.TrimSpace(p.apiKey) != ""
}

func (p *VeoProvider) Generate(ctx context.Context, req *GenerationRequest) (*GenerationResult, error) {
	startTime := time.Now()
	if !p.IsEnabled() {
		return &GenerationResult{Success: false, Error: "Veo provider is not enabled or API key not configured", Provider: p.Name()}, nil
	}

	payload, err := p.buildPredictPayload(req)
	if err != nil {
		return &GenerationResult{Success: false, Error: err.Error(), Provider: p.Name()}, nil
	}

	var createResp map[string]interface{}
	modelCode := p.resolveModelCode(req.Model)
	createPath := "/v1beta/models/" + modelCode + ":predictLongRunning"
	if err := p.doJSONRequest(ctx, http.MethodPost, createPath, payload, &createResp); err != nil {
		return &GenerationResult{Success: false, Error: err.Error(), Provider: p.Name()}, nil
	}
	operationName := strings.TrimSpace(getByPathString(createResp, "name"))
	if operationName == "" {
		return &GenerationResult{Success: false, Error: "missing Veo operation name", Provider: p.Name()}, nil
	}
	if req.OnProviderJob != nil {
		req.OnProviderJob(operationName, map[string]interface{}{"status": "submitted"})
	}

	pollCtx := ctx
	if p.maxWait > 0 {
		var cancel context.CancelFunc
		pollCtx, cancel = context.WithTimeout(ctx, p.maxWait)
		defer cancel()
	}

	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()

	for {
		if err := pollCtx.Err(); err != nil {
			return nil, err
		}

		// Inner retry on the SAME operationName so a transient blip
		// during status fetch doesn't abandon a paid job; the wrapper
		// reads ProviderJobID to decide whether re-submit is safe.
		statusResp, err := PollWithInnerRetry(pollCtx, "Veo", func(c context.Context) (map[string]interface{}, error) {
			var out map[string]interface{}
			if err := p.doJSONRequest(c, http.MethodGet, "/v1beta/"+operationName, nil, &out); err != nil {
				return nil, err
			}
			return out, nil
		})
		if err != nil {
			return &GenerationResult{Success: false, Error: err.Error(), Provider: p.Name(), ProviderJobID: operationName}, nil
		}

		done, _ := getByPath(statusResp, "done")
		if req.OnProgress != nil {
			progress := 25
			if boolDone, ok := done.(bool); ok && boolDone {
				progress = 100
			}
			req.OnProgress(progress, map[string]interface{}{
				"providerJobId":  operationName,
				"providerStatus": firstNonEmptyString(getByPathString(statusResp, "metadata.state"), getByPathString(statusResp, "metadata.status"), "processing"),
			})
		}

		if boolDone, ok := done.(bool); ok && boolDone {
			if errMessage := firstNonEmptyString(getByPathString(statusResp, "error.message"), getByPathString(statusResp, "error.status")); errMessage != "" {
				return &GenerationResult{Success: false, Error: errMessage, Provider: p.Name(), ProviderJobID: operationName}, nil
			}

			videoURI := firstNonEmptyString(
				getByPathString(statusResp, "response.generateVideoResponse.generatedSamples.0.video.uri"),
				getByPathString(statusResp, "response.generatedVideos.0.video.uri"),
			)
			if videoURI == "" {
				return &GenerationResult{Success: false, Error: "missing Veo video uri", Provider: p.Name(), ProviderJobID: operationName}, nil
			}
			data, contentType, err := p.downloadVideoBinary(pollCtx, videoURI)
			if err != nil {
				return &GenerationResult{Success: false, Error: err.Error(), Provider: p.Name(), ProviderJobID: operationName}, nil
			}
			return &GenerationResult{
				Success:            true,
				ProviderJobID:      operationName,
				VideoData:          [][]byte{data},
				VideoContentTypes:  []string{contentType},
				DownloadVideos:     false,
				DownloadThumbnails: false,
				Duration:           time.Since(startTime).Milliseconds(),
				Provider:           p.Name(),
			}, nil
		}

		select {
		case <-pollCtx.Done():
			return nil, pollCtx.Err()
		case <-ticker.C:
		}
	}
}

func (p *VeoProvider) Cancel(ctx context.Context, jobID string) error {
	return fmt.Errorf("Veo provider does not support remote cancel")
}

func (p *VeoProvider) resolveModelCode(modelID string) string {
	modelCode := strings.TrimSpace(modelID)
	if modelCode == "" || modelCode == model.VEO_31 || modelCode == model.VEO_31_FAST {
		if configured := strings.TrimSpace(p.model); configured != "" {
			modelCode = configured
		}
	}
	switch modelCode {
	case "", model.VEO_31:
		return "veo-3.1-generate-preview"
	case model.VEO_31_FAST:
		return "veo-3.1-fast-generate-preview"
	default:
		return modelCode
	}
}

func (p *VeoProvider) buildPredictPayload(req *GenerationRequest) ([]byte, error) {
	instance := map[string]interface{}{
		"prompt": req.Prompt,
	}

	refImages := req.ReferenceImages
	if len(refImages) > 0 {
		// start/end frame takes precedence over generic references.
		if req.StartFrame != "" || req.EndFrame != "" {
			instance["image"] = buildVeoImageBlob(refImages[0])
			if len(refImages) > 1 && strings.TrimSpace(req.EndFrame) != "" {
				instance["lastFrame"] = buildVeoImageBlob(refImages[1])
			}
		} else {
			references := make([]map[string]interface{}, 0, min(3, len(refImages)))
			for idx, ref := range refImages {
				if idx >= 3 {
					break
				}
				references = append(references, map[string]interface{}{
					"image":         buildVeoImageBlob(ref),
					"referenceType": "asset",
				})
			}
			if len(references) > 0 {
				instance["referenceImages"] = references
			}
		}
	}

	parameters := map[string]interface{}{}
	if aspectRatio := dimensionsToAspectRatio(req.Width, req.Height); aspectRatio != "" {
		parameters["aspectRatio"] = aspectRatio
	}
	if resolution := p.resolveResolution(req.Resolution); resolution != "" {
		parameters["resolution"] = resolution
	}
	if duration := p.resolveDuration(
		req.Duration,
		len(refImages) > 0 || strings.EqualFold(req.Resolution, "4k") || strings.EqualFold(req.Resolution, "1080p"),
	); duration > 0 {
		parameters["durationSeconds"] = duration
	}
	// Native audio is on by default per Vertex AI docs; only forward
	// the field when the caller explicitly opts out, otherwise let the
	// upstream pick its default.
	if req.GenerateAudio != nil {
		parameters["generateAudio"] = *req.GenerateAudio
	}
	if neg := strings.TrimSpace(req.NegativePrompt); neg != "" {
		parameters["negativePrompt"] = neg
	}

	body := map[string]interface{}{
		"instances": []interface{}{instance},
	}
	if len(parameters) > 0 {
		body["parameters"] = parameters
	}
	return json.Marshal(body)
}

// buildVeoImageBlob returns the image payload shape that Veo's predictLongRunning
// endpoint expects on both the Gemini Developer API (generativelanguage.googleapis.com)
// and Vertex AI: a flat object with top-level "bytesBase64Encoded" + "mimeType".
// The chat-style nested {"inlineData": {...}} shape used by generateContent is
// rejected here with "inlineData isn't supported by this model".
func buildVeoImageBlob(ref ReferenceImageData) map[string]interface{} {
	return map[string]interface{}{
		"bytesBase64Encoded": ref.Data,
		"mimeType":           ref.MimeType,
	}
}

func (p *VeoProvider) resolveResolution(resolution string) string {
	switch strings.ToLower(strings.TrimSpace(resolution)) {
	case "1080p", "1080":
		return "1080p"
	case "4k":
		return "4k"
	default:
		return "720p"
	}
}

func (p *VeoProvider) resolveDuration(duration string, forceEight bool) int {
	if forceEight {
		return 8
	}
	switch strings.TrimSpace(strings.TrimSuffix(duration, "s")) {
	case "4":
		return 4
	case "6", "5":
		return 6
	case "8", "10":
		return 8
	default:
		return 6
	}
}

func (p *VeoProvider) doJSONRequest(ctx context.Context, method, requestPath string, payload []byte, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(p.endpoint, "/")+requestPath, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("x-goog-api-key", p.apiKey)
	req.Header.Set("Accept", "application/json")
	if len(payload) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := readLimitedProviderResponse(resp.Body, providerMaxJSONResponseLen)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("provider http %d: %s", resp.StatusCode, strings.TrimSpace(truncateResponse(body, 240)))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}
	return nil
}

func (p *VeoProvider) downloadVideoBinary(ctx context.Context, videoURI string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, videoURI, nil)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("x-goog-api-key", p.apiKey)

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := readLimitedProviderResponse(resp.Body, providerMaxVideoDownloadLen)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, "", fmt.Errorf("provider http %d: %s", resp.StatusCode, strings.TrimSpace(truncateResponse(body, 240)))
	}
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if idx := strings.Index(contentType, ";"); idx >= 0 {
		contentType = strings.TrimSpace(contentType[:idx])
	}
	if contentType == "" {
		contentType = "video/mp4"
	}
	return body, contentType, nil
}
