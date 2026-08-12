//go:build desktop

package pi_agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	agentruntime "server/desktop/agentruntime"
	cloudproxy "server/desktop/cloud_proxy"
)

// ---------------------------------------------------------------------------
// Fake subprocess: scripted JSONL frames instead of a pi binary.
// ---------------------------------------------------------------------------

// closableBuffer captures stdin writes and records Close (= graceful EOF).
type closableBuffer struct {
	mu     sync.Mutex
	data   []byte
	closed bool
}

func (b *closableBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *closableBuffer) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	return nil
}

func (b *closableBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}

type fakeProc struct {
	stdin   *closableBuffer
	stdout  io.Reader
	waitErr error
	mu      sync.Mutex
	killed  bool
}

func (p *fakeProc) Stdin() io.WriteCloser { return p.stdin }
func (p *fakeProc) Stdout() io.Reader     { return p.stdout }
func (p *fakeProc) Wait() error           { return p.waitErr }
func (p *fakeProc) Kill() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.killed = true
}

// frames joins scripted stdout frames into an LF-framed stream.
func frames(lines ...string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// newTestRuntime returns a runtime whose starter feeds the scripted stdout,
// plus captures of the spec and the fake process.
func newTestRuntime(t *testing.T, stdout string, waitErr error) (*Runtime, *procSpec, *fakeProc) {
	t.Helper()
	dataDir := t.TempDir()
	bin := filepath.Join(dataDir, "pi")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rt := NewRuntime(Config{BinPath: bin, DataDir: dataDir, WorkspaceRoot: filepath.Join(dataDir, "ws")})
	spec := &procSpec{}
	proc := &fakeProc{stdin: &closableBuffer{}, stdout: strings.NewReader(stdout), waitErr: waitErr}
	rt.start = func(_ context.Context, s procSpec, _ func(string)) (procHandle, error) {
		*spec = s
		return proc, nil
	}
	return rt, spec, proc
}

func turnInput() agentruntime.TurnInput {
	return agentruntime.TurnInput{
		Prompt:    "count the files",
		Workspace: "/tmp/ws/thread_x",
		BaseURL:   "http://127.0.0.1:11434/v1",
		APIKey:    "sk-local",
		ModelID:   "qwen2.5-coder:7b",
	}
}

type captured struct {
	events []agentruntime.Event
	err    error // injected emit failure, nil normally
}

func (c *captured) emit(ev agentruntime.Event) error {
	c.events = append(c.events, ev)
	return c.err
}

func kinds(events []agentruntime.Event) []string {
	var out []string
	for _, ev := range events {
		out = append(out, string(ev.Kind))
	}
	return out
}

const promptOK = `{"id":"workmax-turn","type":"response","command":"prompt","success":true}`

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// The happy path: deltas stream, tool activity narrates, agent_settled ends
// the turn cleanly, and the session file path is reported up front.
func TestRunTurn_NormalTurn(t *testing.T) {
	rt, spec, proc := newTestRuntime(t, frames(
		promptOK,
		`{"type":"agent_start"}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","contentIndex":0,"delta":"mull"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_start","contentIndex":1}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":1,"delta":"He"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":1,"delta":"llo"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_end","contentIndex":2,"toolCall":{"type":"toolCall","id":"c1","name":"read","arguments":{"path":"/tmp/ws/thread_x/sub/notes.md"}}}}`,
		`{"type":"tool_execution_start","toolCallId":"c1","toolName":"read"}`,
		`{"type":"tool_execution_end","toolCallId":"c1","toolName":"read","isError":false,"result":{"content":[]}}`,
		`{"type":"message_end","message":{"role":"assistant","stopReason":"stop"}}`,
		`{"type":"agent_end","messages":[],"willRetry":false}`,
		`{"type":"agent_settled"}`,
	), nil)

	sink := &captured{}
	if err := rt.RunTurn(context.Background(), turnInput(), sink.emit); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}

	want := []string{"session_ref", "thinking_delta", "text_delta", "text_delta", "tool_use", "tool_result"}
	if got := kinds(sink.events); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("event kinds = %v, want %v", got, want)
	}
	ref := sink.events[0].SessionRef
	if !strings.HasSuffix(ref, ".jsonl") || !strings.Contains(ref, "pi_sessions") {
		t.Errorf("session ref = %q, want a fresh .jsonl under pi_sessions", ref)
	}
	// Both ends of the call carry the SHARED tool vocabulary (Claude's), not
	// pi's native lowercase: the renderer folds an approval question and a
	// denial into the step they belong to by matching (name, target), and a
	// call whose announcement says "read" while its question says "Read" is a
	// call the fold can never find.
	if tool := sink.events[4].Tool; tool.Name != "Read" || tool.Target != "notes.md" {
		t.Errorf("tool_use = %+v, want Read · notes.md", tool)
	}
	if tool := sink.events[5].Tool; tool.Name != "Read" || tool.IsError {
		t.Errorf("tool_result = %+v, want Read", tool)
	}

	// The prompt went down stdin as one JSONL command, and stdin was closed
	// (graceful shutdown) after settling.
	var cmd map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(proc.stdin.String())), &cmd); err != nil {
		t.Fatalf("stdin not one JSON line: %q (%v)", proc.stdin.String(), err)
	}
	if cmd["type"] != "prompt" || cmd["message"] != "count the files" || cmd["id"] != "workmax-turn" {
		t.Errorf("prompt command = %v", cmd)
	}
	if !proc.stdin.closed {
		t.Error("stdin must be closed after settle (graceful EOF shutdown)")
	}

	// Launch contract: RPC mode, our session paths, the workmax provider,
	// the read-only tool profile, and the isolation env.
	args := strings.Join(spec.Args, " ")
	for _, fragment := range []string{
		"--mode rpc", "--session " + ref, "--session-dir",
		"--provider workmax", "--model qwen2.5-coder:7b",
		"--tools read,grep,find,ls", "-na", "-nc",
	} {
		if !strings.Contains(args, fragment) {
			t.Errorf("args %q missing %q", args, fragment)
		}
	}
	if spec.Dir != "/tmp/ws/thread_x" {
		t.Errorf("cwd = %q, want the turn workspace", spec.Dir)
	}
	for k, v := range map[string]string{"PI_OFFLINE": "1", "PI_TELEMETRY": "0", "PI_SKIP_VERSION_CHECK": "1"} {
		if spec.Env[k] != v {
			t.Errorf("env %s = %q, want %q", k, spec.Env[k], v)
		}
	}
	// The extension's path guard is told the workspace in THIS side's
	// spelling. Node resolves symlinks and this does not, so a guard that
	// knew only process.cwd() refused every write into the thread's own
	// workspace whenever the data dir sat under a link.
	if spec.Env["WORKMAX_PI_WORKSPACE"] != "/tmp/ws/thread_x" {
		t.Errorf("env WORKMAX_PI_WORKSPACE = %q, want the turn workspace", spec.Env["WORKMAX_PI_WORKSPACE"])
	}

	// models.json landed in the pi home the env points at, carrying the
	// turn's endpoint as the workmax provider.
	raw, err := os.ReadFile(filepath.Join(spec.Env["PI_CODING_AGENT_DIR"], "models.json"))
	if err != nil {
		t.Fatalf("models.json: %v", err)
	}
	var doc struct {
		Providers map[string]struct {
			BaseURL string `json:"baseUrl"`
			API     string `json:"api"`
			APIKey  string `json:"apiKey"`
			Models  []struct {
				ID string `json:"id"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("models.json parse: %v", err)
	}
	p, ok := doc.Providers["workmax"]
	if !ok || p.BaseURL != "http://127.0.0.1:11434/v1" || p.API != "openai-completions" ||
		p.APIKey != "sk-local" || len(p.Models) != 1 || p.Models[0].ID != "qwen2.5-coder:7b" {
		t.Errorf("models.json = %s", raw)
	}
}

// A stored ref is reused verbatim as the --session path.
func TestRunTurn_ResumesStoredSessionPath(t *testing.T) {
	rt, spec, _ := newTestRuntime(t, frames(promptOK, `{"type":"agent_settled"}`), nil)
	in := turnInput()
	in.SessionRef = "/data/pi_sessions/workmax-1-aa.jsonl"
	sink := &captured{}
	if err := rt.RunTurn(context.Background(), in, sink.emit); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if got := strings.Join(spec.Args, " "); !strings.Contains(got, "--session /data/pi_sessions/workmax-1-aa.jsonl") {
		t.Errorf("args = %q, must resume the stored session path", got)
	}
	if sink.events[0].SessionRef != in.SessionRef {
		t.Errorf("reported ref = %q, want the resumed path", sink.events[0].SessionRef)
	}
}

// success:false on the prompt response fails the turn as a typed error;
// nothing after it is trusted.
func TestRunTurn_PromptRejected(t *testing.T) {
	rt, _, _ := newTestRuntime(t, frames(
		`{"id":"workmax-turn","type":"response","command":"prompt","success":false,"error":"agent is busy"}`,
		`{"type":"agent_settled"}`,
	), nil)
	err := rt.RunTurn(context.Background(), turnInput(), (&captured{}).emit)
	var re *agentruntime.RuntimeError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want RuntimeError", err)
	}
	if re.Kind != cloudproxy.KindBadRequest || re.Details["reason"] != "agent is busy" {
		t.Errorf("RuntimeError = %+v", re)
	}
}

// Responses may arrive out of order and for ids we never issued; only our
// prompt's id decides anything.
func TestRunTurn_OutOfOrderResponses(t *testing.T) {
	rt, _, _ := newTestRuntime(t, frames(
		`{"id":"someone-else","type":"response","command":"get_state","success":false,"error":"nope"}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"hi"}}`,
		promptOK, // arrives after events already started flowing
		`{"type":"agent_settled"}`,
	), nil)
	sink := &captured{}
	if err := rt.RunTurn(context.Background(), turnInput(), sink.emit); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	want := []string{"session_ref", "text_delta"}
	if got := kinds(sink.events); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("event kinds = %v, want %v", got, want)
	}
}

// Unknown frame types, unknown delta types, and non-JSON lines are somebody's
// new feature, not an error.
func TestRunTurn_UnknownFramesTolerated(t *testing.T) {
	rt, _, _ := newTestRuntime(t, frames(
		promptOK,
		`{"type":"session_info_changed","sessionName":"x"}`,
		`{"type":"entry_appended","entry":{}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"hologram_delta","delta":"??"}}`,
		`this line is not JSON at all`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"ok"}}`,
		`{"type":"agent_settled"}`,
	), nil)
	sink := &captured{}
	if err := rt.RunTurn(context.Background(), turnInput(), sink.emit); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	want := []string{"session_ref", "text_delta"}
	if got := kinds(sink.events); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("event kinds = %v, want %v", got, want)
	}
}

