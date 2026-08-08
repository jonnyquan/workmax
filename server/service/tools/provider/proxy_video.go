package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"server/model"
	"strconv"
	"strings"
	"time"
)

const (
	defaultVideoSubmitPath      = "/v1/videos/generations"
	defaultVideoStatusPath      = "/v1/videos/generations/{jobId}"
	defaultVideoCancelPath      = "/v1/videos/generations/{jobId}/cancel"
	defaultVideoPollInterval    = 5 * time.Second
	defaultVideoMaxWait         = 10 * time.Minute
	providerMaxJSONResponseLen  = 10 << 20
	providerMaxVideoDownloadLen = 512 << 20
)

// ProxyVideoProvider 本地代理视频生成提供商，支持异步 submit/query/cancel。
type ProxyVideoProvider struct {
	name                      string
	providerType              string
	endpoint                  string
	apiKey                    string
	model                     string
	enabled                   bool
	requestTimeout            time.Duration
	submitPath                string
	submitPayloadTemplate     string
	statusPath                string
	cancelPath                string
	apiKeyHeader              string
	apiKeyPrefix              string
	headers                   map[string]string
	pollInterval              time.Duration
	maxWait                   time.Duration
	downloadVideos            bool
	downloadThumbnails        bool
	responseJobIDField        string
	responseStatusField       string
	responseProgressField     string
	responseErrorField        string
	responseVideoURLsField    string
	responseThumbnailField    string
	responseVideoURLTemplate  string
	responseThumbnailTemplate string
}

