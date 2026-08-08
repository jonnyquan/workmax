//go:build desktop

package cloud_proxy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"server/desktop/buildinfo"
)

// TestProxy_UpstreamRequestContract pins the exact shape of the
// HTTP request the sidecar sends to the cloud for /agent/chat. The
// existing happy-path test in proxy_test.go covers SSE wiring +
// cache writing; this one is focused narrowly on the request itself
// so a regression in path / verb / headers / body framing trips a
// dedicated assertion instead of getting buried in a chat-flow
// failure message.
//
// Companion to wire_contract_test.go which goes the other
// direction (cloud → sidecar response shapes). Together they pin
// both halves of the sidecar↔cloud contract.
func TestProxy_UpstreamRequestContract(t *testing.T) {
	var (
		gotPath          atomic.Value // string
		gotMethod        atomic.Value // string
		gotAuth          atomic.Value // string
		gotContentType   atomic.Value // string
		gotAccept        atomic.Value // string
		gotClient        atomic.Value // string
		gotClientVersion atomic.Value // string
		gotRequestID     atomic.Value // string
		gotBody          atomic.Value // []byte
	)

	proxy, dst, _, _ := newProxyTestFixture(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotPath.Store(r.URL.Path)
		gotMethod.Store(r.Method)
		gotAuth.Store(r.Header.Get("Authorization"))
		gotContentType.Store(r.Header.Get("Content-Type"))
		gotAccept.Store(r.Header.Get("Accept"))
		gotClient.Store(r.Header.Get("X-WorkMax-Client"))
		gotClientVersion.Store(r.Header.Get("X-WorkMax-Client-Version"))
		gotRequestID.Store(r.Header.Get("X-Agent-Request-Id"))
		gotBody.Store(body)

		// Minimal SSE so the relay completes happy-path bookkeeping.
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		sseLine(w, "event: done")
		sseLine(w, `data: {}`)
		sseLine(w, "")
		w.(http.Flusher).Flush()
	})

	bodyIn := []byte(`{"text":"hello"}`)
	if err := proxy.Chat(context.Background(), ChatRequest{
		ThreadID:   1,
		ThreadUUID: "thr_contract",
		TurnUUID:   proxyTestTurnUUID,
		UID:        proxyTestUID,
		UserText:   "hello",
		ChatMode:   "ppt",
		Body:       bodyIn,
	}, dst); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if got, _ := gotPath.Load().(string); got != CloudRouteChatAgent {
		t.Errorf("path: got %q, want %q", got, CloudRouteChatAgent)
	}
	if got, _ := gotMethod.Load().(string); got != http.MethodPost {
		t.Errorf("method: got %q, want POST", got)
	}
	if got, _ := gotAuth.Load().(string); !strings.HasPrefix(got, "Bearer ") || got == "Bearer " {
		t.Errorf("Authorization: got %q (want Bearer <token>)", got)
	}
	if got, _ := gotContentType.Load().(string); got != "application/json" {
		t.Errorf("Content-Type: got %q", got)
	}
	if got, _ := gotAccept.Load().(string); got != "text/event-stream" {
		t.Errorf("Accept: got %q (cloud needs to stream)", got)
	}
	if got, _ := gotClient.Load().(string); got != "desktop" {
		t.Errorf("X-WorkMax-Client: got %q", got)
	}
	if got, _ := gotClientVersion.Load().(string); got != buildinfo.Version {
		t.Errorf("X-WorkMax-Client-Version: got %q, want %q (from buildinfo)", got, buildinfo.Version)
	}
	if got, _ := gotRequestID.Load().(string); got != "desktop-turn:"+proxyTestTurnUUID {
		t.Errorf("X-Agent-Request-Id: got %q, want stable turn idempotency key", got)
	}
	if got, _ := gotBody.Load().([]byte); string(got) != string(bodyIn) {
		t.Errorf("body: got %q, want %q", got, bodyIn)
	}
}

// TestProxy_UpstreamRequest_EmptyBodyFallback pins the specific
// edge case from buildUpstreamRequest: a nil Body falls back to
// "{}" rather than a literally empty body, because the cloud
// JSON parser would otherwise reject the request before any
// useful error message reaches the renderer.
func TestProxy_UpstreamRequest_EmptyBodyFallback(t *testing.T) {
	var gotBody atomic.Value
	proxy, dst, _, _ := newProxyTestFixture(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody.Store(body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		sseLine(w, "event: done")
		sseLine(w, `data: {}`)
		sseLine(w, "")
		w.(http.Flusher).Flush()
	})

	if err := proxy.Chat(context.Background(), ChatRequest{
		ThreadID:   1,
		ThreadUUID: "thr_empty",
		TurnUUID:   proxyTestTurnUUID,
		UID:        proxyTestUID,
		UserText:   "x",
		ChatMode:   "ppt",
		Body:       nil, // <- the case under test
	}, dst); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if got, _ := gotBody.Load().([]byte); string(got) != "{}" {
		t.Errorf("empty-body fallback: got %q, want \"{}\"", got)
	}
}
