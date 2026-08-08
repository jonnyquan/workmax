package canvas

import (
	"strings"
	"testing"
)

func TestNormalizeCanvasStorageKey(t *testing.T) {
	key, err := NormalizeCanvasStorageKey("  el-123  ", "elementId", true)
	if err != nil {
		t.Fatalf("expected valid key, got %v", err)
	}
	if key != "el-123" {
		t.Fatalf("expected trimmed key, got %q", key)
	}

	if _, err := NormalizeCanvasStorageKey(strings.Repeat("a", MaxCanvasStorageKeyBytes+1), "elementId", true); err == nil {
		t.Fatal("expected overlong key to fail")
	}
	if _, err := NormalizeCanvasStorageKey("../escape", "elementId", true); err == nil {
		t.Fatal("expected path-like key to fail")
	}
	if _, err := NormalizeCanvasStorageKey("line\nbreak", "elementId", true); err == nil {
		t.Fatal("expected control character to fail")
	}
	if key, err := NormalizeCanvasStorageKey("  ", "elementId", false); err != nil || key != "" {
		t.Fatalf("optional blank key should normalize to empty, got key=%q err=%v", key, err)
	}
}

func TestValidateShotLinksForSyncRejectsUnsafeInput(t *testing.T) {
	if err := ValidateShotLinksForSync([]ShotLink{{LocalCardID: "card-a", OrderIndex: 1}}); err != nil {
		t.Fatalf("expected valid shot links, got %v", err)
	}

	if err := ValidateShotLinksForSync([]ShotLink{{LocalCardID: strings.Repeat("x", MaxCanvasStorageKeyBytes+1)}}); err == nil {
		t.Fatal("expected overlong localCardId to fail")
	}

	negative := int64(-1)
	if err := ValidateShotLinksForSync([]ShotLink{{LocalCardID: "card-a", TimelineStart: &negative}}); err == nil {
		t.Fatal("expected negative timelineStart to fail")
	}

	shots := make([]ShotLink, MaxShotSyncItems+1)
	for i := range shots {
		shots[i].LocalCardID = "card-" + string(rune('a'+(i%26)))
	}
	if err := ValidateShotLinksForSync(shots); err == nil {
		t.Fatal("expected oversized shot sync payload to fail")
	}
}
