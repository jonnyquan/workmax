//go:build desktop

// R3 of the pi runtime: the approval extension bridge. In approval mode the
// tool profile widens to the write surface and a Go-embedded pi extension
// (workmax-permissions.ts) is injected with -e. The extension raises every
// write/edit call as an extension_ui_request select whose title carries a
// machine-readable envelope; this file intercepts those frames and runs them
// through the same agentruntime approval flow as the Claude engine, so both
// engines share one set of rules, cards, and grants.
//
// Wire contract (Go ↔ extension):
//
//	stdout: {"type":"extension_ui_request","id":..,"method":"select",
//	         "title":"workmax-approval:<tool>:<basename>",
//	         "options":["allow_once","allow_session","allow_always","deny"]}
//	stdin:  {"type":"extension_ui_response","id":..,"value":"<decision>"}
//
// Any allow_* value lets the tool run; "deny" (or cancelled) blocks it. UI
// requests that are not ours are answered with their protocol's refusal
// (confirm → confirmed:false, select/input/editor → cancelled:true) so a
// foreign extension can never hang the turn; fire-and-forget methods are
// ignored as before.
package pi_agent

import (
	"context"
	_ "embed"
	"io"
	"strings"

	agentruntime "server/desktop/agentruntime"
	cloudproxy "server/desktop/cloud_proxy"
)

// permissionsExtension is the pi extension source, embedded so the sidecar
// binary is self-contained; RunTurn writes it under pi_home before an
// approval-mode turn.
//
//go:embed workmax-permissions.ts
var permissionsExtension []byte

// permissionsExtensionFile is the file name under <dataDir>/pi_home.
const permissionsExtensionFile = "workmax-permissions.ts"

// approvalTitlePrefix marks a ui.select as ours: the extension encodes
// "workmax-approval:<tool>:<target basename>" in the title.
const approvalTitlePrefix = "workmax-approval:"

// approvalToolProfile is the approval-mode tool allowlist: the read surface
// plus the write surface the extension gates. Bash stays out, as on the
// Claude engine.
const approvalToolProfile = "read,grep,find,ls,write,edit"

// claudeToolName normalizes pi's lowercase tool names ("write", "edit") to
// the Claude names ("Write", "Edit") that ApprovalConfig's sets and the grant
// bookkeeping use — the two engines must draw on the same always/session
// grants, so the shared vocabulary is Claude's.
func claudeToolName(pi string) string {
	if pi == "" {
		return ""
	}
	return strings.ToUpper(pi[:1]) + pi[1:]
}

// handleUIRequest processes one extension_ui_request frame in approval mode
// (p.approvals != nil; without approvals the caller ignores the frame, the
// pre-R3 behavior). Dialog methods always get an answer — ours through the
// approval flow, foreign ones through their protocol's refusal — because an
// unanswered dialog blocks pi's tool loop forever.
func (p *framePump) handleUIRequest(ctx context.Context, f *piFrame) error {
	switch f.Method {
	case "select":
		if strings.HasPrefix(f.Title, approvalTitlePrefix) {
			return p.handleApprovalSelect(ctx, f)
		}
		return p.respond(ctx, map[string]any{
			"type": "extension_ui_response", "id": f.ID, "cancelled": true,
		})
	case "confirm":
		return p.respond(ctx, map[string]any{
			"type": "extension_ui_response", "id": f.ID, "confirmed": false,
		})
	case "input", "editor":
		return p.respond(ctx, map[string]any{
			"type": "extension_ui_response", "id": f.ID, "cancelled": true,
		})
	}
	// notify/setStatus/setWidget/setTitle/set_editor_text and anything newer:
	// fire-and-forget, nothing waits on an answer.
	return nil
}

// handleApprovalSelect runs one workmax-approval envelope through the shared
// flow and answers pi with the decision. Blocking this pump goroutine while
// Await waits is deliberate: pi itself is blocked on our answer, and tool_call
// handlers are preflighted sequentially, so nothing else needs the stream.
func (p *framePump) handleApprovalSelect(ctx context.Context, f *piFrame) error {
	rest := strings.TrimPrefix(f.Title, approvalTitlePrefix)
	piTool, target, _ := strings.Cut(rest, ":")
	res, cerr := p.approvals.Consult(ctx, p.emit, claudeToolName(piTool), target)
	if cerr != nil {
		return cerr // the EventApprovalReq emit failed: client gone, abort turn
	}
	value := string(agentruntime.ApprovalDeny)
	if res.Allowed {
		value = string(res.Decision)
	}
	return p.respond(ctx, map[string]any{
		"type": "extension_ui_response", "id": f.ID, "value": value,
	})
}

// respond writes one extension_ui_response frame to pi's stdin.
func (p *framePump) respond(ctx context.Context, frame map[string]any) error {
	if werr := writeCommand(p.stdin, frame); werr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &agentruntime.RuntimeError{
			Kind:      cloudproxy.KindServiceUnavailable,
			Message:   "无法向 pi 进程发送应答",
			Retryable: true,
			Details:   map[string]any{"reason": werr.Error()},
		}
	}
	return nil
}

// framePump carries the per-turn state dispatch needs: response routing,
// the emit sink, and — in approval mode — the approval policy plus pi's
// stdin for extension_ui_response answers.
type framePump struct {
	pending   map[string]bool
	emit      agentruntime.EmitFunc
	approvals *agentruntime.ApprovalConfig // nil = pre-approved read-only mode
	stdin     io.Writer
}
