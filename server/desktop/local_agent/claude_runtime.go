//go:build desktop

package local_agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"

	claudesdk "github.com/jonnyquan/claude-agent-sdk-go/pkg/claudesdk"

	agentruntime "server/desktop/agentruntime"
	cloudproxy "server/desktop/cloud_proxy"
	localinference "server/desktop/local_inference"
)

// claudeRuntime is the Claude Agent SDK engine behind the agentruntime seam:
// it starts the CLI subprocess through our own claude-agent-sdk-go and pumps
// the SDK message stream into unified events. Everything turn-agnostic
// (cache, SSE, prompt assembly) stays in Engine; everything SDK-shaped lives
// here, so the Pi runtime can sit beside it without touching the pipeline.
type claudeRuntime struct {
	cliPath       string
	workspaceRoot string
	query         queryStarter
	stream        streamStarter
}

func (r *claudeRuntime) Name() string { return "claude" }

// WorkspaceRoot implements Engine's workspaceRooter seam.
func (r *claudeRuntime) WorkspaceRoot() string { return r.workspaceRoot }

// streamStarter is the seam for approval-mode turns. canUseTool requires the
// SDK's bidirectional Client (Query() rejects it), so approval turns start a
// streaming session; the returned cleanup disconnects it.
type streamStarter func(ctx context.Context, prompt string, opts ...claudesdk.Option) (claudesdk.MessageIterator, func(), error)

// startClientStream is the production streamStarter.
func startClientStream(ctx context.Context, prompt string, opts ...claudesdk.Option) (claudesdk.MessageIterator, func(), error) {
	client := claudesdk.NewClient(opts...)
	if err := client.Connect(ctx); err != nil {
		return nil, nil, err
	}
	if err := client.Query(ctx, prompt); err != nil {
		_ = client.Disconnect()
		return nil, nil, err
	}
	return client.ReceiveResponse(ctx), func() { _ = client.Disconnect() }, nil
}

// RunTurn implements agentruntime.Runtime.
func (r *claudeRuntime) RunTurn(ctx context.Context, in agentruntime.TurnInput, emit agentruntime.EmitFunc) error {
	opts := r.buildQueryOptions(in, emit)
	var (
		iter    claudesdk.MessageIterator
		cleanup func()
		qerr    error
	)
	if in.Approvals != nil {
		start := r.stream
		if start == nil {
			start = startClientStream
		}
		iter, cleanup, qerr = start(ctx, in.Prompt, opts...)
	} else {
		iter, qerr = r.query(ctx, in.Prompt, opts...)
	}
	if qerr != nil {
		if errors.Is(qerr, context.Canceled) || errors.Is(qerr, context.DeadlineExceeded) {
			return qerr
		}
		return &agentruntime.RuntimeError{
			Kind:      cloudproxy.KindServiceUnavailable,
			Message:   "无法启动本地工具循环",
			Retryable: true,
			Details:   map[string]any{"reason": qerr.Error()},
		}
	}
	defer iter.Close()
	if cleanup != nil {
		defer cleanup()
	}
	return r.pump(ctx, iter, emit)
}

// localAgentSystemPrompt is the tool loop's own system prompt — the sidecar
// previously sent none at all. Kept deliberately small and byte-stable: it
// sits at the very front of every request, so any churn here invalidates the
// endpoint's prompt cache for every thread at once.
const localAgentSystemPrompt = `You are WorkMax's local agent. You work inside the thread workspace directory using the provided tools; files you create or edit there are the deliverables the user sees. Stay inside the workspace. Reply in the language the user writes in.`

// placeholderAPIKey is sent when the user configured no key. Local endpoints
// (llama.cpp, vLLM) typically ignore the credential entirely — but the CLI
// requires a non-empty key to select API-key auth at all; with an empty one it
// falls back to looking for a logged-in session, finds none in the isolated
// HOME, and fails the turn with "Not logged in". Found the hard way in the
// first end-to-end run. The value only ever travels to the user's own
// configured base URL.
const placeholderAPIKey = "workmax-local-no-key"

