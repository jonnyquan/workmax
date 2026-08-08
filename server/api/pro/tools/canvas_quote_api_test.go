package tools

// Pure-layer tests for the quote-param coercion helpers that
// canvas_quote_api.go shares with generator_api.go. They pin the
// behaviours the handlers rely on:
//
//   - int coercion handles every JSON-decoded numeric + "5s"-style
//     duration strings (legacy composer dialect)
//   - string coercion trims but does not lowercase
//   - presence predicate treats empty and whitespace-only strings as
//     absent (so "hasStartFrame" toggles don't false-trigger from a
//     carried-over blank value)

import "testing"

func TestIntFromQuoteParams_Float64(t *testing.T) {
	got := intFromQuoteParams(map[string]interface{}{"n": float64(7)}, "n")
	if got != 7 {
		t.Errorf("float64(7) -> %d, want 7", got)
	}
}

func TestIntFromQuoteParams_Int64(t *testing.T) {
	got := intFromQuoteParams(map[string]interface{}{"n": int64(12)}, "n")
	if got != 12 {
		t.Errorf("int64(12) -> %d, want 12", got)
	}
}

func TestIntFromQuoteParams_StringWithSecondsSuffix(t *testing.T) {
	// Composer emits "duration": "10s" for legacy Kling presets.
	got := intFromQuoteParams(map[string]interface{}{"duration": "10s"}, "duration")
	if got != 10 {
		t.Errorf("\"10s\" -> %d, want 10", got)
	}
}

func TestIntFromQuoteParams_StringPlain(t *testing.T) {
	got := intFromQuoteParams(map[string]interface{}{"duration": "  5 "}, "duration")
	if got != 5 {
		t.Errorf("\"  5 \" -> %d, want 5", got)
	}
}

func TestIntFromQuoteParams_UnparseableReturnsZero(t *testing.T) {
	got := intFromQuoteParams(map[string]interface{}{"n": "abc"}, "n")
	if got != 0 {
		t.Errorf("\"abc\" -> %d, want 0 (fail-closed)", got)
	}
}

func TestIntFromQuoteParams_MissingKeyReturnsZero(t *testing.T) {
	got := intFromQuoteParams(map[string]interface{}{}, "n")
	if got != 0 {
		t.Errorf("missing key -> %d, want 0", got)
	}
}

func TestStringFromQuoteParams_TrimsWhitespace(t *testing.T) {
	got := stringFromQuoteParams(map[string]interface{}{"resolution": "  1080p  "}, "resolution")
	if got != "1080p" {
		t.Errorf("trim = %q, want %q", got, "1080p")
	}
}

func TestStringFromQuoteParams_PreservesCase(t *testing.T) {
	got := stringFromQuoteParams(map[string]interface{}{"aspectRatio": "16:9"}, "aspectRatio")
	if got != "16:9" {
		t.Errorf("case preservation = %q, want %q", got, "16:9")
	}
}

func TestStringFromQuoteParams_NonStringReturnsEmpty(t *testing.T) {
	got := stringFromQuoteParams(map[string]interface{}{"x": 42}, "x")
	if got != "" {
		t.Errorf("non-string -> %q, want empty", got)
	}
}

func TestQuoteParamPresent_EmptyStringIsAbsent(t *testing.T) {
	if quoteParamPresent(map[string]interface{}{"startFrameUrl": ""}, "startFrameUrl") {
		t.Errorf("empty string should be treated as absent")
	}
}

func TestQuoteParamPresent_WhitespaceStringIsAbsent(t *testing.T) {
	if quoteParamPresent(map[string]interface{}{"startFrameUrl": "   "}, "startFrameUrl") {
		t.Errorf("whitespace string should be treated as absent")
	}
}

func TestQuoteParamPresent_NonEmptyStringIsPresent(t *testing.T) {
	if !quoteParamPresent(map[string]interface{}{"startFrameUrl": "https://x"}, "startFrameUrl") {
		t.Errorf("non-empty url should be present")
	}
}

func TestQuoteParamPresent_NonStringIsPresent(t *testing.T) {
	// e.g. a number is carried — treat as present so the video credit
	// path can still surface "you sent garbage" rather than silently
	// dropping the flag.
	if !quoteParamPresent(map[string]interface{}{"startFrameUrl": 1}, "startFrameUrl") {
		t.Errorf("non-string value should be present")
	}
}

func TestQuoteParamPresent_NilIsAbsent(t *testing.T) {
	if quoteParamPresent(map[string]interface{}{"startFrameUrl": nil}, "startFrameUrl") {
		t.Errorf("nil value should be treated as absent")
	}
}

// intFromQuoteParams accepts six numeric shapes plus strings, but the
// existing suite only pins float64 + int64. Backfill the other three
// numeric arms so a future refactor that collapsed the switch (e.g.
// deleted the bare `int` case assuming json.Unmarshal only emits
// float64) wouldn't silently start returning 0 for Go-constructed
// quote payloads — which can reach these helpers via the internal
// batch quote path that builds params from typed fields, not JSON.

