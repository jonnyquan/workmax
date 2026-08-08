package tools

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"server/model"

	"github.com/gin-gonic/gin"
)

// canvas_headers_parse_test.go covers the headline contract
// (parseCanvasProjectHeader: absent/blank/malformed/valid/whitespace-
// padded; parseCanvasAssetBindingsHeader: absent/blank/invalid-json/
// empty-struct → nil, scope-only + char+brand ids return non-nil;
// respondCanvasAIError: blank errorCode omits, present includes,
// status is duplicated in body). These fill the quieter gate
// invariants a silent regression would slip past:
//
//   • parseCanvasProjectHeader: "0" (valid uint) returns 0, which is
//     INDISTINGUISHABLE from absent/malformed. Intentional — a client
//     sending id=0 behaves as non-canvas-bound. Pin the aliasing so a
//     refactor that distinguished "explicit zero" from "missing" would
//     surface.
//   • parseCanvasProjectHeader: "+42" with a leading plus sign —
//     strconv.ParseUint base=10 REJECTS the `+` prefix (unlike
//     Atoi). Pin so a future refactor to strconv.Atoi wouldn't
//     silently start accepting signed-positive notation.
//   • parseCanvasProjectHeader: hex ("0x10"), underscore-separated
//     ("1_000"), scientific ("1e2") all reject — base=10 is strict.
//   • parseCanvasProjectHeader: "42abc" mixed-content rejects (no
//     partial-prefix parse). Pin so a refactor to Atoi(raw[:i]) style
//     pre-trim wouldn't slip in.
//   • parseCanvasProjectHeader: internal whitespace like "12 34" —
//     TrimSpace only trims edges, so the remaining space fails
//     ParseUint and returns 0. Pin.
//   • parseCanvasProjectHeader: HTTP header names are case-insensitive
//     via Go's http.Header canonical form — "x-canvas-project-id" and
//     "X-Canvas-Project-Id" resolve to the same slot. Pin so a refactor
//     that read from Request.Header with a raw map lookup instead of
//     c.GetHeader would surface.
//   • parseCanvasProjectHeader: max-uint64 boundary parses cleanly;
//     max-uint64 + 1 overflows to 0 (ParseUint returns RangeError).
//
//   • parseCanvasAssetBindingsHeader: whitespace-only Scope (" ") —
//     len > 0 so the `Scope == ""` guard is bypassed and a non-nil
//     binding is returned with the whitespace Scope intact. Pin the
//     honest observable — a refactor to TrimSpace on Scope would
//     surface.
//   • parseCanvasAssetBindingsHeader: empty arrays + no scope returns
//     nil — extension of base "empty struct" case. `[]int{}` is
//     distinguishable from nil in Go but both produce len == 0, so
//     the guard fires either way.
//   • parseCanvasAssetBindingsHeader: id containing 0 counts as
//     present (len > 0). A binding with CharacterIDs: [0] returns
//     non-nil. Pin so a refactor that filtered zero-id entries
//     before the len check wouldn't silently collapse to nil.
//   • parseCanvasAssetBindingsHeader: unknown top-level fields in
//     JSON are silently ignored (encoding/json default). A future
//     client that adds fields wouldn't fail parsing. Pin the
//     forward-compat observable.
//
//   • respondCanvasAIError: the stored errorCode is the RAW input
//     (not Trim-normalised). Input "  FOO  " with non-whitespace
//     content passes the TrimSpace check but lands in the payload
//     with whitespace intact. Pin so a refactor that stored the
//     trimmed version would surface as a deliberate trim-on-store.
//   • respondCanvasAIError: top-level envelope keys are exactly
//     {code, message, data} — no extras. Pin so a stray addition
//     (like "timestamp") would surface as a deliberate schema bump.
//   • respondCanvasAIError: payload.success is HARDCODED to false
//     (the caller cannot set it). Pin so a refactor that accepted
//     an override argument would surface.
//   • respondCanvasAIError: Content-Type is application/json set by
//     gin.Context.JSON — pin the response header so a refactor to
//     c.String or c.Data would surface.
//   • respondCanvasAIError: empty message "" still lands at
//     envelope.message and payload.message — the call has no
//     side-effect of substituting a default message.

func TestParseCanvasProjectHeader_ExplicitZeroIsIndistinguishableFromAbsent(t *testing.T) {
	c, _ := newGinCtxWithHeaders(map[string]string{"X-Canvas-Project-Id": "0"})
	if got := parseCanvasProjectHeader(c); got != 0 {
		t.Errorf("explicit 0 should return 0 (same as absent); got %d", got)
	}
}

