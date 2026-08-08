package tools

// canvas_quote_api_test.go covers the headline contract (int coercion
// across float64/int64 + "5s" suffix, string trim-preserves-case,
// presence gate treats empty/whitespace as absent). These fill the
// quieter gate invariants a silent regression would slip past:
//
//   • intFromQuoteParams TrimSuffix trims exactly ONE trailing "s".
//     "12ss" → TrimSuffix → "12s" → Atoi fails → 0. Pin so a refactor
//     to TrimRight(val, "s") (which would strip all trailing s's) would
//     silently start accepting "12sss" as 12.
//   • intFromQuoteParams "s" trim is case-sensitive — "12S" does NOT
//     get its suffix trimmed (uppercase S stays), Atoi fails on "12S",
//     returns 0. Pin so a refactor to strings.EqualFold-style trim
//     would silently accept uppercase-suffixed durations.
//   • intFromQuoteParams applies TrimSuffix BEFORE TrimSpace — so
//     "  10s  " (trailing space) does NOT get the "s" removed (because
//     the string doesn't end in "s"), then TrimSpace yields "10s",
//     which Atoi rejects → 0. This is arguably a bug, but pin current
//     behaviour so a refactor that reversed the order (which WOULD fix
//     the "  10s  " case) surfaces as a deliberate choice rather than
//     a silent semantic change.
//   • intFromQuoteParams float-as-string (e.g. "10.5") does NOT coerce
//     — Atoi rejects decimals, returns 0. Pin so a refactor that
//     swapped Atoi for ParseFloat+int-cast would silently start
//     accepting fractional durations (which the video quote pipeline
//     doesn't support).
//   • intFromQuoteParams with explicit nil value falls through the
//     type switch and returns 0. Pin so a refactor that added a nil
//     case (mapping to some default) would surface.
//   • intFromQuoteParams with uint types (uint, uint64) is NOT
//     supported — falls through to 0. Base suite only pins bool-as-
//     unsupported; pin uint variants too so an expansion of the allow-
//     list would be deliberate, not accidental.
//   • intFromQuoteParams negative string ("-5") parses correctly —
//     no "s" suffix to trim, Atoi accepts negative integers, returns
//     -5. Pin so the handler's subsequent `if duration <= 0` gate is
//     testing a value that actually reached it (rather than always
//     being 0 because of some upstream transform we don't see).
//   • intFromQuoteParams with just "s" as input → TrimSuffix returns
//     "" → TrimSpace no-op → Atoi("") fails → 0. Pin the degenerate
//     case so a refactor that short-circuited before TrimSuffix would
//     surface.
//   • stringFromQuoteParams preserves internal whitespace. "foo  bar"
//     (double space) stays as "foo  bar" after trim. Pin so a
//     normalisation refactor (regex /\s+/g → single space) would
//     surface — that refactor may be desirable but must be deliberate.
//   • stringFromQuoteParams on nil value returns "". The type-assert
//     to string fails on nil (nil is not a string), so the inner
//     branch is skipped and the outer default "" returns. Pin so a
//     refactor that stringified nil to "<nil>" would surface.
//   • stringFromQuoteParams whitespace-only string → empty string
//     (TrimSpace eats everything). Different from presence gate but
//     pin the specific contract.
//   • quoteParamPresent treats numeric 0 as PRESENT (not a string, so
//     returns true). This matters because "numberOfImages": 0 would
//     reach the handler as present-but-nonsensical, which the handler
//     then re-clamps to 1 via its own gate. Pin the predicate's
//     blind-to-numeric-zero behaviour.
//   • quoteParamPresent treats tab/newline-only strings as absent (the
//     whole whitespace class, not just space). Pin so a refactor that
//     narrowed trim to only ASCII-space would surface.
//   • quoteParamPresent treats slice/map as present (any non-nil
//     non-string value is present). Pin so a refactor that added a
//     "only strings and numbers are present" gate would surface.

import "testing"

