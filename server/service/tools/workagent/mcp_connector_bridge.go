package workagent

// mcp_connector_bridge.go — Phase B2. Turns the per-uid
// MCPConnector registry rows into the SDK's McpServerConfig
// variants and exposes a single helper agent_processor folds
// into the WithMcpServers option alongside the in-process
// production-tools server.
//
// Failure isolation contract: one malformed / unsupported
// connector row MUST NOT block other connectors or block the
// turn. The builder logs and skips bad rows; the SDK only sees
// the well-formed subset.
//
// The map key (server name) is the user's `name` field. Two
// connectors with the same name would collide in WithMcpServers'
// map; we de-dupe by keeping the first (newest, since
// ListEnabledForOwner orders by updated_at DESC) and logging
// the rest.

import (
	"fmt"
	"strings"

	claudecode "github.com/jonnyquan/claude-agent-sdk-go"
	claudesdk "github.com/jonnyquan/claude-agent-sdk-go/pkg/claudesdk"

	"server/globals"
	"server/model"
	"server/service/mcp_connector"
)

// BuildExternalMcpServers reads every enabled connector for uid
// and returns the SDK-shaped map ready to pass into
// claudecode.WithMcpServers.
//
// On any DB / read failure: returns nil + the error so the
// caller can decide whether to fail the turn or continue
// without external connectors. Per-row failures (bad transport,
// duplicate name, malformed args/env) skip silently — the agent
// shouldn't lose the whole turn because one broken connector
// row sits in the registry.
//
// Returns (nil, nil) when the user has no enabled connectors
// — distinct from the error path. Caller checks len(result)
// rather than a separate sentinel.
func BuildExternalMcpServers(uid uint) (map[string]claudecode.McpServerConfig, error) {
	if uid == 0 {
		return nil, nil
	}
	rows, err := mcp_connector.Default().ListEnabledForOwner(uid)
	if err != nil {
		return nil, fmt.Errorf("load mcp connectors: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}

	out := make(map[string]claudecode.McpServerConfig, len(rows))
	skipped := 0
	for i := range rows {
		row := &rows[i]
		name := strings.TrimSpace(row.Name)
		if name == "" {
			globals.Warn(fmt.Sprintf("[MCPConnector] uid=%d id=%d skipped: empty name", uid, row.Id))
			skipped++
			continue
		}
		if _, collision := out[name]; collision {
			// Duplicate names — keep the first (newest), skip
			// the rest. The user can rename one or delete one
			// from the UI later.
			globals.Warn(fmt.Sprintf("[MCPConnector] uid=%d id=%d skipped: duplicate name %q", uid, row.Id, name))
			skipped++
			continue
		}
		cfg, err := connectorToConfig(row)
		if err != nil {
			globals.Warn(fmt.Sprintf("[MCPConnector] uid=%d id=%d skipped: %v", uid, row.Id, err))
			skipped++
			continue
		}
		out[name] = cfg
	}
	if len(out) == 0 {
		if skipped > 0 {
			return nil, fmt.Errorf("no usable enabled MCP connectors (skipped %d)", skipped)
		}
		return nil, nil
	}
	return out, nil
}

// connectorToConfig is the per-row transport switch. Args is a
// plain JSONMap on the model; Env / Headers are EncryptedJSONMap
// (transparently decrypted by the field's Scan() before we get
// here). The AsJSONMap() cast keeps the stringification helpers
// agnostic of the encrypted-field distinction.
func connectorToConfig(c *model.MCPConnector) (claudecode.McpServerConfig, error) {
	switch c.Transport {
	case model.MCPTransportStdio:
		return nil, fmt.Errorf("stdio transport is disabled for user-managed connectors")
	case model.MCPTransportSSE:
		url := strings.TrimSpace(c.URL)
		if url == "" {
			return nil, fmt.Errorf("sse: empty url")
		}
		if err := mcp_connector.ValidateRemoteConnectorURL(url); err != nil {
			return nil, err
		}
		return &claudesdk.McpSSEServerConfig{
			Type:    claudesdk.McpServerTypeSSE,
			URL:     url,
			Headers: jsonMapToStringMap(c.Headers.AsJSONMap()),
		}, nil
	case model.MCPTransportHTTP:
		url := strings.TrimSpace(c.URL)
		if url == "" {
			return nil, fmt.Errorf("http: empty url")
		}
		if err := mcp_connector.ValidateRemoteConnectorURL(url); err != nil {
			return nil, err
		}
		return &claudesdk.McpHTTPServerConfig{
			Type:    claudesdk.McpServerTypeHTTP,
			URL:     url,
			Headers: jsonMapToStringMap(c.Headers.AsJSONMap()),
		}, nil
	default:
		return nil, fmt.Errorf("unknown transport %q", c.Transport)
	}
}

// jsonMapToStringSlice — Args is stored as a JSON column; the
// model.JSONMap type uses interface{} values. The stdio
// transport's Args is []string, so we flatten via fmt.Sprint to
// stringify whatever JSON value the user persisted (number,
// bool, etc.). Keys are dropped — Args is an ordered list-of-
// strings semantically, but JSON object iteration order isn't
// stable in Go's encoding/json; we look for a top-level
// "argv" key holding a list (the recommended shape), and fall
// back to a deterministic-but-arbitrary key sort otherwise.
//
// Practical advice for users: store args as
//
//	{ "argv": ["--port", "8080", "--verbose"] }
//
// so this helper can pick the list cleanly.
func jsonMapToStringSlice(m model.JSONMap) []string {
	if len(m) == 0 {
		return nil
	}
	if v, ok := m["argv"]; ok {
		if list, ok := v.([]interface{}); ok {
			out := make([]string, 0, len(list))
			for _, item := range list {
				out = append(out, fmt.Sprint(item))
			}
			return out
		}
	}
	// Fall back: collect keys in deterministic order. Users
	// who want explicit ordering should use "argv".
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStringsAscending(keys)
	out := make([]string, 0, len(keys)*2)
	for _, k := range keys {
		out = append(out, k, fmt.Sprint(m[k]))
	}
	return out
}

// jsonMapToStringMap flattens model.JSONMap (map[string]any) to
// map[string]string. Non-string values stringify via fmt.Sprint
// so a user that persists a number / bool / nested object still
// produces a string-valued env / headers entry.
func jsonMapToStringMap(m model.JSONMap) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = fmt.Sprint(v)
	}
	return out
}

// sortStringsAscending — local copy so the package doesn't have
// to import sort just for this tiny path.
func sortStringsAscending(s []string) {
	// Insertion sort — keys count is tiny (single digits in
	// practice). Keeps the dep surface clean.
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
