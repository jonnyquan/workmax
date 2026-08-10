//go:build desktop

package local_inference

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	cloudproxy "server/desktop/cloud_proxy"
)

func TestAnthropicBaseURL(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://relay.example.com", "https://relay.example.com"},
		{"https://relay.example.com/", "https://relay.example.com"},
		{"https://relay.example.com/v1", "https://relay.example.com"},
		{"https://relay.example.com/v1/", "https://relay.example.com"},
		{"  https://relay.example.com/v1  ", "https://relay.example.com"},
		{"https://relay.example.com/anthropic/v1", "https://relay.example.com/anthropic"},
		// Only an exact `v1` segment is a version segment.
		{"https://relay.example.com/v1beta", "https://relay.example.com/v1beta"},
		{"http://127.0.0.1:8931/model-gateway/anthropic", "http://127.0.0.1:8931/model-gateway/anthropic"},
	} {
		if got := AnthropicBaseURL(tc.in); got != tc.want {
			t.Errorf("AnthropicBaseURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// anthropicPathStrict answers /v1/messages and 404s everything else, so a
// request that lands one segment off fails the way a real endpoint would.
type anthropicPathStrict struct {
	mu    sync.Mutex
	paths []string
}

func (s *anthropicPathStrict) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.paths = append(s.paths, r.URL.Path)
	s.mu.Unlock()

	if r.URL.Path != "/v1/messages" {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"type":"error","error":{"type":"not_found_error","message":"no such route"}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	sseLine(w, "event: content_block_delta")
	sseLine(w, `data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello"}}`)
	sseLine(w, "")
	sseLine(w, "event: message_stop")
	sseLine(w, `data: {"type":"message_stop"}`)
	sseLine(w, "")
}

// L1's half of the spelling fix: whichever way the user spelled their
// Anthropic base URL, the request must land on /v1/messages. Before the fix
// the "with /v1" spelling was the only one that worked here — and it was the
// one that broke the claude CLI.
func TestEngine_AnthropicBaseURLSpellingsBothReachMessages(t *testing.T) {
	for _, suffix := range []string{"", "/", "/v1", "/v1/"} {
		t.Run("base"+strings.ReplaceAll(suffix, "/", "_"), func(t *testing.T) {
			strict := &anthropicPathStrict{}
			upstream := httptest.NewServer(strict)
			t.Cleanup(upstream.Close)

			engine, _, dst := newTestEngine(t, stubProfile{
				protocol: protocolAnthropic,
				baseURL:  upstream.URL + suffix,
				modelID:  engineTestModel,
			})
			if err := engine.Chat(context.Background(), cloudproxy.ChatRequest{
				ThreadID: 1, ThreadUUID: "thr_1", TurnUUID: engineTestTurnUUID,
				UID: engineTestUID, UserText: "hi", ChatMode: "general",
			}, dst); err != nil {
				t.Fatalf("Chat with base %q: %v", upstream.URL+suffix, err)
			}

			strict.mu.Lock()
			paths := append([]string(nil), strict.paths...)
			strict.mu.Unlock()
			if len(paths) != 1 || paths[0] != "/v1/messages" {
				t.Fatalf("paths = %v, want exactly [/v1/messages]", paths)
			}

			frames, perrs := dst.snapshot()
			if len(perrs) != 0 {
				t.Fatalf("proxy errors: %+v", perrs)
			}
			joined := ""
			for _, f := range frames {
				joined += f.Data
			}
			if !strings.Contains(joined, "Hello") {
				t.Errorf("stream did not carry the answer: %v", frames)
			}
		})
	}
}

// A 404 from a self-hosted endpoint is a configuration problem, and the error
// must be diagnosable: it names the URL we actually asked for.
func TestEngine_NotFoundNamesTheEndpoint(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(upstream.Close)

	engine, _, dst := newTestEngine(t, stubProfile{
		protocol: protocolAnthropic,
		baseURL:  upstream.URL,
		modelID:  engineTestModel,
	})
	err := engine.Chat(context.Background(), cloudproxy.ChatRequest{
		ThreadID: 1, ThreadUUID: "thr_1", TurnUUID: engineTestTurnUUID,
		UID: engineTestUID, UserText: "hi", ChatMode: "general",
	}, dst)
	if err == nil {
		t.Fatal("a 404 must fail the turn")
	}
	_, perrs := dst.snapshot()
	if len(perrs) != 1 {
		t.Fatalf("proxy errors = %+v", perrs)
	}
	if !strings.Contains(perrs[0].Message, "404") {
		t.Errorf("message must name the status: %q", perrs[0].Message)
	}
	got, _ := perrs[0].Details["local_endpoint"].(string)
	if got != upstream.URL+"/v1/messages" {
		t.Errorf("local_endpoint = %q, want %q", got, upstream.URL+"/v1/messages")
	}
}
