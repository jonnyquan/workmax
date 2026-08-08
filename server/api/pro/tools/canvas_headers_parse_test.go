package tools

// canvas_generation_helpers_test.go covers the pure data helpers.
// These fill in the header-reading + error-response helpers that sit
// next to them and are equally pure (given a *gin.Context) but untested:
//
//   - parseCanvasProjectHeader: five branches — absent, blank, malformed,
//     zero, valid. The return type is uint, so a regression that dropped
//     the error check on strconv.ParseUint would silently map garbage
//     ("abc") to the uint zero value and behave as "not canvas-bound".
//     That masks the caller's bug instead of surfacing it, but we WANT
//     this behaviour — a non-canvas client should never see the canvas
//     binding logic activate. Pin the exact semantics.
//
//   - parseCanvasAssetBindingsHeader: seven branches — absent, blank,
//     bad JSON, empty-struct-equivalent, scope-only, ids-only, full.
//     The "empty-struct equivalent" path is the most subtle: a caller
//     that JSON-serialised an empty AssetBinding{} as `{}` must get
//     nil back, NOT a pointer to a zero-value struct. Callers use
//     `if binding != nil { ... }` to decide whether to stamp the
//     requestData field, so a regression that always returned a
//     non-nil pointer would inject empty bindings into every task.
//
//   - respondCanvasAIError: pin that the error code is omitted from
//     the data payload when blank (TrimSpace rule) and that the wrapper
//     envelope carries the numeric status twice (both as HTTP status
//     AND as `code` in the JSON body — frontends key on either).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"server/model"

	"github.com/gin-gonic/gin"
)

func newGinCtxWithHeaders(headers map[string]string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/t", nil)
	for k, v := range headers {
		c.Request.Header.Set(k, v)
	}
	return c, rec
}

func TestParseCanvasProjectHeader_AbsentReturnsZero(t *testing.T) {
	c, _ := newGinCtxWithHeaders(nil)
	if got := parseCanvasProjectHeader(c); got != 0 {
		t.Errorf("absent header should return 0, got %d", got)
	}
}

func TestParseCanvasProjectHeader_BlankReturnsZero(t *testing.T) {
	// Whitespace-only header — the TrimSpace guard must short-circuit
	// before ParseUint would try to parse "" and produce its own error.
	c, _ := newGinCtxWithHeaders(map[string]string{"X-Canvas-Project-Id": "   "})
	if got := parseCanvasProjectHeader(c); got != 0 {
		t.Errorf("blank header should return 0, got %d", got)
	}
}

func TestParseCanvasProjectHeader_MalformedReturnsZero(t *testing.T) {
	// "not canvas-bound" is the fail-safe — a non-numeric header value
	// (e.g. a bad proxy rewrite) must not crash or leak through as a
	// bogus project id. Canvas bindings skip entirely instead.
	cases := []string{"abc", "1.5", "-1", "1e3", "999999999999999999999999999"}
	for _, raw := range cases {
		c, _ := newGinCtxWithHeaders(map[string]string{"X-Canvas-Project-Id": raw})
		if got := parseCanvasProjectHeader(c); got != 0 {
			t.Errorf("malformed header %q should return 0, got %d", raw, got)
		}
	}
}

func TestParseCanvasProjectHeader_ValidIntegerReturnsValue(t *testing.T) {
	c, _ := newGinCtxWithHeaders(map[string]string{"X-Canvas-Project-Id": "42"})
	if got := parseCanvasProjectHeader(c); got != 42 {
		t.Errorf("valid header should return 42, got %d", got)
	}
}

func TestParseCanvasProjectHeader_TrimsSurroundingWhitespace(t *testing.T) {
	// A proxy or test harness may add leading/trailing whitespace to
	// headers. The TrimSpace call must happen before ParseUint — pin
	// this so a refactor that dropped the trim would slip past the
	// explicit blank case above but fail here.
	c, _ := newGinCtxWithHeaders(map[string]string{"X-Canvas-Project-Id": "  77  "})
	if got := parseCanvasProjectHeader(c); got != 77 {
		t.Errorf("whitespace-padded header should return 77, got %d", got)
	}
}

func TestParseCanvasAssetBindingsHeader_AbsentReturnsNil(t *testing.T) {
	c, _ := newGinCtxWithHeaders(nil)
	if got := parseCanvasAssetBindingsHeader(c); got != nil {
		t.Errorf("absent header should return nil, got %+v", got)
	}
}

func TestParseCanvasAssetBindingsHeader_BlankReturnsNil(t *testing.T) {
	c, _ := newGinCtxWithHeaders(map[string]string{"X-Canvas-Asset-Bindings": "  "})
	if got := parseCanvasAssetBindingsHeader(c); got != nil {
		t.Errorf("blank header should return nil, got %+v", got)
	}
}

func TestParseCanvasAssetBindingsHeader_InvalidJSONReturnsNil(t *testing.T) {
	// Silent failure is the right call — the injector becomes a no-op
	// instead of rejecting the request. A regression that surfaced the
	// parse error would break backward-compat for any older client
	// still serialising the legacy shape. Pin the fail-closed behaviour.
	c, _ := newGinCtxWithHeaders(map[string]string{"X-Canvas-Asset-Bindings": "{not valid json"})
	if got := parseCanvasAssetBindingsHeader(c); got != nil {
		t.Errorf("invalid JSON should return nil, got %+v", got)
	}
}

