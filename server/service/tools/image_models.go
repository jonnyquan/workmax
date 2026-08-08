package tools

import (
	"server/model"
	"server/service/tools/provider"
	"strings"
)

type ImageModelAvailability struct {
	Model             string `json:"model"`
	ProviderType      string `json:"providerType,omitempty"`
	ProviderAvailable bool   `json:"providerAvailable"`
}

var imageModelAvailabilityRegistry = []string{
	model.NANO_BANANA_PRO,
	model.NANO_BANANA_2,
	model.NANO_BANANA,
	model.GPT_IMAGE_2,
	"gpt-image-1.5",
	"flux-2-pro",
	"flux-2-max",
	"seedream-5",
	"seedream-4.5",
}

func IsImageModelProviderAvailable(modelID string) bool {
	summaries := provider.ListProviderSummariesByMediaType(model.MediaTypeImage)
	for _, summary := range summaries {
		if imageProviderSupportsModel(summary, modelID) {
			return true
		}
	}
	return false
}

func ListImageModelAvailability() []ImageModelAvailability {
	out := make([]ImageModelAvailability, 0, len(imageModelAvailabilityRegistry))
	for _, modelID := range imageModelAvailabilityRegistry {
		out = append(out, ImageModelAvailability{
			Model:             modelID,
			ProviderType:      providerTypeByModel(modelID),
			ProviderAvailable: IsImageModelProviderAvailable(modelID),
		})
	}
	return out
}

func imageProviderSupportsModel(summary provider.ProviderSummary, modelID string) bool {
	providerType := model.NormalizeProviderType(summary.Type)
	providerModel := strings.ToLower(strings.TrimSpace(summary.Model))
	normalizedModelID := model.NormalizeModelID(modelID)

	switch normalizedModelID {
	case model.NANO_BANANA_PRO, model.NANO_BANANA_2, model.NANO_BANANA:
		return providerType == model.ProviderTypeGemini ||
			providerType == model.ProviderTypeVertex ||
			model.NormalizeModelID(providerModel) == normalizedModelID
	case model.GPT_IMAGE_2:
		return providerType == model.ProviderTypeOpenAI ||
			strings.Contains(providerModel, "gpt-image-2") ||
			strings.Contains(providerModel, "gpt_image_2")
	case "gpt-image-1.5":
		return providerType == model.ProviderTypeOpenAI ||
			strings.Contains(providerModel, "gpt-image") ||
			strings.Contains(providerModel, "dall-e")
	case "flux-2-pro", "flux-2-max":
		return providerType == model.ProviderTypeReplicate ||
			strings.Contains(providerModel, "flux")
	case "seedream-5", "seedream-4.5":
		return strings.Contains(providerModel, "seedream")
	default:
		return false
	}
}