// NewProxyVideoProvider 创建本地代理视频提供商
func NewProxyVideoProvider(cfg *ProviderConfig) *ProxyVideoProvider {
	submitPath := defaultVideoSubmitPath
	submitPayloadTemplate := ""
	statusPath := defaultVideoStatusPath
	cancelPath := defaultVideoCancelPath
	requestTimeout := time.Duration(0)
	apiKeyHeader := "Authorization"
	apiKeyPrefix := "Bearer "
	headers := map[string]string{}
	pollInterval := defaultVideoPollInterval
	maxWait := defaultVideoMaxWait
	downloadVideos := true
	downloadThumbnails := true
	responseJobIDField := ""
	responseStatusField := ""
	responseProgressField := ""
	responseErrorField := ""
	responseVideoURLsField := ""
	responseThumbnailField := ""
	responseVideoURLTemplate := ""
	responseThumbnailTemplate := ""

	if cfg != nil && cfg.ExtraConfig != nil {
		if cfg.ExtraConfig.Timeout > 0 {
			requestTimeout = time.Duration(cfg.ExtraConfig.Timeout) * time.Second
		}
		if v := strings.TrimSpace(cfg.ExtraConfig.SubmitPath); v != "" {
			submitPath = v
		}
		if v := strings.TrimSpace(cfg.ExtraConfig.SubmitPayloadTemplate); v != "" {
			submitPayloadTemplate = v
		}
		if v := strings.TrimSpace(cfg.ExtraConfig.StatusPath); v != "" {
			statusPath = v
		}
		if v := strings.TrimSpace(cfg.ExtraConfig.CancelPath); v != "" {
			cancelPath = v
		}
		if v := strings.TrimSpace(cfg.ExtraConfig.APIKeyHeader); v != "" {
			apiKeyHeader = v
		}
		if cfg.ExtraConfig.APIKeyPrefix != "" {
			apiKeyPrefix = cfg.ExtraConfig.APIKeyPrefix
		}
		for key, value := range cfg.ExtraConfig.Headers {
			trimmedKey := strings.TrimSpace(key)
			if trimmedKey == "" {
				continue
			}
			headers[trimmedKey] = value
		}
		if cfg.ExtraConfig.PollInterval > 0 {
			pollInterval = time.Duration(cfg.ExtraConfig.PollInterval) * time.Second
		}
		if cfg.ExtraConfig.MaxWaitSeconds > 0 {
			maxWait = time.Duration(cfg.ExtraConfig.MaxWaitSeconds) * time.Second
		}
		if cfg.ExtraConfig.DownloadVideos != nil {
			downloadVideos = *cfg.ExtraConfig.DownloadVideos
		}
		if cfg.ExtraConfig.DownloadThumbnails != nil {
			downloadThumbnails = *cfg.ExtraConfig.DownloadThumbnails
		}
		responseJobIDField = strings.TrimSpace(cfg.ExtraConfig.ResponseJobIDField)
		responseStatusField = strings.TrimSpace(cfg.ExtraConfig.ResponseStatusField)
		responseProgressField = strings.TrimSpace(cfg.ExtraConfig.ResponseProgressField)
		responseErrorField = strings.TrimSpace(cfg.ExtraConfig.ResponseErrorField)
		responseVideoURLsField = strings.TrimSpace(cfg.ExtraConfig.ResponseVideoURLsField)
		responseThumbnailField = strings.TrimSpace(cfg.ExtraConfig.ResponseThumbnailField)
		responseVideoURLTemplate = strings.TrimSpace(cfg.ExtraConfig.ResponseVideoURLTemplate)
		responseThumbnailTemplate = strings.TrimSpace(cfg.ExtraConfig.ResponseThumbnailTemplate)
	}

	return &ProxyVideoProvider{
		name:                      cfg.Name,
		providerType:              cfg.Type,
		endpoint:                  strings.TrimSpace(cfg.Endpoint),
		apiKey:                    cfg.APIKey,
		model:                     cfg.Model,
		enabled:                   cfg.Enabled,
		requestTimeout:            requestTimeout,
		submitPath:                submitPath,
		submitPayloadTemplate:     submitPayloadTemplate,
		statusPath:                statusPath,
		cancelPath:                cancelPath,
		apiKeyHeader:              apiKeyHeader,
		apiKeyPrefix:              apiKeyPrefix,
		headers:                   headers,
		pollInterval:              pollInterval,
		maxWait:                   maxWait,
		downloadVideos:            downloadVideos,
		downloadThumbnails:        downloadThumbnails,
		responseJobIDField:        responseJobIDField,
		responseStatusField:       responseStatusField,
		responseProgressField:     responseProgressField,
		responseErrorField:        responseErrorField,
		responseVideoURLsField:    responseVideoURLsField,
		responseThumbnailField:    responseThumbnailField,
		responseVideoURLTemplate:  responseVideoURLTemplate,
		responseThumbnailTemplate: responseThumbnailTemplate,
	}
}

func (p *ProxyVideoProvider) Name() string {
	return p.name
}

func (p *ProxyVideoProvider) Type() string {
	if strings.TrimSpace(p.providerType) == "" {
		return model.ProviderTypeCustom
	}
	return p.providerType
}

func (p *ProxyVideoProvider) IsEnabled() bool {
	return p.enabled && p.endpoint != ""
}

type genericVideoRequest struct {
	Model             string                 `json:"model"`
	Prompt            string                 `json:"prompt"`
	Duration          string                 `json:"duration,omitempty"`
	StartFrame        string                 `json:"startFrame,omitempty"`
	EndFrame          string                 `json:"endFrame,omitempty"`
	Resolution        string                 `json:"resolution,omitempty"`
	GenerationMethod  string                 `json:"generationMethod,omitempty"`
	MotionMode        string                 `json:"motionMode,omitempty"`
	MotionOrientation string                 `json:"motionOrientation,omitempty"`
	ReferenceImages   []ReferenceImageData   `json:"referenceImages,omitempty"`
	ReferenceVideos   []ReferenceMediaData   `json:"referenceVideos,omitempty"`
	ReferenceAudios   []ReferenceMediaData   `json:"referenceAudios,omitempty"`
	Width             int                    `json:"width,omitempty"`
	Height            int                    `json:"height,omitempty"`
	Params            map[string]interface{} `json:"params,omitempty"`
}

