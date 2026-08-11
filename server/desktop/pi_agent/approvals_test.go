//go:build desktop

package pi_agent

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agentruntime "server/desktop/agentruntime"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const (
	testTurnUUID   = "turn-pi"
	testThreadUUID = "thr-pi"
)

func piApprovalConfig(broker *agentruntime.ApprovalBroker, persisted *[]string, timeout time.Duration) *agentruntime.ApprovalConfig {
	return &agentruntime.ApprovalConfig{
		Broker:      broker,
		TurnUUID:    testTurnUUID,
		ThreadUUID:  testThreadUUID,
		AutoAllowed: map[string]bool{"Read": true, "Glob": true, "Grep": true},
		AskAllowed:  map[string]bool{"Write": true, "Edit": true},
		Persist:     func(tool string) { *persisted = append(*persisted, tool) },
		Timeout:     timeout,
	}
}

// eventChanSink is a thread-safe emit sink that also publishes every event on
// a channel, so a test goroutine can react (Resolve a card) while RunTurn
// blocks in the pump.
type eventChanSink struct {
	mu     sync.Mutex
	events []agentruntime.Event
	ch     chan agentruntime.Event
}

func newEventChanSink() *eventChanSink {
	return &eventChanSink{ch: make(chan agentruntime.Event, 64)}
}

func (s *eventChanSink) emit(ev agentruntime.Event) error {
	s.mu.Lock()
	s.events = append(s.events, ev)
	s.mu.Unlock()
	s.ch <- ev
	return nil
}

func (s *eventChanSink) kinds() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, ev := range s.events {
		out = append(out, string(ev.Kind))
	}
	return out
}

func waitEvent(t *testing.T, sink *eventChanSink, kind agentruntime.EventKind) agentruntime.Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-sink.ch:
			if ev.Kind == kind {
				return ev
			}
		case <-deadline:
			t.Fatalf("no %s event within deadline", kind)
		}
	}
}

// newInteractiveRuntime feeds stdout through a pipe so the test can script
// frames step by step while RunTurn is live.
func newInteractiveRuntime(t *testing.T) (*Runtime, *fakeProc, *io.PipeWriter) {
	t.Helper()
	dataDir := t.TempDir()
	bin := filepath.Join(dataDir, "pi")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rt := NewRuntime(Config{BinPath: bin, DataDir: dataDir, WorkspaceRoot: filepath.Join(dataDir, "ws")})
	pr, pw := io.Pipe()
	proc := &fakeProc{stdin: &closableBuffer{}, stdout: pr}
	rt.start = func(context.Context, procSpec, func(string)) (procHandle, error) { return proc, nil }
	return rt, proc, pw
}

func writeFrame(t *testing.T, pw *io.PipeWriter, line string) {
	t.Helper()
	if _, err := pw.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

// stdinFrames parses every JSONL line the runtime wrote to pi's stdin.
func stdinFrames(t *testing.T, proc *fakeProc) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(proc.stdin.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("stdin line not JSON: %q (%v)", line, err)
		}
		out = append(out, m)
	}
	return out
}

// uiResponses filters stdin frames down to extension_ui_response frames,
// keyed by id.
func uiResponses(t *testing.T, proc *fakeProc) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, m := range stdinFrames(t, proc) {
		if m["type"] == "extension_ui_response" {
			id, _ := m["id"].(string)
			out[id] = m
		}
	}
	return out
}

func uiSelect(id, title string) string {
	return `{"type":"extension_ui_request","id":"` + id + `","method":"select","title":"` + title +
		`","options":["allow_once","allow_session","allow_always","deny"]}`
}

// ---------------------------------------------------------------------------
// Launch contract + golden extension
// ---------------------------------------------------------------------------

// Approval mode widens the tool profile to the write surface and injects the
// embedded extension with -e; the on-disk copy matches the embedded bytes.
func TestRunTurn_ApprovalMode_LaunchContract(t *testing.T) {
	rt, spec, _ := newTestRuntime(t, frames(promptOK, `{"type":"agent_settled"}`), nil)
	in := turnInput()
	in.Approvals = piApprovalConfig(agentruntime.NewApprovalBroker(), &[]string{}, time.Second)
	if err := rt.RunTurn(context.Background(), in, (&captured{}).emit); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}

	extPath := filepath.Join(spec.Env["PI_CODING_AGENT_DIR"], permissionsExtensionFile)
	args := strings.Join(spec.Args, " ")
	for _, fragment := range []string{
		"--tools read,grep,find,ls,write,edit",
		"-e " + extPath,
		"-na", "-nc",
	} {
		if !strings.Contains(args, fragment) {
			t.Errorf("args %q missing %q", args, fragment)
		}
	}
	onDisk, err := os.ReadFile(extPath)
	if err != nil {
		t.Fatalf("extension file: %v", err)
	}
	if string(onDisk) != string(permissionsExtension) {
		t.Error("on-disk extension must be a verbatim copy of the embedded source")
	}
}

