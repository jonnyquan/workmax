package tools

import (
	"context"
	"encoding/json"
	"strings"

	"server/model"
	"server/service/globalmodel"

	"gorm.io/gorm"
)

// BuiltinGlobalModelInputs converts the current in-code model registries into
// global model catalog rows. This is intentionally a sync source, not the
// runtime routing source; provider selection remains in w_generator_provider.
func BuiltinGlobalModelInputs() []globalmodel.UpsertInput {
	videoOrder := VideoModelOrder()
	out := make([]globalmodel.UpsertInput, 0, len(videoOrder)+len(imageModelAvailabilityRegistry))
	sortBase := (len(videoOrder) + len(imageModelAvailabilityRegistry)) * 10

	for index, modelID := range videoOrder {
		capability, ok := videoModelCapabilityRegistry[modelID]
		if !ok {
			continue
		}
		capability.ProviderType = providerTypeByModel(modelID)
		out = append(out, globalmodel.UpsertInput{
			ModelID:       modelID,
			MediaType:     model.MediaTypeVideo,
			ProviderType:  capability.ProviderType,
			DisplayName:   displayNameForModel(modelID),
			PricingStatus: string(capability.PricingStatus),
			SortOrder:     sortBase - index,
			Capabilities:  jsonMapFromValue(capability),
			Metadata: model.JSONMap{
				"source": "builtin-video-model-registry",
			},
		})
	}

	offset := len(videoOrder)
	for index, modelID := range imageModelAvailabilityRegistry {
		out = append(out, globalmodel.UpsertInput{
			ModelID:      modelID,
			MediaType:    model.MediaTypeImage,
			ProviderType: providerTypeByModel(modelID),
			DisplayName:  displayNameForModel(modelID),
			SortOrder:    sortBase - offset - index,
			Capabilities: model.JSONMap{
				"model":        modelID,
				"providerType": providerTypeByModel(modelID),
			},
			Metadata: model.JSONMap{
				"source": "builtin-image-model-registry",
			},
		})
	}
	return out
}

func SyncBuiltinGlobalModels(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return gorm.ErrInvalidDB
	}
	repo := globalmodel.NewRepository(db.WithContext(ctx))
	for _, input := range BuiltinGlobalModelInputs() {
		if _, err := repo.Upsert(input); err != nil {
			return err
		}
	}
	return nil
}

func jsonMapFromValue(value interface{}) model.JSONMap {
	bytes, err := json.Marshal(value)
	if err != nil {
		return model.JSONMap{}
	}
	var out model.JSONMap
	if err := json.Unmarshal(bytes, &out); err != nil {
		return model.JSONMap{}
	}
	if out == nil {
		return model.JSONMap{}
	}
	return out
}

func displayNameForModel(modelID string) string {
	switch model.NormalizeModelID(modelID) {
	case model.NANO_BANANA_PRO:
		return "Nano Banana Pro"
	case model.NANO_BANANA_2:
		return "Nano Banana 2"
	case model.NANO_BANANA:
		return "Nano Banana"
	case model.GPT_IMAGE_2:
		return "GPT Image 2"
	case model.VEO_31:
		return "Veo 3.1"
	case model.VEO_31_FAST:
		return "Veo 3.1 Fast"
	case model.KLING_2_6:
		return "Kling 2.6"
	case model.SORA_2:
		return "Sora 2"
	case model.SEEDANCE:
		return "Seedance"
	case model.SEEDANCE_2:
		return "Seedance 2"
	case model.SEEDANCE_2_FAST:
		return "Seedance 2 Fast"
	case model.MINIMAX_VIDEO:
		return "MiniMax Video 01"
	default:
		return titleizeModelID(modelID)
	}
}

func titleizeModelID(modelID string) string {
	parts := strings.FieldsFunc(strings.TrimSpace(modelID), func(r rune) bool {
		return r == '-' || r == '_' || r == '.'
	})
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, " ")
}
