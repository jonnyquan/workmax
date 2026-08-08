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
	"time"
)

// ProxyProvider 本地代理提供商 (兼容 OpenAI Images API 格式)
type ProxyProvider struct {
	name     string
	endpoint string
	apiKey   string
	model    string
	enabled  bool
}

// NewProxyProvider 创建本地代理提供商
func NewProxyProvider(cfg *ProviderConfig) *ProxyProvider {
	return &ProxyProvider{
		name:     cfg.Name,
		endpoint: cfg.Endpoint,
		apiKey:   cfg.APIKey,
		model:    cfg.Model,
		enabled:  cfg.Enabled,
	}
}

func (p *ProxyProvider) Name() string {
	return p.name
}

func (p *ProxyProvider) Type() string {
	return "custom"
}

func (p *ProxyProvider) IsEnabled() bool {
	return p.enabled && p.endpoint != ""
}

// openaiImageRequest OpenAI 兼容的图片生成请求
type openaiImageRequest struct {
	Model          string `json:"model,omitempty"`
	Prompt         string `json:"prompt"`
	N              int    `json:"n,omitempty"`
	Size           string `json:"size,omitempty"`
	ResponseFormat string `json:"response_format,omitempty"`
	Quality        string `json:"quality,omitempty"`
	Style          string `json:"style,omitempty"`
}

// openaiImageResponse OpenAI 兼容的图片生成响应
type openaiImageResponse struct {
	Created int64 `json:"created"`
	Data    []struct {
		URL           string `json:"url,omitempty"`
		B64JSON       string `json:"b64_json,omitempty"`
		RevisedPrompt string `json:"revised_prompt,omitempty"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error,omitempty"`
}

// Generate 调用本地代理 API 生成图片
func (p *ProxyProvider) Generate(ctx context.Context, req *GenerationRequest) (*GenerationResult, error) {
	startTime := time.Now()

	if !p.IsEnabled() {
		return &GenerationResult{
			Success:  false,
			Error:    "Proxy provider is not enabled",
			Provider: p.Name(),
		}, nil
	}

	// 构建提示词 (添加风格后缀)
	prompt := req.Prompt
	if req.StylePreset != "" {
		prompt += StylePresetToPromptSuffix(req.StylePreset)
	}

	// 构建尺寸字符串
	size := fmt.Sprintf("%dx%d", req.Width, req.Height)

	// 确定使用的模型
	model := p.model
	if req.Model != "" {
		model = req.Model
	}

	// 构建请求
	proxyReq := openaiImageRequest{
		Model:          model,
		Prompt:         prompt,
		N:              1,
		Size:           size,
		ResponseFormat: "b64_json",
	}

	// 序列化请求
	jsonData, err := json.Marshal(proxyReq)
	if err != nil {
		return &GenerationResult{
			Success:  false,
			Error:    fmt.Sprintf("Failed to marshal request: %v", err),
			Provider: p.Name(),
		}, nil
	}

	// 构建 API URL
	apiURL := p.endpoint
	if apiURL[len(apiURL)-1] != '/' {
		apiURL += "/"
	}
	apiURL += "v1/images/generations"

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
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	// 发送请求
	client := &http.Client{Timeout: 300 * time.Second}
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

	// 解析响应
	var proxyResp openaiImageResponse
	if err := json.Unmarshal(body, &proxyResp); err != nil {
		globals.Error(fmt.Sprintf("Proxy response parse error: %s", string(body)))
		return &GenerationResult{
			Success:  false,
			Error:    fmt.Sprintf("Failed to parse response: %v", err),
			Provider: p.Name(),
		}, nil
	}

	// 检查错误
	if proxyResp.Error != nil {
		return &GenerationResult{
			Success:  false,
			Error:    proxyResp.Error.Message,
			Provider: p.Name(),
		}, nil
	}

	// 提取图片数据
	if len(proxyResp.Data) == 0 {
		return &GenerationResult{
			Success:  false,
			Error:    "No images generated",
			Provider: p.Name(),
		}, nil
	}

	imageData := make([][]byte, 0, len(proxyResp.Data))
	imageURLs := make([]string, 0, len(proxyResp.Data))

	for _, item := range proxyResp.Data {
		if item.B64JSON != "" {
			data, err := base64.StdEncoding.DecodeString(item.B64JSON)
			if err != nil {
				continue
			}
			imageData = append(imageData, data)
		} else if item.URL != "" {
			imageURLs = append(imageURLs, item.URL)
		}
	}

	duration := time.Since(startTime).Milliseconds()

	return &GenerationResult{
		Success:   true,
		ImageData: imageData,
		ImageURLs: imageURLs,
		Seed:      req.Seed,
		Duration:  duration,
		Provider:  p.Name(),
	}, nil
}
