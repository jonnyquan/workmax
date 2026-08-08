package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"server/model"
	"strings"
	"time"
)

const (
	defaultSoraEndpoint     = "https://api.openai.com"
	defaultSoraPollInterval = 10 * time.Second
	defaultSoraMaxWait      = 15 * time.Minute
)

type SoraProvider struct {
	name         string
	providerType string
	endpoint     string
	apiKey       string
	model        string
	enabled      bool
	pollInterval time.Duration
	maxWait      time.Duration
}

type soraVideoObject struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Model    string `json:"model"`
	Progress int    `json:"progress"`
	Error    *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func NewSoraProvider(cfg *ProviderConfig) *SoraProvider {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		endpoint = defaultSoraEndpoint
	}

	pollInterval := defaultSoraPollInterval
	maxWait := defaultSoraMaxWait
	if cfg != nil && cfg.ExtraConfig != nil {
		if cfg.ExtraConfig.PollInterval > 0 {
			pollInterval = time.Duration(cfg.ExtraConfig.PollInterval) * time.Second
		}
		if cfg.ExtraConfig.MaxWaitSeconds > 0 {
			maxWait = time.Duration(cfg.ExtraConfig.MaxWaitSeconds) * time.Second
		}
	}

	return &SoraProvider{
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

func (p *SoraProvider) Name() string {
	return p.name
}

func (p *SoraProvider) Type() string {
	return p.providerType
}

func (p *SoraProvider) IsEnabled() bool {
	return p.enabled && strings.TrimSpace(p.apiKey) != ""
}

func (p *SoraProvider) Cancel(ctx context.Context, jobID string) error {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, strings.TrimRight(p.endpoint, "/")+"/v1/videos/"+jobID, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return nil
	}
	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := readLimitedProviderResponse(resp.Body, providerMaxJSONResponseLen)
		return fmt.Errorf("provider http %d: %s", resp.StatusCode, strings.TrimSpace(truncateResponse(body, 240)))
	}
	return nil
}

func (p *SoraProvider) Generate(ctx context.Context, req *GenerationRequest) (*GenerationResult, error) {
	startTime := time.Now()
	if !p.IsEnabled() {
		return &GenerationResult{Success: false, Error: "Sora provider is not enabled or API key not configured", Provider: p.Name()}, nil
	}

	contentType, payload, err := p.buildCreatePayload(req)
	if err != nil {
		return &GenerationResult{Success: false, Error: err.Error(), Provider: p.Name()}, nil
	}

	video, err := p.doJSONRequest(ctx, http.MethodPost, "/v1/videos", contentType, payload)
	if err != nil {
		return &GenerationResult{Success: false, Error: err.Error(), Provider: p.Name()}, nil
	}
	if strings.TrimSpace(video.ID) == "" {
		return &GenerationResult{Success: false, Error: "missing Sora video id", Provider: p.Name()}, nil
	}
	if req.OnProviderJob != nil {
		req.OnProviderJob(video.ID, map[string]interface{}{"status": video.Status})
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
			_ = p.Cancel(context.Background(), video.ID)
			return nil, err
		}

		// Inner retry on the SAME video.ID so a transient HTTP/network
		// blip during status fetch doesn't abandon a paid job — and so
		// the wrapper sees a non-empty ProviderJobID on terminal
		// failure (cost-leak guard in enter.go).
		video, err = PollWithInnerRetry(pollCtx, "Sora", func(c context.Context) (*soraVideoObject, error) {
			return p.doJSONRequest(c, http.MethodGet, "/v1/videos/"+video.ID, "", nil)
		})
		if err != nil {
			return &GenerationResult{Success: false, Error: err.Error(), Provider: p.Name(), ProviderJobID: video.ID}, nil
		}

		if req.OnProgress != nil {
			req.OnProgress(video.Progress, map[string]interface{}{
				"providerJobId":  video.ID,
				"providerStatus": video.Status,
			})
		}

		switch strings.ToLower(strings.TrimSpace(video.Status)) {
		case "completed":
			data, binaryType, err := p.downloadVideoBinary(pollCtx, video.ID)
			if err != nil {
				return &GenerationResult{Success: false, Error: err.Error(), Provider: p.Name(), ProviderJobID: video.ID}, nil
			}
			return &GenerationResult{
				Success:            true,
				ProviderJobID:      video.ID,
				VideoData:          [][]byte{data},
				VideoContentTypes:  []string{binaryType},
				DownloadVideos:     false,
				DownloadThumbnails: false,
				Duration:           time.Since(startTime).Milliseconds(),
				Provider:           p.Name(),
			}, nil
		case "failed":
			return &GenerationResult{Success: false, Error: p.videoError(video), Provider: p.Name(), ProviderJobID: video.ID}, nil
		}

		select {
		case <-pollCtx.Done():
			_ = p.Cancel(context.Background(), video.ID)
			return nil, pollCtx.Err()
		case <-ticker.C:
		}
	}
}