// The embedded extension carries the protocol the Go side parses: the title
// prefix, the four decision values, the gated write surface, and a block
// path. A drifted embed fails here before it fails in the field.
func TestPermissionsExtension_Golden(t *testing.T) {
	src := string(permissionsExtension)
	if len(src) == 0 {
		t.Fatal("embedded extension is empty")
	}
	for _, needle := range []string{
		`"workmax-approval:"`,
		`"workmax-blocked:"`,
		`"allow_once"`, `"allow_session"`, `"allow_always"`, `"deny"`,
		`"write"`, `"edit"`,
		"block: true",
		`pi.on("tool_call"`,
		"ctx.ui.select",
		// The reason codes the Go side renders. A rename on one side only is
		// how a denial silently becomes the generic sentence.
		`"traversal"`, `"sensitive"`, `"outside"`,
		// The guard reads the workspace the sidecar hands it, not only the
		// one Node reports after resolving symlinks.
		"WORKMAX_PI_WORKSPACE",
	} {
		if !strings.Contains(src, needle) {
			t.Errorf("embedded extension missing %s", needle)
		}
	}
}

// ---------------------------------------------------------------------------
// The approval flow over the extension_ui seam
// ---------------------------------------------------------------------------

// The full ask round-trip: ui request → EventApprovalReq (normalized tool
// name, parsed target) → Resolve(allow_once) → extension_ui_response with the
// decision on stdin, and no denial event.
func TestRunTurn_Approval_AllowOnce(t *testing.T) {
	rt, proc, pw := newInteractiveRuntime(t)
	broker := agentruntime.NewApprovalBroker()
	var persisted []string
	in := turnInput()
	in.Approvals = piApprovalConfig(broker, &persisted, 5*time.Second)
	sink := newEventChanSink()
	done := make(chan error, 1)
	go func() { done <- rt.RunTurn(context.Background(), in, sink.emit) }()

	writeFrame(t, pw, promptOK)
	writeFrame(t, pw, uiSelect("ui-1", "workmax-approval:write:notes.md"))
	ev := waitEvent(t, sink, agentruntime.EventApprovalReq)
	if ev.Tool.Name != "Write" || ev.Tool.Target != "notes.md" {
		t.Errorf("approval event tool = %+v, want Write · notes.md (normalized from pi's lowercase)", ev.Tool)
	}
	if !broker.Resolve(testTurnUUID, ev.ApprovalID, agentruntime.ApprovalAllowOnce) {
		t.Fatal("Resolve found nothing pending")
	}
	writeFrame(t, pw, `{"type":"agent_settled"}`)
	_ = pw.Close()
	if err := <-done; err != nil {
		t.Fatalf("RunTurn: %v", err)
	}

	res := uiResponses(t, proc)["ui-1"]
	if res == nil || res["value"] != "allow_once" {
		t.Errorf("ui-1 response = %v, want value allow_once", res)
	}
	for _, k := range sink.kinds() {
		if k == string(agentruntime.EventToolDenied) {
			t.Error("allow_once must not emit tool_denied")
		}
	}
	if broker.SessionGranted(testThreadUUID, "Write") {
		t.Error("allow_once must not record a session grant")
	}
}

// A session grant (recorded under the normalized Claude name, as the claude
// engine would record it) short-circuits: no card, immediate allow_once.
func TestRunTurn_Approval_SessionGrantShortCircuits(t *testing.T) {
	rt, _, proc := newTestRuntime(t, frames(
		promptOK,
		uiSelect("ui-1", "workmax-approval:write:out.md"),
		`{"type":"agent_settled"}`,
	), nil)
	broker := agentruntime.NewApprovalBroker()
	broker.GrantSession(testThreadUUID, "Write")
	var persisted []string
	in := turnInput()
	in.Approvals = piApprovalConfig(broker, &persisted, time.Second)
	sink := newEventChanSink()
	if err := rt.RunTurn(context.Background(), in, sink.emit); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	res := uiResponses(t, proc)["ui-1"]
	if res == nil || res["value"] != "allow_once" {
		t.Errorf("ui-1 response = %v, want the allow_once short-circuit", res)
	}
	for _, k := range sink.kinds() {
		if k == string(agentruntime.EventApprovalReq) {
			t.Error("a session grant must not raise a card")
		}
	}
}

