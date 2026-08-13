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
	EventTurnMeta      EventKind = "turn_meta"
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

	// Turn carries EventTurnMeta.
	Turn TurnMeta
}

// TurnMeta says which engine ran a turn and which model it was told to use.
//
// It exists because the renderer could otherwise only show the CURRENT
// setting, which stops being true the moment the setting changes: scroll back
// after switching engines and every past answer silently claims to have come
// from the new one. An answer should keep saying what produced it.
//
// Engine is certain — the caller holds the Runtime and asks it its name.
// Model is what the turn was told to use: both runtimes pass TurnInput.ModelID
// to their engine verbatim (claudesdk.WithModel, pi's --model) and neither
// reads back what the far side actually loaded, so this is a faithful record
// of the request and not a claim about the response. Empty when no model was
// configured and the engine picked its own default — in which case the honest
// thing is to say nothing rather than to name a model nobody chose.
//
// Deliberately NOT carried: the base URL. It is user-supplied, can hold
// credentials in its query, and adds nothing a reader of one answer needs.
type TurnMeta struct {
	Engine string
	Model  string
	// Mind is the name of the identity's active mind, or "" when none was
	// chosen. The NAME rather than the id: this is read by a person under an
	// answer, and a uuid tells them nothing. A mind that is later renamed
	// therefore leaves older answers naming what it was called at the time,
	// which is what a record should do.
	Mind string
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
