//go:build desktop

// Package local_agent is the L2 turn runner: a real tool loop, run locally.
//
// L1 (local_inference) answers with text. This package hands the turn to the
// Claude Agent SDK, which drives a claude CLI subprocess through the full
// tool conversation — Read/Write/Edit in a per-thread workspace — against the
// user's own anthropic_compatible endpoint. The dispatch rule lives in the
// desktop package: protocol anthropic_compatible with a CLI available comes
// here; openai_compatible stays on L1 pure chat.
//
// Structure (R0 of the dual-runtime plan, ProjectDocs/design/
// l2-agent-runtime-study-2026-08.md): the SDK-facing pump is claudeRuntime,
// an agentruntime.Runtime that speaks unified events; Engine keeps the
// turn-level plumbing (profile, cache, prompt assembly, workspace) and drives
// the runtime through an agentruntime.SSEBridge. The Pi runtime lands beside
// claudeRuntime with the same seam.
//
// Every claim below the SDK call was kill-checked before this package was
// written (ProjectDocs/design/l2-sdk-cli-restart-2026-08.md): WithCLIPath
// spawns an explicit binary with no PATH discovery, env-injected base URL and
// key reach the subprocess, the CLI manages the multi-turn tool conversation
// itself, and tools execute in the WithCwd workspace. The isolation recipe in
// buildQueryOptions reproduces the spike's exactly.
package local_agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/gorm"

	claudesdk "github.com/jonnyquan/claude-agent-sdk-go/pkg/claudesdk"

	agentruntime "server/desktop/agentruntime"
	cloudproxy "server/desktop/cloud_proxy"
	localinference "server/desktop/local_inference"
)

// doneEventData mirrors local_inference's terminal event byte-for-byte: the
// renderer and the cache classifier must not be able to tell the routes apart
// by their terminal frame.
const doneEventData = `{"type":"done","result":"OK"}`

// maxAgentTurns bounds the tool conversation. A loop that has not converged
// in this many model round-trips is burning the user's tokens on drift, not
// closing in on an answer.
const maxAgentTurns = 24

// allowedTools is the L2a tool surface: file work inside the workspace.
// Bash is deliberately absent — a shell is an escape hatch from every path
// rule the other tools respect.
//
// This list is NOT self-enforcing. WithAllowedTools is an auto-approve list
// inside the CLI, not a restriction on what the model may call: composed with
// bypassPermissions it means "these need no prompt" while everything else is
// approved by the bypass anyway. Measured on the real CLI: a Bash call that
// the model invented ran `touch` on a path outside the workspace and the file
// landed. toolSurface below is what actually holds the line.
var allowedTools = []string{"Read", "Write", "Edit", "Glob", "Grep"}

// toolSurface is allowedTools as the PreToolUse hook enforces it: an
// allowlist, so a tool the CLI grows tomorrow (or a hostile endpoint asks for
// today) is denied by default rather than by omission from a blocklist.
var toolSurface = func() map[string]bool {
	set := make(map[string]bool, len(allowedTools))
	for _, t := range allowedTools {
		set[t] = true
	}
	return set
}()

// readOnlyTools is the CLAUDE CLI's pre-allowed list in approval mode — the
// subset that never asks, because reading the workspace is the loop's
// bloodstream and every call still passes the PreToolUse path guard.
//
// It is deliberately NOT the approval policy: it goes to WithAllowedTools, so
// it may name only tools this CLI actually has. The policy vocabulary is
// agentruntime.ApprovalReadSurface, which is the union across both runtimes.
var readOnlyTools = []string{"Read", "Glob", "Grep"}

// askTools is the claude CLI's write surface: these consult the user.
var askTools = []string{"Write", "Edit"}

// queryStarter is the seam between this engine and the SDK, so tests can
// script the message stream without a CLI. Production is claudesdk.Query.
type queryStarter func(ctx context.Context, prompt string, opts ...claudesdk.Option) (claudesdk.MessageIterator, error)

// Engine runs L2 tool-loop turns. It satisfies desktop.TurnRunner and sits
// behind exactly the same CacheWriter/SSE/intent seam as the other runners.
type Engine struct {
	profile   localinference.ProfileReader
	db        *gorm.DB
	loader    localinference.AttachmentLoader // 可 nil
	hooks     localinference.KnowledgeHooks   // 可 nil（RAG off）
	runtime   agentruntime.Runtime
	approvals *agentruntime.ApprovalBroker // nil = legacy pre-approved mode
}

// EnableApprovals switches the engine to interactive tool approvals through
// the given broker. Wired by bootstrap once the renderer's approval card is
// present; without it the engine keeps the kill-checked bypass recipe.
func (e *Engine) EnableApprovals(broker *agentruntime.ApprovalBroker) { e.approvals = broker }

