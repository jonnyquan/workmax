//go:build desktop && cgo

package knowledge

import (
	"strings"
	"testing"
)

func TestChunkText(t *testing.T) {
	if got := ChunkText("", 100); got != nil {
		t.Errorf("empty input -> want nil, got %v", got)
	}
	if got := ChunkText("   \n\t ", 100); got != nil {
		t.Errorf("whitespace-only -> want nil, got %v", got)
	}

	// Single short paragraph → one chunk, verbatim.
	got := ChunkText("hello world", 100)
	if len(got) != 1 || got[0] != "hello world" {
		t.Fatalf("single paragraph: got %v", got)
	}

	// One long line (no paragraph breaks) → multiple chunks within budget.
	long := strings.Repeat("alpha bravo charlie delta. ", 40) // ~1.04 KiB
	got = ChunkText(long, 200)
	if len(got) < 2 {
		t.Fatalf("long line: want multiple chunks, got %d", len(got))
	}
	for i, c := range got {
		if runeLen(c) > 200 {
			t.Errorf("chunk %d is %d runes, over the 200 budget", i, runeLen(c))
		}
	}
	if joined := strings.Join(got, " "); !strings.Contains(joined, "alpha") || !strings.Contains(joined, "delta") {
		t.Error("chunking lost source content")
	}

	// Paragraph merging: two short paragraphs fit in one chunk; a third long
	// one forces a new chunk.
	p1 := strings.Repeat("a", 50)
	p2 := strings.Repeat("b", 50)
	p3 := strings.Repeat("c", 200) // single over-long "word"
	text := p1 + "\n\n" + p2 + "\n\n" + p3
	got = ChunkText(text, 120)
	if len(got) < 2 {
		t.Fatalf("paragraphs: want >=2 chunks, got %d: %v", len(got), got)
	}
	if !strings.Contains(got[0], p1) || !strings.Contains(got[0], p2) {
		t.Errorf("first chunk should merge p1+p2: %q", got[0])
	}

	// targetRunes <= 0 selects the adaptive budget (no panic, produces chunks).
	got = ChunkText(strings.Repeat("x", DefaultChunkRunes*3), 0)
	if len(got) < 2 {
		t.Fatalf("default budget: want multiple chunks, got %d", len(got))
	}
}

// The bug this file exists to fix. A flat character budget sized for English
// put ~400 Chinese characters — about 400 WordPiece tokens, since BERT-style
// tokenizers treat each CJK character as its own token — into a chunk the
// model truncates at 128. Two thirds of every Chinese chunk was embedded as
// nothing and indexed as if it had been read.
//
// The assertion is on the estimated *token* count, not on runes, because
// tokens are what actually truncates and runes are only a proxy for them.
func TestChunkText_StaysInsideTheModelWindow(t *testing.T) {
	chinese := strings.Repeat("本地优先的知识检索需要在离线状态下工作，因此模型必须完全跑在用户自己的机器上。", 40)
	english := strings.Repeat("Local-first retrieval has to work offline, so the model runs entirely on the user's own machine. ", 40)
	mixed := strings.Repeat("本地检索 uses the MiniLM sentence embedding model 做向量化，maxSeqLength 是 128。", 40)
	code := strings.Repeat("func (s *Store) Search(ctx context.Context, uid uint64, queryVec []float32) error {\n\treturn nil\n}\n", 20)

	for _, c := range []struct{ name, text string }{
		{"chinese", chinese},
		{"english", english},
		{"mixed", mixed},
		{"code", code},
	} {
		chunks := ChunkText(c.text, 0)
		if len(chunks) == 0 {
			t.Fatalf("%s: no chunks", c.name)
		}
		for i, ch := range chunks {
			if tokens := EstimateTokens(ch); tokens > maxChunkTokens {
				t.Errorf("%s chunk %d: ~%d tokens (%d runes), over the %d budget — this is text the embedder never sees",
					c.name, i, tokens, runeLen(ch), maxChunkTokens)
			}
		}
		t.Logf("%s: %d chunks, first is %d runes / ~%d tokens",
			c.name, len(chunks), runeLen(chunks[0]), EstimateTokens(chunks[0]))
	}
}

// The adaptive budget has to actually adapt: an English document must not be
// chopped into Chinese-sized pieces, and a Chinese one must not keep the
// English size. Both directions matter — the second is the bug, the first is
// the regression a blunt fix would introduce.
func TestAdaptiveChunkRunes_ScalesWithScript(t *testing.T) {
	english := adaptiveChunkRunes("Local-first retrieval has to work offline on the user's own machine.")
	chinese := adaptiveChunkRunes("本地优先的知识检索需要在离线状态下工作，模型完全跑在用户自己的机器上。")
	mixed := adaptiveChunkRunes("本地检索 uses MiniLM 做向量化 with a 128 token window。")

	if english != DefaultChunkRunes {
		t.Errorf("pure Latin budget = %d, want the Latin default %d", english, DefaultChunkRunes)
	}
	if chinese > 130 {
		t.Errorf("pure CJK budget = %d runes, which is ~%d tokens — over the model's window", chinese, chinese)
	}
	if !(chinese < mixed && mixed < english) {
		t.Errorf("budgets should interpolate: cjk=%d mixed=%d latin=%d", chinese, mixed, english)
	}
}

// A Chinese paragraph is frequently one whitespace-free run hundreds of runes
// long, so the rune hard-split is the ordinary path for Chinese rather than a
// guard against pathological input. Splitting it by bytes would cut runes in
// half and produce mojibake in the index.
func TestChunkText_SplitsUnbrokenCJKRunsWithoutCorruptingRunes(t *testing.T) {
	para := strings.Repeat("知识检索必须离线可用", 40) // 400 runes, no spaces at all
	chunks := ChunkText(para, 0)
	if len(chunks) < 3 {
		t.Fatalf("an unbroken 400-rune Chinese paragraph produced %d chunks", len(chunks))
	}
	var rebuilt strings.Builder
	for _, c := range chunks {
		if strings.ContainsRune(c, '�') {
			t.Fatalf("chunk contains a replacement rune, so the split cut a character: %q", c)
		}
		rebuilt.WriteString(c)
	}
	if rebuilt.String() != para {
		t.Error("splitting an unbroken CJK paragraph lost or reordered content")
	}
}

func TestEstimateTokens(t *testing.T) {
	// One token per CJK character.
	if got := EstimateTokens("知识检索"); got != 4 {
		t.Errorf("EstimateTokens(4 CJK runes) = %d, want 4", got)
	}
	// ~3.6 Latin characters per token; whitespace is not counted.
	if got := EstimateTokens(strings.Repeat("a", 36)); got != 10 {
		t.Errorf("EstimateTokens(36 latin runes) = %d, want 10", got)
	}
	if got := EstimateTokens(""); got != 0 {
		t.Errorf("EstimateTokens(\"\") = %d, want 0", got)
	}
}
