//go:build desktop

// Package pi_agent is the Pi RPC engine behind the agentruntime seam: the
// second runtime of the dual-runtime plan (ProjectDocs/design/
// l2-agent-runtime-study-2026-08.md §4). Where local_agent drives the claude
// CLI through the Agent SDK, this package drives a `pi --mode rpc` subprocess
// over stdin/stdout JSONL and pumps its event stream into the same unified
// events — which is what finally gives openai_compatible endpoints a tool
// loop instead of L1 pure chat.
//
// Shape, deliberately narrow:
//   - one subprocess per turn (start → prompt → agent_settled → shutdown);
//     the process pool with idle reuse is a follow-up;
//   - without approvals: read-only tool surface (--tools read,grep,find,ls)
//     and extension_ui_request frames ignored — nothing asks;
//   - with approvals (TurnInput.Approvals != nil): the R3 extension bridge in
//     approvals.go widens the profile to write/edit and answers the
//     extension's ui requests through the shared approval flow.
//
// Protocol authority is pi's src/modes/rpc/rpc-types.ts and docs/rpc.md:
// strict LF-framed JSONL, responses routed by id (they may arrive out of
// order), everything unknown tolerated silently. The completion criterion is
// the agent_settled event — not agent_end (retries/queued work may follow it)
// and not the prompt response (which only means "accepted").
package pi_agent

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	agentruntime "server/desktop/agentruntime"
	cloudproxy "server/desktop/cloud_proxy"
)

// Config wires a Runtime.
type Config struct {
	// BinPath is the pi binary (WORKMAX_PI_PATH until packaging lands).
	BinPath string
	// DataDir is the sidecar data dir; pi_home (config, models.json) and
	// pi_sessions (session JSONL files) live under it, so a turn never
	// touches the user's ~/.pi.
	DataDir string
	// WorkspaceRoot is the parent of per-thread workspaces, shared with the
	// claude engine so a thread sees the same files whichever runtime runs.
	WorkspaceRoot string
}

// Runtime implements agentruntime.Runtime on a pi RPC subprocess.
type Runtime struct {
	cfg   Config
	start procStarter // nil = startPiProcess; tests inject a fake
}

// NewRuntime builds the pi engine. It does not validate BinPath here —
// RunTurn stats it per turn, so a binary that appears (or vanishes) after
// boot is handled at the moment it matters.
func NewRuntime(cfg Config) *Runtime { return &Runtime{cfg: cfg} }

// Name implements agentruntime.Runtime.
func (r *Runtime) Name() string { return "pi" }

// WorkspaceRoot lets local_agent.Engine place per-thread workspaces (its
// workspaceRooter seam).
func (r *Runtime) WorkspaceRoot() string { return r.cfg.WorkspaceRoot }

// readOnlyToolProfile is the pre-approval tool allowlist: pi's read surface
// only. Approval mode switches to approvalToolProfile (approvals.go).
const readOnlyToolProfile = "read,grep,find,ls"

// promptCommandID correlates our one prompt command with its response frame.
const promptCommandID = "workmax-turn"

// stdoutBufferSize is the initial JSONL read buffer. Frames can reach MB
// scale (tool results, base64 images); ReadBytes grows past this on demand,
// the size just avoids repeated regrowth on big frames.
const stdoutBufferSize = 16 << 20

// shutdownGrace bounds the wait for a clean exit after stdin EOF on the
// settled path.
const shutdownGrace = 3 * time.Second

