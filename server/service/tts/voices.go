package tts

// Provider-agnostic voice presets. Callers reference characters by
// preset ID (e.g. "female-warm") and the catalogue maps that into
// each provider's concrete voice name. Adding a provider means
// extending the inner map — no consumer change required.
//
// Preset IDs are short, descriptive, and language-tagged when the
// voice is locale-specific. "alloy-neutral" maps to OpenAI's alloy
// (English-first); "zh-female-warm" is reserved for Chinese-first
// voices when we wire a Chinese provider.

// VoicePreset is one row in the catalogue — a friendly ID, the
// per-provider voice name, plus a short label for UI rendering.
type VoicePreset struct {
	// ID is the provider-agnostic handle callers pass in. Store this
	// on w_global_character.voice_preset.
	ID string

	// Label is the human-readable name for the UI dropdown. "Warm
	// Female" / "Deep Male" etc.
	Label string

	// Language is BCP-47; "" means multi-lingual.
	Language string

	// Providers maps a provider name to the concrete voice ID that
	// provider accepts. Callers look up via LookupProviderVoice.
	Providers map[string]string
}

// VoicePresets is the app-wide catalogue. Keep the order stable —
// the UI shows them in declaration order. New entries go at the
// bottom so existing ordering doesn't shift.
var VoicePresets = []VoicePreset{
	{
		ID:       "neutral",
		Label:    "Neutral",
		Language: "",
		Providers: map[string]string{
			"openai": "alloy",
		},
	},
	{
		ID:       "female-warm",
		Label:    "Female · Warm",
		Language: "",
		Providers: map[string]string{
			"openai": "shimmer",
		},
	},
	{
		ID:       "female-clear",
		Label:    "Female · Clear",
		Language: "",
		Providers: map[string]string{
			"openai": "nova",
		},
	},
	{
		ID:       "male-deep",
		Label:    "Male · Deep",
		Language: "",
		Providers: map[string]string{
			"openai": "onyx",
		},
	},
	{
		ID:       "male-clear",
		Label:    "Male · Clear",
		Language: "",
		Providers: map[string]string{
			"openai": "echo",
		},
	},
	{
		ID:       "male-storyteller",
		Label:    "Male · Storyteller",
		Language: "",
		Providers: map[string]string{
			"openai": "fable",
		},
	},
}

// LookupProviderVoice resolves a preset ID + provider name to the
// concrete voice the provider's API expects. Returns ("", false)
// when either the preset doesn't exist or the provider hasn't been
// wired for that preset — callers should then fall back to the
// default preset rather than send an invalid voice name.
func LookupProviderVoice(presetID, provider string) (string, bool) {
	for _, p := range VoicePresets {
		if p.ID != presetID {
			continue
		}
		if voice, ok := p.Providers[provider]; ok && voice != "" {
			return voice, true
		}
		return "", false
	}
	return "", false
}

// DefaultPresetID is the fallback when a character has no assigned
// voice. "neutral" was picked because it reads as androgynous and
// doesn't lock the character into a gender assumption the script
// might contradict.
const DefaultPresetID = "neutral"
