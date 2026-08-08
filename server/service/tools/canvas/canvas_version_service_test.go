package canvas

import (
	"testing"
)

// Pure-layer tests for canvas_version_service.go. The DB-coupled
// wrappers (GetCanvasVersion / ListCanvasVersions / CreateCanvasVersion /
// UpdateCanvasVersion) are exercised by the handler-level integration
// suite that already owns a MySQL fixture — this file pins the
// invariants that make those wrappers deterministic.

func TestNormalizeVersionSource_EmptyDefaultsToManual(t *testing.T) {
	if got := NormalizeVersionSource(""); got != "manual" {
		t.Errorf("NormalizeVersionSource(\"\") = %q, want %q", got, "manual")
	}
}

func TestNormalizeVersionSource_WhitespaceDefaultsToManual(t *testing.T) {
	if got := NormalizeVersionSource("   "); got != "manual" {
		t.Errorf("NormalizeVersionSource(spaces) = %q, want manual", got)
	}
}

func TestNormalizeVersionSource_TrimsAndKeepsValid(t *testing.T) {
	if got := NormalizeVersionSource("  auto-save  "); got != "auto-save" {
		t.Errorf("NormalizeVersionSource trim = %q, want auto-save", got)
	}
}

func TestNormalizeListVersionsInput_DefaultsWhenZero(t *testing.T) {
	out := normalizeListVersionsInput(ListVersionsInput{})
	if out.Page != 1 {
		t.Errorf("page default = %d, want 1", out.Page)
	}
	if out.Limit != defaultListVersionsLimit {
		t.Errorf("limit default = %d, want %d", out.Limit, defaultListVersionsLimit)
	}
}

func TestNormalizeListVersionsInput_NegativePageClampsToOne(t *testing.T) {
	out := normalizeListVersionsInput(ListVersionsInput{Page: -5, Limit: 10})
	if out.Page != 1 {
		t.Errorf("page = %d, want clamp to 1", out.Page)
	}
	if out.Limit != 10 {
		t.Errorf("limit = %d, want passthrough 10", out.Limit)
	}
}

func TestNormalizeListVersionsInput_LimitUpperBound(t *testing.T) {
	out := normalizeListVersionsInput(ListVersionsInput{Page: 3, Limit: 5000})
	if out.Limit != maxListVersionsLimit {
		t.Errorf("limit = %d, want clamp to %d", out.Limit, maxListVersionsLimit)
	}
	if out.Page != 3 {
		t.Errorf("page should pass through, got %d", out.Page)
	}
}

func TestNormalizeListVersionsInput_IncludeTotalPassesThrough(t *testing.T) {
	out := normalizeListVersionsInput(ListVersionsInput{IncludeTotal: true})
	if !out.IncludeTotal {
		t.Errorf("IncludeTotal should pass through true")
	}
	out = normalizeListVersionsInput(ListVersionsInput{IncludeTotal: false})
	if out.IncludeTotal {
		t.Errorf("IncludeTotal should pass through false")
	}
}
