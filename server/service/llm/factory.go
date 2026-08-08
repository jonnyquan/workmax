package llm

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"server/globals"

	"github.com/tmc/langchaingo/llms/openai"
)

// 单例客户端 - 消除18个重复实现
var (
	clientInstance              *openai.LLM
	clientOnce                  sync.Once
	imageAnalysisClientInstance *openai.LLM
	imageAnalysisClientOnce     sync.Once
)

// GetClient 返回全局唯一的LLM客户端
// 这个函数将替代所有现有的重复客户端创建代码
func GetClient() *openai.LLM {
	clientOnce.Do(func() {
		clientInstance = createClient()
	})
	return clientInstance
}

// GetImageAnalysisClient 返回图片分析独立客户端（用于视觉等专用能力）
func GetImageAnalysisClient() *openai.LLM {
	imageAnalysisClientOnce.Do(func() {
		imageAnalysisClientInstance = createImageAnalysisClient()
	})
	return imageAnalysisClientInstance
}

// createClient 创建LLM客户端 - 统一配置逻辑
func createClient() *openai.LLM {
	endpoint, apiKey, model, ok := resolveTextClientSettings()
	if !ok {
		return nil
	}
	return createClientWithSettings(endpoint, apiKey, model, "Universal")
}

// IsClientReady 检查客户端是否可用
func IsClientReady() bool {
	return GetClient() != nil
}

// IsImageAnalysisClientReady 检查图片分析客户端是否可用
func IsImageAnalysisClientReady() bool {
	return GetImageAnalysisClient() != nil
}

// GetModel 获取当前使用的模型名称
func GetModel() string {
	return resolveConfiguredTextModel()
}

// GetImageAnalysisModel 获取图片分析客户端配置的模型名
func GetImageAnalysisModel() string {
	cfg := globals.GraConf.Aihub.ImageAnalysis
	if cfg.Model != "" {
		return cfg.Model
	}
	return "gemini-2.0-flash"
}

func createImageAnalysisClient() *openai.LLM {
	cfg := globals.GraConf.Aihub.ImageAnalysis
	if !cfg.Enabled {
		globals.Warn("Image analysis LLM is disabled in config")
		return nil
	}
	if cfg.ApiKey == "" || cfg.Endpoint == "" {
		globals.Error("Image analysis LLM configuration missing: API key or endpoint not set")
		return nil
	}

	model := cfg.Model
	if model == "" {
		model = "gemini-2.0-flash"
	}

	return createClientWithSettings(cfg.Endpoint, cfg.ApiKey, model, "ImageAnalysis")
}

func resolveTextClientSettings() (endpoint string, apiKey string, model string, ok bool) {
	cfg := globals.GraConf.Aihub

	if !cfg.Text.Enabled {
		globals.Error("Text LLM is disabled in config")
		return "", "", "", false
	}
	if cfg.Text.Endpoint == "" || cfg.Text.ApiKey == "" {
		globals.Error("Text LLM configuration missing: API key or endpoint not set")
		return "", "", "", false
	}
	if cfg.Text.Model == "" {
		globals.Error("Text LLM configuration missing: model not set")
		return "", "", "", false
	}

	return cfg.Text.Endpoint, cfg.Text.ApiKey, resolveConfiguredTextModel(), true
}

func resolveConfiguredTextModel() string {
	return globals.GraConf.Aihub.Text.Model
}

func createClientWithSettings(endpoint, apiKey, model, label string) *openai.LLM {
	// 创建HTTP客户端，设置合适的超时时间
	httpClient := &http.Client{
		Timeout: 300 * time.Second, // 5分钟超时，足够处理长文本生成
		Transport: &http.Transport{
			MaxIdleConns:       10,
			IdleConnTimeout:    30 * time.Second,
			DisableCompression: false,
		},
	}

	client, err := openai.New(
		openai.WithBaseURL(endpoint),
		openai.WithToken(apiKey),
		openai.WithModel(model),
		openai.WithHTTPClient(httpClient),
	)
	if err != nil {
		globals.Error(fmt.Sprintf("Failed to create %s LLM client: %v", label, err))
		return nil
	}

	globals.Info(fmt.Sprintf("%s LLM client created: endpoint=%s, model=%s", label, endpoint, model))
	return client
}