type parsedVideoState struct {
	JobID        string
	Status       string
	Progress     int
	Error        string
	VideoURLs    []string
	ThumbnailURL string
}

func (p *ProxyVideoProvider) Generate(ctx context.Context, req *GenerationRequest) (*GenerationResult, error) {
	startTime := time.Now()
	if !p.IsEnabled() {
		return &GenerationResult{Success: false, Error: "Proxy video provider is not enabled", Provider: p.Name()}, nil
	}

	modelID := p.model
	if strings.TrimSpace(req.Model) != "" {
		modelID = strings.TrimSpace(req.Model)
	}

	resolution := ""
	if req.Width > 0 && req.Height > 0 {
		resolution = fmt.Sprintf("%dx%d", req.Width, req.Height)
	}

	payload, err := p.buildSubmitPayload(req, modelID, resolution)
	if err != nil {
		return &GenerationResult{Success: false, Error: fmt.Sprintf("Failed to build request: %v", err), Provider: p.Name()}, nil
	}

	submitURL := p.resolveURL(p.submitPath, "")
	submitBody, err := p.doJSONRequest(ctx, http.MethodPost, submitURL, payload)
	if err != nil {
		return &GenerationResult{Success: false, Error: err.Error(), Provider: p.Name()}, nil
	}

	state := p.parseVideoState(submitBody)
	if state.JobID != "" && req.OnProviderJob != nil {
		req.OnProviderJob(state.JobID, map[string]interface{}{"status": state.Status})
	}
	if len(state.VideoURLs) > 0 {
		if req.OnProgress != nil {
			req.OnProgress(95, map[string]interface{}{"status": normalizeRemoteStatus(state.Status), "providerJobId": state.JobID})
		}
		return &GenerationResult{
			Success:            true,
			VideoURLs:          state.VideoURLs,
			ThumbnailURL:       state.ThumbnailURL,
			ProviderJobID:      state.JobID,
			DownloadVideos:     p.downloadVideos,
			DownloadThumbnails: p.downloadThumbnails,
			Duration:           time.Since(startTime).Milliseconds(),
			Provider:           p.Name(),
		}, nil
	}

	if state.JobID == "" {
		return &GenerationResult{Success: false, Error: p.buildVideoProviderError(submitBody, "Missing provider job id from submit response"), Provider: p.Name()}, nil
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
			_ = p.Cancel(context.Background(), state.JobID)
			return nil, err
		}

		statusURL := p.resolveURL(p.statusPath, state.JobID)
		// Inner retry on the SAME jobID so a transient blip during
		// status fetch doesn't abandon a paid job; the wrapper reads
		// ProviderJobID to decide whether re-submit is safe.
		statusBody, err := PollWithInnerRetry(pollCtx, "ProxyVideo", func(c context.Context) (map[string]interface{}, error) {
			return p.doJSONRequest(c, http.MethodGet, statusURL, nil)
		})
		if err != nil {
			return &GenerationResult{Success: false, Error: err.Error(), Provider: p.Name(), ProviderJobID: state.JobID}, nil
		}

		statusState := p.parseVideoState(statusBody)
		if statusState.JobID == "" {
			statusState.JobID = state.JobID
		}
		state = statusState
		normalizedStatus := normalizeRemoteStatus(state.Status)

		if req.OnProgress != nil {
			meta := map[string]interface{}{
				"providerJobId":  state.JobID,
				"providerStatus": state.Status,
				"thumbnailUrl":   state.ThumbnailURL,
			}
			if state.Error != "" {
				meta["error"] = state.Error
			}
			req.OnProgress(state.Progress, meta)
		}

		switch normalizedStatus {
		case "completed":
			if len(state.VideoURLs) == 0 {
				return &GenerationResult{Success: false, Error: p.buildVideoProviderError(statusBody, "Video task completed without output URLs"), Provider: p.Name()}, nil
			}
			return &GenerationResult{
				Success:            true,
				VideoURLs:          state.VideoURLs,
				ThumbnailURL:       state.ThumbnailURL,
				ProviderJobID:      state.JobID,
				DownloadVideos:     p.downloadVideos,
				DownloadThumbnails: p.downloadThumbnails,
				Duration:           time.Since(startTime).Milliseconds(),
				Provider:           p.Name(),
			}, nil
		case "failed":
			message := state.Error
			if strings.TrimSpace(message) == "" {
				message = p.buildVideoProviderError(statusBody, fmt.Sprintf("Video generation failed (%s)", state.Status))
			}
			return &GenerationResult{Success: false, Error: message, Provider: p.Name(), ProviderJobID: state.JobID}, nil
		case "cancelled":
			return nil, context.Canceled
		}

		select {
		case <-pollCtx.Done():
			_ = p.Cancel(context.Background(), state.JobID)
			return nil, pollCtx.Err()
		case <-ticker.C:
		}
	}
}

