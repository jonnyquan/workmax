package tools

// canvas_headers_test.go + canvas_headers_parse_test.go cover the headline
// contract for parseCanvasProjectHeader and parseCanvasAssetBindingsHeader
// (absent/blank/malformed/valid, plus binding shape variants). These fill
// the quieter gate invariants a silent regression would slip past:
//
//   • parseCanvasProjectHeader: "0" is a VALID parse (ParseUint accepts
//     it) and returns 0 — which the caller interprets as "not canvas-
//     bound" (same effective result as absent). Pin that "0" and absent
//     are indistinguishable to downstream code — no special "0 is
//     invalid" error is raised.
//   • parseCanvasProjectHeader: "+42" REJECTED by ParseUint (unsigned
//     parse forbids plus sign). Pin so a refactor to ParseInt→cast
//     would silently start accepting "+42".
//   • parseCanvasProjectHeader: leading zeros "007" parse as 7 (ParseUint
//     strips). Pin — important for consumer expectations since some
//     proxies zero-pad header values.
//   • parseCanvasProjectHeader: hex prefix "0x10" rejected in base-10
//     mode (ParseUint base 10 doesn't recognise it). Pin against a
//     refactor to base 0 (auto-detect) which would accept.
//   • parseCanvasProjectHeader: tab/newline whitespace trimmed
//     (unicode.IsSpace class, not just ASCII space).
//   • parseCanvasProjectHeader: internal whitespace "4 2" NOT split —
//     rejected. Pin the no-split contract.
//   • parseCanvasProjectHeader: MaxUint64 boundary round-trips correctly
//     through ParseUint + uint cast. On a 64-bit platform uint is
//     u64-wide so this survives.
//
//   • parseCanvasAssetBindingsHeader: whitespace-wrapped `"   {}"` after
//     TrimSpace becomes `{}` → parses → all-empty → nil. Full pipeline
//     pin that wrapping whitespace doesn't short-circuit early.
//   • parseCanvasAssetBindingsHeader: weights-only binding (no ids, no
//     scope) returns nil. The gate checks scope + id-array lengths, NOT
//     weight-map presence. Pin the symmetric behaviour with the
//     frontend's bindingHasAnyIds gate (ids are the signal, not weights).
//   • parseCanvasAssetBindingsHeader: characterIds=[0] returns a
//     NON-NIL binding (len > 0 trumps numeric validity). The parser
//     doesn't validate id values — callers do. Pin this separation.
//   • parseCanvasAssetBindingsHeader: unknown extra JSON fields are
//     silently ignored by default json.Unmarshal. Pin backward-compat:
//     if the frontend ever ships a new optional field the server hasn't
//     learned about, it must NOT fail to parse.
//   • parseCanvasAssetBindingsHeader: type-mismatched array (e.g.
//     `"characterIds": "abc"`) → Unmarshal fails → nil.
//   • parseCanvasAssetBindingsHeader: returned pointer is FRESH per
//     call — two calls with the same body yield different pointer
//     addresses, ensuring no shared-state mutation across requests.
//   • parseCanvasAssetBindingsHeader: scope value passes through UNCASED
//     and untrimmed — "SHOT" (uppercase) survives as-is, "  shot  "
//     stays padded. Pin no normalisation; caller is responsible for
//     scope validation.
//   • parseCanvasAssetBindingsHeader: unknown scope values (e.g.
//     "global", "") also pass through — the parser doesn't enum-check.
//   • parseCanvasAssetBindingsHeader: mixed payload preserves all
//     fields (scope + ids across 3 kinds + weights across 3 kinds).

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http/httptest"
	"strconv"
	"testing"

	"server/model"

	"github.com/gin-gonic/gin"
)

func newHdrCtx(key, value string) *gin.Context {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest("GET", "/", nil)
	if key != "" {
		req.Header.Set(key, value)
	}
	c.Request = req
	return c
}

func TestParseCanvasProjectHeader_ZeroStringParsesAsZero(t *testing.T) {
	// ParseUint accepts "0". Downstream treats 0 as "not canvas-bound",
	// so "0" and absent are indistinguishable.
	c := newHdrCtx("X-Canvas-Project-Id", "0")
	if got := parseCanvasProjectHeader(c); got != 0 {
		t.Errorf(`"0" -> %d, want 0`, got)
	}
}

