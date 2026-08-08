package main

// check-tts-voices guard.
//
// Verifies the internal consistency of service/tts/voices.go:
//   - Every VoicePreset ID is unique.
//   - Every preset has at least one provider mapping.
//   - Every provider-name key inside Providers[] is non-empty, and
//     the voice name it maps to is non-empty. Silently missing
//     voice names surface at runtime as "provider rejected voice ''"
//     errors — we want the failure at build time.
//   - DefaultPresetID resolves to a real preset in the catalogue
//     AND has at least one provider mapping. A missing default is
//     a fatal production bug (every character without an explicit
//     preset would fail synthesis).
//
// We rely on the tts package's public surface (VoicePresets +
// DefaultPresetID + LookupProviderVoice) rather than parsing the
// file — that way schema churn in voices.go only affects the tts
// package itself, not this guard.

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"server/service/tts"
)

func runCheckTTSVoices() int {
	problems := collectVoiceProblems()
	if len(problems) == 0 {
		fmt.Printf("ok: %d TTS voice preset(s) validated\n", len(tts.VoicePresets))
		return 0
	}
	fmt.Fprintln(os.Stderr, "TTS voice catalogue has issues:")
	for _, p := range problems {
		fmt.Fprintf(os.Stderr, "  - %s\n", p)
	}
	return 1
}

// collectVoiceProblems walks tts.VoicePresets and produces a
// sorted, human-readable list of issues. Empty result = healthy.
func collectVoiceProblems() []string {
	var problems []string

	seenIDs := map[string]int{} // id → first index
	for i, preset := range tts.VoicePresets {
		if strings.TrimSpace(preset.ID) == "" {
			problems = append(problems, fmt.Sprintf("VoicePresets[%d] has empty ID", i))
			continue
		}
		if first, dup := seenIDs[preset.ID]; dup {
			problems = append(problems, fmt.Sprintf(
				"duplicate preset ID %q (first seen at index %d, duplicate at %d)",
				preset.ID, first, i,
			))
			continue
		}
		seenIDs[preset.ID] = i

		if strings.TrimSpace(preset.Label) == "" {
			problems = append(problems, fmt.Sprintf("preset %q has empty Label", preset.ID))
		}
		if len(preset.Providers) == 0 {
			problems = append(problems, fmt.Sprintf(
				"preset %q has no Providers wired — synthesis will always fail for this preset",
				preset.ID,
			))
			continue
		}
		for name, voice := range preset.Providers {
			if strings.TrimSpace(name) == "" {
				problems = append(problems, fmt.Sprintf("preset %q has empty provider-name key", preset.ID))
			}
			if strings.TrimSpace(voice) == "" {
				problems = append(problems, fmt.Sprintf(
					"preset %q provider %q maps to empty voice — request would 400",
					preset.ID, name,
				))
			}
		}
	}

	// DefaultPreset must exist and resolve for at least one provider.
	def := tts.DefaultPresetID
	if strings.TrimSpace(def) == "" {
		problems = append(problems, "DefaultPresetID is empty — unassigned characters would fall through to nothing")
	} else if _, ok := seenIDs[def]; !ok {
		problems = append(problems, fmt.Sprintf(
			"DefaultPresetID=%q is not in VoicePresets — every unassigned character fails synthesis",
			def,
		))
	} else {
		// Resolve via the public lookup to confirm at least one
		// provider is wired. Walking Providers directly would miss
		// runtime-only wiring if we ever add it.
		anyProvider := false
		for _, p := range tts.VoicePresets {
			if p.ID != def {
				continue
			}
			for providerName := range p.Providers {
				if _, ok := tts.LookupProviderVoice(def, providerName); ok {
					anyProvider = true
					break
				}
			}
		}
		if !anyProvider {
			problems = append(problems, fmt.Sprintf(
				"DefaultPresetID=%q has no working provider mapping",
				def,
			))
		}
	}

	sort.Strings(problems)
	return problems
}