// message_end with stopReason "error" is a model-declared failure: the turn
// fails with the endpoint's message even though the process is still alive.
func TestRunTurn_ErrorStopReasonFailsTurn(t *testing.T) {
	rt, _, _ := newTestRuntime(t, frames(
		promptOK,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"half"}}`,
		`{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"429 quota exceeded"}}`,
		`{"type":"agent_settled"}`,
	), nil)
	err := rt.RunTurn(context.Background(), turnInput(), (&captured{}).emit)
	var re *agentruntime.RuntimeError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want RuntimeError", err)
	}
	if re.Message != "429 quota exceeded" {
		t.Errorf("message = %q, want the endpoint's error surfaced", re.Message)
	}
}

// A subprocess that dies before agent_settled is an infrastructure failure,
// with the exit and stderr tail in the details.
func TestRunTurn_EarlyExitFailsTurn(t *testing.T) {
	rt, _, _ := newTestRuntime(t, frames(
		promptOK,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"cut "}}`,
	), errors.New("exit status 1"))
	// Route a stderr line through the starter's callback.
	inner := rt.start
	rt.start = func(ctx context.Context, s procSpec, stderrLine func(string)) (procHandle, error) {
		stderrLine("FATAL: provider exploded")
		return inner(ctx, s, stderrLine)
	}
	err := rt.RunTurn(context.Background(), turnInput(), (&captured{}).emit)
	var re *agentruntime.RuntimeError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want RuntimeError", err)
	}
	if re.Kind != cloudproxy.KindServiceUnavailable {
		t.Errorf("kind = %v", re.Kind)
	}
	if re.Details["exit"] != "exit status 1" {
		t.Errorf("details.exit = %v", re.Details["exit"])
	}
	if got, _ := re.Details["stderr"].(string); !strings.Contains(got, "provider exploded") {
		t.Errorf("details.stderr = %q, want the stderr tail", got)
	}
}

