package workagent

// agent_persistence.go owns the post-turn save path and the thread-
// name AI generator that decorates a freshly-named thread on its
// first turn. Pulled out of agent_api.go (B1 chunk 4) — these are
// the "what happens after the SDK is done" concerns, distinct from
// the per-turn phase orchestration in agent_turn_phases.go.
//
// Functions here:
//   - recordAgentError: bumps thread.updated_at on a failed turn so
//     the chat list still moves the thread to the top.
//   - saveAgentConversation: marshals the AgentConversation,
//     extracts the final output + preview, persists the message row,
//     snapshots the latest TodoWrite plan onto the thread row.
//   - updateThreadName: orchestrates "first turn? rename via AI"
//     plus the per-turn statistics refresh.
//   - AIGenerateThreadName: the timeout-bound LLM call that produces
//     the JSON {"name": "..."} response.
//
// Same package as agent_api.go; all call sites work unchanged.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"server/globals"
	workagentModel "server/model/workagent"
	llmService "server/service/llm"
	workagentService "server/service/tools/workagent"

	"github.com/tmc/langchaingo/llms"
)

// recordAgentError updates thread timestamp when error occurs
// Note: Does NOT write to usage_record - errors are not counted as user usage
func recordAgentError(chatThread *workagentModel.ChatThread) {
	if chatThread == nil {
		return
	}

	if err := workagentService.DefaultThreadRepository().TouchTimestamp(chatThread.Id, time.Now()); err != nil {
		globals.Error(fmt.Sprintf("[Agent Error] Failed to update thread timestamp: %v", err))
	} else {
		globals.Info(fmt.Sprintf("[Agent Error] Thread %d timestamp updated", chatThread.Id))
	}
}

// saveAgentConversation saves the conversation to database and returns summary and message ID
func saveAgentConversation(conversation *workagentModel.AgentConversation, uid uint, threadID uint, userContent string, modelTier string, idempotencyKey string) (string, uint, error) {
	// Extract final tool output for summaries/result enrichment
	finalOutput := extractAgentFinalOutput(conversation)
	if finalOutput != "" {
		injectFinalOutputIntoResult(conversation, finalOutput)
	}

	// Marshal conversation to JSON
	conversationJSON, err := json.Marshal(conversation)
	if err != nil {
		return "", 0, fmt.Errorf("failed to marshal conversation: %w", err)
	}

	// Extract summary for display and full content for AIText
	var summary string
	var fullAIText string

	if finalOutput != "" {
		summary = buildAgentOutputPreview(finalOutput)
		fullAIText = finalOutput // Store full output without truncation
	} else if len(conversation.Messages) > 0 {
		summary = workagentService.GetContentSummary(conversation.Messages[len(conversation.Messages)-1])
		// For non-tool messages, extract full text content
		fullAIText = extractFullMessageContent(conversation.Messages[len(conversation.Messages)-1])
	}
	if strings.TrimSpace(summary) == "" {
		summary = "[Agent conversation]"
	}
	if strings.TrimSpace(fullAIText) == "" {
		fullAIText = summary
	}

	// Create message record
	userText := strings.TrimSpace(userContent)

	message := workagentModel.ChatMessage{
		UUID:              conversation.ID,
		ThreadID:          int(threadID),
		UID:               int(uid),
		UserText:          userText,
		AIText:            fullAIText, // Store FULL content without truncation
		ContentType:       "agent_conversation",
		StructuredContent: string(conversationJSON), // Store full conversation
		ChatMode:          "agent",
		Model:             modelTier,
	}
	if strings.TrimSpace(idempotencyKey) != "" {
		key := strings.TrimSpace(idempotencyKey)
		message.MessageIdempotencyKey = &key
	}

	if err := workagentService.DefaultMessageRepository().CreateAgentMessage(&message); err != nil {
		return "", 0, fmt.Errorf("failed to save message: %w", err)
	}

	// Snapshot the latest TodoWrite plan onto the thread row so the
	// ContextPanel's Progress section can rehydrate even when the
	// emitting message has been paginated out. Failure is non-fatal
	// (the message-walk fallback in findLatestTodoInMessages still
	// covers loaded pages) — log and continue rather than rolling
	// back the conversation save. Read-modify-write safety: see the
	// PlanRepository doc — agentTurnLocks serializes turns per thread.
	if planJSON := extractLatestTodoWritePlan(conversation); planJSON != "" {
		if err := workagentService.DefaultPlanRepository().SnapshotPlan(threadID, planJSON, time.Now()); err != nil {
			globals.Warn(fmt.Sprintf("[Agent API] Failed to snapshot plan for thread %d: %v (continuing — message-walk fallback still works)", threadID, err))
		}
	}

	globals.Info(fmt.Sprintf("[Agent API] Saved conversation: id=%s, messageID=%d, messages=%d",
		conversation.ID, message.Id, len(conversation.Messages)))

	return summary, message.Id, nil
}