func TestParseCanvasProjectHeader_PositiveSignIsRejected(t *testing.T) {
	// strconv.ParseUint base=10 does NOT accept `+` prefix. A future
	// refactor to strconv.Atoi would silently start accepting it.
	c, _ := newGinCtxWithHeaders(map[string]string{"X-Canvas-Project-Id": "+42"})
	if got := parseCanvasProjectHeader(c); got != 0 {
		t.Errorf("+42 should be rejected; got %d", got)
	}
}

func TestParseCanvasProjectHeader_BaseTenStrictness(t *testing.T) {
	// base=10 rejects any non-decimal notation.
	for _, raw := range []string{"0x10", "0b10", "1_000", "1e2", "0o7"} {
		c, _ := newGinCtxWithHeaders(map[string]string{"X-Canvas-Project-Id": raw})
		if got := parseCanvasProjectHeader(c); got != 0 {
			t.Errorf("base-10 strict: %q should return 0, got %d", raw, got)
		}
	}
}

func TestParseCanvasProjectHeader_MixedContentRejects(t *testing.T) {
	for _, raw := range []string{"42abc", "abc42", "4.2", "1,000"} {
		c, _ := newGinCtxWithHeaders(map[string]string{"X-Canvas-Project-Id": raw})
		if got := parseCanvasProjectHeader(c); got != 0 {
			t.Errorf("mixed content %q should return 0, got %d", raw, got)
		}
	}
}

func TestParseCanvasProjectHeader_InternalWhitespaceRejects(t *testing.T) {
	// TrimSpace only trims edges — internal whitespace remains and
	// fails ParseUint.
	c, _ := newGinCtxWithHeaders(map[string]string{"X-Canvas-Project-Id": "12 34"})
	if got := parseCanvasProjectHeader(c); got != 0 {
		t.Errorf("internal-space header should return 0, got %d", got)
	}
}

func TestParseCanvasProjectHeader_CaseInsensitiveHeaderName(t *testing.T) {
	// HTTP header names are case-insensitive per the canonical form
	// — c.GetHeader normalises via http.CanonicalHeaderKey.
	c, _ := newGinCtxWithHeaders(map[string]string{"x-canvas-project-id": "99"})
	if got := parseCanvasProjectHeader(c); got != 99 {
		t.Errorf("lowercase header name should resolve; got %d", got)
	}
}

func TestParseCanvasProjectHeader_MaxUint64BoundaryAndOverflow(t *testing.T) {
	// Max uint64 parses cleanly; max + 1 overflows → 0.
	c, _ := newGinCtxWithHeaders(map[string]string{"X-Canvas-Project-Id": "18446744073709551615"})
	if got := parseCanvasProjectHeader(c); got == 0 {
		t.Error("max-uint64 should parse cleanly, got 0")
	}

	c2, _ := newGinCtxWithHeaders(map[string]string{"X-Canvas-Project-Id": "18446744073709551616"})
	if got := parseCanvasProjectHeader(c2); got != 0 {
		t.Errorf("max+1 should overflow to 0; got %d", got)
	}
}

func TestParseCanvasAssetBindingsHeader_WhitespaceScopeByPassesEmptyGuard(t *testing.T) {
	// Scope == " " has len > 0, so `Scope == ""` is false and the
	// whole binding returns non-nil with the whitespace Scope intact.
	// Pin the honest observable — a refactor to TrimSpace on the
	// Scope field would surface here rather than silently shift
	// behaviour.
	payload, _ := json.Marshal(model.AssetBinding{Scope: " "})
	c, _ := newGinCtxWithHeaders(map[string]string{
		"X-Canvas-Asset-Bindings": string(payload),
	})
	got := parseCanvasAssetBindingsHeader(c)
	if got == nil {
		t.Fatal("whitespace-scope binding should return non-nil (no inner Trim)")
	}
	if got.Scope != " " {
		t.Errorf("scope stored as %q, want %q (preserves whitespace)", got.Scope, " ")
	}
}

func TestParseCanvasAssetBindingsHeader_ExplicitEmptyArraysReturnNil(t *testing.T) {
	// `{"characterIds":[],"brandIds":[],"productIds":[],"scope":""}`
	// — all lengths are 0 and scope is blank, guard fires.
	raw := `{"characterIds":[],"brandIds":[],"productIds":[],"scope":""}`
	c, _ := newGinCtxWithHeaders(map[string]string{
		"X-Canvas-Asset-Bindings": raw,
	})
	if got := parseCanvasAssetBindingsHeader(c); got != nil {
		t.Errorf("empty-array binding should return nil, got %+v", got)
	}
}

