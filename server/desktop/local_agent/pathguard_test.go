//go:build desktop

package local_agent

import (
	"path/filepath"
	"strings"
	"testing"

	claudesdk "github.com/jonnyquan/claude-agent-sdk-go/pkg/claudesdk"

	agentruntime "server/desktop/agentruntime"
)

// The containment matrix. Each row is a claim about the boundary; the ones
// marked allow are as load-bearing as the denials — a validator that blocks
// the user's own workspace is a tool loop that cannot work.
func TestValidatePathSafe(t *testing.T) {
	ws := t.TempDir()
	guard := newPathValidator(ws)

	allow := []string{
		filepath.Join(ws, "deck.md"),
		filepath.Join(ws, "sub", "dir", "notes.txt"),
		"relative.txt",
		"sub/relative.txt",
		"report..backup.pdf",      // `..` as substring, not as a segment
		filepath.Join(ws, ".env"), // inside the workspace it is the user's file
	}
	for _, p := range allow {
		if err := guard.validatePathSafe(p); err != nil {
			t.Errorf("must allow %q: %v", p, err)
		}
	}

	deny := []string{
		"../outside.txt",
		"sub/../../outside.txt",
		filepath.Join(ws, "..", "sibling", "x.txt"),
		"/etc/passwd",
		"/tmp/escape.txt",
		"/Users/someone/.ssh/id_rsa",
		filepath.Dir(ws) + "/other-thread/file.txt", // sibling workspace
		"~/.aws/credentials",
	}
	for _, p := range deny {
		if err := guard.validatePathSafe(p); err == nil {
			t.Errorf("must deny %q", p)
		}
	}
}

func TestValidateToolCall_ChecksEveryPathKey(t *testing.T) {
	ws := t.TempDir()
	guard := newPathValidator(ws)

	if err := guard.validateToolCall("Write", map[string]any{"file_path": "/etc/passwd"}); err == nil {
		t.Error("Write outside the workspace must be denied")
	}
	if err := guard.validateToolCall("Write", map[string]any{"file_path": filepath.Join(ws, "ok.txt")}); err != nil {
		t.Errorf("Write inside the workspace must be allowed: %v", err)
	}
	// A tool with no path arguments passes through.
	if err := guard.validateToolCall("WebSearch", map[string]any{"query": "/etc/passwd"}); err != nil {
		t.Errorf("non-path tools are not this guard's business: %v", err)
	}
	// Bash is spot-checked even though it is not currently allowed, so
	// enabling it later cannot silently skip the guard.
	if err := guard.validateToolCall("Bash", map[string]any{"command": "cat /etc/passwd"}); err == nil {
		t.Error("the Bash spot-check must fire")
	}
}

// The hook is the enforcement point; these drive it exactly as the SDK does.
func TestSecurityHook_DeniesAndReports(t *testing.T) {
	ws := t.TempDir()
	var denied []string
	hook := securityHook(newPathValidator(ws), func(ev agentruntime.Event) error {
		denied = append(denied, ev.Tool.Name+": "+ev.Tool.Reason)
		return nil
	})

	out, err := hook(map[string]any{
		"tool_name":  "Write",
		"tool_input": map[string]any{"file_path": "/tmp/escape.txt"},
	}, nil, claudesdk.HookContext{})
	if err != nil {
		t.Fatal(err)
	}
	if !hookDenied(t, out) {
		t.Error("an escape attempt must be denied")
	}
	if len(denied) != 1 || !strings.Contains(denied[0], "Write") {
		t.Errorf("the denial must be reported for narration, got %v", denied)
	}

	out, err = hook(map[string]any{
		"tool_name":  "Write",
		"tool_input": map[string]any{"file_path": filepath.Join(ws, "fine.txt")},
	}, nil, claudesdk.HookContext{})
	if err != nil {
		t.Fatal(err)
	}
	if hookDenied(t, out) {
		t.Error("a workspace write must be allowed")
	}

	// The deliberate deviation from the cloud hook: blind means stop.
	out, err = hook("not-a-map", nil, claudesdk.HookContext{})
	if err != nil {
		t.Fatal(err)
	}
	if !hookDenied(t, out) {
		t.Error("an unparseable payload must deny — a blind validator cannot wave tools through under bypassPermissions")
	}
}

// The tool SURFACE, enforced here rather than by WithAllowedTools — which is
// an auto-approve list, not a restriction. Composed with bypassPermissions it
// let the model reach the CLI's whole tool set; a real run had Bash `touch` a
// path outside the workspace.
func TestSecurityHook_DeniesToolsOutsideTheSurface(t *testing.T) {
	ws := t.TempDir()
	var denied []agentruntime.ToolEvent
	hook := securityHook(newPathValidator(ws), func(ev agentruntime.Event) error {
		denied = append(denied, ev.Tool)
		return nil
	})

	for _, tool := range []string{"Bash", "WebFetch", "WebSearch", "NotebookEdit", "Task", ""} {
		out, err := hook(map[string]any{
			"tool_name":  tool,
			"tool_input": map[string]any{"command": "touch /tmp/pwned"},
		}, nil, claudesdk.HookContext{})
		if err != nil {
			t.Fatal(err)
		}
		if !hookDenied(t, out) {
			t.Errorf("%q is outside the surface and must be denied", tool)
		}
	}
	if len(denied) != 6 {
		t.Errorf("every out-of-surface call must be narrated, got %d", len(denied))
	}

	// And the surface itself still gets through.
	for _, tool := range allowedTools {
		out, err := hook(map[string]any{
			"tool_name":  tool,
			"tool_input": map[string]any{"file_path": filepath.Join(ws, "fine.txt")},
		}, nil, claudesdk.HookContext{})
		if err != nil {
			t.Fatal(err)
		}
		if hookDenied(t, out) {
			t.Errorf("%q is on the surface and must pass", tool)
		}
	}
}

// hookDenied digs the permission decision out of the hook output map — the
// same place the CLI reads it from.
//
// An allowed call carries NO permissionDecision, deliberately: a PreToolUse
// hook that answers "allow" pre-approves inside the CLI and the permission
// system never runs, which silently removed canUseTool — and with it the
// whole approval card — from the loop. The hook's yes is "no objection", and
// it must still be a non-empty object, because the SDK marshals the response
// field with omitempty and an empty one vanishes off the wire (the CLI then
// logs a schema error for every allowed tool call).
func hookDenied(t *testing.T, out claudesdk.HookJSONOutput) bool {
	t.Helper()
	if len(out) == 0 {
		t.Fatal("hook output is empty; omitempty would drop it off the wire entirely")
	}
	specific, ok := out["hookSpecificOutput"].(map[string]any)
	if !ok {
		// No hook-specific output at all: the "no objection" shape.
		return false
	}
	decision, _ := specific["permissionDecision"].(string)
	if decision == claudesdk.PermissionDecisionAllow {
		t.Fatal("the hook must never answer allow: it bypasses the CLI's permission system and takes canUseTool out of the loop")
	}
	return decision == claudesdk.PermissionDecisionDeny
}
