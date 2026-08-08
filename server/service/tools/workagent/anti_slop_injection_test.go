package workagent

import (
	"strings"
	"testing"

	"server/service/tools/workagent/skills"
)

// Sprint-B M6: verifies the Honest Placeholders rules ride through
// the bundle build into the system prompt for all 5 high-frequency
// Sprint A modes. These tests catch the silent regression of "an
// editor accidentally dropped the anti-ai-slop reference" — the rule
// content lives in _shared/anti-ai-slop.md but it's only effective
// when the model's system prompt actually carries it.

func TestAntiSlop_HonestPlaceholdersInjectedAcrossSprintAModes(t *testing.T) {
	registry := skills.NewRegistry(noopLegacy{})
	for _, skill := range []string{"ppt", "character", "productShot", "marketingPoster", "flashCard"} {
		t.Run(skill, func(t *testing.T) {
			bundle, err := registry.Build(skill, skills.BuildContext{})
			if err != nil {
				t.Fatalf("build %s: %v", skill, err)
			}
			// The anti-ai-slop block must appear AND must contain
			// the load-bearing Honest Placeholders section we
			// added in Sprint-B M6.
			if !strings.Contains(bundle.SystemPrompt, "Honest Placeholders") {
				t.Errorf("system prompt for %q missing 'Honest Placeholders' section", skill)
			}
			if !strings.Contains(bundle.SystemPrompt, "永不编造的字段") {
				t.Errorf("system prompt for %q missing 'never-fabricate' rule list", skill)
			}
			// Sanity: the older general anti-slop rules also
			// survive — verifies the file wasn't accidentally
			// truncated.
			if !strings.Contains(bundle.SystemPrompt, "AI Slop") || !strings.Contains(bundle.SystemPrompt, "Emoji 装饰") {
				t.Errorf("system prompt for %q missing pre-M6 anti-slop rules", skill)
			}
		})
	}
}

func TestAntiSlop_HonestPlaceholdersExamplesPresent(t *testing.T) {
	// One representative skill suffices; if the file builds at all
	// it builds the same content for every skill.
	registry := skills.NewRegistry(noopLegacy{})
	bundle, err := registry.Build("ppt", skills.BuildContext{})
	if err != nil {
		t.Fatal(err)
	}
	// Specific placeholder examples the agent should learn to
	// emit instead of fabricating numbers.
	wantExamples := []string{
		"em dash",     // 用 em dash 占位
		"待补充",         // 中文占位
		"industry avg", // 例外标签
	}
	for _, want := range wantExamples {
		if !strings.Contains(bundle.SystemPrompt, want) {
			t.Errorf("system prompt missing placeholder example %q", want)
		}
	}
}

// Sprint-B M4: Brand-Spec 5-step protocol verification. Same
// injection contract as M6 — the rule lives in
// _shared/asset-protocol.md and flows into every skill that
// declares `references.shared: [asset-protocol]`. Tests assert the
// load-bearing pieces (RULE 0, 5-step extraction, 9-section schema,
// Vocalize step) survive into the assembled prompt.

func TestBrandSpec_M4ProtocolInjectedAcrossSprintAModes(t *testing.T) {
	registry := skills.NewRegistry(noopLegacy{})
	for _, skill := range []string{"ppt", "character", "productShot", "marketingPoster", "flashCard"} {
		t.Run(skill, func(t *testing.T) {
			bundle, err := registry.Build(skill, skills.BuildContext{})
			if err != nil {
				t.Fatalf("build %s: %v", skill, err)
			}
			// RULE 0 — never guess from memory.
			if !strings.Contains(bundle.SystemPrompt, "RULE 0") {
				t.Errorf("system prompt for %q missing RULE 0", skill)
			}
			if !strings.Contains(bundle.SystemPrompt, "永不凭记忆猜") {
				t.Errorf("system prompt for %q missing the never-guess directive", skill)
			}
			// 5-step extraction workflow.
			for _, step := range []string{"Locate", "Download", "Grep Hex", "Codify", "Vocalize"} {
				if !strings.Contains(bundle.SystemPrompt, step) {
					t.Errorf("system prompt for %q missing extraction step %q", skill, step)
				}
			}
		})
	}
}

func TestBrandSpec_M4SchemaSectionsPresent(t *testing.T) {
	// 9-section schema must list every section by number — these
	// are the load-bearing anchor points the model uses to know
	// "what to write where" in brand-spec.md.
	registry := skills.NewRegistry(noopLegacy{})
	bundle, err := registry.Build("ppt", skills.BuildContext{})
	if err != nil {
		t.Fatal(err)
	}
	wantSections := []string{
		"## 1. Color",
		"## 2. Typography",
		"## 3. Spacing",
		"## 4. Layout",
		"## 5. Components",
		"## 6. Motion",
		"## 7. Voice",
		"## 8. Brand",
		"## 9. Anti-patterns",
	}
	for _, want := range wantSections {
		if !strings.Contains(bundle.SystemPrompt, want) {
			t.Errorf("system prompt missing schema section %q", want)
		}
	}
}

