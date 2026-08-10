//go:build desktop

package local_agent

import (
	claudesdk "github.com/jonnyquan/claude-agent-sdk-go/pkg/claudesdk"

	agentruntime "server/desktop/agentruntime"
)

// approvalCallback turns the SDK's canUseTool consultation into the unified
// approval flow, which lives in agentruntime.ApprovalConfig.Consult so the pi
// engine's extension bridge runs the exact same rules: auto-allow the declared
// read surface and every stored grant, ask the user for the write surface,
// deny everything else outright.
//
// Runs on the SDK's control goroutine while pump reads on the caller's; it
// touches only the broker (locked) and emit paths that write to the (locked)
// SSE writer, like the security hook before it.
func approvalCallback(cfg *agentruntime.ApprovalConfig, emit agentruntime.EmitFunc) claudesdk.CanUseToolCallback {
	return func(toolName string, toolInput map[string]any, pctx claudesdk.ToolPermissionContext) (claudesdk.PermissionResult, error) {
		res, err := cfg.Consult(pctx.Context, emit, toolName, toolTarget(toolInput))
		if err != nil {
			// The approval card could not reach the client; interrupt rather
			// than let the loop keep asking a dead writer.
			return claudesdk.NewPermissionDeny("approval channel unavailable", true), nil
		}
		if res.Allowed {
			return claudesdk.NewPermissionAllow(nil, nil), nil
		}
		return claudesdk.NewPermissionDeny(res.Reason, false), nil
	}
}