// RunTurn implements agentruntime.Runtime: one subprocess, one prompt, pump
// events until agent_settled.
func (r *Runtime) RunTurn(ctx context.Context, in agentruntime.TurnInput, emit agentruntime.EmitFunc) error {
	if info, statErr := os.Stat(r.cfg.BinPath); statErr != nil || info.IsDir() {
		return &agentruntime.RuntimeError{
			Kind:      cloudproxy.KindServiceUnavailable,
			Message:   "pi 引擎二进制不可用",
			Retryable: false,
			Details:   map[string]any{"bin": r.cfg.BinPath},
		}
	}
	piHome := filepath.Join(r.cfg.DataDir, "pi_home")
	sessionDir := filepath.Join(r.cfg.DataDir, "pi_sessions")
	for _, dir := range []string{piHome, sessionDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return &agentruntime.RuntimeError{
				Kind:      cloudproxy.KindServiceUnavailable,
				Message:   "无法创建 pi 数据目录",
				Retryable: false,
				Details:   map[string]any{"reason": err.Error()},
			}
		}
	}
	// The endpoint config may change between turns (settings edits), so the
	// provider file is regenerated every turn; pi reads it at startup.
	if err := writeModelsJSON(piHome, in); err != nil {
		return &agentruntime.RuntimeError{
			Kind:      cloudproxy.KindServiceUnavailable,
			Message:   "无法写入 pi 模型配置",
			Retryable: false,
			Details:   map[string]any{"reason": err.Error()},
		}
	}

	// The session file path is the continuity handle: pi's SessionManager
	// opens an existing path or starts a fresh session at a new one, so the
	// ref is decided here at startup, not parsed from output.
	sessionPath := in.SessionRef
	if sessionPath == "" {
		sessionPath = newSessionPath(sessionDir)
	}

	// Approval mode (R3): write surface enabled, gated by the embedded
	// extension. The file is rewritten every turn — like models.json, the
	// on-disk copy is a projection of the binary, never a source of truth.
	toolProfile := readOnlyToolProfile
	var extensionArgs []string
	if in.Approvals != nil {
		toolProfile = approvalToolProfile
		extPath := filepath.Join(piHome, permissionsExtensionFile)
		if err := os.WriteFile(extPath, permissionsExtension, 0o644); err != nil {
			return &agentruntime.RuntimeError{
				Kind:      cloudproxy.KindServiceUnavailable,
				Message:   "无法写入 pi 审批扩展",
				Retryable: false,
				Details:   map[string]any{"reason": err.Error()},
			}
		}
		extensionArgs = []string{"-e", extPath}
	}

	stderr := &tailBuffer{}
	start := r.start
	if start == nil {
		start = startPiProcess
	}
	proc, serr := start(ctx, procSpec{
		Bin: r.cfg.BinPath,
		Args: append([]string{
			"--mode", "rpc",
			"--session", sessionPath,
			"--session-dir", sessionDir,
			"--provider", "workmax",
			"--model", in.ModelID,
			"--tools", toolProfile,
			"-na", // --no-approve: trust decisions are ours, not pi's
			"-nc", // --no-context-files: no AGENTS.md surprises in the prompt
		}, extensionArgs...),
		Env: map[string]string{
			// Local-first三件套 + config isolation: no version checks, no
			// telemetry, and pi's config dir is ours, not the user's ~/.pi.
			"PI_OFFLINE":            "1",
			"PI_TELEMETRY":          "0",
			"PI_SKIP_VERSION_CHECK": "1",
			"PI_CODING_AGENT_DIR":   piHome,
			// The workspace as THIS side spells it. The extension's path guard
			// needs the sidecar's own string, not only the one Node reports:
			// process.cwd() resolves symlinks and this does not, so a data dir
			// under a linked path (macOS /tmp, $TMPDIR) gave the guard a root
			// that matched nothing and it refused every write into the thread's
			// own workspace.
			"WORKMAX_PI_WORKSPACE": in.Workspace,
		},
		Dir: in.Workspace,
	}, func(line string) {
		log.Printf("pi agent stderr: %s", line)
		stderr.add(line)
	})
	if serr != nil {
		if errors.Is(serr, context.Canceled) || errors.Is(serr, context.DeadlineExceeded) {
			return serr
		}
		return &agentruntime.RuntimeError{
			Kind:      cloudproxy.KindServiceUnavailable,
			Message:   "无法启动 pi 工具循环",
			Retryable: true,
			Details:   map[string]any{"reason": serr.Error()},
		}
	}
	defer func() {
		proc.Kill()
		_ = proc.Wait() // reap; returns the cached result if already waited
	}()

	// Report the ref immediately: it is known now, and the caller only
	// stores it after a clean turn (SSE bridge records, engine decides).
	if werr := emit(agentruntime.Event{Kind: agentruntime.EventSessionRef, SessionRef: sessionPath}); werr != nil {
		return werr
	}

	if werr := writeCommand(proc.Stdin(), map[string]any{
		"id":      promptCommandID,
		"type":    "prompt",
		"message": in.Prompt,
	}); werr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &agentruntime.RuntimeError{
			Kind:      cloudproxy.KindServiceUnavailable,
			Message:   "无法向 pi 进程发送请求",
			Retryable: true,
			Details:   map[string]any{"reason": werr.Error()},
		}
	}

	settled, perr := pump(ctx, proc.Stdout(), &framePump{
		pending:   map[string]bool{promptCommandID: true},
		emit:      emit,
		approvals: in.Approvals,
		stdin:     proc.Stdin(),
	})
	if perr != nil {
		return perr
	}
	if !settled {
		// Stream ended before agent_settled: cancellation (ctx kill closes
		// the pipes) or a subprocess death mid-turn.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		details := map[string]any{}
		if werr := proc.Wait(); werr != nil {
			details["exit"] = werr.Error()
		}
		if tail := stderr.String(); tail != "" {
			details["stderr"] = tail
		}
		return &agentruntime.RuntimeError{
			Kind:      cloudproxy.KindServiceUnavailable,
			Message:   "pi 工具循环在完成前退出",
			Retryable: true,
			Details:   details,
		}
	}
	// Settled: stdin EOF asks pi to exit cleanly; a laggard is killed. The
	// turn's result is already on the wire either way.
	_ = proc.Stdin().Close()
	waitWithTimeout(proc, shutdownGrace)
	return nil
}