// AutoAllowed (stored "always" rules land here) short-circuits the same way.
func TestRunTurn_Approval_AutoAllowedShortCircuits(t *testing.T) {
	rt, _, proc := newTestRuntime(t, frames(
		promptOK,
		uiSelect("ui-1", "workmax-approval:edit:cfg.md"),
		`{"type":"agent_settled"}`,
	), nil)
	var persisted []string
	in := turnInput()
	in.Approvals = piApprovalConfig(agentruntime.NewApprovalBroker(), &persisted, time.Second)
	in.Approvals.AutoAllowed["Edit"] = true
	sink := newEventChanSink()
	if err := rt.RunTurn(context.Background(), in, sink.emit); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if res := uiResponses(t, proc)["ui-1"]; res == nil || res["value"] != "allow_once" {
		t.Errorf("ui-1 response = %v, want allow_once without a card", res)
	}
	for _, k := range sink.kinds() {
		if k == string(agentruntime.EventApprovalReq) {
			t.Error("an always-allowed tool must not raise a card")
		}
	}
}

// Deny answers the extension with "deny" and narrates a tool_denied event.
func TestRunTurn_Approval_Deny(t *testing.T) {
	rt, proc, pw := newInteractiveRuntime(t)
	broker := agentruntime.NewApprovalBroker()
	var persisted []string
	in := turnInput()
	in.Approvals = piApprovalConfig(broker, &persisted, 5*time.Second)
	sink := newEventChanSink()
	done := make(chan error, 1)
	go func() { done <- rt.RunTurn(context.Background(), in, sink.emit) }()

	writeFrame(t, pw, promptOK)
	writeFrame(t, pw, uiSelect("ui-1", "workmax-approval:write:secrets.md"))
	ev := waitEvent(t, sink, agentruntime.EventApprovalReq)
	if !broker.Resolve(testTurnUUID, ev.ApprovalID, agentruntime.ApprovalDeny) {
		t.Fatal("Resolve found nothing pending")
	}
	denied := waitEvent(t, sink, agentruntime.EventToolDenied)
	if denied.Tool.Name != "Write" || denied.Tool.Target != "secrets.md" {
		t.Errorf("tool_denied = %+v", denied.Tool)
	}
	writeFrame(t, pw, `{"type":"agent_settled"}`)
	_ = pw.Close()
	if err := <-done; err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if res := uiResponses(t, proc)["ui-1"]; res == nil || res["value"] != "deny" {
		t.Errorf("ui-1 response = %v, want value deny", res)
	}
}

// Nobody answering is a denial, not a hang: the broker timeout answers the
// extension with "deny" and narrates a tool_denied event.
func TestRunTurn_Approval_TimeoutDenies(t *testing.T) {
	rt, _, proc := newTestRuntime(t, frames(
		promptOK,
		uiSelect("ui-1", "workmax-approval:write:slow.md"),
		`{"type":"agent_settled"}`,
	), nil)
	broker := agentruntime.NewApprovalBroker()
	var persisted []string
	in := turnInput()
	in.Approvals = piApprovalConfig(broker, &persisted, 30*time.Millisecond)
	sink := newEventChanSink()
	start := time.Now()
	if err := rt.RunTurn(context.Background(), in, sink.emit); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("timeout did not bound the wait")
	}
	if res := uiResponses(t, proc)["ui-1"]; res == nil || res["value"] != "deny" {
		t.Errorf("ui-1 response = %v, want value deny on timeout", res)
	}
	sawDenied := false
	for _, k := range sink.kinds() {
		sawDenied = sawDenied || k == string(agentruntime.EventToolDenied)
	}
	if !sawDenied {
		t.Error("timeout must narrate a tool_denied event")
	}
}

