package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"server/model"
	"strings"
	"time"
)

const (
	defaultMiniMaxEndpoint     = "https://api.minimax.io"
	defaultMiniMaxPollInterval = 5 * time.Second
	defaultMiniMaxMaxWait      = 10 * time.Minute
)

type MiniMaxVideoProvider struct {
	name         string
	providerType string
	endpoint     string
	apiKey       string
	model        string
	enabled      bool
	pollInterval time.Duration
	maxWait      time.Duration
}

type miniMaxCreateRequest struct {
	Model           string `json:"model,omitempty"`
	Prompt          string `json:"prompt,omitempty"`
	FirstFrameImage string `json:"first_frame_image,omitempty"`
	LastFrameImage  string `json:"last_frame_image,omitempty"`
	Duration        int    `json:"duration,omitempty"`
	Resolution      string `json:"resolution,omitempty"`
}

type miniMaxCreateResponse struct {
	TaskID   string `json:"task_id"`
	BaseResp struct {
		StatusCode int    `json:"status_code"`
		StatusMsg  string `json:"status_msg"`
	} `json:"base_resp"`
}

type miniMaxQueryResponse struct {
	TaskID   string `json:"task_id"`
	Status   string `json:"status"`
	FileID   string `json:"file_id"`
	BaseResp struct {
		StatusCode int    `json:"status_code"`
		StatusMsg  string `json:"status_msg"`
	} `json:"base_resp"`
}

type miniMaxDownloadResponse struct {
	File struct {
		FileID      string `json:"file_id"`
		DownloadURL string `json:"download_url"`
	} `json:"file"`
	BaseResp struct {
		StatusCode int    `json:"status_code"`
		StatusMsg  string `json:"status_msg"`
	} `json:"base_resp"`
}

func NewMiniMaxVideoProvider(cfg *ProviderConfig) *MiniMaxVideoProvider {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		endpoint = defaultMiniMaxEndpoint
	}

	pollInterval := defaultMiniMaxPollInterval
	maxWait := defaultMiniMaxMaxWait
	if cfg != nil && cfg.ExtraConfig != nil {
		if cfg.ExtraConfig.PollInterval > 0 {
			pollInterval = time.Duration(cfg.ExtraConfig.PollInterval) * time.Second
		}
		if cfg.ExtraConfig.MaxWaitSeconds > 0 {
			maxWait = time.Duration(cfg.ExtraConfig.MaxWaitSeconds) * time.Second
		}
	}

	return &MiniMaxVideoProvider{
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

func (p *MiniMaxVideoProvider) Name() string {
	return p.name
}

func (p *MiniMaxVideoProvider) Type() string {
	return p.providerType
}

func (p *MiniMaxVideoProvider) IsEnabled() bool {
	return p.enabled && strings.TrimSpace(p.apiKey) != ""
}

func (p *MiniMaxVideoProvider) Cancel(ctx context.Context, jobID string) error {
	return fmt.Errorf("MiniMax video provider does not support remote cancel")
}

func (p *MiniMaxVideoProvider) Generate(ctx context.Context, req *GenerationRequest) (*GenerationResult, error) {
	startTime := time.Now()
	if !p.IsEnabled() {
		return &GenerationResult{Success: false, Error: "MiniMax video provider is not enabled or API key not configured", Provider: p.Name()}, nil
	}

	createReq := p.buildCreateRequest(req)
	payload, err := json.Marshal(createReq)
	if err != nil {
		return &GenerationResult{Success: false, Error: fmt.Sprintf("Failed to marshal request: %v", err), Provider: p.Name()}, nil
	}

	var created miniMaxCreateResponse
	if err := p.doJSONRequest(ctx, http.MethodPost, "/v1/video_generation", nil, payload, &created); err != nil {
		return &GenerationResult{Success: false, Error: err.Error(), Provider: p.Name()}, nil
	}
	if strings.TrimSpace(created.TaskID) == "" {
		return &GenerationResult{Success: false, Error: firstNonEmptyString(created.BaseResp.StatusMsg, "Missing MiniMax task id"), Provider: p.Name()}, nil
	}
	if req.OnProviderJob != nil {
		req.OnProviderJob(created.TaskID, map[string]interface{}{"status": "Preparing"})
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

		// Inner retry on the SAME taskID so a transient blip during
		// status fetch doesn't abandon a paid job; the wrapper reads
		// ProviderJobID to decide whether re-submit is safe.
		statusResp, err := PollWithInnerRetry(pollCtx, "MiniMax", func(c context.Context) (miniMaxQueryResponse, error) {
			var out miniMaxQueryResponse
			if err := p.doJSONRequest(c, http.MethodGet, "/v1/query/video_generation", map[string]string{"task_id": created.TaskID}, nil, &out); err != nil {
				return miniMaxQueryResponse{}, err
			}
			return out, nil
		})
		if err != nil {
			return &GenerationResult{Success: false, Error: err.Error(), Provider: p.Name(), ProviderJobID: created.TaskID}, nil
		}

		normalized := p.normalizeStatus(statusResp.Status)
		if req.OnProgress != nil {
			progress := 20
			switch normalized {
			case "processing":
				progress = 60
			case "completed":
				progress = 100
			}
			req.OnProgress(progress, map[string]interface{}{
				"providerJobId":  statusResp.TaskID,
				"providerStatus": statusResp.Status,
			})
		}

		switch normalized {
		case "completed":
			downloadURL, err := p.retrieveDownloadURL(pollCtx, statusResp.FileID)
			if err != nil {
				return &GenerationResult{Success: false, Error: err.Error(), Provider: p.Name(), ProviderJobID: statusResp.TaskID}, nil
			}
			return &GenerationResult{
				Success:            true,
				VideoURLs:          []string{downloadURL},
				ProviderJobID:      statusResp.TaskID,
				DownloadVideos:     true,
				DownloadThumbnails: false,
				Duration:           time.Since(startTime).Milliseconds(),
				Provider:           p.Name(),
			}, nil
		case "failed":
			return &GenerationResult{Success: false, Error: firstNonEmptyString(statusResp.BaseResp.StatusMsg, "MiniMax generation failed"), Provider: p.Name(), ProviderJobID: statusResp.TaskID}, nil
		}

		select {
		case <-pollCtx.Done():
			return nil, pollCtx.Err()
		case <-ticker.C:
		}
	}
}

func (p *MiniMaxVideoProvider) buildCreateRequest(req *GenerationRequest) *miniMaxCreateRequest {
	firstFrame := strings.TrimSpace(req.StartFrame)
	lastFrame := strings.TrimSpace(req.EndFrame)
	if firstFrame == "" && len(req.ReferenceImages) > 0 {
		firstFrame = strings.TrimSpace(req.ReferenceImages[0].Data)
	}
	if lastFrame == "" && len(req.ReferenceImages) > 1 {
		lastFrame = strings.TrimSpace(req.ReferenceImages[1].Data)
	}

	return &miniMaxCreateRequest{
		Model:           p.resolveModelName(req.Model),
		Prompt:          req.Prompt,
		FirstFrameImage: firstFrame,
		LastFrameImage:  lastFrame,
		Duration:        p.resolveDuration(req.Duration),
		Resolution:      p.resolveResolution(req.Resolution),
	}
}

func (p *MiniMaxVideoProvider) resolveModelName(modelID string) string {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" || modelID == model.MINIMAX_VIDEO {
		if configured := strings.TrimSpace(p.model); configured != "" {
			modelID = configured
		}
	}
	switch modelID {
	case "", model.MINIMAX_VIDEO:
		return "MiniMax-Hailuo-02"
	default:
		return modelID
	}
}

