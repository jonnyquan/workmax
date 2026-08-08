package workagent

import (
	"fmt"
	"sync"

	"server/globals"
	"server/service/tools/workagent/prompts"
	"server/service/tools/workagent/skills"
)

// legacyPromptsBridge satisfies skills.LegacyTemplateProvider by
// delegating to the prompts/ package's exported helpers. Lives in
// the workagent package (not skills/ or prompts/) so it can import
// both without inviting a circular dependency.
//
// Once every mode is fully migrated and no skill.yaml carries a
// Legacy block, this bridge becomes dead code and gets deleted
// alongside the prompts/templates/ folder.
type legacyPromptsBridge struct{}

func (legacyPromptsBridge) Identity(mode string) string {
	return prompts.LoadIdentity(mode)
}

func (legacyPromptsBridge) OutputFormat(mode string) string {
	return prompts.LoadOutputFormat(mode)
}

func (legacyPromptsBridge) RenderFramework(modeIdentity, outputFormat string, ctx skills.BuildContext) string {
	// Forward SystemAdditions verbatim — empty string preserves
	// the legacy framework rendering, populated string drops the
	// _shared / discovery / checklist-digest tail into the
	// {{.SystemAdditions}} placeholder.
	return prompts.RenderFrameworkWithAdditions(
		modeIdentity, outputFormat, ctx.SystemAdditions, ctx.HasFiles, ctx.FileContext,
	)
}

// skillRegistry is the single shared *skills.Registry the workagent
// package hands to chat_service / agent_processor / agent_client_manager.
// Single instance is fine — the registry is stateless past
// construction (its loader caches manifests + reference bodies, all
// keyed by skill name; embed.FS reads are read-only).
var (
	skillRegistry     *skills.Registry
	skillRegistryOnce sync.Once
)

// GetSkillRegistry returns the lazily-initialised skill registry
// wired to the legacy prompts bridge. Call sites that previously
// did `prompts.NewSystemPromptRegistry()` use this instead.
//
// SetWarnFunc routes the registry's misconfigured-skill notifications
// through globals.Warn so a broken skill manifest lands in the same
// telemetry stream as the rest of the workagent module — without
// this hook, the skills package falls back to fmt.Printf, which
// only shows up in stderr and never reaches the dashboards ops
// alerts on. Wired here (not in NewRegistry's signature) so the
// skills package itself stays free of a globals dependency.
func GetSkillRegistry() *skills.Registry {
	skillRegistryOnce.Do(func() {
		skillRegistry = skills.NewRegistry(legacyPromptsBridge{})
		skillRegistry.SetWarnFunc(func(skillName string, err error) {
			globals.Warn(fmt.Sprintf("[skills.Registry] skill %q misconfigured (using ppt fallback): %v", skillName, err))
		})
	})
	return skillRegistry
}