// allow_session and allow_always apply the shared grant bookkeeping: the
// session grant short-circuits the next card, allow_always also persists
// under the normalized name both engines share.
func TestRunTurn_Approval_SessionAndAlwaysGrants(t *testing.T) {
	rt, proc, pw := newInteractiveRuntime(t)
	broker := agentruntime.NewApprovalBroker()
	var persisted []string
	in := turnInput()
	in.Approvals = piApprovalConfig(broker, &persisted, 5*time.Second)
	sink := newEventChanSink()
	done := make(chan error, 1)
	go func() { done <- rt.RunTurn(context.Background(), in, sink.emit) }()

	writeFrame(t, pw, promptOK)
	writeFrame(t, pw, uiSelect("ui-1", "workmax-approval:write:a.md"))
	ev := waitEvent(t, sink, agentruntime.EventApprovalReq)
	if !broker.Resolve(testTurnUUID, ev.ApprovalID, agentruntime.ApprovalAllowSession) {
		t.Fatal("Resolve found nothing pending")
	}
	// The session grant answers the second write without a card.
	writeFrame(t, pw, uiSelect("ui-2", "workmax-approval:write:b.md"))
	// A different tool still asks; allow_always persists it.
	writeFrame(t, pw, uiSelect("ui-3", "workmax-approval:edit:c.md"))
	ev3 := waitEvent(t, sink, agentruntime.EventApprovalReq)
	if ev3.Tool.Name != "Edit" {
		t.Fatalf("second card = %+v, want Edit", ev3.Tool)
	}
	if !broker.Resolve(testTurnUUID, ev3.ApprovalID, agentruntime.ApprovalAllowAlways) {
		t.Fatal("Resolve found nothing pending")
	}
	writeFrame(t, pw, `{"type":"agent_settled"}`)
	_ = pw.Close()
	if err := <-done; err != nil {
		t.Fatalf("RunTurn: %v", err)
	}

	res := uiResponses(t, proc)
	if r := res["ui-1"]; r == nil || r["value"] != "allow_session" {
		t.Errorf("ui-1 = %v, want allow_session", r)
	}
	if r := res["ui-2"]; r == nil || r["value"] != "allow_once" {
		t.Errorf("ui-2 = %v, want the session-grant short-circuit", r)
	}
	if r := res["ui-3"]; r == nil || r["value"] != "allow_always" {
		t.Errorf("ui-3 = %v, want allow_always", r)
	}
	if !broker.SessionGranted(testThreadUUID, "Write") || !broker.SessionGranted(testThreadUUID, "Edit") {
		t.Error("grants must be recorded under the normalized Claude names")
	}
	if len(persisted) != 1 || persisted[0] != "Edit" {
		t.Errorf("persisted = %v, want [Edit]", persisted)
	}
}

// ---------------------------------------------------------------------------
// Foreign ui requests and the nil-approvals baseline
// ---------------------------------------------------------------------------

// UI requests that are not our envelope get their protocol's refusal so
// nothing ever hangs: confirm → confirmed:false, select/input/editor →
// cancelled:true, fire-and-forget → no response at all.
func TestRunTurn_Approval_ForeignUIRequestsAnswered(t *testing.T) {
	rt, _, proc := newTestRuntime(t, frames(
		promptOK,
		`{"type":"extension_ui_request","id":"ui-c","method":"confirm","title":"Delete?","message":"sure?"}`,
		`{"type":"extension_ui_request","id":"ui-s","method":"select","title":"Pick one:","options":["A","B"]}`,
		`{"type":"extension_ui_request","id":"ui-i","method":"input","title":"Name:"}`,
		`{"type":"extension_ui_request","id":"ui-e","method":"editor","title":"Edit:"}`,
		`{"type":"extension_ui_request","id":"ui-n","method":"notify","message":"done"}`,
		`{"type":"extension_ui_request","id":"ui-w","method":"setWidget","widgetKey":"k"}`,
		`{"type":"agent_settled"}`,
	), nil)
	in := turnInput()
	in.Approvals = piApprovalConfig(agentruntime.NewApprovalBroker(), &[]string{}, time.Second)
	sink := newEventChanSink()
	if err := rt.RunTurn(context.Background(), in, sink.emit); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	res := uiResponses(t, proc)
	if r := res["ui-c"]; r == nil || r["confirmed"] != false {
		t.Errorf("confirm response = %v, want confirmed:false", r)
	}
	for _, id := range []string{"ui-s", "ui-i", "ui-e"} {
		if r := res[id]; r == nil || r["cancelled"] != true {
			t.Errorf("%s response = %v, want cancelled:true", id, r)
		}
	}
	for _, id := range []string{"ui-n", "ui-w"} {
		if _, ok := res[id]; ok {
			t.Errorf("fire-and-forget %s must not be answered", id)
		}
	}
	for _, k := range sink.kinds() {
		if k == string(agentruntime.EventApprovalReq) || k == string(agentruntime.EventToolDenied) {
			t.Errorf("foreign ui requests must not touch the approval flow, saw %s", k)
		}
	}
}

