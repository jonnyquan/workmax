package skills

import (
	"fmt"
	"os"
	"sync"
)

// WarnFunc is the misconfigured-skill notification callback the
// Registry calls (at most once per skill name per process) when
// GetSystemPrompt has to fall back to the default skill (ppt)
// because the requested skill is broken. Production wires this to
// the application's structured logger so the warning lands in
// telemetry; tests can leave it nil for the default fmt.Printf
// behaviour.
//
// Why a callback rather than direct globals.Warn import: the skills
// package is intentionally dependency-light so it can be reused
// across modules (and tested in isolation). Pulling in
// server/globals would make this package transitively depend on the
// whole DB/config/world stack — exactly the kind of cycle the
// registry's per-package boundary exists to prevent.
type WarnFunc func(skillName string, err error)

// Registry is the public entry point most callers should use. It
// owns a single Loader instance and provides a stable API surface
// (Get / Build) so the rest of work-agent doesn't need to wire the
// loader plumbing on every call site.
//
// Construction is via NewRegistry(legacyProvider) — the caller
// supplies the bridge to the legacy prompts/ package. Tests can
// pass nil to use the noop provider.
type Registry struct {
	loader *Loader
	warn   WarnFunc

	// warnedSkills dedupes warnings so a flapping mode doesn't spam
	// logs every turn. Per-Registry (not package-level) so tests that
	// build their own Registry don't inherit dedup state from earlier
	// tests in the same process.
	warnedSkills sync.Map
}

// NewRegistry builds a Registry wired to the supplied legacy
// provider. nil is allowed (tests, fully-migrated environments).
//
// The default warn behaviour is fmt.Printf to stderr; production
// callers should use SetWarnFunc to route warnings through their
// structured logger so misconfigured skills land in telemetry. Done
// as a setter (rather than a constructor param) so adding the
// telemetry hook didn't break the signature for existing callers.
func NewRegistry(legacyProvider LegacyTemplateProvider) *Registry {
	return &Registry{loader: NewLoader(legacyProvider)}
}

// SetWarnFunc installs the misconfigured-skill notification hook.
// Safe to call once at process startup, before any traffic arrives;
// concurrent calls during steady-state are not supported (the
// registry is not designed to swap loggers under load — there's no
// real reason to).
func (r *Registry) SetWarnFunc(warn WarnFunc) {
	r.warn = warn
}

// Build is the production-path entry point. Returns the system
// prompt + bundle metadata for `agentMode`. The mode parameter
// matches ChatThread.AgentMode 1:1 — what was a "mode" before is a
// "skill name" now.
//
// Errors here are programmer errors (malformed manifest, missing
// SKILL.md when the manifest declares no legacy fallback). They
// shouldn't reach production once the migration finishes; during
// migration they signal "this skill's authoring is incomplete".
func (r *Registry) Build(agentMode string, ctx BuildContext) (*SkillBundle, error) {
	if agentMode == "" {
		return nil, fmt.Errorf("registry.Build: empty agent mode")
	}
	return r.loader.Build(agentMode, ctx)
}

// GetSystemPrompt is the back-compat shim that mirrors the legacy
// SystemPromptRegistry.GetSystemPrompt signature. Lets call sites
// migrate one at a time without rewriting their argument shapes.
//
// Errors are converted to a fallback-skill path because the old API
// was infallible — callers don't have an error channel to bubble
// through. The error is logged via the injected WarnFunc (or
// fmt.Printf if not set) so misconfigured skills don't go silent.
// fallbackSkillName below is the target.
func (r *Registry) GetSystemPrompt(agentMode string, hasFiles bool, fileContext string) string {
	return r.GetSystemPromptWithAdditions(agentMode, "", hasFiles, fileContext)
}

// fallbackSkillName is the skill used when the requested skill is
// missing or its manifest is malformed. ppt is chosen because it is
// (a) a real user-facing skill with a fully-native SKILL.md and
// (b) the DDL default for chat_thread.agent_mode, so a misconfigured
// or unknown mode surfaces the same behaviour a freshly-created
// thread would. Previously this was "writer", but writer was
// removed from the catalog in 2026-05-12 because no production path
// requested it — its only role was being a fallback target.
const fallbackSkillName = "ppt"

// GetSystemPromptWithAdditions is the Sprint-A onwards entry that
// extends GetSystemPrompt with a precomposed SystemAdditions string
// (the 7-layer prompt stack tail — see prompts.SystemAdditionsComposer).
//
// Empty additions = identical behavior to GetSystemPrompt for the
// existing call sites that haven't migrated yet. Same warn-once +
// fallback semantics.
func (r *Registry) GetSystemPromptWithAdditions(agentMode, additions string, hasFiles bool, fileContext string) string {
	ctx := BuildContext{
		HasFiles:        hasFiles,
		FileContext:     fileContext,
		SystemAdditions: additions,
	}
	bundle, err := r.Build(agentMode, ctx)
	if err != nil {
		r.warnOnce(agentMode, err)
		fallback, ferr := r.loader.Build(fallbackSkillName, ctx)
		if ferr != nil {
			return fmt.Sprintf("[skills.Registry] fatal: %v (%s fallback also failed: %v)", err, fallbackSkillName, ferr)
		}
		return fallback.SystemPrompt
	}
	return bundle.SystemPrompt
}

// ResolveVersion returns the skill bundle's version string for
// observability — call sites use it in per-turn log lines so
// operators can see which skill version produced a given output
// (and notice silently-shipped bumps from `2.0.0` → `2.1.0`).
//
// Failure-mode policy: errors collapse to the literal "unknown"
// rather than propagating, because this is a log-only call site
// and we never want a version-lookup failure to crash a turn.
// The registry's GetSystemPromptWithAdditions path handles the
// real fallback (rendering ppt's prompt); this method's empty
// answer just means "we couldn't tell".
//
// F4 (2026-05-17) — paired with the agent_processor.go log line
// extension. Single-call helper instead of widening
// GetSystemPromptWithAdditions's return shape because every
// existing caller of that method just wants the string.
func (r *Registry) ResolveVersion(agentMode string) string {
	if agentMode == "" {
		return "unknown"
	}
	bundle, err := r.loader.Build(agentMode, BuildContext{})
	if err != nil || bundle == nil {
		return "unknown"
	}
	if bundle.Version == "" {
		return "legacy"
	}
	return bundle.Version
}

// warnOnce dedupes a misconfigured-skill warning so a flapping mode
// doesn't spam logs every turn. Routes through the registry's
// WarnFunc when set; falls back to os.Stderr so a pre-SetWarnFunc
// test still sees the warning without polluting stdout (which a
// caller may be piping as JSON).
func (r *Registry) warnOnce(mode string, err error) {
	if _, seen := r.warnedSkills.LoadOrStore(mode, struct{}{}); seen {
		return
	}

	if r.warn != nil {
		r.warn(mode, err)
		return
	}
	fmt.Fprintf(os.Stderr, "[skills.Registry] WARN: skill %q misconfigured: %v\n", mode, err)
}
