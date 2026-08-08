package tts

import "testing"

func TestLookupProviderVoice_KnownPreset(t *testing.T) {
	cases := map[string]string{
		"neutral":      "alloy",
		"female-warm":  "shimmer",
		"female-clear": "nova",
		"male-deep":    "onyx",
		"male-clear":   "echo",
	}
	for presetID, wantVoice := range cases {
		got, ok := LookupProviderVoice(presetID, "openai")
		if !ok {
			t.Errorf("preset %q should be wired for openai", presetID)
			continue
		}
		if got != wantVoice {
			t.Errorf("preset %q → openai voice: got %q, want %q", presetID, got, wantVoice)
		}
	}
}

func TestLookupProviderVoice_UnknownPreset(t *testing.T) {
	if _, ok := LookupProviderVoice("does-not-exist", "openai"); ok {
		t.Error("unknown preset should return ok=false")
	}
}

func TestLookupProviderVoice_UnwiredProvider(t *testing.T) {
	// Preset exists but the provider isn't wired — caller should see
	// ok=false and fall back to the default preset rather than send
	// an invalid voice name.
	if _, ok := LookupProviderVoice("neutral", "not-a-real-provider"); ok {
		t.Error("unwired provider should return ok=false")
	}
}

func TestVoicePresets_DefaultPresetExists(t *testing.T) {
	// DefaultPresetID is what the TTS caller falls back to when a
	// character has no voice assigned. It must exist in the catalogue
	// AND be wired for at least one provider, otherwise the whole
	// TTS path silently degrades.
	found := false
	for _, p := range VoicePresets {
		if p.ID == DefaultPresetID {
			found = true
			if len(p.Providers) == 0 {
				t.Errorf("default preset %q has no providers wired", DefaultPresetID)
			}
			break
		}
	}
	if !found {
		t.Errorf("DefaultPresetID %q missing from VoicePresets", DefaultPresetID)
	}
}

func TestVoicePresets_UniqueIDs(t *testing.T) {
	// Preset IDs are stored on w_character.voice_preset — duplicates
	// would make the UI render two entries that can't be distinguished.
	seen := map[string]bool{}
	for _, p := range VoicePresets {
		if seen[p.ID] {
			t.Errorf("duplicate preset ID: %q", p.ID)
		}
		seen[p.ID] = true
	}
}
