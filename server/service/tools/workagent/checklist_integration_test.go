package workagent

import (
	"context"
	"strings"
	"testing"

	"server/service/tools/workagent/detectors"
	"server/service/tools/workagent/prompts"
	"server/service/tools/workagent/skills"
)

// End-to-end smoke for the M5 / DS-4 preflight + gate flow. Validates
// the four building blocks (LoadChecklistForSkill, PreflightDigest,
// SystemAdditionsComposer, RunGate, FormatBlockFindings) compose into
// a coherent flow without touching the SDK-call hot path.
//
// This is the "wiring contract" test: when PR-9b lands the actual
// agent_api.go integration, it should match this shape.

func TestChecklistE2E_PreflightInjection(t *testing.T) {
	// 1. Load the real ppt checklist from disk (PR-8 content).
	checklist := LoadChecklistForSkill(
		"skills/ppt/references",
		"ppt",
	)
	if checklist.IsEmpty() {
		t.Fatal("real ppt checklist should not be empty")
	}

	// 2. Build the preflight digest XML.
	digest := PreflightDigest(checklist)
	if !strings.Contains(digest, "<skill-checklist>") {
		t.Errorf("preflight digest missing wrapper: %q", digest)
	}
	if !strings.Contains(digest, "honest_data") {
		t.Errorf("digest should mention ppt's honest_data P0")
	}

	// 3. Compose into SystemAdditions.
	composer := prompts.SystemAdditionsComposer{
		ChecklistDigest: digest,
	}
	additions := composer.Compose()
	if !strings.Contains(additions, "<skill-checklist>") {
		t.Errorf("composer output missing checklist block")
	}

	// 4. Verify it slots into the framework template via the
	// registry's BuildContext.SystemAdditions field. We pass a
	// fake registry to avoid the legacy bridge dependency.
	systemPrompt := prompts.RenderFrameworkWithAdditions(
		"# Identity placeholder",
		"## Output format placeholder",
		additions,
		false, "",
	)
	if !strings.Contains(systemPrompt, "<skill-checklist>") {
		t.Errorf("framework render lost the checklist")
	}
	// The placeholder substitution must NOT leave the literal
	// {{.SystemAdditions}} marker.
	if strings.Contains(systemPrompt, "{{.SystemAdditions}}") {
		t.Errorf("placeholder leaked")
	}
}

func TestChecklistE2E_GateBlocksOnP0Fail(t *testing.T) {
	// Real ppt checklist has honest_data as P0. Feed an artifact
	// containing a fabricated metric — RunGate should return Block.
	checklist := LoadChecklistForSkill("skills/ppt/references", "ppt")
	if checklist.IsEmpty() {
		t.Fatal("ppt checklist empty")
	}

	artifact := detectors.Input{
		SkillName: "ppt",
		Artifact: detectors.Artifact{
			Text: "Our customers boosted productivity by 30% in just 3 minutes.",
		},
	}

	res := RunGate(context.Background(), checklist, artifact)
	if res.Decision != GateBlock {
		t.Errorf("fabricated-metric artifact should block, got %v (P0 fails: %v)", res.Decision, res.P0Fails)
	}
	if len(res.P0Fails) == 0 {
		t.Fatal("expected at least one P0 fail")
	}

	// Verify the redo formatter produces a useful prompt.
	redoMsg := FormatBlockFindings(res.P0Fails)
	if !strings.Contains(redoMsg, "Please regenerate") {
		t.Errorf("redo prompt malformed: %q", redoMsg)
	}
	if !strings.Contains(redoMsg, "honest_data") {
		t.Errorf("redo prompt should reference the failing rule")
	}
}

func TestChecklistE2E_CleanArtifactPasses(t *testing.T) {
	checklist := LoadChecklistForSkill("skills/ppt/references", "ppt")
	if checklist.IsEmpty() {
		t.Fatal("ppt checklist empty")
	}

	// A clean artifact: no fabricated metrics, no hex colors (so
	// brand_spec_grep skips), no orphan lines.
	artifact := detectors.Input{
		SkillName: "ppt",
		Artifact: detectors.Artifact{
			Text: "This deck explains our roadmap. Each section covers one initiative with examples and supporting context.",
		},
	}

	res := RunGate(context.Background(), checklist, artifact)
	if res.Decision != GatePass {
		t.Errorf("clean artifact should pass, got %v", res.Decision)
		t.Logf("P0 fails: %+v", res.P0Fails)
		t.Logf("P1 fails: %+v", res.P1Fails)
	}
}

