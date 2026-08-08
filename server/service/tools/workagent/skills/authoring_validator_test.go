package skills

import (
	"strings"
	"testing"
)

func TestValidateAllOfficialSkills(t *testing.T) {
	if err := ValidateAllOfficialSkills(); err != nil {
		t.Fatalf("official skills should validate: %v", err)
	}
}

func TestValidateOfficialSkill_RejectsSharedDirectory(t *testing.T) {
	err := ValidateOfficialSkill("_shared")
	if err == nil || !strings.Contains(err.Error(), "not an official") {
		t.Fatalf("expected shared directory rejection, got %v", err)
	}
}

func TestValidateOfficialSkill_RejectsMissingManifest(t *testing.T) {
	err := ValidateOfficialSkill("imaginary")
	if err == nil || !strings.Contains(err.Error(), "skill.yaml") {
		t.Fatalf("expected missing manifest error, got %v", err)
	}
}

func TestValidateSkillManifestFilesRejectsMissingSharedReference(t *testing.T) {
	manifest := &Manifest{}
	manifest.References.Shared = []string{"missing-shared-reference"}

	err := validateSkillManifestFiles("ppt", manifest)
	if err == nil || !strings.Contains(err.Error(), "references.shared[0]") {
		t.Fatalf("expected missing shared reference error, got %v", err)
	}
}

func TestValidateSkillManifestFilesRejectsMissingLocalReference(t *testing.T) {
	manifest := &Manifest{}
	manifest.References.Local = []string{"missing-local-reference"}

	err := validateSkillManifestFiles("ppt", manifest)
	if err == nil || !strings.Contains(err.Error(), "references.local[0]") {
		t.Fatalf("expected missing local reference error, got %v", err)
	}
}

func TestValidateSkillManifestFilesRejectsScriptOutsideScriptsDirectory(t *testing.T) {
	manifest := &Manifest{}
	manifest.Scripts = []ScriptDescriptor{{Name: "validate", Path: "assets/template-shell.md", When: "post_generation"}}

	err := validateSkillManifestFiles("ppt", manifest)
	if err == nil || !strings.Contains(err.Error(), "must be under scripts/") {
		t.Fatalf("expected script directory error, got %v", err)
	}
}

func TestValidateSkillManifestFilesRejectsNonPythonScripts(t *testing.T) {
	manifest := &Manifest{}
	manifest.Scripts = []ScriptDescriptor{{Name: "validate-html", Path: "scripts/validate-html.sh", When: "post_generation"}}

	err := validateSkillManifestFiles("ppt", manifest)
	if err == nil || !strings.Contains(err.Error(), "must point to a .py script") {
		t.Fatalf("expected script extension error, got %v", err)
	}
}

func TestValidateSkillManifestFilesAllowsDeclaredPPTScript(t *testing.T) {
	manifest := &Manifest{}
	manifest.References.Local = []string{"checklist"}
	manifest.Scripts = []ScriptDescriptor{{Name: "validate-pptx", Path: "scripts/validate-pptx.py", When: "post_generation"}}

	if err := validateSkillManifestFiles("ppt", manifest); err != nil {
		t.Fatalf("expected existing local reference and script to validate, got %v", err)
	}
}

func TestValidateSkillQuestionFormI18nNamespaceRejectsCrossSkillQuestionKeys(t *testing.T) {
	manifest := &Manifest{QuestionForm: &QuestionForm{
		Enabled: true,
		Questions: []QuestionFormField{{
			ID:       "audience",
			LabelKey: "form.logo.audience.label",
			Options:  []QuestionFormOption{{Value: "exec", LabelKey: "form.ppt.audience.exec"}},
		}},
	}}

	err := validateSkillQuestionFormI18nNamespace("ppt", manifest)
	if err == nil || !strings.Contains(err.Error(), `label_key "form.logo.audience.label" must start with "form.ppt."`) {
		t.Fatalf("expected question namespace error, got %v", err)
	}
}

func TestValidateSkillQuestionFormI18nNamespaceRejectsCrossSkillSkipKey(t *testing.T) {
	manifest := &Manifest{QuestionForm: &QuestionForm{
		Enabled: true,
		SkipKey: "form.logo.skip",
		Questions: []QuestionFormField{{
			ID:       "audience",
			LabelKey: "form.ppt.audience.label",
			Options:  []QuestionFormOption{{Value: "exec", LabelKey: "form.ppt.audience.exec"}},
		}},
	}}

	err := validateSkillQuestionFormI18nNamespace("ppt", manifest)
	if err == nil || !strings.Contains(err.Error(), `question_form.skip_label_key "form.logo.skip" must start with "form.ppt." or equal "form.skip"`) {
		t.Fatalf("expected skip namespace error, got %v", err)
	}
}

