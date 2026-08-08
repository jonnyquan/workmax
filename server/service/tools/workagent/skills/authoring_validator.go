package skills

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

// ValidateOfficialSkill is the authoring-time validator for one
// embedded official skill. It complements parseManifest's field-level
// checks with bundle-wide checks that need the skill directory: manifest
// name/trigger alignment and assets/ side-file safety.
func ValidateOfficialSkill(name string) error {
	if name == "" {
		return fmt.Errorf("skill name is required")
	}
	if strings.HasPrefix(name, "_") {
		return fmt.Errorf("skill %q is not an official user-facing skill directory", name)
	}

	manifestPath := path.Join(name, "skill.yaml")
	data, err := skillsFS.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", manifestPath, err)
	}
	manifest, err := parseManifest(data)
	if err != nil {
		return fmt.Errorf("skill %q: %w", name, err)
	}
	if err := validateManifestForSkillDir(name, manifest); err != nil {
		return fmt.Errorf("skill %q: skill.yaml: %w", name, err)
	}
	if err := validateSkillManifestFiles(name, manifest); err != nil {
		return fmt.Errorf("skill %q: skill.yaml: %w", name, err)
	}
	if err := validateCritiqueAnchorChecklistCoverage(name, manifest); err != nil {
		return fmt.Errorf("skill %q: references/checklist.md: %w", name, err)
	}
	if err := validateHTMLChecklistCoverage(name, manifest); err != nil {
		return fmt.Errorf("skill %q: references/checklist.md: %w", name, err)
	}
	if err := validateMotionChecklistCoverage(name, manifest); err != nil {
		return fmt.Errorf("skill %q: references/checklist.md: %w", name, err)
	}
	if err := validateSkillQuestionFormI18nNamespace(name, manifest); err != nil {
		return fmt.Errorf("skill %q: skill.yaml: %w", name, err)
	}
	if err := validateSkillScriptsDir(name, manifest); err != nil {
		return fmt.Errorf("skill %q: %w", name, err)
	}
	if err := validateSkillSideFiles(name); err != nil {
		return fmt.Errorf("skill %q: %w", name, err)
	}
	return nil
}

