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
	"strconv"
	"strings"
	"time"
)

const (
	defaultSeedanceEndpoint     = "https://ark.cn-beijing.volces.com"
	defaultSeedancePollInterval = 5 * time.Second
	defaultSeedanceMaxWait      = 15 * time.Minute

	defaultSeedanceProModel   = "doubao-seedance-1-5-pro-251215"
	defaultSeedance2Model     = "doubao-seedance-2-0-260128"
	defaultSeedance2FastModel = "doubao-seedance-2-0-fast-260128"
	seedanceTaskPath          = "/api/v3/contents/generations/tasks"
)

type SeedanceProvider struct {
	name         string
	providerType string
	endpoint     string
	apiKey       string
	model        string
	enabled      bool
	pollInterval time.Duration
	maxWait      time.Duration
}

func NewSeedanceProvider(cfg *ProviderConfig) *SeedanceProvider {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		endpoint = defaultSeedanceEndpoint
	}

	pollInterval := defaultSeedancePollInterval
	maxWait := defaultSeedanceMaxWait
	if cfg != nil && cfg.ExtraConfig != nil {
		if cfg.ExtraConfig.PollInterval > 0 {
			pollInterval = time.Duration(cfg.ExtraConfig.PollInterval) * time.Second
		}
		if cfg.ExtraConfig.MaxWaitSeconds > 0 {
			maxWait = time.Duration(cfg.ExtraConfig.MaxWaitSeconds) * time.Second
		}
	}

	return &SeedanceProvider{
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

func (p *SeedanceProvider) Name() string {
	return p.name
}

func (p *SeedanceProvider) Type() string {
	return p.providerType
}

func (p *SeedanceProvider) IsEnabled() bool {
	return p.enabled && strings.TrimSpace(p.apiKey) != ""
}

func (p *SeedanceProvider) Cancel(ctx context.Context, jobID string) error {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" || !p.IsEnabled() {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, strings.TrimRight(p.endpoint, "/")+path.Join(seedanceTaskPath, jobID), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return fmt.Errorf("Seedance cancel request failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (p *SeedanceProvider) Generate(ctx context.Context, req *GenerationRequest) (*GenerationResult, error) {
	startTime := time.Now()
	if !p.IsEnabled() {
		return &GenerationResult{Success: false, Error: "Seedance provider is not enabled or API key not configured", Provider: p.Name()}, nil
	}

	resolvedModel, err := p.resolveModelName(req)
	if err != nil {
		return &GenerationResult{Success: false, Error: err.Error(), Provider: p.Name()}, nil
	}

	payload, err := p.buildCreatePayload(req, resolvedModel)
	if err != nil {
		return &GenerationResult{Success: false, Error: err.Error(), Provider: p.Name()}, nil
	}

	var created map[string]interface{}
	if err := p.doJSONRequest(ctx, http.MethodPost, seedanceTaskPath, payload, &created); err != nil {
		return &GenerationResult{Success: false, Error: err.Error(), Provider: p.Name()}, nil
	}

	jobID := firstNonEmptyString(
		getByPathString(created, "id"),
		getByPathString(created, "task_id"),
		getByPathString(created, "data.id"),
	)
	if jobID == "" {
		return &GenerationResult{Success: false, Error: "missing Seedance task id", Provider: p.Name()}, nil
	}
	if req.OnProviderJob != nil {
		req.OnProviderJob(jobID, map[string]interface{}{
			"status": firstNonEmptyString(getByPathString(created, "status"), "queued"),
			"model":  resolvedModel,
		})
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

		// Inner retry on the SAME jobID — auto-retry == 重新查询 only.
		statusResp, err := PollWithInnerRetry(pollCtx, "Seedance", func(c context.Context) (map[string]interface{}, error) {
			var out map[string]interface{}
			if err := p.doJSONRequest(c, http.MethodGet, path.Join(seedanceTaskPath, jobID), nil, &out); err != nil {
				return nil, err
			}
			return out, nil
		})
		if err != nil {
			return &GenerationResult{Success: false, Error: err.Error(), Provider: p.Name(), ProviderJobID: jobID}, nil
		}

		status := firstNonEmptyString(
			getByPathString(statusResp, "status"),
			getByPathString(statusResp, "data.status"),
		)
		normalized := p.normalizeStatus(status)
		if req.OnProgress != nil {
			progress := p.progressByStatus(normalized)
			req.OnProgress(progress, map[string]interface{}{
				"providerJobId":  jobID,
				"providerStatus": status,
			})
		}

		switch normalized {
		case "completed":
			videoURL := firstNonEmptyString(
				getByPathString(statusResp, "content.video_url"),
				getByPathString(statusResp, "data.content.video_url"),
			)
			if videoURL == "" {
				return &GenerationResult{Success: false, Error: "Seedance task completed without video_url", Provider: p.Name(), ProviderJobID: jobID}, nil
			}
			thumbnailURL := firstNonEmptyString(
				getByPathString(statusResp, "content.last_frame_url"),
				getByPathString(statusResp, "content.cover_url"),
				getByPathString(statusResp, "content.thumbnail_url"),
				getByPathString(statusResp, "data.content.last_frame_url"),
				getByPathString(statusResp, "data.content.cover_url"),
				getByPathString(statusResp, "data.content.thumbnail_url"),
			)
			return &GenerationResult{
				Success:            true,
				VideoURLs:          []string{videoURL},
				ThumbnailURL:       thumbnailURL,
				ProviderJobID:      jobID,
				DownloadVideos:     true,
				DownloadThumbnails: thumbnailURL != "",
				Duration:           time.Since(startTime).Milliseconds(),
				Provider:           p.Name(),
			}, nil
		case "failed":
			return &GenerationResult{
				Success:       false,
				Error:         firstNonEmptyString(getByPathString(statusResp, "failure_reason"), getByPathString(statusResp, "error.message"), getByPathString(statusResp, "message"), "Seedance generation failed"),
				Provider:      p.Name(),
				ProviderJobID: jobID,
			}, nil
		case "cancelled":
			return &GenerationResult{
				Success:       false,
				Error:         "Seedance generation was cancelled",
				Provider:      p.Name(),
				ProviderJobID: jobID,
			}, nil
		}

		select {
		case <-pollCtx.Done():
			_ = p.Cancel(context.Background(), jobID)
			return nil, pollCtx.Err()
		case <-ticker.C:
		}
	}
}

func (p *SeedanceProvider) resolveModelName(req *GenerationRequest) (string, error) {
	configuredModel := strings.TrimSpace(p.model)
	if configuredModel != "" {
		return configuredModel, nil
	}

	switch strings.TrimSpace(req.Model) {
	case model.SEEDANCE_2_FAST:
		return defaultSeedance2FastModel, nil
	case model.SEEDANCE_2:
		return defaultSeedance2Model, nil
	}
	return defaultSeedanceProModel, nil
}

func (p *SeedanceProvider) buildCreatePayload(req *GenerationRequest, modelName string) ([]byte, error) {
	content := make([]map[string]interface{}, 0, 4)
	if prompt := strings.TrimSpace(req.Prompt); prompt != "" {
		content = append(content, map[string]interface{}{
			"type": "text",
			"text": prompt,
		})
	}

	isSeedance2 := p.isSeedance2(modelName)
	hasExplicitFrames := strings.TrimSpace(req.StartFrame) != "" || strings.TrimSpace(req.EndFrame) != ""

	if isSeedance2 && !hasExplicitFrames && len(req.ReferenceImages) > 0 {
		for _, ref := range req.ReferenceImages {
			url := seedanceReferenceToURL(ref)
			if url == "" {
				continue
			}
			content = append(content, seedanceImageContent(url, "reference_image"))
		}
	} else if isSeedance2 {
		firstFrame, lastFrame := p.resolveFrameInputs(req)
		if firstFrame != "" {
			content = append(content, seedanceImageContent(firstFrame, "first_frame"))
		}
		if lastFrame != "" {
			content = append(content, seedanceImageContent(lastFrame, "last_frame"))
		}
		for _, ref := range req.ReferenceImages {
			if hasExplicitFrames && IsFrameReferenceImageID(ref.ID) {
				continue
			}
			url := seedanceReferenceToURL(ref)
			if url == "" {
				continue
			}
			content = append(content, seedanceImageContent(url, "reference_image"))
		}
	} else {
		firstFrame, lastFrame := p.resolveFrameInputs(req)
		if firstFrame != "" {
			content = append(content, seedanceImageContent(firstFrame, "first_frame"))
		}
		if lastFrame != "" {
			content = append(content, seedanceImageContent(lastFrame, "last_frame"))
		}
	}

	if isSeedance2 {
		for _, ref := range req.ReferenceVideos {
			url := strings.TrimSpace(ref.URL)
			if url == "" {
				continue
			}
			content = append(content, seedanceMediaContent("video_url", url, "reference_video"))
		}
		for _, ref := range req.ReferenceAudios {
			url := strings.TrimSpace(ref.URL)
			if url == "" {
				continue
			}
			content = append(content, seedanceMediaContent("audio_url", url, "reference_audio"))
		}
	}

	if len(content) == 0 {
		return nil, fmt.Errorf("Seedance request requires prompt or frame input")
	}

	body := map[string]interface{}{
		"model":             modelName,
		"content":           content,
		"ratio":             p.resolveAspectRatio(req, modelName),
		"duration":          p.resolveDuration(req.Duration),
		"resolution":        p.resolveResolution(req.Resolution, modelName),
		"return_last_frame": true,
		"watermark":         false,
	}
	if isSeedance2 {
		if req.GenerateAudio != nil {
			body["generate_audio"] = *req.GenerateAudio
		}
		if req.Seed > 0 {
			body["seed"] = req.Seed
		}
	}
	return json.Marshal(body)
}

func (p *SeedanceProvider) resolveFrameInputs(req *GenerationRequest) (string, string) {
	firstFrame := strings.TrimSpace(req.StartFrame)
	lastFrame := strings.TrimSpace(req.EndFrame)

	if firstFrame == "" && len(req.ReferenceImages) > 0 {
		firstFrame = seedanceReferenceToURL(req.ReferenceImages[0])
	}
	if lastFrame == "" && len(req.ReferenceImages) > 1 {
		lastFrame = seedanceReferenceToURL(req.ReferenceImages[1])
	}

	return firstFrame, lastFrame
}

func (p *SeedanceProvider) resolveAspectRatio(req *GenerationRequest, modelName string) string {
	supported := map[string]struct{}{
		"16:9": {}, "9:16": {}, "1:1": {},
	}
	if p.isSeedance2(modelName) {
		supported["4:3"] = struct{}{}
		supported["3:4"] = struct{}{}
		supported["21:9"] = struct{}{}
	}

	hint := strings.TrimSpace(req.AspectRatio)
	if _, ok := supported[hint]; ok {
		return hint
	}
	if req.Width > 0 && req.Height > 0 {
		if aspectRatio := dimensionsToAspectRatio(req.Width, req.Height); aspectRatio != "" {
			if _, ok := supported[aspectRatio]; ok {
				return aspectRatio
			}
		}
	}
	return "16:9"
}

func (p *SeedanceProvider) isSeedance2(modelName string) bool {
	return strings.Contains(strings.ToLower(modelName), "seedance-2-0")
}

func (p *SeedanceProvider) resolveDuration(duration string) int {
	raw := strings.TrimSpace(strings.TrimSuffix(duration, "s"))
	if raw == "" {
		return 5
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return 5
	}
	if parsed < 2 {
		return 2
	}
	if parsed > 15 {
		return 15
	}
	return parsed
}

func (p *SeedanceProvider) resolveResolution(requested string, modelName string) string {
	normalized := strings.ToLower(strings.TrimSpace(requested))
	isSeedance2 := strings.Contains(strings.ToLower(modelName), "seedance-2-0")
	switch normalized {
	case "480p", "720p":
		return normalized
	case "1080p":
		if isSeedance2 {
			return "720p"
		}
		return normalized
	}
	return "720p"
}

func (p *SeedanceProvider) normalizeStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "succeeded", "success", "completed":
		return "completed"
	case "failed", "error":
		return "failed"
	case "cancelled", "canceled":
		return "cancelled"
	case "queued", "pending", "submitted", "running", "in_progress", "processing":
		return "processing"
	default:
		return "processing"
	}
}

