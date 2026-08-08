package prompts

// getModeSpecificIdentity returns the identity section for the given agent
// mode by loading the matching templates/identity_<mode>.md file at
// request time. After Stage D migration (2026-05-12) every user-facing
// skill declares its identity inside SKILL.md instead, so no per-mode
// templates exist on disk and this always returns "". Kept as a shim
// so the (dead) buildAIChatAgentSystemPrompt path still links.
func getModeSpecificIdentity(mode string) string {
	return loadIdentityTemplate(mode)
}
