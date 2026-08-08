package utils

import (
	"reflect"
	"strings"
	"testing"
)

// char.go ships ~20 pure helpers used across the request path: JSON
// round-trips, lenient numeric parsing, separator-preserving splits,
// rune-safe truncation, image-URL extraction, unicode-escape decoding,
// prefix-tree sorting, secret masking. They're all zero-dep pure funcs
// and zero of them had tests — a silent edit to SplitItem's "attach sep
// to all-but-last" contract would break prompt parsing, a flipped
// ParseBool default would re-route an auth check, ToSecret dropping
// its length-4 floor would leak the first 4 chars of a 3-char token.

func TestSplitItem_AttachesSeparatorToAllButLastSegment(t *testing.T) {
	// Documented contract: unlike strings.Split, the returned slice
	// preserves the separator on every segment EXCEPT the last. This
	// lets callers round-trip back to the original via strings.Join("").
	got := SplitItem("a,b,c", ",")
	want := []string{"a,", "b,", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SplitItem = %q, want %q", got, want)
	}
	// Rejoining with empty string reproduces the original — this is the
	// whole point of the quirky contract.
	if strings.Join(got, "") != "a,b,c" {
		t.Errorf("round-trip failed")
	}
}

func TestSplitItem_EmptyInputReturnsEmptySlice(t *testing.T) {
	// Must return a non-nil empty slice — callers range over it without
	// guarding, and a nil would be tolerated but an unexpected panic
	// elsewhere would surface. Keep the contract explicit.
	got := SplitItem("", ",")
	if got == nil || len(got) != 0 {
		t.Errorf("SplitItem(\"\") = %#v, want empty slice", got)
	}
}

