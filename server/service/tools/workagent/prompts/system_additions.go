package prompts

import "strings"

// SystemAdditionsComposer assembles the "additions" tail of the system
// prompt — the layers grouped under "discovery directives, active design
// system, skill side files, project metadata".
//
// All fields are optional. Empty fields drop out without a separator,
// so a composer with only `DiscoveryContext` set produces clean output
// without trailing dividers. The renderer's TrimRight handles a fully
// empty composer correctly.
//
// Layer order matters — see Compose() comments for why each layer
// sits where it does.
type SystemAdditionsComposer struct {
	// DiscoveryContext — the answers from the M1 turn-1 form, when
	// the user has filled it. Format: <discovery-context>...</discovery-context>
	// XML block. Lands first in the additions tail because the form's
	// answers (audience / tone / scale) constrain everything else
	// downstream — design-system selection, anti-slop strictness,
	// even which checklist items apply.
	DiscoveryContext string

	// SelectedDirection — when no brand asset is active, M2's
	// directions picker writes the chosen direction's spec here:
	// palette + font_stack + layout_posture. Format: <visual-direction>
	// XML block. Sits after DiscoveryContext because the form may
	// have indicated a tone that the picker then expands into a
	// concrete deterministic spec.
	SelectedDirection string

	// BrandSpec — when the user has a brand asset (file-type via
	// thread workdir/brand-spec.md, or DB-backed via M4 v2), the
	// active spec gets injected here. Mutually exclusive with
	// SelectedDirection in practice (brand wins over fallback
	// direction), but the composer doesn't enforce that — caller
	// decides which to populate. Format: <brand-spec> XML block.
	BrandSpec string

	// DesignSystem — DS-1 active design-system markdown (the 9-
	// section DESIGN.md for `modern-minimal` / `editorial-magazine`
	// / etc., or a user-forked custom system). Verbatim markdown
	// without an XML wrapper — the agent reads it as a reference
	// document, same way it reads a SKILL.md.
	DesignSystem string

	// AssetLibraryIndex — Backlog #11 polish. Compact list of the
	// other assets in the user's library (beyond the active
	// brand / character / director-style above). Lets the agent
	// reference assets by slug ("use my acme-corp brand") instead
	// of re-extracting an asset the user already has.
	//
	// Format: <asset-library-index> XML with type sublists. Empty
	// when the user has no other assets. Lives near the visual-
	// identity layer because the agent uses it to inform brand /
	// character / director-style decisions.
	AssetLibraryIndex string

	// DirectorStyle — Backlog #11 10/n. User's confirmed
	// director_style_asset, the cinematic / posture fingerprint
	// (composition, color grading, lighting, motion, texture).
	// Additive like CharacterContext — orthogonal to brand colors /
	// typography because a director's visual posture is about
	// HOW shots look, not WHAT colors are in them.
	//
	// Layer order: brand identity → director-style posture →
	// character (who appears) → skill side files. The model reads
	// abstract-to-concrete top to bottom.
	DirectorStyle string

	// CharacterContext — Backlog #11 9/n. When the user has a
	// confirmed character_asset row, the latest one rides into
	// preflight as <character-context>. Additive (not mutually
	// exclusive with brand/direction): a character appears WITHIN
	// the brand's visual identity, so the layer sits below the
	// visual-identity block and above skill side files.
	//
	// Format: <character-context> XML carrying name + description +
	// traits + canonical_image_path. Empty when no confirmed
	// character; the M4 watermark path for drafts is deferred.
	CharacterContext string

	// SkillSideFiles — DS-2 preflight injection of the active
	// skill's assets/template-shell.* + references/*. Pre-formatted
	// as <skill-side-files> XML with nested <asset> / <reference>
	// children by the loader; we just splice it in here.
	SkillSideFiles string

	// KnowledgeContext — runtime RAG snippets retrieved from the
	// Work Agent knowledge base. Vector adapters can feed the same
	// XML shape later; the first implementation is a lexical fallback.
	KnowledgeContext string

	// ChecklistDigest — DS-4 pre-flight: the active skill's P0/P1/P2
	// checklist titles (NOT full descriptions) injected so the model
	// reads the constraints BEFORE generating, not after. Format:
	// <skill-checklist> XML with <p0> / <p1> / <p2> children. Lands
	// near the end so it's the freshest constraint the model reads
	// before the user message in its working memory.
	ChecklistDigest string

	// (F2 2026-05-17) AntiSlopProtocol + BrandSpecProtocol fields
	// removed. AntiSlopProtocol was redundant — _shared/anti-ai-slop.md
	// reaches the model via every skill's references.shared
	// inlining at SKILL.md build time (see skills/loader.go's
	// buildIdentity), so a composer-side injection would double the
	// content. BrandSpecProtocol was triple-dead: the field was
	// never assigned in production, IsBrandSpecProtocolEnabled
	// config had no consumer, and the referenced
	// _shared/brand-spec-protocol.md file doesn't exist on disk.
	// Brand-spec content reaches the model via composer.BrandSpec
	// (active brand asset) which IS wired through preflight.go.

	// PlanModeProtocol — A3 plan-then-execute. When the user
	// turns on Plan mode for THIS turn, the propose_plan protocol
	// rides in here so the agent emits a plan card and waits for
	// approval before doing work. Per-turn, not per-thread —
	// matches the canvas surface's same-shape PlanMode flag and
	// keeps "Plan mode was on three weeks ago and I forgot"
	// failure modes out of reach.
	//
	// Lands LAST in the additions tail (after critique, after
	// checklist) so it's the freshest constraint the model reads
	// before the user message — the agent's working-memory order
	// is "rules → identity → format → user signal → plan-or-act".
	// Empty when planMode=false (the default).
	PlanModeProtocol string

	// PassModeProtocol — P3 Pass 1 + Variations runtime gate. This
	// tells the agent which design-workflow pass the current thread is
	// in: briefing, draft, finalize, or revision. Unlike PlanMode this
	// is thread-stateful: the first turn defaults to briefing, form
	// answers can move it to draft, a variation selection can move it
	// to finalize, and redo paths can move it to revision.
	//
	// Lands next to other discovery-layer signals because it describes
	// how to interpret the user's current request, not a visual style
	// or skill asset. It stays before PlanMode so an explicit plan
	// request remains the freshest per-turn constraint.
	PassModeProtocol string

	// PreviousCritique — P0 #3 critique loop. The most recent
	// user-typed feedback paired with a thumbs-down, gated on
	// "the critiqued message is still the latest message in the
	// thread" (MessageRepository.LoadActiveUserCritique).
	//
	// Format: <previous-critique> XML block carrying the verbatim
	// feedback text. Lands in the LayerDiscovery layer alongside
	// the M1 form answers — both are "what the user just signaled"
	// inputs that the model reads right before the next user
	// message. Critique sits AFTER DiscoveryContext in the
	// iteration order so the freshest signal (the user reacting
	// to the last turn's output) is closer to the user's next
	// prompt in the model's working memory.
	//
	// Empty when the thread has no active critique. Repeated-critique
	// dedupe is the staleness gate's responsibility — once the user
	// continues the conversation, the critique stops surfacing.
	PreviousCritique string

	// LayerDisable opts out specific DS-5 layers (debug / A/B /
	// gradual rollout). A nil map is the default "all layers
	// enabled". Layers disabled here produce empty output AND
	// zero trace entries — the layer is invisible end-to-end.
	//
	// See PromptLayer + AllLayers() in prompt_layers.go for the
	// canonical 7-layer list. Most prod callers leave this nil;
	// ops + tests use it to isolate "what if we dropped X?"
	// experiments.
	LayerDisable LayerDisableSet
}