func TestIntFromQuoteParams_DoubleSSuffixRejected(t *testing.T) {
	// TrimSuffix strips exactly one trailing "s" — "12ss" → "12s" →
	// Atoi fails. Pin so a refactor to TrimRight(val, "s") would
	// silently start accepting "12sss".
	got := intFromQuoteParams(map[string]interface{}{"duration": "12ss"}, "duration")
	if got != 0 {
		t.Errorf("\"12ss\" -> %d, want 0 (only one trailing s stripped)", got)
	}
}

func TestIntFromQuoteParams_UppercaseSSuffixRejected(t *testing.T) {
	// Case-sensitive suffix match. "12S" does NOT get the suffix
	// trimmed; Atoi("12S") fails; returns 0.
	got := intFromQuoteParams(map[string]interface{}{"duration": "12S"}, "duration")
	if got != 0 {
		t.Errorf("\"12S\" -> %d, want 0 (case-sensitive suffix)", got)
	}
}

func TestIntFromQuoteParams_TrailingSpaceAfterSuffixRejected(t *testing.T) {
	// TrimSuffix is called BEFORE TrimSpace. "  10s  " doesn't end in
	// "s" (ends in space), so TrimSuffix is a no-op; TrimSpace yields
	// "10s"; Atoi rejects "10s". Pin this ordering bug-or-feature so a
	// refactor reversing the two calls surfaces as deliberate.
	got := intFromQuoteParams(map[string]interface{}{"duration": "  10s  "}, "duration")
	if got != 0 {
		t.Errorf("\"  10s  \" -> %d, want 0 (TrimSuffix runs before TrimSpace)", got)
	}
}

func TestIntFromQuoteParams_FloatStringRejected(t *testing.T) {
	// Atoi does not accept decimals. "10.5" → 0.
	got := intFromQuoteParams(map[string]interface{}{"duration": "10.5"}, "duration")
	if got != 0 {
		t.Errorf("\"10.5\" -> %d, want 0 (Atoi rejects decimals)", got)
	}
}

func TestIntFromQuoteParams_FloatWithSSuffixRejected(t *testing.T) {
	// "10.5s" → TrimSuffix → "10.5" → Atoi rejects. Pin that fractional
	// durations can't sneak in via the legacy "s"-suffix path either.
	got := intFromQuoteParams(map[string]interface{}{"duration": "10.5s"}, "duration")
	if got != 0 {
		t.Errorf("\"10.5s\" -> %d, want 0 (fractional not allowed)", got)
	}
}

func TestIntFromQuoteParams_NilValueReturnsZero(t *testing.T) {
	// Explicit nil (params["n"] = nil) falls through the type switch
	// (no nil case), returns 0.
	got := intFromQuoteParams(map[string]interface{}{"n": nil}, "n")
	if got != 0 {
		t.Errorf("nil -> %d, want 0", got)
	}
}

func TestIntFromQuoteParams_UintUnsupported(t *testing.T) {
	// uint is not in the allow-list — returns 0. Pin so a later
	// expansion of the switch would be deliberate.
	got := intFromQuoteParams(map[string]interface{}{"n": uint(7)}, "n")
	if got != 0 {
		t.Errorf("uint(7) -> %d, want 0 (unsupported)", got)
	}
}

func TestIntFromQuoteParams_Uint64Unsupported(t *testing.T) {
	got := intFromQuoteParams(map[string]interface{}{"n": uint64(7)}, "n")
	if got != 0 {
		t.Errorf("uint64(7) -> %d, want 0 (unsupported)", got)
	}
}

func TestIntFromQuoteParams_NegativeStringParsesNegative(t *testing.T) {
	// "-5" has no "s" suffix, TrimSuffix is a no-op, TrimSpace is a
	// no-op, Atoi accepts "-5" and returns -5. The handler's own
	// `if duration <= 0` gate then re-clamps this, but at the helper
	// level, negative values survive.
	got := intFromQuoteParams(map[string]interface{}{"duration": "-5"}, "duration")
	if got != -5 {
		t.Errorf("\"-5\" -> %d, want -5 (helper preserves negative; handler clamps)", got)
	}
}

