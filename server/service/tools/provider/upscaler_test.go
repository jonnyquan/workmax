package provider

import (
	"context"
	"testing"
)

type fakeUpscalerImageProvider struct {
	lastRequest *GenerationRequest
	result      *GenerationResult
}

func (f *fakeUpscalerImageProvider) Generate(_ context.Context, req *GenerationRequest) (*GenerationResult, error) {
	f.lastRequest = req
	return f.result, nil
}

func (f *fakeUpscalerImageProvider) Name() string    { return "fake-image" }
func (f *fakeUpscalerImageProvider) Type() string    { return "fake" }
func (f *fakeUpscalerImageProvider) IsEnabled() bool { return true }

func TestTransitionalUpscalerProviderDelegatesToImageProvider(t *testing.T) {
	inner := &fakeUpscalerImageProvider{
		result: &GenerationResult{
			Success:       true,
			ImageURLs:     []string{"https://example.com/upscaled.png"},
			ProviderJobID: "job-upscale-1",
			Duration:      900,
			Provider:      "fake-image",
		},
	}

	upscalerProvider := NewTransitionalUpscalerProvider(inner)
	if upscalerProvider == nil {
		t.Fatal("expected transitional upscaler provider")
	}

	result, err := upscalerProvider.Upscale(context.Background(), &UpscaleRequest{
		Model:       "nanobanana-2",
		Prompt:      "upscale image",
		Width:       1024,
		Height:      1536,
		Scale:       4,
		TaskID:      "task-1",
		EnhanceFace: true,
	})
	if err != nil {
		t.Fatalf("Upscale returned error: %v", err)
	}
	if inner.lastRequest == nil {
		t.Fatal("expected inner image provider to receive request")
	}
	if inner.lastRequest.Model != "nanobanana-2" || inner.lastRequest.Prompt != "upscale image" {
		t.Fatalf("unexpected mapped request: %#v", inner.lastRequest)
	}
	if inner.lastRequest.Width != 1024 || inner.lastRequest.Height != 1536 {
		t.Fatalf("unexpected mapped dimensions: %dx%d", inner.lastRequest.Width, inner.lastRequest.Height)
	}
	if result == nil || !result.Success {
		t.Fatalf("expected successful result, got %#v", result)
	}
	if result.ProviderJobID != "job-upscale-1" {
		t.Fatalf("unexpected provider job id: %q", result.ProviderJobID)
	}
	if upscalerProvider.Type() != transitionalUpscalerType {
		t.Fatalf("unexpected provider type: %q", upscalerProvider.Type())
	}
}
