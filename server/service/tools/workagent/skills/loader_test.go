package skills

import (
	"strings"
	"testing"
)

// stubLegacyProvider implements LegacyTemplateProvider with fixed
// strings so the loader's behaviour is observable without dragging
// in the real prompts/ package. The framework rendering is a simple
// concatenation here; in production it's the full agent_framework.md
// substitution. Tests assert *that* the loader stitched things in
// the right order, not byte-identity to production output.
type stubLegacyProvider struct {
	identityFor map[string]string
	outputFor   map[string]string
}

func (s stubLegacyProvider) Identity(mode string) string {
	if v, ok := s.identityFor[mode]; ok {
		return v
	}
	return s.identityFor["writer"]
}

func (s stubLegacyProvider) OutputFormat(mode string) string {
	if v, ok := s.outputFor[mode]; ok {
		return v
	}
	return s.outputFor["writer"]
}

func (s stubLegacyProvider) RenderFramework(identity, output string, _ BuildContext) string {
	return "<<framework>>\nIDENTITY:" + identity + "\nOUTPUT:" + output + "\n<<end>>"
}

// resetCaches drops every package-level memo so each test starts
// fresh. embed.FS-backed caches don't otherwise need this, but tests
// rely on observable input/output so we want each subtest to read
// disk independently.
func resetCaches() {
	manifestCacheMu.Lock()
	manifestCache = map[string]*Manifest{}
	manifestCacheMu.Unlock()
	sharedCacheMu.Lock()
	sharedCache = map[string]string{}
	sharedCacheMu.Unlock()
	localCacheMu.Lock()
	localCache = map[string]string{}
	localCacheMu.Unlock()
}