// securityHook gates every tool call against the workspace path policy
// before the CLI executes it. Denials are narrated through emit — a blocked
// tool is part of the turn's story, not a secret. The hook fires on the SDK's
// transport goroutine while pump reads on the caller's; the denial path of
// the bridge touches only the (locked) SSE writer, so that concurrency is
// the same as it always was.
//
// Deliberate deviation from the cloud hook it descends from: an unparseable
// payload DENIES. The cloud fails open to keep a multi-tenant conversation
// moving; this loop runs with bypassPermissions on the user's own machine,
// and a validator that has gone blind must stop the tool, not wave it
// through. If an SDK shape change trips this, turns fail visibly and the
// fix is one struct away — the quiet alternative is unvalidated file access.
func securityHook(guard *pathValidator, emit agentruntime.EmitFunc) claudesdk.HookCallback {
	return func(input claudesdk.HookInput, _ *string, _ claudesdk.HookContext) (claudesdk.HookJSONOutput, error) {
		inputMap, ok := input.(map[string]any)
		if !ok {
			log.Printf("local agent security: unexpected hook input type %T; denying", input)
			return claudesdk.NewPreToolUseOutput(claudesdk.PermissionDecisionDeny,
				"security hook could not read the tool call", nil), nil
		}
		toolName, _ := inputMap["tool_name"].(string)
		toolInput := map[string]any{}
		if cast, ok := inputMap["tool_input"].(map[string]any); ok && cast != nil {
			toolInput = cast
		}
		if err := guard.validateToolCall(toolName, toolInput); err != nil {
			log.Printf("local agent security: BLOCKED %s: %v", toolName, err)
			_ = emit(agentruntime.Event{
				Kind: agentruntime.EventToolDenied,
				Tool: agentruntime.ToolEvent{Name: toolName, Reason: err.Error()},
			})
			return claudesdk.NewPreToolUseOutput(claudesdk.PermissionDecisionDeny,
				fmt.Sprintf("Security violation: %v", err), nil), nil
		}
		return claudesdk.NewPreToolUseOutput(claudesdk.PermissionDecisionAllow, "", nil), nil
	}
}

// buildQueryOptions reproduces the kill-checked isolation recipe exactly.
//
// HOME points into the workspace root, not the user's: the CLI must not read
// ~/.claude — the user's hooks, settings, and login state belong to their
// interactive sessions, not to a turn running on their configured endpoint.
func (r *claudeRuntime) buildQueryOptions(in agentruntime.TurnInput, emit agentruntime.EmitFunc) []claudesdk.Option {
	apiKey := in.APIKey
	if apiKey == "" {
		apiKey = placeholderAPIKey
	}
	matchAll := "*"
	opts := []claudesdk.Option{
		claudesdk.WithHook(claudesdk.HookEventPreToolUse, claudesdk.HookMatcher{
			Matcher: &matchAll,
			Hooks:   []claudesdk.HookCallback{securityHook(newPathValidator(in.Workspace), emit)},
		}),
		claudesdk.WithCLIPath(r.cliPath),
		claudesdk.WithCwd(in.Workspace),
		claudesdk.WithModel(in.ModelID),
		claudesdk.WithEnv(map[string]string{
			// Canonicalized, not passed through: the CLI appends /v1/messages
			// to whatever it is given, so a base the user typed WITH the
			// version segment ("https://host/v1") would become
			// https://host/v1/v1/messages. AnthropicBaseURL strips it, and the
			// L1 adapter appends the same full path to the same base — one
			// stored value, two engines, one URL.
			"ANTHROPIC_BASE_URL": localinference.AnthropicBaseURL(in.BaseURL),
			"ANTHROPIC_API_KEY":  apiKey,
			"HOME":               filepath.Join(r.workspaceRoot, ".claude-home"),
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
			"DISABLE_TELEMETRY":                        "1",
			"DISABLE_AUTOUPDATER":                      "1",
		}),
		claudesdk.WithMaxTurns(maxAgentTurns),
		claudesdk.WithSystemPrompt(localAgentSystemPrompt),
		// Partial messages turn the CLI's block stream into token deltas —
		// without them a long answer arrives as one frame per block and the
		// renderer reads as frozen.
		claudesdk.WithIncludePartialMessages(true),
		claudesdk.WithStderr(func(line string) { log.Printf("local agent cli: %s", line) }),
	}
	if in.Approvals != nil {
		// Interactive approvals: the read surface is pre-allowed, the write
		// surface consults approvalCallback through the CLI's permission
		// protocol, and everything else is denied there. No bypass — the CLI
		// must ask.
		opts = append(opts,
			claudesdk.WithAllowedTools(readOnlyTools...),
			claudesdk.WithCanUseTool(approvalCallback(in.Approvals, emit)),
		)
	} else {
		// Legacy pre-approved mode, kill-checked: bypass composes with the
		// allowlist — the CLI may use the listed tools without asking, and
		// only those.
		opts = append(opts,
			claudesdk.WithAllowedTools(allowedTools...),
			claudesdk.WithPermissionMode(claudesdk.PermissionModeBypassPermissions),
		)
	}
	if in.SessionRef != "" {
		// Resume the CLI's own session: it replays the thread's prior turns
		// from its session file, so the caller skips flattening history into
		// the prompt. A dead ref fails the turn; the engine then clears the
		// ref and the next turn takes the flatten path.
		opts = append(opts, claudesdk.WithResume(in.SessionRef))
	}
	return opts
}

