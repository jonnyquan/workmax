package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"server/model"
	"strings"
	"time"
)

type VertexVeoProvider struct {
	name         string
	providerType string
	enabled      bool
	vertex       vertexConfig
	pollInterval time.Duration
	maxWait      time.Duration
	storageURI   string
	personGen    string
	sampleCount  int
}

func NewVertexVeoProvider(cfg *ProviderConfig) *VertexVeoProvider {
	pollInterval := defaultVeoPollInterval
	maxWait := defaultVeoMaxWait
	storageURI := ""
	personGen := ""
	sampleCount := 0
	if cfg != nil && cfg.ExtraConfig != nil {
		if cfg.ExtraConfig.PollInterval > 0 {
			pollInterval = time.Duration(cfg.ExtraConfig.PollInterval) * time.Second
		}
		if cfg.ExtraConfig.MaxWaitSeconds > 0 {
			maxWait = time.Duration(cfg.ExtraConfig.MaxWaitSeconds) * time.Second
		}
		storageURI = strings.TrimSpace(cfg.ExtraConfig.VertexStorageURI)
		personGen = strings.TrimSpace(cfg.ExtraConfig.PersonGeneration)
		sampleCount = cfg.ExtraConfig.SampleCount
	}
	return &VertexVeoProvider{
		name:         cfg.Name,
		providerType: cfg.Type,
		enabled:      cfg.Enabled,
		vertex:       newVertexConfig(cfg),
		pollInterval: pollInterval,
		maxWait:      maxWait,
		storageURI:   storageURI,
		personGen:    personGen,
		sampleCount:  sampleCount,
	}
}

func (p *VertexVeoProvider) Name() string { return p.name }

func (p *VertexVeoProvider) Type() string { return p.providerType }

func (p *VertexVeoProvider) IsEnabled() bool {
	return p.enabled && strings.TrimSpace(p.vertex.projectID) != ""
}