// updateThreadName updates thread name and statistics based on AI response
func (api *AIChatApiNew) updateThreadName(chatThread *workagentModel.ChatThread, aiText string) {
	if chatThread.Name == "Untitled" {
		jsonData := api.AIGenerateThreadName(aiText)
		if jsonData["name"] != nil {
			name := jsonData["name"].(string)
			if err := workagentService.DefaultThreadRepository().RenameThread(chatThread.Id, name); err != nil {
				globals.Warn(fmt.Sprintf("[Agent API] Failed to rename thread %d: %v", chatThread.Id, err))
			}
		}
	}

	// Update thread statistics (message count, preview, file count)
	if err := api.updateThreadStatistics(chatThread.Id); err != nil {
		globals.Error(fmt.Sprintf("Failed to update thread statistics: %v", err))
	}
}

// threadNamingTimeout caps how long thread-name generation can hang.
// Naming is decoration — the user's turn already finished and the
// thread row exists with name="Untitled" — so we'd rather skip the
// rename than block other goroutines / pile up requests when the
// underlying text LLM is degraded. 30s matches the soft-timeout used
// elsewhere in the codebase for non-streaming "fire-and-forget" LLM
// calls.
const threadNamingTimeout = 30 * time.Second

// AIGenerateThreadName generates thread name using AI
func (api *AIChatApiNew) AIGenerateThreadName(aiText string) map[string]interface{} {
	prompt := "I want you to act as a brainstormer. \n" +
		"I am given a prompt, please brainstorm to help me generate a name for content. \n" +
		"Requirement:\n" +
		"content: " + aiText + "\n" +
		"- Output json content in . The key is name. name only one result"

	llm := llmService.GetClient()
	if llm == nil {
		globals.Error("Text LLM client not available for thread naming")
		return map[string]interface{}{}
	}
	// Bound the LLM call so a degraded backend can't pile up naming
	// requests indefinitely. Background-context-with-timeout (rather
	// than the request context) so a long agent turn doesn't kill
	// naming if naming takes its full 30s — naming runs after the
	// turn's gin context has already shipped its 200.
	ctx, cancel := context.WithTimeout(context.Background(), threadNamingTimeout)
	defer cancel()

	completion, err := llms.GenerateFromSinglePrompt(ctx,
		llm,
		prompt,
		llms.WithTemperature(0.6),
		llms.WithJSONMode(),
	)
	if err != nil {
		globals.Warn(fmt.Sprintf("Failed to generate thread name: %v", err))
		return map[string]interface{}{}
	}
	globals.Info(fmt.Sprintf("Thread name generation completion: %v", completion))

	dataContent := strings.TrimPrefix(completion, "```json")
	dataContent = strings.TrimSuffix(dataContent, "```")
	var jsonData map[string]interface{}
	err = json.Unmarshal([]byte(dataContent), &jsonData)
	if err != nil {
		globals.Warn(fmt.Sprintf("Failed to parse thread name JSON: %v", err))
		return map[string]interface{}{}
	}
	return jsonData
}
