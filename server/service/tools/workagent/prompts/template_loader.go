package prompts

import (
	"embed"
	"strings"
	"sync"
)

// templatesFS embeds the markdown prompt templates at build time. Adding
// a new template is purely a matter of dropping a file into
// templates/*.md — no Go change needed if the template is loaded by
// loadEmbeddedTemplate at request time.
//
//go:embed templates/*.md
var templatesFS embed.FS

// frameworkTemplateCache holds the agent_framework.md contents after the
// first read. The bytes are immutable for the process lifetime
// (embed.FS is a read-only filesystem), so a single sync.Once load is
// enough — no invalidation, no TTL, no reloading.
var (
	frameworkTemplate     string
	frameworkTemplateOnce sync.Once
)

// loadFrameworkTemplate returns the embedded agent framework markdown.
// Panics if the template is missing — that's a build-time failure, not
// a runtime one, so failing fast at startup is correct.
func loadFrameworkTemplate() string {
	frameworkTemplateOnce.Do(func() {
		data, err := templatesFS.ReadFile("templates/agent_framework.md")
		if err != nil {
			panic("workagent prompts: agent_framework.md missing from embed.FS: " + err.Error())
		}
		frameworkTemplate = string(data)
	})
	return frameworkTemplate
}

// renderFrameworkTemplate substitutes the per-request placeholders in the
// framework template. Three placeholders today:
//   - {{.ModeIdentity}}     — mode-specific persona / behavior (per-mode)
//   - {{.OutputFormat}}     — mode-specific output-format priority (per-mode)
//   - {{.SystemAdditions}}  — _shared protocols + per-turn additions
//                             (discovery context, brand-spec protocol,
//                             active design-system, skill side files,
//                             checklist digest). Empty = no additions
//                             (the placeholder vanishes cleanly).
//
// Plain string substitution is intentional. text/template would force us
// to escape every `{` in the markdown (the framework has many code
// blocks containing braces) and would also reject `{{` inside fenced
// code, neither of which buys us anything for static slots.
//
// systemAdditions intentionally last in the prompt so the agent reads
// the framework rules + mode identity + output format FIRST, then the
// additions layer in (a) per-turn context and (b) anti-slop / asset /
// brand protocols. The closing position matches Open Design's prompt
// stack ordering and keeps the most specific instructions (turn-1
// discovery answers, active design system, skill side files) freshest
// in the model's context window.
func renderFrameworkTemplate(modeIdentity, outputFormat, systemAdditions string) string {
	out := loadFrameworkTemplate()
	out = strings.ReplaceAll(out, "{{.ModeIdentity}}", modeIdentity)
	out = strings.ReplaceAll(out, "{{.OutputFormat}}", outputFormat)
	// systemAdditions empty → strip the placeholder line cleanly,
	// avoiding a trailing blank section. The framework template ends
	// with a blank line + "{{.SystemAdditions}}", so empty input
	// becomes a blank line at end-of-prompt — harmless but ugly. The
	// TrimRight at the end handles it.
	out = strings.ReplaceAll(out, "{{.SystemAdditions}}", systemAdditions)
	return strings.TrimRight(out, " \t\n") + "\n"
}

// outputFormatCache memoizes per-mode output-format markdown after the
// first load. Same rationale as frameworkTemplate — embedded files are
// immutable for the process lifetime.
//
// After the Stage D migration (2026-05-12) every user-facing skill
// declares its own output rules inside SKILL.md, so no template
// files of the form templates/output_format_<mode>.md exist. The
// loader returns "" for every mode now — kept as a package export
// only so the (dead) skill_bridge.go's legacyPromptsBridge.OutputFormat
// still compiles. Will be removed once the bridge is excised.
var (
	outputFormatCache   = map[string]string{}
	outputFormatCacheMu sync.Mutex
)

// loadOutputFormatTemplate returns the embedded output-format markdown
// for the given mode, or "" if no template exists on disk. After Stage
// D no per-mode templates exist, so this always returns "" in
// practice.
func loadOutputFormatTemplate(mode string) string {
	return loadModeTemplate(outputFormatCache, &outputFormatCacheMu, "output_format_", mode)
}

// identityCache memoizes per-mode identity markdown after the first
// load. Same caching/fallback shape as outputFormatCache.
//
// After the Stage D migration (2026-05-12) every user-facing skill
// declares its own identity inside SKILL.md, so no
// templates/identity_<mode>.md files exist. See outputFormatCache.
var (
	identityCache   = map[string]string{}
	identityCacheMu sync.Mutex
)

// loadIdentityTemplate returns the embedded identity markdown for the
// given mode, or "" if no template exists on disk. After Stage D no
// per-mode templates exist, so this always returns "" in practice.
func loadIdentityTemplate(mode string) string {
	return loadModeTemplate(identityCache, &identityCacheMu, "identity_", mode)
}

// loadModeTemplate is the shared lookup used by both the output-format
// and identity loaders. Caches by Go map under the supplied mutex.
// Returns "" when the template is missing — the previous panic-on-
// missing-fallback contract was retired in 2026-05-12 when the last
// legacy mode (writer) was removed; missing files are now the
// normal state and callers (skill_bridge.go) handle "" defensively.
func loadModeTemplate(cache map[string]string, mu *sync.Mutex, prefix, mode string) string {
	mu.Lock()
	defer mu.Unlock()
	if cached, ok := cache[mode]; ok {
		return cached
	}
	data, err := templatesFS.ReadFile("templates/" + prefix + mode + ".md")
	if err != nil {
		cache[mode] = ""
		return ""
	}
	cache[mode] = string(data)
	return cache[mode]
}