func (p *SoraProvider) buildCreatePayload(req *GenerationRequest) (string, []byte, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	writeField := func(name, value string) error {
		if strings.TrimSpace(value) == "" {
			return nil
		}
		return writer.WriteField(name, value)
	}

	modelID := strings.TrimSpace(req.Model)
	if modelID == "" {
		modelID = strings.TrimSpace(p.model)
	}
	if modelID == "" {
		modelID = model.SORA_2
	}

	size := ""
	if req.Width > 0 && req.Height > 0 {
		size = fmt.Sprintf("%dx%d", req.Width, req.Height)
	}
	seconds := strings.TrimSpace(strings.TrimSuffix(req.Duration, "s"))
	if seconds == "" {
		seconds = "8"
	}

	if err := writeField("model", modelID); err != nil {
		return "", nil, err
	}
	if err := writeField("prompt", req.Prompt); err != nil {
		return "", nil, err
	}
	if err := writeField("size", size); err != nil {
		return "", nil, err
	}
	if err := writeField("seconds", seconds); err != nil {
		return "", nil, err
	}

	if len(req.ReferenceImages) > 0 {
		first := req.ReferenceImages[0]
		decoded, err := base64.StdEncoding.DecodeString(first.Data)
		if err != nil {
			return "", nil, fmt.Errorf("failed to decode Sora input reference: %w", err)
		}
		fileExt := extensionFromMimeType(first.MimeType)
		part, err := writer.CreateFormFile("input_reference", "input_reference"+fileExt)
		if err != nil {
			return "", nil, err
		}
		if _, err := part.Write(decoded); err != nil {
			return "", nil, err
		}
	}

	if err := writer.Close(); err != nil {
		return "", nil, err
	}
	return writer.FormDataContentType(), body.Bytes(), nil
}

func (p *SoraProvider) doJSONRequest(ctx context.Context, method, requestPath, contentType string, payload []byte) (*soraVideoObject, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(p.endpoint, "/")+requestPath, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Accept", "application/json")
	if strings.TrimSpace(contentType) != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := readLimitedProviderResponse(resp.Body, providerMaxJSONResponseLen)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, fmt.Errorf("provider http %d: %s", resp.StatusCode, strings.TrimSpace(truncateResponse(body, 240)))
	}

	var video soraVideoObject
	if err := json.Unmarshal(body, &video); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	return &video, nil
}

func (p *SoraProvider) downloadVideoBinary(ctx context.Context, videoID string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.endpoint, "/")+"/v1/videos/"+videoID+"/content", nil)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

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

func (p *SoraProvider) videoError(video *soraVideoObject) string {
	if video != nil && video.Error != nil && strings.TrimSpace(video.Error.Message) != "" {
		return video.Error.Message
	}
	if video != nil && strings.TrimSpace(video.Status) != "" {
		return "Sora generation failed: " + strings.TrimSpace(video.Status)
	}
	return "Sora generation failed"
}

func extensionFromMimeType(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	default:
		return filepath.Ext(".jpg")
	}
}
