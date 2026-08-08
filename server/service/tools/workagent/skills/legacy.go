package skills

// LegacyTemplateProvider is the bridge between the new skills/
// package and the existing prompts/ package's embedded templates.
// Defining it as an interface here keeps skills/ free of any import
// on prompts/ (avoids the obvious circular-import risk once
// prompts/ is gradually shrunk).
//
// Production wires this to a small adapter in
// agent_processor.go (or a dedicated bridge file) that just calls
// prompts.LoadIdentity / prompts.LoadOutputFormat / prompts.RenderFramework.
//
// During the migration window every legacy mode is served via this
// provider; once a mode's SKILL.md is hand-written and the manifest
// drops its Legacy block, the provider stops being consulted for
// that mode.
type LegacyTemplateProvider interface {
	// Identity returns the body of templates/identity_<mode>.md.
	// After Stage D migration (2026-05-12) no per-mode templates
	// exist on disk; implementations should return "" rather than
	// panicking — no caller depends on the previous "writer
	// fallback" behaviour since writer itself was removed.
	Identity(mode string) string

	// OutputFormat returns the body of templates/output_format_<mode>.md.
	// Same post-Stage-D semantics as Identity — returns "".
	OutputFormat(mode string) string

	// RenderFramework wraps mode-identity + output-format in the
	// agent_framework template, applies FileContext substitution
	// when ctx.HasFiles is true, and runs the same sanitization
	// the legacy SystemPromptRegistry did.
	RenderFramework(modeIdentity, outputFormat string, ctx BuildContext) string
}

// legacyTemplates is a thin adapter that exposes the provider's
// methods to loader.go without polluting the loader with provider
// plumbing on every call.
type legacyTemplates struct {
	provider LegacyTemplateProvider
}

func (l *legacyTemplates) identity(mode string) string {
	return l.provider.Identity(mode)
}

func (l *legacyTemplates) outputFormat(mode string) string {
	return l.provider.OutputFormat(mode)
}

// renderFramework is the pure-legacy path (no manifest, no _shared
// overlays). Called when a mode has no skill.yaml at all — produces
// byte-identical output to the old SystemPromptRegistry.
func (l *legacyTemplates) renderFramework(mode string, ctx BuildContext) (string, error) {
	identity := l.provider.Identity(mode)
	output := l.provider.OutputFormat(mode)
	return l.provider.RenderFramework(identity, output, ctx), nil
}

// renderFrameworkWithSlots is the manifest-driven path: caller has
// already assembled the identity + output-format strings from any
// combination of _shared overlays + skill body + local refs.
func (l *legacyTemplates) renderFrameworkWithSlots(identity, output string, ctx BuildContext) (string, error) {
	return l.provider.RenderFramework(identity, output, ctx), nil
}

// noopLegacyProvider is the no-fallback variant used by tests that
// only exercise fully-migrated skills. Returns empty strings so a
// missing legacy reference shows up as visibly-empty output rather
// than a panic — easy to spot in golden assertions.
type noopLegacyProvider struct{}

func (noopLegacyProvider) Identity(string) string                                   { return "" }
func (noopLegacyProvider) OutputFormat(string) string                               { return "" }
func (noopLegacyProvider) RenderFramework(identity, _ string, _ BuildContext) string { return identity }
