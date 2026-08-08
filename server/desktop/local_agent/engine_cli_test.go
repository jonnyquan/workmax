//go:build desktop

package local_agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
)

// These tests run the real claude CLI against a scripted local Anthropic
// endpoint — the packaged shape, hermetically: no PATH discovery, no real
// credentials, no real traffic, isolated HOME. They are the repo-resident
// form of the kill-checks in l2-sdk-cli-restart-2026-08.md.
//
// Gated on WORKMAX_TEST_CLAUDE_CLI because CI has no CLI binary. Locally:
//
//	WORKMAX_TEST_CLAUDE_CLI=$HOME/.local/share/claude/versions/<ver> go test ...
func testCLIPath(t *testing.T) string {
	t.Helper()
	path := os.Getenv("WORKMAX_TEST_CLAUDE_CLI")
	if path == "" {
		t.Skip("WORKMAX_TEST_CLAUDE_CLI not set; skipping real-CLI integration test")
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("WORKMAX_TEST_CLAUDE_CLI=%q: %v", path, err)
	}
	return path
}

// fakeAnthropic scripts the Messages API: first call answers with a Write
// tool_use into the workspace, second call (carrying the tool_result) answers
// with text and end_turn. hang=true never answers, for the cancellation test.
type fakeAnthropic struct {
	workspace string
	hang      bool

	mu       sync.Mutex
	requests int
}

func sseWrite(w http.ResponseWriter, event string, data map[string]any) {
	raw, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw)
}

