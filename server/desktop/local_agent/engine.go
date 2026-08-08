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
// Every claim below the SDK call was kill-checked before this package was
// written (ProjectDocs/design/l2-sdk-cli-restart-2026-08.md): WithCLIPath
// spawns an explicit binary with no PATH discovery, env-injected base URL and
// key reach the subprocess, the CLI manages the multi-turn tool conversation
// itself, and tools execute in the WithCwd workspace. The isolation recipe in
// buildQueryOptions reproduces the spike's exactly.
package local_agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/gorm"

	claudesdk "github.com/jonnyquan/claude-agent-sdk-go/pkg/claudesdk"

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
// Bash is deliberately absent until the PreToolUse safety hooks land (L2b) —
// a shell is an escape hatch from every path rule the other tools respect.
var allowedTools = []string{"Read", "Write", "Edit", "Glob", "Grep"}

// queryStarter is the seam between this engine and the SDK, so tests can
// script the message stream without a CLI. Production is claudesdk.Query.
type queryStarter func(ctx context.Context, prompt string, opts ...claudesdk.Option) (claudesdk.MessageIterator, error)

// Engine runs L2 tool-loop turns. It satisfies desktop.TurnRunner and sits
// behind exactly the same CacheWriter/SSE/intent seam as the other runners.
type Engine struct {
	profile       localinference.ProfileReader
	db            *gorm.DB
	loader        localinference.AttachmentLoader // 可 nil
	hooks         localinference.KnowledgeHooks   // 可 nil（RAG off）
	cliPath       string
	workspaceRoot string
	query         queryStarter
}

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
		profile:       profile,
		db:            db,
		loader:        loader,
		hooks:         hooks,
		cliPath:       cliPath,
		workspaceRoot: workspaceRoot,
		query:         claudesdk.Query,
	}
}

