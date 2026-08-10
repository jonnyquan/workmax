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

// hookDenied digs the permission decision out of the hook output map — the
// same place the CLI reads it from.
func hookDenied(t *testing.T, out claudesdk.HookJSONOutput) bool {
	t.Helper()
	specific, ok := out["hookSpecificOutput"].(map[string]any)
	if !ok {
		t.Fatalf("hook output has no hookSpecificOutput: %#v", out)
	}
	decision, _ := specific["permissionDecision"].(string)
	return decision == claudesdk.PermissionDecisionDeny
}
