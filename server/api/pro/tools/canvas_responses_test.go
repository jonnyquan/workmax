package tools

// Pins deriveCanvasErrorCode + respondCanvasError{,WithCode} +
// respondCanvasChatHTTPError behavior. The frontend error classifier
// reads `errorCode` (top-level) and `data.errorCode` from these
// envelopes, and the §12.1 taxonomy is only useful if the Go layer
// keeps emitting the same codes for the same message substrings.
//
// Kept as pure/handler tests: no DB, no middleware — gin.Context with
// httptest.ResponseRecorder is enough to prove shape.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestDeriveCanvasErrorCode_ExactMatchCases(t *testing.T) {
	cases := []struct {
		message string
		want    string
	}{
		{"Unauthorized", canvasAIErrorUnauthorized},
		{"unauthorized", canvasAIErrorUnauthorized},
		{"  Unauthorized  ", canvasAIErrorUnauthorized},
		{"invalid project id", "INVALID_PROJECT_ID"},
		{"invalid project uuid", "INVALID_PROJECT_UUID"},
		{"invalid version", "INVALID_VERSION"},
		{"project not found", "PROJECT_NOT_FOUND"},
		{"version not found", "VERSION_NOT_FOUND"},
		{"version is read-only", "VERSION_READ_ONLY"},
	}
	for _, tc := range cases {
		if got := deriveCanvasErrorCode(tc.message); got != tc.want {
			t.Errorf("deriveCanvasErrorCode(%q) = %q, want %q", tc.message, got, tc.want)
		}
	}
}

func TestDeriveCanvasErrorCode_PrefixMatch(t *testing.T) {
	// "invalid request" uses HasPrefix so the binding error suffix is
	// tolerated.
	if got := deriveCanvasErrorCode("Invalid request: EOF"); got != canvasAIErrorInvalidReq {
		t.Errorf("got %q, want %q", got, canvasAIErrorInvalidReq)
	}
}

func TestDeriveCanvasErrorCode_SubstringMatch(t *testing.T) {
	// The Contains-based branches must pick up the code even when the
	// handler wraps the message with a prefix/suffix (e.g. "%s: %v").
	cases := []struct {
		message string
		want    string
	}{
		{"upload asset failed: io error", "UPLOAD_ASSET_FAILED"},
		{"Wrapped: create project failed: timeout", "CREATE_PROJECT_FAILED"},
		{"update version failed", "UPDATE_VERSION_FAILED"},
		{"failed to patch elements: conflict", "PATCH_ELEMENTS_FAILED"},
		{"videoUrl is required", "VIDEO_URL_REQUIRED"},
		{"videoUrl is invalid", "VIDEO_URL_INVALID"},
		{"videoUrl must use http or https", "VIDEO_URL_PROTOCOL_INVALID"},
		{"failed to optimize prompt: rate limit", "OPTIMIZE_PROMPT_FAILED"},
		{"title cannot be empty", "TITLE_REQUIRED"},
	}
	for _, tc := range cases {
		if got := deriveCanvasErrorCode(tc.message); got != tc.want {
			t.Errorf("deriveCanvasErrorCode(%q) = %q, want %q", tc.message, got, tc.want)
		}
	}
}

func TestDeriveCanvasErrorCode_Default(t *testing.T) {
	// Anything unrecognized lands on CANVAS_OPERATION_FAILED so the
	// frontend classifier has a stable fallback bucket.
	if got := deriveCanvasErrorCode("something weird happened"); got != "CANVAS_OPERATION_FAILED" {
		t.Errorf("unexpected default: %q", got)
	}
	if got := deriveCanvasErrorCode(""); got != "CANVAS_OPERATION_FAILED" {
		t.Errorf("empty message default: %q", got)
	}
}

func newRecorderCtx() (*httptest.ResponseRecorder, *gin.Context) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/test", nil)
	return w, c
}

func decodeJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("body is not JSON: %v — %s", err, w.Body.String())
	}
	return out
}

