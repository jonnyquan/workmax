package workagent

// agent_mode_contract_test.go — pins the cross-layer contract for
// the agent_mode catalog. Four server/Desktop-neutral sources must agree:
//
//   1. server/contracts/agent/v1's canonical first-party catalog
//   2. server/api/pro/tools/workagent/conversation_api.go's
//      `allowedAgentModes` map (HTTP-layer allowlist — rejects
//      unknown values from external POSTs)
//   3. server/service/tools/workagent/skills/<mode>/ directories
//      (every user-selectable mode must have a skill bundle)
//   4. server/model/workagent/chat_thread.go's `agent_mode` column
//      GORM tag comment (15-mode enum documented for DDL readers)
//
// Desktop consumes the published Go catalog. Web is intentionally not an
// Agent catalog owner and must not be a test/build dependency of this package.
//
// What this test enforces about the disk skill set:
//   - Every allowlisted mode has a skill bundle (LOAD-BEARING)
//   - The disk set MINUS programmatic-only kinds equals the
//     allowlist exactly. Two programmatic-only skills are
//     deliberately not in the allowlist:
//       writer   — registry's misconfigured-skill fallback target
//       critique — invoked by critique_dispatcher post-turn gate
//     Everything else on disk must be in allowedAgentModes; a
//     disk-only mode that isn't either programmatic or allowlisted
//     is dead code (was caught by this contract on 2026-05-12 when
//     9 text/writing modes — academic / analysis / business /
//     design / education / marketing / product / research /
//     technical — were deleted as having no production path).

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	agentv1 "server/contracts/agent/v1"
	"server/model/workagent"
)

// canonicalAgentModes is owned by the dependency-free Agent v1 contract.
// API admission, manifests and Desktop catalog consumers must agree with it.
var canonicalAgentModes = agentv1.OfficialAgentModes()

// TestAgentMode_AllowlistMatchesCanonical pins that the
// HTTP-layer allowlist in conversation_api.go (the wire gate)
// equals the canonical list. Adding a mode to the FE union without
// updating the BE allowlist would let the FE post a value that the
// API rejects — silent feature regression.
func TestAgentMode_AllowlistMatchesCanonical(t *testing.T) {
	got := make([]string, 0, len(allowedAgentModes))
	for k := range allowedAgentModes {
		got = append(got, k)
	}
	sort.Strings(got)

	want := append([]string{}, canonicalAgentModes...)
	sort.Strings(want)

	if len(got) != len(want) {
		t.Fatalf("allowedAgentModes has %d entries, canonical has %d: got=%v want=%v",
			len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("allowedAgentModes[%d] = %q, want %q (sorted comparison)", i, got[i], want[i])
		}
	}
}

// TestAgentMode_EveryAllowlistEntryHasSkillBundle pins the runtime
// contract: every mode the API accepts must resolve to a skill
// bundle on disk, otherwise the agent_processor would fall back
// to the writer skill and silently mis-prompt.
func TestAgentMode_EveryAllowlistEntryHasSkillBundle(t *testing.T) {
	// skills/ sits at server/service/tools/workagent/skills relative
	// to repo root; this test runs from the api package directory so
	// we walk up to find it. Using runtime path discovery keeps the
	// test robust to layout reshuffles (CI vs local).
	skillsRoot := findSkillsRoot(t)

	for _, mode := range canonicalAgentModes {
		t.Run(mode, func(t *testing.T) {
			dir := filepath.Join(skillsRoot, mode)
			info, err := os.Stat(dir)
			if err != nil {
				t.Fatalf("skill bundle dir missing for allowlisted mode %q: %v", mode, err)
			}
			if !info.IsDir() {
				t.Errorf("skill bundle path for %q exists but is not a directory", mode)
			}
			// Every shipped skill must have either a SKILL.md or
			// skill.yaml — at least one of the two is needed for
			// the loader to recognize it.
			hasYAML := fileExists(filepath.Join(dir, "skill.yaml"))
			hasSKILL := fileExists(filepath.Join(dir, "SKILL.md"))
			if !hasYAML && !hasSKILL {
				t.Errorf("skill %q has neither SKILL.md nor skill.yaml; loader cannot resolve it", mode)
			}
		})
	}
}

