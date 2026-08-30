//go:build desktop

package agentruntime

import "strings"

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
	EventPlan          EventKind = "plan"
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

	// Plan carries EventPlan: the agent's own task list, as it stands after
	// the update that produced this event. Whole list every time, not a
	// delta — the tool writes it that way, and a panel that has to
	// reconstruct a list from patches is a panel that can be wrong about
	// what the agent thinks it is doing.
	Plan []PlanItem
}

// PlanItem is one line of the agent's task list.
//
// This is the ONE tool input that reaches the renderer, and the exception is
// deliberate rather than an erosion of the rule beside it. ToolEvent carries
// a name and a file's basename because inputs can hold anything — a Write's
// whole body, a key someone pasted into a prompt. A task list is different in
// kind: it is model-authored prose written to be read by a person, the same
// class of content as the answer itself, which already streams verbatim.
// What does NOT come through is the input as it arrived: the shape below is
// parsed out of it, bounded, and anything unrecognised is dropped.
type PlanItem struct {
	Text  string
	State string
}

// The three states a task can be in. Anything the tool says that is not one
// of these becomes PlanPending: a list that renders is worth more than a
// list that refuses over a word it did not expect, and "not started" is the
// least wrong thing to say about a state nobody here understands.
const (
	PlanPending = "pending"
	PlanActive  = "active"
	PlanDone    = "done"
)

// Bounds. A task list is a panel in a 300px column, not a document: past a
// few dozen lines it has stopped being a summary of the work and the reader
// has stopped reading it either way.
const (
	maxPlanItems    = 32
	maxPlanItemText = 200
)

// ParsePlan reads the shape a todo-writing tool produces and returns the list
// it means. The second result says whether the input was a task list at all —
// a tool of this name whose input does not parse is not a plan, and the
// caller falls back to recording it as an ordinary step rather than
// silently swallowing a call that did something else.
//
// Written against the Claude CLI's TodoWrite: {"todos":[{"content","status"}]}.
// The key names are the tool's, not ours, so both spellings of the text field
// are read — "content" is what the tool sends, "text" is what a reasonable
// alternative would.
func ParsePlan(input map[string]any) ([]PlanItem, bool) {
	raw, ok := input["todos"].([]any)
	if !ok {
		return nil, false
	}
	items := make([]PlanItem, 0, len(raw))
	for _, entry := range raw {
		fields, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		text, _ := fields["content"].(string)
		if text == "" {
			text, _ = fields["text"].(string)
		}
		text = boundedMeta(strings.TrimSpace(text), maxPlanItemText)
		if text == "" {
			continue
		}
		state, _ := fields["status"].(string)
		items = append(items, PlanItem{Text: text, State: planState(state)})
		if len(items) == maxPlanItems {
			break
		}
	}
	// An empty list is a real answer — the agent clearing its plan — and has
	// to be distinguishable from "this was not a plan at all", which is what
	// the boolean is for.
	return items, true
}

func planState(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "in_progress", "active", "running":
		return PlanActive
	case "completed", "done", "complete":
		return PlanDone
	default:
		return PlanPending
	}
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