func TestRespondCanvasError_EmitsErrorCodeAtBothPositions(t *testing.T) {
	// The frontend reads errorCode from either location — the
	// envelope must set both.
	w, c := newRecorderCtx()
	respondCanvasError(c, "project not found")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (legacy envelope)", w.Code)
	}
	body := decodeJSON(t, w)
	if body["errorCode"] != "PROJECT_NOT_FOUND" {
		t.Errorf("top-level errorCode = %#v, want PROJECT_NOT_FOUND", body["errorCode"])
	}
	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("data field missing or wrong type: %#v", body["data"])
	}
	if data["errorCode"] != "PROJECT_NOT_FOUND" {
		t.Errorf("data.errorCode = %#v, want PROJECT_NOT_FOUND", data["errorCode"])
	}
	if data["success"] != false {
		t.Errorf("data.success = %#v, want false", data["success"])
	}
	if _, present := data["reload"]; present {
		t.Errorf("data.reload should not be set for non-auth errors: %#v", data["reload"])
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("error field missing or wrong type: %#v", body["error"])
	}
	if errObj["message"] != "project not found" || errObj["userMessage"] != "project not found" {
		t.Errorf("error.{message,userMessage} mismatch: %#v", errObj)
	}
}

func TestRespondCanvasErrorWithCode_UnauthorizedSetsReload(t *testing.T) {
	// Unauthorized on the legacy envelope must set data.reload=true so
	// the frontend can force a re-login round-trip without relying on
	// the HTTP status (the envelope uses response.ERROR = 7 here).
	w, c := newRecorderCtx()
	respondCanvasErrorWithCode(c, "Unauthorized", canvasAIErrorUnauthorized)

	body := decodeJSON(t, w)
	data := body["data"].(map[string]any)
	if data["reload"] != true {
		t.Errorf("data.reload = %#v, want true", data["reload"])
	}
	if data["errorCode"] != canvasAIErrorUnauthorized {
		t.Errorf("data.errorCode = %#v, want %q", data["errorCode"], canvasAIErrorUnauthorized)
	}
}

func TestRespondCanvasErrorWithCode_BlankCodeOmitted(t *testing.T) {
	// A blank or whitespace-only code must not be emitted at all —
	// the frontend treats the *presence* of errorCode as a signal,
	// and an empty string would surface as an "" taxonomy entry.
	w, c := newRecorderCtx()
	respondCanvasErrorWithCode(c, "something went wrong", "   ")

	body := decodeJSON(t, w)
	if _, present := body["errorCode"]; present {
		t.Errorf("top-level errorCode should be omitted when blank, got %#v", body["errorCode"])
	}
	data := body["data"].(map[string]any)
	if _, present := data["errorCode"]; present {
		t.Errorf("data.errorCode should be omitted when blank, got %#v", data["errorCode"])
	}
}

func TestRespondCanvasChatHTTPError_StatusAndReload(t *testing.T) {
	// The Chat/Agent path uses HTTP status directly, so the recorder
	// code must match. reload=true surfaces into data.reload.
	w, c := newRecorderCtx()
	respondCanvasChatHTTPError(c, http.StatusUnauthorized, "nope", "MY_CODE", true)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
	body := decodeJSON(t, w)
	// code is encoded as JSON number, so it comes back as float64.
	if code, _ := body["code"].(float64); int(code) != http.StatusUnauthorized {
		t.Errorf("body.code = %#v, want 401", body["code"])
	}
	data := body["data"].(map[string]any)
	if data["reload"] != true {
		t.Errorf("data.reload = %#v, want true", data["reload"])
	}
	if data["errorCode"] != "MY_CODE" {
		t.Errorf("data.errorCode = %#v, want MY_CODE", data["errorCode"])
	}
}

func TestRespondCanvasUnauthorized_IsPresetChatHTTPError(t *testing.T) {
	// respondCanvasUnauthorized is the single entry point used by
	// every handler for the auth gate — pin it as a whole so a
	// signature drift (e.g. status → 403, reload → false) fails here.
	w, c := newRecorderCtx()
	respondCanvasUnauthorized(c)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	body := decodeJSON(t, w)
	if msg, _ := body["message"].(string); !strings.EqualFold(msg, "Unauthorized") {
		t.Errorf("body.message = %#v, want Unauthorized", body["message"])
	}
	data := body["data"].(map[string]any)
	if data["reload"] != true {
		t.Errorf("data.reload = %#v, want true", data["reload"])
	}
	if data["errorCode"] != canvasAIErrorUnauthorized {
		t.Errorf("data.errorCode = %#v, want %q", data["errorCode"], canvasAIErrorUnauthorized)
	}
}
