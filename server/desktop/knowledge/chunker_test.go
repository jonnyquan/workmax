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
		// Allow a little slack: a single over-long word is emitted as-is.
		if len(c) > 220 && !strings.Contains(c, " ") {
			continue
		}
		if len(c) > 220 {
			t.Errorf("chunk %d len %d exceeds budget+slack", i, len(c))
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

	// targetChars <= 0 falls back to default (no panic, produces chunks).
	got = ChunkText(strings.Repeat("x", DefaultChunkChars*3), 0)
	if len(got) < 2 {
		t.Fatalf("default budget: want multiple chunks, got %d", len(got))
	}
}