// ValidateAllOfficialSkills walks the embedded skills tree and validates
// every official bundle. Use this in contract tests and in future
// pre-deploy checks; runtime request paths should use Loader/Registry.
func ValidateAllOfficialSkills() error {
	entries, err := fs.ReadDir(skillsFS, ".")
	if err != nil {
		return fmt.Errorf("read skills root: %w", err)
	}
	var errs []error
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), "_") {
			continue
		}
		if err := ValidateOfficialSkill(entry.Name()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func validateSkillManifestFiles(skillName string, manifest *Manifest) error {
	if manifest == nil {
		return fmt.Errorf("manifest is nil")
	}
	var errs []error
	for i, ref := range manifest.References.Shared {
		if err := validateAuthoringFileExists(path.Join("_shared", ref+".md")); err != nil {
			errs = append(errs, fmt.Errorf("references.shared[%d] %q: %w", i, ref, err))
		}
	}
	for i, ref := range manifest.References.Local {
		if err := validateAuthoringFileExists(path.Join(skillName, "references", ref+".md")); err != nil {
			errs = append(errs, fmt.Errorf("references.local[%d] %q: %w", i, ref, err))
		}
	}
	for i, script := range manifest.Scripts {
		if !strings.HasPrefix(script.Path, "scripts/") {
			errs = append(errs, fmt.Errorf("scripts[%d].path %q must be under scripts/", i, script.Path))
			continue
		}
		if path.Ext(script.Path) != ".py" {
			errs = append(errs, fmt.Errorf("scripts[%d].path %q must point to a .py script", i, script.Path))
			continue
		}
		if err := validateAuthoringFileExists(path.Join(skillName, script.Path)); err != nil {
			errs = append(errs, fmt.Errorf("scripts[%d].path %q: %w", i, script.Path, err))
		}
	}
	return errors.Join(errs...)
}

func validateSkillQuestionFormI18nNamespace(skillName string, manifest *Manifest) error {
	if manifest == nil {
		return nil
	}
	prefix := "form." + skillName + "."
	var errs []error
	if manifest.QuestionForm != nil && manifest.QuestionForm.Enabled {
		if err := validateSkillScopedFormKey("question_form.skip_label_key", manifest.QuestionForm.SkipKey, prefix); err != nil {
			errs = append(errs, err)
		}
		for i, question := range manifest.QuestionForm.Questions {
			if err := validateSkillScopedFormKey(fmt.Sprintf("question_form.questions[%d].label_key", i), question.LabelKey, prefix); err != nil {
				errs = append(errs, err)
			}
			for j, option := range question.Options {
				if err := validateSkillScopedFormKey(fmt.Sprintf("question_form.questions[%d].options[%d].label_key", i, j), option.LabelKey, prefix); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	if manifest.DirectionsFallback != nil && manifest.DirectionsFallback.Enabled {
		if err := validateSkillScopedFormKey("directions_fallback.skip_label_key", manifest.DirectionsFallback.SkipKey, prefix); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func validateSkillScopedFormKey(field string, key string, skillPrefix string) error {
	if key == "" || key == "form.skip" {
		return nil
	}
	if !strings.HasPrefix(key, skillPrefix) {
		return fmt.Errorf("%s %q must start with %q or equal \"form.skip\"", field, key, skillPrefix)
	}
	return nil
}

func validateSkillScriptsDir(skillName string, manifest *Manifest) error {
	if manifest == nil {
		return fmt.Errorf("manifest is nil")
	}
	dir := path.Join(skillName, "scripts")
	entries, err := fs.ReadDir(skillsFS, dir)
	if err != nil {
		return nil // scripts/ is optional
	}
	declared := map[string]bool{}
	for _, script := range manifest.Scripts {
		declared[script.Path] = true
	}
	var errs []error
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			errs = append(errs, fmt.Errorf("scripts/%s must be a file; nested script directories are not supported", name))
			continue
		}
		relPath := path.Join("scripts", name)
		if !declared[relPath] {
			errs = append(errs, fmt.Errorf("scripts/%s must be declared in skill.yaml scripts[]", name))
		}
	}
	return errors.Join(errs...)
}

func validateAuthoringFileExists(filePath string) error {
	info, err := fs.Stat(skillsFS, filePath)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("must be a file")
	}
	if info.Size() == 0 {
		return fmt.Errorf("must not be empty")
	}
	return nil
}

var critiqueAnchorChecklistKeywords = map[string][]string{
	"brand_fit":  {"brand_fit", "brand fit", "brand_color", "brand spec", "brand-spec", "brand assets"},
	"compliance": {"compliance", "policy", "platform", "file-size", "destination"},
	"fidelity":   {"fidelity", "reference", "preserve", "source of truth"},
}

func validateCritiqueAnchorChecklistCoverage(skillName string, manifest *Manifest) error {
	if manifest == nil || manifest.Artifacts == nil {
		return nil
	}
	var required []string
	for _, anchor := range manifest.Artifacts.CritiqueAnchors {
		if _, ok := critiqueAnchorChecklistKeywords[anchor]; ok {
			required = append(required, anchor)
		}
	}
	if len(required) == 0 {
		return nil
	}

	filePath := path.Join(skillName, "references", "checklist.md")
	data, err := skillsFS.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("required for critique_anchors %s: %w", strings.Join(required, ", "), err)
	}
	content := strings.ToLower(string(data))
	if !strings.Contains(content, "## p0") && !strings.Contains(content, "## p1") {
		return fmt.Errorf("must include P0 or P1 sections for critique_anchors %s", strings.Join(required, ", "))
	}
	priorityContent := priorityChecklistContent(content)
	if strings.TrimSpace(priorityContent) == "" {
		return fmt.Errorf("must include P0 or P1 checklist content for critique_anchors %s", strings.Join(required, ", "))
	}

	var errs []error
	for _, anchor := range required {
		if !containsAny(priorityContent, critiqueAnchorChecklistKeywords[anchor]) {
			errs = append(errs, fmt.Errorf("must cover critique anchor %q in a P0/P1 checklist item", anchor))
		}
	}
	return errors.Join(errs...)
}

var htmlChecklistKeywordGroups = map[string][]string{
	"viewport":                {"viewport"},
	"visible text":            {"visible text", "readable text"},
	"external script":         {"external script", "external scripts"},
	"iframe":                  {"iframe"},
	"responsive/export ready": {"responsive_export_readiness", "responsive export", "export readiness", "export-ready", "export ready"},
}

func validateHTMLChecklistCoverage(skillName string, manifest *Manifest) error {
	if manifest == nil || manifest.Artifacts == nil || !artifactMatrixContains(manifest.Artifacts, "html") {
		return nil
	}
	filePath := path.Join(skillName, "references", "checklist.md")
	data, err := skillsFS.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("required for html output: %w", err)
	}
	content := strings.ToLower(string(data))
	priorityContent := priorityChecklistContent(content)
	if strings.TrimSpace(priorityContent) == "" {
		return fmt.Errorf("must include P0 or P1 checklist content for html output")
	}

	var errs []error
	for label, keywords := range htmlChecklistKeywordGroups {
		if !containsAny(priorityContent, keywords) {
			errs = append(errs, fmt.Errorf("must cover %s for html output in a P0/P1 checklist item", label))
		}
	}
	return errors.Join(errs...)
}

var motionChecklistKeywords = []string{
	"motion pacing",
	"motion_timeline",
	"reduced-motion",
	"reduced motion",
	"static fallback",
}

func validateMotionChecklistCoverage(skillName string, manifest *Manifest) error {
	if manifest == nil || manifest.Artifacts == nil || (!artifactMatrixContains(manifest.Artifacts, "mp4") && !artifactMatrixContains(manifest.Artifacts, "gif")) {
		return nil
	}
	filePath := path.Join(skillName, "references", "checklist.md")
	data, err := skillsFS.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("required for motion output: %w", err)
	}
	content := strings.ToLower(string(data))
	priorityContent := priorityChecklistContent(content)
	if strings.TrimSpace(priorityContent) == "" {
		return fmt.Errorf("must include P0 or P1 checklist content for motion output")
	}
	if !containsAny(priorityContent, motionChecklistKeywords) {
		return fmt.Errorf("must cover motion pacing, reduced-motion, or static fallback for motion output in a P0/P1 checklist item")
	}
	return nil
}

