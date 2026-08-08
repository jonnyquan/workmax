package workagent

// brand_palette_test.go — coverage for ExtractPaletteSwatches +
// helpers. The function walks heterogeneous JSON shapes (M4 protocol
// doesn't pin a single layout) hunting for hex color values. Three
// contracts to pin:
//
//   1. Conventional slots lead the output in canonical order
//      (primary → accent → muted → bg → fg).
//   2. Other hex values follow in deterministic alphabetical key
//      order — prompt-cache stability depends on this.
//   3. Cap at 4 swatches; dedupe (case-insensitive); reject non-hex.

import (
	"reflect"
	"testing"
)

func TestExtractPaletteSwatches_NilOrEmpty(t *testing.T) {
	if got := ExtractPaletteSwatches(nil); got != nil {
		t.Errorf("nil input → want nil, got %v", got)
	}
	if got := ExtractPaletteSwatches([]byte{}); got != nil {
		t.Errorf("empty input → want nil, got %v", got)
	}
	if got := ExtractPaletteSwatches([]byte("null")); got != nil {
		t.Errorf("null input → want nil, got %v", got)
	}
	if got := ExtractPaletteSwatches([]byte("{}")); got != nil {
		t.Errorf("empty object → want nil, got %v", got)
	}
}

func TestExtractPaletteSwatches_MalformedJSON(t *testing.T) {
	if got := ExtractPaletteSwatches([]byte(`{not-json`)); got != nil {
		t.Errorf("malformed JSON should return nil, got %v", got)
	}
}

func TestExtractPaletteSwatches_ConventionalSlotsLead(t *testing.T) {
	// Five canonical slots present + one extra "other" key — the
	// canonical ordering must surface FIRST regardless of map
	// iteration order, then the extra picks up the remaining slot
	// (capped at 4 total).
	raw := []byte(`{
		"accent":  "#aa0000",
		"primary": "#ff0000",
		"fg":      "#ffffff",
		"bg":      "#000000",
		"muted":   "#777777",
		"extra":   "#abcdef"
	}`)
	got := ExtractPaletteSwatches(raw)
	want := []string{"#ff0000", "#aa0000", "#777777", "#000000"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v (primary→accent→muted→bg, then capped)", got, want)
	}
}

func TestExtractPaletteSwatches_CapsAtFour(t *testing.T) {
	raw := []byte(`{
		"primary": "#111111",
		"accent":  "#222222",
		"muted":   "#333333",
		"bg":      "#444444",
		"fg":      "#555555",
		"extra":   "#666666"
	}`)
	got := ExtractPaletteSwatches(raw)
	if len(got) != 4 {
		t.Errorf("expected exactly 4 swatches, got %d: %v", len(got), got)
	}
}

func TestExtractPaletteSwatches_DedupCaseInsensitive(t *testing.T) {
	// Same hex in different cases should appear once.
	raw := []byte(`{
		"primary": "#FF0000",
		"accent":  "#ff0000",
		"muted":   "#abcdef"
	}`)
	got := ExtractPaletteSwatches(raw)
	if len(got) != 2 {
		t.Errorf("expected 2 unique swatches, got %d: %v", len(got), got)
	}
	// First occurrence (in canonical order) wins — primary is first.
	if got[0] != "#FF0000" {
		t.Errorf("first swatch should preserve original case from primary slot, got %q", got[0])
	}
}

func TestExtractPaletteSwatches_RejectsNonHexValues(t *testing.T) {
	// Values that look like color names, RGB(), or wrong-length
	// hex must be excluded.
	raw := []byte(`{
		"primary": "red",
		"accent":  "rgb(0,0,0)",
		"muted":   "#ff",
		"bg":      "#ggg",
		"fg":      "#123"
	}`)
	got := ExtractPaletteSwatches(raw)
	// Only #123 (3-digit hex shorthand) passes looksLikeHexColor.
	if !reflect.DeepEqual(got, []string{"#123"}) {
		t.Errorf("expected only [#123]; got %v", got)
	}
}

