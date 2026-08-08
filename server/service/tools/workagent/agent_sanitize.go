package workagent

import (
	"encoding/json"

	workagentModel "server/model/workagent"
)

// SanitizeAgentJSON replaces sensitive filesystem paths with relative
// workspace paths in an SDK-emitted JSON payload (Message, Block, or
// Result). Empty payloads and missing workspace roots are short-
// circuited so the caller doesn't have to gate.
//
// Lifted from api/pro/tools/workagent (package-private
// sanitizeAgentJSON) and api/pro/tools (canvas-specific
// sanitizeCanvasAgentJSON, which was a hand-duplicated version of the
// same logic minus a couple safety guards). One implementation now —
// any future canvas + workagent SSE merge picks the same sanitizer.
func SanitizeAgentJSON(data json.RawMessage, workspaceRoot string) json.RawMessage {
	if len(data) == 0 || workspaceRoot == "" {
		return data
	}

	sanitized := ReplaceWorkspacePaths(string(data), workspaceRoot)
	return json.RawMessage(sanitized)
}

// SanitizeAgentConversation runs SanitizeAgentJSON over every message
// payload + the conversation result, in place. Used by the persisted-
// conversation write path so saved transcripts don't leak server
// filesystem paths.
//
// Workspace-root empty / nil conversation are no-ops — callers don't
// have to gate.
func SanitizeAgentConversation(conversation *workagentModel.AgentConversation, workspaceRoot string) {
	if conversation == nil || workspaceRoot == "" {
		return
	}

	for i, msg := range conversation.Messages {
		conversation.Messages[i] = SanitizeAgentJSON(msg, workspaceRoot)
	}

	if len(conversation.Result) > 0 {
		conversation.Result = SanitizeAgentJSON(conversation.Result, workspaceRoot)
	}
}
