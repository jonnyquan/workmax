package tools

// canvas_ai_error_test.go covers the headline contract: HTTP status
// mirrored to body.code, canonical envelope shape (success:false,
// message, errorCode), blank-errorCode omission via TrimSpace, no-
// reload-field discipline (vs chat helper). These fill the quieter
// gate invariants a silent regression would slip past:
//
//   • TrimSpace on errorCode covers the FULL whitespace class — tabs,
//     newlines, CR, mixed — not just ASCII spaces. Base pins "   "
//     only; pin `"\t"`, `"\n"`, `" \t\n "` so a refactor that narrowed
//     the check to `errorCode == ""` would surface (it would silently
//     start landing whitespace-only codes as-is in the envelope).
//   • Message is NOT trimmed. Trailing whitespace in the input message
//     survives byte-for-byte into both the top-level `message` and
//     `data.message` fields. Pin so a defensive-trim refactor would
//     surface — that refactor may be desirable but must be deliberate
//     (and applied symmetrically across both writes).
//   • Top-level `message` and `data.message` are the SAME string.
//     They read from the same variable, but a refactor that diverged
//     them (e.g. sanitising one and not the other) would silently
//     break the frontend which reads EITHER field. Pin equality.
//   • Empty-string message still writes the envelope — no guard on
//     message, no 500-from-empty. Pin so a refactor that short-
//     circuited blank messages would surface (blank messages can be
//     legitimately passed by the handler when the specific classifier
//     has no user-facing reason to display).
//   • Message with embedded newline survives verbatim (not stripped,
//     not escaped beyond standard JSON escaping). Pin control-char
//     preservation.
//   • The `success` key is ALWAYS literal `false` — never omitted,
//     never true. The helper's sole purpose is error writes; pin so
//     a refactor that made success configurable (to share with an OK
//     helper) would surface before it clobbered the error contract.
//   • The `data` payload has EXACTLY the expected keys — no sneaky
//     extras like `timestamp`, `traceId`, or `reload`. Pin the exact
//     keyset for both the blank-errorCode shape (2 keys) and the
//     present-errorCode shape (3 keys).
//   • Non-canonical errorCode (not in the known constants) passes
//     through unchanged — the helper is NOT a whitelist. Pin so a
//     refactor that added a "known code" gate would surface.
//   • Status 403 and 500 land correctly in both HTTP status and body
//     code. Base pins 400 and 401; pin the remaining common statuses.
//   • Response Content-Type is `application/json; charset=utf-8`.
//     gin.JSON sets this; pin so a refactor that switched to a raw
//     writer would surface.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func newAIErrorCtx() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/test", nil)
	return c, rec
}

func TestRespondCanvasAIError_TabOnlyErrorCodeOmitted(t *testing.T) {
	c, rec := newAIErrorCtx()
	respondCanvasAIError(c, http.StatusBadRequest, "oops", "\t")
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	data := body["data"].(map[string]any)
	if _, present := data["errorCode"]; present {
		t.Errorf("tab-only errorCode should be omitted; got %#v", data["errorCode"])
	}
}

func TestRespondCanvasAIError_NewlineOnlyErrorCodeOmitted(t *testing.T) {
	c, rec := newAIErrorCtx()
	respondCanvasAIError(c, http.StatusBadRequest, "oops", "\n")
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	data := body["data"].(map[string]any)
	if _, present := data["errorCode"]; present {
		t.Errorf("newline-only errorCode should be omitted; got %#v", data["errorCode"])
	}
}

func TestRespondCanvasAIError_MixedWhitespaceErrorCodeOmitted(t *testing.T) {
	c, rec := newAIErrorCtx()
	respondCanvasAIError(c, http.StatusBadRequest, "oops", " \t\r\n ")
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	data := body["data"].(map[string]any)
	if _, present := data["errorCode"]; present {
		t.Errorf("mixed-whitespace errorCode should be omitted; got %#v", data["errorCode"])
	}
}

func TestRespondCanvasAIError_MessagePreservesTrailingWhitespace(t *testing.T) {
	// Pin the no-trim-on-message contract. A refactor that added
	// TrimSpace to the message would surface here.
	c, rec := newAIErrorCtx()
	respondCanvasAIError(c, http.StatusBadRequest, "trailing ws   ", canvasAIErrorInvalidReq)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["message"] != "trailing ws   " {
		t.Errorf("top-level message = %q, want preserved with trailing ws", body["message"])
	}
	data := body["data"].(map[string]any)
	if data["message"] != "trailing ws   " {
		t.Errorf("data.message = %q, want preserved with trailing ws", data["message"])
	}
}

func TestRespondCanvasAIError_TopLevelAndDataMessageAreIdentical(t *testing.T) {
	// Pin byte-for-byte identity so a refactor that sanitised only one
	// would surface. Frontends read EITHER field.
	c, rec := newAIErrorCtx()
	msg := "Invalid request: missing prompt; special chars = ©®™"
	respondCanvasAIError(c, http.StatusBadRequest, msg, canvasAIErrorInvalidReq)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	data := body["data"].(map[string]any)
	if body["message"] != data["message"] {
		t.Errorf("divergence: top-level=%q, data=%q", body["message"], data["message"])
	}
	if body["message"] != msg {
		t.Errorf("message corrupted: got %q, want %q", body["message"], msg)
	}
}