func TestExtractPaletteSwatches_WalksDeepStructures(t *testing.T) {
	// M4 protocol may nest hex values inside arrays / objects.
	// walkForHexes must recurse and find them.
	raw := []byte(`{
		"palette": {
			"main": ["#aaaaaa", "#bbbbbb"],
			"shades": {
				"light": "#cccccc",
				"dark":  "#dddddd"
			}
		}
	}`)
	got := ExtractPaletteSwatches(raw)
	// Order: `main` array (preserves array order) → `shades` keys
	// alphabetically (dark < light) → so values are
	// #aaaaaa, #bbbbbb, then #dddddd (dark), then #cccccc (light).
	want := []string{"#aaaaaa", "#bbbbbb", "#dddddd", "#cccccc"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v (deep walk preserves array order, alpha-sorts map keys)", got, want)
	}
}

func TestExtractPaletteSwatches_AlphabeticalNonCanonicalKeys(t *testing.T) {
	// When NO canonical slot is present, non-canonical keys
	// surface in alphabetical order — prompt-cache stability
	// requires this is deterministic, NOT map iteration order.
	raw := []byte(`{
		"zulu":  "#111111",
		"alpha": "#222222",
		"mike":  "#333333",
		"bravo": "#444444"
	}`)
	got := ExtractPaletteSwatches(raw)
	// alphabetical: alpha, bravo, mike, zulu
	want := []string{"#222222", "#444444", "#333333", "#111111"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v (alphabetical key order)", got, want)
	}
}

func TestExtractPaletteSwatches_ReturnsEmptyWhenNoHex(t *testing.T) {
	raw := []byte(`{"name": "Acme", "tone": "professional"}`)
	got := ExtractPaletteSwatches(raw)
	if len(got) != 0 {
		t.Errorf("no hex values present → expect empty slice; got %v", got)
	}
}

// ---------------------------------------------------------------------
// looksLikeHexColor — pin the validator independently so micro-edits
// (e.g. accepting 8-digit RGBA) don't slip through ExtractPaletteSwatches
// tests by coincidence.
// ---------------------------------------------------------------------

func TestLooksLikeHexColor(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		// Accepted shapes
		{"#000", true},
		{"#fff", true},
		{"#123", true},
		{"#abc", true},
		{"#ABC", true},
		{"#000000", true},
		{"#FFFFFF", true},
		{"#1a2B3c", true},

		// Wrong-length
		{"#", false},
		{"#1", false},
		{"#12", false},
		{"#1234", false},
		{"#12345", false},
		{"#1234567", false},  // 8-digit RGBA NOT supported

		// Bad first byte
		{"abc", false},
		{"000000", false},
		{"%fff", false},

		// Non-hex digits inside
		{"#ggg", false},
		{"#abcxyz", false},
		{"#zzz", false},

		// Empty
		{"", false},
	}
	for _, tc := range cases {
		got := looksLikeHexColor(tc.in)
		if got != tc.want {
			t.Errorf("looksLikeHexColor(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------
// sortStringsAsc — tiny insertion sort. Pin both correctness and
// stability (the walker uses it to keep prompt-cache hits stable).
// ---------------------------------------------------------------------

func TestSortStringsAsc(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
	}{
		{[]string{}, []string{}},
		{[]string{"a"}, []string{"a"}},
		{[]string{"b", "a"}, []string{"a", "b"}},
		{[]string{"c", "a", "b"}, []string{"a", "b", "c"}},
		{[]string{"foo", "bar", "baz"}, []string{"bar", "baz", "foo"}},
		// Already sorted
		{[]string{"a", "b", "c"}, []string{"a", "b", "c"}},
	}
	for _, tc := range cases {
		got := append([]string{}, tc.in...)
		sortStringsAsc(got)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("sortStringsAsc(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