func TestParseCanvasAssetBindingsHeader_ZeroIDCountsAsPresent(t *testing.T) {
	// A binding with CharacterIDs: [0] — len > 0 so it passes the
	// guard. Pin so a refactor that filtered zero entries before the
	// len check wouldn't silently collapse to nil.
	payload, _ := json.Marshal(model.AssetBinding{CharacterIDs: []int{0}})
	c, _ := newGinCtxWithHeaders(map[string]string{
		"X-Canvas-Asset-Bindings": string(payload),
	})
	got := parseCanvasAssetBindingsHeader(c)
	if got == nil {
		t.Fatal("binding with [0] character id should return non-nil")
	}
	if len(got.CharacterIDs) != 1 || got.CharacterIDs[0] != 0 {
		t.Errorf("CharacterIDs = %+v, want [0]", got.CharacterIDs)
	}
}

func TestParseCanvasAssetBindingsHeader_UnknownFieldsSilentlyIgnored(t *testing.T) {
	// encoding/json default — unknown fields are silently dropped.
	// Pin the forward-compat observable so a refactor to
	// decoder.DisallowUnknownFields() would surface as a deliberate
	// tightening.
	raw := `{"scope":"shot","mystery":{"nested":true},"futureField":"v2"}`
	c, _ := newGinCtxWithHeaders(map[string]string{
		"X-Canvas-Asset-Bindings": raw,
	})
	got := parseCanvasAssetBindingsHeader(c)
	if got == nil {
		t.Fatal("unknown-fields payload should still parse and return non-nil")
	}
	if got.Scope != "shot" {
		t.Errorf("scope = %q, want shot", got.Scope)
	}
}

func TestRespondCanvasAIError_StoredErrorCodeIsRawNotTrimmed(t *testing.T) {
	// The TrimSpace check GATES on non-whitespace content, but the
	// stored value is the RAW errorCode (whitespace and all). A
	// refactor that trimmed the stored value would surface here.
	_, rec := newGinCtxWithHeaders(nil)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(rec)
	respondCanvasAIError(c, http.StatusBadRequest, "oops", "  FOO  ")

	var envelope struct {
		Data struct {
			ErrorCode string `json:"errorCode"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("unmarshal err = %v", err)
	}
	if envelope.Data.ErrorCode != "  FOO  " {
		t.Errorf("stored errorCode = %q, want raw %q (no internal trim)",
			envelope.Data.ErrorCode, "  FOO  ")
	}
}

func TestRespondCanvasAIError_TopLevelEnvelopeKeysAreExact(t *testing.T) {
	// Schema pin — envelope is exactly {code, message, data}. An
	// additional field (e.g. "timestamp") would surface as a
	// deliberate schema bump.
	_, rec := newGinCtxWithHeaders(nil)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(rec)
	respondCanvasAIError(c, http.StatusBadRequest, "oops", "CODE")

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("unmarshal err = %v", err)
	}
	if len(envelope) != 3 {
		t.Errorf("envelope keys = %v, want exactly {code,message,data}", keysOf(envelope))
	}
	for _, key := range []string{"code", "message", "data"} {
		if _, ok := envelope[key]; !ok {
			t.Errorf("missing key %q in envelope", key)
		}
	}
}

func TestRespondCanvasAIError_PayloadSuccessIsHardcodedFalse(t *testing.T) {
	// `success` is not a caller-overrideable field — it is always
	// false. Pin so a refactor that added an override argument
	// would surface.
	_, rec := newGinCtxWithHeaders(nil)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(rec)
	respondCanvasAIError(c, http.StatusForbidden, "denied", "X")

	var envelope struct {
		Data struct {
			Success bool `json:"success"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("unmarshal err = %v", err)
	}
	if envelope.Data.Success {
		t.Error("payload.success should be hardcoded false")
	}
}

func TestRespondCanvasAIError_ContentTypeIsApplicationJSON(t *testing.T) {
	// gin.Context.JSON sets Content-Type: application/json. Pin so a
	// refactor to c.String / c.Data would surface.
	_, rec := newGinCtxWithHeaders(nil)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(rec)
	respondCanvasAIError(c, http.StatusBadRequest, "oops", "CODE")

	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json prefix", ct)
	}
}

func TestRespondCanvasAIError_EmptyMessageLandsAsEmptyString(t *testing.T) {
	// No default message substitution — empty message stays empty.
	_, rec := newGinCtxWithHeaders(nil)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(rec)
	respondCanvasAIError(c, http.StatusBadRequest, "", "CODE")

	var envelope struct {
		Message string `json:"message"`
		Data    struct {
			Message string `json:"message"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("unmarshal err = %v", err)
	}
	if envelope.Message != "" {
		t.Errorf("envelope.message = %q, want empty string", envelope.Message)
	}
	if envelope.Data.Message != "" {
		t.Errorf("payload.message = %q, want empty string", envelope.Data.Message)
	}
}

// keysOf lifts the keys of a map to a slice for error messages.
func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
