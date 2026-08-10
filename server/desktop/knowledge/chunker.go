//go:build desktop && cgo

package knowledge

import (
	"regexp"
	"strings"
	"unicode"
)

// Chunk sizing is a token budget expressed in runes, because the thing that
// actually truncates is the tokenizer and the thing we can cheaply count is
// runes.
//
// The estimate below is the whole basis for the numbers in this file:
//
//   - MiniLM is a BERT-family WordPiece model. BERT-style pre-tokenization puts
//     whitespace around every CJK ideograph, so a Chinese character is one
//     token — sometimes two, when the character is out of vocabulary and falls
//     back to [UNK]. Call it 1.0 token per CJK rune.
//   - Latin text averages ~4.7 characters per word and ~1.3 WordPiece tokens
//     per word, i.e. ~3.6 characters per token. Call it 1/3.6 token per rune.
//
// maxChunkTokens is the ceiling those estimates aim at. all-MiniLM-L6-v2 ships
// a tokenizer whose truncation is configured at 128 tokens; two of those are
// [CLS]/[SEP], and the estimate itself is approximate, so the target leaves a
// margin below 126 rather than sitting on it. Anything above the cap is not a
// degraded chunk — it is text that never reaches the model at all, silently.
//
// The old constant was a flat 400 *characters* with a comment that reasoned
// only about English. On Chinese that is ~400 tokens against a 128-token line:
// roughly two thirds of every Chinese chunk was dropped inside the tokenizer,
// embedded as a prefix, and indexed under text the vector never saw.
const (
	// maxChunkTokens is the token budget a chunk aims to stay under.
	maxChunkTokens = 110

	// tokensPerCJKRune / runesPerLatinToken are the two estimates above.
	tokensPerCJKRune   = 1.0
	runesPerLatinToken = 3.6
)

// DefaultChunkRunes is the rune budget for pure-Latin text — the value
// adaptiveChunkRunes returns when a document contains no CJK at all
// (110 tokens × 3.6 runes/token ≈ 396). Kept as a named constant because it is
// the number every English-language expectation in the tests is anchored to.
const DefaultChunkRunes = int(maxChunkTokens * runesPerLatinToken)

// minChunkRunes floors the adaptive budget. A pathological document (dense CJK
// with the estimate erring pessimistic) must still produce chunks large enough
// to carry a sentence.
const minChunkRunes = 80

var paragraphBreak = regexp.MustCompile(`(?:\r?\n)[ \t]*(?:\r?\n)+`)

// ChunkText splits extracted text into chunks of at most ~targetRunes runes,
// breaking on paragraph boundaries and word-falling-back when a single
// paragraph exceeds the budget. Chunks are non-empty and trimmed. Returns nil
// for empty input.
//
// targetRunes <= 0 selects the adaptive budget for this text's script mix (see
// adaptiveChunkRunes) — which is what every production caller passes, because
// the right budget for a page of Chinese is a third of the right budget for a
// page of English and only the text knows which it is.
//
// Counting is in runes throughout. Byte counting made a Chinese chunk a third
// the size of a Latin one for the same budget, which is the opposite of the
// correction this file needs: Chinese wants *fewer* units per chunk, not fewer
// bytes.
//
// It deliberately does not overlap. Overlap exists to recover a match that
// straddles a chunk boundary; hybrid retrieval now covers that case from the
// other side, since the FTS5 shadow index matches literal terms wherever they
// land. Paying for overlap would mean duplicating text in both indexes and
// double-counting it in the injection budget, to fix something the lexical
// half already catches.
func ChunkText(text string, targetRunes int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if targetRunes <= 0 {
		targetRunes = adaptiveChunkRunes(text)
	}

	// 1. Split into paragraphs on blank lines; word-split any paragraph that
	//    alone exceeds the budget. After this every segment is <= targetRunes
	//    (except possibly a single over-long word, which is hard-split too).
	var segments []string
	for _, para := range paragraphBreak.Split(text, -1) {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		if runeLen(para) <= targetRunes {
			segments = append(segments, para)
			continue
		}
		segments = append(segments, splitByWords(para, targetRunes)...)
	}
	if len(segments) == 0 {
		return nil
	}

	// 2. Greedily merge consecutive segments into chunks <= targetRunes.
	var chunks []string
	var cur strings.Builder
	curRunes := 0
	for _, seg := range segments {
		segRunes := runeLen(seg)
		if curRunes == 0 {
			cur.WriteString(seg)
			curRunes = segRunes
			continue
		}
		// +2 accounts for the "\n\n" join between merged paragraphs.
		if curRunes+2+segRunes <= targetRunes {
			cur.WriteString("\n\n")
			cur.WriteString(seg)
			curRunes += 2 + segRunes
			continue
		}
		chunks = append(chunks, cur.String())
		cur.Reset()
		cur.WriteString(seg)
		curRunes = segRunes
	}
	if curRunes > 0 {
		chunks = append(chunks, cur.String())
	}
	return chunks
}