func (p *MiniMaxVideoProvider) resolveDuration(duration string) int {
	switch strings.TrimSpace(strings.TrimSuffix(duration, "s")) {
	case "10":
		return 10
	case "6":
		return 6
	case "5":
		return 6
	default:
		return 6
	}
}

func (p *MiniMaxVideoProvider) resolveResolution(resolution string) string {
	switch strings.ToUpper(strings.TrimSpace(resolution)) {
	case "1080P", "1080", "4K":
		return "1080P"
	case "768P", "768":
		return "768P"
	case "720P", "720", "":
		return "720P"
	default:
		return "720P"
	}
}

func (p *MiniMaxVideoProvider) normalizeStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "preparing", "queueing", "processing":
		return "processing"
	case "success":
		return "completed"
	case "fail", "failed":
		return "failed"
	default:
		return "processing"
	}
}

func (p *MiniMaxVideoProvider) retrieveDownloadURL(ctx context.Context, fileID string) (string, error) {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return "", fmt.Errorf("missing MiniMax file id")
	}
	var downloadResp miniMaxDownloadResponse
	if err := p.doJSONRequest(ctx, http.MethodGet, "/v1/files/retrieve", map[string]string{"file_id": fileID}, nil, &downloadResp); err != nil {
		return "", err
	}
	downloadURL := strings.TrimSpace(downloadResp.File.DownloadURL)
	if downloadURL == "" {
		return "", fmt.Errorf("missing MiniMax download url")
	}
	return downloadURL, nil
}

func (p *MiniMaxVideoProvider) doJSONRequest(ctx context.Context, method, endpointPath string, query map[string]string, payload []byte, out interface{}) error {
	targetURL := strings.TrimRight(p.endpoint, "/") + endpointPath
	if len(query) > 0 {
		parsed, err := url.Parse(targetURL)
		if err != nil {
			return fmt.Errorf("failed to parse url: %w", err)
		}
		values := parsed.Query()
		for key, value := range query {
			if strings.TrimSpace(value) == "" {
				continue
			}
			values.Set(key, value)
		}
		parsed.RawQuery = values.Encode()
		targetURL = parsed.String()
	}

	var body io.Reader
	if len(payload) > 0 {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, targetURL, body)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Accept", "application/json")
	if len(payload) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := readLimitedProviderResponse(resp.Body, providerMaxJSONResponseLen)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("provider http %d: %s", resp.StatusCode, strings.TrimSpace(truncateResponse(respBody, 240)))
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}
	return nil
}
