package detectors

import (
	"context"
	"fmt"
	"regexp"
)

// RegexScanner is a generic detector that flags the artifact's text
// when it matches any pattern in a configured pattern set. Used as
// the engine behind both honest_data (fabricated metrics) and the
// anti-ai-slop blacklist (forbidden visual / vocabulary clichés).
//
// The patterns themselves are NOT in this file — they're held by
// the concrete detectors (HonestData, AntiSlop) so each can carry
// its own per-locale dictionary and its own warning thresholds. This
// type is the shared regex-execution machinery.
//
// Performance contract: every pattern is compiled once at construction
// time (NewRegexScanner panics on a malformed pattern, which would
// be a build-time bug). Run does N * O(text) regex passes; for 18-lang
// dictionaries and ~10KB artifacts that's still well under 100ms.
type RegexScanner struct {
	name       string
	categories []regexCategory
}

// regexCategory groups related patterns under a single human-readable
// description. The Issues slice in a Fail result is generated from
// the description + matched substring, so dashboards can group fails
// by category without parsing the issue text.
type regexCategory struct {
	description string
	patterns    []*regexp.Regexp
}

// NewRegexScanner builds a scanner with the supplied name and pattern
// categories. Panics on a malformed regex (build-time bug).
//
// Each category is a (description, []patternStrings) pair. Patterns
// compile with default Go regexp flags. If a pattern needs case
// insensitivity or multiline, use the (?i) / (?m) inline modifiers.
func NewRegexScanner(name string, cats map[string][]string) *RegexScanner {
	if name == "" {
		panic("detectors: NewRegexScanner empty name")
	}
	out := &RegexScanner{name: name}
	for desc, raws := range cats {
		var compiled []*regexp.Regexp
		for _, raw := range raws {
			re, err := regexp.Compile(raw)
			if err != nil {
				panic(fmt.Sprintf("detectors: %s pattern %q failed to compile: %v", name, raw, err))
			}
			compiled = append(compiled, re)
		}
		out.categories = append(out.categories, regexCategory{description: desc, patterns: compiled})
	}
	return out
}

// Name implements Detector.
func (s *RegexScanner) Name() string { return s.name }

// Run scans the artifact's text against every configured pattern.
// Returns Pass when no patterns match, Fail with one issue per
// matched category, Skipped when the artifact has no text (the
// scanner has nothing to scan; another detector might pick up the
// file-based content).
//
// Each category contributes at most one Issue regardless of how
// many patterns within the category fired — the user only needs
// to know "fabricated metrics detected", not "13 fabricated
// metrics detected". Multi-fire details land in Trace for
// dashboards.
func (s *RegexScanner) Run(ctx context.Context, in Input) (Result, error) {
	text := in.Artifact.Text
	if text == "" {
		return Skipped("no text in artifact to scan"), nil
	}

	if err := ctx.Err(); err != nil {
		return Skipped(fmt.Sprintf("context cancelled before scan: %v", err)), nil
	}

	var issues []string
	trace := map[string]any{}

	for _, cat := range s.categories {
		hits := []string{}
		for _, re := range cat.patterns {
			matches := re.FindAllString(text, 5) // cap per-pattern at 5 hits
			hits = append(hits, matches...)
			if len(hits) >= 5 {
				break
			}
		}
		if len(hits) > 0 {
			// One issue per category. Include up to 3 example
			// hits in the issue text — the user gets specific
			// snippets they can search for.
			example := hits
			if len(example) > 3 {
				example = example[:3]
			}
			issues = append(issues, fmt.Sprintf("%s: e.g. %v", cat.description, example))
			trace[cat.description] = hits
		}
	}

	if len(issues) == 0 {
		return Pass(), nil
	}
	return Result{Status: StatusFail, Issues: issues, Trace: trace}, nil
}
