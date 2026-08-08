package provider

import (
	"context"
	"testing"
)

type fakeImageProvider struct {
	lastRequest *GenerationRequest
	result      *GenerationResult
}

func (f *fakeImageProvider) Generate(_ context.Context, req *GenerationRequest) (*GenerationResult, error) {
	f.lastRequest = req
	return f.result, nil
}

func (f *fakeImageProvider) Name() string    { return "fake-image" }
func (f *fakeImageProvider) Type() string    { return "fake" }
func (f *fakeImageProvider) IsEnabled() bool { return true }

func TestTransitionalBackgroundRemoverProviderDelegatesToImageProvider(t *testing.T) {
	inner := &fakeImageProvider{
		result: &GenerationResult{
			Success:       true,
			ImageURLs:     []string{"https://example.com/result.png"},
			ProviderJobID: "job-123",
			Duration:      1200,
			Provider:      "fake-image",
		},
	}

	provider := NewTransitionalBackgroundRemoverProvider(inner)
	if provider == nil {
		t.Fatal("expected transitional remover provider")
	}

	result, err := provider.RemoveBackground(context.Background(), &BackgroundRemovalRequest{
		Model:          "nanobanana-2",
		Prompt:         "remove bg",
		NegativePrompt: "no extra object",
		Width:          1024,
		Height:         768,
		TaskID:         "task-1",
	})
	if err != nil {
		t.Fatalf("RemoveBackground returned error: %v", err)
	}
	if inner.lastRequest == nil {
		t.Fatal("expected inner image provider to receive request")
	}
	if inner.lastRequest.Model != "nanobanana-2" || inner.lastRequest.Prompt != "remove bg" {
		t.Fatalf("unexpected mapped request: %#v", inner.lastRequest)
	}
	if inner.lastRequest.NegativePrompt != "no extra object" {
		t.Fatalf("unexpected negative prompt: %q", inner.lastRequest.NegativePrompt)
	}
	if inner.lastRequest.Width != 1024 || inner.lastRequest.Height != 768 {
		t.Fatalf("unexpected mapped dimensions: %dx%d", inner.lastRequest.Width, inner.lastRequest.Height)
	}
	if result == nil || !result.Success {
		t.Fatalf("expected successful result, got %#v", result)
	}
	if result.ProviderJobID != "job-123" {
		t.Fatalf("unexpected provider job id: %q", result.ProviderJobID)
	}
	if provider.Type() != transitionalBackgroundRemoverType {
		t.Fatalf("unexpected provider type: %q", provider.Type())
	}
}
