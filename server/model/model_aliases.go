package model

import "strings"

func NormalizeModelID(raw string) string {
	normalized := strings.TrimSpace(strings.ToLower(raw))
	if normalized == "" {
		return ""
	}

	switch normalized {
	case NANO_BANANA, "nano banana", "nano-banana":
		return NANO_BANANA
	case NANO_BANANA_2, "nano banana 2", "nano-banana-2", "nanobanana2", "nanobanana_2", "gemini-3.1-flash-image", "gemini-3.1-flash-image-preview":
		return NANO_BANANA_2
	case NANO_BANANA_PRO, "nano banana pro", "nano-banana-pro", "nanobananapro", "nanobanana_pro", "gemini-3-pro-image", "gemini-3-pro-image-preview":
		return NANO_BANANA_PRO
	case GPT_IMAGE_2, "gpt image 2", "gptimage2", "gpt_image_2", "gpt-image2":
		return GPT_IMAGE_2
	case VEO_31, "veo-3.1-generate-001":
		return VEO_31
	case VEO_31_FAST, "veo-3.1-fast-generate-001":
		return VEO_31_FAST
	case SEEDANCE, "doubao-seedance-1-5-pro-251215":
		return SEEDANCE
	case SEEDANCE_2, "doubao-seedance-2-0-260128":
		return SEEDANCE_2
	case SEEDANCE_2_FAST, "doubao-seedance-2-0-fast-260128":
		return SEEDANCE_2_FAST
	case MINIMAX_VIDEO, "minimax-hailuo-02":
		return MINIMAX_VIDEO
	default:
		return normalized
	}
}

func NormalizeProviderType(raw string) string {
	normalized := strings.TrimSpace(strings.ToLower(raw))
	if normalized == "" {
		return ""
	}

	switch normalized {
	case ProviderTypeGemini:
		return ProviderTypeGemini
	case ProviderTypeOpenAI:
		return ProviderTypeOpenAI
	case ProviderTypeVertex:
		return ProviderTypeVertex
	case ProviderTypeKling:
		return ProviderTypeKling
	case ProviderTypeSora:
		return ProviderTypeSora
	case ProviderTypeSeedance:
		return ProviderTypeSeedance
	case ProviderTypeMinimax:
		return ProviderTypeMinimax
	default:
		return normalized
	}
}

func ProviderTypesEqual(a, b string) bool {
	return NormalizeProviderType(a) == NormalizeProviderType(b)
}

func ModelConfigKeys(modelID string) []string {
	normalized := NormalizeModelID(modelID)
	if normalized == "" {
		return nil
	}
	return []string{normalized}
}
