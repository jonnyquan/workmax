package mcp_connector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"server/model"
)

// helperServer wires an http.HandlerFunc into a *httptest.Server
// and registers cleanup so the test doesn't have to. The
// returned URL is what the probe is pointed at.
func helperServer(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv.URL
}

// connector — fixture builder. URL is the only required field;
// transport defaults to HTTP since that's the path most checks
// exercise.
func connector(url, transport string) *model.MCPConnector {
	return &model.MCPConnector{
		URL:       url,
		Transport: transport,
		Name:      "test",
	}
}

func TestProbe_HTTPHappyPath_MCPEnvelope(t *testing.T) {
	url := helperServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Send back the minimal JSON-RPC envelope the probe
		// recognises as "this server speaks MCP".
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"workmax-probe","result":{"protocolVersion":"2025-03-26"}}`))
	})

	got := Probe(context.Background(), connector(url, model.MCPTransportHTTP))
	if !got.OK {
		t.Fatalf("expected OK=true; got %+v", got)
	}
}

func TestProbe_HTTPNon200IsNotOK(t *testing.T) {
	url := helperServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "auth", http.StatusUnauthorized)
	})

	got := Probe(context.Background(), connector(url, model.MCPTransportHTTP))
	if got.OK {
		t.Errorf("expected OK=false for 401 upstream; got %+v", got)
	}
	if !strings.Contains(got.Detail, "401") {
		t.Errorf("detail should mention HTTP 401; got %q", got.Detail)
	}
}

func TestProbe_HTTPNonMCPBodyIsNotOK(t *testing.T) {
	url := helperServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Misconfigured URL: returns 200 but the body is HTML
		// (e.g. the user pointed at the wrong path on a real
		// site). The probe must NOT treat this as a working MCP.
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>hi</body></html>`))
	})

	got := Probe(context.Background(), connector(url, model.MCPTransportHTTP))
	if got.OK {
		t.Errorf("expected OK=false for HTML response; got %+v", got)
	}
}

func TestProbe_HTTPSendsCustomHeaders(t *testing.T) {
	// User-configured headers must reach the upstream — otherwise
	// the probe is meaningless for any MCP server requiring API-
	// key authentication.
	var sawHeader string
	url := helperServer(t, func(w http.ResponseWriter, r *http.Request) {
		sawHeader = r.Header.Get("X-WorkMax-Test")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"x","result":{}}`))
	})

	c := connector(url, model.MCPTransportHTTP)
	// EncryptedJSONMap is a map-typed alias; constructing it
	// directly skips the at-rest encryption path (irrelevant for
	// the in-process probe test) while exercising the same
	// AsJSONMap() reader path Probe uses.
	c.Headers = model.EncryptedJSONMap{"X-WorkMax-Test": "yes"}

	got := Probe(context.Background(), c)
	if !got.OK {
		t.Fatalf("probe failed: %+v", got)
	}
	if sawHeader != "yes" {
		t.Errorf("upstream did not see X-WorkMax-Test header (got %q)", sawHeader)
	}
}

func TestProbe_SSEHappyPathContentType(t *testing.T) {
	url := helperServer(t, func(w http.ResponseWriter, r *http.Request) {
		// SSE upgrade: server promotes to text/event-stream and
		// can immediately close (the probe only needs the
		// promotion to count it as a working SSE endpoint).
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	})

	got := Probe(context.Background(), connector(url, model.MCPTransportSSE))
	if !got.OK {
		t.Errorf("expected OK=true for SSE content-type; got %+v", got)
	}
}

func TestProbe_SSEWrongContentTypeIsNotOK(t *testing.T) {
	url := helperServer(t, func(w http.ResponseWriter, r *http.Request) {
		// 200 + JSON body — the server is reachable but isn't
		// speaking SSE. Treat as a misconfigured transport pick.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hello":"world"}`))
	})

	got := Probe(context.Background(), connector(url, model.MCPTransportSSE))
	if got.OK {
		t.Errorf("SSE with JSON content-type must NOT be OK; got %+v", got)
	}
}

func TestProbe_StdioRejected(t *testing.T) {
	got := Probe(context.Background(), connector("ignored", model.MCPTransportStdio))
	if got.OK {
		t.Errorf("stdio probes must always report OK=false; got %+v", got)
	}
}

func TestProbe_UnknownTransportRejected(t *testing.T) {
	got := Probe(context.Background(), connector("ignored", "websocket"))
	if got.OK || !strings.Contains(got.Detail, "unknown transport") {
		t.Errorf("unknown transport: got %+v", got)
	}
}

func TestProbe_NilConnectorReturnsErrorShape(t *testing.T) {
	got := Probe(context.Background(), nil)
	if got.OK {
		t.Errorf("nil connector must report OK=false; got %+v", got)
	}
}

func TestProbe_EmptyURLRejected(t *testing.T) {
	got := Probe(context.Background(), connector("", model.MCPTransportHTTP))
	if got.OK || !strings.Contains(got.Detail, "url") {
		t.Errorf("empty URL must report OK=false with url message; got %+v", got)
	}
}