func (p *ProxyVideoProvider) Cancel(ctx context.Context, jobID string) error {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return fmt.Errorf("missing provider job id")
	}
	if strings.TrimSpace(p.cancelPath) == "" {
		return nil
	}
	cancelURL := p.resolveURL(p.cancelPath, jobID)
	_, err := p.doJSONRequest(ctx, http.MethodPost, cancelURL, nil)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "404") || strings.Contains(strings.ToLower(err.Error()), "405") {
			return nil
		}
		return err
	}
	return nil
}

func (p *ProxyVideoProvider) resolveURL(pathTemplate string, jobID string) string {
	base := strings.TrimRight(strings.TrimSpace(p.endpoint), "/")
	pathValue := strings.TrimSpace(pathTemplate)
	if pathValue == "" {
		return base
	}
	pathValue = strings.ReplaceAll(pathValue, "{jobId}", url.PathEscape(jobID))
	if strings.HasPrefix(pathValue, "http://") || strings.HasPrefix(pathValue, "https://") {
		return pathValue
	}
	if !strings.HasPrefix(pathValue, "/") {
		pathValue = "/" + pathValue
	}
	return base + path.Clean(pathValue)
}

func (p *ProxyVideoProvider) doJSONRequest(ctx context.Context, method, targetURL string, payload []byte) (map[string]interface{}, error) {
	var reader io.Reader
	if len(payload) > 0 {
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, targetURL, reader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for key, value := range p.headers {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			continue
		}
		req.Header.Set(key, value)
	}
	if p.apiKey != "" {
		headerName := strings.TrimSpace(p.apiKeyHeader)
		if headerName == "" {
			headerName = "Authorization"
		}
		if req.Header.Get(headerName) == "" {
			req.Header.Set(headerName, p.apiKeyPrefix+p.apiKey)
		}
	}

	client := &http.Client{}
	if p.requestTimeout > 0 {
		client.Timeout = p.requestTimeout
	}
	resp, err := client.Do(req)
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
	if len(bytes.TrimSpace(body)) == 0 {
		return map[string]interface{}{}, nil
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	return decoded, nil
}

func (p *ProxyVideoProvider) buildSubmitPayload(req *GenerationRequest, modelID, resolution string) ([]byte, error) {
	if strings.TrimSpace(p.submitPayloadTemplate) != "" {
		rendered, err := renderJSONTemplate(p.submitPayloadTemplate, map[string]interface{}{
			"model":             modelID,
			"prompt":            req.Prompt,
			"negativePrompt":    req.NegativePrompt,
			"duration":          req.Duration,
			"startFrame":        req.StartFrame,
			"endFrame":          req.EndFrame,
			"resolution":        resolution,
			"generationMethod":  req.GenerationMethod,
			"motionMode":        req.MotionMode,
			"motionOrientation": req.MotionOrientation,
			"referenceImages":   req.ReferenceImages,
			"referenceVideos":   req.ReferenceVideos,
			"referenceAudios":   req.ReferenceAudios,
			"width":             req.Width,
			"height":            req.Height,
			"steps":             req.Steps,
			"cfgScale":          req.CFGScale,
			"seed":              req.Seed,
			"stylePreset":       req.StylePreset,
			"sampler":           req.Sampler,
			"numImages":         req.NumImages,
		})
		if err != nil {
			return nil, err
		}
		return json.Marshal(rendered)
	}

	body := genericVideoRequest{
		Model:             modelID,
		Prompt:            req.Prompt,
		Duration:          req.Duration,
		StartFrame:        req.StartFrame,
		EndFrame:          req.EndFrame,
		Resolution:        resolution,
		GenerationMethod:  req.GenerationMethod,
		MotionMode:        req.MotionMode,
		MotionOrientation: req.MotionOrientation,
		ReferenceImages:   req.ReferenceImages,
		ReferenceVideos:   req.ReferenceVideos,
		ReferenceAudios:   req.ReferenceAudios,
		Width:             req.Width,
		Height:            req.Height,
	}
	return json.Marshal(body)
}

func (p *ProxyVideoProvider) parseVideoState(body map[string]interface{}) parsedVideoState {
	state := parsedVideoState{}
	state.JobID = firstNonEmptyString(
		getByPathString(body, p.responseJobIDField),
		getByPathString(body, "jobId"),
		getByPathString(body, "taskId"),
		getByPathString(body, "id"),
		getByPathString(body, "data.jobId"),
		getByPathString(body, "data.taskId"),
		getByPathString(body, "data.id"),
	)
	state.Status = firstNonEmptyString(
		getByPathString(body, p.responseStatusField),
		getByPathString(body, "status"),
		getByPathString(body, "data.status"),
		getByPathString(body, "data.task.status"),
		getByPathString(body, "result.status"),
	)
	state.Progress = firstPositiveInt(
		getByPathInt(body, p.responseProgressField),
		getByPathInt(body, "progress"),
		getByPathInt(body, "data.progress"),
		getByPathInt(body, "data.task.progress"),
	)
	state.Error = firstNonEmptyString(
		getByPathString(body, p.responseErrorField),
		getByPathString(body, "error.message"),
		getByPathString(body, "message"),
		getByPathString(body, "data.message"),
		getByPathString(body, "data.error"),
		getByPathString(body, "data.error.message"),
	)
	if strings.TrimSpace(p.responseVideoURLsField) != "" {
		state.VideoURLs = uniqueStrings(extractStringSliceByPath(body, p.responseVideoURLsField))
	}
	state.VideoURLs = append(state.VideoURLs, extractStringSliceByPath(body, "videoUrls")...)
	state.VideoURLs = append(state.VideoURLs, extractStringSliceByPath(body, "data.videoUrls")...)
	state.VideoURLs = append(state.VideoURLs, extractStringSliceByPath(body, "result.videoUrls")...)
	state.VideoURLs = append(state.VideoURLs, extractStringSliceByPath(body, "output.videoUrls")...)
	state.VideoURLs = append(state.VideoURLs, extractDataURLs(body)...)
	state.VideoURLs = uniqueStrings(state.VideoURLs)
	state.VideoURLs = p.applyOutputTemplate(state.VideoURLs, p.responseVideoURLTemplate)
	state.ThumbnailURL = firstNonEmptyString(
		getByPathString(body, p.responseThumbnailField),
		getByPathString(body, "thumbnailUrl"),
		getByPathString(body, "data.thumbnailUrl"),
		getByPathString(body, "result.thumbnailUrl"),
		getByPathString(body, "output.thumbnailUrl"),
		getFirstDataField(body, "thumbnailUrl"),
	)
	state.ThumbnailURL = p.applyOutputTemplateToSingle(state.ThumbnailURL, p.responseThumbnailTemplate)
	return state
}

func normalizeRemoteStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "queued", "pending", "processing", "running", "in_progress", "submitted":
		return "processing"
	case "completed", "succeeded", "success", "done", "finished":
		return "completed"
	case "failed", "error", "rejected", "expired":
		return "failed"
	case "cancelled", "canceled", "aborted":
		return "cancelled"
	default:
		return "processing"
	}
}

