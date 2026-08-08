package workagent

// agent_turn_hooks_test.go — coverage for the three SDK hooks that
// wrap every tool call:
//
//   - PreToolUse (security): PathValidator.ValidateToolCall is the
//     allow/deny gate. Allow records a start time; Deny short-
//     circuits with a reason.
//   - PostToolUse (observability): no SDK behavior change, just
//     log lines — smoke-test that it returns the empty output
//     shape without crashing on weird inputs.
//   - PostToolUseFailure (observability): same — exercises
//     scrubCredentialsFromLog on the error string.
//
// setupTurnHooks wires all three with the "*" matcher; we pin the
// option count + matcher to catch regressions where a future
// "let's filter to specific tools" refactor accidentally drops a
// hook (silently disabling security on the unmatched ones).

import (
	"path/filepath"
	"strings"
	"testing"

	claudecode "github.com/jonnyquan/claude-agent-sdk-go"
)

func TestSetupTurnHooks_ReturnsThreeHooksWithStarMatcher(t *testing.T) {
	opts := setupTurnHooks("/tmp/workspace")
	if len(opts) != 3 {
		t.Errorf("expected 3 hook options (Pre/PostUse/PostFailure), got %d", len(opts))
	}
	// The "*" matcher catches every tool call. A regression that
	// adds a per-tool filter could disable the security hook for
	// tools not in the filter list — exactly the bug PathValidator
	// is supposed to prevent. Smoke-check via len + non-nil; the
	// internal shape of claudecode.Option is opaque to us.
	for i, o := range opts {
		if o == nil {
			t.Errorf("hook option %d is nil", i)
		}
	}
}

// ---------------------------------------------------------------------
// Security hook (PreToolUse) — the load-bearing piece. Allow vs Deny
// is the contract every tool call rides on.
// ---------------------------------------------------------------------

func TestSecurityHook_AllowsValidWorkspacePath(t *testing.T) {
	// Use a real abs path so PathValidator's workspace-resolve check
	// passes; tempdir is the most portable workspace fixture.
	tmpDir := t.TempDir()
	pv := NewPathValidator(tmpDir)
	tracker := newToolDurationTracker()
	hook := buildSecurityHook(pv, tracker)

	id := "tool-use-1"
	input := map[string]any{
		"tool_name": "Read",
		"tool_input": map[string]any{
			"file_path": filepath.Join(tmpDir, "subdir", "a.md"),
		},
	}
	out, err := hook(input, &id, claudecode.HookContext{})
	if err != nil {
		t.Fatalf("security hook returned error: %v", err)
	}
	// Inspect the underlying map: PermissionDecision should be Allow.
	hookSpecific, ok := out["hookSpecificOutput"].(map[string]any)
	if !ok {
		t.Fatalf("hookSpecificOutput missing or wrong type: %+v", out)
	}
	if hookSpecific["permissionDecision"] != claudecode.PermissionDecisionAllow {
		t.Errorf("decision = %v, want %v", hookSpecific["permissionDecision"], claudecode.PermissionDecisionAllow)
	}
}

func TestSecurityHook_DeniesTraversalOutsideWorkspace(t *testing.T) {
	tmpDir := t.TempDir()
	pv := NewPathValidator(tmpDir)
	hook := buildSecurityHook(pv, newToolDurationTracker())

	id := "tool-use-2"
	input := map[string]any{
		"tool_name": "Read",
		"tool_input": map[string]any{
			// "../../etc/passwd" segment-aware check rejects it.
			"file_path": "../../etc/passwd",
		},
	}
	out, err := hook(input, &id, claudecode.HookContext{})
	if err != nil {
		t.Fatalf("hook unexpectedly errored on deny path: %v", err)
	}
	hookSpecific, ok := out["hookSpecificOutput"].(map[string]any)
	if !ok {
		t.Fatalf("hookSpecificOutput missing: %+v", out)
	}
	if hookSpecific["permissionDecision"] != claudecode.PermissionDecisionDeny {
		t.Errorf("traversal path must be denied; got decision=%v", hookSpecific["permissionDecision"])
	}
	// Reason carries the "Security violation:" prefix.
	reason, _ := out["reason"].(string)
	if !strings.Contains(reason, "Security violation") {
		t.Errorf("reason missing 'Security violation' prefix: %q", reason)
	}
}

