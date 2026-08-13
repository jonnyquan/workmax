//go:build desktop

package agentruntime

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	cloudproxy "server/desktop/cloud_proxy"
)

type memWriter struct {
	mu     sync.Mutex
	frames []cloudproxy.SSEEvent
}

func (m *memWriter) WriteEvent(ev cloudproxy.SSEEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.frames = append(m.frames, ev)
	return nil
}
func (m *memWriter) WriteProxyError(cloudproxy.ProxyError) error { return nil }
func (m *memWriter) WriteKeepalive() error                       { return nil }

// The frame shapes are a wire contract with the shim's validators and the
// pre-bridge local_agent frames: each row pins the exact bytes.
func TestBridgeFrameShapes(t *testing.T) {
	cases := []struct {
		name     string
		ev       Event
		wantType string
		wantData string
	}{
		{"text", Event{Kind: EventTextDelta, Delta: "hi"}, "text_delta", `{"delta":"hi"}`},
		{"thinking", Event{Kind: EventThinkingDelta, Delta: "mull"}, "reasoning_delta", `{"delta":"mull"}`},
		{"tool with target", Event{Kind: EventToolUse, Tool: ToolEvent{Name: "Write", Target: "a.md"}},
			"tool_use", `{"name":"Write","target":"a.md"}`},
		{"tool without target", Event{Kind: EventToolUse, Tool: ToolEvent{Name: "Glob"}},
			"tool_use", `{"name":"Glob"}`},
		{"denied", Event{Kind: EventToolDenied, Tool: ToolEvent{Name: "Write", Reason: "outside workspace"}},
			"tool_denied", `{"name":"Write","reason":"outside workspace"}`},
		// The denial's target is what lets the renderer fold it into the step
		// it settles instead of drawing a second row for a call that ran zero
		// times.
		{"denied with target", Event{Kind: EventToolDenied, Tool: ToolEvent{Name: "Write", Target: "a.md", Reason: "用户拒绝了此操作"}},
			"tool_denied", `{"name":"Write","reason":"用户拒绝了此操作","target":"a.md"}`},
		{"result ok", Event{Kind: EventToolResult, Tool: ToolEvent{Name: "Read"}},
			"tool_result", `{"name":"Read"}`},
		{"result with target", Event{Kind: EventToolResult, Tool: ToolEvent{Name: "Write", Target: "a.md"}},
			"tool_result", `{"name":"Write","target":"a.md"}`},
		{"result error", Event{Kind: EventToolResult, Tool: ToolEvent{Name: "Read", IsError: true}},
			"tool_result", `{"is_error":"true","name":"Read"}`},
		{"turn meta", Event{Kind: EventTurnMeta, Turn: TurnMeta{Engine: "pi", Model: "qwen3-coder"}},
			"turn_meta", `{"engine":"pi","model":"qwen3-coder"}`},
		// The mind is what tells two answers apart when they share a model,
		// which is the ordinary case.
		{"turn meta with a mind", Event{Kind: EventTurnMeta, Turn: TurnMeta{Engine: "pi", Model: "m", Mind: "Payroll mind"}},
			"turn_meta", `{"engine":"pi","mind":"Payroll mind","model":"m"}`},
		{"turn meta without a mind", Event{Kind: EventTurnMeta, Turn: TurnMeta{Engine: "pi", Mind: "  "}},
			"turn_meta", `{"engine":"pi"}`},
		// No model configured means the engine chose its own default, and
		// naming a model nobody picked would be worse than saying nothing.
		{"turn meta without a model", Event{Kind: EventTurnMeta, Turn: TurnMeta{Engine: "claude"}},
			"turn_meta", `{"engine":"claude"}`},
		{"turn meta trims", Event{Kind: EventTurnMeta, Turn: TurnMeta{Engine: " claude ", Model: "  m  "}},
			"turn_meta", `{"engine":"claude","model":"m"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dst := &memWriter{}
			b := NewSSEBridge(dst, nil)
			if err := b.Emit(tc.ev); err != nil {
				t.Fatal(err)
			}
			if len(dst.frames) != 1 {
				t.Fatalf("frames = %d, want 1", len(dst.frames))
			}
			if dst.frames[0].Type != tc.wantType || dst.frames[0].Data != tc.wantData {
				t.Errorf("frame = %s %s, want %s %s",
					dst.frames[0].Type, dst.frames[0].Data, tc.wantType, tc.wantData)
			}
		})
	}
}

// Empty deltas, session refs, and unknown kinds produce no frame: silence on
// the wire, not a malformed event for the shim to reject.
func TestBridgeQuietEvents(t *testing.T) {
	dst := &memWriter{}
	b := NewSSEBridge(dst, nil)
	for _, ev := range []Event{
		{Kind: EventTextDelta, Delta: ""},
		{Kind: EventThinkingDelta, Delta: ""},
		{Kind: EventSessionRef, SessionRef: "sess-1"},
		// An engine with no name is not a fact worth putting on the wire.
		{Kind: EventTurnMeta, Turn: TurnMeta{Model: "m"}},
		{Kind: EventTurnMeta, Turn: TurnMeta{Engine: "   "}},
		{Kind: EventKind("from_the_future")},
	} {
		if err := b.Emit(ev); err != nil {
			t.Fatal(err)
		}
	}
	if len(dst.frames) != 0 {
		t.Errorf("quiet events wrote %d frames: %v", len(dst.frames), dst.frames)
	}
	if b.SessionRef() != "sess-1" {
		t.Errorf("SessionRef = %q, want sess-1", b.SessionRef())
	}
}

// AssistantText accumulates exactly the text deltas, for post-turn indexing.
func TestBridgeAssistantAccumulation(t *testing.T) {
	b := NewSSEBridge(&memWriter{}, nil)
	for _, d := range []string{"he", "", "llo"} {
		if err := b.Emit(Event{Kind: EventTextDelta, Delta: d}); err != nil {
			t.Fatal(err)
		}
	}
	_ = b.Emit(Event{Kind: EventToolUse, Tool: ToolEvent{Name: "Read"}})
	if got := b.AssistantText(); got != "hello" {
		t.Errorf("AssistantText = %q, want hello", got)
	}
}

// The model id is whatever the user typed into settings, so the bridge bounds
// it rather than trusting it. Truncation is by rune: cutting a multi-byte
// character in half would put invalid UTF-8 on a wire whose other end parses
// JSON strictly, and the far end is a webview.
func TestBridgeBoundsTurnMeta(t *testing.T) {
	dst := &memWriter{}
	b := NewSSEBridge(dst, nil)
	if err := b.Emit(Event{Kind: EventTurnMeta, Turn: TurnMeta{
		Engine: strings.Repeat("e", 100),
		Model:  strings.Repeat("模", 200),
	}}); err != nil {
		t.Fatal(err)
	}
	if len(dst.frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(dst.frames))
	}
	var got struct {
		Engine string `json:"engine"`
		Model  string `json:"model"`
	}
	if err := json.Unmarshal([]byte(dst.frames[0].Data), &got); err != nil {
		t.Fatalf("the bounded frame must still be valid JSON: %v (%s)", err, dst.frames[0].Data)
	}
	if len([]rune(got.Engine)) != 32 {
		t.Errorf("engine runes = %d, want 32", len([]rune(got.Engine)))
	}
	if len([]rune(got.Model)) != 80 {
		t.Errorf("model runes = %d, want 80", len([]rune(got.Model)))
	}
	if !utf8.ValidString(got.Model) {
		t.Error("truncation must not split a rune")
	}
}
