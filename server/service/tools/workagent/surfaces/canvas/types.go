package canvas

import "time"

// ─── Context keys ───────────────────────────────────────────────────────────

// agentContextKey is a typed wrapper for context.WithValue keys. Using a
// distinct unexported type prevents collision with other packages that
// might use a bare string "user_id" key (staticcheck SA1029 warning).
type agentContextKey struct{ name string }

func (k agentContextKey) String() string { return "canvasagent." + k.name }

// AgentUserIDKey threads the authenticated uid into the agent invocation
// context. No downstream consumer reads it today; preserved for behavioral
// parity with the legacy handler in case a workagent callback ever needs
// per-user state inside the agent run.
var AgentUserIDKey = agentContextKey{name: "user_id"}


// ─── Request types ──────────────────────────────────────────────────────────

// CanvasAgentChatRequest is the request body from the frontend.
type CanvasAgentChatRequest struct {
	Messages       []CanvasAgentMessage    `json:"messages" binding:"required"`
	ConversationID string                  `json:"conversationId,omitempty"`
	Context        CanvasAgentContext      `json:"context" binding:"required"`
	Attachments    []CanvasAgentAttachment `json:"attachments,omitempty"`
	Metadata       map[string]any          `json:"metadata,omitempty"`
}

// CanvasAgentAttachment represents a file/image attachment sent with the chat message.
type CanvasAgentAttachment struct {
	Type     string `json:"type"`               // "image" | "video" | "file"
	Data     string `json:"data,omitempty"`     // base64-encoded data
	URL      string `json:"url,omitempty"`      // uploaded file URL
	MimeType string `json:"mimeType"`           // MIME type
	Filename string `json:"filename,omitempty"` // original filename
}

// CanvasAgentMessage represents a single conversation message.
type CanvasAgentMessage struct {
	Role        string                  `json:"role"`
	Content     string                  `json:"content"`
	Attachments []CanvasAgentAttachment `json:"attachments,omitempty"`
	Timestamp   time.Time               `json:"timestamp,omitempty"`
}

// CanvasAgentContext carries the canvas state that gets injected into the system prompt.
//
// PlanMode (#12 plan-then-execute, 2026-05-15): when true, the
// system prompt is extended with the propose_plan protocol so the
// model emits a single `propose_plan` tool_use block instead of
// executing operations directly. The frontend renders the plan as
// an editable card; the user approves / edits / rejects, and a
// follow-up user message carries the approved plan back for
// execution. Off by default — single-shot intents (e.g. "draw a
// red square") still flow through the legacy direct-execution
// path so the toggle is opt-in cognitive overhead.
type CanvasAgentContext struct {
	Elements    []map[string]any `json:"elements"`
	SelectedIDs []string         `json:"selectedIds"`
	Viewport    map[string]any   `json:"viewport"`
	Skill       string           `json:"skill,omitempty"`
	Model       string           `json:"model,omitempty"`
	ProjectID   string           `json:"projectId,omitempty"`
	PlanMode    bool             `json:"planMode,omitempty"`
}

// ─── SSE event types ────────────────────────────────────────────────────────

// CanvasAgentSSEEvent types (values for the "type" field in each SSE data line).
const (
	SSETypeMessage = "message"
	SSETypeBlock   = "block"
	SSETypeFile    = "file"
	SSETypeDone    = "done"
	SSETypeError   = "error"
)