func artifactMatrixContains(artifacts *ArtifactMetadata, value string) bool {
	if artifacts == nil {
		return false
	}
	for _, outputType := range artifacts.OutputTypes {
		if outputType == value {
			return true
		}
	}
	for _, exportTarget := range artifacts.ExportTargets {
		if exportTarget == value {
			return true
		}
	}
	return false
}

func priorityChecklistContent(content string) string {
	lines := strings.Split(content, "\n")
	var b strings.Builder
	inPriority := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			title := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(trimmed, "## ")))
			inPriority = strings.HasPrefix(title, "p0") || strings.HasPrefix(title, "p1")
			continue
		}
		if inPriority {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func containsAny(content string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(content, needle) {
			return true
		}
	}
	return false
}

func validateSkillSideFiles(skillName string) error {
	dir := path.Join(skillName, "assets")
	entries, err := fs.ReadDir(skillsFS, dir)
	if err != nil {
		return nil // assets/ is optional
	}
	var errs []error
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			errs = append(errs, fmt.Errorf("assets/%s must be a file; nested asset directories are not supported", name))
			continue
		}
		full := path.Join(dir, name)
		data, err := skillsFS.ReadFile(full)
		if err != nil {
			errs = append(errs, fmt.Errorf("read assets/%s: %w", name, err))
			continue
		}
		if err := validateSideFileForAuthoring(name, data); err != nil {
			errs = append(errs, fmt.Errorf("assets/%s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}
