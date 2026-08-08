// Package workagent — preflight.go.
//
// Builds the SystemAdditions string the SDK turn renders into the
// {{.SystemAdditions}} slot of the framework template.
package workagent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"server/config"
	"server/globals"
	"server/model"
	workagentModel "server/model/workagent"
	"server/service/asset_library"
	"server/service/brand"
	"server/service/tools/workagent/prompts"
	"server/service/tools/workagent/skills"
)

// newDiscoveryMarkerMessage builds the chat_message row used to
// persist NLP-inferred or form-submitted discovery answers. The row
// is metadata-only: user_text + ai_text empty, content_type marks it
// as a discovery marker so frontend lists can filter it out from the
// visible conversation surface.
func newDiscoveryMarkerMessage(uid uint, threadID uint, metaJSON string) *workagentModel.ChatMessage {
	return &workagentModel.ChatMessage{
		UID:         int(uid),
		UUID:        uuid.New().String(),
		ThreadID:    int(threadID),
		ContentType: "discovery_marker",
		ChatMode:    string(workagentModel.ChatModeAgent),
		Metadata:    metaJSON,
	}
}

// BuildPreflightAdditions composes the system-prompt tail injection
// for the supplied (uid, skill) pair. Returns the empty string when
// the gate feature flag is disabled for the user/skill — the
// renderer's TrimRight then drops the {{.SystemAdditions}} placeholder
// cleanly so byte-for-byte parity holds with the legacy path.
//
// Errors are intentionally absorbed: a missing checklist file or a
// disabled feature flag both produce empty output; a misconfigured
// skill produces a warn-once log via LoadChecklistForSkill but still
// returns empty. The contract is "preflight injection MUST NOT block
// a turn".
func BuildPreflightAdditions(uid uint, skillName string) string {
	return BuildPreflightAdditionsForThread(uid, skillName, 0)
}

// BuildPreflightAdditionsForThread is the same as BuildPreflightAdditions
// but also surfaces M1 discovery context (NLP-inferred or form-submitted
// answers) when threadID > 0. Discovery context only persists across
// turns when there's a thread to look it up in; for fresh-thread
// callers (rare; mostly tests) BuildPreflightAdditions() is sufficient.
//
// Lookup is best-effort: a missing message repository connection or
// a missing row degrades to "no discovery context" (empty addition).
// Never blocks a turn.
func BuildPreflightAdditionsForThread(uid uint, skillName string, threadID uint) string {
	return BuildPreflightAdditionsForThreadWithOptions(uid, skillName, threadID, PreflightOptions{})
}

// PreflightOptions threads per-turn-only signals through the
// preflight composer without forcing every call site to grow a
// new positional argument. New per-turn signals (planMode being
// the first) land here as fields, defaulting to zero values so
// callers that don't care continue calling the convenience
// wrapper above.
type PreflightOptions struct {
	// PlanMode (A3, 2026-05-16) — when true the composer
	// receives the propose_plan protocol body in
	// PlanModeProtocol so the agent emits a plan card and waits
	// for approval before doing work. Mirrors the canvas
	// surface's same-name flag in CanvasAgentContext.
	PlanMode bool

	// UserMessage lets the composer retrieve per-turn knowledge context.
	// Empty keeps the historical no-RAG path.
	UserMessage string

	// ProjectID scopes runtime retrieval to global docs plus project docs.
	ProjectID uint

	// TeamIDs scopes runtime retrieval to active team docs.
	TeamIDs []uint64

	// KnowledgeMetadata receives the RAG metadata matching the injected
	// KnowledgeContext so the SSE done payload can update the frontend.
	KnowledgeMetadata *KnowledgeRetrievalMetadata
}

type WorkAgentPassMode string

const (
	WorkAgentPassModeBriefing WorkAgentPassMode = "briefing"
	WorkAgentPassModeDraft    WorkAgentPassMode = "draft"
	WorkAgentPassModeFinalize WorkAgentPassMode = "finalize"
	WorkAgentPassModeRevision WorkAgentPassMode = "revision"
)