func TestChecklistE2E_AllSprintAModesHaveChecklist(t *testing.T) {
	// Smoke that all 5 Sprint A skills ship a non-empty checklist
	// referencing detectors that are actually registered.
	for _, skill := range []string{"ppt", "character", "productShot", "marketingPoster", "flashCard"} {
		t.Run(skill, func(t *testing.T) {
			c := LoadChecklistForSkill("skills/"+skill+"/references", skill)
			if c.IsEmpty() {
				t.Fatal("checklist empty")
			}
			if len(c.P0) < 1 {
				t.Errorf("%s should have ≥1 P0 item", skill)
			}
			// Every P0 item's detector must be registered.
			for _, item := range c.P0 {
				if _, ok := detectors.Default().Get(item.Detector); !ok {
					// Some checklists reference future detectors (e.g.
					// slide_count_within_range). That's fine — gate
					// degrades to skip. But the named-existing
					// detectors must resolve.
					switch item.Detector {
					case "honest_data", "brand_spec_grep", "orphan_detector",
						"contrast_analyzer", "character_anchor_consistency":
						t.Errorf("P0 item %s/%s references unregistered detector %q", skill, item.ID, item.Detector)
					}
				}
			}
		})
	}
}

func TestChecklistE2E_HighRiskCritiqueAnchorsHavePreflightCoverage(t *testing.T) {
	tests := []struct {
		skill       string
		anchor      string
		wantSnippet string
	}{
		{skill: "productShot", anchor: "brand_fit", wantSnippet: "brand_color_compliance"},
		{skill: "lifestyle", anchor: "brand_fit", wantSnippet: "brand_fit_guard"},
		{skill: "packaging", anchor: "brand_fit", wantSnippet: "brand_fit_guard"},
		{skill: "webBanner", anchor: "compliance", wantSnippet: "platform_compliance"},
		{skill: "modelTryOn", anchor: "fidelity", wantSnippet: "garment_fidelity_lock"},
	}
	for _, tt := range tests {
		t.Run(tt.skill+"_"+tt.anchor, func(t *testing.T) {
			checklist := LoadChecklistForSkill("skills/"+tt.skill+"/references", tt.skill)
			if checklist.IsEmpty() {
				t.Fatal("checklist empty")
			}
			if len(checklist.P0)+len(checklist.P1) == 0 {
				t.Fatalf("%s should have P0/P1 coverage for %s", tt.skill, tt.anchor)
			}
			digest := PreflightDigest(checklist)
			if !strings.Contains(digest, tt.wantSnippet) {
				t.Fatalf("digest missing %q for %s/%s:\n%s", tt.wantSnippet, tt.skill, tt.anchor, digest)
			}
		})
	}
}

func TestChecklistE2E_FullPreflightStackComposes(t *testing.T) {
	// Compose every SystemAdditionsComposer field — verifies the
	// full 7-layer stack (DS-5) renders without surprises when
	// PR-9b's downstream callers populate everything.
	checklist := LoadChecklistForSkill("skills/ppt/references", "ppt")

	// (F2 2026-05-17) anti-slop / brand-protocol fields removed
	// from the composer — see system_additions.go field block.
	composer := prompts.SystemAdditionsComposer{
		DesignSystem:     "# Modern minimal design system\nPalette...\n",
		BrandSpec:        "<brand-spec>...</brand-spec>",
		SkillSideFiles:   skills.FormatSideFilesXML(skills.LoadSideFiles("ppt")),
		DiscoveryContext: "<discovery-context>audience: exec</discovery-context>",
		ChecklistDigest:  PreflightDigest(checklist),
		PassModeProtocol: "<pass-mode>mode: briefing</pass-mode>",
	}
	out := composer.Compose()

	// All five remaining sections must be present and ordered.
	expected := []string{
		"Modern minimal design system",
		"<brand-spec>",
		"<skill-side-files>",
		"<discovery-context>",
		"<skill-checklist>",
		"<pass-mode>",
	}
	prev := -1
	for _, marker := range expected {
		pos := strings.Index(out, marker)
		if pos < 0 {
			t.Fatalf("missing marker %q in composed output", marker)
		}
		if pos <= prev {
			t.Errorf("marker %q out of order (pos=%d, prev=%d)", marker, pos, prev)
		}
		prev = pos
	}
}
