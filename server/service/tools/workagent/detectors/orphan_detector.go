package detectors

import (
	"context"
	"fmt"
	"strings"
	"unicode"
)

// OrphanDetector flags typographic orphans — single-word lines that
// hang at the end of a paragraph or column, breaking visual rhythm.
// Used by ppt and pictureBook checklists as a P1 check.
//
// Definition (CJK + Latin combined): a "line" of text that contains
// only one word AND is not the only line of its block. The detector
// scans the artifact's text by markdown headings and paragraph
// boundaries; output that's a flat dump (no paragraph breaks) is
// Skipped, since the orphan concept doesn't apply.
//
// What it catches:
//
//   - "...because\nimportant.\n\n" (the second line is one word)
//   - "我们的目标是\n创新。\n\n" (CJK orphan, one character is one
//     "word" in CJK typography sense)
//
// What it doesn't catch:
//
//   - Headings: a single-word heading is not an orphan, it's a title.
//   - Captions / labels: short by design, not orphans.
//
// We approximate by skipping markdown heading lines (start with #).
// Caption / label discrimination is not feasible from text alone, so
// some false positives are expected; that's why this is P1 (warn),
// not P0 (block).
type OrphanDetector struct{}

func (o *OrphanDetector) Name() string { return "orphan_detector" }

// isWordy returns true when the line contains at least 2 graphemes
// that look like content (letter / digit / CJK ideograph). Punctuation,
// whitespace, and markdown markers don't count toward the threshold.
//
// CJK languages have a ~1 char = 1 word density, so we apply a stricter
// rule there: lines with fewer than 3 CJK characters are flagged.
// Latin lines with fewer than 2 words are flagged. The threshold is
// generous on purpose — false-positive on a 2-char Latin word is much
// worse than missing a 4-char CJK orphan.
func contentRuneCount(line string) (latinWords int, cjkChars int) {
	inWord := false
	for _, r := range line {
		switch {
		case isCJK(r):
			cjkChars++
			inWord = false
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if !inWord {
				latinWords++
				inWord = true
			}
		default:
			inWord = false
		}
	}
	return
}

// isCJK reports whether r is a CJK ideograph. Covers the basic CJK
// Unified Ideographs block and extension A. Hangul / Kana fall outside
// (they have their own typographic rules); we conservatively treat
// them as Latin-like.
func isCJK(r rune) bool {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF:
		return true
	case r >= 0x3400 && r <= 0x4DBF:
		return true
	}
	return false
}

// looksLikeOrphan returns true for single-word / single-CJK-char
// lines that aren't headings, list items, or empty.
func looksLikeOrphan(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	// Markdown heading / list / code marker.
	if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "- ") ||
		strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "```") {
		return false
	}
	latin, cjk := contentRuneCount(trimmed)
	switch {
	case cjk > 0 && cjk < 3 && latin == 0:
		return true
	case cjk == 0 && latin == 1:
		return true
	}
	return false
}

func (o *OrphanDetector) Run(ctx context.Context, in Input) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Skipped(fmt.Sprintf("context cancelled: %v", err)), nil
	}

	// Only run for skills where typography matters.
	switch in.SkillName {
	case "ppt", "pictureBook", "marketingPoster":
		// proceed
	default:
		return Skipped(fmt.Sprintf("orphan_detector not relevant for skill %q", in.SkillName)), nil
	}

	text := in.Artifact.Text
	if text == "" {
		return Skipped("no text in artifact"), nil
	}

	// Split into paragraph blocks (double-newline separated).
	// Within a block, look for orphans on the LAST line — that's
	// the only position that actually breaks rhythm. A 1-word line
	// in the middle of a block is intentional.
	blocks := strings.Split(text, "\n\n")
	var orphans []string
	for _, block := range blocks {
		lines := strings.Split(strings.TrimRight(block, "\n"), "\n")
		if len(lines) < 2 {
			// Single-line block — orphan rule doesn't apply.
			continue
		}
		last := lines[len(lines)-1]
		if looksLikeOrphan(last) {
			orphans = append(orphans, fmt.Sprintf("orphan line: %q", strings.TrimSpace(last)))
		}
		if len(orphans) >= 5 {
			break
		}
	}

	if len(orphans) == 0 {
		return Pass(), nil
	}
	return Result{Status: StatusFail, Issues: orphans}, nil
}

func init() {
	Default().Register(&OrphanDetector{})
}
