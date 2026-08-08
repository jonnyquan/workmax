package prompts

// prompt_layers.go — DS-5. Makes the 7-layer prompt stack
// explicit in code so each layer can be:
//   - independently disabled (debug / A/B / gradual rollout)
//   - traced (which layer contributed how many bytes)
//   - reasoned about in isolation (each composer field maps to
//     exactly one layer)
//
// The 7 layers and what each carries:
//
//	1 Discovery directives    — the M1 form / NLP-inferred user
//	                            answers (audience / tone / scale)
//	2 Identity charter        — anti-AI-slop + junior-pass
//	                            principles (universal "how to think")
//	3 Protocols               — brand-spec extraction rules,
//	                            future "how to handle X" playbooks
//	4 Active design-system    — the 9-section DESIGN.md for the
//	                            selected aesthetic
//	5 Active SKILL.md         — skill body (LIVES IN BASE PROMPT,
//	                            reserved slot here for future
//	                            additions-side skill references)
//	6 Skill side files        — assets/* + references/* +
//	                            checklist digest (per-skill seed
//	                            material the agent reads before
//	                            generating)
//	7 Project metadata        — this thread's brand / direction /
//	                            characters / director-style /
//	                            asset-library index (the "what's
//	                            active for THIS conversation")
//
// The default Compose() ordering is the LEGACY ordering (preserved
// for byte-for-byte backwards compatibility); DS-5's canonical
// ordering is exposed via Canonical() for future opt-in via
// feature flag.

// PromptLayer identifies one of the 7 DS-5 layers.
type PromptLayer int

const (
	// LayerUnknown is the zero value, used by helpers that need
	// to signal "not classified" without returning an error.
	LayerUnknown PromptLayer = 0

	LayerDiscovery       PromptLayer = 1
	LayerIdentity        PromptLayer = 2
	LayerProtocols       PromptLayer = 3
	LayerDesignSystem    PromptLayer = 4
	LayerSkill           PromptLayer = 5
	LayerSkillSideFiles  PromptLayer = 6
	LayerProjectMetadata PromptLayer = 7
)

// AllLayers returns the 7 layers in canonical DS-5 order (1 → 7).
// Callers that want to iterate "every layer once" — disable
// resolution, trace output, debug dumps — use this. The Compose()
// path uses its own legacy field-iteration order to preserve
// byte-for-byte output; this list is reserved for the introspection
// surfaces.
func AllLayers() []PromptLayer {
	return []PromptLayer{
		LayerDiscovery,
		LayerIdentity,
		LayerProtocols,
		LayerDesignSystem,
		LayerSkill,
		LayerSkillSideFiles,
		LayerProjectMetadata,
	}
}

// Name returns the short identifier used in log lines + the
// debug trace surface. Stable across versions — operators grep
// for these.
func (l PromptLayer) Name() string {
	switch l {
	case LayerDiscovery:
		return "discovery"
	case LayerIdentity:
		return "identity"
	case LayerProtocols:
		return "protocols"
	case LayerDesignSystem:
		return "design-system"
	case LayerSkill:
		return "skill"
	case LayerSkillSideFiles:
		return "skill-side-files"
	case LayerProjectMetadata:
		return "project-metadata"
	default:
		return "unknown"
	}
}

// Canonical returns the DS-5 canonical order (1-7). Identical to
// the enum int value today; broken out so a future re-numbering
// of the constants (low risk; only test code looks at them as
// ints) doesn't shift the canonical order semantics.
func (l PromptLayer) Canonical() int {
	return int(l)
}

// LayerContribution records one layer's slice of the final
// composed string. The ComposeWithTrace() path returns these in
// the order they appear in the rendered prompt, so debug surfaces
// can show "this section came from the discovery layer, this one
// from project-metadata, etc."
//
// Fields lists the SystemAdditionsComposer field names that fed
// this layer's text — e.g. ["brand-spec", "library-index"] for the
// project-metadata layer when both fields were populated. Useful
// when the trace is too coarse to debug ("which exact field is
// making this turn's prompt 30KB?").
type LayerContribution struct {
	Layer  PromptLayer
	Text   string
	Fields []string
}

// LayerDisableSet captures the per-layer opt-out for ops debug
// + per-uid rollback. The Compose() path checks this before
// emitting any of a layer's contributions; a disabled layer
// produces empty output and zero entries in the trace.
//
// Map shape (not slice) because lookup is on the hot path; the
// O(1) check beats a small-slice linear scan and reads cleaner
// at call sites.
type LayerDisableSet map[PromptLayer]bool

// IsDisabled returns true when the layer has been opted out.
// nil-safe — a nil set returns false for every layer (default
// "all enabled"), letting callers skip the nil check.
func (s LayerDisableSet) IsDisabled(l PromptLayer) bool {
	if s == nil {
		return false
	}
	return s[l]
}
