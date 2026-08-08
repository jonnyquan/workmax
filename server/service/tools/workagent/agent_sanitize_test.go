package workagent

// agent_sanitize_test.go — coverage for the sanitizer that strips
// server-filesystem paths out of agent JSON payloads before they
// hit the SSE stream / persisted transcript. Two contracts to pin:
//
//   1. Path replacement is accurate (root → "agent_workspace") so
//      file links in the UI stay clickable.
//   2. NO leaks — server-side absolute paths must not survive into
//      saved transcripts; this is the substantive security property.

import (
	"encoding/json"
	"strings"
	"testing"

	workagentModel "server/model/workagent"
)

const sanitizerRoot = "/var/lib/workmax/workspaces/uid-42/thread-abc"

func TestSanitizeAgentJSON_EmptyInputsNoOp(t *testing.T) {
	if got := SanitizeAgentJSON(nil, sanitizerRoot); got != nil {
		t.Errorf("nil input must pass through as nil; got %v", got)
	}
	if got := SanitizeAgentJSON(json.RawMessage{}, sanitizerRoot); len(got) != 0 {
		t.Errorf("empty input must pass through unchanged; got %v", got)
	}
	// Empty workspace root → pass-through (caller may not have a
	// workspace bound yet, e.g. on the first turn before lifecycle).
	original := json.RawMessage(`{"text": "any content"}`)
	if got := SanitizeAgentJSON(original, ""); string(got) != string(original) {
		t.Errorf("empty workspaceRoot must pass through unchanged")
	}
}

func TestSanitizeAgentJSON_ReplacesAbsolutePath(t *testing.T) {
	payload := json.RawMessage(
		`{"path": "/var/lib/workmax/workspaces/uid-42/thread-abc/draft.md"}`,
	)
	got := SanitizeAgentJSON(payload, sanitizerRoot)
	out := string(got)
	if strings.Contains(out, "/var/lib/workmax/workspaces") {
		t.Errorf("server filesystem path leaked into sanitized output: %s", out)
	}
	if !strings.Contains(out, "agent_workspace/draft.md") {
		t.Errorf("expected agent_workspace prefix; got %s", out)
	}
}

func TestSanitizeAgentJSON_ReplacesMultipleOccurrences(t *testing.T) {
	// Realistic shape: an SDK message carrying multiple file
	// references in different keys.
	payload := json.RawMessage(`{
		"input": "/var/lib/workmax/workspaces/uid-42/thread-abc/a.md",
		"output": "/var/lib/workmax/workspaces/uid-42/thread-abc/b.md",
		"trace": ["/var/lib/workmax/workspaces/uid-42/thread-abc/c.md"]
	}`)
	got := SanitizeAgentJSON(payload, sanitizerRoot)
	out := string(got)
	if strings.Contains(out, "/var/lib/workmax") {
		t.Errorf("server path leaked: %s", out)
	}
	// All three references should be rewritten.
	if c := strings.Count(out, "agent_workspace/"); c != 3 {
		t.Errorf("expected 3 rewrites, got %d: %s", c, out)
	}
}

func TestSanitizeAgentJSON_PathOutsideWorkspaceUnchanged(t *testing.T) {
	// A path that doesn't share the workspace prefix must NOT be
	// touched — otherwise we'd rewrite system paths like /tmp or
	// /etc that the sanitizer has no business touching.
	payload := json.RawMessage(`{"system_log": "/var/log/syslog"}`)
	got := SanitizeAgentJSON(payload, sanitizerRoot)
	if string(got) != string(payload) {
		t.Errorf("non-workspace path was rewritten; before=%s after=%s", payload, got)
	}
}

func TestSanitizeAgentJSON_TrailingSlashInRootHandled(t *testing.T) {
	// filepath.Clean drops the trailing slash; ensure that doesn't
	// break the substring match on the raw payload.
	payload := json.RawMessage(
		`{"path": "/var/lib/workmax/workspaces/uid-42/thread-abc/draft.md"}`,
	)
	got := SanitizeAgentJSON(payload, sanitizerRoot+"/")
	if strings.Contains(string(got), "/var/lib") {
		t.Errorf("trailing-slash root failed to sanitize: %s", got)
	}
}

func TestSanitizeAgentConversation_RewritesEveryMessageAndResult(t *testing.T) {
	conv := &workagentModel.AgentConversation{
		Messages: []json.RawMessage{
			json.RawMessage(`{"file": "/var/lib/workmax/workspaces/uid-42/thread-abc/m1"}`),
			json.RawMessage(`{"file": "/var/lib/workmax/workspaces/uid-42/thread-abc/m2"}`),
		},
		Result: json.RawMessage(`{"final": "/var/lib/workmax/workspaces/uid-42/thread-abc/out"}`),
	}
	SanitizeAgentConversation(conv, sanitizerRoot)

	for i, m := range conv.Messages {
		if strings.Contains(string(m), "/var/lib") {
			t.Errorf("messages[%d] leaked server path: %s", i, m)
		}
		if !strings.Contains(string(m), "agent_workspace") {
			t.Errorf("messages[%d] missing agent_workspace rewrite: %s", i, m)
		}
	}
	if strings.Contains(string(conv.Result), "/var/lib") {
		t.Errorf("result leaked server path: %s", conv.Result)
	}
}

func TestSanitizeAgentConversation_NilOrEmptyNoOp(t *testing.T) {
	// Must not panic on nil conversation or empty root.
	SanitizeAgentConversation(nil, sanitizerRoot)

	conv := &workagentModel.AgentConversation{
		Messages: []json.RawMessage{
			json.RawMessage(`{"file": "/var/lib/workmax/keep"}`),
		},
	}
	SanitizeAgentConversation(conv, "")
	// Empty root → no rewrite.
	if !strings.Contains(string(conv.Messages[0]), "/var/lib") {
		t.Errorf("empty workspaceRoot must skip sanitization; got %s", conv.Messages[0])
	}
}

func TestSanitizeAgentConversation_EmptyResultNotMutated(t *testing.T) {
	conv := &workagentModel.AgentConversation{
		Messages: []json.RawMessage{json.RawMessage(`{"a": 1}`)},
		// Result deliberately empty.
	}
	SanitizeAgentConversation(conv, sanitizerRoot)
	if len(conv.Result) != 0 {
		t.Errorf("empty Result must stay empty; got %s", conv.Result)
	}
}
