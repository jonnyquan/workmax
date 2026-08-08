package prompts

import (
	"strings"
	"unicode/utf8"
)

// Public exports added during the Skill-bundle migration. The
// skills/ package implements its own loader on top of these so the
// prompts/ package can stay focused on raw template I/O while
// skills/ handles manifest-driven composition.
//
// Once every agent mode has its own SKILL.md (no Manifest.Legacy
// block), these exports become the only thing the runtime calls
// from prompts/, and the rest of the package shrinks to its
// embed-FS shell. At that point we can fold the exports into
// skills/ and delete this package entirely.

// LoadIdentity returns the body of templates/identity_<mode>.md.
// After Stage D migration (2026-05-12) no per-mode identity
// templates exist on disk, so this always returns "" — the live
// path is skills.Loader.readSkillBody which reads SKILL.md instead.
// Kept as a public export so skill_bridge.go's legacyPromptsBridge
// still compiles; will be removed when the bridge is excised.
func LoadIdentity(mode string) string {
	return loadIdentityTemplate(mode)
}

// LoadOutputFormat returns the body of templates/output_format_<mode>.md.
// After Stage D migration (2026-05-12) no per-mode output-format
// templates exist on disk, so this always returns "". See
// LoadIdentity for the same rationale.
func LoadOutputFormat(mode string) string {
	return loadOutputFormatTemplate(mode)
}

// RenderFrameworkAndApplyContext wraps mode-identity + output-format
// in templates/agent_framework.md and applies the FileContext
// substitution + sanitization the legacy SystemPromptRegistry did.
//
// Backward-compatible signature — pre-Sprint-A callers don't pass a
// SystemAdditions argument. The placeholder is filled with the empty
// string, which the renderer's TrimRight handles cleanly. New callers
// that need to inject discovery / design-system / skill-side-files
// content should use RenderFrameworkWithAdditions.
func RenderFrameworkAndApplyContext(identity, outputFormat string, hasFiles bool, fileContext string) string {
	return RenderFrameworkWithAdditions(identity, outputFormat, "", hasFiles, fileContext)
}

// RenderFrameworkWithAdditions is the Sprint-A-onwards entry point for
// callers that compose system-additions content (Open Design 7-layer
// stack: discovery directives, brand-spec protocol, active design-
// system, skill side files, checklist digest). The composer struct in
// system_additions.go keeps the layer ordering / formatting in one
// place; pass its .Compose() output here.
//
// systemAdditions empty = same behavior as the pre-Sprint-A path,
// which preserves snapshot-test parity for any code path that hasn't
// migrated yet.
func RenderFrameworkWithAdditions(identity, outputFormat, systemAdditions string, hasFiles bool, fileContext string) string {
	out := renderFrameworkTemplate(identity, outputFormat, systemAdditions)
	if hasFiles && fileContext != "" {
		out = strings.ReplaceAll(out, "{{.FileContext}}", sanitizeFileContextDefault(fileContext))
	}
	return out
}

// sanitizeFileContextDefault duplicates SystemPromptRegistry.sanitizeFileContext
// so the export is self-contained — pulling the receiver method out
// would either widen the public API (too many helpers) or require a
// dummy registry value at every call site (clutter).
//
// Keep this in lock-step with prompt_registry.go's receiver version.
// Both run identical logic; a fix in only one half regresses
// whichever caller routes through the unfixed path. (28f344cc fixed
// the receiver; this is the parallel fix for the exported variant
// that skill_bridge.go's RenderFrameworkAndApplyContext actually
// drives in production.)
func sanitizeFileContextDefault(fileContext string) string {
	const maxContextLength = 5000

	// Byte-cap with a rune-boundary walk-back. The previous shape
	// sliced at byte index 5000, which for CJK content (3 bytes/rune)
	// commonly cuts inside a multi-byte UTF-8 sequence. The resulting
	// invalid bytes flow through the system prompt the SDK ships to
	// the model and JSON-marshal as U+FFFD, garbling whichever
	// character was at the cut. Same family as cb8f207c, 68e83818,
	// 6d8bfcf5, 28f344cc.
	if len(fileContext) > maxContextLength {
		cut := maxContextLength
		for cut > 0 && !utf8.RuneStart(fileContext[cut]) {
			cut--
		}
		fileContext = fileContext[:cut] + "... (truncated for security)"
	}

	dangerous := []string{
		"{{", "}}",
		"<script>", "</script>",
		"{{.FileContext}}",
		"DROP TABLE", "DELETE FROM",
	}
	for _, pattern := range dangerous {
		fileContext = strings.ReplaceAll(fileContext, pattern, "[REMOVED]")
	}
	return fileContext
}
