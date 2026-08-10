//go:build desktop

package agentruntime

import (
	"sync"
	"testing"

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
		{"result ok", Event{Kind: EventToolResult, Tool: ToolEvent{Name: "Read"}},
			"tool_result", `{"name":"Read"}`},
		{"result error", Event{Kind: EventToolResult, Tool: ToolEvent{Name: "Read", IsError: true}},
			"tool_result", `{"is_error":"true","name":"Read"}`},
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