// TestAgentMode_EveryAllowlistEntryEmbedded pins the Go runtime
// packaging contract: go:embed does not support recursive **/*
// globs, so every skill directory must be explicitly listed in
// skills/embed.go. The disk-dir test above catches "folder missing";
// this catches the subtler failure mode where the folder exists but
// the production binary cannot read it from embedded FS.
func TestAgentMode_EveryAllowlistEntryEmbedded(t *testing.T) {
	embedSrc := findRepoFile(t, "server/service/tools/workagent/skills/embed.go")
	data, err := os.ReadFile(embedSrc)
	if err != nil {
		t.Fatalf("read embed.go: %v", err)
	}
	text := string(data)
	for _, mode := range canonicalAgentModes {
		if !strings.Contains(text, "all:"+mode) {
			t.Errorf("allowed mode %q missing from skills/embed.go go:embed list — runtime binary will not embed its skill bundle", mode)
		}
	}
}

// TestAgentMode_DiskSetEqualsAllowlistPlusProgrammaticOnly pins the
// tighter direction of the contract: every directory under skills/
// (excluding _shared) must be EITHER in allowedAgentModes OR in
// the explicit programmatic-only allowlist (currently just critique).
// This is the gate that surfaced the 9 orphan text/writing modes
// before their 2026-05-12 deletion — they were neither user-facing
// nor programmatic, but had no production reachability either. The
// same gate caught `writer` once it stopped serving a fallback role
// (also removed 2026-05-12; the fallback now targets `ppt` which is
// itself in allowedAgentModes).
func TestAgentMode_DiskSetEqualsAllowlistPlusProgrammaticOnly(t *testing.T) {
	programmaticOnly := map[string]string{
		"critique": "invoked by critique_dispatcher post-turn gate",
	}

	skillsRoot := findSkillsRoot(t)
	entries, err := os.ReadDir(skillsRoot)
	if err != nil {
		t.Fatalf("read skills dir: %v", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, "_") {
			continue // _shared and friends are framework-level
		}
		if _, ok := allowedAgentModes[name]; ok {
			continue // user-facing allowlist
		}
		if reason, ok := programmaticOnly[name]; ok {
			_ = reason
			continue // explicit programmatic-only carve-out
		}
		t.Errorf("skill dir %q is neither user-facing (allowedAgentModes) nor in the programmatic-only carve-out (%v) — looks like dead code",
			name, programmaticOnly)
	}
}

// TestAgentMode_DefaultAgentModeIsAllowed pins that the canonical
// fallback constant (DefaultAgentMode in chat_thread.go) is itself
// a valid allowlisted mode. A regression that renamed the default
// to a deprecated value would let new threads be created in an
// un-allowed state.
func TestAgentMode_DefaultAgentModeIsAllowed(t *testing.T) {
	if _, ok := allowedAgentModes[workagent.DefaultAgentMode]; !ok {
		t.Errorf("DefaultAgentMode = %q is NOT in allowedAgentModes (would create un-allowed threads)",
			workagent.DefaultAgentMode)
	}
}

// TestAgentMode_ColumnCommentMatchesAllowlist pins the DDL-level
// documentation: the comment on chat_thread.AgentMode lists the
// allowed values. A new mode added to the allowlist without
// updating the column comment would leave DDL readers with stale
// enum docs.
//
// Comment shape (from the GORM tag):
//
//	comment:Agent模式(ppt/flashCard/.../oohBillboard)
//
// We extract the slash-separated list and compare to the
// allowlist.
func TestAgentMode_ColumnCommentMatchesAllowlist(t *testing.T) {
	// Re-parse the column comment by reading the model source file —
	// keeps the test honest against the actual GORM tag.
	modelSrc := findRepoFile(t, "server/model/workagent/chat_thread.go")
	data, err := os.ReadFile(modelSrc)
	if err != nil {
		t.Fatalf("read chat_thread.go: %v", err)
	}
	// Match: comment:Agent模式(ppt/flashCard/.../oohBillboard)
	re := regexp.MustCompile(`comment:Agent模式\(([^)]+)\)`)
	m := re.FindStringSubmatch(string(data))
	if m == nil {
		t.Fatalf("chat_thread.go missing Agent模式(...) comment — DDL docs stale")
	}
	modes := strings.Split(m[1], "/")
	got := make(map[string]bool, len(modes))
	for _, mode := range modes {
		got[strings.TrimSpace(mode)] = true
	}

	// Every allowlisted mode must appear in the comment.
	for mode := range allowedAgentModes {
		if !got[mode] {
			t.Errorf("allowed mode %q not in column comment — DDL docs need refresh", mode)
		}
	}
	// And vice versa: the comment must not list a mode the API
	// would reject (stale docs).
	for mode := range got {
		if _, ok := allowedAgentModes[mode]; !ok {
			t.Errorf("column comment lists %q but allowlist rejects it (stale docs)", mode)
		}
	}
}

