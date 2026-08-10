//go:build desktop

package local_agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	claudesdk "github.com/jonnyquan/claude-agent-sdk-go/pkg/claudesdk"
)

// A user typing their Anthropic endpoint has two reasonable spellings, and
// before this both engines could not be right at once: L1 appended /messages
// to whatever was stored, the CLI appends /v1/messages to ANTHROPIC_BASE_URL.
// Whichever the user typed, one engine 404'd.
//
// The contract now: whatever was stored, the CLI is handed the base WITHOUT
// the version segment, because that is the only base it can be given.
func TestClaudeRuntime_BaseURLSpellingIsCanonical(t *testing.T) {
	db := openTestDB(t)
	seedThreadRow(t, db)

	for _, tc := range []struct {
		name   string
		stored string
		want   string
	}{
		{"no version segment", "https://relay.example.com", "https://relay.example.com"},
		{"with version segment", "https://relay.example.com/v1", "https://relay.example.com"},
		{"with version segment and slash", "https://relay.example.com/v1/", "https://relay.example.com"},
		{"trailing slash only", "https://relay.example.com/", "https://relay.example.com"},
		{"path prefix keeps its path", "https://relay.example.com/anthropic/v1", "https://relay.example.com/anthropic"},
		// The sidecar's own loopback gateway base is already canonical and
		// must survive untouched — it is not a user-typed value.
		{"loopback gateway base", "http://127.0.0.1:8931/model-gateway/anthropic", "http://127.0.0.1:8931/model-gateway/anthropic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := NewEngine(
				stubProfile{baseURL: tc.stored, modelID: "m", apiKey: "sk"},
				db, nil, nil, "/fake/claude", t.TempDir(),
			)
			_, opts, err := scriptedTurn(t, engine, chatReqWithTurn("de305d54-75b4-431b-adb2-eb6b9e5460a1"),
				&scriptedIterator{messages: []claudesdk.Message{
					text("ok"), resultWithSession(""),
				}}, &memSSEWriter{})
			if err != nil {
				t.Fatalf("Chat: %v", err)
			}
			if got := opts.ExtraEnv["ANTHROPIC_BASE_URL"]; got != tc.want {
				t.Errorf("ANTHROPIC_BASE_URL = %q, want %q", got, tc.want)
			}
		})
	}
}

// pathStrictAnthropic answers ONLY the real Anthropic paths. Anything else is
// a 404 — which is the whole point: a stub that answers every path (like the
// other integration fakes here) cannot catch a base URL that is one segment
// off, and that is exactly the bug this guards.
type pathStrictAnthropic struct {
	inner *fakeAnthropic

	mu    sync.Mutex
	paths []string
}

func (f *pathStrictAnthropic) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.paths = append(f.paths, r.URL.Path)
	f.mu.Unlock()

	switch r.URL.Path {
	case "/v1/messages":
		f.inner.ServeHTTP(w, r)
	case "/v1/messages/count_tokens":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens":1}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// The L2 half of the spelling fix, end to end against the real CLI: a base
// URL stored WITH /v1 must still land on /v1/messages exactly once.
func TestIntegration_BaseURLWithVersionSegmentStillReachesTheCLI(t *testing.T) {
	cli := testCLIPath(t)
	db := openTestDB(t)
	seedThreadRow(t, db)

	inner := &fakeAnthropic{}
	strict := &pathStrictAnthropic{inner: inner}
	upstream := httptest.NewServer(strict)
	t.Cleanup(upstream.Close)

	root := t.TempDir()
	engine := NewEngine(
		// The user typed the version segment. Before the fix this produced
		// /v1/v1/messages and every turn died on a 404.
		stubProfile{baseURL: upstream.URL + "/v1", modelID: "fake-l2", apiKey: "sk-test"},
		db, nil, nil, cli, root,
	)
	workspace := filepath.Join(root, "thread_thr_l2")
	inner.workspace = workspace

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := engine.Chat(ctx, chatReq(), &memSSEWriter{}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "it-proof.txt")); err != nil {
		t.Fatalf("the tool loop did not run: %v", err)
	}
	strict.mu.Lock()
	defer strict.mu.Unlock()
	for _, p := range strict.paths {
		if !strings.HasPrefix(p, "/v1/messages") {
			t.Errorf("CLI reached %q; the base URL was not canonicalized", p)
		}
	}
	if len(strict.paths) == 0 {
		t.Error("the CLI never reached the endpoint")
	}
}
