package tools

// Pins the JSON envelope of AdminStats — the admin-only canvas counter
// snapshot landed as B6 PR 3 on 2026-05-15.
//
// The handler is intentionally simple (atomic Load × 9 + json.Marshal),
// but the JSON shape becomes a contract the moment ops scripts or a
// future Grafana scraper starts polling it. A test pin on the
// envelope shape catches drift that a typecheck cannot — e.g., a
// refactor that renames PromptGuard.Stats() fields would compile but
// silently break the wire contract.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	canvasService "server/service/tools/canvas"

	"github.com/gin-gonic/gin"
)

func TestAdminStats_EmitsCanonicalJSONShape(t *testing.T) {
	// Exercise the counter increments so the snapshot has non-zero
	// fields to assert on. Reads from the same package-level guards /
	// counters that AdminStats reads.
	g := canvasService.Default()
	g.Check(canvasService.GuardInput{Prompt: "a clean prompt"})

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/stats", nil)

	api := &CanvasApi{}
	api.AdminStats(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var body CanvasStatsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not the canonical envelope: %v\nbody: %s", err, w.Body.String())
	}

	// The clean Check above must have advanced both `checks` and
	// `allowed`. We assert >= rather than == because other parallel
	// tests in the same package share the singleton guard.
	if body.PromptGuard.Checks == 0 {
		t.Errorf("PromptGuard.Checks should be > 0 after a Check call, got %d", body.PromptGuard.Checks)
	}
	if body.PromptGuard.Allowed == 0 {
		t.Errorf("PromptGuard.Allowed should be > 0 after a clean Check, got %d", body.PromptGuard.Allowed)
	}
}

func TestAdminStats_JSONFieldNamesAreCamelCase(t *testing.T) {
	// Drift in field naming would silently break any downstream scraper
	// or Grafana dashboard. Pin the exact JSON key names.
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/stats", nil)

	api := &CanvasApi{}
	api.AdminStats(c)

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}

	for _, key := range []string{"promptGuard", "consistencyJudge"} {
		if _, present := raw[key]; !present {
			t.Errorf("top-level key %q missing from response: %s", key, w.Body.String())
		}
	}

	var guard map[string]json.RawMessage
	if err := json.Unmarshal(raw["promptGuard"], &guard); err != nil {
		t.Fatalf("promptGuard not an object: %v", err)
	}
	for _, key := range []string{
		"checks",
		"allowed",
		"truncatedPrompt",
		"truncatedNegative",
		"rejectedSensitive",
		"warnedInjection",
	} {
		if _, present := guard[key]; !present {
			t.Errorf("promptGuard.%s missing: %s", key, raw["promptGuard"])
		}
	}

	var judge map[string]json.RawMessage
	if err := json.Unmarshal(raw["consistencyJudge"], &judge); err != nil {
		t.Fatalf("consistencyJudge not an object: %v", err)
	}
	for _, key := range []string{"ok", "warned", "skipped"} {
		if _, present := judge[key]; !present {
			t.Errorf("consistencyJudge.%s missing: %s", key, raw["consistencyJudge"])
		}
	}
}

func TestAdminStats_AllCountersAreUInt64Numbers(t *testing.T) {
	// A refactor that switched any counter to a signed type would
	// silently allow negative values into the wire — guard against
	// that by parsing each field as a uint64.
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/stats", nil)

	api := &CanvasApi{}
	api.AdminStats(c)

	var body struct {
		PromptGuard      map[string]json.Number `json:"promptGuard"`
		ConsistencyJudge map[string]json.Number `json:"consistencyJudge"`
	}
	dec := json.NewDecoder(w.Body)
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	check := func(label string, fields map[string]json.Number) {
		for name, n := range fields {
			i, err := n.Int64()
			if err != nil {
				t.Errorf("%s.%s = %q, want integer", label, name, n)
				continue
			}
			if i < 0 {
				t.Errorf("%s.%s = %d, want non-negative", label, name, i)
			}
		}
	}
	check("promptGuard", body.PromptGuard)
	check("consistencyJudge", body.ConsistencyJudge)
}