// pump drains the SDK message stream into unified events.
//
// With IncludePartialMessages on, text and thinking arrive twice: as raw
// Anthropic stream events (token deltas) and again inside the full
// AssistantMessage blocks. The deltas are the product; the full blocks are
// skipped — but only once a delta of that kind has actually been seen, so a
// CLI or gateway that drops stream events degrades to block-sized frames
// instead of silence.
func (r *claudeRuntime) pump(ctx context.Context, iter claudesdk.MessageIterator, emit agentruntime.EmitFunc) error {
	sawResult := false
	sawTextDelta := false
	sawThinkingDelta := false
	for {
		msg, nerr := iter.Next(ctx)
		if nerr != nil {
			if errors.Is(nerr, claudesdk.ErrNoMoreMessages) {
				break
			}
			if errors.Is(nerr, context.Canceled) || errors.Is(nerr, context.DeadlineExceeded) {
				return nerr
			}
			return &agentruntime.RuntimeError{
				Kind:      cloudproxy.KindServiceUnavailable,
				Message:   "本地工具循环中断",
				Retryable: true,
				Details:   map[string]any{"reason": nerr.Error()},
			}
		}
		switch m := msg.(type) {
		case *claudesdk.StreamEvent:
			kind, delta := streamDelta(m.Event)
			switch kind {
			case agentruntime.EventTextDelta:
				sawTextDelta = true
			case agentruntime.EventThinkingDelta:
				sawThinkingDelta = true
			default:
				continue
			}
			if werr := emit(agentruntime.Event{Kind: kind, Delta: delta}); werr != nil {
				return werr
			}
		case *claudesdk.AssistantMessage:
			for _, block := range m.Content {
				switch b := block.(type) {
				case *claudesdk.TextBlock:
					if sawTextDelta {
						continue // already streamed token by token
					}
					if werr := emit(agentruntime.Event{
						Kind:  agentruntime.EventTextDelta,
						Delta: b.Text,
					}); werr != nil {
						return werr
					}
				case *claudesdk.ThinkingBlock:
					if sawThinkingDelta {
						continue
					}
					if werr := emit(agentruntime.Event{
						Kind:  agentruntime.EventThinkingDelta,
						Delta: b.Thinking,
					}); werr != nil {
						return werr
					}
				case *claudesdk.ToolUseBlock:
					if werr := emit(agentruntime.Event{
						Kind: agentruntime.EventToolUse,
						Tool: agentruntime.ToolEvent{Name: b.Name, Target: toolTarget(b.Input)},
					}); werr != nil {
						return werr
					}
				}
			}
		case *claudesdk.ResultMessage:
			sawResult = true
			if m.SessionID != "" {
				// Reported even on an error result: the session file exists
				// either way. The caller decides whether to store it.
				if werr := emit(agentruntime.Event{
					Kind:       agentruntime.EventSessionRef,
					SessionRef: m.SessionID,
				}); werr != nil {
					return werr
				}
			}
			if m.IsError {
				detail := "本地工具循环失败"
				if m.Result != nil && *m.Result != "" {
					detail = *m.Result
				}
				return &agentruntime.RuntimeError{
					Kind:      cloudproxy.KindUnknown,
					Message:   detail,
					Retryable: true,
				}
			}
		}
	}
	if !sawResult {
		return &agentruntime.RuntimeError{
			Kind:      cloudproxy.KindServiceUnavailable,
			Message:   "本地工具循环在结果前终止",
			Retryable: true,
		}
	}
	return nil
}

// streamDelta digs the token delta out of a raw Anthropic stream event.
// Returns ("", "") for every event shape that is not a text or thinking
// delta — unknown shapes are somebody's new feature, not an error.
func streamDelta(event map[string]any) (agentruntime.EventKind, string) {
	if t, _ := event["type"].(string); t != "content_block_delta" {
		return "", ""
	}
	delta, ok := event["delta"].(map[string]any)
	if !ok {
		return "", ""
	}
	switch dt, _ := delta["type"].(string); dt {
	case "text_delta":
		text, _ := delta["text"].(string)
		return agentruntime.EventTextDelta, text
	case "thinking_delta":
		text, _ := delta["thinking"].(string)
		return agentruntime.EventThinkingDelta, text
	}
	return "", ""
}

// toolTarget names the file a tool call touches when there is one: the
// BASENAME only (≤80 chars), enough to read "Write · outline.md" while full
// paths and every other input stay out of the stream.
func toolTarget(input map[string]any) string {
	for _, key := range []string{"file_path", "path", "filepath"} {
		if raw, ok := input[key].(string); ok && raw != "" {
			base := filepath.Base(raw)
			if len(base) > 80 {
				base = base[:80]
			}
			return base
		}
	}
	return ""
}
