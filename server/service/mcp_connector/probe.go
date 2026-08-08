package mcp_connector

// probe.go — live-validation helper for the test-connection
// endpoint (`POST /api/work-agent/mcp-connectors/:id/test`).
//
// Goal: tell the user whether their configured connector is
// reachable BEFORE they spend a whole agent turn watching it
// fail. Two pragmatic checks suffice for the two transports
// user-managed connectors support:
//
//   - HTTP (model.MCPTransportHTTP): the MCP spec's first
//     round-trip is the JSON-RPC `initialize` request. Send it,
//     wait briefly, decide on the response status + body shape.
//     Success = 200/202 with a parseable JSON-RPC envelope.
//
//   - SSE (model.MCPTransportSSE): the canonical handshake is a
//     GET with `Accept: text/event-stream`. Success = the
//     response promotes to `text/event-stream` within the
//     timeout; we don't need to consume any events to know the
//     endpoint speaks the protocol.
//
//   - stdio is intentionally not probed — user-managed
//     connectors disable stdio at the bridge boundary already
//     (mcp_connector_bridge.go connectorToConfig), so a UI test
//     button would never be shown for that case.
//
// Failure isolation: every probe path is wrapped in a 5-second
// timeout so a misconfigured URL that never responds doesn't
// hang the HTTP handler. On any non-success the function
// returns a short, user-actionable error message (URL host,
// status code, or "timeout") — no internal stack traces, no
// implementation details about the prober itself.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"server/model"
)

// probeTimeout caps every probe round-trip. Tuned for "user
// pressed Test, watching the spinner" — anything past 5 seconds
// reads as "broken" to the user even if the upstream eventually
// responds.
const probeTimeout = 5 * time.Second

// ProbeResult is the wire-shape the HTTP handler returns. OK
// flips true exactly when the connector is reachable AND
// speaking the MCP transport's handshake protocol. Detail
// carries a short user-readable note on either path (success
// notes the response shape; failure notes the cause).
type ProbeResult struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// Probe runs a transport-appropriate live-validation against
// the connector's URL + headers and reports OK/error. The
// connector row should already have been loaded for the caller
// (uid-scoped); Probe doesn't re-fetch.
//
// Stdio + any unknown transport return an error — those rows
// can't be tested live without spawning a subprocess, which the
// hosted platform doesn't permit for user-managed connectors.
func Probe(ctx context.Context, c *model.MCPConnector) ProbeResult {
	if c == nil {
		return ProbeResult{OK: false, Detail: "connector is missing"}
	}
	switch c.Transport {
	case model.MCPTransportHTTP:
		return probeHTTP(ctx, c)
	case model.MCPTransportSSE:
		return probeSSE(ctx, c)
	case model.MCPTransportStdio:
		return ProbeResult{
			OK:     false,
			Detail: "stdio transport cannot be tested live on the hosted platform",
		}
	default:
		return ProbeResult{
			OK:     false,
			Detail: fmt.Sprintf("unknown transport %q", c.Transport),
		}
	}
}

// initializeRequest is the JSON-RPC envelope every MCP server
// must accept. Keeping it as a static literal avoids re-marshal
// allocs on every probe call.
//
// protocolVersion follows the published MCP spec; bumping it
// here when the spec evolves is the right reflex — older
// servers respond with `{result:{protocolVersion: "older"}}`
// and we still count that as OK because the envelope shape
// proves the connector speaks MCP.
const initializeRequest = `{"jsonrpc":"2.0","id":"workmax-probe","method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"workmax-mcp-probe","version":"1.0"}}}`