// universalSharedRefs are the 4 horizontal _shared/*.md files
// EVERY user-facing skill MUST inline through its
// skill.yaml::references.shared list. They are universal safety /
// quality rules; a skill that forgets one
// silently loses that rule in its system prompt — the model would
// then emit AI-slop / unverified facts / etc. without any test
// signal because the omission is per-yaml, not framework-enforced.
//
// We could framework-inject these instead and remove the per-yaml
// list, but that's a larger loader refactor; the contract test is
// the lower-cost guard for the same drift.
//
// Update both this list AND every skill.yaml when adding a new
// universal _shared file.
//
// critique is excluded because it is a REVIEWER skill (programmatic-
// only, post-turn) rather than a GENERATOR skill. Generator-only
// rules (anti-ai-slop / asset-protocol) don't apply to a reviewer
// agent that doesn't produce visual content; including them inside
// the reviewer's system prompt would create a self-referential loop
// where the reviewer might confuse "the output shouldn't have AI
// slop" with "I, the reviewer, should write in non-slop voice".
// Critique encodes the anti-slop rules in its rubric instead
// (skills/critique/references/rubric-detail.md) so it can grade
// outputs against them without consuming the generator-facing text.
// TestSkill_CritiqueExcludesGenerationOnlyRefs (G12) pins this
// architecture so a future contributor can't accidentally widen
// critique's _shared list.
var universalSharedRefs = []string{
	"fact-verification",
	"asset-protocol",
	"anti-ai-slop",
	"junior-designer-workflow",
}

// TestSkill_AllUserFacingSkillsCarryUniversalSharedRefs pins
// G1 from the 2026-05-17 skill audit: every user-facing skill
// must list all 4 universal _shared refs in its skill.yaml.
// Without this test, a new skill author could forget e.g.
// anti-ai-slop and the output would have generic AI-slop look
// in production with no CI signal.
func TestSkill_AllUserFacingSkillsCarryUniversalSharedRefs(t *testing.T) {
	skillsRoot := findSkillsRoot(t)
	for _, mode := range canonicalAgentModes {
		t.Run(mode, func(t *testing.T) {
			yamlPath := filepath.Join(skillsRoot, mode, "skill.yaml")
			data, err := os.ReadFile(yamlPath)
			if err != nil {
				t.Fatalf("read skill.yaml: %v", err)
			}
			// Extract the references.shared list. Cheap line-scan
			// rather than full yaml unmarshal so this test stays
			// dependency-free and fast. The format we expect is:
			//
			//   references:
			//     shared:
			//       - fact-verification
			//       - asset-protocol
			//       ...
			//
			// We walk until we hit a "references:" section, then a
			// "shared:" key inside it, then collect "- foo" entries
			// at the matching indent level until a non-list line.
			shared := parseYAMLSharedList(string(data))
			present := make(map[string]bool, len(shared))
			for _, s := range shared {
				present[s] = true
			}
			for _, ref := range universalSharedRefs {
				if !present[ref] {
					t.Errorf("skill %q references.shared MISSING universal ref %q (would silently lose that rule in system prompt)",
						mode, ref)
				}
			}
		})
	}
}

// generationOnlyRefs are the _shared/*.md files that ONLY apply
// to generator skills. A reviewer / classifier / analyzer skill
// (today: critique; future: potential audit / score-only skills)
// MUST NOT carry these — they're rules about how to produce
// visual output, not how to evaluate it. Quoting them in a
// reviewer's prompt creates a self-referential loop (the reviewer
// would confuse "the output shouldn't have AI slop" with "I, the
// reviewer, should write in non-slop voice").
//
// fact-verification + junior-designer-workflow are NOT here —
// those apply to both generators (don't fabricate stats / show
// your work) and reviewers (don't fabricate a critique / show
// your reasoning).
var generationOnlyRefs = []string{
	"anti-ai-slop",
	"asset-protocol",
}

// criticueExpectedSharedRefs pins the exact references.shared set
// critique should carry. Lives as data (not derived) so a future
// contributor adding e.g. "junior-designer-workflow-v2" to
// critique sees the failure and either updates this list (with a
// note explaining WHY the new ref applies to a reviewer) or
// re-routes the new content into the reviewer-specific
// rubric-detail.md.
//
// G12 (2026-05-17) — distinguishes reviewer skills from generator
// skills via this carve-out instead of a yaml `category` field
// (over-engineering until we have ≥3 reviewer-type skills).
var critiqueExpectedSharedRefs = []string{
	"fact-verification",
	"junior-designer-workflow",
}