func (f *fakeAnthropic) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests++
	f.mu.Unlock()

	if f.hang {
		// Headers out, then silence: the turn is mid-flight when the user
		// cancels, which is exactly the state that must tear down cleanly.
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		return
	}

	var body struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	hasToolResult := false
	for _, m := range body.Messages {
		if strings.Contains(string(m.Content), "tool_result") {
			hasToolResult = true
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	sseWrite(w, "message_start", map[string]any{"type": "message_start", "message": map[string]any{
		"id": "msg_it", "type": "message", "role": "assistant", "model": "fake",
		"content": []any{}, "stop_reason": nil,
		"usage": map[string]any{"input_tokens": 1, "output_tokens": 1}}})
	if !hasToolResult {
		input, _ := json.Marshal(map[string]string{
			"file_path": filepath.Join(f.workspace, "it-proof.txt"),
			"content":   "written by the integration test tool loop",
		})
		sseWrite(w, "content_block_start", map[string]any{"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "tool_use", "id": "toolu_it", "name": "Write", "input": map[string]any{}}})
		sseWrite(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": string(input)}})
		sseWrite(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		sseWrite(w, "message_delta", map[string]any{"type": "message_delta",
			"delta": map[string]any{"stop_reason": "tool_use", "stop_sequence": nil},
			"usage": map[string]any{"output_tokens": 5}})
	} else {
		sseWrite(w, "content_block_start", map[string]any{"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "text", "text": ""}})
		sseWrite(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": "INTEGRATION-COMPLETE"}})
		sseWrite(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		sseWrite(w, "message_delta", map[string]any{"type": "message_delta",
			"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
			"usage": map[string]any{"output_tokens": 2}})
	}
	sseWrite(w, "message_stop", map[string]any{"type": "message_stop"})
}

func newCLIEngine(t *testing.T, db *gorm.DB, upstreamURL string) (*Engine, string) {
	t.Helper()
	root := t.TempDir()
	engine := NewEngine(
		stubProfile{baseURL: upstreamURL, modelID: "fake-l2", apiKey: "sk-test"},
		db, nil, nil,
		testCLIPath(t), root,
	)
	return engine, filepath.Join(root, "thread_"+"thr_l2")
}

// The whole packaged shape at once: SDK spawns the pinned CLI, env routes it
// to the scripted endpoint, the tool executes in the per-thread workspace,
// and the renderer-side stream carries tool activity, text, and done.
func TestIntegration_RealCLIToolLoop(t *testing.T) {
	db := openTestDB(t)
	seedThreadRow(t, db)

	fake := &fakeAnthropic{}
	upstream := httptest.NewServer(fake)
	t.Cleanup(upstream.Close)

	engine, workspace := newCLIEngine(t, db, upstream.URL)
	fake.workspace = workspace

	dst := &memSSEWriter{}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := engine.Chat(ctx, chatReq(), dst); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	proof, err := os.ReadFile(filepath.Join(workspace, "it-proof.txt"))
	if err != nil {
		t.Fatalf("the tool loop did not write the proof file: %v", err)
	}
	if string(proof) != "written by the integration test tool loop" {
		t.Errorf("proof content = %q", proof)
	}

	frames, perrs := dst.snapshot()
	if len(perrs) != 0 {
		t.Fatalf("proxy errors: %+v", perrs)
	}
	var kinds []string
	for _, f := range frames {
		kinds = append(kinds, f.Type)
	}
	joined := strings.Join(kinds, ",")
	if !strings.Contains(joined, "tool_use") || !strings.HasSuffix(joined, "done") {
		t.Errorf("frames = %v; want tool activity and a done terminal", kinds)
	}

	var state, aiText string
	if err := db.Raw(
		`SELECT streaming_state, COALESCE(ai_text,'') FROM w_workagent_message WHERE message_idempotency_key = ?`,
		"desktop-turn:"+testTurnUUID,
	).Row().Scan(&state, &aiText); err != nil {
		t.Fatalf("cache row: %v", err)
	}
	if state != "complete" || !strings.Contains(aiText, "INTEGRATION-COMPLETE") {
		t.Errorf("cache row = %q %q", state, aiText)
	}
}

// Cancelling a turn mid-flight must return promptly and leave no orphaned CLI
// subprocess behind — the SDK SIGTERMs, then kills. A leaked node process per
// cancelled turn would be a resource leak the user cannot see or stop.
func TestIntegration_CancellationKillsTheSubprocess(t *testing.T) {
	db := openTestDB(t)
	seedThreadRow(t, db)

	fake := &fakeAnthropic{hang: true}
	upstream := httptest.NewServer(fake)
	t.Cleanup(upstream.Close)

	engine, _ := newCLIEngine(t, db, upstream.URL)

	dst := &memSSEWriter{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- engine.Chat(ctx, chatReq(), dst) }()

	// Give the CLI time to start and reach the hanging endpoint.
	deadline := time.After(30 * time.Second)
	for {
		fake.mu.Lock()
		started := fake.requests > 0
		fake.mu.Unlock()
		if started {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("Chat returned before the endpoint was reached: %v", err)
		case <-deadline:
			t.Fatal("the CLI never reached the endpoint")
		case <-time.After(100 * time.Millisecond):
		}
	}

	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled turn must not report success")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Chat did not return within 15s of cancellation; the subprocess is likely wedged")
	}

	// Cancellation is the user's own act: whatever error surfaced internally,
	// no proxy_error bubble may reach the renderer for it.
	_, perrs := dst.snapshot()
	if len(perrs) != 0 {
		t.Errorf("cancellation surfaced proxy errors: %+v", perrs)
	}
}

// A local endpoint with no API key configured is the normal case, and the CLI
// refuses empty-key auth outright ("Not logged in"). The placeholder must
// carry the turn — and only ever to the user's own base URL.
func TestIntegration_EmptyAPIKeyStillRuns(t *testing.T) {
	db := openTestDB(t)
	seedThreadRow(t, db)

	fake := &fakeAnthropic{}
	upstream := httptest.NewServer(fake)
	t.Cleanup(upstream.Close)

	root := t.TempDir()
	engine := NewEngine(
		stubProfile{baseURL: upstream.URL, modelID: "fake-l2", apiKey: ""},
		db, nil, nil, testCLIPath(t), root,
	)
	fake.workspace = filepath.Join(root, "thread_thr_l2")

	dst := &memSSEWriter{}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := engine.Chat(ctx, chatReq(), dst); err != nil {
		t.Fatalf("Chat with empty api key: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fake.workspace, "it-proof.txt")); err != nil {
		t.Fatalf("tool loop did not run: %v", err)
	}
}

// escapeFake scripts a model that first tries to write OUTSIDE the workspace,
// then — told off by the security hook — writes inside it. The interesting
// assertions are on the filesystem: the escape file must not exist.
type escapeFake struct {
	workspace  string
	escapePath string

	mu    sync.Mutex
	calls int
}

func (f *escapeFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls++
	call := f.calls
	f.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	sseWrite(w, "message_start", map[string]any{"type": "message_start", "message": map[string]any{
		"id": "msg_esc", "type": "message", "role": "assistant", "model": "fake",
		"content": []any{}, "stop_reason": nil,
		"usage": map[string]any{"input_tokens": 1, "output_tokens": 1}}})
	switch call {
	case 1:
		writeToolUse(w, f.escapePath, "should never land")
	case 2:
		writeToolUse(w, filepath.Join(f.workspace, "inside.txt"), "landed where allowed")
	default:
		sseWrite(w, "content_block_start", map[string]any{"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "text", "text": ""}})
		sseWrite(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": "ESCAPE-HANDLED"}})
		sseWrite(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		sseWrite(w, "message_delta", map[string]any{"type": "message_delta",
			"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
			"usage": map[string]any{"output_tokens": 2}})
	}
	if call <= 2 {
		sseWrite(w, "message_delta", map[string]any{"type": "message_delta",
			"delta": map[string]any{"stop_reason": "tool_use", "stop_sequence": nil},
			"usage": map[string]any{"output_tokens": 5}})
	}
	sseWrite(w, "message_stop", map[string]any{"type": "message_stop"})
}

func writeToolUse(w http.ResponseWriter, path, content string) {
	input, _ := json.Marshal(map[string]string{"file_path": path, "content": content})
	sseWrite(w, "content_block_start", map[string]any{"type": "content_block_start", "index": 0,
		"content_block": map[string]any{"type": "tool_use", "id": "toolu_esc", "name": "Write", "input": map[string]any{}}})
	sseWrite(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0,
		"delta": map[string]any{"type": "input_json_delta", "partial_json": string(input)}})
	sseWrite(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
}

// The boundary, exercised through the whole stack: SDK → CLI → PreToolUse
// hook → deny → the model corrects course. The escape file must never touch
// the disk, the denial must be narrated on the stream, and the turn must
// still complete — a blocked tool is a corrected step, not a dead turn.
func TestIntegration_WorkspaceEscapeIsDeniedAndNarrated(t *testing.T) {
	db := openTestDB(t)
	seedThreadRow(t, db)

	escapePath := filepath.Join(t.TempDir(), "escape-proof.txt")
	fake := &escapeFake{escapePath: escapePath}
	upstream := httptest.NewServer(fake)
	t.Cleanup(upstream.Close)

	engine, workspace := newCLIEngine(t, db, upstream.URL)
	fake.workspace = workspace

	dst := &memSSEWriter{}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := engine.Chat(ctx, chatReq(), dst); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if _, err := os.Stat(escapePath); !os.IsNotExist(err) {
		t.Fatalf("the escape file reached the disk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "inside.txt")); err != nil {
		t.Errorf("the corrected in-workspace write must succeed: %v", err)
	}

	frames, _ := dst.snapshot()
	sawDenied := false
	for _, f := range frames {
		if f.Type == "tool_denied" {
			sawDenied = true
			if !strings.Contains(f.Data, "Write") {
				t.Errorf("denial payload must name the tool: %s", f.Data)
			}
		}
	}
	if !sawDenied {
		t.Error("the denial must be narrated on the stream")
	}
}
