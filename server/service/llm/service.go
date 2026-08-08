package llm

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/tmc/langchaingo/llms"
	"server/globals"
)

// UniversalLLMService - 替代所有现有LLM服务
// 基于Linus原则：一个简单的解决方案胜过复杂的架构
type UniversalLLMService struct {
	// 无需存储client，直接使用全局单例
}

// 全局服务实例
var serviceInstance *UniversalLLMService
var serviceOnce sync.Once

// GetService 返回服务实例 - 单例模式
func GetService() *UniversalLLMService {
	serviceOnce.Do(func() {
		serviceInstance = &UniversalLLMService{}
	})
	return serviceInstance
}

// ProcessRequest - 一个函数处理所有LLM请求
// 这个函数将替代18个不同服务的重复实现
func (s *UniversalLLMService) ProcessRequest(ctx context.Context, req UniversalLLMRequest) (*UniversalLLMResponse, error) {
	startTime := time.Now()

	// 生成请求ID用于追踪
	if req.RequestID == "" {
		req.RequestID = fmt.Sprintf("llm_%d", startTime.UnixNano())
	}

	globals.Info(fmt.Sprintf("[UniversalLLM] Processing request %s from service %s", req.RequestID, req.Service))

	client := GetClient()
	if client == nil {
		return buildErrorResponse(req.RequestID, req.Service, "LLM client not available", startTime),
			fmt.Errorf("LLM client not initialized")
	}

	// 构建消息 - 简单直接的实现
	messages := buildMessages(req)

	// 设置选项 - 统一默认值处理
	options := buildOptions(req)

	// 执行调用
	response, err := client.GenerateContent(ctx, messages, options...)

	// 构建统一响应
	return buildResponse(response, err, startTime, req.RequestID, req.Service), err
}

// ProcessStreamingRequest - 流式请求处理
func (s *UniversalLLMService) ProcessStreamingRequest(
	ctx context.Context,
	req UniversalLLMRequest,
	handler StreamingHandler,
) error {
	if req.RequestID == "" {
		req.RequestID = fmt.Sprintf("stream_%d", time.Now().UnixNano())
	}

	globals.Info(fmt.Sprintf("[UniversalLLM] Processing streaming request %s from service %s", req.RequestID, req.Service))

	client := GetClient()
	if client == nil {
		return fmt.Errorf("LLM client not initialized")
	}

	messages := buildMessages(req)
	options := append(buildOptions(req),
		llms.WithStreamingFunc(func(ctx context.Context, chunk []byte) error {
			if len(chunk) > 0 {
				return handler(string(chunk))
			}
			return nil
		}),
	)

	_, err := client.GenerateContent(ctx, messages, options...)
	return err
}

// buildMessages 构建消息 - 消除所有特殊情况
func buildMessages(req UniversalLLMRequest) []llms.MessageContent {
	var messages []llms.MessageContent

	// 系统提示
	if req.SystemPrompt != "" {
		messages = append(messages, llms.MessageContent{
			Role:  llms.ChatMessageTypeSystem,
			Parts: []llms.ContentPart{llms.TextContent{Text: req.SystemPrompt}},
		})
	}

	// 上下文（如果有）
	userContent := req.UserMessage
	if req.Context != "" {
		userContent = fmt.Sprintf("Context:\n%s\n\nRequest:\n%s", req.Context, req.UserMessage)
	}

	// 用户消息
	messages = append(messages, llms.MessageContent{
		Role:  llms.ChatMessageTypeHuman,
		Parts: []llms.ContentPart{llms.TextContent{Text: userContent}},
	})

	return messages
}

// buildOptions 构建调用选项 - 统一默认值
func buildOptions(req UniversalLLMRequest) []llms.CallOption {
	var options []llms.CallOption

	// 温度设置 - 消除各服务不同的默认值
	temp := req.Temperature
	if temp == 0 {
		temp = DefaultTemperature
	}
	options = append(options, llms.WithTemperature(temp))

	// Token限制 - 统一默认值
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = DefaultMaxTokens
	}
	options = append(options, llms.WithMaxTokens(maxTokens))

	// JSON Mode - 确保结构化输出
	if req.JSONMode {
		options = append(options, llms.WithJSONMode())
	}

	return options
}

// buildResponse 构建响应 - 统一响应格式
func buildResponse(response *llms.ContentResponse, err error, startTime time.Time, requestID, service string) *UniversalLLMResponse {
	resp := &UniversalLLMResponse{
		ResponseTime: time.Since(startTime),
		RequestID:    requestID,
		Service:      service,
		Model:        GetModel(),
	}

	if err != nil {
		resp.Success = false
		resp.Error = err.Error()
		globals.Error(fmt.Sprintf("[UniversalLLM] Request %s failed: %s", requestID, err.Error()))
		return resp
	}

	if len(response.Choices) > 0 && len(response.Choices[0].Content) > 0 {
		resp.Success = true
		resp.Content = response.Choices[0].Content
		globals.Info(fmt.Sprintf("[UniversalLLM] Request %s completed successfully in %v",
			requestID, resp.ResponseTime))
	} else {
		resp.Success = false
		resp.Error = "empty response from LLM"
		globals.Warn(fmt.Sprintf("[UniversalLLM] Request %s got empty response", requestID))
	}

	return resp
}

// buildErrorResponse 构建错误响应
func buildErrorResponse(requestID, service, errorMsg string, startTime time.Time) *UniversalLLMResponse {
	return &UniversalLLMResponse{
		Success:      false,
		Error:        errorMsg,
		ResponseTime: time.Since(startTime),
		RequestID:    requestID,
		Service:      service,
		Model:        GetModel(),
	}
}
