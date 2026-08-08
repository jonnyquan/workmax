package workagent

// mcp_connector_api_test.go — Phase B4a handler tests. Mirrors
// agent_direction_api_test.go's shape: in-memory DB + inline
// route registration + httptest. The key contracts pinned here
// are the wire-shape posture (list scrubs values, detail
// reveals plaintext for owner) and the IDOR guards.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"server/model"
	"server/service/secrets"
	"server/utils/testutil"

	"github.com/gin-gonic/gin"
)

// putJSON / patchJSON — siblings of postJSON
// (agent_api_handle_chat_test.go) for the methods this file needs.
func putJSON(engine *gin.Engine, path string, body any) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(w, req)
	return w
}

func patchJSON(engine *gin.Engine, path string, body any) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPatch, path, bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(w, req)
	return w
}

// getJSON alias for getRequest — keeps the test reads symmetrical
// with postJSON / putJSON / patchJSON naming.
func getJSON(engine *gin.Engine, path string) *httptest.ResponseRecorder {
	return getRequest(engine, path)
}

// extractIDFromResponse grabs the `"id":N` integer out of a JSON
// response body. Cheaper than full json.Unmarshal for tests that
// only need the id to chain the next request.
func extractIDFromResponse(t *testing.T, body string) string {
	t.Helper()
	re := regexp.MustCompile(`"id":(\d+)`)
	m := re.FindStringSubmatch(body)
	if len(m) < 2 {
		t.Fatalf("no id field in response: %s", body)
	}
	return m[1]
}

// TestMain — like service/mcp_connector + workagent service
// packages, install a deterministic 0x42 master key so the
// encrypted columns round-trip in tests.
func TestMain(m *testing.M) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = 0x42
	}
	secrets.SetKeyForTesting(key)
	code := m.Run()
	secrets.ClearKeyForTesting()
	os.Exit(code)
}

func buildMCPConnectorEngine(_ *testing.T, uid uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := NewAIChatApiNew()
	mw := withClaims(uid)
	r.GET("/mcp-connectors", mw, api.ListMCPConnectors)
	r.GET("/mcp-connectors/:id", mw, api.GetMCPConnector)
	r.POST("/mcp-connectors", mw, api.CreateMCPConnector)
	r.PUT("/mcp-connectors/:id", mw, api.UpdateMCPConnector)
	r.PATCH("/mcp-connectors/:id/enabled", mw, api.SetMCPConnectorEnabled)
	r.DELETE("/mcp-connectors/:id", mw, api.DeleteMCPConnector)
	r.POST("/mcp-connectors/:id/test", mw, api.TestMCPConnector)
	return r
}