// Compose returns the final additions string ready to splice into the
// {{.SystemAdditions}} placeholder. Layer ordering rationale:
//
//  1. Anti-slop protocol         (universal hard rules)
//  2. Brand-spec protocol        (process: how to handle brand refs)
//  3. Active design-system       (constraints: what palette/typography)
//  4. Active brand or direction  (concrete: which colors right now)
//  5. Asset library index        (other assets the user has)
//  6. Director-style + character (additive layers under identity)
//  7. Skill side files           (skill-specific seeds + references)
//  8. Discovery context          (this turn's user-form answers)
//  9. Checklist digest           (final pre-flight constraints)
//
// Discovery + checklist live closest to the agent's "next thing to
// do" because that's the most recent context the model accumulates
// before reading the user message. The protocols (1, 2) are
// upstream because they govern how the model interprets EVERYTHING
// downstream — keeping them at the top mirrors how a human designer
// reads a brief: "what are the rules?" before "what's the project?"
func (c SystemAdditionsComposer) Compose() string {
	out, _ := c.ComposeWithTrace()
	return out
}

// ComposeWithTrace returns both the final additions string AND a
// per-layer trace of which DS-5 layer each section came from.
// Identical text to Compose() for callers that ignore the trace —
// the introspection ride-along is free. Ops surfaces consume the
// trace for "which layer is making this turn's prompt huge?"
// debugging.
//
// The trace's order matches the text's order, so a debug view
// rendering "section #3 came from project-metadata, fed by
// brand-spec + library-index" stays aligned with what the model
// sees.
func (c SystemAdditionsComposer) ComposeWithTrace() (string, []LayerContribution) {
	if c.isEmpty() {
		return "", nil
	}

	// Per-field "what layer am I" mapping. Iteration order below
	// is the legacy Compose() field-by-field order — keep it
	// stable so byte-for-byte output parity holds for every
	// caller. The layer tag exists for trace introspection AND
	// for the disable-set short-circuit; reordering the fields
	// would change the text the model sees.
	type fieldEntry struct {
		text  string
		layer PromptLayer
		field string
	}

	// BrandSpec wins over SelectedDirection when both populated —
	// caller should not set both, but if they do, treat the brand
	// reference as more authoritative.
	brandOrDirection := strings.TrimSpace(c.BrandSpec)
	brandOrDirectionField := "brand-spec"
	if brandOrDirection == "" {
		brandOrDirection = strings.TrimSpace(c.SelectedDirection)
		brandOrDirectionField = "direction"
	}

	entries := []fieldEntry{
		// (F2 2026-05-17) anti-slop + brand-protocol layers removed
		// — see the AntiSlopProtocol/BrandSpecProtocol removal note
		// in the field block above for the rationale.
		{strings.TrimSpace(c.DesignSystem), LayerDesignSystem, "design-system"},
		{brandOrDirection, LayerProjectMetadata, brandOrDirectionField},
		{strings.TrimSpace(c.AssetLibraryIndex), LayerProjectMetadata, "library-index"},
		// Director-style + character are both additive (orthogonal to
		// brand/direction). Director-style is abstract posture; character
		// is concrete (who appears). Abstract-to-concrete top to bottom.
		{strings.TrimSpace(c.DirectorStyle), LayerProjectMetadata, "director-style"},
		{strings.TrimSpace(c.CharacterContext), LayerProjectMetadata, "character-context"},
		{strings.TrimSpace(c.SkillSideFiles), LayerSkillSideFiles, "side-files"},
		{strings.TrimSpace(c.KnowledgeContext), LayerProjectMetadata, "knowledge-context"},
		{strings.TrimSpace(c.DiscoveryContext), LayerDiscovery, "discovery"},
		// PreviousCritique sits AFTER DiscoveryContext in this list
		// because critique is the freshest user signal — discovery is
		// "what the user told me at the start of the thread", critique
		// is "what the user told me about my last reply". Closest to
		// the user's next message in working memory = highest impact.
		{strings.TrimSpace(c.PreviousCritique), LayerDiscovery, "previous-critique"},
		{strings.TrimSpace(c.ChecklistDigest), LayerSkillSideFiles, "checklist"},
		{strings.TrimSpace(c.PassModeProtocol), LayerDiscovery, "pass-mode"},
		// PlanMode protocol lands last — see field doc. The agent
		// reads it as "the user requires plan-then-act for THIS
		// turn", which is the freshest constraint to honor.
		{strings.TrimSpace(c.PlanModeProtocol), LayerDiscovery, "plan-mode"},
	}

	var parts []string
	var trace []LayerContribution
	for _, e := range entries {
		if e.text == "" {
			continue
		}
		if c.LayerDisable.IsDisabled(e.layer) {
			continue
		}
		parts = append(parts, e.text)
		trace = append(trace, LayerContribution{
			Layer:  e.layer,
			Text:   e.text,
			Fields: []string{e.field},
		})
	}

	if len(parts) == 0 {
		return "", nil
	}
	// Use a section divider that's visible in the rendered prompt
	// (helps debugging / log analysis) but doesn't conflict with
	// markdown's own heading conventions. The renderer pipes through
	// the model verbatim, so a horizontal rule with explicit
	// separator chars is more legible than just blank lines.
	return "\n\n---\n\n" + strings.Join(parts, "\n\n---\n\n") + "\n", trace
}

func (c SystemAdditionsComposer) isEmpty() bool {
	return strings.TrimSpace(c.DesignSystem) == "" &&
		strings.TrimSpace(c.BrandSpec) == "" &&
		strings.TrimSpace(c.SelectedDirection) == "" &&
		strings.TrimSpace(c.AssetLibraryIndex) == "" &&
		strings.TrimSpace(c.DirectorStyle) == "" &&
		strings.TrimSpace(c.CharacterContext) == "" &&
		strings.TrimSpace(c.SkillSideFiles) == "" &&
		strings.TrimSpace(c.KnowledgeContext) == "" &&
		strings.TrimSpace(c.DiscoveryContext) == "" &&
		strings.TrimSpace(c.PreviousCritique) == "" &&
		strings.TrimSpace(c.ChecklistDigest) == "" &&
		strings.TrimSpace(c.PassModeProtocol) == "" &&
		strings.TrimSpace(c.PlanModeProtocol) == ""
}