// Cancellation returns the ctx error unwrapped — stopping is not a failure —
// and the subprocess is killed rather than orphaned.
func TestRunTurn_CancellationReturnsCtxErr(t *testing.T) {
	dataDir := t.TempDir()
	bin := filepath.Join(dataDir, "pi")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rt := NewRuntime(Config{BinPath: bin, DataDir: dataDir, WorkspaceRoot: dataDir})

	ctx, cancel := context.WithCancel(context.Background())
	pr, pw := io.Pipe()
	proc := &fakeProc{stdin: &closableBuffer{}, stdout: pr}
	rt.start = func(context.Context, procSpec, func(string)) (procHandle, error) { return proc, nil }
	go func() {
		_, _ = pw.Write([]byte(frames(
			promptOK,
			`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"part"}}`,
		)))
		cancel()
		// ctx kill closes the pipes in production; the fake mirrors it.
		_ = pw.Close()
	}()

	err := rt.RunTurn(ctx, turnInput(), (&captured{}).emit)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled through unwrapped", err)
	}
	proc.mu.Lock()
	defer proc.mu.Unlock()
	if !proc.killed {
		t.Error("cancelled turn must kill the subprocess")
	}
}

// A missing binary is a configuration state reported before any subprocess
// work happens.
func TestRunTurn_MissingBinaryIsTypedError(t *testing.T) {
	rt := NewRuntime(Config{BinPath: "/nonexistent/pi", DataDir: t.TempDir(), WorkspaceRoot: t.TempDir()})
	err := rt.RunTurn(context.Background(), turnInput(), (&captured{}).emit)
	var re *agentruntime.RuntimeError
	if !errors.As(err, &re) || re.Kind != cloudproxy.KindServiceUnavailable {
		t.Fatalf("err = %v, want service_unavailable RuntimeError", err)
	}
}

