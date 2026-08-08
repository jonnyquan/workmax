package workagent

// agent_result_helpers.go holds the stateless utility functions that
// massage the SDK's AgentConversation / Result / message JSON into
// the shapes the agent_api handler chain consumes. Pulled out of
// agent_api.go (B1 chunk 1) to shrink the god-object handler file
// without changing any behaviour.
//
// All functions here are pure (or close to it — getRemainingCredits
// ForUser hits the DB, kept here because it's a single-purpose
// wrapper and lives next to its only caller). No gin context,
// no AIChatApiNew receiver. Tests for any of these can drive them
// without spinning up the request pipeline.

import (
	"encoding/json"
	"fmt"
	"strings"

	"server/globals"
	workagentModel "server/model/workagent"
	accountService "server/service/account"
	workagentService "server/service/tools/workagent"
)

// extractLatestTodoWritePlan walks the conversation backward looking
// for the most recent assistant block of type tool_use named
// "TodoWrite", returning its `input.todos` JSON as-is. Used by
// persistAgentTurn to populate the plan-history slice without
// re-running the SDK's TodoWrite parser.
func extractLatestTodoWritePlan(conversation *workagentModel.AgentConversation) string {
	if conversation == nil || len(conversation.Messages) == 0 {
		return ""
	}
	for msgIdx := len(conversation.Messages) - 1; msgIdx >= 0; msgIdx-- {
		var envelope struct {
			Type    string            `json:"type"`
			Content []json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(conversation.Messages[msgIdx], &envelope); err != nil {
			continue
		}
		if envelope.Type != "assistant" {
			continue
		}
		for blockIdx := len(envelope.Content) - 1; blockIdx >= 0; blockIdx-- {
			var meta struct {
				Type  string          `json:"type"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			}
			if err := json.Unmarshal(envelope.Content[blockIdx], &meta); err != nil {
				continue
			}
			if meta.Type != "tool_use" || meta.Name != "TodoWrite" || len(meta.Input) == 0 {
				continue
			}
			// Pull out input.todos specifically — the rest of input is
			// SDK-internal metadata we don't need to round-trip.
			var input struct {
				Todos json.RawMessage `json:"todos"`
			}
			if err := json.Unmarshal(meta.Input, &input); err != nil || len(input.Todos) == 0 {
				continue
			}
			return string(input.Todos)
		}
	}
	return ""
}

// extractAgentFinalOutput attempts to find the most meaningful tool_result output.
func extractAgentFinalOutput(conversation *workagentModel.AgentConversation) string {
	if conversation == nil || len(conversation.Messages) == 0 {
		return ""
	}

	var fallback string

	for msgIndex := len(conversation.Messages) - 1; msgIndex >= 0; msgIndex-- {
		var envelope struct {
			Type    string            `json:"type"`
			Content []json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(conversation.Messages[msgIndex], &envelope); err != nil {
			continue
		}
		if envelope.Type != "user" {
			continue
		}

		for blockIndex := len(envelope.Content) - 1; blockIndex >= 0; blockIndex-- {
			blockJSON := envelope.Content[blockIndex]

			var meta struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(blockJSON, &meta); err != nil {
				continue
			}
			if meta.Type != "tool_result" {
				continue
			}

			text := strings.TrimSpace(extractToolResultText(blockJSON))
			if text == "" {
				continue
			}

			if isMeaningfulAgentOutput(text) {
				return text
			}

			if runeLen(text) > runeLen(fallback) {
				fallback = text
			}
		}
	}

	return fallback
}

// extractToolResultText flattens a tool_result block into plain text.
func extractToolResultText(blockJSON json.RawMessage) string {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(blockJSON, &payload); err != nil {
		return ""
	}

	candidates := []string{"content", "text", "output", "result", "stdout", "markdown"}
	for _, key := range candidates {
		if raw, ok := payload[key]; ok {
			if text := extractStringFromRawJSON(raw); strings.TrimSpace(text) != "" {
				return text
			}
		}
	}
	return ""
}

// extractStringFromRawJSON recursively attempts to flatten JSON into text.
func extractStringFromRawJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		return str
	}

	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err == nil {
		var lines []string
		for _, item := range arr {
			text := extractStringFromRawJSON(item)
			if strings.TrimSpace(text) != "" {
				lines = append(lines, text)
			}
		}
		return strings.TrimSpace(strings.Join(lines, "\n"))
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err == nil {
		keys := []string{"text", "stdout", "markdown", "content", "message"}
		for _, key := range keys {
			if child, ok := obj[key]; ok {
				if text := extractStringFromRawJSON(child); strings.TrimSpace(text) != "" {
					return text
				}
			}
		}
		serialized, err := json.Marshal(obj)
		if err == nil {
			return string(serialized)
		}
	}

	return ""
}

// isMeaningfulAgentOutput heuristically checks if the text looks like a final report.
func isMeaningfulAgentOutput(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}

	if runeLen(trimmed) >= 200 {
		return true
	}

	lineCount := strings.Count(trimmed, "\n") + 1
	if lineCount >= 6 {
		return true
	}

	if runeLen(trimmed) >= 100 && strings.Contains(trimmed, "#") {
		return true
	}

	return false
}

// injectFinalOutputIntoResult attaches the extracted output to Result JSON for frontend rendering.
func injectFinalOutputIntoResult(conversation *workagentModel.AgentConversation, output string) {
	output = strings.TrimSpace(output)
	if conversation == nil || output == "" {
		return
	}

	const maxStored = 8000
	const maxPreview = 400

	stored := truncateRunes(output, maxStored)
	preview := truncateRunes(output, maxPreview)

	update := func(result map[string]interface{}) {
		result["excel_final_output"] = stored
		result["excel_final_output_preview"] = preview
	}

	if len(conversation.Result) > 0 {
		var existing map[string]interface{}
		if err := json.Unmarshal(conversation.Result, &existing); err == nil {
			update(existing)
			if data, err := json.Marshal(existing); err == nil {
				conversation.Result = data
			}
			return
		}
		// Existing Result wasn't a JSON object — could be a string
		// error from the SDK. Don't synthesise a fresh object that
		// would replace the error with just the final-output keys.
		// Same posture as f5264048 / 01dfa5a3 / d98f2929: enrichment
		// is best-effort, the SDK's authoritative result wins.
		globals.Warn("[Agent API] conversation.Result is not a JSON object, skipping final-output injection to preserve original")
		return
	}

	// No prior Result — safe to mint a fresh decoration object.
	result := map[string]interface{}{
		"type":                       "result",
		"excel_final_output":         stored,
		"excel_final_output_preview": preview,
	}
	if data, err := json.Marshal(result); err == nil {
		conversation.Result = data
	}
}

// buildAgentOutputPreview condenses the final output for list display.
func buildAgentOutputPreview(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}

	normalized := strings.Join(strings.Fields(trimmed), " ")
	return truncateRunes(normalized, 200)
}

func runeLen(text string) int {
	return len([]rune(text))
}

func truncateRunes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

// extractFullMessageContent extracts the full text content from a message without truncation
func extractFullMessageContent(msg json.RawMessage) string {
	if len(msg) == 0 {
		return ""
	}

	var envelope struct {
		Type    string            `json:"type"`
		Content []json.RawMessage `json:"content"`
	}

	if err := json.Unmarshal(msg, &envelope); err != nil {
		return ""
	}

	var fullText strings.Builder

	for _, block := range envelope.Content {
		var contentBlock struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(block, &contentBlock); err != nil {
			continue
		}
		if contentBlock.Type == "text" && contentBlock.Text != "" {
			if fullText.Len() > 0 {
				fullText.WriteString("\n")
			}
			fullText.WriteString(contentBlock.Text)
		}
	}

	return fullText.String()
}

// describeBlockType extracts the block type string for logging.
func describeBlockType(block json.RawMessage) string {
	if len(block) == 0 {
		return "unknown"
	}
	var meta struct {
		Type string `json:"type"`
		Name string `json:"name,omitempty"`
	}
	if err := json.Unmarshal(block, &meta); err != nil {
		return "unknown"
	}
	if meta.Type == "tool_use" && strings.TrimSpace(meta.Name) != "" {
		return fmt.Sprintf("tool_use(%s)", meta.Name)
	}
	return meta.Type
}

// resultMeta captures commonly used fields from ResultMessage.
type resultMeta struct {
	SessionID string
	NumTurns  int
}

func extractResultMeta(result json.RawMessage) resultMeta {
	meta := resultMeta{}
	if len(result) == 0 {
		return meta
	}
	var parsed struct {
		SessionID string `json:"session_id"`
		NumTurns  int    `json:"num_turns"`
	}
	if err := json.Unmarshal(result, &parsed); err != nil {
		return meta
	}
	meta.SessionID = strings.TrimSpace(parsed.SessionID)
	meta.NumTurns = parsed.NumTurns
	return meta
}

// extractTotalCostUSDFromResult pulls the SDK-reported per-turn cost
// from a ResultMessage JSON. Returns (cost, true) when the field is
// present, (nil, false) on absence / malformed input. Used at the
// finalize site to log estimate-vs-actual billing drift over time so
// ops can validate the project's predictive credit-cost model against
// what the upstream gateway actually charged.
//
// Distinct from extractResultMeta because the cost field is rare —
// only populated when the upstream returns it (some models / gateways
// don't surface cost). Splitting the two parsers means a missing
// session_id doesn't suppress cost extraction and vice-versa.
func extractTotalCostUSDFromResult(result json.RawMessage) (*float64, bool) {
	if len(result) == 0 {
		return nil, false
	}
	var parsed struct {
		TotalCostUSD *float64 `json:"total_cost_usd,omitempty"`
	}
	if err := json.Unmarshal(result, &parsed); err != nil {
		return nil, false
	}
	if parsed.TotalCostUSD == nil {
		return nil, false
	}
	return parsed.TotalCostUSD, true
}

func errorResultPayload() json.RawMessage {
	payload := map[string]any{
		"type":         "result",
		"subtype":      "error",
		"is_error":     true,
		"user_message": "An error occurred while processing your request. Please try again.",
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return json.RawMessage(`{"type":"result","is_error":true,"user_message":"An error occurred"}`)
	}
	return json.RawMessage(data)
}

func fileReferenceErrorPayload(err error) json.RawMessage {
	message := "One or more selected files are no longer available. Remove the missing files and try again."
	if err != nil && err.Error() != "" {
		message = err.Error()
	}
	payload := map[string]any{
		"type":         "result",
		"subtype":      "file_reference_error",
		"is_error":     true,
		"code":         "FILE_REFERENCE_ERROR",
		"user_message": message,
		"error":        message,
	}
	data, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		return json.RawMessage(`{"type":"result","subtype":"file_reference_error","is_error":true,"code":"FILE_REFERENCE_ERROR","user_message":"One or more selected files are no longer available."}`)
	}
	return json.RawMessage(data)
}

func attachGeneratedFilesToResult(result json.RawMessage, files []map[string]interface{}) json.RawMessage {
	if len(files) == 0 {
		return result
	}

	payload := map[string]interface{}{}
	if len(result) > 0 {
		if err := json.Unmarshal(result, &payload); err != nil {
			// Result is not a JSON object — could be a JSON string
			// error payload (ResultMessage.result is typed
			// `Record<string, any> | string` in the SDK), null, an
			// array, etc. Wrapping it as `{generated_files, ...}`
			// would silently drop the error string the SDK shipped.
			// Same posture d98f2929 applied to the validator
			// enrichment path: skip enrichment, log + return original.
			globals.Warn(fmt.Sprintf("[Agent API] result is not a JSON object, skipping generated_files attach to preserve original: %v", err))
			return result
		}
	}

	payload["generated_files"] = files
	payload["generatedFiles"] = files

	data, err := json.Marshal(payload)
	if err != nil {
		return result
	}
	return json.RawMessage(data)
}

func attachRAGMetadataToResult(result json.RawMessage, metadata workagentService.KnowledgeRetrievalMetadata) json.RawMessage {
	payloadMeta := metadata.ToMap()
	if len(payloadMeta) == 0 {
		return result
	}

	payload := map[string]interface{}{}
	if len(result) > 0 {
		if err := json.Unmarshal(result, &payload); err != nil {
			globals.Warn(fmt.Sprintf("[Agent API] result is not a JSON object, skipping rag_metadata attach to preserve original: %v", err))
			return result
		}
	}
	payload["rag_metadata"] = payloadMeta

	data, err := json.Marshal(payload)
	if err != nil {
		return result
	}
	return json.RawMessage(data)
}

// attachRemainingCreditsToResult enriches the done event's result with
// the user's post-deduction credit balance. This lets the input bar
// render "~N turns left" so the user knows they're approaching
// exhaustion BEFORE typing the next message — without it, the only
// signal is a "insufficient credits" error after they've already
// committed to sending. Reserve has already drawn down credits_used
// by this point in the flow, so GetBalance reflects the post-turn
// state. Best-effort: any DB error gets a warn-and-skip so the done
// event still ships its primary payload.
func attachRemainingCreditsToResult(result json.RawMessage, uid uint) json.RawMessage {
	remaining, err := getRemainingCreditsForUser(uid)
	if err != nil {
		globals.Warn(fmt.Sprintf("[Agent API] Failed to read remaining credits for uid=%d: %v", uid, err))
		return result
	}

	payload := map[string]interface{}{}
	if len(result) > 0 {
		if err := json.Unmarshal(result, &payload); err != nil {
			// Same non-object preservation as f5264048 / d98f2929 —
			// don't replace a string-shaped error result with a
			// new object that contains only the credits field.
			// The credits enrichment is decoration; the SDK's
			// authoritative result wins.
			globals.Warn(fmt.Sprintf("[Agent API] result is not a JSON object, skipping remaining_credits attach to preserve original: %v", err))
			return result
		}
	}
	payload["remaining_credits"] = remaining
	payload["remainingCredits"] = remaining

	data, err := json.Marshal(payload)
	if err != nil {
		return result
	}
	return json.RawMessage(data)
}

// extractOutputPaths flattens the FileOutputInfo slice the OnDone
// callback gets from scanOutputsDirectorySince into the absolute
// path strings the skill validators want. Tiny helper, lives here so
// the OnDone block stays one-liner-ish.
func extractOutputPaths(files []FileOutputInfo) []string {
	paths := make([]string, 0, len(files))
	for _, f := range files {
		if f.Path != "" {
			paths = append(paths, f.Path)
		}
	}
	return paths
}

// getRemainingCreditsForUser is a thin wrapper over the credits-pack
// service so the OnDone callback doesn't need to know about its
// shape. Returns the raw remaining count; caller decides how to
// surface zero/negative balances.
func getRemainingCreditsForUser(uid uint) (int, error) {
	_, _, remaining, err := accountService.NewCreditsPackService().GetBalance(int(uid))
	if err != nil {
		return 0, err
	}
	return remaining, nil
}

func normalizeFileOutputList(raw interface{}) []map[string]interface{} {
	if raw == nil {
		return nil
	}

	switch v := raw.(type) {
	case []map[string]interface{}:
		return v
	case []interface{}:
		files := make([]map[string]interface{}, 0, len(v))
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				files = append(files, m)
			}
		}
		return files
	default:
		return nil
	}
}

// wrapResultErrorIfNeeded checks if result contains error and wraps with user-friendly message
func wrapResultErrorIfNeeded(result json.RawMessage) json.RawMessage {
	if len(result) == 0 {
		return result
	}

	var resultCheck struct {
		IsError bool   `json:"is_error"`
		Result  string `json:"result"`
	}

	if err := json.Unmarshal(result, &resultCheck); err != nil {
		return result // Parse error, return original
	}

	if !resultCheck.IsError {
		return result // No error, return original
	}

	// Check if it's a 429 error (GLM subscription, rate limit, etc.)
	if strings.Contains(resultCheck.Result, "429") {
		// Wrap with user-friendly message
		var resultObj map[string]interface{}
		if err := json.Unmarshal(result, &resultObj); err == nil {
			resultObj["user_message"] = "Agent is temporarily unavailable. Please try again later."

			// Keep the technical error for debugging
			resultObj["error_detail"] = resultCheck.Result

			if wrapped, err := json.Marshal(resultObj); err == nil {
				globals.Info("[Agent API] Wrapped 429 error with user-friendly message")
				return json.RawMessage(wrapped)
			}
		}
	}

	// For other errors, return original
	return result
}

// (sanitize helpers moved to service/tools/workagent/agent_sanitize.go
// as workagentService.SanitizeAgentJSON / SanitizeAgentConversation —
// canvas now uses the same code path. R3 phase 4-prep.)
