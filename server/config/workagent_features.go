package config

// WorkAgentFeatures is kept for backward-compatible config unmarshalling.
// Work-agent product features are now always on for authenticated users;
// helper methods below intentionally ignore these rollout fields.
//
// Historical note: these fields used to drive staged rollout. They are
// still accepted in YAML so old config files do not fail to unmarshal,
// but they no longer disable or scope the runtime paths.
type WorkAgentFeatures struct {
	// QuestionForm — turn-1 discovery form short-circuit (M1).
	QuestionFormEnabled      bool   `mapstructure:"question_form_enabled" json:"question_form_enabled" yaml:"question_form_enabled"`
	QuestionFormUIDWhitelist []uint `mapstructure:"question_form_uid_whitelist" json:"question_form_uid_whitelist" yaml:"question_form_uid_whitelist"`

	// DirectionsFallback — visual directions picker when no brand /
	// director / template is available (M2).
	DirectionsFallbackEnabled      bool   `mapstructure:"directions_fallback_enabled" json:"directions_fallback_enabled" yaml:"directions_fallback_enabled"`
	DirectionsFallbackUIDWhitelist []uint `mapstructure:"directions_fallback_uid_whitelist" json:"directions_fallback_uid_whitelist" yaml:"directions_fallback_uid_whitelist"`

	// PreEmitGate — pre-emit checklist + critique gate (M3 / M5).
	// Granular per-skill via PreEmitGateSkills: only listed skill
	// names get gated. Empty list = no gating (defensive default).
	PreEmitGateEnabled      bool     `mapstructure:"pre_emit_gate_enabled" json:"pre_emit_gate_enabled" yaml:"pre_emit_gate_enabled"`
	PreEmitGateUIDWhitelist []uint   `mapstructure:"pre_emit_gate_uid_whitelist" json:"pre_emit_gate_uid_whitelist" yaml:"pre_emit_gate_uid_whitelist"`
	PreEmitGateSkills       []string `mapstructure:"pre_emit_gate_skills" json:"pre_emit_gate_skills" yaml:"pre_emit_gate_skills"`

	// PreEmitGateAutoRedoEnabled — independent kill switch for the
	// PR-9b/2b redo loop. Lives next to PreEmitGate so a Block
	// verdict can be observed (frontend banner) without the server
	// auto-spending tokens on a re-invoke. Default false: opt in
	// when the gate has been live long enough that block decisions
	// are trustworthy and the cost of an extra turn is acceptable.
	PreEmitGateAutoRedoEnabled bool `mapstructure:"pre_emit_gate_auto_redo_enabled" json:"pre_emit_gate_auto_redo_enabled" yaml:"pre_emit_gate_auto_redo_enabled"`

	// CritiqueGate — M3 critique sub-agent (Sprint-B). When enabled
	// for (uid, skill), the post-emit pipeline asks Haiku to score
	// the artifact across 5 dimensions; a Block verdict triggers a
	// redo (when CritiqueGateAutoRedoEnabled). Distinct from the
	// pre-emit gate (M5) — pre-emit catches deterministic rule
	// violations (anti-slop, missing brand tokens), critique catches
	// taste / craft issues a regex can't see.
	//
	// Granular per-skill via CritiqueGateSkills. Empty list = off
	// (same posture as PreEmitGateSkills). Doubles per-turn token
	// cost on average (Haiku call + potential redo), so opt-in is
	// per-skill rather than global.
	CritiqueGateEnabled         bool     `mapstructure:"critique_gate_enabled" json:"critique_gate_enabled" yaml:"critique_gate_enabled"`
	CritiqueGateUIDWhitelist    []uint   `mapstructure:"critique_gate_uid_whitelist" json:"critique_gate_uid_whitelist" yaml:"critique_gate_uid_whitelist"`
	CritiqueGateSkills          []string `mapstructure:"critique_gate_skills" json:"critique_gate_skills" yaml:"critique_gate_skills"`
	CritiqueGateAutoRedoEnabled bool     `mapstructure:"critique_gate_auto_redo_enabled" json:"critique_gate_auto_redo_enabled" yaml:"critique_gate_auto_redo_enabled"`

	// (F2 2026-05-17) BrandSpecProtocolEnabled removed —
	// IsBrandSpecProtocolEnabled had no production consumer and the
	// referenced _shared/brand-spec-protocol.md file doesn't exist
	// on disk. Brand-spec content reaches the model via the active
	// brand asset's composer.BrandSpec injection (wired through
	// preflight.go's asset-library walk).

	// HonestDataDetector — anti-fabricated-metrics scanner (M6).
	HonestDataDetectorEnabled bool `mapstructure:"honest_data_detector_enabled" json:"honest_data_detector_enabled" yaml:"honest_data_detector_enabled"`

	// SkillPreflight — auto-inject skill side-files (assets/* +
	// references/*) into the system prompt's SystemAdditions block
	// (DS-2 / DS-5).
	SkillPreflightEnabled bool `mapstructure:"skill_preflight_enabled" json:"skill_preflight_enabled" yaml:"skill_preflight_enabled"`

	// KnowledgeRetrieverBackend selects the Work Agent RAG retriever.
	// Supported in-tree values: "local-vector" and "lexical".
	// External values such as "pgvector", "qdrant", and "pinecone"
	// are accepted for config compatibility but currently fall back
	// through the retriever factory until those adapters are installed.
	KnowledgeRetrieverBackend string `mapstructure:"knowledge_retriever_backend" json:"knowledge_retriever_backend" yaml:"knowledge_retriever_backend"`

	// Retired 2026-05-18 — the production-tools layer (script_generator
	// / shotboard_generator / shot_prompt_generator / render_shot /
	// lookup_asset + the async render dispatcher) was removed because
	// no skill in the catalog drove user discovery of it; only
	// user-installed external MCP connectors contribute tools now.
	// The config-block fields (ProductionToolsEnabled,
	// ProductionToolsUIDWhitelist, ProductionToolsRenderBackend) were
	// dropped from this struct in the same commit; existing
	// `workagent_features.production_tools_*` keys in operator YAML
	// unmarshal into nothing and are silently ignored — clean them up
	// at the operator's convenience.

	// PromptLayersDisabled opts specific DS-5 prompt-stack layers
	// out of the SystemAdditions composition (preflight.go). Values
	// match PromptLayer.Name(): "discovery" / "identity" /
	// "protocols" / "design-system" / "skill" / "skill-side-files"
	// / "project-metadata". Unknown names are silently ignored —
	// a typo never accidentally breaks composition.
	//
	// Default empty (all layers enabled). Operators use this to
	// A/B-test layer contributions ("does dropping protocols hurt
	// the brand-spec walkthrough?"), debug ("which layer is making
	// this prompt 50KB?"), or stage rollback for a misbehaving
	// layer without a redeploy.
	PromptLayersDisabled []string `mapstructure:"prompt_layers_disabled" json:"prompt_layers_disabled" yaml:"prompt_layers_disabled"`
}

