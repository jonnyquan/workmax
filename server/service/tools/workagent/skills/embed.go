// Package skills owns the Skill bundle layer for work-agent. A skill =
// a manifest (skill.yaml) + a behavior contract (SKILL.md) + optional
// references/ + scripts/. Each agent-mode (ppt / comic / short-drama
// / etc.) is one skill.
//
// This package replaces the legacy prompts/ package's
// SystemPromptRegistry: GetSystemPrompt(mode, hasFiles, fileContext)
// → Loader.Build(mode, BuildContext{...}).SystemPrompt. During
// migration the loader synthesizes a SkillBundle from legacy
// identity_<mode>.md + output_format_<mode>.md when no skill.yaml
// exists for that mode — see loader.go Build() for the dispatch.
//
// Why a new package: keeping it next to prompts/ would have invited
// circular imports and confused the embed.FS roots. Sibling packages
// with separate FS roots is the cleaner cut.
package skills

import "embed"

// skillsFS embeds every file under skills/ at build time so the
// Loader can read manifests and references without a runtime
// filesystem dependency. The two roots match the directory layout:
//
//	skills/
//	├── _shared/             ← horizontal references reused across skills
//	└── <skill_name>/        ← per-skill bundle (manifest + body + refs)
//
// Adding a new skill requires adding its directory below to the
// explicit go:embed list. The all:* prefix ensures dotfiles and
// underscore-prefixed dirs (_shared) get included; the explicit
// list keeps Go-tooling artefacts (test caches, .DS_Store) out.
//
// Each skill directory needs to be enumerated explicitly because
// `embed` does not support `**/*` recursive globs — that's a
// known limitation of go:embed (the `all:` prefix only flips the
// dot/underscore-skipping rule, not recursion). Adding a new skill
// = appending one entry below.
//
// Skills retained: critique (programmatic-only, post-turn gate) +
// the 14 user-facing modes listed in allowedAgentModes
// (api/pro/tools/workagent/conversation_api.go). Previously also
// listed 9 text/writing modes (academic / analysis / business /
// design / education / marketing / product / research / technical)
// that had no production path — deleted in the agent_mode line
// cleanup. `writer` (formerly the registry's misconfigured-skill
// fallback target) was also removed 2026-05-12; the fallback now
// targets `ppt` (a real user-facing skill that is also the DDL
// default for chat_thread.agent_mode), so a misconfigured manifest
// surfaces ppt-mode behaviour instead of a hidden "writer" path
// that didn't exist anywhere else in the catalog. The contract
// test asserts allowedAgentModes ⊆ skills_on_disk so any future
// drift surfaces immediately.
//
//go:embed all:_shared
//go:embed all:character    all:critique      all:flashCard
//go:embed all:lifestyle    all:logo          all:marketingPoster
//go:embed all:mobileStory  all:modelTryOn    all:oohBillboard
//go:embed all:packaging    all:pictureBook   all:ppt
//go:embed all:productShot  all:socialAd      all:webBanner
var skillsFS embed.FS