func TestMCPConnector_CreateHappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildMCPConnectorEngine(t, 42)

	w := postJSON(engine, "/mcp-connectors", map[string]any{
		"name":      "linear",
		"transport": "http",
		"url":       "https://example.com/mcp",
		"headers":   map[string]any{"Authorization": "Bearer k_canary_12345"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "k_canary_12345") {
		t.Errorf("detail response must not echo secret values: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Authorization") {
		t.Errorf("detail response should expose secret key names: %s", w.Body.String())
	}
}

func TestMCPConnector_CreateRejectsEmptyName(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildMCPConnectorEngine(t, 42)

	w := postJSON(engine, "/mcp-connectors", map[string]any{
		"name":      "  ",
		"transport": "http",
		"url":       "https://example.com/mcp",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d (body=%s)", w.Code, w.Body.String())
	}
}

func TestMCPConnector_CreateRejectsUnknownTransport(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildMCPConnectorEngine(t, 42)

	w := postJSON(engine, "/mcp-connectors", map[string]any{
		"name":      "weird",
		"transport": "ws",
		"url":       "https://example/ws",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestMCPConnector_CreateRejectsStdioTransport(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildMCPConnectorEngine(t, 42)

	w := postJSON(engine, "/mcp-connectors", map[string]any{
		"name":      "local",
		"transport": "stdio",
		"command":   "/usr/bin/mcp-local",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d (body=%s)", w.Code, w.Body.String())
	}
}

func TestMCPConnector_CreateRejectsUnsafeRemoteURL(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildMCPConnectorEngine(t, 42)

	for _, rawURL := range []string{
		"http://example.com/mcp",
		"https://localhost/mcp",
		"https://127.0.0.1/mcp",
		"https://10.0.0.1/mcp",
		"https://169.254.169.254/latest/meta-data",
	} {
		w := postJSON(engine, "/mcp-connectors", map[string]any{
			"name":      "unsafe",
			"transport": "http",
			"url":       rawURL,
		})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("url %q: expected 400, got %d (body=%s)", rawURL, w.Code, w.Body.String())
		}
	}
}

func TestMCPConnector_Unauthorized(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	// uid=0 → anonymous; handler must reject.
	engine := buildMCPConnectorEngine(t, 0)
	w := postJSON(engine, "/mcp-connectors", map[string]any{
		"name":      "x",
		"transport": "http",
		"url":       "https://example.com/mcp",
	})
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestMCPConnector_ListScrubsSecretValues(t *testing.T) {
	// Wire-shape contract: list MUST NOT return env / header
	// VALUES. Only the key names (so UI can render "3 vars set"
	// + names) + has-secrets booleans. The canary string we
	// stored above must NOT appear in list output.
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildMCPConnectorEngine(t, 42)

	postJSON(engine, "/mcp-connectors", map[string]any{
		"name":      "linear",
		"transport": "http",
		"url":       "https://example.com/mcp",
		"headers":   map[string]any{"Authorization": "Bearer k_canary_secret_999"},
	})

	w := getJSON(engine, "/mcp-connectors")
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "k_canary_secret_999") {
		t.Errorf("LIST LEAKED PLAINTEXT VALUE: %s", body)
	}
	// But it SHOULD include the key name so UI knows what's set.
	if !strings.Contains(body, "Authorization") {
		t.Errorf("list should expose header KEY names: %s", body)
	}
	if !strings.Contains(body, `"headerKeys"`) {
		t.Errorf("expected headerKeys field in summary: %s", body)
	}
}

func TestMCPConnector_GetDoesNotReturnPlaintextSecrets(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildMCPConnectorEngine(t, 42)

	w := postJSON(engine, "/mcp-connectors", map[string]any{
		"name":      "linear",
		"transport": "http",
		"url":       "https://example.com/mcp",
		"headers":   map[string]any{"Authorization": "Bearer k_canary_for_owner"},
	})
	id := extractIDFromResponse(t, w.Body.String())

	w2 := getJSON(engine, "/mcp-connectors/"+id)
	if w2.Code != http.StatusOK {
		t.Fatalf("get status = %d", w2.Code)
	}
	if strings.Contains(w2.Body.String(), "k_canary_for_owner") {
		t.Errorf("detail leaked plaintext secret: %s", w2.Body.String())
	}
	if !strings.Contains(w2.Body.String(), "Authorization") {
		t.Errorf("detail should include secret key names: %s", w2.Body.String())
	}
}

func TestMCPConnector_GetCrossTenantReturns404(t *testing.T) {
	// IDOR — uid=42 creates, uid=99 tries to read. Must
	// 404, no plaintext leak.
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	ownerEngine := buildMCPConnectorEngine(t, 42)

	w := postJSON(ownerEngine, "/mcp-connectors", map[string]any{
		"name":      "private",
		"transport": "http",
		"url":       "https://example.com/mcp",
		"headers":   map[string]any{"Authorization": "Bearer k_canary_cross_tenant"},
	})
	id := extractIDFromResponse(t, w.Body.String())

	attackerEngine := buildMCPConnectorEngine(t, 99)
	w2 := getJSON(attackerEngine, "/mcp-connectors/"+id)
	if w2.Code != http.StatusNotFound {
		t.Errorf("cross-tenant should 404, got %d (body=%s)", w2.Code, w2.Body.String())
	}
	if strings.Contains(w2.Body.String(), "k_canary_cross_tenant") {
		t.Errorf("cross-tenant LEAKED plaintext: %s", w2.Body.String())
	}
}

func TestMCPConnector_UpdateChangesName(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildMCPConnectorEngine(t, 42)

	w := postJSON(engine, "/mcp-connectors", map[string]any{
		"name":      "old",
		"transport": "http",
		"url":       "https://example.com/mcp",
	})
	id := extractIDFromResponse(t, w.Body.String())

	w2 := putJSON(engine, "/mcp-connectors/"+id, map[string]any{
		"name": "new",
	})
	if w2.Code != http.StatusOK {
		t.Fatalf("update status = %d (body=%s)", w2.Code, w2.Body.String())
	}
	if !strings.Contains(w2.Body.String(), `"name":"new"`) {
		t.Errorf("update should echo new name: %s", w2.Body.String())
	}
}

func TestMCPConnector_PatchEnabled(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildMCPConnectorEngine(t, 42)

	w := postJSON(engine, "/mcp-connectors", map[string]any{
		"name":      "x",
		"transport": "http",
		"url":       "https://example.com/mcp",
	})
	id := extractIDFromResponse(t, w.Body.String())

	w2 := patchJSON(engine, "/mcp-connectors/"+id+"/enabled", map[string]any{
		"enabled": false,
	})
	if w2.Code != http.StatusOK {
		t.Fatalf("patch status = %d", w2.Code)
	}
	if !strings.Contains(w2.Body.String(), `"enabled":false`) {
		t.Errorf("patch should echo state: %s", w2.Body.String())
	}

	// Verify via GET.
	w3 := getJSON(engine, "/mcp-connectors/"+id)
	if !strings.Contains(w3.Body.String(), `"enabled":false`) {
		t.Errorf("subsequent GET should reflect disabled: %s", w3.Body.String())
	}
}

func TestMCPConnector_DeleteRemovesFromList(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildMCPConnectorEngine(t, 42)

	w := postJSON(engine, "/mcp-connectors", map[string]any{
		"name":      "to-delete",
		"transport": "http",
		"url":       "https://example.com/mcp",
	})
	id := extractIDFromResponse(t, w.Body.String())

	w2 := deleteRequest(engine, "/mcp-connectors/"+id)
	if w2.Code != http.StatusOK {
		t.Fatalf("delete status = %d", w2.Code)
	}

	w3 := getJSON(engine, "/mcp-connectors")
	if strings.Contains(w3.Body.String(), `"to-delete"`) {
		t.Errorf("deleted connector should not appear: %s", w3.Body.String())
	}

	// And direct GET should now 404.
	w4 := getJSON(engine, "/mcp-connectors/"+id)
	if w4.Code != http.StatusNotFound {
		t.Errorf("get of deleted should 404, got %d", w4.Code)
	}
}

func TestMCPConnector_DeleteCrossTenant(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	owner := buildMCPConnectorEngine(t, 42)
	w := postJSON(owner, "/mcp-connectors", map[string]any{
		"name": "x", "transport": "http", "url": "https://example.com/mcp",
	})
	id := extractIDFromResponse(t, w.Body.String())

	attacker := buildMCPConnectorEngine(t, 99)
	w2 := deleteRequest(attacker, "/mcp-connectors/"+id)
	if w2.Code != http.StatusNotFound {
		t.Errorf("cross-tenant delete should 404, got %d", w2.Code)
	}

	// Verify the row STILL exists for the owner — the
	// attacker's 404 must not have side-affected anything.
	w3 := getJSON(owner, "/mcp-connectors/"+id)
	if w3.Code != http.StatusOK {
		t.Errorf("owner row should survive attacker's failed delete, got %d", w3.Code)
	}
}

func TestMCPConnector_TestHappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildMCPConnectorEngine(t, 42)

	seenAuth := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("probe method = %s, want POST", r.Method)
		}
		if r.Header.Get("Authorization") == "Bearer k_probe_secret" {
			seenAuth = true
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"workmax-probe","result":{"protocolVersion":"2025-03-26"}}`))
	}))
	defer upstream.Close()

	w := postJSON(engine, "/mcp-connectors", map[string]any{
		"name":      "probe",
		"transport": "http",
		"url":       "https://example.com/mcp",
		"headers":   map[string]any{"Authorization": "Bearer k_probe_secret"},
	})
	id := extractIDFromResponse(t, w.Body.String())

	if err := db.Model(&model.MCPConnector{}).
		Where("uid = ? AND name = ?", 42, "probe").
		Update("url", upstream.URL).Error; err != nil {
		t.Fatalf("rewrite probe url: %v", err)
	}

	w2 := postJSON(engine, "/mcp-connectors/"+id+"/test", map[string]any{})
	if w2.Code != http.StatusOK {
		t.Fatalf("test status = %d (body=%s)", w2.Code, w2.Body.String())
	}
	if !seenAuth {
		t.Fatal("probe did not forward configured header value to upstream")
	}
	body := w2.Body.String()
	if !strings.Contains(body, `"ok":true`) {
		t.Fatalf("expected successful probe: %s", body)
	}
	if strings.Contains(body, "k_probe_secret") {
		t.Fatalf("probe response leaked plaintext secret: %s", body)
	}
}

func TestMCPConnector_TestReportsBrokenConnectorAsBodyResult(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildMCPConnectorEngine(t, 42)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer upstream.Close()

	w := postJSON(engine, "/mcp-connectors", map[string]any{
		"name":      "broken",
		"transport": "http",
		"url":       "https://example.com/mcp",
	})
	id := extractIDFromResponse(t, w.Body.String())
	if err := db.Model(&model.MCPConnector{}).
		Where("uid = ? AND name = ?", 42, "broken").
		Update("url", upstream.URL).Error; err != nil {
		t.Fatalf("rewrite probe url: %v", err)
	}

	w2 := postJSON(engine, "/mcp-connectors/"+id+"/test", map[string]any{})
	if w2.Code != http.StatusOK {
		t.Fatalf("broken connector should still return 200 body result, got %d (%s)", w2.Code, w2.Body.String())
	}
	body := w2.Body.String()
	if !strings.Contains(body, `"ok":false`) || !strings.Contains(body, "HTTP 502") {
		t.Fatalf("expected ok:false with upstream status detail: %s", body)
	}
}

func TestMCPConnector_TestCrossTenantReturns404(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	owner := buildMCPConnectorEngine(t, 42)
	w := postJSON(owner, "/mcp-connectors", map[string]any{
		"name": "x", "transport": "http", "url": "https://example.com/mcp",
	})
	id := extractIDFromResponse(t, w.Body.String())

	attacker := buildMCPConnectorEngine(t, 99)
	w2 := postJSON(attacker, "/mcp-connectors/"+id+"/test", map[string]any{})
	if w2.Code != http.StatusNotFound {
		t.Errorf("cross-tenant test should 404, got %d (body=%s)", w2.Code, w2.Body.String())
	}
}