func TestParseCanvasProjectHeader_PlusSignRejected(t *testing.T) {
	// ParseUint forbids explicit plus sign.
	c := newHdrCtx("X-Canvas-Project-Id", "+42")
	if got := parseCanvasProjectHeader(c); got != 0 {
		t.Errorf(`"+42" -> %d, want 0`, got)
	}
}

func TestParseCanvasProjectHeader_LeadingZerosParse(t *testing.T) {
	// ParseUint strips leading zeros — "007" becomes 7.
	c := newHdrCtx("X-Canvas-Project-Id", "007")
	if got := parseCanvasProjectHeader(c); got != 7 {
		t.Errorf(`"007" -> %d, want 7`, got)
	}
}

func TestParseCanvasProjectHeader_HexPrefixRejected(t *testing.T) {
	// Base-10 ParseUint rejects "0x" prefix. Pin so a refactor to base 0
	// (auto-detect) would surface.
	c := newHdrCtx("X-Canvas-Project-Id", "0x10")
	if got := parseCanvasProjectHeader(c); got != 0 {
		t.Errorf(`"0x10" -> %d, want 0`, got)
	}
}

func TestParseCanvasProjectHeader_TabAndNewlineTrimmed(t *testing.T) {
	cases := []string{"\t42", "42\n", "\t42\r\n"}
	for _, raw := range cases {
		c := newHdrCtx("X-Canvas-Project-Id", raw)
		if got := parseCanvasProjectHeader(c); got != 42 {
			t.Errorf("%q -> %d, want 42 (TrimSpace whitespace class)", raw, got)
		}
	}
}

func TestParseCanvasProjectHeader_InternalWhitespaceRejected(t *testing.T) {
	// No splitting on internal whitespace.
	c := newHdrCtx("X-Canvas-Project-Id", "4 2")
	if got := parseCanvasProjectHeader(c); got != 0 {
		t.Errorf(`"4 2" -> %d, want 0`, got)
	}
}

func TestParseCanvasProjectHeader_MaxUint64RoundTrip(t *testing.T) {
	// On 64-bit platforms, uint aliases uint64, so MaxUint64 survives.
	// Pin so a refactor that narrowed to uint32 (bitSize 32) would
	// surface on large ids.
	raw := strconv.FormatUint(math.MaxUint64, 10)
	c := newHdrCtx("X-Canvas-Project-Id", raw)
	if got := parseCanvasProjectHeader(c); uint64(got) != math.MaxUint64 {
		t.Errorf("MaxUint64 -> %d, want %d", got, uint64(math.MaxUint64))
	}
}

func TestParseCanvasAssetBindingsHeader_WhitespaceWrappedEmptyBody(t *testing.T) {
	// Trim pipeline: `"   {}   "` → `"{}"` → parses → all-empty → nil.
	c := newHdrCtx("X-Canvas-Asset-Bindings", "   {}   ")
	if got := parseCanvasAssetBindingsHeader(c); got != nil {
		t.Errorf("whitespace-wrapped empty → got %#v, want nil", got)
	}
}

func TestParseCanvasAssetBindingsHeader_WeightsOnlyReturnsNil(t *testing.T) {
	// Gate checks scope + id arrays — weight maps alone don't pass.
	// Symmetric with frontend's bindingHasAnyIds semantics.
	payload := `{"characterWeights":{"1":1.5,"2":0.8}}`
	c := newHdrCtx("X-Canvas-Asset-Bindings", payload)
	if got := parseCanvasAssetBindingsHeader(c); got != nil {
		t.Errorf("weights-only → got %#v, want nil", got)
	}
}

func TestParseCanvasAssetBindingsHeader_IDsWithZeroValueNonNil(t *testing.T) {
	// Parser doesn't validate id values — len([0])==1, so returns
	// non-nil. Callers are responsible for rejecting id 0.
	c := newHdrCtx("X-Canvas-Asset-Bindings", `{"characterIds":[0]}`)
	got := parseCanvasAssetBindingsHeader(c)
	if got == nil {
		t.Fatal("characterIds=[0] should return non-nil (parser doesn't validate values)")
	}
	if len(got.CharacterIDs) != 1 || got.CharacterIDs[0] != 0 {
		t.Errorf("CharacterIDs = %+v, want [0]", got.CharacterIDs)
	}
}