func (p *ProxyVideoProvider) buildVideoProviderError(body map[string]interface{}, fallback string) string {
	state := p.parseVideoState(body)
	if strings.TrimSpace(state.Error) != "" {
		return state.Error
	}
	if strings.TrimSpace(fallback) != "" {
		return fallback
	}
	return "video provider request failed"
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func firstPositiveInt(values ...int) int {
	for _, value := range values {
		if value > 0 {
			if value > 100 {
				return 100
			}
			return value
		}
	}
	return 0
}

func getByPath(body map[string]interface{}, pathExpr string) (interface{}, bool) {
	if body == nil {
		return nil, false
	}
	parts := strings.Split(pathExpr, ".")
	var current interface{} = body
	for _, part := range parts {
		switch typed := current.(type) {
		case map[string]interface{}:
			value, ok := typed[part]
			if !ok {
				return nil, false
			}
			current = value
		case []interface{}:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(typed) {
				return nil, false
			}
			current = typed[index]
		default:
			return nil, false
		}
	}
	return current, true
}

func getByPathString(body map[string]interface{}, pathExpr string) string {
	value, ok := getByPath(body, pathExpr)
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		return strconv.Itoa(typed)
	default:
		return ""
	}
}

func getByPathInt(body map[string]interface{}, pathExpr string) int {
	value, ok := getByPath(body, pathExpr)
	if !ok || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err == nil {
			return parsed
		}
	}
	return 0
}