// uidPasses (the former shared D5 gate helper) was retired alongside
// the 2026-05-13 always-on simplification (commit 31e35bcd). Every
// IsXxxEnabledFor method now reduces to a plain `uid != 0` check —
// see the methods below. The struct's whitelist fields stay only for
// backward-compatible YAML unmarshalling and intentionally have no
// readers.

// GetWorkAgentFeatures returns the package-level WorkAgentFeatures
// pointer parsed from server/config.yaml's `workagent_features` block.
// Returns nil when the config block is missing — every helper method
// on *WorkAgentFeatures is nil-safe (returns false), so callers can
// always invoke `config.GetWorkAgentFeatures().IsXEnabledFor(uid)`
// without a nil check.
//
// Wired through globals.GraConf (the canonical config landing zone
// established by server/core/viper.go's Unmarshal flow).
func GetWorkAgentFeatures() *WorkAgentFeatures {
	return workAgentFeaturesAccessor()
}

// workAgentFeaturesAccessor is overridable for tests. Production
// implementation is set in init() to read globals.GraConf, which
// avoids a circular import (the config package can't import globals
// without breaking server/initialize ordering, but globals depends
// on config).
var workAgentFeaturesAccessor = func() *WorkAgentFeatures { return nil }

// SetWorkAgentFeaturesAccessor lets the bootstrap path (server/core
// or server/initialize) install the live accessor that reads from
// globals.GraConf. Call once at startup, after viper has unmarshalled
// the config; concurrent reset under load is not supported.
func SetWorkAgentFeaturesAccessor(fn func() *WorkAgentFeatures) {
	if fn == nil {
		workAgentFeaturesAccessor = func() *WorkAgentFeatures { return nil }
		return
	}
	workAgentFeaturesAccessor = fn
}