// An empty API key becomes the placeholder — pi hides keyless models
// entirely, and local endpoints ignore the credential anyway.
func TestModelsJSON_EmptyKeyGetsPlaceholder(t *testing.T) {
	rt, spec, _ := newTestRuntime(t, frames(promptOK, `{"type":"agent_settled"}`), nil)
	in := turnInput()
	in.APIKey = ""
	if err := rt.RunTurn(context.Background(), in, (&captured{}).emit); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(spec.Env["PI_CODING_AGENT_DIR"], "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), placeholderAPIKey) {
		t.Errorf("models.json must carry the placeholder key, got %s", raw)
	}
}

// The official-model path reaches pi as an ordinary endpoint: the sidecar's
// own loopback gateway, addressed with the in-memory gateway token as the
// "API key". Pinned here because pi is the one runtime that writes its
// credential to disk — if the provider file ever stopped being rebuilt from
// the turn's input, a rotated-away token would keep being replayed.
func TestModelsJSON_CarriesTheLoopbackGatewayEndpoint(t *testing.T) {
	rt, spec, _ := newTestRuntime(t, frames(promptOK, `{"type":"agent_settled"}`), nil)
	in := turnInput()
	in.BaseURL = "http://127.0.0.1:51999/model-gateway/openai"
	in.APIKey = "loopback-gateway-token"
	in.ModelID = "work-pro"
	if err := rt.RunTurn(context.Background(), in, (&captured{}).emit); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(spec.Env["PI_CODING_AGENT_DIR"], "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Providers struct {
			WorkMax struct {
				BaseURL string `json:"baseUrl"`
				APIKey  string `json:"apiKey"`
				Models  []struct {
					ID string `json:"id"`
				} `json:"models"`
			} `json:"workmax"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("models.json parse: %v (%s)", err, raw)
	}
	provider := doc.Providers.WorkMax
	if provider.BaseURL != in.BaseURL || provider.APIKey != in.APIKey ||
		len(provider.Models) != 1 || provider.Models[0].ID != in.ModelID {
		t.Fatalf("models.json = %s", raw)
	}
	// pi appends /chat/completions to the configured base, which is why the
	// gateway registers the unversioned spelling alongside the /v1 one.
	if strings.HasSuffix(provider.BaseURL, "/chat/completions") {
		t.Fatalf("the base URL must stay a base: %s", provider.BaseURL)
	}
}

// The first emit failure aborts the turn: a dead SSE writer means the client
// is gone.
func TestRunTurn_EmitErrorAborts(t *testing.T) {
	rt, _, _ := newTestRuntime(t, frames(promptOK, `{"type":"agent_settled"}`), nil)
	sinkErr := errors.New("client gone")
	err := rt.RunTurn(context.Background(), turnInput(), (&captured{err: sinkErr}).emit)
	if !errors.Is(err, sinkErr) {
		t.Fatalf("err = %v, want the emit error back", err)
	}
}

// A call pi refused for being outside --tools must read as "blocked, and here
// is why", not as "failed". pi's own answer is "Tool bash not found" with
// isError, which the sidecar used to forward verbatim as a tool_result: the
// user saw the same red row a disk error produces, while the claude engine
// explains its refusals. Same event, same sentence, either runtime.
func TestRunTurn_ToolOutsideTheProfileIsNarratedAsDenied(t *testing.T) {
	rt, _, _ := newTestRuntime(t, frames(
		promptOK,
		`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_end","contentIndex":0,"toolCall":{"type":"toolCall","id":"c1","name":"bash","arguments":{"command":"touch /tmp/escape"}}}}`,
		`{"type":"tool_execution_end","toolCallId":"c1","toolName":"bash","isError":true,"result":{"content":[{"type":"text","text":"Tool bash not found"}]}}`,
		`{"type":"agent_settled"}`,
	), nil)

	sink := &captured{}
	if err := rt.RunTurn(context.Background(), turnInput(), sink.emit); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}

	want := []string{"session_ref", "tool_use", "tool_denied"}
	if got := kinds(sink.events); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("event kinds = %v, want %v", got, want)
	}
	denied := sink.events[2].Tool
	if denied.Name != "Bash" {
		t.Errorf("denial names %q, want the shared spelling of the call it refuses", denied.Name)
	}
	if denied.Reason != agentruntime.OutsideSurfaceReason {
		t.Errorf("denial reason = %q, want the same sentence the claude engine uses", denied.Reason)
	}
}