// BuildPreflightAdditionsForThreadWithOptions is the form the
// per-turn handler calls when the ChatStreamRequest carries
// per-turn-only signals (planMode for now; future signals join
// PreflightOptions). The zero-options form behaves identically
// to BuildPreflightAdditionsForThread above so legacy tests
// continue to pass.
func BuildPreflightAdditionsForThreadWithOptions(uid uint, skillName string, threadID uint, opts PreflightOptions) string {
	if skillName == "" {
		return ""
	}

	// G15 — start latency clock. Deferred metric covers BOTH the
	// fast no-op path (returned early below if everything degrades
	// to empty) and the full-load path; downstream queries can
	// percentile-bucket per-skill latency without distinguishing
	// the shapes. Side-files token cost is emitted from its own
	// load site so the two metrics stay independent — a slow
	// preflight isn't always a heavy-side-files preflight.
	preflightStart := time.Now()

	composer := prompts.SystemAdditionsComposer{
		LayerDisable: resolvePromptLayerDisableSet(),
	}

	if opts.PlanMode {
		composer.PlanModeProtocol = WorkAgentPlanModeProtocol
	}

	// Checklist preflight — flag-gated per (uid, skill).
	if config.GetWorkAgentFeatures().IsPreEmitGateEnabledForSkill(uid, skillName) {
		checklist := LoadChecklistForSkill("", skillName)
		if !checklist.IsEmpty() {
			composer.ChecklistDigest = PreflightDigest(checklist)
		}
	}

	// Asset-library injections — Backlog #11 (3/n + 9/n + 10/n);
	// Sprint-D 3/7 collapsed three near-identical loadXxxForOwner
	// flows into a single registry walk. The composer's typed
	// fields (BrandSpec / CharacterContext / DirectorStyle) keep
	// their Compose()-time precedence semantics; this loop just
	// fills them per-kind.
	//
	// Brand-spec (uid-scoped, cross-thread): when present, Compose()
	// prefers it over SelectedDirection. Empty for fresh users —
	// directions-picker fallback fires.
	// Character context: additive (lives within brand). Composer
	// slots it between visual-identity and skill side files.
	// Director-style: orthogonal cinematic fingerprint — composition,
	// color grading, lighting, motion, texture.
	for _, kind := range asset_library.AllKinds() {
		xml := loadActiveAssetXML(uid, kind)
		if xml == "" {
			continue
		}
		switch kind {
		case asset_library.AssetKindBrand:
			composer.BrandSpec = xml
		case asset_library.AssetKindCharacter:
			composer.CharacterContext = xml
		case asset_library.AssetKindDirectorStyle:
			composer.DirectorStyle = xml
		}
	}

	// Asset library index — Backlog #11 polish (agent-native parity).
	// Compact list of the user's other assets so the agent can
	// suggest them by slug instead of re-extracting. Skipped when
	// the user has no library at all.
	if libraryIndex := loadAssetLibraryIndex(uid); libraryIndex != "" {
		composer.AssetLibraryIndex = libraryIndex
	}

	// Skill side files — DS-2 preflight (F1, 2026-05-17). Loads
	// every file under skills/<skillName>/assets/ and wraps them
	// in a <skill-side-files> XML block. Design-facing skills can
	// ship template seeds, asset-contract examples, or optional
	// motion helpers here; skills without assets return empty and
	// the composer's TrimRight drops the layer cleanly. Pre-F1 this
	// injection path was dead — LoadSideFiles + composer field +
	// XML wrapper all existed but nothing wired them, so 222 lines
	// of authored prompt content sat dark on disk.
	if sideFiles := loadSkillSideFilesForPreflight(skillName); len(sideFiles) > 0 {
		composer.SkillSideFiles = skills.FormatSideFilesXML(sideFiles)
		// G15 — surface side-files cost per skill. approx_tokens
		// uses the ~4 chars / token heuristic (OpenAI BPE rule of
		// thumb); good enough for budget watching, not for billing.
		// Pinned by metrics_side_files_test.
		var totalBytes int
		for _, f := range sideFiles {
			totalBytes += len(f.Contents)
		}
		EmitMetric("wa_preflight_side_files", map[string]any{
			"skill":         skillName,
			"file_count":    len(sideFiles),
			"total_bytes":   totalBytes,
			"approx_tokens": totalBytes / 4,
		})
	}

	teamIDs := opts.TeamIDs
	if len(teamIDs) == 0 {
		teamIDs = LoadKnowledgeTeamIDsForUser(globals.GraDBs["system"], uid)
	}
	if knowledge := loadKnowledgeContextForPreflight(uid, skillName, opts.UserMessage, opts.ProjectID, teamIDs, opts.KnowledgeMetadata); knowledge != "" {
		composer.KnowledgeContext = knowledge
	}

	// Discovery context — surfaces inferred / form-submitted answers
	// from earlier turns. Always read regardless of preflight gate
	// status: discovery context aids the model whether or not the
	// gate is on.
	if threadID > 0 {
		// G10 (2026-05-17) — scoped to skillName so a skill switch
		// mid-thread doesn't drag the previous skill's answers into
		// the new skill's preflight.
		if discovery := loadDiscoveryContext(threadID, skillName); discovery != "" {
			composer.DiscoveryContext = discovery
		}
		// Previous-critique injection (P0 #3). When the user has
		// thumbs-down'd the last assistant reply AND typed feedback,
		// surface that feedback verbatim so the agent reads it
		// before the next turn rather than guessing from the new
		// user message. Staleness gate lives in
		// LoadActiveUserCritique — once the user continues the
		// thread, the critique stops surfacing.
		if critique := loadPreviousCritique(threadID); critique != "" {
			composer.PreviousCritique = critique
		}
		if passMode := loadPassModeProtocol(threadID); passMode != "" {
			composer.PassModeProtocol = passMode
		}
		// Visual direction — surfaces the deterministic OKLch +
		// font_stack tokens locked when the user picked from the
		// directions picker (Sprint C M2 UI). Mutually exclusive
		// with brand-spec in the composer (brand wins); both fields
		// can be populated, the composer's Compose() handles the
		// precedence.
		//
		// loadSelectedDirectionPair returns both the rendered
		// <visual-direction> XML and the matching design-system
		// markdown body so we can populate two composer fields
		// from one DB lookup.
		if directionXML, designSystem := loadSelectedDirectionPair(threadID); directionXML != "" {
			composer.SelectedDirection = directionXML
			if designSystem != "" {
				composer.DesignSystem = designSystem
			}
		}
	}

	out, trace := composer.ComposeWithTrace()
	if out != "" {
		globals.Info("[Preflight] system additions composed: " + layerTraceSummary(trace))
	}
	// G15 — per-preflight latency, emitted even when the composer
	// returned empty so the no-op path's overhead stays visible
	// (a regression that suddenly makes empty-preflights expensive
	// would otherwise go unnoticed).
	EmitMetric("wa_preflight_compose", map[string]any{
		"skill":       skillName,
		"thread_id":   threadID,
		"latency_ms":  time.Since(preflightStart).Milliseconds(),
		"output_len":  len(out),
		"layer_count": len(trace),
	})
	return out
}

func loadKnowledgeContextForPreflight(uid uint, skillName string, userMessage string, projectID uint, teamIDs []uint64, out *KnowledgeRetrievalMetadata) string {
	userMessage = strings.TrimSpace(userMessage)
	if userMessage == "" || globals.GraDBs == nil || globals.GraDBs["system"] == nil {
		return ""
	}
	result, err := RetrieveKnowledgeContext(nil, globals.GraDBs["system"], config.GetWorkAgentFeatures().KnowledgeRetrieverBackendName(), KnowledgeRetrievalOptions{
		Query:     userMessage,
		UID:       uid,
		ProjectID: projectID,
		TeamIDs:   teamIDs,
		AgentMode: skillName,
	})
	if err != nil {
		globals.Warn(fmt.Sprintf("[Preflight] knowledge retrieval skipped: %v", err))
		return ""
	}
	if result.Metadata.IsEmpty() {
		return ""
	}
	if out != nil {
		*out = result.Metadata
	}
	return result.ContextXML
}

func loadSkillSideFilesForPreflight(skillName string) []skills.SideFile {
	if !config.GetWorkAgentFeatures().IsSkillPreflightEnabled() {
		return nil
	}
	return skills.LoadSideFiles(skillName)
}

