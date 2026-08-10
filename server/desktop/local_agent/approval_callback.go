//go:build desktop

package local_agent

import (
	"fmt"
	"log"

	claudesdk "github.com/jonnyquan/claude-agent-sdk-go/pkg/claudesdk"

	agentruntime "server/desktop/agentruntime"
)

// approvalCallback turns the SDK's canUseTool consultation into the unified
// approval flow: auto-allow the declared read surface and every stored
// grant, ask the user for the write surface, deny everything else outright.
//
// Runs on the SDK's control goroutine while pump reads on the caller's; it
// touches only the broker (locked) and emit paths that write to the (locked)
// SSE writer, like the security hook before it.
func approvalCallback(cfg *agentruntime.ApprovalConfig, emit agentruntime.EmitFunc) claudesdk.CanUseToolCallback {
	return func(toolName string, toolInput map[string]any, pctx claudesdk.ToolPermissionContext) (claudesdk.PermissionResult, error) {
		if cfg.AutoAllowed[toolName] || cfg.Broker.SessionGranted(cfg.ThreadUUID, toolName) {
			return claudesdk.NewPermissionAllow(nil, nil), nil
		}
		if !cfg.AskAllowed[toolName] {
			// Outside the declared surface entirely — no card, no appeal.
			_ = emit(agentruntime.Event{
				Kind: agentruntime.EventToolDenied,
				Tool: agentruntime.ToolEvent{Name: toolName, Reason: "工具不在本地循环的许可面内"},
			})
			return claudesdk.NewPermissionDeny(
				fmt.Sprintf("tool %s is outside the local loop's surface", toolName), false), nil
		}

		id, wait := cfg.Broker.Begin(cfg.TurnUUID)
		target := toolTarget(toolInput)
		if werr := emit(agentruntime.Event{
			Kind:       agentruntime.EventApprovalReq,
			ApprovalID: id,
			Tool:       agentruntime.ToolEvent{Name: toolName, Target: target},
		}); werr != nil {
			cfg.Broker.Abandon(cfg.TurnUUID, id)
			return claudesdk.NewPermissionDeny("approval channel unavailable", true), nil
		}
		decision, aerr := cfg.Broker.Await(pctx.Context, cfg.TurnUUID, id, wait, cfg.Timeout)
		if aerr != nil {
			log.Printf("local agent approval: %s %s: %v", toolName, id, aerr)
			_ = emit(agentruntime.Event{
				Kind: agentruntime.EventToolDenied,
				Tool: agentruntime.ToolEvent{Name: toolName, Target: target, Reason: "等待批准超时或中止"},
			})
			return claudesdk.NewPermissionDeny("the user did not approve in time", false), nil
		}
		switch decision {
		case agentruntime.ApprovalAllowOnce:
			return claudesdk.NewPermissionAllow(nil, nil), nil
		case agentruntime.ApprovalAllowSession:
			cfg.Broker.GrantSession(cfg.ThreadUUID, toolName)
			return claudesdk.NewPermissionAllow(nil, nil), nil
		case agentruntime.ApprovalAllowAlways:
			cfg.Broker.GrantSession(cfg.ThreadUUID, toolName)
			if cfg.Persist != nil {
				cfg.Persist(toolName)
			}
			return claudesdk.NewPermissionAllow(nil, nil), nil
		default:
			_ = emit(agentruntime.Event{
				Kind: agentruntime.EventToolDenied,
				Tool: agentruntime.ToolEvent{Name: toolName, Target: target, Reason: "用户拒绝了此操作"},
			})
			return claudesdk.NewPermissionDeny("the user declined this tool call", false), nil
		}
	}
}