func TestBrandSpec_M4VocalizeRequirement(t *testing.T) {
	// The Vocalize step requires the agent to read back the
	// extracted spec + WAIT for confirmation before generating
	// any artifact. Verify the prompt actually surfaces both
	// halves of that contract.
	registry := skills.NewRegistry(noopLegacy{})
	bundle, err := registry.Build("ppt", skills.BuildContext{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"是否确认",                  // ask user to confirm
		"unconfirmed",          // status marker for skip-confirmation case
		"待品牌方确认",              // visible-watermark fallback when skipped
		"绝不",                   // hard rule wording
	} {
		if !strings.Contains(bundle.SystemPrompt, want) {
			t.Errorf("Vocalize step missing %q in system prompt", want)
		}
	}
}

// Sprint-B DS-4: visual AI-tells + design-system anti-pattern
// enforcement ride into every skill's system prompt. The DS-4
// section adds two universal anti-tells (Inter-as-default,
// glassmorphism) plus a meta-rule promoting the selected design
// system's Anti-patterns section to P0. All three contracts must
// survive the bundle-build pipeline so the post-emit gate has a
// clean rule set to detect violations against.

func TestAntiSlop_DS4VisualAITellsInjectedAcrossSprintAModes(t *testing.T) {
	registry := skills.NewRegistry(noopLegacy{})
	for _, skill := range []string{"ppt", "character", "productShot", "marketingPoster", "flashCard"} {
		t.Run(skill, func(t *testing.T) {
			bundle, err := registry.Build(skill, skills.BuildContext{})
			if err != nil {
				t.Fatalf("build %s: %v", skill, err)
			}
			// Glassmorphism is the most common 2022-era AI tell.
			if !strings.Contains(bundle.SystemPrompt, "Glassmorphism") {
				t.Errorf("system prompt for %q missing Glassmorphism rule", skill)
			}
			if !strings.Contains(bundle.SystemPrompt, "backdrop-filter") {
				t.Errorf("system prompt for %q missing backdrop-filter callout", skill)
			}
			// Inter-as-default — neutral-default's anti-pattern
			// generalised to a universal AI signature.
			if !strings.Contains(bundle.SystemPrompt, "Inter 当默认字体") {
				t.Errorf("system prompt for %q missing Inter-as-default rule", skill)
			}
			if !strings.Contains(bundle.SystemPrompt, "system-ui") {
				t.Errorf("system prompt for %q missing system-ui fallback guidance", skill)
			}
		})
	}
}

func TestAntiSlop_DS4DesignSystemEnforcementRule(t *testing.T) {
	// The meta-rule is the load-bearing piece: it tells the model
	// that <design-system>'s Anti-patterns section is P0, not just
	// one of 9 informational sections. Without it, the model treats
	// the DS body as background reading rather than a hard floor.
	registry := skills.NewRegistry(noopLegacy{})
	bundle, err := registry.Build("ppt", skills.BuildContext{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<design-system>",      // refers to the SystemAdditions block name
		"Anti-patterns",        // the section being promoted
		"P0",                   // priority promotion
		"checklist gate 直接 Block", // enforcement consequence
	} {
		if !strings.Contains(bundle.SystemPrompt, want) {
			t.Errorf("DS-4 enforcement rule missing %q in system prompt", want)
		}
	}
	// Per-direction anti-pattern callouts surface the direction →
	// concrete rule mapping. At least 3 of the 5 representative
	// directions must be named (modern-minimal / editorial-magazine /
	// brutalist / vintage-film) so the model has working examples.
	calloutCount := 0
	for _, name := range []string{"modern-minimal", "editorial-magazine", "brutalist", "vintage-film"} {
		if strings.Contains(bundle.SystemPrompt, name) {
			calloutCount++
		}
	}
	if calloutCount < 3 {
		t.Errorf("DS-4 enforcement rule should cite at least 3 representative directions; got %d", calloutCount)
	}
}

// noopLegacy is the same nil-provider used by the skills package
// tests — registry.Build doesn't need legacy fallback when every
// Sprint-A skill has its own SKILL.md or legacy block.
type noopLegacy struct{}

func (noopLegacy) Identity(string) string                                   { return "" }
func (noopLegacy) OutputFormat(string) string                               { return "" }
func (noopLegacy) RenderFramework(id, of string, _ skills.BuildContext) string { return id + "\n\n" + of }