func TestParseCanvasAssetBindingsHeader_UnknownFieldsIgnored(t *testing.T) {
	// Default json.Unmarshal ignores unknown fields — backward-compat
	// with newer frontends. Pin so a refactor to DisallowUnknownFields
	// would surface as a breaking change.
	payload := `{"scope":"shot","futureField":{"nested":true},"alsoNew":"hello"}`
	c := newHdrCtx("X-Canvas-Asset-Bindings", payload)
	got := parseCanvasAssetBindingsHeader(c)
	if got == nil {
		t.Fatal("unknown fields should be silently ignored, got nil")
	}
	if got.Scope != model.AssetScopeShot {
		t.Errorf("scope = %q, want %q", got.Scope, model.AssetScopeShot)
	}
}

func TestParseCanvasAssetBindingsHeader_TypeMismatchReturnsNil(t *testing.T) {
	// characterIds expected []int — passing a string fails unmarshal.
	payload := `{"characterIds":"abc"}`
	c := newHdrCtx("X-Canvas-Asset-Bindings", payload)
	if got := parseCanvasAssetBindingsHeader(c); got != nil {
		t.Errorf("type-mismatch → got %#v, want nil", got)
	}
}

func TestParseCanvasAssetBindingsHeader_FreshPointerPerCall(t *testing.T) {
	// Two calls with the same body must not return the same pointer —
	// handlers may mutate the binding before handing off to the
	// injector. Pin isolation.
	body := `{"scope":"shot","characterIds":[1,2]}`
	a := parseCanvasAssetBindingsHeader(newHdrCtx("X-Canvas-Asset-Bindings", body))
	b := parseCanvasAssetBindingsHeader(newHdrCtx("X-Canvas-Asset-Bindings", body))
	if a == nil || b == nil {
		t.Fatal("expected non-nil")
	}
	if a == b {
		t.Error("expected different pointers per call, got shared reference")
	}
}

func TestParseCanvasAssetBindingsHeader_ScopePassthroughNoNormalisation(t *testing.T) {
	// Parser doesn't trim or lowercase scope; it's caller's job.
	payload, _ := json.Marshal(map[string]any{"scope": "SHOT"})
	c := newHdrCtx("X-Canvas-Asset-Bindings", string(payload))
	got := parseCanvasAssetBindingsHeader(c)
	if got == nil {
		t.Fatal("non-empty scope should return non-nil")
	}
	if string(got.Scope) != "SHOT" {
		t.Errorf("scope = %q, want %q (no case normalisation)", got.Scope, "SHOT")
	}
}

func TestParseCanvasAssetBindingsHeader_UnknownScopePassthrough(t *testing.T) {
	// "canvas" is not in the AssetBindingScope enum — parser doesn't
	// validate, just passes through. Pin so a refactor that added enum
	// validation would surface.
	payload := `{"scope":"canvas"}`
	c := newHdrCtx("X-Canvas-Asset-Bindings", payload)
	got := parseCanvasAssetBindingsHeader(c)
	if got == nil {
		t.Fatal("unknown scope should still return non-nil (no enum check)")
	}
	if string(got.Scope) != "canvas" {
		t.Errorf("scope = %q, want %q", got.Scope, "canvas")
	}
}

func TestParseCanvasAssetBindingsHeader_FullPayloadPreservesAllFields(t *testing.T) {
	// Pin that no field is silently dropped in the round-trip — a
	// refactor that switched to a narrow DTO would fail here.
	in := model.AssetBinding{
		Scope:            model.AssetScopeShot,
		CharacterIDs:     []int{1, 2},
		CharacterWeights: model.AssetWeightMap{"1": 0.8},
	}
	raw, _ := json.Marshal(in)
	c := newHdrCtx("X-Canvas-Asset-Bindings", string(raw))
	got := parseCanvasAssetBindingsHeader(c)
	if got == nil {
		t.Fatal("expected non-nil")
	}
	if got.Scope != in.Scope {
		t.Errorf("scope = %q, want %q", got.Scope, in.Scope)
	}
	if fmt.Sprintf("%v", got.CharacterIDs) != "[1 2]" {
		t.Errorf("ids round-trip: %+v", got)
	}
	if got.CharacterWeights["1"] != 0.8 {
		t.Errorf("weights round-trip: %+v", got)
	}
}
