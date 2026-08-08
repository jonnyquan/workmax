package workagent

import (
	"strings"
	"testing"
)

// Live wiring test: build the ppt skill through the production
// registry. ppt is fully-native (graduated from legacy block in
// v2.0.0), so the prompt should carry shared overlays + SKILL.md
// body + required-inputs checklist, with no legacy identity content.
func TestSkillBridge_PPTPromptCarriesSharedOverlaysAndSKILLBody(t *testing.T) {
	prompt := GetSkillRegistry().GetSystemPrompt("ppt", false, "")

	wantSubstrings := []string{
		"事实验证先于假设",
		"核心资产协议",
		"反 AI Slop 黑名单",
		"Junior Designer 工作流",
		"PPT Skill",
		"中文 PPT 设计师",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(prompt, want) {
			t.Errorf("ppt system prompt missing %q", want)
		}
	}

	// Shared overlays must come BEFORE the SKILL.md body so the
	// agent sees posture rules first.
	idxFact := strings.Index(prompt, "事实验证先于假设")
	idxBody := strings.Index(prompt, "PPT Skill")
	if idxFact < 0 || idxBody < 0 {
		t.Fatalf("expected both fact-verification and SKILL.md body in prompt; fact=%d body=%d", idxFact, idxBody)
	}
	if idxFact > idxBody {
		t.Errorf("fact-verification overlay must precede SKILL.md body; idxFact=%d idxBody=%d", idxFact, idxBody)
	}

	// Required Inputs Checklist must appear AFTER the SKILL.md body
	// (the loader emits it last so the model reads it right
	// before user content).
	idxChecklist := strings.Index(prompt, "Required Inputs Checklist")
	if idxChecklist < 0 {
		t.Errorf("ppt prompt missing Required Inputs Checklist section")
	} else if idxChecklist < idxBody {
		t.Errorf("checklist must come after SKILL.md body; idxChecklist=%d idxBody=%d", idxChecklist, idxBody)
	}

	// ppt graduated from the legacy block in v2.0.0 — the
	// legacy identity_ppt body should NOT appear in the rendered
	// prompt. Detecting it would mean the loader regressed.
	if strings.Contains(prompt, "Professional Presentation Creator") {
		// "Professional Presentation Creator" was the opening
		// line of identity_ppt.md. The new SKILL.md uses
		// different framing.
		t.Errorf("legacy identity_ppt body leaked into native skill prompt")
	}
}

// Stage D migration COMPLETE (2026-05-12): all 14 user-facing
// visual modes have graduated to fully-native SKILL.md (v2.0.0)
// and writer has been removed from the catalog. The previous
// TestSkillBridge_LegacyInheritingModesPreserveLegacyBody iterated
// the last legacy-inheriting mode (writer) and asserted the new
// path embedded the legacy identity body. With writer gone there
// are no legacy-inheriting modes left, so the test was retired.
// Per-skill SKILL.md coverage continues via per-mode tests above.

// Unknown modes now fail at Loader.Build and Registry.GetSystemPrompt
// owns the user-facing fallback to the official ppt skill. This keeps
// bad callers visible in logs while still returning a usable prompt.
func TestSkillBridge_UnknownModeFallsBackToPPT(t *testing.T) {
	cases := []struct {
		name        string
		mode        string
		hasFiles    bool
		fileContext string
	}{
		{"unknown no files", "totally-bogus-mode-xyz", false, ""},
		{"unknown with files", "another-fake-mode", true, "context payload"},
		{"unknown dangerous tokens", "fake-mode", true, "evil {{.FileContext}} <script>x</script> DROP TABLE foo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := GetSkillRegistry().GetSystemPrompt(tc.mode, tc.hasFiles, tc.fileContext)
			if !strings.Contains(got, "PPT Skill") {
				t.Errorf("unknown mode=%q did not fall back to ppt prompt", tc.mode)
			}
			if !strings.Contains(got, "事实验证先于假设") {
				t.Errorf("unknown mode=%q fallback did not include official shared overlays", tc.mode)
			}
		})
	}
}