// Without approvals nothing changes from the pre-R3 shape: read-only profile,
// no -e, no extension file, and ui requests are ignored without an answer.
func TestRunTurn_NilApprovals_KeepsLegacyBehavior(t *testing.T) {
	rt, spec, proc := newTestRuntime(t, frames(
		promptOK,
		uiSelect("ui-1", "workmax-approval:write:notes.md"),
		`{"type":"extension_ui_request","id":"ui-c","method":"confirm","title":"Delete?","message":"sure?"}`,
		`{"type":"agent_settled"}`,
	), nil)
	if err := rt.RunTurn(context.Background(), turnInput(), (&captured{}).emit); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	args := strings.Join(spec.Args, " ")
	if !strings.Contains(args, "--tools read,grep,find,ls") || strings.Contains(args, "write,edit") {
		t.Errorf("args = %q, want the read-only profile", args)
	}
	if strings.Contains(args, "-e ") {
		t.Errorf("args = %q, must not inject the extension", args)
	}
	if _, err := os.Stat(filepath.Join(spec.Env["PI_CODING_AGENT_DIR"], permissionsExtensionFile)); !os.IsNotExist(err) {
		t.Error("nil approvals must not write the extension file")
	}
	if len(uiResponses(t, proc)) != 0 {
		t.Errorf("stdin = %q, ui requests must be ignored without approvals", proc.stdin.String())
	}
}

// A path-guard block never reaches the approval flow — the extension refuses
// it on its own — but it MUST reach the user as a denial with the reason. The
// hole this closes was measured end to end: a write to a path outside the
// workspace came back to the renderer as a bare failed result, indis-
// tinguishable from a disk error, while the same call on the claude engine
// says "blocked — 路径在工作区之外".
func TestRunTurn_Approval_GuardBlockNarratesReason(t *testing.T) {
	rt, _, proc := newTestRuntime(t, frames(
		promptOK,
		`{"type":"extension_ui_request","id":"ui-b","method":"select","title":"workmax-blocked:write:escape.txt:outside","options":["ack"]}`,
		`{"type":"extension_ui_request","id":"ui-u","method":"select","title":"workmax-blocked:edit:x.txt:brand-new-code","options":["ack"]}`,
		`{"type":"agent_settled"}`,
	), nil)
	in := turnInput()
	in.Approvals = piApprovalConfig(agentruntime.NewApprovalBroker(), &[]string{}, time.Second)
	sink := newEventChanSink()
	if err := rt.RunTurn(context.Background(), in, sink.emit); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}

	var denials []agentruntime.ToolEvent
	for _, ev := range sink.events {
		if ev.Kind == agentruntime.EventToolDenied {
			denials = append(denials, ev.Tool)
		}
		if ev.Kind == agentruntime.EventApprovalReq {
			t.Error("a guard block must not raise an approval card")
		}
	}
	if len(denials) != 2 {
		t.Fatalf("tool_denied events = %d, want 2 (%+v)", len(denials), denials)
	}
	if denials[0].Name != "Write" || denials[0].Target != "escape.txt" || denials[0].Reason != "路径在工作区之外" {
		t.Errorf("first denial = %+v", denials[0])
	}
	// An unknown reason code (a newer extension against this sidecar) still
	// denies, with the generic sentence rather than a leaked identifier.
	if denials[1].Name != "Edit" || denials[1].Reason != pathGuardFallbackReason {
		t.Errorf("unknown-code denial = %+v", denials[1])
	}

	// The extension is released either way: an unanswered dialog blocks pi's
	// tool loop forever, and this one is not waiting on a human.
	res := uiResponses(t, proc)
	for _, id := range []string{"ui-b", "ui-u"} {
		if r := res[id]; r == nil || r["cancelled"] != true {
			t.Errorf("%s response = %v, want cancelled:true", id, r)
		}
	}
}
