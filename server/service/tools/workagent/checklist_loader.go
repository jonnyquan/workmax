// Package workagent — checklist_loader.go.
//
// Parses skills/<mode>/references/checklist.md into a structured
// Checklist value the gate (PR-9) consumes. The format is:
//
//	### P0-N · <name>
//	- detector: `detector_name`
//	- description: ...
//	- on_fail: block | warn_require_ack
//	- only_if: <free-form predicate, optional>
//
// The loader is intentionally a hand-rolled scanner — checklists are
// authored by humans, the file is small (<10KB), and a real markdown
// parser dependency would be heavyweight overkill. The format is
// regular enough that line-by-line state machine works.
//
// Failure mode: malformed checklist.md → log a warn-once and return
// an empty Checklist. The gate reading an empty checklist treats it
// as "no rules apply", which is a safe default (better than crashing
// the turn or denying emit).
package workagent

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"server/globals"
	"server/service/tools/workagent/skills"
)

// Checklist is the parsed form of a skill's references/checklist.md.
// Items are grouped by priority for the gate's decision logic (P0
// fail = block, P1 fail = warn-require-ack, P2 = trace only).
type Checklist struct {
	SkillName string
	P0        []ChecklistItem
	P1        []ChecklistItem
	P2        []ChecklistItem
}

// ChecklistItem mirrors one ### block in the markdown source. The
// detector name is the registry key the gate will look up in
// server/service/tools/workagent/detectors.Default().
type ChecklistItem struct {
	ID          string // e.g. "P0-1"
	Title       string // e.g. "honest_data" — the heading suffix after `·`
	Detector    string // registry name, e.g. "honest_data" / "brand_spec_grep"
	Description string
	OnFail      string // "block" | "warn_require_ack" | "" (P2 default = trace)
	OnlyIf      string // optional predicate, free-form
}

// Items returns the union of P0 + P1 + P2 in priority order. Useful
// for the pre-flight digest (which surfaces all priority levels).
func (c *Checklist) Items() []ChecklistItem {
	if c == nil {
		return nil
	}
	all := make([]ChecklistItem, 0, len(c.P0)+len(c.P1)+len(c.P2))
	all = append(all, c.P0...)
	all = append(all, c.P1...)
	all = append(all, c.P2...)
	return all
}

// IsEmpty reports whether the checklist has any items at all. The
// gate uses this to short-circuit: an empty checklist means the
// skill has no rules, the gate trivially passes.
func (c *Checklist) IsEmpty() bool {
	return c == nil || (len(c.P0) == 0 && len(c.P1) == 0 && len(c.P2) == 0)
}

// section is the parser's state — which P-level we're currently
// accumulating items into.
type section int

const (
	sectionNone section = iota
	sectionP0
	sectionP1
	sectionP2
)

var (
	// Headings the parser recognizes. Tolerates a wide range of
	// human-authored separators (·, -, —, en-dash) and trailing
	// notes (e.g. "P0 · MUST PASS（阻塞 emit）").
	sectionP0Pattern = regexp.MustCompile(`(?i)^\s*##\s+P0\b`)
	sectionP1Pattern = regexp.MustCompile(`(?i)^\s*##\s+P1\b`)
	sectionP2Pattern = regexp.MustCompile(`(?i)^\s*##\s+P2\b`)

	// Item heading: "### P0-N · <title>" — title may contain CJK,
	// punctuation, parentheses. Anchor to the leading "###" so a
	// "####" sub-heading inside an item body doesn't trigger.
	itemHeadingPattern = regexp.MustCompile(`(?m)^###\s+(P[012]-\d+)\s*[·\-—]?\s*(.*)$`)

	// Field rows: "- detector: `name`" / "- description: ..." /
	// "- on_fail: block".
	fieldPattern = regexp.MustCompile(`^\s*-\s+([a-z_]+)\s*:\s*(.+?)\s*$`)
)

// LoadChecklistForSkill reads the checklist for the given skill.
//
// Resolution order:
//  1. If `skillDir` is non-empty, read from disk at
//     `<skillDir>/checklist.md`. Useful for tests that supply a
//     tmp-dir or working-dir relative path.
//  2. Otherwise (production path), read from the embedded skill
//     tree via skills.ReadChecklist(skillName). The embed FS is
//     stable across deployment layouts (Docker / installed binary)
//     where CWD-relative paths break.
//
// Returns an empty checklist (not an error) when the file doesn't
// exist, is empty, or can't be parsed. Contract: checklist loading
// must never block a turn.
func LoadChecklistForSkill(skillDir, skillName string) *Checklist {
	if skillName == "" {
		return &Checklist{SkillName: skillName}
	}

	var (
		data []byte
		err  error
	)
	if skillDir != "" {
		// Disk path (tests / explicit overrides).
		path := filepath.Join(skillDir, "checklist.md")
		data, err = os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				warnChecklistOnce(skillName, fmt.Sprintf("read %s: %v", path, err))
			}
			return &Checklist{SkillName: skillName}
		}
	} else {
		// Embedded path (production).
		data, err = skills.ReadChecklist(skillName)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) && !os.IsNotExist(err) {
				warnChecklistOnce(skillName, fmt.Sprintf("read embedded checklist for %s: %v", skillName, err))
			}
			return &Checklist{SkillName: skillName}
		}
	}

	if len(data) == 0 {
		return &Checklist{SkillName: skillName}
	}
	if len(data) > 1<<20 {
		warnChecklistOnce(skillName, fmt.Sprintf("checklist for %q exceeds 1 MiB; ignoring", skillName))
		return &Checklist{SkillName: skillName}
	}
	return parseChecklistMarkdown(skillName, string(data))
}

