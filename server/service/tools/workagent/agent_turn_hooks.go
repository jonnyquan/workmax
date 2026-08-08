package workagent

import (
	"encoding/json"
	"fmt"

	"server/globals"

	claudecode "github.com/jonnyquan/claude-agent-sdk-go"
)

// setupTurnHooks builds the three SDK hook options for a single
// turn:
//
//   - PreToolUse (security): the workspace-scoped PathValidator gates
//     every tool call; on Allow it also records a start time for
//     duration computation.
//   - PostToolUse (observability): logs tool name, response size,
//     and turn-around duration on success.
//   - PostToolUseFailure (observability): logs tool name + scrubbed
//     error + interrupt flag on failure.
//
// All three hooks are wired with a "*" matcher (every tool). State
// captured by the closures (pathValidator, toolDurationTracker) is
// per-call — a new instance per setupTurnHooks call, so concurrent
// turns can't see each other's tool_use_ids.
//
// Extracted from processAgentConversationInternal to keep that
// function focused on the SDK message loop. Returns the hook options
// ready to append; the caller must NOT register additional hooks for
// the same events without merging into this slice.
func setupTurnHooks(workspacePath string) []claudecode.Option {
	pathValidator := NewPathValidator(workspacePath)
	durations := newToolDurationTracker()

	securityHook := buildSecurityHook(pathValidator, durations)
	postToolUseHook := buildPostToolUseHook(durations)
	postToolUseFailureHook := buildPostToolUseFailureHook(durations)

	matchAllTools := "*"
	return []claudecode.Option{
		claudecode.WithHook(claudecode.HookEventPreToolUse, claudecode.HookMatcher{
			Matcher: &matchAllTools,
			Hooks:   []claudecode.HookCallback{securityHook},
		}),
		claudecode.WithHook(claudecode.HookEventPostToolUse, claudecode.HookMatcher{
			Matcher: &matchAllTools,
			Hooks:   []claudecode.HookCallback{postToolUseHook},
		}),
		claudecode.WithHook(claudecode.HookEventPostToolUseFailure, claudecode.HookMatcher{
			Matcher: &matchAllTools,
			Hooks:   []claudecode.HookCallback{postToolUseFailureHook},
		}),
	}
}

// buildSecurityHook returns the PreToolUse callback that validates
// tool inputs against the workspace path policy. On Allow it records
// a start time for duration computation; on Deny the matching
// PostToolUse won't fire so we skip recording (would leak otherwise).
func buildSecurityHook(pathValidator *PathValidator, durations *toolDurationTracker) claudecode.HookCallback {
	return func(input claudecode.HookInput, toolUseID *string, hctx claudecode.HookContext) (claudecode.HookJSONOutput, error) {
		inputMap, ok := input.(map[string]any)
		if !ok {
			// Keep conversation moving if SDK sends a non-map hook payload.
			globals.Warn(fmt.Sprintf("[Agent Security] Unexpected hook input type: %T", input))
			return claudecode.NewPreToolUseOutput(claudecode.PermissionDecisionAllow, "", nil), nil
		}

		toolName, _ := inputMap["tool_name"].(string)
		toolInputAny := inputMap["tool_input"]
		toolInput := map[string]interface{}{}
		if cast, ok := toolInputAny.(map[string]interface{}); ok && cast != nil {
			toolInput = cast
		} else if castAny, ok := toolInputAny.(map[string]any); ok && castAny != nil {
			for k, v := range castAny {
				toolInput[k] = v
			}
		}

		if err := pathValidator.ValidateToolCall(toolName, toolInput); err != nil {
			globals.Warn(fmt.Sprintf("[Agent Security] BLOCKED %s: %v", toolName, err))
			return claudecode.NewPreToolUseOutput(claudecode.PermissionDecisionDeny,
				fmt.Sprintf("Security violation: %v", err), nil), nil
		}

		durations.record(toolUseID)
		globals.Debug(fmt.Sprintf("[Agent Security] ALLOWED %s", toolName))
		return claudecode.NewPreToolUseOutput(claudecode.PermissionDecisionAllow, "", nil), nil
	}
}

// buildPostToolUseHook returns the success-path observability hook.
// Returns no additional context (empty string) — we don't want to
// inject anything into the model's context, this is logging-only.
func buildPostToolUseHook(durations *toolDurationTracker) claudecode.HookCallback {
	return func(input claudecode.HookInput, toolUseID *string, hctx claudecode.HookContext) (claudecode.HookJSONOutput, error) {
		inputMap, ok := input.(map[string]any)
		if !ok {
			return claudecode.NewPostToolUseOutput(""), nil
		}
		toolName, _ := inputMap["tool_name"].(string)
		response := inputMap["tool_response"]

		// Estimate response size by JSON-marshaling. Cost is
		// O(response) per tool call; at chat-scale (dozens of tools
		// per turn) this is noise. If response payloads ever push
		// into MB, switch to a streaming length-only walker.
		responseSize := 0
		if response != nil {
			if data, jerr := json.Marshal(response); jerr == nil {
				responseSize = len(data)
			}
		}

		if duration, ok := durations.consume(toolUseID); ok {
			globals.Debug(fmt.Sprintf("[Agent] PostToolUse %s: response_size=%d duration_ms=%d", toolName, responseSize, duration.Milliseconds()))
		} else {
			globals.Debug(fmt.Sprintf("[Agent] PostToolUse %s: response_size=%d", toolName, responseSize))
		}
		return claudecode.NewPostToolUseOutput(""), nil
	}
}

// buildPostToolUseFailureHook returns the failure-path observability
// hook. Distinguishes Info (interrupt) from Warn (real error) so
// log triage doesn't conflate the two. Error strings are scrubbed
// because the SDK occasionally surfaces upstream diagnostics that
// may carry credentials.
func buildPostToolUseFailureHook(durations *toolDurationTracker) claudecode.HookCallback {
	return func(input claudecode.HookInput, toolUseID *string, hctx claudecode.HookContext) (claudecode.HookJSONOutput, error) {
		inputMap, ok := input.(map[string]any)
		if !ok {
			return claudecode.HookJSONOutput{}, nil
		}
		toolName, _ := inputMap["tool_name"].(string)
		errStr, _ := inputMap["error"].(string)
		isInterrupt := false
		if v, ok := inputMap["is_interrupt"].(bool); ok {
			isInterrupt = v
		}

		safeErr := scrubCredentialsFromLog(errStr)
		durSuffix := ""
		if duration, ok := durations.consume(toolUseID); ok {
			durSuffix = fmt.Sprintf(" duration_ms=%d", duration.Milliseconds())
		}

		if isInterrupt {
			globals.Info(fmt.Sprintf("[Agent] PostToolUseFailure %s INTERRUPTED%s: %s", toolName, durSuffix, safeErr))
		} else {
			globals.Warn(fmt.Sprintf("[Agent] PostToolUseFailure %s%s: %s", toolName, durSuffix, safeErr))
		}
		return claudecode.HookJSONOutput{}, nil
	}
}