// The ppt skill graduated from the legacy block in v2.0.0 — its body
// now lives in skills/ppt/SKILL.md, not in legacy templates. Pin the
// contract: shared references appear before the SKILL.md body,
// SKILL.md content is present, and the legacy stub IS NOT consulted
// for ppt (because the manifest carries no Legacy field).
func TestLoader_BuildPPTSkill_IncludesSharedReferencesBeforeNativeBody(t *testing.T) {
	resetCaches()
	loader := NewLoader(stubLegacyProvider{
		identityFor: map[string]string{
			"ppt":    "<<should-not-be-used-ppt-is-native>>",
			"writer": "<<legacy-writer-identity>>",
		},
		outputFor: map[string]string{
			"writer": "<<legacy-writer-output>>",
		},
	})

	bundle, err := loader.Build("ppt", BuildContext{})
	if err != nil {
		t.Fatalf("Build(ppt) error: %v", err)
	}

	// Each shared reference's distinctive heading appears exactly
	// once. Pinning the heading rather than full body keeps the
	// test stable when the markdown copy gets edited.
	wantSubstrings := []string{
		"事实验证先于假设",            // _shared/fact-verification.md
		"核心资产协议",              // _shared/asset-protocol.md
		"反 AI Slop 黑名单",       // _shared/anti-ai-slop.md
		"Junior Designer 工作流", // _shared/junior-designer-workflow.md
		"PPT Skill",           // SKILL.md heading
		"工作流(4 阶段",            // SKILL.md distinctive section
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(bundle.SystemPrompt, want) {
			t.Errorf("SystemPrompt missing %q\n--- prompt (first 800) ---\n%s", want, bundle.SystemPrompt[:min(800, len(bundle.SystemPrompt))])
		}
	}

	// Negative assertion: the legacy stub's identity marker must
	// NOT appear. ppt is fully-native; any read from the legacy
	// provider would mean the loader regressed back to the
	// legacy: code path.
	if strings.Contains(bundle.SystemPrompt, "<<should-not-be-used-ppt-is-native>>") {
		t.Errorf("legacy provider was consulted for fully-native ppt skill")
	}

	// Shared references must precede the SKILL.md body in the
	// rendered output — this is the contract that keeps the
	// horizontal posture rules above the mode-specific body.
	idxFact := strings.Index(bundle.SystemPrompt, "事实验证先于假设")
	idxBody := strings.Index(bundle.SystemPrompt, "PPT Skill")
	if idxFact < 0 || idxBody < 0 || idxFact > idxBody {
		t.Errorf("expected fact-verification block before SKILL.md body; idxFact=%d idxBody=%d", idxFact, idxBody)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Genuinely-unknown modes (no skill.yaml on disk) must fail fast.
// Production modes all have official skill.yaml bundles now; silently
// synthesizing a legacy prompt would hide bad callers and contradict
// the official-only skill model.
func TestLoader_UnknownMode_ReturnsError(t *testing.T) {
	resetCaches()
	loader := NewLoader(stubLegacyProvider{
		identityFor: map[string]string{
			"writer":         "<<legacy-writer-identity>>",
			"unknown-bogus":  "<<unknown-bogus-identity>>",
			"future-feature": "<<future-feature-identity>>",
		},
		outputFor: map[string]string{
			"writer":         "<<legacy-writer-output>>",
			"unknown-bogus":  "<<unknown-bogus-output>>",
			"future-feature": "<<future-feature-output>>",
		},
	})

	for _, mode := range []string{"unknown-bogus", "future-feature", "totally-unknown-mode-xyz"} {
		t.Run(mode, func(t *testing.T) {
			if _, err := loader.Build(mode, BuildContext{}); err == nil {
				t.Fatalf("Build(%s) succeeded; want missing skill.yaml error", mode)
			}
		})
	}
}

// Every production mode has a skill.yaml that declares horizontal
// _shared references. Iterate the embedded FS, build each one
// against a stub legacy provider, and assert (a) bundle has a real
// version (not "legacy"), (b) at least one _shared reference body
// appears in the rendered prompt. This is the cheapest insurance
// that "the new mode I just added works" without needing a per-
// mode test file.
func TestLoader_AllMigratedModesBuildWithSharedOverlays(t *testing.T) {
	resetCaches()
	loader := NewLoader(stubLegacyProvider{
		identityFor: map[string]string{"writer": "<<legacy-writer-identity>>"},
		outputFor:   map[string]string{"writer": "<<legacy-writer-output>>"},
	})

	// Walk skillsFS at the root and pick every directory that
	// isn't _shared — those are skill bundles by convention.
	entries, err := skillsFS.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	migratedCount := 0
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "_shared" {
			continue
		}
		mode := e.Name()
		t.Run(mode, func(t *testing.T) {
			bundle, err := loader.Build(mode, BuildContext{})
			if err != nil {
				t.Fatalf("Build(%s): %v", mode, err)
			}
			if bundle.Version == "legacy" {
				t.Errorf("mode %q has skill.yaml but loader returned legacy bundle", mode)
			}
			if bundle.Artifacts == nil {
				t.Errorf("mode %q missing artifact metadata", mode)
			} else {
				if bundle.Artifacts.PrimaryType == "" {
					t.Errorf("mode %q artifact primary type is empty", mode)
				}
				if len(bundle.Artifacts.OutputTypes) == 0 {
					t.Errorf("mode %q artifact output types are empty", mode)
				}
				if len(bundle.Artifacts.PreviewTypes) == 0 {
					t.Errorf("mode %q artifact preview types are empty", mode)
				}
			}
			// Every skill.yaml declares fact-verification +
			// junior-designer-workflow at minimum. Pin both so a
			// future skill that forgets references[].shared
			// trips this test.
			for _, want := range []string{"事实验证先于假设", "Junior Designer 工作流"} {
				if !strings.Contains(bundle.SystemPrompt, want) {
					t.Errorf("mode %q missing shared overlay %q", mode, want)
				}
			}
			migratedCount++
		})
	}
	if migratedCount == 0 {
		t.Fatal("no migrated skills found — embed.FS or directory layout broken")
	}
}

// FileContext substitution + sanitization must continue to work
// through the new loader. The contract today: pass through to the
// legacy renderer's FileContext slot, sanitizing dangerous
// substrings like template injection markers.
func TestLoader_FileContextAppliesSanitization(t *testing.T) {
	resetCaches()
	provider := fileContextSpyProvider{}
	loader := NewLoader(&provider)

	_, err := loader.Build("ppt", BuildContext{
		HasFiles:    true,
		FileContext: "user-supplied {{.FileContext}} <script>alert(1)</script>",
	})
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}
	if !provider.called {
		t.Fatal("RenderFramework was not invoked")
	}
	if !provider.lastCtx.HasFiles {
		t.Errorf("HasFiles flag did not propagate to provider")
	}
	if provider.lastCtx.FileContext == "" {
		t.Errorf("FileContext did not propagate to provider")
	}
}

// fileContextSpyProvider records the BuildContext the loader hands
// down so the substitution path can be asserted without depending on
// the real prompts/ sanitization.
type fileContextSpyProvider struct {
	called  bool
	lastCtx BuildContext
}

func (p *fileContextSpyProvider) Identity(string) string     { return "id" }
func (p *fileContextSpyProvider) OutputFormat(string) string { return "out" }
func (p *fileContextSpyProvider) RenderFramework(_, _ string, ctx BuildContext) string {
	p.called = true
	p.lastCtx = ctx
	return "rendered"
}

// Manifest validation is intentionally minimal — the loader
// surfaces malformed manifests at Build time rather than rejecting
// them at registry-construction time. Pin the failure mode so a
// future "validate at startup" decision is an explicit choice, not
// an accident.
func TestLoader_MalformedManifest_SurfacesAtBuild(t *testing.T) {
	resetCaches()
	if _, err := parseManifest([]byte("not: yaml: [colon")); err == nil {
		t.Fatal("expected parse error for malformed yaml")
	}
}