// Chat runs one tool-loop turn. Same contract as the other runners: nil =
// clean completion (done event sent); non-nil = failure (proxy_error sent,
// except user cancellation, which is not an error to report).
func (e *Engine) Chat(ctx context.Context, req cloudproxy.ChatRequest, dst cloudproxy.SSEWriter) (err error) {
	_, baseURL, modelID, apiKey, perr := e.profile.LocalInferenceProfile()
	if perr != nil {
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
	if e.cliPath == "" {
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

	prompt, aerr := e.assemblePrompt(ctx, req, dst, requestID)
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

	iter, qerr := e.query(ctx, prompt, e.buildQueryOptions(baseURL, apiKey, modelID, workspace)...)
	if qerr != nil {
		if errors.Is(qerr, context.Canceled) || errors.Is(qerr, context.DeadlineExceeded) {
			return qerr
		}
		_ = dst.WriteProxyError(cloudproxy.ProxyError{
			Kind:      cloudproxy.KindServiceUnavailable,
			Message:   "无法启动本地工具循环",
			Retryable: true,
			Details:   map[string]any{"reason": qerr.Error()},
		})
		return qerr
	}
	defer iter.Close()

	assistantText, runErr := e.pump(ctx, iter, dst, cache)
	if runErr != nil {
		return runErr
	}
	if e.hooks != nil {
		go e.indexCompletedTurn(req.TurnUUID, req.UserText, assistantText)
	}
	return nil
}

// assemblePrompt builds the single prompt string the CLI receives: retrieval
// context, then conversation history, then attachments, then the request.
// The CLI manages the intra-turn tool conversation itself (kill-check §1);
// what it cannot know is everything that happened before this turn.
func (e *Engine) assemblePrompt(ctx context.Context, req cloudproxy.ChatRequest, dst cloudproxy.SSEWriter, requestID string) (string, error) {
	userText := req.UserText
	if e.hooks != nil {
		if found, rerr := e.hooks.Retrieve(ctx, req.UID, req.UserText, retrievalTopK); rerr != nil {
			log.Printf("knowledge: retrieve for turn %s: %v", req.TurnUUID, rerr)
		} else if len(found) > 0 {
			var used []localinference.RetrievedSource
			userText, used = localinference.PrependKnowledgeContext(req.UserText, found)
			localinference.EmitRetrievalEvent(dst, used)
		}
	}

	var b strings.Builder
	if history, herr := localinference.LoadThreadHistory(e.db, req.UID, req.ThreadID, requestID); herr != nil {
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

// placeholderAPIKey is sent when the user configured no key. Local endpoints
// (llama.cpp, vLLM) typically ignore the credential entirely — but the CLI
// requires a non-empty key to select API-key auth at all; with an empty one it
// falls back to looking for a logged-in session, finds none in the isolated
// HOME, and fails the turn with "Not logged in". Found the hard way in the
// first end-to-end run. The value only ever travels to the user's own
// configured base URL.
const placeholderAPIKey = "workmax-local-no-key"

// buildQueryOptions reproduces the kill-checked isolation recipe exactly.
//
// HOME points into the workspace root, not the user's: the CLI must not read
// ~/.claude — the user's hooks, settings, and login state belong to their
// interactive sessions, not to a turn running on their configured endpoint.
func (e *Engine) buildQueryOptions(baseURL, apiKey, modelID, workspace string) []claudesdk.Option {
	if apiKey == "" {
		apiKey = placeholderAPIKey
	}
	return []claudesdk.Option{
		claudesdk.WithCLIPath(e.cliPath),
		claudesdk.WithCwd(workspace),
		claudesdk.WithModel(modelID),
		claudesdk.WithEnv(map[string]string{
			"ANTHROPIC_BASE_URL": baseURL,
			"ANTHROPIC_API_KEY":  apiKey,
			"HOME":               filepath.Join(e.workspaceRoot, ".claude-home"),
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
			"DISABLE_TELEMETRY":                        "1",
			"DISABLE_AUTOUPDATER":                      "1",
		}),
		claudesdk.WithAllowedTools(allowedTools...),
		// Bypass composes with the allowlist: the CLI may use the listed
		// tools without asking, and only those. The per-tool safety hooks
		// (path validation) are L2b.
		claudesdk.WithPermissionMode(claudesdk.PermissionModeBypassPermissions),
		claudesdk.WithMaxTurns(maxAgentTurns),
		claudesdk.WithStderr(func(line string) { log.Printf("local agent cli: %s", line) }),
	}
}

// pump drains the SDK message stream into the renderer and the cache.
// Returns the accumulated assistant text for post-turn indexing.
func (e *Engine) pump(ctx context.Context, iter claudesdk.MessageIterator, dst cloudproxy.SSEWriter, cache *cloudproxy.CacheWriter) (string, error) {
	var assistant strings.Builder
	sawResult := false
	for {
		msg, nerr := iter.Next(ctx)
		if nerr != nil {
			if errors.Is(nerr, claudesdk.ErrNoMoreMessages) {
				break
			}
			if errors.Is(nerr, context.Canceled) || errors.Is(nerr, context.DeadlineExceeded) {
				// User cancellation: the SDK tears the subprocess down
				// (SIGTERM, then SIGKILL). No proxy_error — stopping is not a
				// failure to report.
				return assistant.String(), nerr
			}
			_ = dst.WriteProxyError(cloudproxy.ProxyError{
				Kind:      cloudproxy.KindServiceUnavailable,
				Message:   "本地工具循环中断",
				Retryable: true,
				Details:   map[string]any{"reason": nerr.Error()},
			})
			return assistant.String(), nerr
		}
		switch m := msg.(type) {
		case *claudesdk.AssistantMessage:
			for _, block := range m.Content {
				switch b := block.(type) {
				case *claudesdk.TextBlock:
					if b.Text == "" {
						continue
					}
					assistant.WriteString(b.Text)
					_ = cache.Enqueue("text_delta", b.Text)
					if werr := dst.WriteEvent(cloudproxy.SSEEvent{
						Type: "text_delta",
						Data: mustJSON(map[string]string{"delta": b.Text}),
					}); werr != nil {
						return assistant.String(), werr
					}
				case *claudesdk.ToolUseBlock:
					// Non-terminal activity event. Today's renderer surfaces
					// it as a tolerated unknown; L2c teaches the Run overview
					// to narrate it. Name only — inputs can hold user data.
					if werr := dst.WriteEvent(cloudproxy.SSEEvent{
						Type: "tool_use",
						Data: mustJSON(map[string]string{"name": b.Name}),
					}); werr != nil {
						return assistant.String(), werr
					}
				}
			}
		case *claudesdk.ResultMessage:
			sawResult = true
			if m.IsError {
				detail := "本地工具循环失败"
				if m.Result != nil && *m.Result != "" {
					detail = *m.Result
				}
				_ = dst.WriteProxyError(cloudproxy.ProxyError{
					Kind:      cloudproxy.KindUnknown,
					Message:   detail,
					Retryable: true,
				})
				return assistant.String(), fmt.Errorf("local agent: result error: %s", detail)
			}
		}
	}
	if !sawResult {
		_ = dst.WriteProxyError(cloudproxy.ProxyError{
			Kind:      cloudproxy.KindServiceUnavailable,
			Message:   "本地工具循环在结果前终止",
			Retryable: true,
		})
		return assistant.String(), errors.New("local agent: stream ended without a result")
	}
	if werr := dst.WriteEvent(cloudproxy.SSEEvent{Type: "done", Data: doneEventData}); werr != nil {
		return assistant.String(), werr
	}
	return assistant.String(), nil
}

// ensureWorkspace creates (or reuses) the per-thread workspace directory.
// Thread UUIDs are canonical v4 by the time they reach a runner, so the path
// segment is closed to traversal; the format mirrors the cloud layout's tail.
func (e *Engine) ensureWorkspace(threadUUID string) (string, error) {
	dir := filepath.Join(e.workspaceRoot, "thread_"+threadUUID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	// The isolated HOME must exist before the CLI starts, or it errors on
	// first write of its own state.
	if err := os.MkdirAll(filepath.Join(e.workspaceRoot, ".claude-home"), 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func (e *Engine) indexCompletedTurn(turnUUID, userText, assistantText string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := e.hooks.IndexTurn(ctx, turnUUID, userText, assistantText); err != nil {
		log.Printf("knowledge: index turn %s: %v", turnUUID, err)
	}
}

// emitProxyError mirrors local_inference's helper: send the typed error, and
// return a non-nil error so the intent is marked interrupted.
func emitProxyError(dst cloudproxy.SSEWriter, pe cloudproxy.ProxyError) error {
	_ = dst.WriteProxyError(pe)
	return fmt.Errorf("local agent: %s", pe.Message)
}

func mustJSON(v map[string]string) string {
	raw, err := json.Marshal(v)
	if err != nil {
		// A map[string]string cannot fail to marshal; belt for the braces.
		return "{}"
	}
	return string(raw)
}