func extractStringSliceByPath(body map[string]interface{}, pathExpr string) []string {
	value, ok := getByPath(body, pathExpr)
	if !ok || value == nil {
		return nil
	}
	result := make([]string, 0)
	switch typed := value.(type) {
	case []string:
		for _, item := range typed {
			if strings.TrimSpace(item) != "" {
				result = append(result, item)
			}
		}
	case []interface{}:
		for _, item := range typed {
			if str := extractURLLikeString(item); strings.TrimSpace(str) != "" {
				result = append(result, str)
			}
		}
	case string:
		if strings.TrimSpace(typed) != "" {
			result = append(result, typed)
		}
	}
	return result
}

func (p *ProxyVideoProvider) applyOutputTemplate(items []string, template string) []string {
	if strings.TrimSpace(template) == "" {
		return items
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if resolved := p.applyOutputTemplateToSingle(item, template); resolved != "" {
			result = append(result, resolved)
		}
	}
	return uniqueStrings(result)
}

func (p *ProxyVideoProvider) applyOutputTemplateToSingle(value string, template string) string {
	trimmedValue := strings.TrimSpace(value)
	if trimmedValue == "" {
		return ""
	}
	trimmedTemplate := strings.TrimSpace(template)
	if trimmedTemplate == "" || strings.HasPrefix(trimmedValue, "http://") || strings.HasPrefix(trimmedValue, "https://") {
		return trimmedValue
	}
	resolved := strings.ReplaceAll(trimmedTemplate, "{valueEscaped}", url.PathEscape(trimmedValue))
	resolved = strings.ReplaceAll(resolved, "{value}", trimmedValue)
	if strings.HasPrefix(resolved, "http://") || strings.HasPrefix(resolved, "https://") {
		return resolved
	}
	return p.resolveURL(resolved, "")
}

