package prompts

import (
	"strings"
	"testing"
)

// Sanity tests for the embed.FS-backed framework template. They lock
// in three things the migration could break: the template loads at
// all, the two placeholders get substituted, and the H1 header is
// preserved. If any of these fail, the .md file is out of sync with
// what callers expect.

func TestLoadFrameworkTemplate_ContainsPlaceholders(t *testing.T) {
	tmpl := loadFrameworkTemplate()
	if !strings.Contains(tmpl, "{{.ModeIdentity}}") {
		t.Error("framework template missing {{.ModeIdentity}} placeholder")
	}
	if !strings.Contains(tmpl, "{{.OutputFormat}}") {
		t.Error("framework template missing {{.OutputFormat}} placeholder")
	}
	// Header line — pinned because a stray edit that drops the H1 is
	// the kind of thing a markdown editor might "helpfully" reflow.
	if !strings.HasPrefix(tmpl, "# AI Agent - Multi-Format Data & Content Assistant") {
		t.Errorf("framework template lost H1 header; starts with: %q", tmpl[:60])
	}
}

func TestRenderFrameworkTemplate_SubstitutesPlaceholders(t *testing.T) {
	rendered := renderFrameworkTemplate("MODE_IDENTITY_MARKER", "OUTPUT_FORMAT_MARKER", "")

	if strings.Contains(rendered, "{{.ModeIdentity}}") {
		t.Error("ModeIdentity placeholder leaked into rendered output")
	}
	if strings.Contains(rendered, "{{.OutputFormat}}") {
		t.Error("OutputFormat placeholder leaked into rendered output")
	}
	if !strings.Contains(rendered, "MODE_IDENTITY_MARKER") {
		t.Error("ModeIdentity substitution did not land")
	}
	if !strings.Contains(rendered, "OUTPUT_FORMAT_MARKER") {
		t.Error("OutputFormat substitution did not land")
	}
}

// Stage D migration COMPLETE (2026-05-12): every user-facing skill
// declares its identity + output rules inline in SKILL.md, so no
// templates/identity_<mode>.md or templates/output_format_<mode>.md
// files exist on disk anymore. The previous tests in this file
// asserted that those per-mode templates loaded; they are now
// retired. The two loader functions remain as public exports for
// the (dead) legacyPromptsBridge and always return "" — covered
// indirectly by the bridge's own behaviour test in the workagent
// package.
//
// Previously tested (and now retired with their templates):
//   - TestLoadOutputFormatTemplate_AllShippedModes — iterated
//     `writer` after Stage D removed the other 14 templates;
//     writer's removal 2026-05-12 emptied the iteration.
//   - TestLoadOutputFormatTemplate_UnknownModeFallsBackToWriter —
//     no writer fallback exists anymore (skills.Registry falls
//     back to ppt instead).
//   - TestLoadIdentityTemplate_AllShippedModes — same shape as
//     the output-format variant.
//   - TestLoadIdentityTemplate_UnknownModeFallsBackToWriter — same
//     shape as the output-format variant.
//   - TestBuildAIChatAgentSystemPrompt_KnownStructuralMarkers —
//     pinned three markers from agent_framework.md plus one from
//     output_format_writer.md; the latter is gone, and the
//     framework markers are covered by the placeholder tests above.
//
// Sanity check that the loader returns "" gracefully (rather than
// panicking) for any mode, since callers depend on this contract.

func TestLoadOutputFormatTemplate_ReturnsEmptyForAnyMode(t *testing.T) {
	for _, mode := range []string{"writer", "ppt", "totally-bogus-xyz", ""} {
		if got := loadOutputFormatTemplate(mode); got != "" {
			t.Errorf("loadOutputFormatTemplate(%q) = %q, want \"\"", mode, got)
		}
	}
}

func TestLoadIdentityTemplate_ReturnsEmptyForAnyMode(t *testing.T) {
	for _, mode := range []string{"writer", "ppt", "totally-bogus-xyz", ""} {
		if got := loadIdentityTemplate(mode); got != "" {
			t.Errorf("loadIdentityTemplate(%q) = %q, want \"\"", mode, got)
		}
	}
}
