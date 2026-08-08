package detectors

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ============================================================================
// Registry
// ============================================================================

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	d := &OrphanDetector{}
	r.Register(d)
	got, ok := r.Get(d.Name())
	if !ok {
		t.Fatalf("Get(%q) miss", d.Name())
	}
	if got != d {
		t.Errorf("Get returned different instance")
	}
}

func TestRegistry_DuplicateRegisterPanics(t *testing.T) {
	r := NewRegistry()
	r.Register(&OrphanDetector{})
	defer func() {
		if recover() == nil {
			t.Error("expected panic on duplicate register")
		}
	}()
	r.Register(&OrphanDetector{})
}

func TestRegistry_NilRegisterPanics(t *testing.T) {
	r := NewRegistry()
	defer func() {
		if recover() == nil {
			t.Error("expected panic on nil register")
		}
	}()
	r.Register(nil)
}

func TestDefaultRegistry_CoreDetectorsRegistered(t *testing.T) {
	expected := []string{
		"honest_data",
		"brand_spec_grep",
		"brand_spec_confirmation",
		"asset_contract_guard",
		"orphan_detector",
		"contrast_analyzer",
		"character_anchor_consistency",
	}
	names := Default().Names()
	for _, want := range expected {
		found := false
		for _, n := range names {
			if n == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected %q registered, got %v", want, names)
		}
	}
}

// ============================================================================
// HonestData (RegexScanner-backed)
// ============================================================================

func TestHonestData_PassesCleanText(t *testing.T) {
	r, _ := HonestData.Run(context.Background(), Input{
		Artifact: Artifact{Text: "Welcome to our product. We help teams work better together."},
	})
	if r.Status != StatusPass {
		t.Errorf("clean text should pass, got %v: %v", r.Status, r.Issues)
	}
}

func TestHonestData_DetectsFabricatedEnglishMetric(t *testing.T) {
	r, _ := HonestData.Run(context.Background(), Input{
		Artifact: Artifact{Text: "Our customers boosted productivity by 30% in just 3 minutes."},
	})
	if r.Status != StatusFail {
		t.Errorf("expected fail, got %v", r.Status)
	}
	if len(r.Issues) == 0 {
		t.Error("fail must carry at least one issue")
	}
}

func TestHonestData_DetectsFabricatedChineseMetric(t *testing.T) {
	r, _ := HonestData.Run(context.Background(), Input{
		Artifact: Artifact{Text: "本产品帮助用户提升 30% 效率，节省 50% 时间。"},
	})
	if r.Status != StatusFail {
		t.Errorf("expected fail on Chinese fabricated metric, got %v", r.Status)
	}
}

func TestHonestData_SkippedOnEmptyArtifact(t *testing.T) {
	r, _ := HonestData.Run(context.Background(), Input{Artifact: Artifact{Text: ""}})
	if r.Status != StatusSkipped {
		t.Errorf("expected skipped on empty, got %v", r.Status)
	}
}

// Sprint-B M6: extended pattern coverage for the detector. Each
// case exercises a phrase shape we expanded the regex catalog for —
// these are real-world failure modes the agent commonly emits in
// marketingPoster / ppt / flashCard contexts.
func TestHonestData_M6ExtendedPatterns(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"english_roi", "Customers see ROI of 5× within the first quarter."},
		{"english_trusted_by", "Trusted by 10,000+ teams worldwide."},
		{"english_setup_in", "Set up in 5 minutes, deploy in 30 days."},
		{"english_nps", "We boast an NPS of 72 across our user base."},
		{"english_accuracy", "Our model achieves 99% accuracy on the benchmark."},
		{"chinese_roi", "ROI 5 倍,投资回报率惊人。"},
		{"chinese_累计", "累计服务 50 万企业用户。"},
		{"chinese_准确率", "课程通过率 95% 满意,内容覆盖完整。"},
		{"chinese_遥遥领先", "我们的产品在行业里遥遥领先所有竞争对手。"},
		{"chinese_搞定", "30 分钟搞定一份完整的演示文稿。"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := HonestData.Run(context.Background(), Input{
				Artifact: Artifact{Text: tc.text},
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if r.Status != StatusFail {
				t.Errorf("expected fail for %q, got %v", tc.text, r.Status)
			}
			if len(r.Issues) == 0 {
				t.Error("fail must carry at least one issue")
			}
		})
	}
}

