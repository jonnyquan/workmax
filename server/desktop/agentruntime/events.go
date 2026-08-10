//go:build desktop

package agentruntime

// EventKind discriminates Event. The vocabulary is the union of what both
// engines can produce, mapped from their native streams:
//
//	Claude SDK            Pi RPC                     unified
//	--------------------  -------------------------  -----------------
//	TextBlock / partial   message_update.text_delta  EventTextDelta
//	thinking delta        thinking_delta             EventThinkingDelta
//	ToolUseBlock          toolcall_end               EventToolUse
//	PreToolUse deny       tool_call block            EventToolDenied
//	tool_result           tool_execution_end         EventToolResult
//	(canUseTool, R1)      extension_ui_request       EventApprovalRequest
//	session_id            session file path          EventSessionRef
//
// Kinds an engine cannot produce are simply never emitted; the SSE bridge
// forwards unknown-to-the-renderer kinds as new frame types, which the shim
// surfaces as tolerated "unknown" events on old renderers (shim.js keeps a
// stream with new event names alive rather than dropping it).
type EventKind string

const (
	EventTextDelta     EventKind = "text_delta"
	EventThinkingDelta EventKind = "thinking_delta"
	EventToolUse       EventKind = "tool_use"
	EventToolDenied    EventKind = "tool_denied"
	EventToolResult    EventKind = "tool_result"
	EventSessionRef    EventKind = "session_ref"
	EventApprovalReq   EventKind = "approval_request"
)

// Event is one unified runtime event. Exactly the fields for its Kind are
// set; the rest stay zero.
type Event struct {
	Kind EventKind

	// Delta carries EventTextDelta / EventThinkingDelta text.
	Delta string

	// Tool carries EventToolUse / EventToolDenied / EventToolResult.
	Tool ToolEvent

	// SessionRef carries EventSessionRef: the continuity handle to store on
	// the thread for the next turn.
	SessionRef string

	// ApprovalID carries EventApprovalReq: the id the renderer answers with
	// on the approve endpoint. Tool names what is being approved.
	ApprovalID string
}

// ToolEvent names a tool step the way a work log would. Target is the
// BASENAME of the file the call touches when there is one (≤80 chars):
// enough to read "Write · outline.md" while full paths and every other
// input stay out of the stream — inputs can hold anything.
type ToolEvent struct {
	Name   string
	Target string
	// Reason is set on EventToolDenied only.
	Reason string
	// IsError is set on EventToolResult.
	IsError bool
}
