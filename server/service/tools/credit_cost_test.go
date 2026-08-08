package tools

import (
	"server/model"
	"testing"
)

func TestGetCreditCostByToolID_Upscaler(t *testing.T) {
	tests := []struct {
		name string
		in   CreditCostParams
		want int
	}{
		{
			name: "2x without enhance uses base cost",
			in: CreditCostParams{
				ToolID: model.TOOL_IMAGE_UPSCALER,
				Scale:  2,
			},
			want: 3,
		},
		{
			name: "4x uses higher cost",
			in: CreditCostParams{
				ToolID: model.TOOL_IMAGE_UPSCALER,
				Scale:  4,
			},
			want: 5,
		},
		{
			name: "enhance face uses higher cost",
			in: CreditCostParams{
				ToolID:      model.TOOL_IMAGE_UPSCALER,
				Scale:       2,
				EnhanceFace: true,
			},
			want: 5,
		},
		{
			name: "scale from params is respected",
			in: CreditCostParams{
				ToolID: "upscaler",
				Params: map[string]interface{}{
					"scale": 4,
				},
			},
			want: 5,
		},
		{
			name: "enhance face from params is respected",
			in: CreditCostParams{
				ToolID: "upscaler",
				Params: map[string]interface{}{
					"enhanceFace": true,
				},
			},
			want: 5,
		},
		{
			// Legacy: pre-rename DB rows only stored the enhanceQuality alias.
			// Pricing must keep matching for those records.
			name: "legacy enhance quality alias from params still respected",
			in: CreditCostParams{
				ToolID: "upscaler",
				Params: map[string]interface{}{
					"enhanceQuality": true,
				},
			},
			want: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetCreditCostByToolID(tt.in)
			if got != tt.want {
				t.Fatalf("GetCreditCostByToolID() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestGetCreditCostByToolID_Vectorizer(t *testing.T) {
	tests := []struct {
		name string
		in   CreditCostParams
		want int
	}{
		{
			name: "medium detail uses base cost",
			in: CreditCostParams{
				ToolID: model.TOOL_IMAGE_VECTORIZER,
				Model:  "vectorizer",
				Params: map[string]interface{}{
					"detailLevel": "medium",
				},
			},
			want: 3,
		},
		{
			name: "high detail uses higher cost",
			in: CreditCostParams{
				ToolID: model.TOOL_IMAGE_VECTORIZER,
				Model:  "vectorizer",
				Params: map[string]interface{}{
					"detailLevel": "high",
				},
			},
			want: 4,
		},
		{
			name: "detail level from nested params is respected",
			in: CreditCostParams{
				ToolID: model.TOOL_IMAGE_VECTORIZER,
				Model:  "vectorizer",
				Params: map[string]interface{}{
					"params": map[string]interface{}{
						"detailLevel": "high",
					},
				},
			},
			want: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetCreditCostByToolID(tt.in)
			if got != tt.want {
				t.Fatalf("GetCreditCostByToolID() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestGetCreditCostByToolID_Remover(t *testing.T) {
	tests := []struct {
		name string
		in   CreditCostParams
		want int
	}{
		{
			name: "direct remover cost stays fixed",
			in: CreditCostParams{
				ToolID: model.TOOL_BACKGROUND_REMOVER,
				Model:  model.NANO_BANANA_2,
			},
			want: 4,
		},
		{
			name: "nested params do not change remover cost",
			in: CreditCostParams{
				ToolID: model.TOOL_BACKGROUND_REMOVER,
				Model:  model.NANO_BANANA_2,
				Params: map[string]interface{}{
					"params": map[string]interface{}{
						"aspectRatio": "16:9",
						"prompt":      "remove background",
					},
				},
			},
			want: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetCreditCostByToolID(tt.in)
			if got != tt.want {
				t.Fatalf("GetCreditCostByToolID() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestGetCreditCostByToolID_LoraStudio(t *testing.T) {
	got := GetCreditCostByToolID(CreditCostParams{
		ToolID: model.TOOL_LORA_STUDIO + ":gender-swap",
		Model:  model.NANO_BANANA_2,
	})

	if got != 4 {
		t.Fatalf("GetCreditCostByToolID() for lora studio = %d, want %d", got, 4)
	}
}

func TestGetCreditCostByToolID_AvatarStudio(t *testing.T) {
	tests := []struct {
		name string
		in   CreditCostParams
		want int
	}{
		{
			name: "single image uses 4 credits",
			in: CreditCostParams{
				ToolID:         model.TOOL_AVATAR_STUDIO,
				NumberOfImages: 1,
			},
			want: 4,
		},
		{
			name: "multiple images scale linearly",
			in: CreditCostParams{
				ToolID:         model.TOOL_AVATAR_STUDIO,
				NumberOfImages: 4,
			},
			want: 16,
		},
		{
			name: "number of images can come from params",
			in: CreditCostParams{
				ToolID: "avatars",
				Params: map[string]interface{}{
					"numberOfImages": 2,
				},
			},
			want: 8,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetCreditCostByToolID(tt.in)
			if got != tt.want {
				t.Fatalf("GetCreditCostByToolID() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestGetCreditCostByToolID_VideoReferenceImagesUseImagePricing(t *testing.T) {
	base := CreditCostParams{
		ToolID: model.TOOL_VIDEO_GENERATOR,
		Model:  model.KLING_2_6,
		Params: map[string]interface{}{
			"model":       model.KLING_2_6,
			"duration":    5,
			"motionMode":  "std",
			"aspectRatio": "16:9",
		},
	}
	textCost := GetCreditCostByToolID(base)

	withReference := base
	withReference.Params = map[string]interface{}{
		"model":       model.KLING_2_6,
		"duration":    5,
		"motionMode":  "std",
		"aspectRatio": "16:9",
		"referenceImages": []struct {
			ID     string
			URL    string
			Weight float32
		}{
			{ID: "character", URL: "/uploads/references/uid/42/ref.png", Weight: 1},
		},
	}
	imageCost := GetCreditCostByToolID(withReference)

	if imageCost <= textCost {
		t.Fatalf("video cost with generic referenceImages = %d, want greater than text cost %d", imageCost, textCost)
	}
}

func TestGetCreditCostByToolID_AvatarStudioQuoteScalesByImageCount(t *testing.T) {
	tests := []struct {
		name string
		in   CreditCostParams
		want int
	}{
		{
			name: "single image keeps unit cost",
			in: CreditCostParams{
				ToolID:         model.TOOL_AVATAR_STUDIO,
				Model:          model.NANO_BANANA_2,
				NumberOfImages: 1,
			},
			want: 4,
		},
		{
			name: "multiple images scale linearly",
			in: CreditCostParams{
				ToolID:         model.TOOL_AVATAR_STUDIO,
				Model:          model.NANO_BANANA_2,
				NumberOfImages: 4,
			},
			want: 16,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetCreditCostByToolID(tt.in)
			if got != tt.want {
				t.Fatalf("GetCreditCostByToolID() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestGetCreditCostByToolID_ImageGeneratorNanoBanana2(t *testing.T) {
	tests := []struct {
		name string
		in   CreditCostParams
		want int
	}{
		{
			name: "single image uses 4 credits",
			in: CreditCostParams{
				ToolID: model.TOOL_IMAGE_GENERATOR,
				Model:  model.NANO_BANANA_2,
			},
			want: 4,
		},
		{
			name: "multiple images scale linearly",
			in: CreditCostParams{
				ToolID:         model.TOOL_IMAGE_GENERATOR,
				Model:          model.NANO_BANANA_2,
				NumberOfImages: 4,
			},
			want: 16,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetCreditCostByToolID(tt.in)
			if got != tt.want {
				t.Fatalf("GetCreditCostByToolID() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestGetCreditCostByToolID_ImageGeneratorNanoBananaPro(t *testing.T) {
	tests := []struct {
		name string
		in   CreditCostParams
		want int
	}{
		{
			name: "2k uses base pro cost",
			in: CreditCostParams{
				ToolID:     model.TOOL_IMAGE_GENERATOR,
				Model:      model.NANO_BANANA_PRO,
				Resolution: "2k",
			},
			want: 8,
		},
		{
			name: "4k doubles pro cost",
			in: CreditCostParams{
				ToolID:     model.TOOL_IMAGE_GENERATOR,
				Model:      model.NANO_BANANA_PRO,
				Resolution: "4k",
			},
			want: 16,
		},
		{
			name: "upscale uses dedicated pro cost",
			in: CreditCostParams{
				ToolID:  model.TOOL_IMAGE_GENERATOR,
				Model:   model.NANO_BANANA_PRO,
				Upscale: true,
			},
			want: 12,
		},
		{
			name: "4k with multiple images scales linearly",
			in: CreditCostParams{
				ToolID:         model.TOOL_IMAGE_GENERATOR,
				Model:          model.NANO_BANANA_PRO,
				Resolution:     "4k",
				NumberOfImages: 4,
			},
			want: 64,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetCreditCostByToolID(tt.in)
			if got != tt.want {
				t.Fatalf("GetCreditCostByToolID() = %d, want %d", got, tt.want)
			}
		})
	}
}