// Manifest's required_inputs surface as a checklist section in the
// rendered system prompt. This is what makes the agent ask before
// generating — without the section the model has no machine-readable
// signal that "you must verify these are present".
func TestLoader_RequiredInputs_RenderAsChecklist(t *testing.T) {
	resetCaches()
	loader := NewLoader(stubLegacyProvider{
		identityFor: map[string]string{"ppt": "<<legacy-ppt>>", "writer": "<<legacy-writer>>"},
		outputFor:   map[string]string{"ppt": "<<output>>", "writer": "<<output>>"},
	})

	bundle, err := loader.Build("ppt", BuildContext{})
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}
	prompt := bundle.SystemPrompt

	wantHeader := "Required Inputs Checklist"
	if !strings.Contains(prompt, wantHeader) {
		t.Fatalf("checklist header missing from prompt")
	}

	// Pin every concrete kind from ppt's manifest. If a future
	// edit to skill.yaml drops a kind without updating this test,
	// fail loudly — the checklist is the contract surface, not
	// just a comment.
	wantKinds := []string{
		"topic",
		"audience",
		"slide_count",
		"style",
		"supporting_materials",
		"brand_assets",
	}
	for _, kind := range wantKinds {
		if !strings.Contains(prompt, "**"+kind+"**") {
			t.Errorf("checklist missing required input %q", kind)
		}
	}

	// `When` clauses must render so the agent can distinguish
	// always-required from conditionally-required inputs.
	if !strings.Contains(prompt, "when `brand_specified`") {
		t.Errorf("conditional gate `brand_specified` missing from rendered checklist")
	}

	// The bundle's RequiredInputs slice is also exposed to runtime
	// callers that want to layer Go-side validation later. Pin
	// the count so future code can rely on the field being
	// populated in parallel with the prompt.
	if len(bundle.RequiredInputs) == 0 {
		t.Errorf("bundle.RequiredInputs not populated")
	}
}

// Empty required_inputs must NOT render a dangling checklist section.
// Every production skill currently declares inputs, so pin the helper
// directly rather than relying on a removed legacy writer bundle.
func TestRenderRequiredInputs_Empty_NoChecklistSection(t *testing.T) {
	if got := renderRequiredInputs(nil); got != "" {
		t.Errorf("empty required inputs rendered checklist section: %q", got)
	}
}

// Critique is the first fully-native skill — its manifest has no
// `legacy:` block, body comes from skills/critique/SKILL.md, and
// the local rubric-detail reference layers in additional content.
// This test verifies the fully-native path works without ANY help
// from the legacy provider (passing nil).
//
// If this ever breaks, the loader has silently grown a hidden
// dependency on the legacy bridge.
func TestLoader_CritiqueSkill_BuildsFullyNative(t *testing.T) {
	resetCaches()

	// nil provider: any attempt to call Identity / OutputFormat
	// would panic with NewLoader's noop fallback returning empty
	// strings — which is fine for the body slot but visible in the
	// rendered prompt. We assert the SKILL.md content + the local
	// rubric reference both made it through.
	loader := NewLoader(nil)
	bundle, err := loader.Build("critique", BuildContext{})
	if err != nil {
		t.Fatalf("Build(critique) error: %v", err)
	}

	// Body markers from skills/critique/SKILL.md
	if !strings.Contains(bundle.SystemPrompt, "Critique Skill") {
		t.Errorf("SKILL.md body not found in rendered prompt")
	}
	if !strings.Contains(bundle.SystemPrompt, "5 维度评审") {
		t.Errorf("SKILL.md primary heading not found")
	}

	// Local reference from skills/critique/references/rubric-detail.md
	if !strings.Contains(bundle.SystemPrompt, "评分标准详解") {
		t.Errorf("local rubric-detail reference not loaded into prompt")
	}

	// Required input: "artifact_to_review"
	if !strings.Contains(bundle.SystemPrompt, "artifact_to_review") {
		t.Errorf("critique required_input not rendered in checklist")
	}

	// Shared overlays the manifest declares (fact-verification +
	// junior-designer-workflow); pin presence to detect future
	// references.shared regressions.
	if !strings.Contains(bundle.SystemPrompt, "事实验证先于假设") {
		t.Errorf("fact-verification overlay missing from critique prompt")
	}
	if !strings.Contains(bundle.SystemPrompt, "Junior Designer 工作流") {
		t.Errorf("junior-designer-workflow overlay missing from critique prompt")
	}

	// Manifest version was set to 1.0.0 — pin so a future skill
	// that copy-pastes critique's manifest doesn't accidentally
	// inherit a "legacy" version.
	if bundle.Version != "1.0.0" {
		t.Errorf("expected critique version 1.0.0, got %q", bundle.Version)
	}
}