func TestIntFromQuoteParams_BareInt(t *testing.T) {
	got := intFromQuoteParams(map[string]interface{}{"n": 7}, "n")
	if got != 7 {
		t.Errorf("int(7) -> %d, want 7", got)
	}
}

func TestIntFromQuoteParams_Int32(t *testing.T) {
	got := intFromQuoteParams(map[string]interface{}{"n": int32(13)}, "n")
	if got != 13 {
		t.Errorf("int32(13) -> %d, want 13", got)
	}
}

func TestIntFromQuoteParams_Float32(t *testing.T) {
	// Truncation toward zero matches int(float64) behaviour — pin it
	// so a future refactor that switched to math.Round doesn't silently
	// change credit totals by ±1 unit.
	got := intFromQuoteParams(map[string]interface{}{"n": float32(4.7)}, "n")
	if got != 4 {
		t.Errorf("float32(4.7) -> %d, want 4 (truncate toward zero)", got)
	}
}

func TestIntFromQuoteParams_UnsupportedTypeReturnsZero(t *testing.T) {
	// bool is not in the allow-list of numeric shapes; the switch
	// falls through and the function returns 0. Pin the fail-closed
	// semantics so a "bonus" coercion added later (e.g. true->1) would
	// surface here rather than leak into downstream quote math.
	got := intFromQuoteParams(map[string]interface{}{"n": true}, "n")
	if got != 0 {
		t.Errorf("bool -> %d, want 0 (unsupported)", got)
	}
}

func TestStringFromQuoteParams_MissingKeyReturnsEmpty(t *testing.T) {
	// Parity with the missing-key branch of intFromQuoteParams — both
	// helpers must fail closed so a handler that forgot to stamp a
	// field doesn't get a zero-value back and proceed as if the caller
	// had sent one.
	if got := stringFromQuoteParams(map[string]interface{}{}, "resolution"); got != "" {
		t.Errorf("missing key -> %q, want empty", got)
	}
}

// boolFromQuoteParams powers the new mode=upscale branch (enhanceFace
// flag). Older clients sometimes stringify the field as "true" /
// "false", so the helper must accept both literal-bool and
// string-bool. Anything else collapses to false — failing closed
// keeps a malformed flag from silently flipping the upscale price
// from 3 to 5.

func TestBoolFromQuoteParams_LiteralTrue(t *testing.T) {
	if !boolFromQuoteParams(map[string]interface{}{"enhanceFace": true}, "enhanceFace") {
		t.Errorf("bool literal true should be true")
	}
}

func TestBoolFromQuoteParams_LiteralFalse(t *testing.T) {
	if boolFromQuoteParams(map[string]interface{}{"enhanceFace": false}, "enhanceFace") {
		t.Errorf("bool literal false should be false")
	}
}

func TestBoolFromQuoteParams_StringTrue(t *testing.T) {
	// Composer used to JSON-serialise the toggle as a string. Until
	// every legacy client has been retired, the server-side helper
	// must accept the string form.
	if !boolFromQuoteParams(map[string]interface{}{"enhanceFace": "true"}, "enhanceFace") {
		t.Errorf("string \"true\" should coerce to true")
	}
}

func TestBoolFromQuoteParams_StringTrueWithWhitespace(t *testing.T) {
	if !boolFromQuoteParams(map[string]interface{}{"enhanceFace": "  TRUE  "}, "enhanceFace") {
		t.Errorf("string \"  TRUE  \" should coerce to true (trim + case-insensitive)")
	}
}

func TestBoolFromQuoteParams_StringFalse(t *testing.T) {
	if boolFromQuoteParams(map[string]interface{}{"enhanceFace": "false"}, "enhanceFace") {
		t.Errorf("string \"false\" should coerce to false")
	}
}

func TestBoolFromQuoteParams_MissingKey(t *testing.T) {
	if boolFromQuoteParams(map[string]interface{}{}, "enhanceFace") {
		t.Errorf("missing key should fail closed to false")
	}
}

func TestBoolFromQuoteParams_NilValue(t *testing.T) {
	if boolFromQuoteParams(map[string]interface{}{"enhanceFace": nil}, "enhanceFace") {
		t.Errorf("nil value should fail closed to false")
	}
}

func TestBoolFromQuoteParams_NonBoolNonStringFailsClosed(t *testing.T) {
	// A number / object / array shouldn't be coerced to true.
	// Failing closed means a malformed payload can never flip
	// the upscale price tier without an explicit bool/string-true.
	cases := []interface{}{1, "yes", "1", "ok", []interface{}{}, map[string]interface{}{}}
	for _, v := range cases {
		if boolFromQuoteParams(map[string]interface{}{"enhanceFace": v}, "enhanceFace") {
			t.Errorf("value %v should fail closed to false", v)
		}
	}
}