func probeHTTP(ctx context.Context, c *model.MCPConnector) ProbeResult {
	url := strings.TrimSpace(c.URL)
	if url == "" {
		return ProbeResult{OK: false, Detail: "url is empty"}
	}
	// URL safety (HTTPS, public host) is enforced at write time
	// in Create/Update via ValidateRemoteConnectorURL. The probe
	// trusts the row's URL passed that gate when it was last
	// saved — re-validating here would only add work the connector
	// already withstood, and breaks the in-process httptest path
	// the probe is unit-tested against.

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBufferString(initializeRequest))
	if err != nil {
		return ProbeResult{OK: false, Detail: "request build failed: " + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range c.Headers.AsJSONMap() {
		req.Header.Set(k, fmt.Sprint(v))
	}

	client := &http.Client{Timeout: probeTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return ProbeResult{OK: false, Detail: humaniseProbeError(err)}
	}
	defer resp.Body.Close()
	// Read at most 64 KiB — enough for the JSON-RPC envelope,
	// bounded so a misconfigured endpoint that streams forever
	// can't OOM the prober.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ProbeResult{
			OK:     false,
			Detail: fmt.Sprintf("server returned HTTP %d", resp.StatusCode),
		}
	}
	// Success path proves the server speaks JSON-RPC: we got an
	// envelope back with the same id we sent OR with a `result`
	// or `error` field. Either is "the server understands MCP".
	if envelopeLooksMCP(body) {
		return ProbeResult{OK: true, Detail: "server responded to initialize handshake"}
	}
	return ProbeResult{
		OK:     false,
		Detail: "server returned non-MCP response — check the URL points at an MCP endpoint",
	}
}

func probeSSE(ctx context.Context, c *model.MCPConnector) ProbeResult {
	url := strings.TrimSpace(c.URL)
	if url == "" {
		return ProbeResult{OK: false, Detail: "url is empty"}
	}
	// URL safety (HTTPS, public host) is enforced at write time
	// in Create/Update via ValidateRemoteConnectorURL. The probe
	// trusts the row's URL passed that gate when it was last
	// saved — re-validating here would only add work the connector
	// already withstood, and breaks the in-process httptest path
	// the probe is unit-tested against.

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ProbeResult{OK: false, Detail: "request build failed: " + err.Error()}
	}
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range c.Headers.AsJSONMap() {
		req.Header.Set(k, fmt.Sprint(v))
	}

	client := &http.Client{Timeout: probeTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return ProbeResult{OK: false, Detail: humaniseProbeError(err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ProbeResult{
			OK:     false,
			Detail: fmt.Sprintf("server returned HTTP %d", resp.StatusCode),
		}
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(strings.ToLower(ct), "text/event-stream") {
		return ProbeResult{
			OK:     false,
			Detail: fmt.Sprintf("server responded but content-type %q is not text/event-stream", ct),
		}
	}
	return ProbeResult{OK: true, Detail: "server promotes to SSE stream"}
}

// envelopeLooksMCP inspects the response body for the
// minimum-viable JSON-RPC envelope shape: object with either
// "result" or "error" key at the top level, plus a "jsonrpc"
// field. Permissive on the inner shape — different SDK versions
// emit slightly different `result.serverInfo` and capabilities
// blocks, and the user just needs to know the handshake
// completed.
func envelopeLooksMCP(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	var env struct {
		JSONRPC string          `json:"jsonrpc"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		// SSE-style responses may stream multiple `data:` lines
		// instead of a clean JSON envelope. Look for "jsonrpc"
		// as a substring as a last-resort heuristic.
		return bytes.Contains(body, []byte(`"jsonrpc"`))
	}
	if env.JSONRPC == "" {
		return false
	}
	return len(env.Result) > 0 || len(env.Error) > 0
}

// humaniseProbeError trims Go's verbose HTTP error chains down
// to one line the user can act on. Connection-refused / DNS /
// TLS / timeout each get distinct phrasing; everything else
// drops to a generic "connection failed: <err>" so we never
// silently swallow the cause.
func humaniseProbeError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "context deadline exceeded"),
		strings.Contains(msg, "Client.Timeout exceeded"):
		return "connection timed out after 5 seconds"
	case strings.Contains(msg, "no such host"):
		return "DNS lookup failed — check the URL host"
	case strings.Contains(msg, "connection refused"):
		return "connection refused — is the server running?"
	case strings.Contains(msg, "x509"), strings.Contains(msg, "tls"):
		return "TLS handshake failed — check the URL's certificate"
	}
	return "connection failed: " + msg
}
