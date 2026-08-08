package tools

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// respondInternal owns one contract: the err detail goes to the
// log (verified manually since globals.Error is a package
// singleton), the user-facing body carries only the stable phrase.

func TestRespondInternal_BodyHasOnlyUserMessage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("PUT", "/api/test/42", nil)

	leak := errors.New("Error 1062: Duplicate entry 'abc' for key 'idx_uid_slug'")
	respondInternal(c, "Update failed", leak)

	body := w.Body.String()
	if !strings.Contains(body, "Update failed") {
		t.Errorf("body missing user message, got: %s", body)
	}
	if strings.Contains(body, "Error 1062") || strings.Contains(body, "Duplicate entry") {
		t.Errorf("body leaked driver detail: %s", body)
	}
	if strings.Contains(body, "idx_uid_slug") {
		t.Errorf("body leaked schema fragment: %s", body)
	}
}

func TestRespondInternal_DoesNotPanicWithNilErr(t *testing.T) {
	// Defensive: a refactor that hands nil through must not crash
	// the handler. The wrapped log line will read "<nil>" but the
	// response still goes out.
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/api/test", nil)

	respondInternal(c, "Create failed", nil) // must not panic

	if !strings.Contains(w.Body.String(), "Create failed") {
		t.Errorf("body missing user message, got: %s", w.Body.String())
	}
}