// TestSkill_CritiqueExcludesGenerationOnlyRefs pins G12: the
// critique skill MUST NOT carry anti-ai-slop or asset-protocol.
// Reviewer skills encode anti-slop rules in their own rubric
// (skills/critique/references/rubric-detail.md) so they can grade
// outputs against them without polluting their own voice. A
// regression that "helpfully" widens critique's shared list to
// the 4 universal refs (e.g. someone misreading the universal
// invariant) would break the reviewer-vs-generator separation.
func TestSkill_CritiqueExcludesGenerationOnlyRefs(t *testing.T) {
	skillsRoot := findSkillsRoot(t)
	yamlPath := filepath.Join(skillsRoot, "critique", "skill.yaml")
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatalf("read critique skill.yaml: %v", err)
	}
	shared := parseYAMLSharedList(string(data))
	present := make(map[string]bool, len(shared))
	for _, s := range shared {
		present[s] = true
	}

	// Negative invariant: generation-only refs MUST be absent.
	for _, banned := range generationOnlyRefs {
		if present[banned] {
			t.Errorf("critique references.shared MUST NOT contain %q — reviewer skills encode generator rules in their own rubric, not via direct prompt inclusion (see critique/skill.yaml comment + rubric-detail.md)",
				banned)
		}
	}

	// Positive invariant: critique's expected reviewer-applicable
	// refs ARE present. Catches an accidental "removed too much"
	// edit that strips even the reviewer-relevant refs.
	for _, expected := range critiqueExpectedSharedRefs {
		if !present[expected] {
			t.Errorf("critique references.shared MISSING %q — required for reviewer skills (fact discipline + show-your-work posture)",
				expected)
		}
	}
}

// parseYAMLSharedList extracts the references.shared list from a
// skill.yaml as a slice of strings. Returns empty slice if the
// block is absent. Strict about indentation — matches the
// canonical 2-space-per-level format every skill.yaml uses.
func parseYAMLSharedList(yamlText string) []string {
	const referencesPrefix = "references:"
	const sharedPrefix = "  shared:"
	var out []string
	lines := strings.Split(yamlText, "\n")
	inReferences := false
	inShared := false
	for _, line := range lines {
		if strings.HasPrefix(line, referencesPrefix) {
			inReferences = true
			inShared = false
			continue
		}
		if inReferences && strings.HasPrefix(line, sharedPrefix) {
			inShared = true
			continue
		}
		if inShared {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "- ") {
				out = append(out, strings.TrimPrefix(trimmed, "- "))
				continue
			}
			// Empty line or new top-level key ends the list.
			if trimmed == "" || !strings.HasPrefix(line, "    ") {
				inShared = false
				if !strings.HasPrefix(line, "  ") {
					inReferences = false
				}
			}
		}
	}
	return out
}

// TestSkill_EveryUserFacingSkillHasServerCatalogEntry prevents a skill from
// shipping without a Desktop-renderable canonical label. The authoritative
// fallback now lives in the Go contract; Web locale files are deliberately
// outside the Agent build and test graph.
func TestSkill_EveryUserFacingSkillHasServerCatalogEntry(t *testing.T) {
	for _, mode := range canonicalAgentModes {
		descriptor, ok := agentv1.LookupOfficialSkill(mode)
		if !ok {
			t.Errorf("official Agent catalog missing mode %q", mode)
			continue
		}
		if strings.TrimSpace(descriptor.DisplayName) == "" {
			t.Errorf("official Agent catalog mode %q has empty display name", mode)
		}
		if strings.TrimSpace(descriptor.Description) == "" {
			t.Errorf("official Agent catalog mode %q has empty description", mode)
		}
	}
}

// ---------- helpers ----------

// findSkillsRoot walks up from the current working dir to find
// server/service/tools/workagent/skills. Used so the test
// works regardless of where Go's test runner places its CWD.
func findSkillsRoot(t *testing.T) string {
	t.Helper()
	return findRepoFile(t, "server/service/tools/workagent/skills")
}

func findRepoFile(t *testing.T, relPath string) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	// Walk up at most 5 levels — server tests typically run from
	// server/api/pro/tools/workagent so we need 4 hops to reach the
	// server root.
	dir := cwd
	for i := 0; i < 6; i++ {
		candidate := filepath.Join(dir, relPath)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not find %q walking up from %q", relPath, cwd)
	return ""
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