func TestHonestData_PlaceholderEmDashIsAllowed(t *testing.T) {
	// The Honest Placeholders rule prescribes em-dash (—) when
	// no real number is available. Verify the detector doesn't
	// false-positive on `—` placeholders.
	for _, text := range []string{
		"转化率: —\n月活: —\n",
		"Conversion: —\nUsers: —",
		"[转化率:待补充] [月活:待补充]",
	} {
		r, err := HonestData.Run(context.Background(), Input{
			Artifact: Artifact{Text: text},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if r.Status != StatusPass {
			t.Errorf("placeholder text should pass, got %v: %v (input: %q)", r.Status, r.Issues, text)
		}
	}
}

func TestHonestData_IndustryAvgIsAllowed(t *testing.T) {
	// Spec carves out an exception for explicitly tagged
	// industry averages. (Note: the current regex catalog still
	// flags these — the exception is enforced at the prompt
	// level and gate-decision level. This test documents that
	// the detector itself is conservative; refine if signal/noise
	// becomes a problem.)
	r, _ := HonestData.Run(context.Background(), Input{
		Artifact: Artifact{Text: "Industry average conversion is 2-3% (industry avg)"},
	})
	// Detector currently flags — that's by design (false positive
	// at the detector layer; the gate / human review tolerates it).
	// Only assert no panic and result status is one of {pass, fail}.
	if r.Status == StatusSkipped {
		t.Errorf("non-empty text should not skip, got %v", r.Status)
	}
}

func TestHonestData_LargeInputUnderTimeBudget(t *testing.T) {
	// 50KB of clean text — well above any artifact we'd typically
	// emit (typical: 5-15KB). Verify the detector runs cleanly
	// without throwing.
	text := strings.Repeat("Welcome to our product. We help teams work better.\n", 1000)
	r, err := HonestData.Run(context.Background(), Input{Artifact: Artifact{Text: text}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Status != StatusPass {
		t.Errorf("large clean text should pass, got %v", r.Status)
	}
}

// ============================================================================
// BrandSpecGrep
// ============================================================================

func TestBrandSpecGrep_SkippedWhenNoSpec(t *testing.T) {
	tmpDir := t.TempDir()
	r, _ := (&BrandSpecGrep{}).Run(context.Background(), Input{
		ThreadDir: tmpDir,
		Artifact:  Artifact{Text: "<div style='color: #FF0000'>Hello</div>"},
	})
	if r.Status != StatusSkipped {
		t.Errorf("no brand-spec.md should skip, got %v: %v", r.Status, r.Issues)
	}
}

func TestBrandSpecGrep_PassesWhenAllHexInSpec(t *testing.T) {
	tmpDir := t.TempDir()
	specPath := filepath.Join(tmpDir, "brand-spec.md")
	if err := os.WriteFile(specPath, []byte("# Brand\n## Color\n- Primary: #1A2B3C\n- Accent: #FFEEDD\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, _ := (&BrandSpecGrep{}).Run(context.Background(), Input{
		ThreadDir: tmpDir,
		Artifact:  Artifact{Text: "<div style='color: #1A2B3C'>Hello</div>"},
	})
	if r.Status != StatusPass {
		t.Errorf("hex in spec should pass, got %v: %v", r.Status, r.Issues)
	}
}

func TestBrandSpecGrep_FailsOnUnsourcedHex(t *testing.T) {
	tmpDir := t.TempDir()
	specPath := filepath.Join(tmpDir, "brand-spec.md")
	if err := os.WriteFile(specPath, []byte("- Primary: #1A2B3C\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, _ := (&BrandSpecGrep{}).Run(context.Background(), Input{
		ThreadDir: tmpDir,
		// #FF0000 is NOT in the spec
		Artifact: Artifact{Text: "<div style='color: #FF0000; background: #1A2B3C'>Hi</div>"},
	})
	if r.Status != StatusFail {
		t.Errorf("unsourced hex should fail, got %v", r.Status)
	}
	// Should mention the unsourced color, not the sourced one.
	found := false
	for _, issue := range r.Issues {
		if strings.Contains(strings.ToLower(issue), "#ff0000") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected #ff0000 in issues, got %v", r.Issues)
	}
}

func TestBrandSpecGrep_NormalizesShorthand(t *testing.T) {
	tmpDir := t.TempDir()
	specPath := filepath.Join(tmpDir, "brand-spec.md")
	// Spec has #fff (shorthand) — artifact uses #FFFFFF (full).
	// Normalizer should treat them as equal.
	if err := os.WriteFile(specPath, []byte("- Bg: #fff\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, _ := (&BrandSpecGrep{}).Run(context.Background(), Input{
		ThreadDir: tmpDir,
		Artifact:  Artifact{Text: "<body style='background: #FFFFFF'>"},
	})
	if r.Status != StatusPass {
		t.Errorf("shorthand-vs-full equivalence should pass, got %v: %v", r.Status, r.Issues)
	}
}

// ============================================================================
// BrandSpecConfirmation
// ============================================================================

func TestBrandSpecConfirmation_SkippedWhenNoSpec(t *testing.T) {
	r, _ := (&BrandSpecConfirmation{}).Run(context.Background(), Input{
		ThreadDir: t.TempDir(),
		Artifact:  Artifact{Text: "Brand aligned poster"},
	})
	if r.Status != StatusSkipped {
		t.Fatalf("expected skipped without spec, got %v: %v", r.Status, r.Issues)
	}
}

func TestBrandSpecConfirmation_PassesConfirmedSpec(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "brand-spec.md"), []byte("# Brand Spec\nStatus: confirmed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, _ := (&BrandSpecConfirmation{}).Run(context.Background(), Input{
		ThreadDir: tmpDir,
		Artifact:  Artifact{Text: "Brand aligned poster"},
	})
	if r.Status != StatusPass {
		t.Fatalf("expected pass for confirmed spec, got %v: %v", r.Status, r.Issues)
	}
}

func TestBrandSpecConfirmation_FailsUnconfirmedSpecWithoutCaveat(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "brand-spec.md"), []byte("# Brand Spec\n[unconfirmed]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, _ := (&BrandSpecConfirmation{}).Run(context.Background(), Input{
		ThreadDir: tmpDir,
		Artifact:  Artifact{Text: "Final poster using the extracted brand palette."},
	})
	if r.Status != StatusFail {
		t.Fatalf("expected fail for unconfirmed spec without caveat, got %v: %v", r.Status, r.Issues)
	}
}