// parseChecklistMarkdown is the core parser. Public for testability
// (tests can feed string fixtures without hitting disk).
//
// Algorithm:
//  1. Walk lines, track current section (P0 / P1 / P2 / none).
//  2. On an item heading "### P0-N · title", start a new item
//     under the active section.
//  3. On a "- field: value" line, populate the active item.
//  4. Drop items without a detector — gate has nothing to dispatch.
func parseChecklistMarkdown(skillName, body string) *Checklist {
	cl := &Checklist{SkillName: skillName}

	var (
		current section
		item    *ChecklistItem
		commit  = func() {
			if item == nil {
				return
			}
			// Drop items without a detector — gate can't dispatch.
			if item.Detector == "" {
				item = nil
				return
			}
			switch current {
			case sectionP0:
				cl.P0 = append(cl.P0, *item)
			case sectionP1:
				cl.P1 = append(cl.P1, *item)
			case sectionP2:
				cl.P2 = append(cl.P2, *item)
			}
			item = nil
		}
	)

	for _, line := range strings.Split(body, "\n") {
		switch {
		case sectionP0Pattern.MatchString(line):
			commit()
			current = sectionP0
			continue
		case sectionP1Pattern.MatchString(line):
			commit()
			current = sectionP1
			continue
		case sectionP2Pattern.MatchString(line):
			commit()
			current = sectionP2
			continue
		}

		if m := itemHeadingPattern.FindStringSubmatch(line); m != nil && current != sectionNone {
			commit()
			item = &ChecklistItem{ID: m[1], Title: strings.TrimSpace(m[2])}
			continue
		}

		if item == nil {
			continue
		}
		if m := fieldPattern.FindStringSubmatch(line); m != nil {
			value := stripBackticks(m[2])
			switch m[1] {
			case "detector":
				item.Detector = value
			case "description":
				item.Description = value
			case "on_fail":
				item.OnFail = value
			case "only_if":
				item.OnlyIf = value
				// rationale / additional fields are tolerated but not
				// stored — they're documentation, not gate logic.
			}
		}
	}
	commit()

	return cl
}

// stripBackticks normalizes the value half of a "- field: value" row.
// Three shapes the parser needs to handle robustly:
//
//  1. Bare value: `detector: honest_data`         → "honest_data"
//  2. Wrapped:    `detector: \`honest_data\“     → "honest_data"
//  3. Wrapped + trailing prose / parenthetical (Chinese inline
//     authoring notes are common): `detector: \`name\` (note)`
//     → "name"
//
// Detector names are conventionally lower_snake_case identifiers; we
// take the first backtick-wrapped token when present, otherwise fall
// back to the value-up-to-first-whitespace-or-paren so a free-form
// note doesn't become part of the name.
func stripBackticks(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	// Case 2 + 3: pull the FIRST backtick-wrapped run.
	if i := strings.IndexByte(s, '`'); i >= 0 {
		// Find the closing backtick after the first one.
		if j := strings.IndexByte(s[i+1:], '`'); j >= 0 {
			return s[i+1 : i+1+j]
		}
	}
	// Case 1: no backticks; truncate at first whitespace OR
	// opening paren / em-dash. This catches "detector_name (note)"
	// and "detector_name — note" patterns.
	cut := len(s)
	for idx, r := range s {
		if r == ' ' || r == '\t' || r == '(' || r == '（' || r == '—' || r == '–' {
			cut = idx
			break
		}
	}
	return s[:cut]
}

// warnChecklistOnce dedupes warning emissions per skill to avoid
// spamming logs every turn when a checklist is malformed. Same
// rationale as the i18n missingKeys deduplication.
var (
	checklistWarnedSkills sync.Map
)

func warnChecklistOnce(skillName, reason string) {
	if _, loaded := checklistWarnedSkills.LoadOrStore(skillName, true); !loaded {
		globals.Warn(fmt.Sprintf("[Checklist] skill %q: %s", skillName, reason))
	}
}
