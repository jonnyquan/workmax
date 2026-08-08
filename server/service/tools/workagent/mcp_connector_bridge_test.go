package workagent

// mcp_connector_bridge_test.go — Phase B2 tests. Covers the
// connector → SDK config translation + the failure-isolation
// contract (one bad row skips, others survive).

import (
	"testing"

	claudesdk "github.com/jonnyquan/claude-agent-sdk-go/pkg/claudesdk"

	"server/model"
	"server/service/mcp_connector"
	"server/utils/testutil"

	"gorm.io/gorm"
)

func seedTestConnector(t *testing.T, db *gorm.DB, c *model.MCPConnector) *model.MCPConnector {
	t.Helper()
	repo := mcp_connector.NewRepository(db)
	if err := repo.Create(c); err != nil {
		t.Fatalf("seed connector: %v", err)
	}
	return c
}

func TestBuildExternalMcpServers_NoEnabledRows(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	got, err := BuildExternalMcpServers(42)
	if err != nil {
		t.Fatalf("BuildExternalMcpServers: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 servers, got %d", len(got))
	}
}

func TestBuildExternalMcpServers_StdioRowsSkipped(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	seedTestConnector(t, db, &model.MCPConnector{
		UID:       42,
		Name:      "linear",
		Transport: model.MCPTransportStdio,
		Command:   "/usr/bin/mcp-linear",
		Args:      model.JSONMap{"argv": []interface{}{"--api-key", "secret"}},
		Env:       model.EncryptedJSONMap{"LOG_LEVEL": "info"},
		Enabled:   true,
	})

	got, err := BuildExternalMcpServers(42)
	if err == nil {
		t.Fatal("expected all-skipped stdio connector to return an error")
	}
	if len(got) != 0 {
		t.Fatalf("stdio user-managed connectors must be skipped; got keys %v", keysOf(got))
	}
}

func TestBuildExternalMcpServers_HTTPRoundTrip(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	seedTestConnector(t, db, &model.MCPConnector{
		UID:       42,
		Name:      "remote-tools",
		Transport: model.MCPTransportHTTP,
		URL:       "https://example.test/mcp",
		Headers:   model.EncryptedJSONMap{"Authorization": "Bearer abc123"},
		Enabled:   true,
	})

	got, _ := BuildExternalMcpServers(42)
	httpCfg, ok := got["remote-tools"].(*claudesdk.McpHTTPServerConfig)
	if !ok {
		t.Fatalf("expected *McpHTTPServerConfig, got %T", got["remote-tools"])
	}
	if httpCfg.URL != "https://example.test/mcp" {
		t.Errorf("url lost: %q", httpCfg.URL)
	}
	if httpCfg.Headers["Authorization"] != "Bearer abc123" {
		t.Errorf("header lost: %v", httpCfg.Headers)
	}
}

func TestBuildExternalMcpServers_SSERoundTrip(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	seedTestConnector(t, db, &model.MCPConnector{
		UID:       42,
		Name:      "events",
		Transport: model.MCPTransportSSE,
		URL:       "https://example.test/sse",
		Enabled:   true,
	})

	got, _ := BuildExternalMcpServers(42)
	if _, ok := got["events"].(*claudesdk.McpSSEServerConfig); !ok {
		t.Errorf("expected *McpSSEServerConfig, got %T", got["events"])
	}
}

func TestBuildExternalMcpServers_UnsafeRemoteRowsSkipped(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	seedTestConnector(t, db, &model.MCPConnector{
		UID:       42,
		Name:      "metadata",
		Transport: model.MCPTransportHTTP,
		URL:       "https://169.254.169.254/latest/meta-data",
		Enabled:   true,
	})

	got, err := BuildExternalMcpServers(42)
	if err == nil {
		t.Fatal("expected all-skipped unsafe remote connector to return an error")
	}
	if len(got) != 0 {
		t.Fatalf("unsafe remote connector must be skipped; got keys %v", keysOf(got))
	}
}

func TestBuildExternalMcpServers_DisabledRowsSkipped(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	repo := mcp_connector.NewRepository(db)
	seedTestConnector(t, db, &model.MCPConnector{
		UID: 42, Name: "enabled", Transport: model.MCPTransportHTTP,
		URL: "https://example.test/enabled", Enabled: true,
	})
	disabled := seedTestConnector(t, db, &model.MCPConnector{
		UID: 42, Name: "disabled", Transport: model.MCPTransportHTTP,
		URL: "https://example.test/disabled", Enabled: true,
	})
	if err := repo.SetEnabled(disabled.Id, 42, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}

	got, _ := BuildExternalMcpServers(42)
	if _, ok := got["disabled"]; ok {
		t.Error("disabled connector should not appear in map")
	}
	if _, ok := got["enabled"]; !ok {
		t.Error("enabled connector missing")
	}
}

func TestBuildExternalMcpServers_ScopedByUID(t *testing.T) {
	// Cross-tenant guard at the bridge layer mirrors the repo's
	// IDOR posture: uid=99 must never see uid=42's connectors.
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	seedTestConnector(t, db, &model.MCPConnector{
		UID: 42, Name: "private", Transport: model.MCPTransportHTTP,
		URL: "https://example.test/private", Enabled: true,
	})

	got, _ := BuildExternalMcpServers(99)
	if len(got) != 0 {
		t.Errorf("cross-tenant leak; got %d connectors", len(got))
	}
}

func TestBuildExternalMcpServers_ZeroUIDReturnsNil(t *testing.T) {
	got, err := BuildExternalMcpServers(0)
	if err != nil {
		t.Errorf("uid=0 should not error: %v", err)
	}
	if got != nil {
		t.Errorf("uid=0 should return nil, got %v", got)
	}
}

func TestJsonMapToStringSlice_PrefersArgv(t *testing.T) {
	got := jsonMapToStringSlice(model.JSONMap{
		"argv": []interface{}{"--port", "8080", true, 42},
	})
	want := []string{"--port", "8080", "true", "42"}
	if len(got) != len(want) {
		t.Fatalf("len=%d, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("[%d] got %q, want %q", i, got[i], w)
		}
	}
}

func TestJsonMapToStringSlice_FallsBackToSortedKeys(t *testing.T) {
	// Without "argv", flatten to key,value,... sorted by key for
	// determinism.
	got := jsonMapToStringSlice(model.JSONMap{"b": "2", "a": "1"})
	want := []string{"a", "1", "b", "2"}
	if len(got) != len(want) {
		t.Fatalf("len=%d, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("[%d] got %q, want %q", i, got[i], w)
		}
	}
}

func TestJsonMapToStringSlice_Empty(t *testing.T) {
	if got := jsonMapToStringSlice(nil); got != nil {
		t.Errorf("nil input should return nil, got %v", got)
	}
	if got := jsonMapToStringSlice(model.JSONMap{}); got != nil {
		t.Errorf("empty input should return nil, got %v", got)
	}
}

func TestJsonMapToStringMap_StringifiesValues(t *testing.T) {
	got := jsonMapToStringMap(model.JSONMap{
		"INT":    42,
		"BOOL":   true,
		"STRING": "raw",
	})
	if got["INT"] != "42" {
		t.Errorf("int not stringified: %q", got["INT"])
	}
	if got["BOOL"] != "true" {
		t.Errorf("bool not stringified: %q", got["BOOL"])
	}
	if got["STRING"] != "raw" {
		t.Errorf("string not preserved: %q", got["STRING"])
	}
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