// adaptiveChunkRunes derives the rune budget from the text's own script mix.
//
// Solving maxChunkTokens = n·(r·tokensPerCJKRune + (1-r)/runesPerLatinToken)
// for n, where r is the fraction of runes that are CJK, gives a budget that
// lands on the same *token* count whatever the language: ~396 runes for pure
// English, ~110 for pure Chinese, and a smooth interpolation for the mixed
// documents that are the common case here (Chinese prose quoting code, a
// bilingual spec).
func adaptiveChunkRunes(text string) int {
	cjk, total := 0, 0
	for _, r := range text {
		if unicode.IsSpace(r) {
			continue
		}
		total++
		if isCJK(r) {
			cjk++
		}
	}
	if total == 0 {
		return DefaultChunkRunes
	}
	ratio := float64(cjk) / float64(total)
	tokensPerRune := ratio*tokensPerCJKRune + (1-ratio)/runesPerLatinToken
	budget := int(maxChunkTokens / tokensPerRune)
	if budget < minChunkRunes {
		return minChunkRunes
	}
	if budget > DefaultChunkRunes {
		return DefaultChunkRunes
	}
	return budget
}

// EstimateTokens reports the approximate WordPiece token count of a string
// under the estimates documented at the top of this file. It exists so tests
// (and any future budget tuning) can assert the property that actually
// matters — chunks fit the model's window — rather than a rune count that only
// stands in for it.
func EstimateTokens(text string) int {
	cjk, other := 0, 0
	for _, r := range text {
		if unicode.IsSpace(r) {
			continue
		}
		if isCJK(r) {
			cjk++
			continue
		}
		other++
	}
	return int(float64(cjk)*tokensPerCJKRune + float64(other)/runesPerLatinToken)
}

// isCJK reports whether a rune is one the tokenizer will treat as its own
// token: Han ideographs plus the kana that share the same pre-tokenization
// rule. Hangul is included for the same reason.
func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r)
}

func runeLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

// splitByWords packs a too-long paragraph into pieces no longer than target
// runes.
//
// "Words" are whitespace-separated, which is the right unit for Latin text and
// the wrong one for Chinese — a Chinese paragraph is frequently one
// whitespace-free run hundreds of runes long. That case is not an edge case
// here, it is the common one, so the rune hard-split below is a first-class
// path rather than a guard against base64 blobs: it is how every Chinese
// paragraph over budget gets divided.
func splitByWords(paragraph string, target int) []string {
	words := strings.Fields(paragraph)
	var pieces []string
	var cur strings.Builder
	curRunes := 0
	flush := func() {
		if curRunes > 0 {
			pieces = append(pieces, cur.String())
			cur.Reset()
			curRunes = 0
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
			if w == "" {
				continue
			}
		}
		wRunes := runeLen(w)
		switch {
		case curRunes == 0:
			cur.WriteString(w)
			curRunes = wRunes
		case curRunes+1+wRunes <= target:
			cur.WriteByte(' ')
			cur.WriteString(w)
			curRunes += 1 + wRunes
		default:
			flush()
			cur.WriteString(w)
			curRunes = wRunes
		}
	}
	flush()
	return pieces
}
