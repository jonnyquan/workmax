//go:build integration

package provider

import (
	"context"
	"os"
	"server/model"
	"strings"
	"testing"
	"time"
)

func TestNanoBananaGeminiImageLive(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("WORKMAX_GEMINI_IMAGE_TEST_API_KEY"))
	if apiKey == "" {
		t.Skip("set WORKMAX_GEMINI_IMAGE_TEST_API_KEY to run live Gemini image integration test")
	}

	modelCode := strings.TrimSpace(os.Getenv("WORKMAX_GEMINI_IMAGE_TEST_MODEL"))
	requestModel := model.NANO_BANANA_2
	if modelCode == "" {
		modelCode = "gemini-3.1-flash-image-preview"
	} else if strings.Contains(modelCode, "pro-image") {
		requestModel = model.NANO_BANANA_PRO
	}

	p := NewNanoBananaProvider(&ProviderConfig{
		Name:      "gemini-image-live-test",
		Type:      model.ProviderTypeGemini,
		MediaType: model.MediaTypeImage,
		Enabled:   true,
		APIKey:    apiKey,
		Model:     modelCode,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	res, err := p.Generate(ctx, &GenerationRequest{
		Model:     requestModel,
		Prompt:    "Generate one simple flat icon of a blue paper plane on a white background. Return only the image.",
		Width:     1024,
		Height:    1024,
		NumImages: 1,
	})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if res == nil || !res.Success {
		if res == nil {
			t.Fatal("Generate returned nil result")
		}
		t.Fatalf("Generate failed: %s", res.Error)
	}
	if len(res.ImageData) == 0 {
		t.Fatalf("expected image bytes, got none; provider=%s duration=%dms", res.Provider, res.Duration)
	}
	if len(res.ImageData[0]) < 1024 {
		t.Fatalf("image payload is unexpectedly small: %d bytes", len(res.ImageData[0]))
	}
}

func TestVertexGeminiImageLive(t *testing.T) {
	projectID := strings.TrimSpace(os.Getenv("WORKMAX_VERTEX_GEMINI_IMAGE_TEST_PROJECT_ID"))
	if projectID == "" {
		t.Skip("set WORKMAX_VERTEX_GEMINI_IMAGE_TEST_PROJECT_ID to run live Vertex Gemini image integration test")
	}

	// Vertex AI supports both API key and OAuth/ADC for Gemini generateContent.
	// A plain API key uses /v1/publishers/google/models/{model}:generateContent?key=...
	// while OAuth/ADC uses the project/location publisher endpoint.
	bearerToken := strings.TrimSpace(os.Getenv("WORKMAX_VERTEX_GEMINI_IMAGE_TEST_API_KEY"))
	credentialsJSON := strings.TrimSpace(os.Getenv("WORKMAX_VERTEX_GEMINI_IMAGE_TEST_CREDENTIALS_JSON"))

	modelCode := strings.TrimSpace(os.Getenv("WORKMAX_VERTEX_GEMINI_IMAGE_TEST_MODEL"))
	requestModel := model.NANO_BANANA_2
	if modelCode == "" {
		modelCode = "gemini-3.1-flash-image-preview"
	} else if strings.Contains(modelCode, "pro-image") {
		requestModel = model.NANO_BANANA_PRO
	}
	location := strings.TrimSpace(os.Getenv("WORKMAX_VERTEX_GEMINI_IMAGE_TEST_LOCATION"))
	if location == "" {
		location = "global"
	}

	p := NewVertexGeminiImageProvider(&ProviderConfig{
		Name:      "vertex-gemini-image-live-test",
		Type:      model.ProviderTypeVertex,
		MediaType: model.MediaTypeImage,
		Enabled:   true,
		APIKey:    bearerToken,
		Model:     modelCode,
		ExtraConfig: &model.GeneratorProviderExtraConfig{
			VertexProjectID:       projectID,
			VertexLocation:        location,
			VertexCredentialsJSON: credentialsJSON,
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	res, err := p.Generate(ctx, &GenerationRequest{
		Model:     requestModel,
		Prompt:    "Generate one simple flat icon of a blue paper plane on a white background. Return only the image.",
		Width:     1024,
		Height:    1024,
		NumImages: 1,
	})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if res == nil || !res.Success {
		if res == nil {
			t.Fatal("Generate returned nil result")
		}
		t.Fatalf("Generate failed: %s", res.Error)
	}
	if len(res.ImageData) == 0 {
		t.Fatalf("expected image bytes, got none; provider=%s duration=%dms", res.Provider, res.Duration)
	}
	if len(res.ImageData[0]) < 1024 {
		t.Fatalf("image payload is unexpectedly small: %d bytes", len(res.ImageData[0]))
	}
}
