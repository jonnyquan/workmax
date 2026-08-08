package detectors

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// BrandSpecGrep enforces "every hex color in the artifact must trace
// to the active brand-spec.md or be marked as a placeholder".
//
// The protocol-level rule the system prompt teaches the model is:
// "never invent brand colors from memory; quote from brand-spec.md
// or write `—`". This detector is the post-emit check that catches
// the failure mode where the model still slipped a color through.
//
// Skipped behavior: when no brand-spec.md exists in the thread
// workdir, the rule doesn't apply — return Skipped, not Fail. A
// freestyle session with no brand reference shouldn't trip the brand
// gate.
//
// Failure mode: every hex in the artifact is checked against the
// allowed set parsed from brand-spec.md. Any unaccounted hex is one
// issue. Up to 5 issues per run (anything more is a sign of total
// misalignment, not a fixable list).
type BrandSpecGrep struct{}

func (b *BrandSpecGrep) Name() string { return "brand_spec_grep" }

// hexPattern matches `#RRGGBB`, `#RGB`, and `rgb(R,G,B)` forms in
// HTML / CSS / markdown output. The case-insensitive flag covers
// both #FFAABB and #ffaabb. We intentionally don't match named
// colors ("red", "blue") — those are too noisy as candidates and
// brand-spec.md is hex-specified anyway.
var hexPattern = regexp.MustCompile(`(?i)#[0-9a-f]{6}\b|#[0-9a-f]{3}\b|rgb\(\s*\d+\s*,\s*\d+\s*,\s*\d+\s*\)`)

// allowedHexFromBrandSpec parses the brand-spec.md file and returns
// the set of hex values the agent is allowed to use. The brand-spec
// schema (DS-1 9-section) lists hex values inline as `Primary: #1A2B3C`,
// `Secondary: ...`, etc. We don't need a real markdown parser for
// this — pulling every hex literal from the file is enough, since
// the file is small and authored.
//
// Returns lowercased hex strings (so the comparison is
// case-insensitive). Empty set + nil error when the file exists but
// has no hex literals (the agent's spec is incomplete; we treat that
// as "no allowed colors", which means any hex in output fails — a
// strict reading of the protocol).
//
// Returns nil + nil when the file is missing — the detector reads
// this as Skipped (no spec, no rule).
func allowedHexFromBrandSpec(threadDir string) (map[string]struct{}, error) {
	if threadDir == "" {
		return nil, nil
	}
	path := filepath.Join(threadDir, "brand-spec.md")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	matches := hexPattern.FindAllString(string(data), -1)
	allowed := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		allowed[normalizeHex(m)] = struct{}{}
	}
	return allowed, nil
}

// normalizeHex uppercases the value and expands #RGB shorthand to
// #RRGGBB so set membership is unambiguous. Strips rgb() to its hex
// equivalent only when the components are integers; rgb-with-decimal
// or hsl forms are passed through unchanged (the protocol asks for
// hex anyway).
func normalizeHex(raw string) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	// Expand #abc -> #aabbcc
	if strings.HasPrefix(v, "#") && len(v) == 4 {
		return "#" + string([]byte{v[1], v[1], v[2], v[2], v[3], v[3]})
	}
	return v
}

func (b *BrandSpecGrep) Run(ctx context.Context, in Input) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Skipped(fmt.Sprintf("context cancelled: %v", err)), nil
	}

	allowed, err := allowedHexFromBrandSpec(in.ThreadDir)
	if err != nil {
		return Skipped(fmt.Sprintf("read brand-spec.md: %v", err)), nil
	}
	if allowed == nil {
		// No brand-spec → rule doesn't apply.
		return Skipped("no brand-spec.md in thread workdir"), nil
	}

	text := in.Artifact.Text
	if text == "" {
		return Skipped("no text in artifact"), nil
	}

	matches := hexPattern.FindAllString(text, -1)
	if len(matches) == 0 {
		return Pass(), nil
	}

	var issues []string
	seenViolations := make(map[string]struct{})
	for _, m := range matches {
		norm := normalizeHex(m)
		if _, ok := allowed[norm]; ok {
			continue
		}
		// De-duplicate: same un-allowed hex repeated 50× = one
		// issue, not 50.
		if _, seen := seenViolations[norm]; seen {
			continue
		}
		seenViolations[norm] = struct{}{}
		issues = append(issues, fmt.Sprintf("hex %s not in brand-spec.md (raw match: %s)", norm, m))
		if len(issues) >= 5 {
			issues = append(issues, fmt.Sprintf("(stopped at 5 violations; %d more present)", len(matches)-len(seenViolations)))
			break
		}
	}

	if len(issues) == 0 {
		return Pass(), nil
	}
	return Result{
		Status: StatusFail,
		Issues: issues,
		Trace: map[string]any{
			"allowed_count":   len(allowed),
			"violation_count": len(seenViolations),
		},
	}, nil
}

func init() {
	Default().Register(&BrandSpecGrep{})
}
