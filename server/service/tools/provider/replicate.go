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
	"strings"
	"time"
)

// ReplicateProvider Replicate API 提供商
type ReplicateProvider struct {
	name     string
	endpoint string
	apiKey   string
	model    string
	enabled  bool
}

// NewReplicateProvider 创建 Replicate 提供商
func NewReplicateProvider(cfg *ProviderConfig) *ReplicateProvider {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		endpoint = "https://api.replicate.com"
	}

	return &ReplicateProvider{
		name:     cfg.Name,
		endpoint: strings.TrimRight(endpoint, "/"),
		apiKey:   cfg.APIKey,
		model:    cfg.Model,
		enabled:  cfg.Enabled,
	}
}

func (p *ReplicateProvider) Name() string {
	return p.name
}

func (p *ReplicateProvider) Type() string {
	return "replicate"
}

func (p *ReplicateProvider) IsEnabled() bool {
	return p.enabled && p.apiKey != ""
}

// replicateCreateRequest Replicate API 创建预测请求
type replicateCreateRequest struct {
	Model string                 `json:"model,omitempty"`
	Input map[string]interface{} `json:"input"`
}

// replicatePrediction Replicate API 预测响应
type replicatePrediction struct {
	ID          string                 `json:"id"`
	Status      string                 `json:"status"`
	Output      interface{}            `json:"output"`
	Error       interface{}            `json:"error"`
	Metrics     map[string]interface{} `json:"metrics"`
	URLs        map[string]string      `json:"urls"`
	CreatedAt   string                 `json:"created_at"`
	CompletedAt string                 `json:"completed_at"`
}

// Generate 调用 Replicate API 生成图片
func (p *ReplicateProvider) Generate(ctx context.Context, req *GenerationRequest) (*GenerationResult, error) {
	startTime := time.Now()

	if !p.IsEnabled() {
		return &GenerationResult{
			Success:  false,
			Error:    "Replicate provider is not enabled",
			Provider: p.Name(),
		}, nil
	}

	// 构建提示词
	prompt := req.Prompt
	if req.StylePreset != "" {
		prompt += StylePresetToPromptSuffix(req.StylePreset)
	}

	// 构建输入参数
	input := map[string]interface{}{
		"prompt": prompt,
	}

	// 添加可选参数
	if req.NegativePrompt != "" {
		input["negative_prompt"] = req.NegativePrompt
	}
	if req.Width > 0 {
		input["width"] = req.Width
	}
	if req.Height > 0 {
		input["height"] = req.Height
	}
	if req.Steps > 0 {
		input["num_inference_steps"] = req.Steps
	}
	if req.CFGScale > 0 {
		input["guidance_scale"] = req.CFGScale
	}
	if req.Seed > 0 {
		input["seed"] = req.Seed
	}
	if req.NumImages > 0 {
		input["num_outputs"] = req.NumImages
	} else {
		input["num_outputs"] = 1
	}

	// 构建请求
	createReq := replicateCreateRequest{
		Input: input,
	}

	// 使用 model
	if p.model != "" {
		createReq.Model = p.model
	} else {
		createReq.Model = "black-forest-labs/flux-schnell"
	}

	// 序列化请求
	jsonData, err := json.Marshal(createReq)
	if err != nil {
		return &GenerationResult{
			Success:  false,
			Error:    fmt.Sprintf("Failed to marshal request: %v", err),
			Provider: p.Name(),
		}, nil
	}

	// 创建预测
	prediction, err := p.createPrediction(ctx, jsonData)
	if err != nil {
		return &GenerationResult{
			Success:  false,
			Error:    fmt.Sprintf("Failed to create prediction: %v", err),
			Provider: p.Name(),
		}, nil
	}

	// 轮询等待结果
	prediction, err = p.waitForCompletion(ctx, prediction.ID)
	if err != nil {
		return &GenerationResult{
			Success:  false,
			Error:    fmt.Sprintf("Failed waiting for prediction: %v", err),
			Provider: p.Name(),
		}, nil
	}

	// 检查状态
	if prediction.Status == "failed" || prediction.Status == "canceled" {
		errorMsg := "Generation failed"
		if prediction.Error != nil {
			errorMsg = fmt.Sprintf("%v", prediction.Error)
		}
		return &GenerationResult{
			Success:  false,
			Error:    errorMsg,
			Provider: p.Name(),
		}, nil
	}

	// 提取输出图片
	imageURLs, imageData, err := p.extractOutput(prediction.Output)
	if err != nil {
		return &GenerationResult{
			Success:  false,
			Error:    fmt.Sprintf("Failed to extract output: %v", err),
			Provider: p.Name(),
		}, nil
	}

	duration := time.Since(startTime).Milliseconds()

	return &GenerationResult{
		Success:   true,
		ImageURLs: imageURLs,
		ImageData: imageData,
		Seed:      req.Seed,
		Duration:  duration,
		Provider:  p.Name(),
	}, nil
}

// createPrediction 创建预测
func (p *ReplicateProvider) createPrediction(ctx context.Context, jsonData []byte) (*replicatePrediction, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.endpoint+"/v1/predictions", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Prefer", "wait")

	client := &http.Client{Timeout: 300 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		globals.Error(fmt.Sprintf("Replicate API error: %s", string(body)))
		return nil, fmt.Errorf("API error: %s", resp.Status)
	}

	var prediction replicatePrediction
	if err := json.Unmarshal(body, &prediction); err != nil {
		return nil, err
	}

	return &prediction, nil
}

// waitForCompletion 等待预测完成
func (p *ReplicateProvider) waitForCompletion(ctx context.Context, predictionID string) (*replicatePrediction, error) {
	url := fmt.Sprintf("%s/v1/predictions/%s", p.endpoint, predictionID)

	for i := 0; i < 120; i++ { // 最多等待 2 分钟
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, err
		}

		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Do(httpReq)
		if err != nil {
			return nil, err
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}

		var prediction replicatePrediction
		if err := json.Unmarshal(body, &prediction); err != nil {
			return nil, err
		}

		if prediction.Status == "succeeded" || prediction.Status == "failed" || prediction.Status == "canceled" {
			return &prediction, nil
		}

		time.Sleep(1 * time.Second)
	}

	return nil, fmt.Errorf("prediction timed out")
}

// extractOutput 提取输出
func (p *ReplicateProvider) extractOutput(output interface{}) ([]string, [][]byte, error) {
	var imageURLs []string
	var imageData [][]byte

	switch v := output.(type) {
	case string:
		// 单个URL或base64
		if strings.HasPrefix(v, "data:") {
			// Base64 数据
			parts := strings.SplitN(v, ",", 2)
			if len(parts) == 2 {
				data, err := base64.StdEncoding.DecodeString(parts[1])
				if err == nil {
					imageData = append(imageData, data)
				}
			}
		} else {
			imageURLs = append(imageURLs, v)
		}
	case []interface{}:
		// 多个输出
		for _, item := range v {
			if s, ok := item.(string); ok {
				if strings.HasPrefix(s, "data:") {
					parts := strings.SplitN(s, ",", 2)
					if len(parts) == 2 {
						data, err := base64.StdEncoding.DecodeString(parts[1])
						if err == nil {
							imageData = append(imageData, data)
						}
					}
				} else {
					imageURLs = append(imageURLs, s)
				}
			}
		}
	}

	if len(imageURLs) == 0 && len(imageData) == 0 {
		return nil, nil, fmt.Errorf("no output images found")
	}

	return imageURLs, imageData, nil
}