// resolvePromptLayerDisableSet reads the ops-configured layer
// disable list from the feature flag and translates the layer
// names (PromptLayersDisabled) to the PromptLayer enum values the
// composer expects. Unknown names are silently dropped so a typo
// in config never bricks composition.
//
// Builds the lookup table fresh on every call — the feature flag
// is read frequently elsewhere, and rebuilding a sub-10-entry
// map per turn is cheaper than the alternative (lock + memoise).
func resolvePromptLayerDisableSet() prompts.LayerDisableSet {
	names := config.GetWorkAgentFeatures().DisabledPromptLayers()
	if len(names) == 0 {
		return nil
	}
	byName := map[string]prompts.PromptLayer{}
	for _, l := range prompts.AllLayers() {
		byName[l.Name()] = l
	}
	out := prompts.LayerDisableSet{}
	for _, n := range names {
		if layer, ok := byName[n]; ok {
			out[layer] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// layerTraceSummary renders a compact, grep-friendly log line
// from the composer trace. One bracketed pair per contribution,
// e.g. "[identity:anti-slop] [protocols:brand-protocol] ...".
// Avoids dumping the entire (multi-KB) additions string into the
// log while still telling ops which layers / fields fired.
func layerTraceSummary(trace []prompts.LayerContribution) string {
	if len(trace) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(trace))
	for _, c := range trace {
		field := ""
		if len(c.Fields) > 0 {
			field = c.Fields[0]
		}
		if field == "" {
			parts = append(parts, "["+c.Layer.Name()+"]")
		} else {
			parts = append(parts, "["+c.Layer.Name()+":"+field+"]")
		}
	}
	return strings.Join(parts, " ")
}

// loadDiscoveryContext reads the most recent discovery_form_result
// metadata row for the SUPPLIED SKILL in this thread and renders
// it as the <discovery-context> XML block format consumed by
// SystemAdditionsComposer. Returns "" when no row exists for the
// skill or the metadata fails to parse — either case degrades
// silently so a corrupt row doesn't block the turn.
//
// G10 (2026-05-17) — skill-scoped lookup. Pre-G10 this read the
// latest discovery row regardless of which skill produced it; a
// user that started a ppt thread (audience=exec) then switched to
// socialAd had ppt's answers silently injected into socialAd's
// preflight. After G10 each skill's preflight only sees its own
// discovery row.
//
// Empty skillName preserves the legacy skill-agnostic read for
// tests + the rare call-site that doesn't have an agentMode in
// scope.
//
// Format mirrors the design's example (§v2.4 §3.6):
//
//	<discovery-context>
//	audience: exec
//	tone: modern_minimal
//	scale: medium
//	</discovery-context>
//
// We sort keys alphabetically so the output is deterministic across
// turns (helps SDK prompt-cache hits).
func loadDiscoveryContext(threadID uint, skillName string) string {
	repo := DefaultMessageRepository()
	if repo == nil {
		return ""
	}
	msg, err := repo.FindLatestDiscoveryAnswersForSkill(threadID, skillName)
	if err != nil || msg == nil {
		return ""
	}

	type discoveryMeta struct {
		Kind       string            `json:"kind"`
		FormID     string            `json:"form_id,omitempty"`
		Answers    map[string]string `json:"answers"`
		SkipReason string            `json:"skip_reason,omitempty"`
	}
	var meta discoveryMeta
	if err := json.Unmarshal([]byte(msg.Metadata), &meta); err != nil {
		return ""
	}
	if meta.Kind != "discovery_form_result" || len(meta.Answers) == 0 {
		return ""
	}

	keys := make([]string, 0, len(meta.Answers))
	for k := range meta.Answers {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString("<discovery-context>\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "%s: %s\n", k, meta.Answers[k])
	}
	if meta.SkipReason != "" {
		fmt.Fprintf(&b, "(captured via: %s)\n", meta.SkipReason)
	}
	b.WriteString("</discovery-context>")
	return b.String()
}

// loadPreviousCritique renders the active user critique (P0 #3) as
// the <previous-critique> XML block consumed by SystemAdditionsComposer.
// Returns "" when no critique is active (the common case — most
// threads never get a thumbs-down, OR the user has since continued
// past the critique). Never errors — a DB hiccup degrades to "no
// critique injection" rather than blocking the turn.
//
// Format chosen to match the existing block conventions (lowercase
// hyphenated tag, plain-text content body, single trailing newline
// inside the closing tag). The agent reads this as a verbatim user
// note, same way it reads discovery answers.
//
// Capping: we trust the rate handler's maxFeedbackBytes (4 KiB) —
// no second cap here. A row that exceeds 4 KiB shouldn't exist in
// the first place; defensively re-capping would silently truncate
// a row a future migration might legitimately have written longer.
func loadPreviousCritique(threadID uint) string {
	repo := DefaultMessageRepository()
	if repo == nil {
		return ""
	}
	critique, err := repo.LoadActiveUserCritique(threadID)
	if err != nil || critique == nil {
		return ""
	}
	feedback := strings.TrimSpace(critique.UserFeedback)
	if feedback == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("<previous-critique>\n")
	b.WriteString("The user disliked your previous response and asked you to change the following:\n")
	b.WriteString(feedback)
	b.WriteString("\n</previous-critique>")
	return b.String()
}

func loadPassModeProtocol(threadID uint) string {
	state := loadPassModeState(threadID)
	return formatPassModeProtocol(state)
}

type WorkAgentPassModeState struct {
	Mode                WorkAgentPassMode
	Source              string
	SelectedVariationID string
	SelectedArtifactID  string
	SelectedFileID      string
	DesignSystem        string
	AssetContract       string
}

func loadPassModeState(threadID uint) WorkAgentPassModeState {
	if threadID == 0 {
		return WorkAgentPassModeState{}
	}
	repo := DefaultMessageRepository()
	if repo == nil {
		return WorkAgentPassModeState{Mode: WorkAgentPassModeBriefing, Source: "default"}
	}
	msg, err := repo.FindMostRecentByMetadataKind(threadID, "workagent_pass_mode")
	if err != nil || msg == nil {
		return WorkAgentPassModeState{Mode: WorkAgentPassModeBriefing, Source: "default"}
	}
	var meta struct {
		Kind                string `json:"kind"`
		Mode                string `json:"mode"`
		Source              string `json:"source"`
		SelectedVariationID string `json:"selected_variation_id"`
		SelectedArtifactID  string `json:"selected_artifact_id"`
		SelectedFileID      string `json:"selected_file_id"`
		DesignSystem        string `json:"design_system_basename"`
		AssetContract       string `json:"asset_contract"`
	}
	if err := json.Unmarshal([]byte(msg.Metadata), &meta); err != nil {
		return WorkAgentPassModeState{Mode: WorkAgentPassModeBriefing, Source: "default"}
	}
	if meta.Kind != "workagent_pass_mode" {
		return WorkAgentPassModeState{Mode: WorkAgentPassModeBriefing, Source: "default"}
	}
	mode := normalizePassMode(meta.Mode)
	if mode == "" {
		return WorkAgentPassModeState{Mode: WorkAgentPassModeBriefing, Source: "default"}
	}
	source := strings.TrimSpace(meta.Source)
	if source == "" {
		source = "metadata"
	}
	return WorkAgentPassModeState{
		Mode:                mode,
		Source:              source,
		SelectedVariationID: strings.TrimSpace(meta.SelectedVariationID),
		SelectedArtifactID:  strings.TrimSpace(meta.SelectedArtifactID),
		SelectedFileID:      strings.TrimSpace(meta.SelectedFileID),
		DesignSystem:        strings.TrimSpace(meta.DesignSystem),
		AssetContract:       strings.TrimSpace(meta.AssetContract),
	}
}

func normalizePassMode(mode string) WorkAgentPassMode {
	switch WorkAgentPassMode(strings.TrimSpace(strings.ToLower(mode))) {
	case WorkAgentPassModeBriefing:
		return WorkAgentPassModeBriefing
	case WorkAgentPassModeDraft:
		return WorkAgentPassModeDraft
	case WorkAgentPassModeFinalize:
		return WorkAgentPassModeFinalize
	case WorkAgentPassModeRevision:
		return WorkAgentPassModeRevision
	default:
		return ""
	}
}

func formatPassModeProtocol(state WorkAgentPassModeState) string {
	mode := state.Mode
	if mode == "" {
		return ""
	}
	source := strings.TrimSpace(state.Source)
	if source == "" {
		source = "default"
	}
	var b strings.Builder
	b.WriteString("<pass-mode>\n")
	fmt.Fprintf(&b, "mode: %s\n", mode)
	fmt.Fprintf(&b, "source: %s\n", source)
	if state.SelectedVariationID != "" {
		fmt.Fprintf(&b, "selected_variation_id: %s\n", state.SelectedVariationID)
	}
	if state.SelectedArtifactID != "" {
		fmt.Fprintf(&b, "selected_artifact_id: %s\n", state.SelectedArtifactID)
	}
	if state.SelectedFileID != "" {
		fmt.Fprintf(&b, "selected_file_id: %s\n", state.SelectedFileID)
	}
	if state.DesignSystem != "" {
		fmt.Fprintf(&b, "design_system_basename: %s\n", state.DesignSystem)
	}
	if state.AssetContract != "" {
		fmt.Fprintf(&b, "asset_contract: %s\n", state.AssetContract)
	}
	b.WriteString("workflow:\n")
	switch mode {
	case WorkAgentPassModeBriefing:
		b.WriteString("- Treat this as Pass 1 unless the user explicitly asks for a final deliverable now.\n")
		b.WriteString("- Output assumptions, missing inputs, safe placeholders, and 2-3 distinct design directions.\n")
		b.WriteString("- If enough context exists to choose among directions, include a variations_picker block using conservative/balanced/bold ids.\n")
		b.WriteString("- Do not present a polished final artifact as accepted work in this pass.\n")
	case WorkAgentPassModeDraft:
		b.WriteString("- Turn confirmed answers into draft artifacts and variation candidates.\n")
		b.WriteString("- Keep drafts previewable and clearly label unresolved assumptions or placeholders.\n")
		b.WriteString("- Materialize every variation as a draft file under outputs/ before asking the user to choose. Also write outputs/.workagent/pass_1_variations.json with {\"variations\":[{\"id\":\"conservative|balanced|bold\",\"label\":\"...\",\"stance\":\"...\",\"file_path\":\"relative/path.ext\",\"design_system_basename\":\"...\",\"asset_contract\":\"...\"}]} so the platform can bind picker cards to artifact drafts.\n")
		b.WriteString("- Emit a variations_picker content block when offering choices. Shape: {\"type\":\"variations_picker\",\"picker_id\":\"pass_1_variations\",\"schema\":{\"variations\":[{\"id\":\"conservative\",\"label\":\"Conservative\",\"stance\":\"conservative\",\"description\":\"...\"},{\"id\":\"balanced\",\"label\":\"Balanced\",\"stance\":\"balanced\",\"description\":\"...\"},{\"id\":\"bold\",\"label\":\"Bold\",\"stance\":\"bold\",\"description\":\"...\"}]}}.\n")
		b.WriteString("- Ask for a variation choice before finalizing unless the user already selected one.\n")
	case WorkAgentPassModeFinalize:
		b.WriteString("- Produce the final selected variation and lock its design system and asset contract.\n")
		if state.SelectedVariationID != "" {
			fmt.Fprintf(&b, "- Treat variation %q as the selected direction; do not re-open variation choice unless the user asks.\n", state.SelectedVariationID)
		}
		if state.SelectedArtifactID != "" || state.SelectedFileID != "" {
			b.WriteString("- Use the selected draft artifact/file as the source of truth for this final pass; preserve its intent and only refine toward delivery quality.\n")
		}
		b.WriteString("- Avoid introducing a new direction unless the user explicitly changes the selection.\n")
		b.WriteString("- Make export/reuse readiness visible in the artifact notes.\n")
	case WorkAgentPassModeRevision:
		b.WriteString("- Revise the existing artifact version using critique, validation, or comparison notes.\n")
		b.WriteString("- Preserve the chosen direction unless the requested fix requires a scoped change.\n")
		b.WriteString("- Register the result as the next artifact version rather than replacing history.\n")
	}
	b.WriteString("</pass-mode>")
	return b.String()
}

func PersistPassMode(uid, threadID uint, mode WorkAgentPassMode, source string, selectedVariationID ...string) bool {
	state := WorkAgentPassModeState{
		Mode:   mode,
		Source: source,
	}
	if len(selectedVariationID) > 0 {
		state.SelectedVariationID = selectedVariationID[0]
	}
	return PersistPassModeState(uid, threadID, state)
}

func PersistPassModeState(uid, threadID uint, state WorkAgentPassModeState) bool {
	if uid == 0 || threadID == 0 {
		return false
	}
	mode := normalizePassMode(string(state.Mode))
	if mode == "" {
		return false
	}
	source := strings.TrimSpace(state.Source)
	if strings.TrimSpace(source) == "" {
		source = "system"
	}
	meta := map[string]interface{}{
		"kind":   "workagent_pass_mode",
		"mode":   string(mode),
		"source": strings.TrimSpace(source),
	}
	if strings.TrimSpace(state.SelectedVariationID) != "" {
		meta["selected_variation_id"] = strings.TrimSpace(state.SelectedVariationID)
	}
	if strings.TrimSpace(state.SelectedArtifactID) != "" {
		meta["selected_artifact_id"] = strings.TrimSpace(state.SelectedArtifactID)
	}
	if strings.TrimSpace(state.SelectedFileID) != "" {
		meta["selected_file_id"] = strings.TrimSpace(state.SelectedFileID)
	}
	if strings.TrimSpace(state.DesignSystem) != "" {
		meta["design_system_basename"] = strings.TrimSpace(state.DesignSystem)
	}
	if strings.TrimSpace(state.AssetContract) != "" {
		meta["asset_contract"] = strings.TrimSpace(state.AssetContract)
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		globals.Warn(fmt.Sprintf("[Preflight] marshal pass mode failed: %v", err))
		return false
	}
	if err := DefaultMessageRepository().CreateAgentMessage(newDiscoveryMarkerMessage(uid, threadID, string(metaJSON))); err != nil {
		globals.Warn(fmt.Sprintf("[Preflight] persist pass mode failed: %v", err))
		return false
	}
	globals.Info(fmt.Sprintf("[Preflight] persisted pass mode: thread=%d mode=%s source=%s", threadID, mode, source))
	return true
}

// loadSelectedDirection scans the thread for a visual_direction_selected
// metadata row and returns the rendered <visual-direction> XML when
// present. Empty string when no selection has been made.
//
// The selection row is written by PersistSelectedDirection (Sprint-C
// M2 UI calls this when the user picks from the directions picker).
// Multiple selections in the same thread (user changes mind) → the
// LATEST one wins.
func loadSelectedDirection(threadID uint) string {
	xml, _ := loadSelectedDirectionPair(threadID)
	return xml
}

// loadSelectedDirectionPair returns BOTH the rendered
// <visual-direction> XML and the matching design-system markdown
// body in one call. Used by BuildPreflightAdditionsForThread to
// populate composer.SelectedDirection and composer.DesignSystem
// from a single DB lookup.
//
// Either return value may be empty if the corresponding piece is
// missing (e.g. direction selected but ds_link unknown). The
// caller decides which composer fields to set.
func loadSelectedDirectionPair(threadID uint) (xml, designSystemBody string) {
	repo := DefaultMessageRepository()
	if repo == nil {
		return "", ""
	}
	msg, err := repo.FindMostRecentByMetadataKind(threadID, "visual_direction_selected")
	if err != nil || msg == nil {
		return "", ""
	}

	var meta struct {
		Kind                 string `json:"kind"`
		DirectionID          string `json:"direction_id"`
		DesignSystemBasename string `json:"design_system_basename"`
		DesignSystemTitle    string `json:"design_system_title"`
		DesignSystemDerived  string `json:"design_system_derived_from"`
		Source               string `json:"source"`
	}
	if err := json.Unmarshal([]byte(msg.Metadata), &meta); err != nil {
		return "", ""
	}
	if meta.Kind != "visual_direction_selected" || meta.DirectionID == "" {
		return "", ""
	}
	dir := skills.FindDirection(meta.DirectionID)
	if dir == nil {
		return "", ""
	}
	xml = skills.FormatDirectionXML(dir)

	// Prefer the persisted design_system_basename so a selection locks
	// the concrete system used by future turns even if the direction's
	// ds_link changes in a later deploy. Older rows fall back to ds_link.
	if meta.DesignSystemBasename != "" {
		if ds, err := skills.LoadDesignSystem(meta.DesignSystemBasename); err == nil && ds != nil {
			designSystemBody = ds.Body
			emitDesignSystemUsedMetric(msg.UID, threadID, meta.DirectionID, meta.Source, ds)
			return xml, designSystemBody
		}
	}
	if ds, err := skills.LoadDesignSystemForDirection(dir); err == nil && ds != nil {
		designSystemBody = ds.Body
		emitDesignSystemUsedMetric(msg.UID, threadID, meta.DirectionID, meta.Source, ds)
	}
	return xml, designSystemBody
}

func emitDesignSystemUsedMetric(uid int, threadID uint, directionID string, source string, ds *skills.DesignSystem) {
	if ds == nil {
		return
	}
	EmitMetric("wa_design_system_used", map[string]any{
		"uid":                    uid,
		"thread_id":              threadID,
		"direction_id":           strings.TrimSpace(directionID),
		"source":                 strings.TrimSpace(source),
		"design_system_basename": ds.Basename,
		"design_system_title":    skills.DesignSystemTitle(ds),
		"design_system_derived":  ds.DerivedFrom,
	})
}

// PersistSelectedDirection writes a synthetic chat_message row
// recording the user's chosen visual direction. Read back by
// loadSelectedDirection on subsequent turns so the OKLch + font_stack
// tokens stay in the system prompt across the conversation.
//
// directionID must match an id in
// _shared/visual-directions.yaml (validated against
// skills.FindDirection); unknown ids return false to avoid poisoning
// the lookup with bad references.
//
// source identifies the selection origin (e.g. "user_picker",
// "nlp_inferred_from_brief", "fork_from_brand"). Logged for
// dashboard analytics.
func PersistSelectedDirection(uid, threadID uint, directionID, source string) bool {
	if uid == 0 || threadID == 0 || directionID == "" {
		return false
	}
	dir := skills.FindDirection(directionID)
	if dir == nil {
		globals.Warn(fmt.Sprintf("[Preflight] reject unknown direction id %q", directionID))
		return false
	}
	if source == "" {
		source = "user_picker"
	}

	meta := map[string]interface{}{
		"kind":         "visual_direction_selected",
		"direction_id": directionID,
		"source":       source,
	}
	if ds, err := skills.LoadDesignSystemForDirection(dir); err == nil && ds != nil {
		meta["design_system_basename"] = ds.Basename
		meta["design_system_title"] = skills.DesignSystemTitle(ds)
		meta["design_system_derived_from"] = ds.DerivedFrom
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		globals.Warn(fmt.Sprintf("[Preflight] marshal direction selection failed: %v", err))
		return false
	}
	if err := DefaultMessageRepository().CreateAgentMessage(newDiscoveryMarkerMessage(uid, threadID, string(metaJSON))); err != nil {
		globals.Warn(fmt.Sprintf("[Preflight] persist direction selection failed: %v", err))
		return false
	}
	EmitMetric("wa_design_system_selected", designSystemSelectionMetricFields(uid, threadID, directionID, source, meta))
	globals.Info(fmt.Sprintf("[Preflight] persisted direction selection: thread=%d direction=%s source=%s", threadID, directionID, source))
	return true
}

func designSystemSelectionMetricFields(uid uint, threadID uint, directionID string, source string, meta map[string]interface{}) map[string]any {
	fields := map[string]any{
		"uid":          uid,
		"thread_id":    threadID,
		"direction_id": strings.TrimSpace(directionID),
		"source":       strings.TrimSpace(source),
	}
	if basename, ok := meta["design_system_basename"].(string); ok && strings.TrimSpace(basename) != "" {
		fields["design_system_basename"] = strings.TrimSpace(basename)
	}
	if title, ok := meta["design_system_title"].(string); ok && strings.TrimSpace(title) != "" {
		fields["design_system_title"] = strings.TrimSpace(title)
	}
	if derived, ok := meta["design_system_derived_from"].(string); ok && strings.TrimSpace(derived) != "" {
		fields["design_system_derived"] = strings.TrimSpace(derived)
	}
	return fields
}

func SelectedDirectionResponse(directionID string) map[string]interface{} {
	out := map[string]interface{}{
		"persisted":    true,
		"direction_id": directionID,
	}
	dir := skills.FindDirection(directionID)
	if dir == nil {
		return out
	}
	if ds, err := skills.LoadDesignSystemForDirection(dir); err == nil && ds != nil {
		out["design_system"] = map[string]interface{}{
			"basename":    ds.Basename,
			"title":       skills.DesignSystemTitle(ds),
			"derivedFrom": ds.DerivedFrom,
		}
	}
	return out
}

// HasBrandContext reports whether the current user has a brand
// asset in scope. Reads the platform w_global_brand table via
// brand.Default().FindLatestForOwner — brands are uid-scoped and
// cross-thread reusable, so the threadID arg is no longer
// consulted (kept on the signature for call-site stability).
//
// Errors and uid==0 degrade to "no brand" so the M2 directions
// picker errs on the side of "show picker" rather than "silently
// suppress."
//
// Sprint-E close-out: the pre-Backlog-#11 metadata-marker fallback
// (chat_message metadata scan for "brand_spec_extracted" /
// "brand_asset_picked" kinds) was retired here. Every user that
// previously had a metadata-only brand has had a year + several
// migration windows to extract a real brand_asset row; the
// fallback no longer earned its complexity cost.
func HasBrandContext(uid, threadID uint) bool {
	_ = threadID // kept on the signature; no longer consulted
	if uid == 0 || globals.GraDBs == nil || globals.GraDBs["system"] == nil {
		return false
	}
	asset, err := brand.Default().FindLatestForOwner(uid)
	return err == nil && asset != nil
}

// loadActiveAssetXML reads the user's latest CONFIRMED asset of the
// given kind via the registry, returning the descriptor's
// FormatXML output or "" when nothing surfaceable exists. Sprint-D
// 3/7 — collapses loadBrandSpecForOwner / loadCharacterContextForOwner /
// loadDirectorStyleContextForOwner into one registry walk. The
// per-type variants now thin-alias to this; Phase 6 removes the
// aliases once test call sites migrate.
//
// Best-effort: any failure path (uid=0, null DB, kind not registered,
// no row, lookup error) returns "" and the composer treats the asset
// as absent. The hot-path posture mirrors loadDiscoveryContext /
// loadSelectedDirectionPair — a turn never blocks on a preflight read.
//
// Drafts are NOT surfaced — preflight only injects fully-vetted
// (confirmed) assets to keep the SystemAdditions block trustworthy.
// The (future) draft watermark path will be a separate composer
// field so the model can distinguish "user-confirmed" from "best-
// guess extraction" without leaking unvetted content into the prompt.
func loadActiveAssetXML(uid uint, kind asset_library.AssetKind) string {
	if uid == 0 {
		return ""
	}
	if globals.GraDBs == nil || globals.GraDBs["system"] == nil {
		return ""
	}
	d, err := asset_library.Default().Get(kind)
	if err != nil {
		return ""
	}
	asset, err := d.LoadLatestActive(uid)
	if err != nil || asset == nil {
		return ""
	}
	return d.FormatXML(asset)
}

// loadAssetLibraryIndex returns a compact list of the user's
// non-archived assets across all three types (brand / character /
// director-style). The agent reads it to know what's available
// beyond the active asset already injected — e.g. when the user
// says "use my acme-corp brand" the agent can map by slug instead
// of asking for clarification or re-extracting.
//
// Format:
//
//	<asset-library-index>
//	brands:
//	  - acme-corp (confirmed)
//	  - blueberry (draft)
//	characters:
//	  - lin-mei (confirmed)
//	director_styles:
//	  (none)
//	</asset-library-index>
//
// Empty subsections render "(none)" so the agent can distinguish
// "no characters" from "characters list missing entirely." The
// whole block omits when ALL three types are empty (fresh user).
//
// Reads cap at 10 per type to keep prompt tokens predictable on
// power users with deep libraries; the agent rarely needs more
// than the recent set, and a follow-up tool call can fetch deeper
// when needed.
func loadAssetLibraryIndex(uid uint) string {
	if uid == 0 {
		return ""
	}
	if globals.GraDBs == nil || globals.GraDBs["system"] == nil {
		return ""
	}

	// Sprint-D 3/7 — walks asset_library.Default().All() instead
	// of three hard-coded repo branches. Adding a fourth asset
	// type now lights up here automatically as soon as its
	// descriptor registers.
	const maxPerType = 10
	type section struct {
		label string
		rows  []asset_library.LibraryAsset
	}
	sections := make([]section, 0, len(asset_library.AllKinds()))
	hasAny := false
	for _, d := range asset_library.Default().All() {
		rows, _ := d.List(uid, maxPerType, 0)
		if len(rows) > 0 {
			hasAny = true
		}
		sections = append(sections, section{label: d.IndexLabel(), rows: rows})
	}
	if !hasAny {
		return ""
	}

	var b strings.Builder
	b.WriteString("<asset-library-index>\n")
	for _, s := range sections {
		fmt.Fprintf(&b, "%s:\n", s.label)
		if len(s.rows) == 0 {
			b.WriteString("  (none)\n")
			continue
		}
		for _, row := range s.rows {
			fmt.Fprintf(&b, "  - %s (%s)\n", librarySlug(row), row.GetStatus())
		}
	}
	b.WriteString("</asset-library-index>")
	return b.String()
}

// librarySlug picks the most stable identifier for index listings —
// slug is the user-authored handle; falls back to name then numeric
// id so a row without slug still surfaces. Sprint-D 3/7 — collapses
// brandLibrarySlug / characterLibrarySlug / directorStyleLibrarySlug
// into one helper that operates on the LibraryAsset interface.
func librarySlug(a asset_library.LibraryAsset) string {
	if slug := a.GetSlug(); slug != "" {
		return slug
	}
	if name := a.GetName(); name != "" {
		return name
	}
	return fmt.Sprintf("#%d", a.GetID())
}

func writeCreativeAssetContractHeader(out *strings.Builder, kind string, confirmed bool) {
	fmt.Fprintf(out, "asset_kind: %s\n", strings.TrimSpace(kind))
	out.WriteString("contract_schema: creative_asset_contract.v1\n")
	if confirmed {
		out.WriteString("contract_status: confirmed\n")
		return
	}
	out.WriteString("contract_status: draft_unconfirmed\n")
}

// formatDirectorStyleXML renders a model.DirectorStyle row (Sprint-E
// platform table) into the <director-style> XML block. Identity
// (name/era/genre) leads; the 5 cinematic axes follow as JSON lines
// (composition / color / lighting / motion / texture). Reference
// imagery now lives in w_global_director_style_reference, so this
// formatter joins the first few non-deleted rows and emits them as
// compact reference lines for prompt grounding.
//
// Same drop-empty / drop-null / drop-{} posture as the brand-spec
// formatter — populated axes ride, missing axes drop cleanly.
func formatDirectorStyleXML(d *model.DirectorStyle) string {
	if d == nil {
		return ""
	}
	var out strings.Builder
	out.WriteString("<director-style>\n")
	writeCreativeAssetContractHeader(&out, "director_style", d.Confirmed)
	if d.Name != "" {
		fmt.Fprintf(&out, "name: %s\n", d.Name)
	}
	if d.Slug != "" {
		fmt.Fprintf(&out, "slug: %s\n", d.Slug)
	}
	if d.Era != "" {
		fmt.Fprintf(&out, "era: %s\n", d.Era)
	}
	if d.Genre != "" {
		fmt.Fprintf(&out, "genre: %s\n", d.Genre)
	}
	for _, axis := range []struct {
		label string
		value model.JSONMap
	}{
		{"composition", d.Composition},
		{"color", d.Color},
		{"lighting", d.Lighting},
		{"motion", d.Motion},
		{"texture", d.Texture},
	} {
		if len(axis.value) == 0 {
			continue
		}
		raw, err := json.Marshal(axis.value)
		if err != nil || len(raw) == 0 || string(raw) == "null" || string(raw) == "{}" {
			continue
		}
		fmt.Fprintf(&out, "%s: %s\n", axis.label, string(raw))
	}
	if d.PromptSuffix != "" {
		fmt.Fprintf(&out, "prompt_suffix: %s\n", strings.TrimSpace(d.PromptSuffix))
	}
	if d.NegativePrompt != "" {
		fmt.Fprintf(&out, "negative_prompt: %s\n", strings.TrimSpace(d.NegativePrompt))
	}
	for _, ref := range loadDirectorStyleReferenceLines(d) {
		fmt.Fprintf(&out, "reference: %s\n", ref)
	}
	if !d.Confirmed {
		out.WriteString("status: [待品牌方确认]\n")
	}
	out.WriteString("</director-style>")
	return out.String()
}

func loadDirectorStyleReferenceLines(d *model.DirectorStyle) []string {
	if d == nil || d.Id == 0 || d.UID == 0 {
		return nil
	}
	db, ok := globals.GraDBs["system"]
	if !ok || db == nil {
		return nil
	}
	var refs []model.DirectorStyleReference
	if err := db.
		Where("director_style_id = ? AND uid = ? AND deleted_at IS NULL", d.Id, d.UID).
		Order("sort_order ASC, id ASC").
		Limit(6).
		Find(&refs).Error; err != nil {
		globals.Warn(fmt.Sprintf("[Preflight] load director-style references failed style=%d uid=%d: %v", d.Id, d.UID, err))
		return nil
	}
	lines := make([]string, 0, len(refs))
	for _, ref := range refs {
		imageURL := strings.TrimSpace(ref.ImageURL)
		if imageURL == "" {
			continue
		}
		parts := []string{imageURL}
		if ref.ReferenceType != "" {
			parts = append(parts, "type="+ref.ReferenceType)
		}
		if label := strings.TrimSpace(ref.Label); label != "" {
			parts = append(parts, "label="+label)
		}
		lines = append(lines, strings.Join(parts, " | "))
	}
	return lines
}

// formatCharacterContextXML renders a CharacterAsset row into the
// <character-context> XML block. Identity (name/slug/role_type) leads;
// appearance description follows. avatar_image_url rides as a path
// string — image-gen pipelines that need to load the file resolve
// it against the workspace root themselves.
//
// Sprint-E note: takes *model.Character now (workagent
// CharacterAsset folded into the platform table). Field mapping
// across the workagent → platform schema migration:
//   - Description (workagent) → Appearance + Personality (platform).
//     Both surface in the <character-context> block now: Appearance
//     emits as "description:" (visual one-liner the prompt directly
//     consumes), Personality emits as "personality:" when present
//     (richer behavioural pattern the model uses for voice / tone).
//     Both drop cleanly when empty so partial rows stay tight.
//   - Traits (workagent JSON) → IdentityAnchors (platform JSON).
//     Same wire shape, different name; the formatter emits the
//     6-layer anchors as "traits:" for prompt-stack stability.
//   - CanonicalImagePath (workagent) → AvatarImageURL (platform).
//
// prompt_suffix / negative_prompt surface verbatim so the model
// can append them to its own prompt synthesis.
func formatCharacterContextXML(c *model.Character) string {
	if c == nil {
		return ""
	}
	var out strings.Builder
	out.WriteString("<character-context>\n")
	writeCreativeAssetContractHeader(&out, "character", c.Confirmed)
	if c.Name != "" {
		fmt.Fprintf(&out, "name: %s\n", c.Name)
	}
	if c.Slug != "" {
		fmt.Fprintf(&out, "slug: %s\n", c.Slug)
	}
	if c.RoleType != "" {
		fmt.Fprintf(&out, "role: %s\n", c.RoleType)
	}
	if c.Gender != "" {
		fmt.Fprintf(&out, "gender: %s\n", c.Gender)
	}
	if c.AgeRange != "" {
		fmt.Fprintf(&out, "age_range: %s\n", c.AgeRange)
	}
	if c.Appearance != "" {
		fmt.Fprintf(&out, "description: %s\n", strings.TrimSpace(c.Appearance))
	}
	if c.Personality != "" {
		fmt.Fprintf(&out, "personality: %s\n", strings.TrimSpace(c.Personality))
	}
	if len(c.IdentityAnchors) > 0 {
		if raw, err := json.Marshal(c.IdentityAnchors); err == nil &&
			len(raw) > 0 && string(raw) != "null" && string(raw) != "{}" {
			fmt.Fprintf(&out, "traits: %s\n", string(raw))
		}
	}
	if c.AvatarImageURL != "" {
		fmt.Fprintf(&out, "reference_image: %s\n", c.AvatarImageURL)
	}
	if c.PromptSuffix != "" {
		fmt.Fprintf(&out, "prompt_suffix: %s\n", strings.TrimSpace(c.PromptSuffix))
	}
	if c.NegativePrompt != "" {
		fmt.Fprintf(&out, "negative_prompt: %s\n", strings.TrimSpace(c.NegativePrompt))
	}
	if !c.Confirmed {
		out.WriteString("status: [待品牌方确认]\n")
	}
	out.WriteString("</character-context>")
	return out.String()
}

// brandSpecPromptBudgetChars is the soft cap on the rendered
// <brand-spec> block's character count (B3, 2026-05-16). At 4 chars
// ≈ 1 prompt token, 4000 chars ≈ 1000 tokens, which is the upper
// bound we want a single visual-identity layer to consume out of
// the per-turn ~200K-token budget.
//
// "Soft" because we never truncate mid-section — sections emit
// whole or not at all. A user with a heavyweight `colors` map that
// pushes the budget by itself still gets `colors` emitted; the next
// section just doesn't fit. Order in fullBrandSpecSections below
// is the priority — earlier sections are emitted first; later ones
// drop when budget runs out.
const brandSpecPromptBudgetChars = 4000

// brandSpecSection is one (label, value) pair the emit loop walks.
// Hoisted out of formatBrandSpecXML so the priority order is
// readable as data, not as code shape.
type brandSpecSection struct {
	label string
	value model.JSONMap
}

// formatBrandSpecXML renders a model.Brand row (Sprint-E platform
// table) into the <brand-spec> XML block the prompt composer
// expects. Format mirrors the M4 9-section schema — only sections
// present in the row get emitted; missing sections drop cleanly
// without "empty" placeholder noise.
//
// Sprint-E note: the type changed from *workagentModel.BrandAsset
// to *model.Brand. JSONMap (map[string]interface{}) sections marshal
// to JSON for emission rather than carrying raw []byte through the
// row — same wire shape the model reads.
//
// B3 (2026-05-16) — token budget. Brand-heavy users were shipping
// 3-10 KB of brand JSON per turn. Sections now emit in priority
// order (voice → colors → typography → motion → layout → spacing →
// components). When the running render would push past
// brandSpecPromptBudgetChars, remaining sections are replaced with
// a single `omitted: [section1, section2, ...]` line so the agent
// knows what it didn't read and can ask the user to drill in. The
// running budget is checked before each section append — we never
// truncate mid-section, since a partial JSON object would confuse
// the model more than its absence.
//
// Priority rationale: `voice` is direction (the agent's instruction
// layer) and `colors` is the most-referenced visual constraint in
// real prompts. Spacing/components/layout are downstream — most
// brand-aware prompts reach for them only on follow-up turns.
func formatBrandSpecXML(b *model.Brand) string {
	if b == nil {
		return ""
	}
	var out strings.Builder
	out.WriteString("<brand-spec>\n")
	writeCreativeAssetContractHeader(&out, "brand", b.Confirmed)
	if b.Name != "" {
		fmt.Fprintf(&out, "name: %s\n", b.Name)
	}
	if b.Slug != "" {
		fmt.Fprintf(&out, "slug: %s\n", b.Slug)
	}
	// Priority order: voice (direction) → colors (most-referenced
	// visual constraint) → typography → motion → layout → spacing
	// → components. When budget runs out, downstream sections drop
	// and an `omitted:` line lists them so the model can ask.
	sections := []brandSpecSection{
		{"voice", b.Voice},
		{"colors", b.Colors},
		{"typography", b.Typography},
		{"motion", b.Motion},
		{"layout", b.Layout},
		{"spacing", b.Spacing},
		{"components", b.Components},
	}
	var omitted []string
	for _, section := range sections {
		if len(section.value) == 0 {
			continue
		}
		raw, err := json.Marshal(section.value)
		if err != nil || len(raw) == 0 || string(raw) == "null" || string(raw) == "{}" {
			continue
		}
		// Pre-check: would appending push us past the soft cap?
		// `+1` accounts for the trailing newline.
		projected := out.Len() + len(section.label) + len(raw) + 3 // ": " + "\n"
		if projected > brandSpecPromptBudgetChars {
			omitted = append(omitted, section.label)
			continue
		}
		fmt.Fprintf(&out, "%s: %s\n", section.label, string(raw))
	}
	if len(omitted) > 0 {
		fmt.Fprintf(&out, "omitted: [%s]\n", strings.Join(omitted, ", "))
	}
	if !b.Confirmed {
		// Per the M4 protocol §"Vocalize" rule — drafts ride with a
		// watermark. The strict FindLatestActiveForOwner read
		// filters Confirmed=true so this branch is dormant on the
		// hot path, but stays in for defensive symmetry with the
		// character formatter.
		out.WriteString("status: [待品牌方确认]\n")
	}
	out.WriteString("</brand-spec>")
	return out.String()
}

// formatProductSpecXML renders a model.Product row into the
// <product-context> XML block the prompt composer consumes.
// Format parallel to brand-spec: identity columns (name / slug /
// sku / category) as plain key:value lines; JSON sections
// (specs / visual_guidance / target_audience) as one JSON-encoded
// line each so partial extractions drop cleanly.
//
// Note the tag name — <product-context> not <product-spec> —
// because Product is more about WHAT IS BEING SHOWN than about
// the brand's visual identity. The agent reads it as "here's the
// thing to feature in the shot" alongside the brand's visual
// language.
func formatProductSpecXML(p *model.Product) string {
	if p == nil {
		return ""
	}
	var out strings.Builder
	out.WriteString("<product-context>\n")
	writeCreativeAssetContractHeader(&out, "product", p.Confirmed)
	if p.Name != "" {
		fmt.Fprintf(&out, "name: %s\n", p.Name)
	}
	if p.Slug != "" {
		fmt.Fprintf(&out, "slug: %s\n", p.Slug)
	}
	if p.SKU != "" {
		fmt.Fprintf(&out, "sku: %s\n", p.SKU)
	}
	if p.Category != "" {
		fmt.Fprintf(&out, "category: %s\n", p.Category)
	}
	if desc := strings.TrimSpace(p.Description); desc != "" {
		// Description is free-form prose. Cap at 800 chars to
		// keep the prompt block bounded — the agent doesn't need
		// the full pitch deck, just the load-bearing selling
		// points.
		if len([]rune(desc)) > 800 {
			desc = string([]rune(desc)[:800]) + "…"
		}
		fmt.Fprintf(&out, "description: %s\n", desc)
	}
	for _, section := range []struct {
		label string
		value model.JSONMap
	}{
		{"specs", p.Specs},
		{"visual_guidance", p.VisualGuidance},
		{"target_audience", p.TargetAudience},
	} {
		if len(section.value) == 0 {
			continue
		}
		raw, err := json.Marshal(section.value)
		if err != nil || len(raw) == 0 || string(raw) == "null" || string(raw) == "{}" {
			continue
		}
		fmt.Fprintf(&out, "%s: %s\n", section.label, string(raw))
	}
	if !p.Confirmed {
		out.WriteString("status: [待商家确认]\n")
	}
	out.WriteString("</product-context>")
	return out.String()
}

// PersistDiscoveryAnswers writes a synthetic chat_message row carrying
// the form answers as discovery_form_result metadata. Used by both the
// M1 NLP pre-scan (via PersistInferredDiscoveryAnswers) and the user
// submission path (POST /threads/:id/discovery — agent_discovery_api.go).
//
// formID identifies which question_form definition produced the
// answers. Empty string defaults to "discovery" so the legacy NLP
// path stays bit-compatible. Frontend reads this back via the
// discovery-context block in SystemAdditions; the value is metadata-
// only, no schema lookup off it.
//
// skipReason categorises the submission origin:
//   - "" / "user_submitted" — user actively answered all questions
//   - "user_skipped"        — user clicked Skip; defaults filled in
//   - "nlp_inferred"        — NLP pre-scan path
//   - "context_inferred"    — future: brand/asset-driven defaults
//
// Returns true only when the marker row was inserted. Errors are logged
// but not returned in detail — hot-path agent flows can continue without
// discovery context, while API submit paths can still reject a failed
// write instead of accepting stale state.
func PersistDiscoveryAnswers(uid, threadID uint, formID string, answers map[string]string, skipReason string) bool {
	if uid == 0 || threadID == 0 || len(answers) == 0 {
		return false
	}
	if formID == "" {
		formID = "discovery"
	}
	if skipReason == "" {
		skipReason = "user_submitted"
	}

	meta := map[string]interface{}{
		"kind":        "discovery_form_result",
		"form_id":     formID,
		"answers":     answers,
		"skip_reason": skipReason,
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		globals.Warn(fmt.Sprintf("[Preflight] marshal discovery answers failed: %v", err))
		return false
	}

	if err := DefaultMessageRepository().CreateAgentMessage(newDiscoveryMarkerMessage(uid, threadID, string(metaJSON))); err != nil {
		globals.Warn(fmt.Sprintf("[Preflight] persist discovery answers failed: %v", err))
		return false
	}
	globals.Info(fmt.Sprintf("[Preflight] persisted discovery: thread=%d form=%s answers=%d source=%s",
		threadID, formID, len(answers), skipReason))
	// G15 — per-skill discovery outcome. `source` carries the
	// origin (user_submitted / user_skipped / nlp_inferred /
	// context_inferred); downstream queries can rate-by-skill to
	// surface "this skill's wizard is being skipped 80% of the
	// time" without grep-parsing the text log. form_id == skill
	// per the G10/G11 convention; emitted as `skill` for query
	// uniformity with the preflight events.
	EmitMetric("wa_discovery_persist", map[string]any{
		"skill":        formID,
		"thread_id":    threadID,
		"answer_count": len(answers),
		"source":       skipReason,
	})
	return true
}

// PersistInferredDiscoveryAnswers is the NLP pre-scan call site's
// stable entry point. Pre-DS-2 it was the only persistence path; the
// new user-submission endpoint shares the underlying writer via
// PersistDiscoveryAnswers so both flows produce structurally identical
// rows for the preflight reader.
//
// G10 (2026-05-17) — the NLP path now takes `skillName` so the
// persisted row's form_id matches the skill that generated the
// inference. Empty skillName falls back to the legacy constant
// "discovery" so legacy call sites (one-off scripts, tests) keep
// working unchanged.
func PersistInferredDiscoveryAnswers(uid, threadID uint, skillName string, answers map[string]string, source string) {
	if source == "" {
		source = "nlp_inferred"
	}
	formID := skillName
	if formID == "" {
		formID = "discovery"
	}
	PersistDiscoveryAnswers(uid, threadID, formID, answers, source)
}