// ---------------------------------------------------------------------------
// JSONL codec + three-way dispatch
// ---------------------------------------------------------------------------

// piFrame is the superset of every stdout frame field this runtime reads.
// Unknown fields — and whole unknown frame types — fall through untouched:
// pi's event vocabulary is larger than its docs (rpc-types.ts is authority,
// and even that trails the code), so tolerance is part of the protocol.
type piFrame struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Command string `json:"command"`
	Success *bool  `json:"success"`
	Error   string `json:"error"`

	// message_update
	AssistantMessageEvent *assistantEvent `json:"assistantMessageEvent"`

	// message_end carries an assistantMessage object here — but the same key
	// is a plain string on confirm/notify extension_ui_request frames, so it
	// stays raw and is decoded only where the object shape is expected.
	Message json.RawMessage `json:"message"`

	// tool_execution_end
	ToolName string `json:"toolName"`
	IsError  bool   `json:"isError"`

	// extension_ui_request (approvals.go); ID doubles as the response key.
	Method string `json:"method"`
	Title  string `json:"title"`
}

type assistantEvent struct {
	Type     string    `json:"type"`
	Delta    string    `json:"delta"`
	ToolCall *toolCall `json:"toolCall"`
}

type toolCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type assistantMessage struct {
	StopReason   string `json:"stopReason"`
	ErrorMessage string `json:"errorMessage"`
}

// writeCommand frames one command as strict JSONL: JSON, then a single LF.
func writeCommand(w io.Writer, cmd map[string]any) error {
	raw, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	_, err = w.Write(append(raw, '\n'))
	return err
}

// pump reads stdout frames until agent_settled, EOF, or a failure. Returns
// settled=true on the one good outcome. Frames are split on LF only, with a
// trailing CR stripped — pi's jsonl.ts contract; generic line readers that
// also split on U+2028/U+2029 would corrupt JSON strings.
func pump(ctx context.Context, stdout io.Reader, p *framePump) (bool, error) {
	reader := bufio.NewReaderSize(stdout, stdoutBufferSize)
	for {
		line, rerr := reader.ReadBytes('\n')
		line = trimFrame(line)
		if len(line) > 0 {
			settled, derr := p.dispatch(ctx, line)
			if derr != nil || settled {
				return settled, derr
			}
		}
		if rerr != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			if errors.Is(rerr, io.EOF) {
				return false, nil // caller decides what an early EOF means
			}
			return false, &agentruntime.RuntimeError{
				Kind:      cloudproxy.KindServiceUnavailable,
				Message:   "读取 pi 输出失败",
				Retryable: true,
				Details:   map[string]any{"reason": rerr.Error()},
			}
		}
	}
}

func trimFrame(line []byte) []byte {
	for len(line) > 0 && (line[len(line)-1] == '\n' || line[len(line)-1] == '\r') {
		line = line[:len(line)-1]
	}
	return line
}

