//go:build desktop && cgo

package knowledge

import (
	"regexp"
	"strings"
)

// DefaultChunkChars is the default soft character budget for a knowledge chunk.
// Sized for the MiniLM tokenizer's 128-token truncation: ~400 chars is roughly
// 100 English tokens, leaving headroom so a chunk is fully encoded (no
// truncation) while still coarse enough to carry meaningful context.
const DefaultChunkChars = 400

var paragraphBreak = regexp.MustCompile(`(?:\r?\n)[ \t]*(?:\r?\n)+`)

// ChunkText splits extracted text into chunks of at most ~targetChars, breaking
// on paragraph boundaries and word-falling-back when a single paragraph
// exceeds the budget. Chunks are non-empty and trimmed. Returns nil for empty
// input. targetChars <= 0 falls back to DefaultChunkChars.
//
// It deliberately does not overlap: the goal is faithful, non-redundant
// coverage of the source. Overlap can be added later if boundary recall proves
// weak in practice.
func ChunkText(text string, targetChars int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if targetChars <= 0 {
		targetChars = DefaultChunkChars
	}

	// 1. Split into paragraphs on blank lines; word-split any paragraph that
	//    alone exceeds the budget. After this every segment is <= targetChars
	//    (except possibly a single over-long word, which the embedder truncates).
	var segments []string
	for _, para := range paragraphBreak.Split(text, -1) {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		if len(para) <= targetChars {
			segments = append(segments, para)
			continue
		}
		segments = append(segments, splitByWords(para, targetChars)...)
	}
	if len(segments) == 0 {
		return nil
	}

	// 2. Greedily merge consecutive segments into chunks <= targetChars.
	var chunks []string
	var cur strings.Builder
	for _, seg := range segments {
		if cur.Len() == 0 {
			cur.WriteString(seg)
			continue
		}
		// +2 accounts for the "\n\n" join between merged paragraphs.
		if cur.Len()+2+len(seg) <= targetChars {
			cur.WriteString("\n\n")
			cur.WriteString(seg)
			continue
		}
		chunks = append(chunks, cur.String())
		cur.Reset()
		cur.WriteString(seg)
	}
	if cur.Len() > 0 {
		chunks = append(chunks, cur.String())
	}
	return chunks
}

// splitByWords packs the words of a too-long paragraph into pieces no longer
// than target (joined by spaces). A single word longer than target is
// hard-split by runes into target-sized pieces, so a pathological token
// (base64, minified blob) is indexed in pieces instead of one over-long chunk
// the embedder would truncate to 128 tokens.
func splitByWords(paragraph string, target int) []string {
	words := strings.Fields(paragraph)
	var pieces []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			pieces = append(pieces, cur.String())
			cur.Reset()
		}
	}
	for _, w := range words {
		if r := []rune(w); len(r) > target {
			flush()
			for len(r) > target {
				pieces = append(pieces, string(r[:target]))
				r = r[target:]
			}
			w = string(r) // leftover tail, now <= target
		}
		switch {
		case cur.Len() == 0:
			cur.WriteString(w)
		case cur.Len()+1+len(w) <= target:
			cur.WriteByte(' ')
			cur.WriteString(w)
		default:
			flush()
			cur.WriteString(w)
		}
	}
	flush()
	return pieces
}
