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
	if strings.Join(kinds, ",") != "turn_meta,text_delta,done" {
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

// A mind picks the model, and the turn says so.
//
// This is the point at which a mind stops being a label: the value it names
// reaches the runtime AND the provenance frame, so the transcript records
// which brain answered rather than which one is configured right now. The
// endpoint stays the identity's — a mind chooses a model, not a provider.
func TestChat_ActiveMindChoosesTheModel(t *testing.T) {
	run := func(t *testing.T, mind func(uid uint64) MindPolicy) (agentruntime.TurnInput, []cloudproxy.SSEEvent) {
		t.Helper()
		db := openTestDB(t)
		seedThreadRow(t, db)
		rt := &fakeRuntime{name: "pi", root: t.TempDir(), events: []agentruntime.Event{
			{Kind: agentruntime.EventTextDelta, Delta: "answer"},
		}}
		engine := NewEngineWithRuntime(
			stubProfile{baseURL: "http://127.0.0.1:1", modelID: "identity-model"}, db, nil, nil, rt)
		if mind != nil {
			engine.UseMind(mind)
		}
		dst := &memSSEWriter{}
		if err := engine.Chat(context.Background(), chatReq(), dst); err != nil {
			t.Fatalf("Chat: %v", err)
		}
		frames, _ := dst.snapshot()
		return rt.gotIn, frames
	}

	in, frames := run(t, func(uint64) MindPolicy {
		return MindPolicy{
			Name:    "Payroll mind",
			Model:   "the-minds-model",
			Persona: "Answer only in bullet points.",
		}
	})
	if in.ModelID != "the-minds-model" {
		t.Errorf("model = %q, want the active mind's", in.ModelID)
	}
	if in.BaseURL != "http://127.0.0.1:1" {
		t.Errorf("base url = %q; a mind chooses a model, not a provider", in.BaseURL)
	}
	if frames[0].Type != "turn_meta" || !strings.Contains(frames[0].Data, "the-minds-model") {
		t.Errorf("the turn must announce the model it was actually told to use: %q", frames[0].Data)
	}
	// The role hint reaches the runtime as system-level instruction, separate
	// from the prompt: a mind's memory changes what the model knows, its
	// persona changes how it works, and the two travel in different fields.
	if in.Persona != "Answer only in bullet points." {
		t.Errorf("persona = %q, want the active mind's role hint", in.Persona)
	}
	// The name changes nothing about the turn; it is the label the transcript
	// keeps so a reader can tell two minds apart when they share a model.
	if !strings.Contains(frames[0].Data, `"mind":"Payroll mind"`) {
		t.Errorf("the turn must record which mind produced it: %q", frames[0].Data)
	}
	if strings.Contains(in.Prompt, "bullet points") {
		t.Error("the persona must not be folded into the user's turn by the engine")
	}

	// A mind with no opinion, and no mind wiring at all, both leave the
	// identity's configured model alone. Failing towards the setting is the
	// only safe direction: a mind that cannot be read should cost a
	// preference, never an answer.
	for name, fn := range map[string]func(uint64) MindPolicy{
		"no opinion": func(uint64) MindPolicy { return MindPolicy{} },
		"whitespace": func(uint64) MindPolicy { return MindPolicy{Model: "   ", Persona: "  "} },
		"not wired":  nil,
	} {
		t.Run(name, func(t *testing.T) {
			in, _ := run(t, fn)
			if in.ModelID != "identity-model" {
				t.Errorf("model = %q, want the identity's", in.ModelID)
			}
			if in.Persona != "" {
				t.Errorf("persona = %q, want none", in.Persona)
			}
		})
	}
}