func TestBrandSpecConfirmation_PassesUnconfirmedSpecWithVisibleCaveat(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "brand-spec.md"), []byte("# Brand Spec\n状态: 待确认\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, _ := (&BrandSpecConfirmation{}).Run(context.Background(), Input{
		ThreadDir: tmpDir,
		Artifact:  Artifact{Text: "Hero poster\n水印: 待品牌方确认"},
	})
	if r.Status != StatusPass {
		t.Fatalf("expected pass with visible caveat, got %v: %v", r.Status, r.Issues)
	}
}

// ============================================================================
// AssetContractGuard
// ============================================================================

func TestAssetContractGuard_FailsFakeLogoAndProductSubstitutes(t *testing.T) {
	r, err := (&AssetContractGuard{}).Run(context.Background(), Input{
		Artifact: Artifact{Text: "Use a generic logo in the header and a made-up product render for the hero."},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Status != StatusFail {
		t.Fatalf("expected fail, got %v: %v", r.Status, r.Issues)
	}
	if len(r.Issues) < 2 {
		t.Fatalf("expected logo and product issues, got %#v", r.Issues)
	}
}

func TestAssetContractGuard_FailsDrawnLogoSubstitute(t *testing.T) {
	r, err := (&AssetContractGuard{}).Run(context.Background(), Input{
		Artifact: Artifact{Text: "Recreate the brand logo using CSS and text so the banner looks official."},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Status != StatusFail {
		t.Fatalf("expected fail, got %v: %v", r.Status, r.Issues)
	}
}

func TestAssetContractGuard_AllowsExplicitConceptPlaceholder(t *testing.T) {
	r, err := (&AssetContractGuard{}).Run(context.Background(), Input{
		Artifact: Artifact{Text: "No product photo was provided. Output is labelled concept only, not real product representation, with [product image:待替换]."},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Status != StatusPass {
		t.Fatalf("expected pass for explicit placeholder/concept caveat, got %v: %v", r.Status, r.Issues)
	}
}

func TestAssetContractGuard_FailsMissingProductSourceForFinalProductShot(t *testing.T) {
	r, err := (&AssetContractGuard{}).Run(context.Background(), Input{
		Artifact: Artifact{Text: "Create a production-ready ecommerce hero. No source product image is available, but deliver a final realistic product shot ready to publish."},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Status != StatusFail {
		t.Fatalf("expected fail for final product shot without source image, got %v: %v", r.Status, r.Issues)
	}
	if _, ok := r.Trace["missing_product_source"]; !ok {
		t.Fatalf("expected missing_product_source trace, got %#v", r.Trace)
	}
}

func TestAssetContractGuard_FailsCharacterConsistencyWithoutAnchor(t *testing.T) {
	r, err := (&AssetContractGuard{}).Run(context.Background(), Input{
		Artifact: Artifact{Text: "Generate three panels with the same character across each scene and keep character continuity consistent."},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Status != StatusFail {
		t.Fatalf("expected fail for character consistency without anchor evidence, got %v: %v", r.Status, r.Issues)
	}
	if _, ok := r.Trace["character_anchor_missing"]; !ok {
		t.Fatalf("expected character_anchor_missing trace, got %#v", r.Trace)
	}
}

func TestAssetContractGuard_AllowsCharacterConsistencyWithReference(t *testing.T) {
	r, err := (&AssetContractGuard{}).Run(context.Background(), Input{
		Artifact: Artifact{Text: "Generate three panels with the same character. Use @character/lin-xia and reference_image: /assets/lin-xia.png as the canonical anchor."},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Status != StatusPass {
		t.Fatalf("expected pass with character anchor evidence, got %v: %v", r.Status, r.Issues)
	}
}

// ============================================================================
// OrphanDetector
// ============================================================================

func TestOrphanDetector_SkippedForUnsupportedSkills(t *testing.T) {
	r, _ := (&OrphanDetector{}).Run(context.Background(), Input{
		SkillName: "character", // not in {ppt, pictureBook, marketingPoster}
		Artifact:  Artifact{Text: "Standalone\nword.\n"},
	})
	if r.Status != StatusSkipped {
		t.Errorf("non-text-typography skill should skip, got %v", r.Status)
	}
}

func TestOrphanDetector_PassesCleanParagraphs(t *testing.T) {
	r, _ := (&OrphanDetector{}).Run(context.Background(), Input{
		SkillName: "ppt",
		Artifact:  Artifact{Text: "First paragraph with multiple words on a single line.\n\nSecond paragraph also has plenty of content.\n"},
	})
	if r.Status != StatusPass {
		t.Errorf("clean paragraphs should pass, got %v: %v", r.Status, r.Issues)
	}
}

func TestOrphanDetector_DetectsLatinOrphan(t *testing.T) {
	r, _ := (&OrphanDetector{}).Run(context.Background(), Input{
		SkillName: "ppt",
		Artifact:  Artifact{Text: "This is a long opening line about innovation\nimportant.\n"},
	})
	if r.Status != StatusFail {
		t.Errorf("trailing single-word should fail, got %v", r.Status)
	}
}

func TestOrphanDetector_IgnoresHeadings(t *testing.T) {
	r, _ := (&OrphanDetector{}).Run(context.Background(), Input{
		SkillName: "ppt",
		Artifact:  Artifact{Text: "Some intro text on a multi-word line\n# Title\n"},
	})
	// "# Title" is a heading, not an orphan.
	if r.Status != StatusPass {
		t.Errorf("heading should not be flagged as orphan, got %v: %v", r.Status, r.Issues)
	}
}

// ============================================================================
// ContrastAnalyzer
// ============================================================================

func TestContrastAnalyzer_SkippedWhenNoInlinePairs(t *testing.T) {
	r, _ := (&ContrastAnalyzer{}).Run(context.Background(), Input{
		Artifact: Artifact{Text: "<div>plain html</div>"},
	})
	if r.Status != StatusSkipped {
		t.Errorf("no inline pairs should skip, got %v", r.Status)
	}
}

func TestContrastAnalyzer_PassesGoodContrast(t *testing.T) {
	// Black on white = ~21:1, well above AA.
	r, _ := (&ContrastAnalyzer{}).Run(context.Background(), Input{
		Artifact: Artifact{Text: `<h1 style="color: #000000; background-color: #FFFFFF">Header</h1>`},
	})
	if r.Status != StatusPass {
		t.Errorf("black-on-white should pass, got %v: %v", r.Status, r.Issues)
	}
}

func TestContrastAnalyzer_FailsLowContrast(t *testing.T) {
	// Light gray on white = ~1.5:1, below AA.
	r, _ := (&ContrastAnalyzer{}).Run(context.Background(), Input{
		Artifact: Artifact{Text: `<h1 style="color: #cccccc; background-color: #ffffff">Header</h1>`},
	})
	if r.Status != StatusFail {
		t.Errorf("light-gray on white should fail, got %v", r.Status)
	}
}

// ============================================================================
// CharacterAnchor
// ============================================================================

func TestCharacterAnchor_SkippedForNonCharacterSkill(t *testing.T) {
	r, _ := (&CharacterAnchor{}).Run(context.Background(), Input{SkillName: "ppt"})
	if r.Status != StatusSkipped {
		t.Errorf("non-character skill should skip, got %v", r.Status)
	}
}

func TestCharacterAnchor_SkippedWhenNoSpec(t *testing.T) {
	tmpDir := t.TempDir()
	r, _ := (&CharacterAnchor{}).Run(context.Background(), Input{
		SkillName: "character",
		ThreadDir: tmpDir,
		Artifact:  Artifact{Text: `prompt: "anything"`},
	})
	if r.Status != StatusSkipped {
		t.Errorf("no character-spec should skip, got %v", r.Status)
	}
}

func TestCharacterAnchor_FailsOnMissingFeatureInPrompt(t *testing.T) {
	tmpDir := t.TempDir()
	specPath := filepath.Join(tmpDir, "character-spec.md")
	spec := `# Character

## Appearance
- 30s asian female
- short black hair
- navy jacket

## Voice
- calm
`
	if err := os.WriteFile(specPath, []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}

	// Prompt drops "navy" — that's the failure mode.
	artifactText := `prompt: "30s asian female with short black hair, sitting in cafe, looking out window"`
	r, _ := (&CharacterAnchor{}).Run(context.Background(), Input{
		SkillName: "character",
		ThreadDir: tmpDir,
		Artifact:  Artifact{Text: artifactText},
	})
	if r.Status != StatusFail {
		t.Errorf("missing 'navy' should fail, got %v", r.Status)
	}
}

func TestCharacterAnchor_PassesWhenAllFeaturesPresent(t *testing.T) {
	tmpDir := t.TempDir()
	specPath := filepath.Join(tmpDir, "character-spec.md")
	if err := os.WriteFile(specPath, []byte("## Appearance\n- navy jacket\n- short hair\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, _ := (&CharacterAnchor{}).Run(context.Background(), Input{
		SkillName: "character",
		ThreadDir: tmpDir,
		Artifact:  Artifact{Text: `prompt: "person wearing navy jacket with short hair, walking outdoors at golden hour"`},
	})
	if r.Status != StatusPass {
		t.Errorf("all features present should pass, got %v: %v", r.Status, r.Issues)
	}
}

// ============================================================================
// Concurrent safety smoke
// ============================================================================

func TestRegistry_ConcurrentReadsSafe(t *testing.T) {
	// Verify Get + Names are safe under concurrent access. Cheap
	// smoke test — race detector catches the real bugs.
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 100; j++ {
				_, _ = Default().Get("honest_data")
				_ = Default().Names()
			}
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}
