//go:build desktop

package local_agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "server/desktop/agentruntime"
	cloudproxy "server/desktop/cloud_proxy"
)

// fakeRuntime is a non-claude agentruntime.Runtime (the pi seam from the
// Engine's point of view): it owns its workspace root and needs no CLI path.
type fakeRuntime struct {
	name   string
	root   string
	events []agentruntime.Event
	err    error
	gotIn  agentruntime.TurnInput
	runs   int
}

func (f *fakeRuntime) Name() string          { return f.name }
func (f *fakeRuntime) WorkspaceRoot() string { return f.root }

func (f *fakeRuntime) RunTurn(_ context.Context, in agentruntime.TurnInput, emit agentruntime.EmitFunc) error {
	f.runs++
	f.gotIn = in
	for _, ev := range f.events {
		if werr := emit(ev); werr != nil {
			return werr
		}
	}
	return f.err
}

// An engine built with NewEngineWithRuntime skips claude's CLI-path check,
// places the workspace where the runtime says, and books the session ref
// under the runtime's own name.
func TestChat_ForeignRuntimeEngine(t *testing.T) {
	db := openTestDB(t)
	seedThreadRow(t, db)
	root := t.TempDir()
	rt := &fakeRuntime{name: "pi", root: root, events: []agentruntime.Event{
		{Kind: agentruntime.EventSessionRef, SessionRef: "/sessions/s1.jsonl"},
		{Kind: agentruntime.EventTextDelta, Delta: "answer"},
	}}
	engine := NewEngineWithRuntime(stubProfile{baseURL: "http://127.0.0.1:1", modelID: "m"}, db, nil, nil, rt)

	dst := &memSSEWriter{}
	if err := engine.Chat(context.Background(), chatReq(), dst); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	frames, perrs := dst.snapshot()
	if len(perrs) != 0 {
		t.Fatalf("proxy errors: %+v", perrs)
	}
	var kinds []string
	for _, f := range frames {
		kinds = append(kinds, f.Type)
	}
	if strings.Join(kinds, ",") != "text_delta,done" {
		t.Fatalf("frames = %v", kinds)
	}
	if rt.gotIn.Workspace != filepath.Join(root, "thread_thr_l2") {
		t.Errorf("workspace = %q, want it under the runtime's root", rt.gotIn.Workspace)
	}
	if fi, err := os.Stat(rt.gotIn.Workspace); err != nil || !fi.IsDir() {
		t.Errorf("workspace dir must exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude-home")); !os.IsNotExist(err) {
		t.Error(".claude-home is claude wiring and must not appear for a foreign runtime")
	}
	if got := loadSessionRef(db, "thr_l2", "pi"); got != "/sessions/s1.jsonl" {
		t.Errorf("session ref = %q, want stored under the runtime's name", got)
	}
}

// A foreign runtime's RuntimeError surfaces as a typed proxy_error, exactly
// like claude's.
func TestChat_ForeignRuntimeErrorSurfaces(t *testing.T) {
	db := openTestDB(t)
	seedThreadRow(t, db)
	rt := &fakeRuntime{name: "pi", root: t.TempDir(), err: &agentruntime.RuntimeError{
		Kind:      cloudproxy.KindServiceUnavailable,
		Message:   "pi 工具循环在完成前退出",
		Retryable: true,
	}}
	engine := NewEngineWithRuntime(stubProfile{baseURL: "http://127.0.0.1:1", modelID: "m"}, db, nil, nil, rt)

	dst := &memSSEWriter{}
	if err := engine.Chat(context.Background(), chatReq(), dst); err == nil {
		t.Fatal("a RuntimeError must fail the turn")
	}
	_, perrs := dst.snapshot()
	if len(perrs) != 1 || perrs[0].Kind != cloudproxy.KindServiceUnavailable ||
		perrs[0].Message != "pi 工具循环在完成前退出" {
		t.Fatalf("proxy errors = %+v", perrs)
	}
}
