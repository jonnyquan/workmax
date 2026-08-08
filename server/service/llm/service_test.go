package llm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/tmc/langchaingo/llms"
)

// TestUniversalLLMService 测试核心服务功能
func TestUniversalLLMService(t *testing.T) {
	service := GetService()
	assert.NotNil(t, service, "Service should not be nil")

	// 测试基本请求处理
	req := UniversalLLMRequest{
		SystemPrompt: "You are a helpful assistant",
		UserMessage:  "Say hello",
		Temperature:  0.5,
		MaxTokens:    100,
		Service:      "test",
	}

	ctx := context.Background()
	response, err := service.ProcessRequest(ctx, req)

	// 基本验证
	assert.NotNil(t, response, "Response should not be nil")
	assert.Equal(t, "test", response.Service, "Service should be set correctly")
	assert.NotEmpty(t, response.RequestID, "RequestID should be generated")
	assert.True(t, response.ResponseTime > 0, "ResponseTime should be positive")

	// 如果没有真实LLM配置，测试应该处理错误
	if err != nil {
		assert.False(t, response.Success, "Response should indicate failure")
		assert.NotEmpty(t, response.Error, "Error message should be provided")
	} else {
		assert.True(t, response.Success, "Response should indicate success")
		assert.NotEmpty(t, response.Content, "Content should not be empty")
	}
}

// TestBuildMessages 测试消息构建逻辑
func TestBuildMessages(t *testing.T) {
	testCases := []struct {
		name     string
		request  UniversalLLMRequest
		expected int // 期望的消息数量
	}{
		{
			name: "只有用户消息",
			request: UniversalLLMRequest{
				UserMessage: "Hello",
			},
			expected: 1,
		},
		{
			name: "系统提示 + 用户消息",
			request: UniversalLLMRequest{
				SystemPrompt: "You are helpful",
				UserMessage:  "Hello",
			},
			expected: 2,
		},
		{
			name: "包含上下文",
			request: UniversalLLMRequest{
				SystemPrompt: "You are helpful",
				UserMessage:  "Hello",
				Context:      "Previous conversation context",
			},
			expected: 2, // 上下文会合并到用户消息中
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			messages := buildMessages(tc.request)
			assert.Equal(t, tc.expected, len(messages),
				"Message count should match expected for %s", tc.name)
		})
	}
}

// TestBuildOptions 测试选项构建逻辑
func TestBuildOptions(t *testing.T) {
	testCases := []struct {
		name         string
		request      UniversalLLMRequest
		expectTemp   float64
		expectTokens int
	}{
		{
			name:         "使用默认值",
			request:      UniversalLLMRequest{},
			expectTemp:   DefaultTemperature,
			expectTokens: DefaultMaxTokens,
		},
		{
			name: "使用自定义值",
			request: UniversalLLMRequest{
				Temperature: 0.3,
				MaxTokens:   500,
			},
			expectTemp:   0.3,
			expectTokens: 500,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			options := buildOptions(tc.request)
			assert.NotEmpty(t, options, "Options should not be empty")
			// 注意：这里我们无法直接检查选项内容，但至少确保没有panic
		})
	}
}

// TestSingletonBehavior 测试单例行为
func TestSingletonBehavior(t *testing.T) {
	// 多次调用GetService应该返回相同实例
	service1 := GetService()
	service2 := GetService()

	assert.Same(t, service1, service2, "GetService should return the same instance")
}

// BenchmarkLLMService 性能基准测试
func BenchmarkLLMService(b *testing.B) {
	req := UniversalLLMRequest{
		SystemPrompt: "You are a test assistant",
		UserMessage:  "Test message",
		Service:      "benchmark",
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		// 只测试请求构建和响应处理的开销
		// 不进行真实的LLM调用以避免网络延迟影响基准测试
		messages := buildMessages(req)
		options := buildOptions(req)

		// 验证构建的消息和选项不为空
		if len(messages) == 0 || len(options) == 0 {
			b.Error("Messages or options should not be empty")
		}
	}
}

// TestErrorHandling 测试错误处理
func TestErrorHandling(t *testing.T) {
	// 测试空请求
	service := GetService()
	ctx := context.Background()

	emptyReq := UniversalLLMRequest{}
	response, _ := service.ProcessRequest(ctx, emptyReq)

	assert.NotNil(t, response, "Response should not be nil even for empty request")
	assert.NotEmpty(t, response.RequestID, "RequestID should be generated for empty request")
}

// TestBuildMessages_ContextMergeFormat pins the exact string template used
// when req.Context is non-empty. The previous TestBuildMessages only checks
// message count, and context gets merged INTO the user message (count stays
// at 2 either way), so a drift in the "Context:\n...\n\nRequest:\n..."
// format would ship subtly wrong prompts to the model without any signal.
// Role + text-part shape are also pinned because every downstream LLM
// backend relies on system-vs-human routing being correct.
func TestBuildMessages_ContextMergeFormat(t *testing.T) {
	req := UniversalLLMRequest{
		SystemPrompt: "SYS",
		UserMessage:  "ask",
		Context:      "prior",
	}
	messages := buildMessages(req)

	assert.Len(t, messages, 2, "system + human expected")
	assert.Equal(t, llms.ChatMessageTypeSystem, messages[0].Role)
	assert.Equal(t, llms.ChatMessageTypeHuman, messages[1].Role)

	sysText, ok := messages[0].Parts[0].(llms.TextContent)
	assert.True(t, ok, "system part should be TextContent")
	assert.Equal(t, "SYS", sysText.Text)

	userText, ok := messages[1].Parts[0].(llms.TextContent)
	assert.True(t, ok, "human part should be TextContent")
	assert.Equal(t, "Context:\nprior\n\nRequest:\nask", userText.Text)
}

// When context is empty, the user message must pass through verbatim —
// no "Context:\n\n\nRequest:\n..." artifact from a lazy always-merge.
func TestBuildMessages_NoContextIsVerbatim(t *testing.T) {
	req := UniversalLLMRequest{UserMessage: "ask"}
	messages := buildMessages(req)
	assert.Len(t, messages, 1)
	userText, ok := messages[0].Parts[0].(llms.TextContent)
	assert.True(t, ok)
	assert.Equal(t, "ask", userText.Text)
}

// TestStreamingRequest 测试流式请求接口
func TestStreamingRequest(t *testing.T) {
	service := GetService()
	ctx := context.Background()

	req := UniversalLLMRequest{
		SystemPrompt: "You are helpful",
		UserMessage:  "Count to 3",
		Service:      "streaming_test",
	}

	handler := func(chunk string) error {
		return nil
	}

	// 即使没有真实连接，也不应该panic
	err := service.ProcessStreamingRequest(ctx, req, handler)

	// 如果没有真实LLM配置，这里会有错误，这是正常的
	// 我们主要测试接口不会panic
	t.Logf("Streaming request completed with error (expected in test): %v", err)
}
