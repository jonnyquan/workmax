package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"server/model"
	"strings"
	"time"
)

const (
	defaultKlingEndpoint     = "https://api-singapore.klingai.com"
	defaultKlingPollInterval = 5 * time.Second
	defaultKlingMaxWait      = 10 * time.Minute
)

type KlingProvider struct {
	name         string
	providerType string
	endpoint     string
	apiKey       string
	model        string
	enabled      bool
	pollInterval time.Duration
	maxWait      time.Duration
}

type klingCreateRequest struct {
	ModelName      string `json:"model_name,omitempty"`
	Prompt         string `json:"prompt,omitempty"`
	NegativePrompt string `json:"negative_prompt,omitempty"`
	Duration       string `json:"duration,omitempty"`
	Mode           string `json:"mode,omitempty"`
	Sound          string `json:"sound,omitempty"`
	AspectRatio    string `json:"aspect_ratio,omitempty"`
	Image          string `json:"image,omitempty"`
	ImageTail      string `json:"image_tail,omitempty"`
	ExternalTaskID string `json:"external_task_id,omitempty"`
	CallbackURL    string `json:"callback_url,omitempty"`
}

type klingResponse struct {
	Code      int             `json:"code"`
	Message   string          `json:"message"`
	RequestID string          `json:"request_id"`
	Data      json.RawMessage `json:"data"`
}

type klingCreateData struct {
	TaskID     string `json:"task_id"`
	TaskStatus string `json:"task_status"`
}

type klingStatusData struct {
	TaskID        string `json:"task_id"`
	TaskStatus    string `json:"task_status"`
	TaskStatusMsg string `json:"task_status_msg"`
	TaskResult    struct {
		Videos []struct {
			ID           string `json:"id"`
			URL          string `json:"url"`
			WatermarkURL string `json:"watermark_url"`
			Duration     string `json:"duration"`
		} `json:"videos"`
	} `json:"task_result"`
}

