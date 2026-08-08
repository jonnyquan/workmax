package prompts

import (
	"strings"
	"unicode/utf8"
)

// SystemPromptRegistry builds the system prompt that gets handed to the
// Claude Agent SDK. The "registry" name is historical — there used to be a
// template-versioning scheme here that's since been collapsed into a
// single mode-driven builder. Kept as a struct receiver because callers
// already construct it via NewSystemPromptRegistry() and might one day
// want to attach state (caching, A/B selection) without changing those
// call sites.
type SystemPromptRegistry struct{}

// NewSystemPromptRegistry returns a registry instance.
func NewSystemPromptRegistry() *SystemPromptRegistry {
	return &SystemPromptRegistry{}
}

// GetSystemPrompt retrieves the system prompt for the given agent mode.
// agentMode: one of allowedAgentModes (see
// api/pro/tools/workagent/conversation_api.go), e.g. "ppt",
// "flashCard", "socialAd". This struct/method is the legacy
// pre-skill-bundle path; production traffic now goes through
// skills.Registry. Kept as dead code for the moment so existing
// tests still link — will be removed alongside prompts/templates/
// once the legacy bridge is excised.
func (r *SystemPromptRegistry) GetSystemPrompt(agentMode string, hasFiles bool, fileContext string) string {
	systemPrompt := r.buildAIChatAgentSystemPrompt(agentMode)

	if hasFiles && fileContext != "" {
		sanitizedContext := r.sanitizeFileContext(fileContext)
		systemPrompt = strings.ReplaceAll(systemPrompt, "{{.FileContext}}", sanitizedContext)
	}

	return systemPrompt
}

// sanitizeFileContext caps user-controlled context length and strips a
// short denylist of injection patterns before it lands in the system
// prompt. This is defense-in-depth — the prompt is server-built so user
// content shouldn't reach this path uncontrolled, but if upstream ever
// regresses we don't want a 1MB context or a `{{.FileContext}}` recursion
// nuking the model.
func (r *SystemPromptRegistry) sanitizeFileContext(fileContext string) string {
	const maxContextLength = 5000

	// Byte-cap with a rune-boundary walk-back. The cap is byte-length
	// (matches the docstring's "1MB context" intent), but the cut must
	// land on a rune boundary so a CJK fileContext doesn't end on a
	// partial UTF-8 sequence. A naive byte slice at index 5000 in CJK
	// content commonly lands inside a 3-byte rune; the resulting
	// invalid bytes flow into the system prompt as U+FFFD replacement
	// characters in JSON-marshalled SDK requests, garbling whichever
	// character was truncated. Same fix family as cb8f207c, 68e83818,
	// 6d8bfcf5.
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