// NewEngine wires the L2 runner. cliPath must point at a claude CLI binary;
// workspaceRoot is the parent under which per-thread workspaces are created
// (conventionally <DataDir>/agent_workspace).
func NewEngine(
	profile localinference.ProfileReader,
	db *gorm.DB,
	loader localinference.AttachmentLoader,
	hooks localinference.KnowledgeHooks,
	cliPath string,
	workspaceRoot string,
) *Engine {
	return &Engine{
		profile: profile,
		db:      db,
		loader:  loader,
		hooks:   hooks,
		runtime: &claudeRuntime{
			cliPath:       cliPath,
			workspaceRoot: workspaceRoot,
			query:         claudesdk.Query,
		},
	}
}

// NewEngineWithRuntime wires the L2 runner around an externally constructed
// runtime (the pi engine today). The runtime owns its own binary discovery
// (a missing binary is its RuntimeError to report) and its workspace root
// (the workspaceRooter seam); Engine keeps the turn plumbing.
func NewEngineWithRuntime(
	profile localinference.ProfileReader,
	db *gorm.DB,
	loader localinference.AttachmentLoader,
	hooks localinference.KnowledgeHooks,
	rt agentruntime.Runtime,
) *Engine {
	return &Engine{
		profile: profile,
		db:      db,
		loader:  loader,
		hooks:   hooks,
		runtime: rt,
	}
}

// claudeCfg returns the engine's runtime as its concrete type for wiring
// details (cliPath checks, test seams). Panics on a foreign runtime — only
// call it on engines built by NewEngine.
func (e *Engine) claudeCfg() *claudeRuntime { return e.runtime.(*claudeRuntime) }

// workspaceRooter is the seam through which Engine asks a runtime where
// per-thread workspaces live. claudeRuntime and pi_agent.Runtime both
// implement it; a runtime without one cannot host tool turns.
type workspaceRooter interface{ WorkspaceRoot() string }

