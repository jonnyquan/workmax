package llm

import (
	"context"
	"fmt"
	"time"

	"github.com/tmc/langchaingo/llms"
)

type ImageAnalysisRequest struct {
	SystemPrompt string
	UserMessage  string
	MimeType     string
	ImageBytes   []byte

	Temperature float64
	MaxTokens   int
	JSONMode    bool

	RequestID string
	Service   string
}

func ProcessImageAnalysisRequest(ctx context.Context, req ImageAnalysisRequest) (*UniversalLLMResponse, error) {
	startTime := time.Now()
	if req.RequestID == "" {
		req.RequestID = fmt.Sprintf("image_analysis_%d", startTime.UnixNano())
	}

	client := GetImageAnalysisClient()
	if client == nil {
		return buildErrorResponse(req.RequestID, req.Service, "image analysis client not available", startTime),
			fmt.Errorf("image analysis client not initialized")
	}

	messages := []llms.MessageContent{
		{
			Role:  llms.ChatMessageTypeSystem,
			Parts: []llms.ContentPart{llms.TextPart(req.SystemPrompt)},
		},
		{
			Role: llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{
				llms.BinaryPart(req.MimeType, req.ImageBytes),
				llms.TextPart(req.UserMessage),
			},
		},
	}

	options := []llms.CallOption{
		llms.WithTemperature(defaultImageAnalysisTemperature(req.Temperature)),
		llms.WithMaxTokens(defaultImageAnalysisMaxTokens(req.MaxTokens)),
	}
	if req.JSONMode {
		options = append(options, llms.WithJSONMode())
	}

	response, err := client.GenerateContent(ctx, messages, options...)
	resp := buildResponse(response, err, startTime, req.RequestID, req.Service)
	resp.Model = GetImageAnalysisModel()
	return resp, err
}

func defaultImageAnalysisTemperature(value float64) float64 {
	if value == 0 {
		return 0.2
	}
	return value
}

func defaultImageAnalysisMaxTokens(value int) int {
	if value == 0 {
		return 1800
	}
	return value
}