func extractURLLikeString(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case map[string]interface{}:
		return firstNonEmptyString(
			getByPathString(typed, "url"),
			getByPathString(typed, "videoUrl"),
			getByPathString(typed, "outputUrl"),
			getByPathString(typed, "src"),
			getByPathString(typed, "downloadUrl"),
		)
	default:
		return ""
	}
}

func extractDataURLs(body map[string]interface{}) []string {
	result := make([]string, 0)
	for _, pathExpr := range []string{"data", "result.data", "output.data"} {
		value, ok := getByPath(body, pathExpr)
		if !ok || value == nil {
			continue
		}
		items, ok := value.([]interface{})
		if !ok {
			continue
		}
		for _, item := range items {
			itemMap, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			urlValue := firstNonEmptyString(
				getByPathString(itemMap, "url"),
				getByPathString(itemMap, "videoUrl"),
				getByPathString(itemMap, "outputUrl"),
			)
			if urlValue != "" {
				result = append(result, urlValue)
			}
		}
	}
	return result
}

func getFirstDataField(body map[string]interface{}, field string) string {
	value, ok := getByPath(body, "data")
	if !ok || value == nil {
		return ""
	}
	items, ok := value.([]interface{})
	if !ok {
		return ""
	}
	for _, item := range items {
		itemMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if v := getByPathString(itemMap, field); strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func uniqueStrings(items []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(items))
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		result = append(result, trimmed)
	}
	return result
}

func truncateResponse(body []byte, max int) string {
	trimmed := strings.TrimSpace(string(body))
	if len(trimmed) <= max {
		return trimmed
	}
	return trimmed[:max]
}

func readLimitedProviderResponse(body io.Reader, maxBytes int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("provider response too large")
	}
	return data, nil
}

func renderJSONTemplate(template string, vars map[string]interface{}) (interface{}, error) {
	var raw interface{}
	if err := json.Unmarshal([]byte(template), &raw); err != nil {
		return nil, err
	}
	return resolveTemplateNode(raw, vars), nil
}

func resolveTemplateNode(node interface{}, vars map[string]interface{}) interface{} {
	switch typed := node.(type) {
	case map[string]interface{}:
		if variableName, ok := typed["$var"].(string); ok && len(typed) == 1 {
			return vars[variableName]
		}
		resolved := make(map[string]interface{}, len(typed))
		for key, value := range typed {
			next := resolveTemplateNode(value, vars)
			if shouldOmitTemplateValue(next) {
				continue
			}
			resolved[key] = next
		}
		return resolved
	case []interface{}:
		resolved := make([]interface{}, 0, len(typed))
		for _, item := range typed {
			next := resolveTemplateNode(item, vars)
			if shouldOmitTemplateValue(next) {
				continue
			}
			resolved = append(resolved, next)
		}
		return resolved
	case string:
		return resolveTemplateString(typed, vars)
	default:
		return node
	}
}

func resolveTemplateString(template string, vars map[string]interface{}) interface{} {
	trimmed := strings.TrimSpace(template)
	if strings.HasPrefix(trimmed, "{{") && strings.HasSuffix(trimmed, "}}") && strings.Count(trimmed, "{{") == 1 && strings.Count(trimmed, "}}") == 1 {
		varName := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "{{"), "}}"))
		if value, ok := vars[varName]; ok {
			return value
		}
		return ""
	}
	result := template
	for key, value := range vars {
		placeholder := "{{" + key + "}}"
		if !strings.Contains(result, placeholder) {
			continue
		}
		result = strings.ReplaceAll(result, placeholder, scalarTemplateValue(value))
	}
	return result
}

func scalarTemplateValue(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return typed
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 32)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

func shouldOmitTemplateValue(value interface{}) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(typed) == ""
	case []interface{}:
		return len(typed) == 0
	case []ReferenceImageData:
		return len(typed) == 0
	case map[string]interface{}:
		return len(typed) == 0
	default:
		return false
	}
}
