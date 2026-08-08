package skills

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	designSystemHexColorRE = regexp.MustCompile(`#[0-9a-fA-F]{6}\b`)
	designSystemOKLchRE    = regexp.MustCompile(`oklch\([^)]*\)`)
	designSystemDurationRE = regexp.MustCompile(`\b(\d+)\s*ms\b`)
	designSystemEasingRE   = regexp.MustCompile(`(?i)\b(?:ease(?:-in|-out|-in-out)?|linear|cubic-bezier|snap|fade)\b`)
)

var requiredDesignSystemSections = []string{
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

// ValidateAllDesignSystems validates every shipped design-system file.
// This is an authoring-time guard: runtime lookup remains tolerant, but
// official catalog updates should fail tests when the 9-section schema,
// color tokens, font fallbacks, or motion tokens drift.
func ValidateAllDesignSystems() error {
	var errs []error
	for _, name := range AvailableDesignSystems() {
		ds, err := LoadDesignSystem(name)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		if ds == nil {
			errs = append(errs, fmt.Errorf("%s: missing design system", name))
			continue
		}
		if err := validateDesignSystem(ds); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ValidateDesignSystemMarkdown validates a single design-system body
// outside the shipped catalog. Project-local design-system candidates
// use this before confirmation so reusable systems meet the same shape
// as embedded starters.
func ValidateDesignSystemMarkdown(basename string, body string) error {
	return validateDesignSystem(&DesignSystem{
		Basename: basename,
		Body:     body,
	})
}

func validateDesignSystem(ds *DesignSystem) error {
	if ds == nil {
		return fmt.Errorf("design system is nil")
	}
	var errs []error
	if ds.Basename == "" {
		errs = append(errs, fmt.Errorf("design system basename is required"))
	} else if err := validateRelPath("design system basename", ds.Basename+".md", maxScriptPathLen); err != nil {
		errs = append(errs, err)
	}
	if strings.TrimSpace(ds.Body) == "" {
		errs = append(errs, fmt.Errorf("%s: body is empty", ds.Basename))
		return errors.Join(errs...)
	}
	for _, section := range requiredDesignSystemSections {
		if !strings.Contains(ds.Body, section) {
			errs = append(errs, fmt.Errorf("%s: missing %q", ds.Basename, section))
		}
	}
	if err := validateDesignSystemColors(ds); err != nil {
		errs = append(errs, err)
	}
	if err := validateDesignSystemFonts(ds); err != nil {
		errs = append(errs, err)
	}
	if err := validateDesignSystemMotion(ds); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func validateDesignSystemColors(ds *DesignSystem) error {
	color := markdownSection(ds.Body, "## 1. Color", "## 2. Typography")
	if color == "" {
		return fmt.Errorf("%s: color section is empty", ds.Basename)
	}
	if len(designSystemOKLchRE.FindAllString(color, -1)) < 4 {
		return fmt.Errorf("%s: color section must declare at least 4 OKLch tokens", ds.Basename)
	}
	if len(designSystemHexColorRE.FindAllString(color, -1)) < 4 {
		return fmt.Errorf("%s: color section must declare at least 4 hex tokens", ds.Basename)
	}
	return nil
}

func validateDesignSystemFonts(ds *DesignSystem) error {
	typography := markdownSection(ds.Body, "## 2. Typography", "## 3. Spacing")
	if typography == "" {
		return fmt.Errorf("%s: typography section is empty", ds.Basename)
	}
	var errs []error
	for _, role := range []string{"Display", "Body", "Mono"} {
		line := findDesignSystemRoleLine(typography, role)
		if line == "" {
			errs = append(errs, fmt.Errorf("%s: typography missing %s role", ds.Basename, role))
			continue
		}
		if !hasFontFallback(line, role) {
			errs = append(errs, fmt.Errorf("%s: typography %s role lacks a generic fallback", ds.Basename, role))
		}
	}
	return errors.Join(errs...)
}

func validateDesignSystemMotion(ds *DesignSystem) error {
	motion := markdownSection(ds.Body, "## 6. Motion", "## 7. Voice")
	if motion == "" {
		return fmt.Errorf("%s: motion section is empty", ds.Basename)
	}
	var errs []error
	durations := map[string]int{}
	for _, token := range []string{"Fast", "Default", "Slow"} {
		line := findDesignSystemRoleLine(motion, token)
		if line == "" {
			errs = append(errs, fmt.Errorf("%s: motion missing %s token", ds.Basename, token))
			continue
		}
		duration, ok := designSystemMotionDuration(line)
		if !ok {
			errs = append(errs, fmt.Errorf("%s: motion %s token must use ms duration", ds.Basename, token))
			continue
		}
		durations[token] = duration
	}
	if len(durations) == 3 && !(durations["Fast"] < durations["Default"] && durations["Default"] < durations["Slow"]) {
		errs = append(errs, fmt.Errorf("%s: motion durations must increase Fast < Default < Slow", ds.Basename))
	}
	if !designSystemEasingRE.MatchString(motion) {
		errs = append(errs, fmt.Errorf("%s: motion section must declare an easing or transition descriptor", ds.Basename))
	}
	return errors.Join(errs...)
}

func designSystemMotionDuration(line string) (int, bool) {
	match := designSystemDurationRE.FindStringSubmatch(line)
	if len(match) != 2 {
		return 0, false
	}
	var out int
	for _, ch := range match[1] {
		out = out*10 + int(ch-'0')
	}
	return out, true
}

func markdownSection(body, start, end string) string {
	startIdx := strings.Index(body, start)
	if startIdx < 0 {
		return ""
	}
	body = body[startIdx+len(start):]
	if endIdx := strings.Index(body, end); endIdx >= 0 {
		body = body[:endIdx]
	}
	return strings.TrimSpace(body)
}

func findDesignSystemRoleLine(section, role string) string {
	prefix := "- " + role + ":"
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	return ""
}

func hasFontFallback(line, role string) bool {
	lower := strings.ToLower(line)
	switch role {
	case "Mono":
		return strings.Contains(lower, "monospace") ||
			strings.Contains(lower, "mono") ||
			strings.Contains(lower, "menlo") ||
			strings.Contains(lower, "courier")
	default:
		return strings.Contains(lower, "system-ui") ||
			strings.Contains(lower, "sans-serif") ||
			strings.Contains(lower, "serif") ||
			strings.Contains(lower, "monospace") ||
			strings.Contains(lower, "georgia") ||
			strings.Contains(lower, "arial")
	}
}