// Chat runs one tool-loop turn. Same contract as the other runners: nil =
// clean completion (done event sent); non-nil = failure (proxy_error sent,
// except user cancellation, which is not an error to report).
func (e *Engine) Chat(ctx context.Context, req cloudproxy.ChatRequest, dst cloudproxy.SSEWriter) (err error) {
	_, baseURL, modelID, apiKey, perr := e.profile.LocalInferenceProfile()
	if perr != nil {
		// "Not signed in", "no official model chosen" and "gateway not ready"
		// are the resolver's to explain — the tool loop cannot improve on
		// them, and wrapping them in a generic message would bury the one
		// sentence the user can act on.
		if pe, typed := localinference.ProfileProxyError(perr); typed {
			return emitProxyError(dst, pe)
		}
		return emitProxyError(dst, cloudproxy.ProxyError{
			Kind:      cloudproxy.KindServiceUnavailable,
			Message:   "无法读取本地模型配置",
			Retryable: false,
			Details:   map[string]any{"reason": perr.Error()},
		})
	}
	if baseURL == "" || modelID == "" {
		return emitProxyError(dst, cloudproxy.ProxyError{
			Kind:      cloudproxy.KindServiceUnavailable,
			Message:   "本地模型未配置，请在设置中填写 base_url 与 model_id",
			Retryable: false,
		})
	}
	// The CLI check is claude-specific wiring: other runtimes (pi) stat
	// their own binary inside RunTurn and fail as a typed RuntimeError.
	if cr, ok := e.runtime.(*claudeRuntime); ok && cr.cliPath == "" {
		return emitProxyError(dst, cloudproxy.ProxyError{
			Kind:      cloudproxy.KindServiceUnavailable,
			Message:   "本地工具循环需要 claude CLI；未找到可用的 CLI 二进制",
			Retryable: false,
		})
	}

	requestID, ridErr := cloudproxy.DesktopTurnRequestID(req.TurnUUID)
	if ridErr != nil {
		return emitProxyError(dst, cloudproxy.ProxyError{
			Kind:    cloudproxy.KindBadRequest,
			Message: "invalid turn_uuid",
		})
	}
	cache, cerr := cloudproxy.NewCacheWriter(e.db, cloudproxy.CacheWriterParams{
		UID:                   req.UID,
		ThreadID:              req.ThreadID,
		ThreadUUID:            req.ThreadUUID,
		MessageIdempotencyKey: requestID,
		UserText:              req.UserText,
		ChatMode:              req.ChatMode,
	})
	if cerr != nil {
		return emitProxyError(dst, cloudproxy.ProxyError{
			Kind:      cloudproxy.KindServiceUnavailable,
			Message:   "本地缓存初始化失败",
			Retryable: false,
			Details:   map[string]any{"reason": cerr.Error()},
		})
	}
	defer func() { _ = cache.Finalize(err) }()

	// The stored session ref decides prompt shape: with one, the runtime's
	// own session replays prior turns and the prompt carries only this turn;
	// without one, history is flattened in.
	sessionRef := loadSessionRef(e.db, req.ThreadUUID, e.runtime.Name())

	prompt, aerr := e.assemblePrompt(ctx, req, dst, requestID, sessionRef != "")
	if aerr != nil {
		return emitProxyError(dst, cloudproxy.ProxyError{
			Kind:    cloudproxy.KindBadRequest,
			Message: "附件加载失败",
			Details: map[string]any{"reason": aerr.Error()},
		})
	}

	workspace, werr := e.ensureWorkspace(req.ThreadUUID)
	if werr != nil {
		return emitProxyError(dst, cloudproxy.ProxyError{
			Kind:      cloudproxy.KindServiceUnavailable,
			Message:   "无法创建线程工作区",
			Retryable: false,
			Details:   map[string]any{"reason": werr.Error()},
		})
	}

	bridge := agentruntime.NewSSEBridge(dst, cache)
	// Said before the turn runs, not after it: a turn that is interrupted or
	// fails still came from an engine, and the answer it left behind should
	// still be able to say which. Emitted here rather than by the runtime
	// because this is where both facts are certain — the Runtime is asked its
	// own name, and modelID is the value about to be handed to it.
	if merr := bridge.Emit(agentruntime.Event{
		Kind: agentruntime.EventTurnMeta,
		Turn: agentruntime.TurnMeta{Engine: e.runtime.Name(), Model: modelID},
	}); merr != nil {
		return merr
	}
	runErr := e.runtime.RunTurn(ctx, agentruntime.TurnInput{
		Prompt:     prompt,
		Workspace:  workspace,
		BaseURL:    baseURL,
		APIKey:     apiKey,
		ModelID:    modelID,
		SessionRef: sessionRef,
		Approvals:  e.approvalConfig(req),
	}, bridge.Emit)
	if runErr != nil {
		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			// User cancellation: the SDK tears the subprocess down (SIGTERM,
			// then SIGKILL). No proxy_error — stopping is not a failure to
			// report. The ref stays: the previous turn's session is still
			// the right resume target.
			return runErr
		}
		var re *agentruntime.RuntimeError
		if errors.As(runErr, &re) {
			_ = dst.WriteProxyError(cloudproxy.ProxyError{
				Kind:      re.Kind,
				Message:   re.Message,
				Retryable: re.Retryable,
				Details:   re.Details,
			})
		}
		if sessionRef != "" {
			// The turn that tried to resume failed. Whether the ref was the
			// cause or a bystander, dropping it moves the next turn onto the
			// flatten path, which always works — one turn of continuity is
			// the price of self-healing.
			clearSessionRef(e.db, req.ThreadUUID, e.runtime.Name())
		}
		return runErr
	}
	if werr := dst.WriteEvent(cloudproxy.SSEEvent{Type: "done", Data: doneEventData}); werr != nil {
		return werr
	}
	storeSessionRef(e.db, req.ThreadUUID, e.runtime.Name(), bridge.SessionRef())
	if e.hooks != nil {
		go e.indexCompletedTurn(req.UID, req.TurnUUID, req.UserText, bridge.AssistantText())
	}
	return nil
}

// approvalConfig assembles the turn's approval policy: the read surface plus
// every stored "always" grant auto-allow; the write surface asks. Nil when
// approvals are not enabled.
//
// The surfaces come from agentruntime, not from this file's claude lists: ONE
// Engine drives both runtimes (NewEngineWithRuntime hands it pi), so the policy
// must cover the union of what either can call. Narrowing it to the claude
// spellings is what left pi's find/ls in neither set — denied outright the
// moment anything routed them through Consult.
func (e *Engine) approvalConfig(req cloudproxy.ChatRequest) *agentruntime.ApprovalConfig {
	if e.approvals == nil {
		return nil
	}
	auto := make(map[string]bool, len(agentruntime.ApprovalReadSurface)+2)
	for _, t := range agentruntime.ApprovalReadSurface {
		auto[t] = true
	}
	for t := range loadAlwaysAllowed(e.db, req.UID) {
		auto[t] = true
	}
	ask := make(map[string]bool, len(agentruntime.ApprovalWriteSurface))
	for _, t := range agentruntime.ApprovalWriteSurface {
		ask[t] = true
	}
	uid := req.UID
	return &agentruntime.ApprovalConfig{
		Broker:      e.approvals,
		TurnUUID:    req.TurnUUID,
		ThreadUUID:  req.ThreadUUID,
		AutoAllowed: auto,
		AskAllowed:  ask,
		Persist:     func(tool string) { storeAlwaysAllow(e.db, uid, tool) },
	}
}

