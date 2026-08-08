package detectors

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// CharacterAnchor enforces "the same character anchor reference appears
// across all generated images of the character" — the M5 P0 check
// for character mode.
//
// Sprint A scope (intentional): static-text inspection only. The
// detector reads character-spec.md from the thread workdir to learn
// the canonical anchor descriptor (e.g. "30s asian female, short
// black hair, navy jacket"), then verifies the artifact's text /
// image generation prompts mention these features consistently. This
// catches the "model forgot the navy jacket on shot 3" failure mode.
//
// What it doesn't do (Sprint A): pixel-level consistency analysis.
// Comparing generated PNG faces requires an embedding model — out
// of scope until M3 critique sub-agent (Sprint B) which can ask an
// LLM to spot-check.
//
// Skipped behavior:
//
//   - SkillName != "character" → not relevant
//   - No character-spec.md in thread workdir → no anchor to enforce
//   - No generation prompt strings in artifact text → no chance to
//     inspect (e.g. text-only output)
//
// Failure mode: each generation prompt that mentions a character (by
// role name) but omits one of the canonical features is one Issue.
// Up to 5 issues per run.
type CharacterAnchor struct{}

func (c *CharacterAnchor) Name() string { return "character_anchor_consistency" }

// canonicalFeaturesFromSpec extracts the anchor-defining features
// from character-spec.md. The schema (§M4 9-section) lists features
// under `## Appearance` — facial_features + outfit + body. We use a
// loose extraction: every list-item bullet becomes a feature string.
//
// Returns nil + nil when the file is missing.
func canonicalFeaturesFromSpec(threadDir string) ([]string, error) {
	if threadDir == "" {
		return nil, nil
	}
	specs := []string{"character-spec.md", "characters/spec.md"}
	for _, name := range specs {
		path := name
		if !strings.HasPrefix(path, "/") {
			path = threadDir + "/" + path
		}
		data, err := readFileTextSafe(path)
		if err != nil {
			continue
		}
		return extractFeatureLines(string(data)), nil
	}
	return nil, nil
}

// readFileTextSafe is a tiny wrapper around os.ReadFile that returns
// the data and any error (the spec scanning logic is identical to
// brand_spec_grep's read; lifting both into a helper would create a
// circular-ish utils file for two callsites).
func readFileTextSafe(path string) ([]byte, error) {
	return readFileBytes(path)
}

// extractFeatureLines pulls list-item bullets from the appearance /
// outfit / consistency sections of a character-spec.md. We don't run
// a real markdown parser — character-spec is a small file and a
// heuristic line scan is sufficient.
func extractFeatureLines(md string) []string {
	var inAppearance bool
	var out []string
	for _, line := range strings.Split(md, "\n") {
		l := strings.TrimSpace(line)
		// Section heading detection.
		if strings.HasPrefix(l, "##") {
			lower := strings.ToLower(l)
			inAppearance = strings.Contains(lower, "appearance") ||
				strings.Contains(lower, "outfit") ||
				strings.Contains(lower, "consistency_rules")
			continue
		}
		if !inAppearance {
			continue
		}
		// Bullet list items.
		if strings.HasPrefix(l, "- ") || strings.HasPrefix(l, "* ") {
			feat := strings.TrimSpace(l[2:])
			if feat != "" && len(feat) < 200 {
				out = append(out, feat)
			}
		}
	}
	return out
}

// promptStringPattern matches text that looks like an image-generation
// prompt — a quoted string preceded by a generation directive. We
// match on `"...descriptive text..."` near words like "prompt:",
// "generate", "image:". Imperfect but catches the common shapes used
// in the agent's text output before tool-calling an image generator.
var promptStringPattern = regexp.MustCompile(`(?i)(?:prompt|generate|image|render)\s*[:：]\s*"([^"]{20,400})"`)

func (c *CharacterAnchor) Run(ctx context.Context, in Input) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Skipped(fmt.Sprintf("context cancelled: %v", err)), nil
	}
	if in.SkillName != "character" {
		return Skipped(fmt.Sprintf("character_anchor_consistency only relevant for character skill, got %q", in.SkillName)), nil
	}

	features, err := canonicalFeaturesFromSpec(in.ThreadDir)
	if err != nil {
		return Skipped(fmt.Sprintf("read character-spec.md: %v", err)), nil
	}
	if len(features) == 0 {
		return Skipped("no character-spec.md or no canonical features"), nil
	}

	matches := promptStringPattern.FindAllStringSubmatch(in.Artifact.Text, -1)
	if len(matches) == 0 {
		return Skipped("no generation prompts found in artifact text"), nil
	}

	// Build a feature → distinctive-keyword map. The check is a
	// fuzzy contains, so we only need a stable keyword from each
	// feature line ("navy jacket" → "navy"). Pick the longest
	// alphabetic run as the keyword — works for both Latin and
	// CJK input where the full feature line might be a sentence.
	keywords := make([]string, 0, len(features))
	for _, f := range features {
		if kw := pickKeyword(f); kw != "" {
			keywords = append(keywords, kw)
		}
	}
	if len(keywords) == 0 {
		return Skipped("could not extract keywords from character-spec features"), nil
	}

	var issues []string
	for i, m := range matches {
		prompt := m[1]
		var missing []string
		for _, kw := range keywords {
			if !strings.Contains(strings.ToLower(prompt), strings.ToLower(kw)) {
				missing = append(missing, kw)
			}
		}
		if len(missing) > 0 {
			snippet := prompt
			if len(snippet) > 60 {
				snippet = snippet[:60] + "..."
			}
			issues = append(issues, fmt.Sprintf("prompt #%d %q omits canonical features: %v", i+1, snippet, missing))
			if len(issues) >= 5 {
				break
			}
		}
	}

	if len(issues) == 0 {
		return Pass(), nil
	}
	return Result{
		Status: StatusFail,
		Issues: issues,
		Trace: map[string]any{
			"prompts_inspected": len(matches),
			"keywords_required": keywords,
		},
	}, nil
}

// pickKeyword returns the longest contiguous alphanumeric / CJK run
// in the feature line, lowercased. For "30s, asian female, navy
// jacket" the picks are "asian", "female", "navy", "jacket"; we
// take the longest. This is approximate but good enough: the goal
// is to spot cases where the model dropped a feature entirely, not
// to do strict semantic matching.
func pickKeyword(feature string) string {
	var best string
	var current strings.Builder
	flush := func() {
		s := current.String()
		if len(s) > len(best) {
			best = s
		}
		current.Reset()
	}
	for _, r := range feature {
		switch {
		case isAlphaNumish(r):
			current.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return strings.ToLower(best)
}

func isAlphaNumish(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z':
		return true
	case r >= 'A' && r <= 'Z':
		return true
	case r >= '0' && r <= '9':
		return true
	case isCJK(r):
		return true
	}
	return false
}

func init() {
	Default().Register(&CharacterAnchor{})
}
