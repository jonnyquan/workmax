package tools

import "testing"

func TestGetAvatarScenePresets_ReturnsCanonicalSet(t *testing.T) {
	presets := GetAvatarScenePresets()
	if len(presets) == 0 {
		t.Fatalf("expected non-empty preset list, got 0")
	}

	seenKeys := make(map[string]struct{}, len(presets))
	for _, preset := range presets {
		if preset.Key == "" {
			t.Errorf("preset has empty Key: %+v", preset)
		}
		if preset.ID == "" {
			t.Errorf("preset %q has empty ID", preset.Key)
		}
		if preset.Prompt == "" {
			t.Errorf("preset %q has empty Prompt", preset.Key)
		}
		if preset.AspectRatio == "" {
			t.Errorf("preset %q has empty AspectRatio", preset.Key)
		}
		if _, dup := seenKeys[preset.Key]; dup {
			t.Errorf("duplicate preset key %q", preset.Key)
		}
		seenKeys[preset.Key] = struct{}{}
	}
}

func TestIsKnownAvatarScenePresetKey(t *testing.T) {
	// Every canonical preset key must be recognised.
	for _, preset := range GetAvatarScenePresets() {
		if !IsKnownAvatarScenePresetKey(preset.Key) {
			t.Errorf("canonical key %q rejected by IsKnownAvatarScenePresetKey", preset.Key)
		}
	}

	// Empty means "no preset selected" — valid so callers don't need to
	// special-case it before the check.
	if !IsKnownAvatarScenePresetKey("") {
		t.Errorf("empty key should be accepted")
	}

	// Unknown strings (including near-misses) must be rejected.
	rejected := []string{
		"professional_headshot", // wrong case style
		"ProfessionalHeadshot",  // wrong casing
		"unknown",
		"<script>",
		" professionalHeadshot", // stray whitespace — we expect callers to trim
	}
	for _, key := range rejected {
		if IsKnownAvatarScenePresetKey(key) {
			t.Errorf("unexpected accept for %q", key)
		}
	}
}

func TestGetAvatarScenePresets_IsDefensiveCopy(t *testing.T) {
	original := GetAvatarScenePresets()
	if len(original) == 0 {
		t.Fatalf("expected non-empty preset list")
	}

	firstKey := original[0].Key
	original[0].Prompt = "tampered"
	original[0].Key = "tampered-key"

	refetched := GetAvatarScenePresets()
	if refetched[0].Key != firstKey {
		t.Errorf("registry was mutated through returned slice: got key %q, want %q", refetched[0].Key, firstKey)
	}
	if refetched[0].Prompt == "tampered" {
		t.Errorf("registry Prompt was mutated through returned slice")
	}
}