// assemblePrompt builds the single prompt string the CLI receives: retrieval
// context, then conversation history, then attachments, then the request.
// The CLI manages the intra-turn tool conversation itself (kill-check §1);
// what it cannot know is everything that happened before this turn — unless
// hasSession, in which case its own resumed session already holds the prior
// turns and flattened history would duplicate them.
func (e *Engine) assemblePrompt(ctx context.Context, req cloudproxy.ChatRequest, dst cloudproxy.SSEWriter, requestID string, hasSession bool) (string, error) {
	userText := req.UserText
	if e.hooks != nil {
		if found, rerr := e.hooks.Retrieve(ctx, req.UID, req.UserText, retrievalTopK); rerr != nil {
			log.Printf("knowledge: retrieve for turn %s: %v", req.TurnUUID, rerr)
		} else if len(found) > 0 {
			var used []localinference.RetrievedSource
			userText, used = localinference.AttachKnowledgeContext(req.UserText, found)
			localinference.EmitRetrievalEvent(dst, used)
		}
	}

	var b strings.Builder
	if hasSession {
		// Resumed session: history lives in the runtime.
	} else if history, herr := localinference.LoadThreadHistory(e.db, req.UID, req.ThreadID, requestID); herr != nil {
		log.Printf("local agent: history for thread %d: %v", req.ThreadID, herr)
	} else if len(history) > 0 {
		b.WriteString("Previous conversation in this thread:\n\n")
		for _, m := range history {
			if m.Role == "user" {
				b.WriteString("User: ")
			} else {
				b.WriteString("Assistant: ")
			}
			b.WriteString(m.Text)
			b.WriteString("\n\n")
		}
		b.WriteString("---\n\n")
	}

	if e.loader != nil && len(req.FileIDs) > 0 {
		atts, lerr := e.loader.Load(req.FileIDs, req.UID)
		if lerr != nil {
			return "", lerr
		}
		for _, att := range atts {
			// Text rides inline, as on L1. Images have no seat in a plain
			// prompt string; naming the gap beats dropping it silently.
			if att.Kind == "text" && att.Text != "" {
				fmt.Fprintf(&b, "Attached document (%s):\n%s\n\n", att.MimeType, att.Text)
			} else if att.Kind == "image" {
				b.WriteString("(An attached image could not be included: the local tool loop does not carry images yet.)\n\n")
			}
		}
	}

	b.WriteString(userText)
	return b.String(), nil
}

// retrievalTopK matches L1: the injection budget is a property of the model
// context, not of which runner fills it.
const retrievalTopK = 4

// ensureWorkspace creates (or reuses) the per-thread workspace directory.
// Thread UUIDs are canonical v4 by the time they reach a runner, so the path
// segment is closed to traversal; the format mirrors the cloud layout's tail.
func (e *Engine) ensureWorkspace(threadUUID string) (string, error) {
	rooter, ok := e.runtime.(workspaceRooter)
	if !ok || rooter.WorkspaceRoot() == "" {
		return "", fmt.Errorf("runtime %s has no workspace root", e.runtime.Name())
	}
	root := rooter.WorkspaceRoot()
	dir := filepath.Join(root, "thread_"+threadUUID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if _, isClaude := e.runtime.(*claudeRuntime); isClaude {
		// The isolated HOME must exist before the CLI starts, or it errors
		// on first write of its own state. Claude-specific: pi's isolation
		// dir (pi_home) is the runtime's own business.
		if err := os.MkdirAll(filepath.Join(root, ".claude-home"), 0o755); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// indexCompletedTurn mirrors local_inference's helper, including the recover:
// it runs in a bare goroutine, and a panic in the native embedding stack must
// cost one turn's indexing, not the sidecar process.
func (e *Engine) indexCompletedTurn(uid uint64, turnUUID, userText, assistantText string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("knowledge: index turn %s panicked: %v", turnUUID, r)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := e.hooks.IndexTurn(ctx, uid, turnUUID, userText, assistantText); err != nil {
		log.Printf("knowledge: index turn %s: %v", turnUUID, err)
	}
}

// emitProxyError mirrors local_inference's helper: send the typed error, and
// return a non-nil error so the intent is marked interrupted.
func emitProxyError(dst cloudproxy.SSEWriter, pe cloudproxy.ProxyError) error {
	_ = dst.WriteProxyError(pe)
	return fmt.Errorf("local agent: %s", pe.Message)
}
