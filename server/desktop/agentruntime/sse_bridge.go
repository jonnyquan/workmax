//go:build desktop

package agentruntime

import (
	"encoding/json"
	"strings"

	cloudproxy "server/desktop/cloud_proxy"
)

// SSEBridge translates unified runtime events into the workmax SSE frames and
// cache writes the renderer and CacheWriter already understand. One bridge
// per turn; not safe for concurrent use (EmitFunc's contract already says
// events arrive from one goroutine).
//
// Frame shapes are byte-compatible with what local_agent emitted before this
// seam existed — the renderer, the shim's validators, and the cache
// classifier must not be able to tell that a bridge appeared in between.
type SSEBridge struct {
	dst        cloudproxy.SSEWriter
	cache      *cloudproxy.CacheWriter // may be nil (tests)
	assistant  strings.Builder
	sessionRef string
}

// NewSSEBridge wires a bridge for one turn. cache may be nil when the caller
// has no persistence (tests script the writer directly).
func NewSSEBridge(dst cloudproxy.SSEWriter, cache *cloudproxy.CacheWriter) *SSEBridge {
	return &SSEBridge{dst: dst, cache: cache}
}

// Emit implements EmitFunc. Returns the first SSE write error so the runtime
// aborts the turn when the client is gone; cache failures are non-fatal (the
// stream is the product, the cache is a copy).
func (b *SSEBridge) Emit(ev Event) error {
	switch ev.Kind {
	case EventTextDelta:
		if ev.Delta == "" {
			return nil
		}
		b.assistant.WriteString(ev.Delta)
		if b.cache != nil {
			_ = b.cache.Enqueue("text_delta", ev.Delta)
		}
		return b.dst.WriteEvent(cloudproxy.SSEEvent{
			Type: "text_delta",
			Data: mustJSON(map[string]string{"delta": ev.Delta}),
		})
	case EventThinkingDelta:
		if ev.Delta == "" {
			return nil
		}
		// New frame type: current renderers surface it as a tolerated
		// "unknown" event; the reasoning strip consumes it from R1 on.
		return b.dst.WriteEvent(cloudproxy.SSEEvent{
			Type: "reasoning_delta",
			Data: mustJSON(map[string]string{"delta": ev.Delta}),
		})
	case EventToolUse:
		payload := map[string]string{"name": ev.Tool.Name}
		if ev.Tool.Target != "" {
			payload["target"] = ev.Tool.Target
		}
		return b.dst.WriteEvent(cloudproxy.SSEEvent{
			Type: "tool_use",
			Data: mustJSON(payload),
		})
	case EventToolDenied:
		return b.dst.WriteEvent(cloudproxy.SSEEvent{
			Type: "tool_denied",
			Data: mustJSON(map[string]string{"name": ev.Tool.Name, "reason": ev.Tool.Reason}),
		})
	case EventToolResult:
		payload := map[string]string{"name": ev.Tool.Name}
		if ev.Tool.IsError {
			payload["is_error"] = "true"
		}
		return b.dst.WriteEvent(cloudproxy.SSEEvent{
			Type: "tool_result",
			Data: mustJSON(payload),
		})
	case EventApprovalReq:
		payload := map[string]string{"id": ev.ApprovalID, "name": ev.Tool.Name}
		if ev.Tool.Target != "" {
			payload["target"] = ev.Tool.Target
		}
		return b.dst.WriteEvent(cloudproxy.SSEEvent{
			Type: "approval_request",
			Data: mustJSON(payload),
		})
	case EventSessionRef:
		// Continuity bookkeeping, not narration: recorded for the caller,
		// nothing on the wire.
		b.sessionRef = ev.SessionRef
		return nil
	default:
		// A runtime that grew a kind this bridge does not know yet must not
		// kill the turn over narration.
		return nil
	}
}

// AssistantText returns the accumulated answer text, for post-turn indexing.
func (b *SSEBridge) AssistantText() string { return b.assistant.String() }

// SessionRef returns the continuity handle the runtime reported this turn
// ("" when it reported none).
func (b *SSEBridge) SessionRef() string { return b.sessionRef }

func mustJSON(v map[string]string) string {
	raw, err := json.Marshal(v)
	if err != nil {
		// A map[string]string cannot fail to marshal; belt for the braces.
		return "{}"
	}
	return string(raw)
}
