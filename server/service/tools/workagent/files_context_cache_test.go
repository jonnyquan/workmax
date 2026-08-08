package workagent

import (
	"strings"
	"testing"
)

// The cache exists to keep the system prompt stable across turns when
// the file set hasn't changed — that's what lets Claude's prompt cache
// hit. These tests pin the contract: same fileset → same fingerprint
// → same returned context; any field change → fresh build.

func TestFilesContextFingerprint_StableForSameSet(t *testing.T) {
	a := []AgentFileInfo{
		{ID: "1", Name: "report.xlsx", Path: "uploads/report.xlsx", Type: "spreadsheet", Size: 1024},
		{ID: "2", Name: "notes.md", Path: "uploads/notes.md", Type: "text/markdown", Size: 256},
	}
	b := []AgentFileInfo{
		// Same set, different declared order
		{ID: "2", Name: "notes.md", Path: "uploads/notes.md", Type: "text/markdown", Size: 256},
		{ID: "1", Name: "report.xlsx", Path: "uploads/report.xlsx", Type: "spreadsheet", Size: 1024},
	}
	if filesContextFingerprint(a) != filesContextFingerprint(b) {
		t.Error("same file set in different order should produce identical fingerprint")
	}
}

func TestFilesContextFingerprint_DifferentForDifferentSets(t *testing.T) {
	cases := []struct {
		name string
		a, b []AgentFileInfo
	}{
		{
			name: "different ID",
			a:    []AgentFileInfo{{ID: "1", Name: "a", Path: "p", Type: "t", Size: 10}},
			b:    []AgentFileInfo{{ID: "2", Name: "a", Path: "p", Type: "t", Size: 10}},
		},
		{
			name: "different size",
			a:    []AgentFileInfo{{ID: "1", Name: "a", Path: "p", Type: "t", Size: 10}},
			b:    []AgentFileInfo{{ID: "1", Name: "a", Path: "p", Type: "t", Size: 11}},
		},
		{
			name: "different name",
			a:    []AgentFileInfo{{ID: "1", Name: "a", Path: "p", Type: "t", Size: 10}},
			b:    []AgentFileInfo{{ID: "1", Name: "b", Path: "p", Type: "t", Size: 10}},
		},
		{
			name: "different type",
			a:    []AgentFileInfo{{ID: "1", Name: "a", Path: "p", Type: "t1", Size: 10}},
			b:    []AgentFileInfo{{ID: "1", Name: "a", Path: "p", Type: "t2", Size: 10}},
		},
		{
			name: "different path",
			a:    []AgentFileInfo{{ID: "1", Name: "a", Path: "p1", Type: "t", Size: 10}},
			b:    []AgentFileInfo{{ID: "1", Name: "a", Path: "p2", Type: "t", Size: 10}},
		},
		{
			// In-place re-upload — same logical file, new content. Without
			// hash in the fingerprint the cache silently served the old
			// system prompt; this case pins that the new bytes bust the
			// cache.
			name: "same row, different hash",
			a:    []AgentFileInfo{{ID: "1", Name: "a", Path: "p", Type: "t", Size: 10, Hash: "abc123"}},
			b:    []AgentFileInfo{{ID: "1", Name: "a", Path: "p", Type: "t", Size: 10, Hash: "def456"}},
		},
		{
			// Hash-less rows fall back to mtime — same trap, same fix.
			name: "same row, different mtime",
			a:    []AgentFileInfo{{ID: "1", Name: "a", Path: "p", Type: "t", Size: 10, ModTime: 1_700_000_000_000_000_000}},
			b:    []AgentFileInfo{{ID: "1", Name: "a", Path: "p", Type: "t", Size: 10, ModTime: 1_700_000_001_000_000_000}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if filesContextFingerprint(tc.a) == filesContextFingerprint(tc.b) {
				t.Error("expected different fingerprints")
			}
		})
	}
}

func TestFilesContextFingerprint_SeparatorImmunity(t *testing.T) {
	// The fingerprint uses 0x1F (unit separator) as the field separator
	// to avoid the classic | / : collision where two different fields
	// could produce the same delimited string. Construct a pair that
	// would collide under | separation but not under 0x1F.
	a := []AgentFileInfo{{ID: "1", Name: "a|b", Path: "c", Type: "t", Size: 0}}
	b := []AgentFileInfo{{ID: "1", Name: "a", Path: "b|c", Type: "t", Size: 0}}
	if filesContextFingerprint(a) == filesContextFingerprint(b) {
		t.Error("'a|b'+'c' must not collide with 'a'+'b|c' even though pipe-joined they look identical")
	}
}

func TestBuildIntelligentFilesContext_CacheReturnsSameString(t *testing.T) {
	files := []AgentFileInfo{
		{ID: "1", Name: "data.csv", Path: "uploads/data.csv", Type: "text/csv", Size: 2048},
	}
	first := buildIntelligentFilesContext(files)
	second := buildIntelligentFilesContext(files)
	if first != second {
		t.Error("cached call returned different string than miss call")
	}
	if !strings.Contains(first, "data.csv") {
		t.Errorf("rendered context missing file name; got first 200 chars: %q", first[:min(200, len(first))])
	}
}

func TestBuildIntelligentFilesContext_DifferentSetsProduceDifferentStrings(t *testing.T) {
	a := []AgentFileInfo{{ID: "1", Name: "a.csv", Path: "uploads/a.csv", Type: "text/csv", Size: 100}}
	b := []AgentFileInfo{{ID: "2", Name: "b.csv", Path: "uploads/b.csv", Type: "text/csv", Size: 100}}
	if buildIntelligentFilesContext(a) == buildIntelligentFilesContext(b) {
		t.Error("different file sets should not produce identical context")
	}
}

func TestFilesContextCache_LRUEviction(t *testing.T) {
	// Verify the cache evicts the LRU entry when capacity is exceeded.
	// Tests on a fresh instance to avoid polluting the singleton.
	c := newFilesContextCache(2)

	c.put("a", "context-a")
	c.put("b", "context-b")
	c.put("c", "context-c") // should evict 'a'

	if _, ok := c.get("a"); ok {
		t.Error("'a' should have been evicted as the LRU tail")
	}
	if _, ok := c.get("b"); !ok {
		t.Error("'b' should still be cached")
	}
	if _, ok := c.get("c"); !ok {
		t.Error("'c' should still be cached")
	}
}