// The other side of the same coin, and the one that must NOT be mistaken for a
// refusal: an ENABLED tool that genuinely failed. "Refused" and "tried and
// failed" mean different things to the person reading, and only one of them is
// theirs to fix.
func TestRunTurn_AnEnabledToolThatFailsStaysAnErroredResult(t *testing.T) {
	rt, _, _ := newTestRuntime(t, frames(
		promptOK,
		`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_end","contentIndex":0,"toolCall":{"type":"toolCall","id":"c1","name":"read","arguments":{"path":"/tmp/ws/thread_x/gone.md"}}}}`,
		`{"type":"tool_execution_end","toolCallId":"c1","toolName":"read","isError":true,"result":{"content":[{"type":"text","text":"ENOENT"}]}}`,
		`{"type":"agent_settled"}`,
	), nil)

	sink := &captured{}
	if err := rt.RunTurn(context.Background(), turnInput(), sink.emit); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}

	want := []string{"session_ref", "tool_use", "tool_result"}
	if got := kinds(sink.events); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("event kinds = %v, want %v", got, want)
	}
	if tool := sink.events[2].Tool; tool.Name != "Read" || !tool.IsError {
		t.Errorf("tool_result = %+v, want an errored Read", tool)
	}
}

// Approval mode enables write/edit, so a write that fails there is a failure,
// not a refusal — the profile the pump checks against has to be the one that
// actually went on argv, not the read-only default.
func TestRunTurn_TheDenialTestFollowsTheTurnsOwnProfile(t *testing.T) {
	in := turnInput()
	in.Approvals = &agentruntime.ApprovalConfig{
		Broker:      agentruntime.NewApprovalBroker(),
		TurnUUID:    "t-1",
		ThreadUUID:  "th-1",
		AutoAllowed: map[string]bool{},
		AskAllowed:  map[string]bool{"Write": true},
	}
	rt, _, _ := newTestRuntime(t, frames(
		promptOK,
		`{"type":"tool_execution_end","toolCallId":"c1","toolName":"write","isError":true,"result":{"content":[{"type":"text","text":"disk full"}]}}`,
		`{"type":"agent_settled"}`,
	), nil)

	sink := &captured{}
	if err := rt.RunTurn(context.Background(), in, sink.emit); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	want := []string{"session_ref", "tool_result"}
	if got := kinds(sink.events); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("event kinds = %v, want %v; write is enabled in approval mode", got, want)
	}
}