func TestRespondCanvasAIError_EmptyMessageStillWrites(t *testing.T) {
	// No short-circuit on blank message — envelope still lands, just
	// with empty strings.
	c, rec := newAIErrorCtx()
	respondCanvasAIError(c, http.StatusInternalServerError, "", canvasAIErrorInvalidReq)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body unparseable: %s", rec.Body.String())
	}
	if body["message"] != "" {
		t.Errorf("message = %q, want empty", body["message"])
	}
	data := body["data"].(map[string]any)
	if data["message"] != "" {
		t.Errorf("data.message = %q, want empty", data["message"])
	}
	// errorCode still present because it's non-blank.
	if data["errorCode"] != canvasAIErrorInvalidReq {
		t.Errorf("errorCode = %#v, want %q", data["errorCode"], canvasAIErrorInvalidReq)
	}
}

func TestRespondCanvasAIError_MessageWithNewlineSurvives(t *testing.T) {
	// Embedded newline survives through JSON encoding (as \n in the
	// wire form, decoded back to a literal newline).
	c, rec := newAIErrorCtx()
	msg := "line one\nline two"
	respondCanvasAIError(c, http.StatusBadRequest, msg, canvasAIErrorInvalidReq)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["message"] != msg {
		t.Errorf("newline stripped: got %q, want %q", body["message"], msg)
	}
}

func TestRespondCanvasAIError_SuccessIsAlwaysLiteralFalse(t *testing.T) {
	// Try a few invocations — success must be false every time.
	cases := []struct {
		status int
		code   string
	}{
		{http.StatusBadRequest, canvasAIErrorInvalidReq},
		{http.StatusUnauthorized, canvasAIErrorUnauthorized},
		{http.StatusInternalServerError, "   "}, // blank code
		{http.StatusForbidden, canvasAIErrorProRequired},
	}
	for _, tc := range cases {
		c, rec := newAIErrorCtx()
		respondCanvasAIError(c, tc.status, "msg", tc.code)
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		data := body["data"].(map[string]any)
		if data["success"] != false {
			t.Errorf("status=%d code=%q: success = %#v, want false", tc.status, tc.code, data["success"])
		}
	}
}

func TestRespondCanvasAIError_DataKeysetWithBlankCode(t *testing.T) {
	// Blank errorCode → data has exactly {success, message}, no extras.
	c, rec := newAIErrorCtx()
	respondCanvasAIError(c, http.StatusBadRequest, "msg", "")
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	data := body["data"].(map[string]any)
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	if len(keys) != 2 {
		t.Errorf("data keyset size = %d (%v), want 2", len(keys), keys)
	}
	if _, ok := data["success"]; !ok {
		t.Errorf("missing success; got %v", keys)
	}
	if _, ok := data["message"]; !ok {
		t.Errorf("missing message; got %v", keys)
	}
}

func TestRespondCanvasAIError_DataKeysetWithPresentCode(t *testing.T) {
	// Present errorCode → data has exactly {success, message, errorCode}.
	c, rec := newAIErrorCtx()
	respondCanvasAIError(c, http.StatusBadRequest, "msg", canvasAIErrorInvalidReq)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	data := body["data"].(map[string]any)
	if len(data) != 3 {
		keys := make([]string, 0, len(data))
		for k := range data {
			keys = append(keys, k)
		}
		t.Errorf("data keyset size = %d (%v), want 3", len(data), keys)
	}
	for _, k := range []string{"success", "message", "errorCode"} {
		if _, ok := data[k]; !ok {
			t.Errorf("missing key %q", k)
		}
	}
}

func TestRespondCanvasAIError_UnknownErrorCodePassesThrough(t *testing.T) {
	// Helper is not a whitelist — any non-blank string lands as-is.
	c, rec := newAIErrorCtx()
	respondCanvasAIError(c, http.StatusBadRequest, "msg", "COMPLETELY_MADE_UP_CODE")
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	data := body["data"].(map[string]any)
	if data["errorCode"] != "COMPLETELY_MADE_UP_CODE" {
		t.Errorf("errorCode = %#v, want COMPLETELY_MADE_UP_CODE", data["errorCode"])
	}
}

func TestRespondCanvasAIError_StatusForbiddenRoundTrips(t *testing.T) {
	c, rec := newAIErrorCtx()
	respondCanvasAIError(c, http.StatusForbidden, "denied", canvasAIErrorProRequired)
	if rec.Code != http.StatusForbidden {
		t.Errorf("HTTP status = %d, want 403", rec.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if code, _ := body["code"].(float64); int(code) != http.StatusForbidden {
		t.Errorf("body.code = %#v, want 403", body["code"])
	}
}

func TestRespondCanvasAIError_Status500RoundTrips(t *testing.T) {
	c, rec := newAIErrorCtx()
	respondCanvasAIError(c, http.StatusInternalServerError, "boom", "INTERNAL")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("HTTP status = %d, want 500", rec.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if code, _ := body["code"].(float64); int(code) != http.StatusInternalServerError {
		t.Errorf("body.code = %#v, want 500", body["code"])
	}
}

func TestRespondCanvasAIError_ContentTypeIsJSON(t *testing.T) {
	c, rec := newAIErrorCtx()
	respondCanvasAIError(c, http.StatusBadRequest, "msg", canvasAIErrorInvalidReq)
	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json prefix", ct)
	}
}