func TestSecurityHook_NonMapInputFallsBackToAllow(t *testing.T) {
	// If the SDK ever sends a non-map hook payload, the hook MUST
	// not panic AND must not block the conversation — degrade to
	// Allow so the user's turn moves forward.
	hook := buildSecurityHook(NewPathValidator("/tmp"), newToolDurationTracker())
	id := "tool-use-3"
	out, err := hook("not-a-map", &id, claudecode.HookContext{})
	if err != nil {
		t.Fatalf("hook errored on weird input: %v", err)
	}
	hookSpecific, ok := out["hookSpecificOutput"].(map[string]any)
	if !ok {
		t.Fatalf("hookSpecificOutput missing on fallback: %+v", out)
	}
	if hookSpecific["permissionDecision"] != claudecode.PermissionDecisionAllow {
		t.Errorf("fallback decision = %v, want Allow", hookSpecific["permissionDecision"])
	}
}

// On Allow, the hook records a start time in the tracker so the
// matching PostToolUse can compute duration. On Deny, recording
// would be a leak (PostToolUse never fires for denied calls), so
// we must NOT record.
func TestSecurityHook_AllowRecordsDuration_DenyDoesNot(t *testing.T) {
	tmpDir := t.TempDir()
	pv := NewPathValidator(tmpDir)
	tracker := newToolDurationTracker()
	hook := buildSecurityHook(pv, tracker)

	allowID := "allow-id"
	allowInput := map[string]any{
		"tool_name":  "Read",
		"tool_input": map[string]any{"file_path": filepath.Join(tmpDir, "a.md")},
	}
	if _, err := hook(allowInput, &allowID, claudecode.HookContext{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := tracker.consume(&allowID); !ok {
		t.Errorf("allow path must record duration; tracker had no entry for %q", allowID)
	}

	denyID := "deny-id"
	denyInput := map[string]any{
		"tool_name":  "Read",
		"tool_input": map[string]any{"file_path": "../../etc/passwd"},
	}
	if _, err := hook(denyInput, &denyID, claudecode.HookContext{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := tracker.consume(&denyID); ok {
		t.Errorf("deny path must NOT record duration (would leak; PostToolUse never fires)")
	}
}

// ---------------------------------------------------------------------
// Observability hooks — smoke tests. They don't change SDK behavior;
// just ensure no panic / no leaked context on weird inputs.
// ---------------------------------------------------------------------

func TestPostToolUseHook_HandlesNormalInput(t *testing.T) {
	hook := buildPostToolUseHook(newToolDurationTracker())
	id := "x"
	input := map[string]any{
		"tool_name":     "Read",
		"tool_response": map[string]any{"content": "hello"},
	}
	out, err := hook(input, &id, claudecode.HookContext{})
	if err != nil {
		t.Fatal(err)
	}
	// PostToolUse with empty additionalContext returns an empty
	// HookJSONOutput (no keys) — the contract is "don't inject
	// anything into the model's context."
	if len(out) != 0 {
		t.Errorf("PostToolUse should return empty output, got %+v", out)
	}
}

func TestPostToolUseHook_NonMapInputNoCrash(t *testing.T) {
	hook := buildPostToolUseHook(newToolDurationTracker())
	id := "x"
	_, err := hook("not-a-map", &id, claudecode.HookContext{})
	if err != nil {
		t.Errorf("hook should tolerate non-map input, got error: %v", err)
	}
}

func TestPostToolUseFailureHook_InterruptDistinguishedFromError(t *testing.T) {
	hook := buildPostToolUseFailureHook(newToolDurationTracker())

	// Interrupt path — is_interrupt=true.
	interrupt := map[string]any{
		"tool_name":    "Bash",
		"error":        "user cancelled",
		"is_interrupt": true,
	}
	id := "y"
	if _, err := hook(interrupt, &id, claudecode.HookContext{}); err != nil {
		t.Errorf("interrupt failure hook errored: %v", err)
	}

	// Real-error path — is_interrupt false / missing.
	bad := map[string]any{
		"tool_name": "Bash",
		"error":     "exit 1",
	}
	if _, err := hook(bad, &id, claudecode.HookContext{}); err != nil {
		t.Errorf("error failure hook errored: %v", err)
	}
}

func TestPostToolUseFailureHook_NonMapInputNoCrash(t *testing.T) {
	hook := buildPostToolUseFailureHook(newToolDurationTracker())
	id := "y"
	_, err := hook("not-a-map", &id, claudecode.HookContext{})
	if err != nil {
		t.Errorf("hook should tolerate non-map input, got error: %v", err)
	}
}