func TestIntFromQuoteParams_JustSSuffix(t *testing.T) {
	// "s" → TrimSuffix → "" → TrimSpace → "" → Atoi("") fails → 0.
	got := intFromQuoteParams(map[string]interface{}{"duration": "s"}, "duration")
	if got != 0 {
		t.Errorf("\"s\" -> %d, want 0 (empty after suffix strip)", got)
	}
}

func TestStringFromQuoteParams_PreservesInternalWhitespace(t *testing.T) {
	// Only TrimSpace (boundary) — internal spaces stay. Pin against a
	// /\s+/ regex collapse refactor.
	got := stringFromQuoteParams(map[string]interface{}{"prompt": "  foo  bar  "}, "prompt")
	if got != "foo  bar" {
		t.Errorf("internal whitespace = %q, want %q", got, "foo  bar")
	}
}

func TestStringFromQuoteParams_NilValueReturnsEmpty(t *testing.T) {
	// Type-assert to string fails on nil, so we fall through to the
	// default empty return.
	got := stringFromQuoteParams(map[string]interface{}{"resolution": nil}, "resolution")
	if got != "" {
		t.Errorf("nil -> %q, want empty", got)
	}
}

func TestStringFromQuoteParams_WhitespaceOnlyReturnsEmpty(t *testing.T) {
	// TrimSpace eats everything, returning empty. Different contract
	// from presence gate but pin it here too.
	got := stringFromQuoteParams(map[string]interface{}{"x": "   \t\n  "}, "x")
	if got != "" {
		t.Errorf("whitespace-only -> %q, want empty", got)
	}
}

func TestQuoteParamPresent_NumericZeroIsPresent(t *testing.T) {
	// 0 is not a string, so the non-string branch returns true. The
	// handler then clamps to 1. Pin that numeric 0 reaches the handler
	// as "present" — a refactor that added a "zero is absent" gate
	// would change semantics for `numberOfImages: 0` payloads.
	if !quoteParamPresent(map[string]interface{}{"numberOfImages": 0}, "numberOfImages") {
		t.Errorf("numeric 0 should be present (non-string branch)")
	}
}

func TestQuoteParamPresent_TabOnlyIsAbsent(t *testing.T) {
	if quoteParamPresent(map[string]interface{}{"startFrameUrl": "\t"}, "startFrameUrl") {
		t.Errorf("tab-only should be absent")
	}
}

func TestQuoteParamPresent_NewlineOnlyIsAbsent(t *testing.T) {
	if quoteParamPresent(map[string]interface{}{"startFrameUrl": "\n"}, "startFrameUrl") {
		t.Errorf("newline-only should be absent")
	}
}

func TestQuoteParamPresent_MixedWhitespaceIsAbsent(t *testing.T) {
	if quoteParamPresent(map[string]interface{}{"startFrameUrl": " \t\r\n "}, "startFrameUrl") {
		t.Errorf("mixed whitespace should be absent")
	}
}

func TestQuoteParamPresent_SliceIsPresent(t *testing.T) {
	// Non-nil, non-string → present.
	if !quoteParamPresent(map[string]interface{}{"tags": []string{"a"}}, "tags") {
		t.Errorf("slice should be present")
	}
}

func TestQuoteParamPresent_EmptySliceIsPresent(t *testing.T) {
	// Even an empty slice is present (non-nil, non-string). Pin so a
	// refactor that emptied-collection-is-absent gate would surface.
	if !quoteParamPresent(map[string]interface{}{"tags": []string{}}, "tags") {
		t.Errorf("empty slice should still be present (not nil)")
	}
}

func TestQuoteParamPresent_MapIsPresent(t *testing.T) {
	if !quoteParamPresent(map[string]interface{}{"opts": map[string]any{}}, "opts") {
		t.Errorf("map should be present")
	}
}

func TestQuoteParamPresent_BoolFalseIsPresent(t *testing.T) {
	// false is a non-string non-nil value, so the predicate returns
	// true. The handler layer interprets false as "not a start frame",
	// but at the predicate level it's still "present". Pin so a
	// refactor that treated falsy values as absent would surface.
	if !quoteParamPresent(map[string]interface{}{"hasStart": false}, "hasStart") {
		t.Errorf("bool(false) should be present (non-string branch)")
	}
}