func (p *VertexVeoProvider) Generate(ctx context.Context, req *GenerationRequest) (*GenerationResult, error) {
	startTime := time.Now()
	if !p.IsEnabled() {
		return &GenerationResult{Success: false, Error: "Vertex Veo provider is not enabled or project id not configured", Provider: p.Name()}, nil
	}

	payload, err := p.buildPredictPayload(req)
	if err != nil {
		return &GenerationResult{Success: false, Error: err.Error(), Provider: p.Name()}, nil
	}

	modelCode := p.resolveModelCode(req.Model)
	requestPath := p.vertex.modelPath(modelCode)
	doRequest := p.vertex.doJSONRequest
	if p.vertex.apiKeyMode() {
		doRequest = p.vertex.doAPIKeyJSONRequest
	}
	var createResp map[string]interface{}
	if err := doRequest(ctx, http.MethodPost, requestPath+":predictLongRunning", payload, &createResp); err != nil {
		return &GenerationResult{Success: false, Error: err.Error(), Provider: p.Name()}, nil
	}
	operationName := strings.TrimSpace(getByPathString(createResp, "name"))
	if operationName == "" {
		return &GenerationResult{Success: false, Error: "missing Vertex Veo operation name", Provider: p.Name()}, nil
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
		statusResp, err := PollWithInnerRetry(pollCtx, "Vertex Veo", func(c context.Context) (map[string]interface{}, error) {
			var out map[string]interface{}
			body, _ := json.Marshal(map[string]interface{}{"operationName": operationName})
			if err := doRequest(c, http.MethodPost, requestPath+":fetchPredictOperation", body, &out); err != nil {
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
			result, err := p.extractResult(pollCtx, statusResp, operationName, time.Since(startTime).Milliseconds())
			if err != nil {
				return &GenerationResult{Success: false, Error: err.Error(), Provider: p.Name(), ProviderJobID: operationName}, nil
			}
			return result, nil
		}

		select {
		case <-pollCtx.Done():
			return nil, pollCtx.Err()
		case <-ticker.C:
		}
	}
}

func (p *VertexVeoProvider) Cancel(ctx context.Context, jobID string) error {
	return fmt.Errorf("Vertex Veo provider does not support remote cancel")
}

func (p *VertexVeoProvider) resolveModelCode(modelID string) string {
	modelCode := strings.TrimSpace(modelID)
	if modelCode == "" || modelCode == model.VEO_31 || modelCode == model.VEO_31_FAST {
		if configured := strings.TrimSpace(p.vertex.model); configured != "" {
			modelCode = configured
		}
	}
	switch modelCode {
	case "", model.VEO_31:
		return "veo-3.1-generate-001"
	case model.VEO_31_FAST:
		return "veo-3.1-fast-generate-001"
	default:
		return modelCode
	}
}

func (p *VertexVeoProvider) buildPredictPayload(req *GenerationRequest) ([]byte, error) {
	payload, err := (&VeoProvider{}).buildPredictPayload(req)
	if err != nil {
		return nil, err
	}
	var body map[string]interface{}
	if err := json.Unmarshal(payload, &body); err != nil {
		return nil, err
	}
	params, _ := body["parameters"].(map[string]interface{})
	if params == nil {
		params = map[string]interface{}{}
		body["parameters"] = params
	}
	sampleCount := p.sampleCount
	if sampleCount <= 0 {
		sampleCount = 1
	}
	if sampleCount > 4 {
		sampleCount = 4
	}
	params["sampleCount"] = sampleCount
	if req.Seed > 0 {
		params["seed"] = req.Seed
	}
	if p.personGen != "" {
		params["personGeneration"] = p.personGen
	}
	if p.storageURI != "" {
		params["storageUri"] = p.storageURI
	}
	return json.Marshal(body)
}

func (p *VertexVeoProvider) extractResult(ctx context.Context, statusResp map[string]interface{}, operationName string, elapsedMS int64) (*GenerationResult, error) {
	if encoded := firstNonEmptyString(
		getByPathString(statusResp, "response.videos.0.bytesBase64Encoded"),
		getByPathString(statusResp, "response.generatedVideos.0.video.bytesBase64Encoded"),
		getByPathString(statusResp, "response.generateVideoResponse.generatedSamples.0.video.bytesBase64Encoded"),
	); encoded != "" {
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("failed to decode Vertex Veo video: %w", err)
		}
		contentType := firstNonEmptyString(
			getByPathString(statusResp, "response.videos.0.mimeType"),
			getByPathString(statusResp, "response.generatedVideos.0.video.mimeType"),
			"video/mp4",
		)
		return &GenerationResult{
			Success:            true,
			ProviderJobID:      operationName,
			VideoData:          [][]byte{data},
			VideoContentTypes:  []string{contentType},
			DownloadVideos:     false,
			DownloadThumbnails: false,
			Duration:           elapsedMS,
			Provider:           p.Name(),
		}, nil
	}

	videoURI := firstNonEmptyString(
		getByPathString(statusResp, "response.videos.0.gcsUri"),
		getByPathString(statusResp, "response.videos.0.uri"),
		getByPathString(statusResp, "response.generatedVideos.0.video.gcsUri"),
		getByPathString(statusResp, "response.generatedVideos.0.video.uri"),
		getByPathString(statusResp, "response.generateVideoResponse.generatedSamples.0.video.uri"),
	)
	if videoURI == "" {
		return nil, fmt.Errorf("missing Vertex Veo video output")
	}
	if strings.HasPrefix(strings.ToLower(videoURI), "http://") || strings.HasPrefix(strings.ToLower(videoURI), "https://") {
		data, contentType, err := p.downloadVideoBinary(ctx, videoURI)
		if err != nil {
			return nil, err
		}
		return &GenerationResult{
			Success:            true,
			ProviderJobID:      operationName,
			VideoData:          [][]byte{data},
			VideoContentTypes:  []string{contentType},
			DownloadVideos:     false,
			DownloadThumbnails: false,
			Duration:           elapsedMS,
			Provider:           p.Name(),
		}, nil
	}
	return &GenerationResult{
		Success:            true,
		ProviderJobID:      operationName,
		VideoURLs:          []string{videoURI},
		DownloadVideos:     false,
		DownloadThumbnails: false,
		Duration:           elapsedMS,
		Provider:           p.Name(),
	}, nil
}

func (p *VertexVeoProvider) downloadVideoBinary(ctx context.Context, videoURI string) ([]byte, string, error) {
	client, err := p.vertex.httpClient(ctx)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, videoURI, nil)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create Vertex video download request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("Vertex video download failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := readLimitedProviderResponse(resp.Body, providerMaxVideoDownloadLen)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read Vertex video: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, "", fmt.Errorf("Vertex video download http %d: %s", resp.StatusCode, strings.TrimSpace(truncateResponse(body, 240)))
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
