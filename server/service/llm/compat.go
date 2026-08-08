package llm

import (
	"context"
)

// LLMResponse is a minimal compatibility wrapper used by legacy callers
type LLMResponse struct {
	Response string
}

// LLMService is a minimal compatibility shim that forwards to the unified service
type LLMService struct{}

// NewLLMService returns a new compatibility service instance
func NewLLMService() *LLMService { return &LLMService{} }

// CallUniversalLLM adapts legacy signature to the unified ProcessRequest API.
// The model parameter is ignored; the active model is configured globally via GetClient().
func (s *LLMService) CallUniversalLLM(ctx context.Context, prompt string, model string, stream bool) (*LLMResponse, error) {
	core := GetService()
	resp, err := core.ProcessRequest(ctx, UniversalLLMRequest{
		SystemPrompt: "",
		UserMessage:  prompt,
		// Let defaults apply; callers historically didn't pass fine-grained options here
		Service: "compat_llm_service",
	})
	if err != nil {
		return nil, err
	}
	return &LLMResponse{Response: resp.Content}, nil
}