func TestSplitItem_TrailingSeparatorProducesEmptyTail(t *testing.T) {
	// "a," → strings.Split gives ["a", ""] → last element (empty) is
	// the "tail"; earlier element gets the separator attached.
	got := SplitItem("a,", ",")
	want := []string{"a,", ""}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSplitItems_MultipleSeparatorsAppliedInOrder(t *testing.T) {
	// Split first by ',' then by ' ' — "a,b c" → ["a,", "b c"] → ["a,",
	// "b ", "c"]. The order matters: a later separator can further split
	// segments that were already produced by an earlier pass.
	got := SplitItems("a,b c", []string{",", " "})
	want := []string{"a,", "b ", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSplitItems_EmptySeparatorListReturnsEmpty(t *testing.T) {
	// Explicit short-circuit: zero separators means zero output, NOT
	// [data]. This prevents a caller passing nil seps from silently
	// getting back an un-split blob that looks correct in happy paths.
	got := SplitItems("whatever", nil)
	if got == nil || len(got) != 0 {
		t.Errorf("SplitItems with nil seps = %#v, want empty slice", got)
	}
}

func TestSplitLangItems_SplitsOnCJKCommaAndWhitespace(t *testing.T) {
	// SplitLangItems is what parses multi-language user input; it must
	// recognise `,`, CJK `，`, space, and newline as equivalent.
	got := SplitLangItems("en,zh，ja fr\nde")
	joined := strings.Join(got, "|")
	// We don't assert exact strings because SplitItem attaches separators;
	// the important contract is that all 5 language codes surface.
	for _, want := range []string{"en", "zh", "ja", "fr", "de"} {
		if !strings.Contains(joined, want) {
			t.Errorf("SplitLangItems missing %q in %q", want, joined)
		}
	}
}

func TestExtract_RuneAwareTruncation(t *testing.T) {
	// Extract uses []rune, not byte length — so multi-byte characters
	// count as one unit each. A byte-based slice here would cut a CJK
	// rune in half and produce invalid UTF-8.
	if got := Extract("你好世界", 2); got != "你好" {
		t.Errorf("Extract rune-based = %q, want %q", got, "你好")
	}
	// When under the limit, return the original untouched (no flow suffix).
	if got := Extract("hi", 5, "..."); got != "hi" {
		t.Errorf("short input with flow = %q, want %q", got, "hi")
	}
	// Over the limit → truncate + flow suffix.
	if got := Extract("hello", 3, "..."); got != "hel..." {
		t.Errorf("over-limit with flow = %q, want %q", got, "hel...")
	}
	// Flow defaults to empty string when not passed.
	if got := Extract("hello", 3); got != "hel" {
		t.Errorf("default flow = %q, want %q", got, "hel")
	}
}

func TestExtractExternalImages_OnlyRecognisedExtensions(t *testing.T) {
	// The allowlist of extensions matches OpenAI vision — png/jpg/jpeg/
	// gif/webp/heif/heic/bmp/svg/ico. A .mp4 in the same regex would
	// accidentally route video URLs through the image pipeline.
	got := ExtractExternalImages("pic https://example.com/a.png and https://example.com/vid.mp4 plus https://example.com/b.jpg?v=1")
	if len(got) != 2 {
		t.Fatalf("got %d images, want 2: %q", len(got), got)
	}
	if got[0] != "https://example.com/a.png" {
		t.Errorf("first = %q", got[0])
	}
	// Query strings are preserved on matched URLs.
	if got[1] != "https://example.com/b.jpg?v=1" {
		t.Errorf("second (with query) = %q", got[1])
	}
}

func TestExtractImagesFromMarkdown_CapturesUrlNotAltText(t *testing.T) {
	// The capture group picks the URL, NOT the `![alt]` portion —
	// pinning this defends against a regex tweak that accidentally
	// swaps the groups and leaks alt text into URL slots.
	got := ExtractImagesFromMarkdown("see ![cat](https://a.com/cat.png) the end")
	want := []string{"https://a.com/cat.png"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExtractImagesFromMarkdown_GreedyCollapseIsAKnownFootgun(t *testing.T) {
	// The regex uses greedy `.*` between `![` and `](` — so when two
	// images appear in one string, greedy backtracking matches from the
	// FIRST `![` through the LAST `](url)`, collapsing the middle image
	// into the alt-text slot and returning only the last URL. This is
	// a real bug and pinning it as-is makes any future fix to `.*?` a
	// visibly failing assertion (at which point the test body flips).
	got := ExtractImagesFromMarkdown("![a](https://x.com/1.png) and ![b](https://x.com/2.png)")
	if len(got) != 1 || got[0] != "https://x.com/2.png" {
		t.Errorf("greedy footgun drifted: got %q", got)
	}
}

func TestContainUnicode_AndDecodeUnicode_RoundTrip(t *testing.T) {
	// Input uses a LITERAL backslash + u + 4 hex digits, which is how
	// user pastes from JSON-encoded strings sometimes arrive. The "\\u"
	// in the double-quoted literal compiles to two chars (backslash, u)
	// — that's what the regex `\\u[0-9a-fA-F]{4}` is built to match.
	raw := "hi\\u2019s"
	if !ContainUnicode(raw) {
		t.Fatalf("ContainUnicode(%q) = false, want true", raw)
	}
	// ’ is U+2019 right-single-quote → ’.
	if got := DecodeUnicode(raw); got != "hi’s" {
		t.Errorf("DecodeUnicode = %q, want %q", got, "hi’s")
	}
	// Already-decoded strings have no \u sequences → no change.
	decoded := "hi’s"
	if ContainUnicode(decoded) {
		t.Errorf("decoded string should not match")
	}
	if got := DecodeUnicode(decoded); got != decoded {
		t.Errorf("DecodeUnicode of decoded string = %q, want no change", got)
	}
}

func TestEscapeChar_ConvertsDoubleBackslashEscapes(t *testing.T) {
	// `hi\nworld` (6 chars before world: h, i, backslash, n, w...) must
	// become `hi` + actual newline + `world`. This normalises input from
	// LLMs that over-escape control characters.
	got := EscapeChar(`hi\nworld`)
	if got != "hi\nworld" {
		t.Errorf("got %q, want %q", got, "hi\nworld")
	}
	// All six named escapes covered: n r t v f b.
	if EscapeChar(`\t\r`) != "\t\r" {
		t.Errorf("tab/cr failed")
	}
}

func TestSortString_PrefixTreeOrdering(t *testing.T) {
	// SortString expects its input already sorted alphabetically — it
	// then performs prefix grouping: every entry whose common prefix
	// matches the current root is recursed into. Input that's NOT sorted
	// first gives undefined output (the current implementation preserves
	// input order for mismatches), so pin the documented happy path.
	input := []string{"a", "ab", "ac", "b", "bc", "c"}
	got := SortString(input)
	want := []string{"a", "ab", "ac", "b", "bc", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}

	// A single-element input (or empty) is returned as-is without
	// recursion — the length-<=1 short circuit.
	if got := SortString([]string{"solo"}); !reflect.DeepEqual(got, []string{"solo"}) {
		t.Errorf("single-element = %q", got)
	}
}

func TestSafeSplit_PadsOrJoinsToFixedLength(t *testing.T) {
	// Under-length input pads with empty strings so callers can destructure
	// [first, second, third] without checking length.
	if got := SafeSplit("a", ",", 3); !reflect.DeepEqual(got, []string{"a", "", ""}) {
		t.Errorf("under-length pad: %q", got)
	}
	// Exact length → untouched.
	if got := SafeSplit("a,b,c", ",", 3); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("exact length: %q", got)
	}
	// Over-length → the tail (everything past seglen) collapses into the
	// LAST slot with the separator preserved. Callers rely on this to
	// keep the overflow inspectable rather than dropped.
	if got := SafeSplit("abc,def,ghi", ",", 2); !reflect.DeepEqual(got, []string{"abc", "def,ghi"}) {
		t.Errorf("over-length collapse: %q", got)
	}
	// Empty input always produces seglen empty strings — never nil.
	if got := SafeSplit("", ",", 3); !reflect.DeepEqual(got, []string{"", "", ""}) {
		t.Errorf("empty input: %q", got)
	}
}

func TestToSecret_MinimumFourStarForShortInput(t *testing.T) {
	// Strings under 4 runes are fully masked to "****" — if we revealed
	// the first 4 of a 3-char token, we'd be leaking 100% of a short
	// secret. The length-floor is load-bearing.
	for _, short := range []string{"", "a", "ab", "abc"} {
		if got := ToSecret(short); got != "****" {
			t.Errorf("ToSecret(%q) = %q, want %q", short, got, "****")
		}
	}
}

func TestToSecret_RevealsFirstFourThenStars(t *testing.T) {
	// For 9-char input: "axVb" visible, 5 stars after.
	if got := ToSecret("axVbeixvN"); got != "axVb*****" {
		t.Errorf("got %q, want %q", got, "axVb*****")
	}
	// Rune-count, not byte-count: "报告报告报告" is 6 runes, so first 4
	// runes visible + 2 stars.
	got := ToSecret("报告报告报告")
	if got != "报告报告**" {
		t.Errorf("rune-counted got %q, want %q", got, "报告报告**")
	}
}

func TestParseInt_ParseInt64_ParseFloat32_ParseBool_FallbackOnError(t *testing.T) {
	// Intentionally lenient: all four parsers swallow the error and
	// return a zero-value default. This is documented via usage — every
	// caller treats them as "best effort". Pin the behaviour so nobody
	// silently changes a zero-value to a panic or a sentinel.
	if ParseInt("abc") != 0 {
		t.Errorf("ParseInt bad input")
	}
	if ParseInt("42") != 42 {
		t.Errorf("ParseInt good input")
	}
	if ParseInt64("xyz") != 0 {
		t.Errorf("ParseInt64 bad input")
	}
	if StringToInt64("1234567890123") != 1234567890123 {
		t.Errorf("StringToInt64 passthrough")
	}
	if ParseFloat32("nope") != 0 {
		t.Errorf("ParseFloat32 bad input")
	}
	// ParseBool on an empty string → strconv.ParseBool errors → false.
	// A silent flip to `true` would be catastrophic for "allow by
	// default" config reads.
	if ParseBool("") != false {
		t.Errorf("ParseBool empty")
	}
	if ParseBool("true") != true {
		t.Errorf("ParseBool true")
	}
	if ParseBool("TRUE") != true {
		t.Errorf("ParseBool TRUE (strconv.ParseBool accepts upper)")
	}
}

func TestMarshalUnmarshal_RoundTrip(t *testing.T) {
	type example struct {
		A int    `json:"a"`
		B string `json:"b"`
	}
	raw := Marshal(example{A: 7, B: "hi"})
	if raw != `{"a":7,"b":"hi"}` {
		t.Errorf("Marshal = %q", raw)
	}
	got, err := Unmarshal[example]([]byte(raw))
	if err != nil {
		t.Fatalf("Unmarshal err: %v", err)
	}
	if got.A != 7 || got.B != "hi" {
		t.Errorf("round-trip: %+v", got)
	}

	// Marshal of a non-serialisable value (channel) returns empty string,
	// NOT a panic or error — this is the "best effort" contract again.
	if Marshal(make(chan int)) != "" {
		t.Errorf("Marshal of channel should be empty string")
	}
}