func NewKlingProvider(cfg *ProviderConfig) *KlingProvider {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		endpoint = defaultKlingEndpoint
	}

	pollInterval := defaultKlingPollInterval
	maxWait := defaultKlingMaxWait
	if cfg != nil && cfg.ExtraConfig != nil {
		if cfg.ExtraConfig.PollInterval > 0 {
			pollInterval = time.Duration(cfg.ExtraConfig.PollInterval) * time.Second
		}
		if cfg.ExtraConfig.MaxWaitSeconds > 0 {
			maxWait = time.Duration(cfg.ExtraConfig.MaxWaitSeconds) * time.Second
		}
	}

	return &KlingProvider{
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

func (p *KlingProvider) Name() string {
	return p.name
}

func (p *KlingProvider) Type() string {
	return p.providerType
}

func (p *KlingProvider) IsEnabled() bool {
	return p.enabled && strings.TrimSpace(p.apiKey) != ""
}

func (p *KlingProvider) Cancel(ctx context.Context, jobID string) error {
	return fmt.Errorf("Kling provider does not support remote cancel")
}

func (p *KlingProvider) Generate(ctx context.Context, req *GenerationRequest) (*GenerationResult, error) {
	startTime := time.Now()
	if !p.IsEnabled() {
		return &GenerationResult{Success: false, Error: "Kling provider is not enabled or API key not configured", Provider: p.Name()}, nil
	}

	createReq, endpointPath := p.buildCreateRequest(req)
	payload, err := json.Marshal(createReq)
	if err != nil {
		return &GenerationResult{Success: false, Error: fmt.Sprintf("Failed to marshal request: %v", err), Provider: p.Name()}, nil
	}

	createResp, err := p.doRequest(ctx, http.MethodPost, endpointPath, payload)
	if err != nil {
		return &GenerationResult{Success: false, Error: err.Error(), Provider: p.Name()}, nil
	}

	var created klingCreateData
	if err := json.Unmarshal(createResp.Data, &created); err != nil {
		return &GenerationResult{Success: false, Error: fmt.Sprintf("Failed to parse create response: %v", err), Provider: p.Name()}, nil
	}
	if strings.TrimSpace(created.TaskID) == "" {
		return &GenerationResult{Success: false, Error: firstNonEmptyString(createResp.Message, "Missing Kling task id"), Provider: p.Name()}, nil
	}
	if req.OnProviderJob != nil {
		req.OnProviderJob(created.TaskID, map[string]interface{}{"status": created.TaskStatus})
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

		// Inner retry on the SAME taskID so a transient HTTP/network
		// blip during status fetch doesn't abandon a paid job, and the
		// wrapper sees a non-empty ProviderJobID on terminal failure
		// (cost-leak guard in enter.go).
		statusResp, err := PollWithInnerRetry(pollCtx, "Kling", func(c context.Context) (*klingResponse, error) {
			return p.doRequest(c, http.MethodGet, path.Join(endpointPath, created.TaskID), nil)
		})
		if err != nil {
			return &GenerationResult{Success: false, Error: err.Error(), Provider: p.Name(), ProviderJobID: created.TaskID}, nil
		}

		var statusData klingStatusData
		if err := json.Unmarshal(statusResp.Data, &statusData); err != nil {
			return &GenerationResult{Success: false, Error: fmt.Sprintf("Failed to parse status response: %v", err), Provider: p.Name(), ProviderJobID: created.TaskID}, nil
		}

		normalized := normalizeRemoteStatus(statusData.TaskStatus)
		if req.OnProgress != nil {
			progress := 15
			switch normalized {
			case "processing":
				progress = 55
			case "completed":
				progress = 100
			}
			req.OnProgress(progress, map[string]interface{}{
				"providerJobId":  statusData.TaskID,
				"providerStatus": statusData.TaskStatus,
			})
		}

		switch normalized {
		case "completed":
			videoURLs := make([]string, 0, len(statusData.TaskResult.Videos))
			for _, video := range statusData.TaskResult.Videos {
				if url := firstNonEmptyString(video.URL, video.WatermarkURL); url != "" {
					videoURLs = append(videoURLs, url)
				}
			}
			videoURLs = uniqueStrings(videoURLs)
			if len(videoURLs) == 0 {
				return &GenerationResult{Success: false, Error: "Kling task completed without video URLs", Provider: p.Name(), ProviderJobID: statusData.TaskID}, nil
			}
			return &GenerationResult{
				Success:            true,
				VideoURLs:          videoURLs,
				ProviderJobID:      statusData.TaskID,
				DownloadVideos:     true,
				DownloadThumbnails: false,
				Duration:           time.Since(startTime).Milliseconds(),
				Provider:           p.Name(),
			}, nil
		case "failed":
			return &GenerationResult{Success: false, Error: firstNonEmptyString(statusData.TaskStatusMsg, statusResp.Message, "Kling generation failed"), Provider: p.Name(), ProviderJobID: statusData.TaskID}, nil
		}

		select {
		case <-pollCtx.Done():
			return nil, pollCtx.Err()
		case <-ticker.C:
		}
	}
}

func (p *KlingProvider) buildCreateRequest(req *GenerationRequest) (*klingCreateRequest, string) {
	image, imageTail := p.resolveImageInputs(req)
	aspectRatio := ""
	if req.Width > 0 && req.Height > 0 {
		aspectRatio = dimensionsToAspectRatio(req.Width, req.Height)
	}

	request := &klingCreateRequest{
		ModelName:      p.resolveModelName(req.Model),
		Prompt:         req.Prompt,
		NegativePrompt: req.NegativePrompt,
		Duration:       normalizeKlingDuration(req.Duration),
		Mode:           normalizeKlingMode(req.MotionMode),
		Sound:          "off",
		AspectRatio:    aspectRatio,
		ExternalTaskID: req.TaskID,
	}
	if request.Duration == "" {
		request.Duration = "5"
	}
	if request.Mode == "" {
		request.Mode = "std"
	}

	if image != "" || imageTail != "" {
		request.Image = image
		request.ImageTail = imageTail
		return request, "/v1/videos/image2video"
	}
	return request, "/v1/videos/text2video"
}

func (p *KlingProvider) resolveImageInputs(req *GenerationRequest) (string, string) {
	image := strings.TrimSpace(req.StartFrame)
	imageTail := strings.TrimSpace(req.EndFrame)
	if image == "" && len(req.ReferenceImages) > 0 {
		image = strings.TrimSpace(req.ReferenceImages[0].Data)
	}
	if imageTail == "" && len(req.ReferenceImages) > 1 {
		imageTail = strings.TrimSpace(req.ReferenceImages[1].Data)
	}
	return image, imageTail
}

func (p *KlingProvider) resolveModelName(modelID string) string {
	switch strings.TrimSpace(modelID) {
	case "", model.KLING_2_6:
		if strings.TrimSpace(p.model) != "" {
			modelID = p.model
		}
	}

	switch strings.TrimSpace(modelID) {
	case "kling-2.6", "kling-v2.6":
		return "kling-v2-6"
	case "kling-3", "kling-v3":
		return "kling-v3"
	case "kling-3.0":
		return "kling-v3"
	default:
		if strings.HasPrefix(strings.TrimSpace(modelID), "kling-v") {
			return strings.TrimSpace(modelID)
		}
		return "kling-v2-6"
	}
}

func normalizeKlingDuration(duration string) string {
	duration = strings.TrimSpace(strings.TrimSuffix(duration, "s"))
	if duration == "" {
		return ""
	}
	return duration
}

func normalizeKlingMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "pro", "professional", "expert":
		return "pro"
	case "", "std", "standard":
		return "std"
	default:
		return "std"
	}
}

func (p *KlingProvider) doRequest(ctx context.Context, method, endpointPath string, payload []byte) (*klingResponse, error) {
	targetURL := strings.TrimRight(p.endpoint, "/") + endpointPath
	var body io.Reader
	if len(payload) > 0 {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, targetURL, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Accept", "application/json")
	if len(payload) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := readLimitedProviderResponse(resp.Body, providerMaxJSONResponseLen)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, fmt.Errorf("provider http %d: %s", resp.StatusCode, strings.TrimSpace(truncateResponse(respBody, 240)))
	}

	var parsed klingResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	if parsed.Code != 0 {
		return nil, fmt.Errorf("%s", firstNonEmptyString(parsed.Message, "kling request failed"))
	}
	return &parsed, nil
}