// dispatch is the three-way split from the design doc: responses by id,
// extension_ui_request through the approval bridge (or ignored without
// approvals — the pre-R3 behavior, when no extension is loaded), everything
// else an event. Unknown types, unknown events, and unparseable lines are all
// tolerated silently.
func (p *framePump) dispatch(ctx context.Context, line []byte) (bool, error) {
	emit := p.emit
	var f piFrame
	if err := json.Unmarshal(line, &f); err != nil {
		log.Printf("pi agent: ignoring non-JSON stdout line (%d bytes)", len(line))
		return false, nil
	}
	switch f.Type {
	case "response":
		if !p.pending[f.ID] {
			return false, nil // out-of-order or foreign id: not ours to fail on
		}
		delete(p.pending, f.ID)
		if f.Success == nil || !*f.Success {
			// The prompt was rejected before acceptance. Failures *after*
			// acceptance never come back on this channel — they ride the
			// event stream (message_end stopReason, or a dead process).
			msg := "pi 拒绝了本轮请求"
			details := map[string]any{}
			if f.Error != "" {
				details["reason"] = f.Error
			}
			return false, &agentruntime.RuntimeError{
				Kind:      cloudproxy.KindBadRequest,
				Message:   msg,
				Retryable: false,
				Details:   details,
			}
		}
		return false, nil
	case "extension_ui_request":
		if p.approvals == nil {
			// Read-only profile, no extension loaded: nothing of ours can ask,
			// and ignoring foreign chatter is the pre-R3 behavior.
			return false, nil
		}
		return false, p.handleUIRequest(ctx, &f)
	case "message_update":
		ev := f.AssistantMessageEvent
		if ev == nil {
			return false, nil
		}
		switch ev.Type {
		case "text_delta":
			if ev.Delta == "" {
				return false, nil
			}
			return false, emit(agentruntime.Event{Kind: agentruntime.EventTextDelta, Delta: ev.Delta})
		case "thinking_delta":
			if ev.Delta == "" {
				return false, nil
			}
			return false, emit(agentruntime.Event{Kind: agentruntime.EventThinkingDelta, Delta: ev.Delta})
		case "toolcall_end":
			if ev.ToolCall == nil {
				return false, nil
			}
			return false, emit(agentruntime.Event{
				Kind: agentruntime.EventToolUse,
				Tool: agentruntime.ToolEvent{Name: claudeToolName(ev.ToolCall.Name), Target: toolTarget(ev.ToolCall.Arguments)},
			})
		}
		return false, nil
	case "tool_execution_end":
		return false, emit(agentruntime.Event{
			Kind: agentruntime.EventToolResult,
			Tool: agentruntime.ToolEvent{Name: claudeToolName(f.ToolName), IsError: f.IsError},
		})
	case "message_end":
		var end assistantMessage
		if len(f.Message) > 0 {
			// Tolerant: an unparseable message object is somebody's new shape,
			// not a turn failure.
			_ = json.Unmarshal(f.Message, &end)
		}
		if end.StopReason == "error" {
			msg := strings.TrimSpace(end.ErrorMessage)
			if msg == "" {
				msg = "pi 模型调用失败"
			}
			return false, &agentruntime.RuntimeError{
				Kind:      cloudproxy.KindUnknown,
				Message:   msg,
				Retryable: true,
			}
		}
		return false, nil
	case "agent_settled":
		// The completion criterion: not agent_end (a retry or queued
		// continuation may follow), only settled means done.
		return true, nil
	}
	return false, nil
}

// toolTarget mirrors local_agent's: the BASENAME of the file a call touches
// (≤80 chars), enough to read "read · notes.md" while full paths and every
// other argument stay out of the stream.
func toolTarget(args map[string]any) string {
	for _, key := range []string{"path", "file_path", "filepath"} {
		if raw, ok := args[key].(string); ok && raw != "" {
			base := filepath.Base(raw)
			if len(base) > 80 {
				base = base[:80]
			}
			return base
		}
	}
	return ""
}

// newSessionPath names a fresh session file. Random, not thread-derived:
// the thread↔ref binding lives in w_desktop_agent_session, and a cleared ref
// must start a genuinely new session rather than resurrecting the old file.
func newSessionPath(sessionDir string) string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return filepath.Join(sessionDir, fmt.Sprintf("workmax-%d.jsonl", time.Now().UnixNano()))
	}
	return filepath.Join(sessionDir, fmt.Sprintf("workmax-%d-%s.jsonl", time.Now().UnixMilli(), hex.EncodeToString(buf[:])))
}

// tailBuffer keeps the last few stderr lines for error details. Single
// writer (the stderr scanner goroutine) but read from the turn goroutine on
// failure, so it locks.
type tailBuffer struct {
	mu    sync.Mutex
	lines []string
}

const tailKeep = 20

func (t *tailBuffer) add(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, line)
	if len(t.lines) > tailKeep {
		t.lines = t.lines[len(t.lines)-tailKeep:]
	}
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines, "\n")
}