func TestValidateSkillQuestionFormI18nNamespaceRejectsCrossSkillOptionKeys(t *testing.T) {
	manifest := &Manifest{QuestionForm: &QuestionForm{
		Enabled: true,
		Questions: []QuestionFormField{{
			ID:       "audience",
			LabelKey: "form.ppt.audience.label",
			Options:  []QuestionFormOption{{Value: "exec", LabelKey: "form.logo.audience.exec"}},
		}},
	}}

	err := validateSkillQuestionFormI18nNamespace("ppt", manifest)
	if err == nil || !strings.Contains(err.Error(), `options[0].label_key "form.logo.audience.exec" must start with "form.ppt."`) {
		t.Fatalf("expected option namespace error, got %v", err)
	}
}

func TestValidateSkillQuestionFormI18nNamespaceAllowsSkillScopedKeys(t *testing.T) {
	manifest := &Manifest{QuestionForm: &QuestionForm{
		Enabled: true,
		Questions: []QuestionFormField{{
			ID:       "audience",
			LabelKey: "form.ppt.audience.label",
			Options:  []QuestionFormOption{{Value: "exec", LabelKey: "form.ppt.audience.exec"}},
		}},
	}}

	if err := validateSkillQuestionFormI18nNamespace("ppt", manifest); err != nil {
		t.Fatalf("expected skill-scoped form keys to validate, got %v", err)
	}
}

func TestValidateSkillQuestionFormI18nNamespaceAllowsSharedSkipKey(t *testing.T) {
	manifest := &Manifest{
		QuestionForm: &QuestionForm{
			Enabled: true,
			SkipKey: "form.skip",
			Questions: []QuestionFormField{{
				ID:       "audience",
				LabelKey: "form.ppt.audience.label",
				Options:  []QuestionFormOption{{Value: "exec", LabelKey: "form.ppt.audience.exec"}},
			}},
		},
		DirectionsFallback: &DirectionsFallback{
			Enabled: true,
			SkipKey: "form.skip",
		},
	}

	if err := validateSkillQuestionFormI18nNamespace("ppt", manifest); err != nil {
		t.Fatalf("expected shared skip key to validate, got %v", err)
	}
}

func TestValidateSkillQuestionFormI18nNamespaceRejectsCrossSkillDirectionsSkipKey(t *testing.T) {
	manifest := &Manifest{DirectionsFallback: &DirectionsFallback{
		Enabled: true,
		SkipKey: "form.logo.skip",
	}}

	err := validateSkillQuestionFormI18nNamespace("ppt", manifest)
	if err == nil || !strings.Contains(err.Error(), `directions_fallback.skip_label_key "form.logo.skip" must start with "form.ppt." or equal "form.skip"`) {
		t.Fatalf("expected directions fallback skip namespace error, got %v", err)
	}
}

func TestValidateCritiqueAnchorChecklistCoverageRejectsMissingChecklist(t *testing.T) {
	manifest := &Manifest{Artifacts: &ArtifactMetadata{CritiqueAnchors: []string{"brand_fit"}}}

	err := validateCritiqueAnchorChecklistCoverage("logo", manifest)
	if err == nil || !strings.Contains(err.Error(), "required for critique_anchors brand_fit") {
		t.Fatalf("expected missing high-risk checklist error, got %v", err)
	}
}

func TestPriorityChecklistContentExcludesP2Coverage(t *testing.T) {
	content := strings.ToLower(`
## P0
- Verify layout basics.

## P2
- Check brand fit and source of truth once the draft is otherwise stable.
`)

	got := priorityChecklistContent(content)
	if strings.Contains(got, "brand fit") || strings.Contains(got, "source of truth") {
		t.Fatalf("priority checklist content = %q, should exclude P2-only coverage", got)
	}
	if !strings.Contains(got, "layout basics") {
		t.Fatalf("priority checklist content = %q, should include P0 content", got)
	}
}

func TestValidateCritiqueAnchorChecklistCoverageAllowsBrandFitChecklist(t *testing.T) {
	manifest := &Manifest{Artifacts: &ArtifactMetadata{CritiqueAnchors: []string{"brand_fit"}}}

	if err := validateCritiqueAnchorChecklistCoverage("productShot", manifest); err != nil {
		t.Fatalf("expected productShot brand-fit checklist to validate, got %v", err)
	}
}

func TestValidateCritiqueAnchorChecklistCoverageAllowsFidelityChecklist(t *testing.T) {
	manifest := &Manifest{Artifacts: &ArtifactMetadata{CritiqueAnchors: []string{"fidelity"}}}

	if err := validateCritiqueAnchorChecklistCoverage("modelTryOn", manifest); err != nil {
		t.Fatalf("expected modelTryOn fidelity checklist to validate, got %v", err)
	}
}

func TestValidateHTMLChecklistCoverageRejectsMissingChecklist(t *testing.T) {
	manifest := &Manifest{Artifacts: &ArtifactMetadata{OutputTypes: []string{"html"}}}

	err := validateHTMLChecklistCoverage("logo", manifest)
	if err == nil || !strings.Contains(err.Error(), "required for html output") {
		t.Fatalf("expected missing HTML checklist error, got %v", err)
	}
}

