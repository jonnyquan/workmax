package provider

import (
	"server/model"
	"testing"
)

func TestRowMatchesProvider_TypeAndMedia(t *testing.T) {
	cases := []struct {
		name         string
		row          model.GeneratorProvider
		providerType string
		mediaType    string
		want         bool
	}{
		{
			name:         "exact type + image",
			row:          model.GeneratorProvider{Name: "a", Type: model.ProviderTypeGemini, MediaType: "image", Enabled: true},
			providerType: model.ProviderTypeGemini,
			mediaType:    model.MediaTypeImage,
			want:         true,
		},
		{
			name:         "unknown provider type no longer aliases to gemini",
			row:          model.GeneratorProvider{Name: "a", Type: "unknown_provider", MediaType: "image", Enabled: true},
			providerType: model.ProviderTypeGemini,
			mediaType:    model.MediaTypeImage,
			want:         false,
		},
		{
			name:         "type mismatch",
			row:          model.GeneratorProvider{Name: "a", Type: model.ProviderTypeOpenAI, MediaType: "image", Enabled: true},
			providerType: model.ProviderTypeGemini,
			mediaType:    model.MediaTypeImage,
			want:         false,
		},
		{
			name:         "disabled rows are skipped",
			row:          model.GeneratorProvider{Name: "a", Type: model.ProviderTypeGemini, MediaType: "image", Enabled: false},
			providerType: model.ProviderTypeGemini,
			mediaType:    model.MediaTypeImage,
			want:         false,
		},
		{
			name:         "empty media defaults to image",
			row:          model.GeneratorProvider{Name: "a", Type: model.ProviderTypeGemini, MediaType: "", Enabled: true},
			providerType: model.ProviderTypeGemini,
			mediaType:    model.MediaTypeImage,
			want:         true,
		},
		{
			name:         "video media must match exactly",
			row:          model.GeneratorProvider{Name: "a", Type: model.ProviderTypeGemini, MediaType: "video", Enabled: true},
			providerType: "",
			mediaType:    model.MediaTypeVideo,
			want:         true,
		},
		{
			name:         "video request must NOT pick image provider",
			row:          model.GeneratorProvider{Name: "a", Type: model.ProviderTypeGemini, MediaType: "image", Enabled: true},
			providerType: "",
			mediaType:    model.MediaTypeVideo,
			want:         false,
		},
		{
			name:         "no filter accepts any enabled row",
			row:          model.GeneratorProvider{Name: "a", Type: model.ProviderTypeGemini, MediaType: "image", Enabled: true},
			providerType: "",
			mediaType:    "",
			want:         true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rowMatchesProvider(&tc.row, tc.providerType, tc.mediaType); got != tc.want {
				t.Fatalf("rowMatchesProvider got=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestPickProviderRow_PreferenceOrder(t *testing.T) {
	rows := []model.GeneratorProvider{
		{Name: "lo-priority", Type: model.ProviderTypeGemini, MediaType: "image", Enabled: true, Priority: 1},
		{Name: "default-one", Type: model.ProviderTypeGemini, MediaType: "image", Enabled: true, Priority: 5, IsDefault: true},
		{Name: "wrong-type", Type: model.ProviderTypeOpenAI, MediaType: "image", Enabled: true, Priority: 99},
	}

	t.Run("preferred name wins when type matches", func(t *testing.T) {
		got := pickProviderRow(rows, "lo-priority", model.ProviderTypeGemini, "image")
		if got == nil || got.Name != "lo-priority" {
			t.Fatalf("expected lo-priority, got %+v", got)
		}
	})

	t.Run("preferred name skipped when type does not match", func(t *testing.T) {
		got := pickProviderRow(rows, "wrong-type", model.ProviderTypeGemini, "image")
		if got == nil || got.Name == "wrong-type" {
			t.Fatalf("must not return wrong-type for gemini image request, got %+v", got)
		}
	})

	t.Run("falls back to is_default", func(t *testing.T) {
		got := pickProviderRow(rows, "", model.ProviderTypeGemini, "image")
		if got == nil || got.Name != "default-one" {
			t.Fatalf("expected default-one, got %+v", got)
		}
	})

	t.Run("returns nil when no row matches the requested type", func(t *testing.T) {
		onlyOpenAI := []model.GeneratorProvider{
			{Name: "gpt-image", Type: model.ProviderTypeOpenAI, MediaType: "image", Enabled: true, Priority: 99},
		}
		got := pickProviderRow(onlyOpenAI, "", model.ProviderTypeGemini, "image")
		if got != nil {
			t.Fatalf("expected nil (no gemini image provider), got %+v", got)
		}
	})
}

func TestPickProviderRowForModel_ModelEquivalentProviders(t *testing.T) {
	rows := []model.GeneratorProvider{
		// pickProviderRowForModel receives rows in DB priority-desc order.
		{
			Name:      "gemini-api-wrong-model",
			Type:      model.ProviderTypeGemini,
			MediaType: model.MediaTypeImage,
			Enabled:   true,
			Priority:  50,
			Model:     "gemini-3-pro-image-preview",
		},
		{
			Name:      "vertex-high",
			Type:      model.ProviderTypeVertex,
			MediaType: model.MediaTypeImage,
			Enabled:   true,
			Priority:  10,
			Model:     "gemini-3.1-flash-image-preview",
		},
		{
			Name:      "vertex-wrong-model",
			Type:      model.ProviderTypeVertex,
			MediaType: model.MediaTypeImage,
			Enabled:   true,
			Priority:  99,
			Model:     "gemini-3-pro-image-preview",
		},
		{
			Name:      "gemini-api",
			Type:      model.ProviderTypeGemini,
			MediaType: model.MediaTypeImage,
			Enabled:   true,
			Priority:  5,
			Model:     "gemini-3.1-flash-image-preview",
		},
	}

	got := pickProviderRowForModel(rows, "", model.ProviderTypeGemini, model.MediaTypeImage, model.NANO_BANANA_2)
	if got == nil || got.Name != "vertex-high" {
		t.Fatalf("expected highest priority compatible Vertex provider, got %+v", got)
	}

	got = pickProviderRowForModel(rows, "gemini-api", model.ProviderTypeGemini, model.MediaTypeImage, model.NANO_BANANA_2)
	if got == nil || got.Name != "gemini-api" {
		t.Fatalf("preferred provider should win when compatible, got %+v", got)
	}

	rows[3].IsDefault = true
	got = pickProviderRowForModel(rows, "", model.ProviderTypeGemini, model.MediaTypeImage, model.NANO_BANANA_2)
	if got == nil || got.Name != "gemini-api" {
		t.Fatalf("default compatible provider should win over priority fallback, got %+v", got)
	}
}

func TestPickProviderRowForModel_VertexVeoEquivalentProvider(t *testing.T) {
	rows := []model.GeneratorProvider{
		{Name: "vertex-fast", Type: model.ProviderTypeVertex, MediaType: model.MediaTypeVideo, Enabled: true, Priority: 99, Model: "veo-3.1-fast-generate-001"},
		{Name: "vertex-veo", Type: model.ProviderTypeVertex, MediaType: model.MediaTypeVideo, Enabled: true, Priority: 8, Model: "veo-3.1-generate-001"},
		{Name: "native-veo", Type: model.ProviderTypeGemini, MediaType: model.MediaTypeVideo, Enabled: true, Priority: 1, Model: "veo-3.1-generate-001"},
	}

	got := pickProviderRowForModel(rows, "", model.ProviderTypeGemini, model.MediaTypeVideo, model.VEO_31)
	if got == nil || got.Name != "vertex-veo" {
		t.Fatalf("expected matching Vertex Veo provider, got %+v", got)
	}
}
