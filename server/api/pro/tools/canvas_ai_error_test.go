package tools

// Pins respondCanvasAIError — the envelope writer for the Canvas generation
// handlers (Img2Img / Outpaint / Inpaint / Mockup / EditText / SplitLayers).
//
// Distinct from respondCanvasError (legacy 200 + response.ERROR) and
// respondCanvasChatHTTPError (chat/agent with reload flag): the generation
// endpoints use the HTTP status directly and have no reload semantics —
// the frontend classifier only reads data.errorCode / data.message.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRespondCanvasAIError_WritesHTTPStatusAndCanonicalEnvelope(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/test", nil)

	respondCanvasAIError(c, http.StatusBadRequest, "Invalid request: missing prompt", canvasAIErrorInvalidReq)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}

	// Top-level code mirrors the HTTP status.
	if code, _ := body["code"].(float64); int(code) != http.StatusBadRequest {
		t.Errorf("body.code = %#v, want 400", body["code"])
	}
	if body["message"] != "Invalid request: missing prompt" {
		t.Errorf("body.message = %#v", body["message"])
	}

	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("data field missing or wrong type: %#v", body["data"])
	}
	if data["success"] != false {
		t.Errorf("data.success = %#v, want false", data["success"])
	}
	if data["message"] != "Invalid request: missing prompt" {
		t.Errorf("data.message = %#v", data["message"])
	}
	if data["errorCode"] != canvasAIErrorInvalidReq {
		t.Errorf("data.errorCode = %#v, want %q", data["errorCode"], canvasAIErrorInvalidReq)
	}
}

func TestRespondCanvasAIError_UnauthorizedPreservesStatus(t *testing.T) {
	// The Img2Img / Outpaint handlers all short-circuit to 401 via this
	// helper. Pin that the status is honored (no implicit downgrade to
	// 200 — the legacy envelope does that, this one does not).
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/test", nil)

	respondCanvasAIError(c, http.StatusUnauthorized, "Unauthorized", canvasAIErrorUnauthorized)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}

	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	data := body["data"].(map[string]any)
	if data["errorCode"] != canvasAIErrorUnauthorized {
		t.Errorf("data.errorCode = %#v, want %q", data["errorCode"], canvasAIErrorUnauthorized)
	}
}

func TestRespondCanvasAIError_BlankErrorCodeOmitted(t *testing.T) {
	// Blank/whitespace error codes must not land as "" in the envelope —
	// the frontend classifier treats presence as a signal and an empty
	// string would surface as an empty taxonomy entry.
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/test", nil)

	respondCanvasAIError(c, http.StatusInternalServerError, "something broke", "   ")

	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	data := body["data"].(map[string]any)
	if _, present := data["errorCode"]; present {
		t.Errorf("data.errorCode should be omitted when blank, got %#v", data["errorCode"])
	}
}

func TestRespondCanvasAIError_DoesNotSetReload(t *testing.T) {
	// respondCanvasAIError deliberately does NOT emit data.reload —
	// that's a chat/agent concern, not a generation concern. Pin the
	// omission so we don't accidentally copy the chat helper's shape.
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/test", nil)

	respondCanvasAIError(c, http.StatusUnauthorized, "Unauthorized", canvasAIErrorUnauthorized)

	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	data := body["data"].(map[string]any)
	if _, present := data["reload"]; present {
		t.Errorf("data.reload should not be set by respondCanvasAIError, got %#v", data["reload"])
	}
}
