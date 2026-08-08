package workagent

import (
	"encoding/json"
	"strings"

	workagentModel "server/model/workagent"
)

// determineAgentMode resolves the effective agent mode by priority:
// message metadata > thread default > workagentModel.DefaultAgentMode.
func determineAgentMode(metadataMode, threadMode string) string {
	if mode := normalizeAgentMode(metadataMode); mode != "" {
		return mode
	}
	if mode := normalizeAgentMode(threadMode); mode != "" {
		return mode
	}
	return workagentModel.DefaultAgentMode
}

// requiresSessionReset reports whether a thread-config delta must
// abandon the SDK transcript on the next turn.
//
// True only when the agent mode changes: mode rotates the system
// prompt (different skill bundle), and resuming the old session_id
// against the new prompt silently replays the previous skill's
// identity/tools/output-format. Tier change, by contrast, keeps the
// same prompt and only routes to a different upstream model brain on
// the same conversation history — the SDK runtime HOME is per-process
// (see agent_client_manager.go buildEnvVarsFromAccount), so resuming
// across accounts is the desired UX. If the SDK ever starts persisting
// per-account session state, this predicate needs revisiting.
func requiresSessionReset(prevMode, newMode string) bool {
	return newMode != "" && newMode != prevMode
}

// determineModelTier resolves the effective model tier by priority:
// message metadata > thread stored model > "work-pro".
func determineModelTier(metadataTier, threadModel string) string {
	if tier := normalizeModelTier(metadataTier); tier != "" {
		return tier
	}
	if tier := normalizeModelTier(threadModel); tier != "" {
		return tier
	}
	return "work-pro"
}

func buildTierAccessDeniedPayload(message string) json.RawMessage {
	payload := map[string]interface{}{
		"type":     "result",
		"subtype":  "quota_exceeded",
		"is_error": true,
		"error":    message,
		// Error code follows the user-visible tier name (renamed from
		// "Work Plus" → "Pro" on 2026-05-14). FE doesn't branch on
		// the literal value today — it forwards `result.error` to the
		// renderer — so this rename is safe-by-grep. Kept in
		// SCREAMING_SNAKE_CASE to match the codebase convention.
		"code": "PRO_TIER_REQUIRED",
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return errorResultPayload()
	}
	return json.RawMessage(data)
}

func buildSkillAccessDeniedPayload(message string, source string, requiredTier string) json.RawMessage {
	payload := map[string]interface{}{
		"type":     "result",
		"subtype":  "skill_access_required",
		"is_error": true,
		"error":    message,
		"code":     "SKILL_ACCESS_REQUIRED",
		"source":   source,
	}
	if requiredTier = strings.TrimSpace(requiredTier); requiredTier != "" {
		payload["required_tier"] = requiredTier
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return errorResultPayload()
	}
	return json.RawMessage(data)
}

func buildSkillUnavailablePayload(message string) json.RawMessage {
	payload := map[string]interface{}{
		"type":     "result",
		"subtype":  "skill_unavailable",
		"is_error": true,
		"error":    message,
		"code":     "SKILL_UNAVAILABLE",
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return errorResultPayload()
	}
	return json.RawMessage(data)
}

func resultIndicatesError(result json.RawMessage) bool {
	var parsed struct {
		IsError bool `json:"is_error"`
	}
	if err := json.Unmarshal(result, &parsed); err != nil {
		return false
	}
	return parsed.IsError
}

func markResultAsError(result json.RawMessage, subtype, message, code string) json.RawMessage {
	var payload map[string]interface{}
	if err := json.Unmarshal(result, &payload); err != nil || payload == nil {
		payload = map[string]interface{}{}
	}
	payload["type"] = "result"
	payload["subtype"] = subtype
	payload["is_error"] = true
	payload["error"] = message
	if strings.TrimSpace(code) != "" {
		payload["code"] = code
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return result
	}
	return json.RawMessage(data)
}

func markResultAsWarning(
	result json.RawMessage,
	subtype,
	message,
	code string,
	missingFields []string,
	suggestedPrompt string,
) json.RawMessage {
	var payload map[string]interface{}
	if err := json.Unmarshal(result, &payload); err != nil || payload == nil {
		payload = map[string]interface{}{}
	}
	payload["type"] = "result"
	payload["subtype"] = subtype
	payload["is_error"] = false
	payload["warning"] = message
	if strings.TrimSpace(code) != "" {
		payload["code"] = code
	}
	if len(missingFields) > 0 {
		payload["missing_fields"] = missingFields
	}
	if strings.TrimSpace(suggestedPrompt) != "" {
		payload["suggested_prompt"] = suggestedPrompt
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return result
	}
	return json.RawMessage(data)
}