func TestValidateHTMLChecklistCoverageRejectsIncompleteChecklist(t *testing.T) {
	manifest := &Manifest{Artifacts: &ArtifactMetadata{OutputTypes: []string{"html"}}}

	err := validateHTMLChecklistCoverage("productShot", manifest)
	if err == nil || !strings.Contains(err.Error(), "external script") {
		t.Fatalf("expected incomplete HTML checklist error, got %v", err)
	}
}

func TestValidateHTMLChecklistCoverageAllowsHTMLNativeChecklist(t *testing.T) {
	manifest := &Manifest{Artifacts: &ArtifactMetadata{OutputTypes: []string{"html"}}}

	if err := validateHTMLChecklistCoverage("webBanner", manifest); err != nil {
		t.Fatalf("expected webBanner HTML checklist to validate, got %v", err)
	}
}

func TestValidateMotionChecklistCoverageRejectsMissingChecklist(t *testing.T) {
	manifest := &Manifest{Artifacts: &ArtifactMetadata{OutputTypes: []string{"mp4"}}}

	err := validateMotionChecklistCoverage("logo", manifest)
	if err == nil || !strings.Contains(err.Error(), "required for motion output") {
		t.Fatalf("expected missing motion checklist error, got %v", err)
	}
}

func TestValidateMotionChecklistCoverageRejectsP2OnlyMotionChecklist(t *testing.T) {
	manifest := &Manifest{Artifacts: &ArtifactMetadata{OutputTypes: []string{"gif"}}}

	err := validateMotionChecklistCoverage("flashCard", manifest)
	if err == nil || !strings.Contains(err.Error(), "must cover motion pacing") {
		t.Fatalf("expected missing motion checklist coverage error, got %v", err)
	}
}

func TestValidateMotionChecklistCoverageAllowsMotionChecklist(t *testing.T) {
	manifest := &Manifest{Artifacts: &ArtifactMetadata{OutputTypes: []string{"mp4"}}}

	if err := validateMotionChecklistCoverage("mobileStory", manifest); err != nil {
		t.Fatalf("expected mobileStory motion checklist to validate, got %v", err)
	}
}

func TestValidateSkillScriptsDirRejectsUndeclaredScripts(t *testing.T) {
	manifest := &Manifest{}

	err := validateSkillScriptsDir("ppt", manifest)
	if err == nil || !strings.Contains(err.Error(), "must be declared in skill.yaml scripts[]") {
		t.Fatalf("expected undeclared script error, got %v", err)
	}
}

func TestValidateSkillScriptsDirAllowsDeclaredScripts(t *testing.T) {
	manifest := &Manifest{}
	manifest.Scripts = []ScriptDescriptor{{Name: "validate-pptx", Path: "scripts/validate-pptx.py", When: "post_generation"}}

	if err := validateSkillScriptsDir("ppt", manifest); err != nil {
		t.Fatalf("expected declared script directory to validate, got %v", err)
	}
}

func TestValidateArtifactsRejectsDuplicateOutputTypes(t *testing.T) {
	err := validateArtifacts(&ArtifactMetadata{
		PrimaryType:     "poster",
		OutputTypes:     []string{"png", "png"},
		PreviewTypes:    []string{"image"},
		ExportTargets:   []string{"png"},
		CritiqueAnchors: []string{"craft"},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("expected duplicate output type error, got %v", err)
	}
}

func TestValidateArtifactsRejectsPreviewWithoutCompatibleOutput(t *testing.T) {
	err := validateArtifacts(&ArtifactMetadata{
		PrimaryType:     "poster",
		OutputTypes:     []string{"markdown"},
		PreviewTypes:    []string{"image"},
		ExportTargets:   []string{"markdown"},
		CritiqueAnchors: []string{"craft"},
	})
	if err == nil || !strings.Contains(err.Error(), "not supported by output_types") {
		t.Fatalf("expected preview/output consistency error, got %v", err)
	}
}

func TestValidateArtifactsRejectsExportTargetWithoutOutputType(t *testing.T) {
	err := validateArtifacts(&ArtifactMetadata{
		PrimaryType:     "poster",
		OutputTypes:     []string{"html", "markdown"},
		PreviewTypes:    []string{"html", "markdown"},
		ExportTargets:   []string{"png"},
		CritiqueAnchors: []string{"craft"},
	})
	if err == nil || !strings.Contains(err.Error(), "must also be declared in output_types") {
		t.Fatalf("expected export/output consistency error, got %v", err)
	}
}

func TestValidateArtifactsAllowsZipWithoutOutputType(t *testing.T) {
	err := validateArtifacts(&ArtifactMetadata{
		PrimaryType:     "poster",
		OutputTypes:     []string{"html", "markdown"},
		PreviewTypes:    []string{"html", "markdown"},
		ExportTargets:   []string{"html", "zip"},
		CritiqueAnchors: []string{"craft"},
	})
	if err != nil {
		t.Fatalf("expected zip package target to validate without zip output type, got %v", err)
	}
}