func TestParseCanvasAssetBindingsHeader_EmptyBindingReturnsNil(t *testing.T) {
	// `{}` unmarshals into a zero-value AssetBinding{}. The handler MUST
	// return nil in that case so callers' `if binding != nil` gate stays
	// load-bearing — otherwise every task would get an empty
	// assetBindings field injected, which the downstream injector would
	// then have to re-check. Pin the nil return to short-circuit early.
	c, _ := newGinCtxWithHeaders(map[string]string{"X-Canvas-Asset-Bindings": "{}"})
	if got := parseCanvasAssetBindingsHeader(c); got != nil {
		t.Errorf("empty-object binding should return nil, got %+v", got)
	}
}

func TestParseCanvasAssetBindingsHeader_ScopeOnlyReturnsBinding(t *testing.T) {
	// A scope without any character/brand/product ids is still a valid
	// binding — the scope field alone alters the injector behaviour.
	// Pin the non-nil return so the scope signal survives the parse.
	payload, _ := json.Marshal(model.AssetBinding{Scope: "shot"})
	c, _ := newGinCtxWithHeaders(map[string]string{
		"X-Canvas-Asset-Bindings": string(payload),
	})
	got := parseCanvasAssetBindingsHeader(c)
	if got == nil {
		t.Fatal("scope-only binding should return non-nil")
	}
	if got.Scope != "shot" {
		t.Errorf("scope = %q, want %q", got.Scope, "shot")
	}
}

func TestParseCanvasAssetBindingsHeader_IDsOnlyReturnsBinding(t *testing.T) {
	// A binding with ids but no scope is also valid — the scope may
	// default on the downstream side. Pin the non-nil return to the
	// ids-only path so a regression that required BOTH scope AND ids
	// would surface here.
	payload, _ := json.Marshal(model.AssetBinding{
		CharacterIDs: []int{1, 2},
	})
	c, _ := newGinCtxWithHeaders(map[string]string{
		"X-Canvas-Asset-Bindings": string(payload),
	})
	got := parseCanvasAssetBindingsHeader(c)
	if got == nil {
		t.Fatal("ids-only binding should return non-nil")
	}
	if len(got.CharacterIDs) != 2 || got.CharacterIDs[0] != 1 {
		t.Errorf("CharacterIDs = %+v, want [1,2]", got.CharacterIDs)
	}
}

func TestRespondCanvasAIError_OmitsErrorCodeWhenBlank(t *testing.T) {
	// TrimSpace rule — blank / whitespace-only error codes must NOT
	// land in the payload as an empty-string field. Frontends that
	// check `payload.errorCode != null` to render a CTA would otherwise
	// see a truthy empty string and render the wrong UI branch.
	cases := []string{"", "   ", "\t\n"}
	for _, ec := range cases {
		_, rec := newGinCtxWithHeaders(nil)
		gin.SetMode(gin.TestMode)
		c, _ := gin.CreateTestContext(rec)
		respondCanvasAIError(c, http.StatusBadRequest, "oops", ec)

		var envelope struct {
			Code    int `json:"code"`
			Message string `json:"message"`
			Data    struct {
				Success   bool   `json:"success"`
				Message   string `json:"message"`
				ErrorCode string `json:"errorCode,omitempty"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("body = %s; unmarshal err = %v", rec.Body.String(), err)
		}
		if envelope.Data.ErrorCode != "" {
			t.Errorf("errorCode for input %q = %q, want empty/omitted",
				ec, envelope.Data.ErrorCode)
		}
	}
}

func TestRespondCanvasAIError_IncludesErrorCodeWhenPresent(t *testing.T) {
	// Symmetric case: a non-blank error code lands at payload.errorCode.
	// Pin the field name so a rename (e.g. "code" → "errorCode" drift)
	// surfaces here rather than only in a frontend that silently stops
	// matching.
	_, rec := newGinCtxWithHeaders(nil)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(rec)
	respondCanvasAIError(c, http.StatusForbidden, "denied", "PRO_MODEL_REQUIRED")

	var envelope struct {
		Code int `json:"code"`
		Data struct {
			ErrorCode string `json:"errorCode"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("unmarshal err = %v (body: %s)", err, rec.Body.String())
	}
	if envelope.Data.ErrorCode != "PRO_MODEL_REQUIRED" {
		t.Errorf("errorCode = %q, want PRO_MODEL_REQUIRED", envelope.Data.ErrorCode)
	}
	if envelope.Code != http.StatusForbidden {
		t.Errorf("code = %d, want %d", envelope.Code, http.StatusForbidden)
	}
}

func TestRespondCanvasAIError_EnvelopeCarriesStatusTwice(t *testing.T) {
	// The JSON body carries `code` (mirrors the HTTP status) alongside
	// the HTTP response status itself. Pin the duplication so a future
	// cleanup that unified to one representation notices both consumers
	// (frontend + ops dashboards) depend on the numeric field in the body.
	_, rec := newGinCtxWithHeaders(nil)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(rec)
	respondCanvasAIError(c, http.StatusUnauthorized, "no auth", "UNAUTHORIZED")

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("HTTP status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	var envelope struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("unmarshal err = %v (body: %s)", err, rec.Body.String())
	}
	if envelope.Code != http.StatusUnauthorized {
		t.Errorf("body code = %d, want %d (must mirror HTTP status)",
			envelope.Code, http.StatusUnauthorized)
	}
	if envelope.Message != "no auth" {
		t.Errorf("body message = %q, want %q", envelope.Message, "no auth")
	}
}
