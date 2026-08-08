package llm

import (
	"time"
)

// UniversalLLMRequest - 统一所有LLM请求的数据结构
// 基于Linus原则：好的数据结构比聪明的算法更重要
type UniversalLLMRequest struct {
	// 核心内容
	SystemPrompt string `json:"system_prompt"`
	UserMessage  string `json:"user_message"`

	// 可选参数 - 统一默认值处理
	Temperature float64 `json:"temperature,omitempty"`
	MaxTokens   int     `json:"max_tokens,omitempty"`
	JSONMode    bool    `json:"json_mode,omitempty"` // Enable JSON mode for structured output

	// 上下文（可选）
	Context string `json:"context,omitempty"`

	// 元数据
	RequestID string `json:"request_id,omitempty"`
	UserID    uint   `json:"user_id,omitempty"`
	Service   string `json:"service,omitempty"` // 用于监控和调试
}

// UniversalLLMResponse - 统一所有LLM响应
type UniversalLLMResponse struct {
	// 核心内容
	Content string `json:"content"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`

	// 元数据
	Model        string         `json:"model"`
	TokenUsage   map[string]int `json:"token_usage,omitempty"`
	ResponseTime time.Duration  `json:"response_time"`
	RequestID    string         `json:"request_id,omitempty"`
	Service      string         `json:"service,omitempty"`

	// 调试信息
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// StreamingHandler - 流式响应处理器
type StreamingHandler func(chunk string) error

// DefaultConfig - 默认配置常量
const (
	DefaultTemperature = 0.7
	DefaultMaxTokens   = 2000
	DefaultTimeout     = 30 // seconds
)