// IsQuestionFormEnabledFor — M1 turn-1 form short-circuit.
// Product decision: WorkAgent features are globally enabled; config
// fields remain only for backward-compatible unmarshalling.
func (f *WorkAgentFeatures) IsQuestionFormEnabledFor(uid uint) bool {
	return uid != 0
}

// IsDirectionsFallbackEnabledFor — M2 directions picker.
func (f *WorkAgentFeatures) IsDirectionsFallbackEnabledFor(uid uint) bool {
	return uid != 0
}

// IsPreEmitGateEnabledForSkill — combines per-user gate with per-skill
// allowlist. Both must pass: a user-eligible turn on a non-listed
// skill returns false, and a listed skill called by an excluded user
// also returns false.
//
// Skill names match the agent_mode column / skills/<name>/ folder.
func (f *WorkAgentFeatures) IsPreEmitGateEnabledForSkill(uid uint, skill string) bool {
	return uid != 0 && skill != ""
}

// IsPreEmitGateAutoRedoEnabledFor — gates whether a Block verdict
// from the pre-emit gate triggers an automatic SDK re-invoke. AND'd
// against IsPreEmitGateEnabledForSkill so the redo loop is always a
// strict subset of where the gate runs (no redo on skills the gate
// doesn't even cover).
func (f *WorkAgentFeatures) IsPreEmitGateAutoRedoEnabledFor(uid uint, skill string) bool {
	return f.IsPreEmitGateEnabledForSkill(uid, skill)
}

// IsCritiqueGateEnabledForSkill — M3 critique sub-agent gate. Same
// (uid AND skill) shape as IsPreEmitGateEnabledForSkill. Empty
// CritiqueGateSkills list returns false even for opted-in users so
// an unset operator config defaults safely off.
func (f *WorkAgentFeatures) IsCritiqueGateEnabledForSkill(uid uint, skill string) bool {
	return uid != 0 && skill != ""
}

// IsCritiqueGateAutoRedoEnabledFor — gates whether a critique Block
// verdict triggers an automatic SDK re-invoke. AND'd against the
// gate flag itself so redo is always a strict subset of where the
// gate runs (no redo on skills the gate doesn't even cover).
func (f *WorkAgentFeatures) IsCritiqueGateAutoRedoEnabledFor(uid uint, skill string) bool {
	return f.IsCritiqueGateEnabledForSkill(uid, skill)
}

// (F2 2026-05-17) IsBrandSpecProtocolEnabled removed — see the
// BrandSpecProtocolEnabled field-removal note above. The gate had
// zero production consumers; the brand-spec injection path that
// DOES fire goes through composer.BrandSpec (asset-library walk
// in preflight.go), gated by "user has an active brand asset",
// not by this feature flag.

// IsHonestDataDetectorEnabled — global gate.
func (f *WorkAgentFeatures) IsHonestDataDetectorEnabled() bool {
	return true
}

// IsSkillPreflightEnabled — global gate.
func (f *WorkAgentFeatures) IsSkillPreflightEnabled() bool {
	return true
}

func (f *WorkAgentFeatures) KnowledgeRetrieverBackendName() string {
	if f != nil && f.KnowledgeRetrieverBackend != "" {
		return f.KnowledgeRetrieverBackend
	}
	return "local-vector"
}

// (Removed 2026-05-18) IsProductionToolsEnabledFor — the
// production-tools MCP bridge was retired; agent_processor.go no
// longer reads any feature flag for that surface.

// DisabledPromptLayers returns the set of DS-5 layer names ops has
// opted out via PromptLayersDisabled. Unknown names are silently
// dropped — a typo in config never accidentally bricks composition.
// The caller (preflight.go) walks this against PromptLayer.Name()
// to build the prompts.LayerDisableSet the composer consumes.
//
// nil-safe: a nil features pointer returns nil so callers can
// chain without a guard.
func (f *WorkAgentFeatures) DisabledPromptLayers() []string {
	return nil
}