func (p *SeedanceProvider) progressByStatus(status string) int {
	switch status {
	case "completed":
		return 100
	case "failed", "cancelled":
		return 100
	default:
		return 45
	}
}

func (p *SeedanceProvider) doJSONRequest(ctx context.Context, method, requestPath string, payload []byte, out interface{}) error {
	var bodyReader io.Reader
	if len(payload) > 0 {
		bodyReader = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(p.endpoint, "/")+requestPath, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Accept", "application/json")
	if len(payload) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := readLimitedProviderResponse(resp.Body, providerMaxJSONResponseLen)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return fmt.Errorf("Seedance request failed (%d): %s", resp.StatusCode, message)
	}
	if len(body) == 0 || out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("failed to parse Seedance response: %w", err)
	}
	return nil
}

func seedanceImageContent(url, role string) map[string]interface{} {
	item := map[string]interface{}{
		"type": "image_url",
		"image_url": map[string]interface{}{
			"url": url,
		},
	}
	if role != "" {
		item["role"] = role
	}
	return item
}

func seedanceMediaContent(contentType, url, role string) map[string]interface{} {
	item := map[string]interface{}{
		"type": contentType,
		contentType: map[string]interface{}{
			"url": url,
		},
	}
	if role != "" {
		item["role"] = role
	}
	return item
}

func seedanceReferenceToURL(ref ReferenceImageData) string {
	if strings.TrimSpace(ref.Data) == "" {
		return ""
	}
	mimeType := strings.TrimSpace(ref.MimeType)
	if mimeType == "" {
		mimeType = "image/png"
	}
	return fmt.Sprintf("data:%s;base64,%s", mimeType, ref.Data)
}
