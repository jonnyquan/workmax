package tools

// AvatarScenePreset captures the server-owned identity of an Avatar Studio scene preset.
// Labels and descriptions remain client-side (i18n) keyed by Key.
type AvatarScenePreset struct {
	Key            string `json:"key"`
	ID             string `json:"id"`
	Prompt         string `json:"prompt"`
	NegativePrompt string `json:"negativePrompt"`
	AspectRatio    string `json:"aspectRatio"`
}

var avatarScenePresets = []AvatarScenePreset{
	{
		Key:            "professionalHeadshot",
		ID:             "professional-headshot",
		Prompt:         "Professional studio portrait, natural skin texture, soft key light, clean background, business-casual outfit, centered composition, high-detail face, profile picture ready",
		NegativePrompt: "blurry, deformed face, extra limbs, watermark, text overlay, low quality",
		AspectRatio:    "1:1",
	},
	{
		Key:            "animeProfile",
		ID:             "anime-profile",
		Prompt:         "Anime portrait avatar, expressive eyes, clean line art, vibrant but balanced colors, high-detail face, soft rim light, polished illustration style",
		NegativePrompt: "photorealistic, blurry, distorted face, low detail",
		AspectRatio:    "1:1",
	},
	{
		Key:            "gamingHero",
		ID:             "gaming-hero",
		Prompt:         "Epic gaming character portrait, dramatic lighting, confident expression, cyber-fantasy atmosphere, sharp details, high-contrast background glow, avatar icon quality",
		NegativePrompt: "low contrast, blurry, flat lighting, deformed face",
		AspectRatio:    "1:1",
	},
	{
		Key:            "cinematic",
		ID:             "cinematic",
		Prompt:         "Cinematic portrait, filmic color grading, dramatic key light and shadows, premium lens look, natural facial proportions, storytelling mood, ultra-detailed close-up",
		NegativePrompt: "overexposed, noisy, deformed face, cartoon style",
		AspectRatio:    "1:1",
	},
	{
		Key:            "cleanId",
		ID:             "passport-clean",
		Prompt:         "Clean frontal headshot, neutral expression, plain light background, balanced light, realistic details, no accessories, passport-style portrait composition",
		NegativePrompt: "dramatic shadows, strong color cast, blur, artifacts",
		AspectRatio:    "1:1",
	},
}

// GetAvatarScenePresets returns a copy of the canonical preset list so callers
// cannot mutate the shared registry.
func GetAvatarScenePresets() []AvatarScenePreset {
	out := make([]AvatarScenePreset, len(avatarScenePresets))
	copy(out, avatarScenePresets)
	return out
}

// IsKnownAvatarScenePresetKey reports whether key identifies a canonical
// Avatar Studio preset. The empty string is treated as "no preset selected"
// and therefore valid — callers decide whether an empty key is acceptable
// for their flow.
func IsKnownAvatarScenePresetKey(key string) bool {
	if key == "" {
		return true
	}
	for _, preset := range avatarScenePresets {
		if preset.Key == key {
			return true
		}
	}
	return false
}
