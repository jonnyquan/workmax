package prompts

// getModeSpecificOutputFormat returns the output format priority for the
// given agent mode by loading the matching templates/output_format_<mode>.md
// file at request time. After Stage D migration (2026-05-12) no such
// files exist, so this always returns "" — kept as a shim so the
// (dead) buildAIChatAgentSystemPrompt path still links.
func getModeSpecificOutputFormat(mode string) string {
	return loadOutputFormatTemplate(mode)
}

// buildAIChatAgentSystemPrompt assembles the Agent mode system prompt by
// loading templates/agent_framework.md (embedded at build time) and
// substituting the mode-specific identity + output-format sections.
//
// The framework template lives as plain markdown so prompt edits become
// reviewable .md diffs instead of 800-line Go raw-string mutations.
//
// SystemAdditions is empty here — this entry point is the legacy
// SystemPromptRegistry path used by callers that don't drive the new
// _shared / discovery / design-system layers. Skill-bundle callers
// take RenderFrameworkAndApplyContext (legacy_export.go), which
// accepts a full SystemAdditionsComposer. After Stage D this whole
// pipeline is effectively dead in production — skills.Registry is
// the live path — but it is retained as a fallback shape for any
// caller that still constructs SystemPromptRegistry directly.
func (r *SystemPromptRegistry) buildAIChatAgentSystemPrompt(mode string) string {
	return renderFrameworkTemplate(getModeSpecificIdentity(mode), getModeSpecificOutputFormat(mode), "")
}
