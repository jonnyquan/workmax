package workagent

import (
	"encoding/json"
	"time"
)

// AgentConversation represents a complete Agent conversation aligned with SDK design.
// Messages and result are stored as raw JSON to preserve the SDK schema without wrappers.
// This follows the principle: SDK → JSON → Storage/Transfer → Frontend (zero conversion)
type AgentConversation struct {
	ID               string                   `json:"id"`
	ThreadID         string                   `json:"thread_id"`
	Messages         []json.RawMessage        `json:"messages"` // SDK native Message JSON array
	Result           json.RawMessage          `json:"result"`   // SDK native ResultMessage JSON
	GeneratedFiles   []map[string]interface{} `json:"generated_files,omitempty"`
	GeneratedFilesV2 []map[string]interface{} `json:"generatedFiles,omitempty"`
	CreatedAt        time.Time                `json:"created_at"`
}

// AgentSSEEventType represents SSE event types (simplified to 3 types)
type AgentSSEEventType string

const (
	AgentEventMessage AgentSSEEventType = "message" // New Message starts
	AgentEventBlock   AgentSSEEventType = "block"   // New ContentBlock received
	AgentEventDone    AgentSSEEventType = "done"    // Conversation complete
)

// AgentSSEEvent represents a Server-Sent Event for Agent streaming.
// All payloads (message, block, result) are SDK native JSON without any conversion.
type AgentSSEEvent struct {
	Type           AgentSSEEventType `json:"type"`
	MessageID      string            `json:"message_id"`
	ConversationID string            `json:"conversationId,omitempty"`
	MessageType    string            `json:"message_type,omitempty"`  // "assistant" | "user"
	MessageIndex   int               `json:"message_index,omitempty"` // Index in messages array
	Message        json.RawMessage   `json:"message,omitempty"`       // SDK native Message JSON
	BlockIndex     int               `json:"block_index,omitempty"`   // Index in current message's content array
	Block          json.RawMessage   `json:"block,omitempty"`         // SDK native ContentBlock JSON
	Result         json.RawMessage   `json:"result,omitempty"`        // SDK native ResultMessage JSON
}

// NewMessageEvent creates a message event with SDK native JSON payload
func NewMessageEvent(messageID string, messageType string, messageIndex int, message json.RawMessage) AgentSSEEvent {
	return AgentSSEEvent{
		Type:         AgentEventMessage,
		MessageID:    messageID,
		MessageType:  messageType,
		MessageIndex: messageIndex,
		Message:      message,
	}
}

// NewBlockEvent creates a block event with SDK native JSON payload
func NewBlockEvent(messageID string, messageIndex int, blockIndex int, block json.RawMessage) AgentSSEEvent {
	return AgentSSEEvent{
		Type:         AgentEventBlock,
		MessageID:    messageID,
		MessageIndex: messageIndex,
		BlockIndex:   blockIndex,
		Block:        block,
	}
}

// NewDoneEvent creates a done event with SDK native JSON payload
func NewDoneEvent(messageID string, result json.RawMessage) AgentSSEEvent {
	return AgentSSEEvent{
		Type:      AgentEventDone,
		MessageID: messageID,
		Result:    result,
	}
}
