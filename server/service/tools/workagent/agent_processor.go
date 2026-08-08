package workagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"server/globals"
	workagentModel "server/model/workagent"

	"github.com/google/uuid"
	claudecode "github.com/jonnyquan/claude-agent-sdk-go"
	claudesdk "github.com/jonnyquan/claude-agent-sdk-go/pkg/claudesdk"
)

// scrubCredentialsFromLog masks credential-shaped tokens before they
// hit the log stream. Some upstream gateways (and the Anthropic CLI's
// transport-error path itself) echo Authorization headers and raw
// API keys in 401/403 responses; without this scrubber, an
// upstream-misconfiguration burst would page-spam the logs with
// recoverable-but-sensitive material — and the same logs are what
// support engineers paste into incident chats.
//
// Patterns covered:
//   - "Bearer <token>" — the most common HTTP echo
//   - "sk-..." — Anthropic-style API keys (20+ chars after the prefix
//     including the sk-ant-* / sk-or-* / sk-* families)
//   - HTTP-header-shaped "Authorization: <value>"
//   - JSON / env-shape "api_key": "...", "ANTHROPIC_API_KEY=..."
//
// Targeted at known-secret shapes rather than blanket-redacting any
// high-entropy substring — that would obscure legitimate diagnostic
// IDs (request IDs, trace tokens) ops needs to correlate.
// debugStderrWarnOnce gates the runtime warning that fires when
// CLAUDE_AGENT_DEBUG_STDERR=1 is detected. Once-only: the env var
// is sampled at every turn but we only want one log line per
// process so accidentally-enabled prod deploys are visible without
// spamming. See the warn site below for rationale.
var debugStderrWarnOnce sync.Once

var (
	bearerTokenRE = regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9._\-]+`)
	// 16+ chars after "sk-" so a key fragment matches but typical
	// filename-shaped strings ("sk-short.txt" — 8 chars after prefix)
	// don't false-positive. Real Anthropic keys are 100+ chars so 16
	// is a safe floor.
	skKeyRE      = regexp.MustCompile(`sk-[A-Za-z0-9._\-]{16,}`)
	authHeaderRE = regexp.MustCompile(`(?i)Authorization\s*:\s*\S+`)
	apiKeyEqRE   = regexp.MustCompile(`(?i)(?:api[_\-]?key|anthropic_api_key)\s*[:=]\s*["']?[^"'\s,}]+`)
)

func scrubCredentialsFromLog(s string) string {
	s = bearerTokenRE.ReplaceAllString(s, "Bearer [REDACTED]")
	s = skKeyRE.ReplaceAllString(s, "sk-[REDACTED]")
	s = authHeaderRE.ReplaceAllString(s, "Authorization: [REDACTED]")
	s = apiKeyEqRE.ReplaceAllString(s, "api_key=[REDACTED]")
	return s
}

// isClientCancellation reports whether an error came from the caller's
// context being canceled (tab close, SSE drop, navigation away). Such
// errors look like "request failed" from the SDK's perspective but
// are not the agent account's fault — recording them as account
// failures lets a flaky-network user trip the per-account circuit
// breaker and force an auto-switch + admin email storm. Treat as
// neutral: log it, return upstream so the reservation refunds, but
// don't penalize the account.
func isClientCancellation(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// Some SDK paths surface the cancellation as a plain wrapped error
	// with a substring rather than via errors.Is. Belt-and-suspenders.
	if ctx != nil && ctx.Err() != nil {
		return true
	}
	return false
}

// poolOutcome classifies a completed turn's effect on agent-account
// accounting. Distinct from the Go-error path: the SDK can return
// nil error while the embedded ResultMessage signals IsError=true
// (upstream HTTP failure surfaced as a result frame).
type poolOutcome int

const (
	// poolOutcomeSuccess: turn completed cleanly, account credentials
	// worked, count toward the breaker's success window.
	poolOutcomeSuccess poolOutcome = iota
	// poolOutcomeFailure: turn returned an error attributable to the
	// account (401/403 = bad creds, 404 = model unavailable, default
	// for unrecognised IsError). Feeds the failure counter so the
	// breaker can rotate accounts.
	poolOutcomeFailure
	// poolOutcomeNeutral: turn failed for an upstream-systemic reason
	// the account isn't responsible for (429 rate-limit). Don't credit
	// the account (the user's request didn't actually succeed) and
	// don't penalize it (the credentials are fine, the upstream is
	// throttling). Lands in logs at Info so an operator can correlate
	// with upstream incidents.
	poolOutcomeNeutral
)

// resultErrorStatus is the minimal subset of ResultMessage fields the
// pool classifier reads. Defined inline here (rather than depending on
// the SDK type) so a future SDK reshape that renames or moves the
// fields surfaces as a parsing-side issue rather than a compile error
// across an unrelated module.
type resultErrorStatus struct {
	IsError        bool     `json:"is_error"`
	Subtype        string   `json:"subtype,omitempty"`
	APIErrorStatus *int     `json:"api_error_status,omitempty"`
	StopReason     *string  `json:"stop_reason,omitempty"`
	TotalCostUSD   *float64 `json:"total_cost_usd,omitempty"`
}

// projectSideKillSwitchSubtypes lists the result subtypes that
// represent OUR-side caps firing (not upstream errors). These must
// route to poolOutcomeNeutral — the account's credentials are fine,
// we hit our own configured limit, so penalising the account would
// silently rotate accounts due to operator-set policy.
//
// Currently:
//   - "error_max_budget_usd": WithMaxBudgetUSD cap exceeded.
//
// Add new entries here as the SDK gains more project-side caps;
// each addition needs a paired test case.
var projectSideKillSwitchSubtypes = map[string]struct{}{
	"error_max_budget_usd": {},
}

// classifyResultForPool reads the result frame and decides how the
// agent-account pool should account this turn. See poolOutcome for
// the policy. Returns Success on parse failure (preserves prior
// behaviour: a malformed result counted as success because the Go
// error path was clean).
func classifyResultForPool(resultJSON json.RawMessage) (poolOutcome, resultErrorStatus) {
	var status resultErrorStatus
	if len(resultJSON) == 0 {
		return poolOutcomeSuccess, status
	}
	if err := json.Unmarshal(resultJSON, &status); err != nil {
		return poolOutcomeSuccess, status
	}
	if !status.IsError {
		return poolOutcomeSuccess, status
	}
	// Project-side kill-switch (MaxBudgetUSD etc.) — Neutral, not
	// Failure. Check before APIErrorStatus because a kill-switch
	// result has no upstream HTTP status to inspect.
	if _, isKillSwitch := projectSideKillSwitchSubtypes[status.Subtype]; isKillSwitch {
		return poolOutcomeNeutral, status
	}
	if status.APIErrorStatus != nil && *status.APIErrorStatus == 429 {
		return poolOutcomeNeutral, status
	}
	return poolOutcomeFailure, status
}

// Session error constants
const (
	ErrSessionNotFound = "No conversation found with session ID"
	ErrInvalidSession  = "Invalid session"
	ErrSessionExpired  = "Session expired"
	ErrSessionClosed   = "Session closed"
)

// AgentMessageCallback is invoked when a full message payload is available.
type AgentMessageCallback func(messageType string, message json.RawMessage, messageIndex int)

// AgentBlockCallback is invoked for each content block emitted by the SDK.
type AgentBlockCallback func(messageType string, block json.RawMessage, messageIndex int, blockIndex int)

// AgentDoneCallback is invoked when the conversation completes.
type AgentDoneCallback func(result json.RawMessage)

// AgentCallbacks groups all callback handlers used during streaming.
type AgentCallbacks struct {
	OnMessage AgentMessageCallback
	OnBlock   AgentBlockCallback
	OnDone    AgentDoneCallback
}

// derefStr returns the dereferenced string or "" for a nil pointer.
// Used to render optional SDK fields (TaskType, LastToolName) inside
// log lines without nil-checks at every site.
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// invokeSafe runs `fn` and absorbs any panic into a logged warning so
// a buggy callback (or a transient marshal failure inside one) can't
// unwind through the agent message loop and break the SSE connection
// mid-stream. Without this guard, a downstream change that drops a
// recover wrapper at the api/ layer would leave us with a half-
// finished stream and no done event.
//
// The label distinguishes call sites in logs (assistant-message vs
// user-block vs result-done) — without it every recovered panic
// looks identical.
func invokeSafe(label string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			globals.Error(fmt.Sprintf("[Agent] callback panic recovered (%s): %v", label, r))
		}
	}()
	fn()
}

// ProcessAgentConversationWithRetry executes the Agent workflow with automatic session recovery.
// It attempts to use the existing session first, and if that fails due to session not found error,
// it automatically creates a new session and retries.
// Parameters:
// - agentMode: The mode to use (one of allowedAgentModes — ppt, flashCard, character, …) — empty defaults to "ppt" (DDL default for chat_thread.agent_mode)
// - customPrompt: Optional custom prompt from thread settings - empty string to skip
// Returns the conversation, the new session ID (may be different from input), and any error.
func (c *ClaudeAgentGoClient) ProcessAgentConversationWithRetry(
	ctx context.Context,
	prompt string,
	threadID string,
	agentSessionID string,
	workspacePath string,
	files []AgentFileInfo,
	callbacks AgentCallbacks,
	agentMode string,
	customPrompt string,
	account *workagentModel.AgentAccount,
	systemAdditions string,
) (*workagentModel.AgentConversation, string, error) {

	// 1. Resolve account
	pool := GetAgentAccountPool()
	activeAccount := account
	if activeAccount == nil {
		var err error
		activeAccount, err = pool.GetActiveAccount()
		if err != nil {
			globals.Error(fmt.Sprintf("[Agent] No active account available: %v", err))
			return nil, "", fmt.Errorf("no active account: %w", err)
		}
	}
	accountID := activeAccount.ID

	// Log which provider is being used
	globals.Info(fmt.Sprintf("[Agent] Using account: %s (Provider: %s, ID: %d, BaseURL: %s)",
		activeAccount.Name, activeAccount.Provider, activeAccount.ID, activeAccount.BaseURL))

	// 2. First attempt: try with existing session
	conversation, err := c.processAgentConversationInternal(
		ctx, prompt, threadID, agentSessionID, workspacePath, files, callbacks, agentMode, customPrompt, systemAdditions,
	)

	if err == nil {
		// Pool accounting now reads the result frame: a Go-clean turn
		// can still carry IsError=true (upstream HTTP error surfaced
		// as a result instead of an error). 429 is upstream-throttling
		// (don't penalise the account); other IsError variants are
		// account-attributable. See classifyResultForPool docs.
		sessionID := extractSessionIDFromResult(conversation.Result)
		outcome, status := classifyResultForPool(conversation.Result)
		switch outcome {
		case poolOutcomeSuccess:
			pool.RecordSuccess(accountID)
			globals.Info(fmt.Sprintf("[Agent] Request succeeded, sessionID: '%s', account: %d", sessionID, accountID))
		case poolOutcomeNeutral:
			// Two reasons to land here: upstream 429 (rate-limit) and
			// project-side kill-switch (e.g. MaxBudgetUSD). Surface
			// both fields in the log line so ops can tell which one
			// fired without parsing the result frame.
			apiStatus := 0
			if status.APIErrorStatus != nil {
				apiStatus = *status.APIErrorStatus
			}
			globals.Info(fmt.Sprintf("[Agent] Request returned account-neutral result (status=%d, subtype=%s), sessionID: '%s', account: %d", apiStatus, status.Subtype, sessionID, accountID))
		case poolOutcomeFailure:
			apiStatus := 0
			if status.APIErrorStatus != nil {
				apiStatus = *status.APIErrorStatus
			}
			synthErr := fmt.Errorf("result is_error=true api_status=%d subtype=%s", apiStatus, status.Subtype)
			pool.RecordFailure(accountID, synthErr)
			globals.Warn(fmt.Sprintf("[Agent] Request returned IsError result (status=%d, subtype=%s), account: %d, sessionID: '%s'", apiStatus, status.Subtype, accountID, sessionID))
		}
		return conversation, sessionID, nil
	}

	// 3. Check if this is a session not found error
	if !isSessionNotFoundError(err) {
		// Client cancellation (tab close, network drop) is not the
		// account's fault — skip the failure counter and the admin
		// email so a flaky-network user can't auto-switch the active
		// account. The reservation still refunds upstream because we
		// return the error.
		if isClientCancellation(ctx, err) {
			globals.Info(fmt.Sprintf("[Agent] Request canceled by client, account: %d, error: %v", accountID, err))
			return nil, "", err
		}
		pool.RecordFailure(accountID, err)
		NotifyAgentError(ctx, err, threadID, UserIDStringFromContext(ctx), agentSessionID)
		globals.Error(fmt.Sprintf("[Agent] Request failed, account: %d, error: %v", accountID, err))
		return nil, "", err
	}

	// 4. Session not found - log and prepare for retry (don't record as failure)
	globals.Warn(fmt.Sprintf(
		"[Agent] Session not found, creating new session. Old: %s, Error: %v",
		agentSessionID, err,
	))
	RecordSessionNotFound(err)

	// 5. Second attempt: retry with empty session ID (will create new session)
	conversation, err = c.processAgentConversationInternal(
		ctx, prompt, threadID, "", workspacePath, files, callbacks, agentMode, customPrompt, systemAdditions, // Empty session ID
	)

	if err != nil {
		// Same client-cancel exclusion as the first-attempt path —
		// see the matching note above.
		if isClientCancellation(ctx, err) {
			RecordRecoveryFailed()
			globals.Info(fmt.Sprintf("[Agent] Session recovery canceled by client, account: %d, error: %v", accountID, err))
			return nil, "", err
		}
		// Retry failed - record failure
		pool.RecordFailure(accountID, err)
		RecordRecoveryFailed()
		NotifyAgentError(ctx, err, threadID, UserIDStringFromContext(ctx), agentSessionID)
		globals.Error(fmt.Sprintf("[Agent] Session recovery failed, account: %d, error: %v", accountID, err))
		return nil, "", fmt.Errorf("failed to create new session: %w", err)
	}

	// Success after retry
	pool.RecordSuccess(accountID)
	newSessionID := extractSessionIDFromResult(conversation.Result)
	RecordSessionRecovered()

	globals.Info(fmt.Sprintf(
		"[Agent] Session recovered successfully: old=%s, new=%s, account: %d",
		agentSessionID, newSessionID, accountID,
	))

	return conversation, newSessionID, nil
}

// ProcessAgentConversation runs a single conversation attempt without
// the session-recovery retry that ProcessAgentConversationWithRetry
// provides. Used by the agent_account_test_runner: a "hi" probe
// against a specific account where the test path explicitly does NOT
// want auto-recovery (a session-not-found on the test path is a real
// failure to surface, not something to silently retry around).
//
// Production chat flows must use ProcessAgentConversationWithRetry.
func (c *ClaudeAgentGoClient) ProcessAgentConversation(
	ctx context.Context,
	prompt string,
	sessionID string,
	agentSessionID string,
	workspacePath string,
	callbacks AgentCallbacks,
) (*workagentModel.AgentConversation, error) {
	// DefaultAgentMode is the canonical fallback — keep aligned with
	// the constant rather than hardcoding "ppt", so a future config
	// migration that changes the default doesn't leave this caller
	// stuck on the old value.
	return c.processAgentConversationInternal(ctx, prompt, sessionID, agentSessionID, workspacePath, nil, callbacks, workagentModel.DefaultAgentMode, "", "")
}

// processAgentConversationInternal is the internal implementation that executes the Agent workflow.
//
// systemAdditions is the precomposed SystemAdditions string (see
// preflight.go's BuildPreflightAdditions). Empty value preserves the
// pre-Sprint-A byte-for-byte system prompt — the renderer drops the
// placeholder cleanly.
func (c *ClaudeAgentGoClient) processAgentConversationInternal(
	ctx context.Context,
	prompt string,
	sessionID string,
	agentSessionID string,
	workspacePath string,
	files []AgentFileInfo,
	callbacks AgentCallbacks,
	agentMode string,
	customPrompt string,
	systemAdditions string,
) (*workagentModel.AgentConversation, error) {
	// Per-turn ctx timeout. Per-attempt (not per-conversation) so the
	// retry on session-not-found gets its own budget instead of
	// inheriting whatever was left after the first attempt timed out.
	// The deferred cancel releases the timer regardless of how the
	// function exits — important on the success path where ctx isn't
	// otherwise cancelled.
	ctx, cancel := context.WithTimeout(ctx, effectiveTurnTimeout(c.config.Timeout))
	defer cancel()

	RecordSessionAttempt()

	globals.Info(fmt.Sprintf("[Agent] Starting conversation: session=%s, agent_session=%s, mode=%s",
		sessionID, agentSessionID, agentMode))

	// Default to ppt mode if not specified
	if agentMode == "" {
		agentMode = workagentModel.DefaultAgentMode
	}

	// System prompt comes from the skill registry — see
	// skill_bridge.go for the legacy fallback semantics. ppt mode
	// gets the four _shared/ horizontal references stacked on top
	// of the legacy identity body; other modes still pass through
	// byte-identically until their own skill.yaml ships.
	//
	// systemAdditions (Sprint-A onwards) carries the M5 ChecklistDigest
	// + future DS-1/2/4 layers built by BuildPreflightAdditions. Empty
	// when the gate feature flag is off for this (uid, skill) — the
	// renderer drops the {{.SystemAdditions}} placeholder cleanly.
	systemPrompt := GetSkillRegistry().GetSystemPromptWithAdditions(agentMode, systemAdditions, false, "")

	// Apply custom prompt from thread settings if provided. Single
	// trim — the previous shape did `customPrompt != "" && trim != ""`
	// then trimmed again inside the body, three trim calls for what
	// is one decision.
	if trimmedCustom := strings.TrimSpace(customPrompt); trimmedCustom != "" {
		systemPrompt = systemPrompt + "\n\n## 📌 User Custom Instructions\n\n" + trimmedCustom
		globals.Info(fmt.Sprintf("[Agent] Applied custom prompt (%d chars) to mode %s", len(trimmedCustom), agentMode))
	}

	// F4 (2026-05-17) — surface the resolved skill version so
	// operators can grep "[Agent] skill=ppt v=2.0.0" to see which
	// version produced a given turn. Silent skill-version bumps
	// (e.g. ppt 2.0.0 → 2.1.0 via git PR + deploy) were previously
	// invisible to anyone except git log; this puts them in the
	// per-turn log stream that already carries everything else
	// observability cares about.
	skillVersion := GetSkillRegistry().ResolveVersion(agentMode)
	globals.Info(fmt.Sprintf("[Agent] Using system prompt for mode '%s' (skill v=%s): %d chars", agentMode, skillVersion, len(systemPrompt)))

	// Build intelligent files context grouped by type
	filesContext := buildIntelligentFilesContext(files)

	// Inject workspace context into system prompt to reinforce path rules.
	// Both %s slots resolve to workspacePath — the second line teaches the
	// model that the literal "." in any tool input refers to that path.
	// The previous shape bound the second %s to sessionID (a thread UUID),
	// producing two contradictory "current directory" assertions inside the
	// same paragraph. The model would either ignore the second line or get
	// confused about which path is canonical.
	workspaceContext := fmt.Sprintf(`

**CURRENT WORKSPACE CONTEXT:**
- Working Directory: %s
- Your current directory (.) is: %s
- Files uploaded by user are in: ./uploads/
- You MUST save all output files to: ./outputs/
- Use ONLY relative paths from current directory
- NEVER use absolute paths or navigate to parent directories (..)

**REMINDER: All output files go to outputs/ directory!**
Example: fig.write_html('outputs/chart.html')
`, workspacePath, workspacePath)

	fullSystemPrompt := systemPrompt + filesContext + workspaceContext

	options := append([]claudecode.Option{}, c.baseOptions()...)
	options = append(options, claudecode.WithSystemPrompt(fullSystemPrompt))

	// strict-mcp-config has a typed option in the current SDK; use it
	// instead of the extraArgs back-channel so the flag is type-checked
	// and shows up in the public API surface. disable-slash-commands
	// has no dedicated option (no plans for one — it's CLI-shape, not
	// session-shape), so it stays in extraArgs.
	options = append(options, claudesdk.WithStrictMcpConfig(true))
	extraArgs := map[string]*string{
		"disable-slash-commands": nil,
	}
	// CLAUDE_AGENT_DEBUG_STDERR is a development knob: when "1", the
	// CLI emits [DEBUG] lines and every line lands in our log stream
	// (otherwise [DEBUG] lines are filtered out below). Useful when
	// chasing a transport bug, hostile in production:
	//
	//   - Higher log volume (every SDK message → log line).
	//   - Verbose lines occasionally exceed the M1 credential
	//     scrubber's pattern coverage (e.g. an upstream that
	//     formats secrets in a non-canonical shape that doesn't
	//     match Bearer/sk-/api_key=). Scrubber is best-effort;
	//     debug mode multiplies the volume of strings going
	//     through it.
	//
	// Surface a one-time warn at first detection so an accidentally-
	// enabled prod deploy is visible in the log stream without
	// spamming every turn.
	debugCLIStderr := os.Getenv("CLAUDE_AGENT_DEBUG_STDERR") == "1"
	if debugCLIStderr {
		debugStderrWarnOnce.Do(func() {
			globals.Warn("[Agent] CLAUDE_AGENT_DEBUG_STDERR=1 is set — verbose CLI stderr will land in the log stream. M1 credential scrubber is active but debug mode amplifies log volume; do NOT leave this enabled in production.")
		})
		extraArgs["debug-to-stderr"] = nil
	}
	options = append(options, claudesdk.WithExtraArgs(extraArgs))

	// Capture CLI stderr to diagnose transport/control-protocol initialization failures.
	var stderrMu sync.Mutex
	stderrLines := make([]string, 0, 50)
	options = append(options, claudesdk.WithStderr(func(line string) {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			return
		}
		isDebugLine := strings.Contains(strings.ToLower(trimmed), "[debug]")
		if isDebugLine && !debugCLIStderr {
			return
		}
		// Scrub credential-shaped tokens before logging. The CLI's
		// 401/403 transport-error path can echo Authorization headers
		// and api keys; we don't want those in the application log
		// stream regardless of how rare the path is. Same scrubbed
		// form goes into the in-memory ring (stderrLines) so the tail
		// logs at error time stay clean too.
		safe := scrubCredentialsFromLog(trimmed)
		if strings.Contains(strings.ToLower(trimmed), "[warn]") {
			globals.Warn(fmt.Sprintf("[Agent CLI stderr] %s", safe))
		} else {
			globals.Error(fmt.Sprintf("[Agent CLI stderr] %s", safe))
		}
		stderrMu.Lock()
		if len(stderrLines) >= 50 {
			stderrLines = stderrLines[1:]
		}
		stderrLines = append(stderrLines, safe)
		stderrMu.Unlock()
	}))

	// Set thread-specific workspace as working directory
	// workspacePath is always validated by caller (EnsureThreadWorkspace)
	options = append(options, claudecode.WithCwd(workspacePath))
	globals.Info(fmt.Sprintf("[Agent] Working directory: %s", workspacePath))

	// 🔒 CRITICAL SECURITY: PreToolUse hook gates every tool call
	// against the workspace PathValidator. Plus observability-only
	// PostToolUse / PostToolUseFailure hooks for tool-execution
	// telemetry. State (pathValidator, duration tracker) is
	// per-turn — encapsulated by setupTurnHooks.
	options = append(options, setupTurnHooks(workspacePath)...)

	// Phase B2: assemble the per-turn MCP server map from the user's
	// enabled external MCP connectors. Read fresh from the registry on
	// every turn so a recent enable/disable / re-keying takes effect
	// on the next submit. Bad connector rows are skipped when at least
	// one usable connector remains; if the registry fails or every
	// enabled connector is unusable, fail the turn visibly instead of
	// silently dropping the user's external tools.
	//
	// The in-process production-tools server (script/shotboard/
	// shot_prompt/render_shot/lookup_asset) was retired 2026-05-18. Only user-installed
	// external connectors contribute to mcpServers now.
	mcpServers := map[string]claudecode.McpServerConfig{}
	if uid, hasUID := UserIDFromContext(ctx); hasUID {
		external, err := BuildExternalMcpServers(uid)
		if err != nil {
			// Connector registry errors are turn-fatal once the user has
			// enabled external tools. Silent degradation makes the UI say
			// "connector enabled" while the SDK actually runs without it.
			return nil, fmt.Errorf("failed to load external MCP connectors: %w", err)
		}
		for name, cfg := range external {
			mcpServers[name] = cfg
		}
	}
	if len(mcpServers) > 0 {
		options = append(options, claudecode.WithMcpServers(mcpServers))
		globals.Info(fmt.Sprintf("[Agent] MCP servers active: %d", len(mcpServers)))
	}

	if agentSessionID != "" {
		options = append(options, claudecode.WithResume(agentSessionID))
		globals.Info(fmt.Sprintf("[Agent] Resuming session: %s", agentSessionID))
	} else {
		globals.Info("[Agent] Starting new session")
	}

	iterator, err := claudecode.Query(ctx, prompt, options...)
	if err != nil {
		stderrTail := recentCLIErrors(stderrLines, &stderrMu)
		if stderrTail != "" {
			globals.Error(fmt.Sprintf("[Agent] Query initialization stderr tail:\n%s", stderrTail))
		}
		logTransportHint(err, stderrTail)
		// Send email notification for query execution failure
		NotifyAgentError(ctx, err, sessionID, UserIDStringFromContext(ctx), agentSessionID)
		return nil, fmt.Errorf("failed to execute query: %w", err)
	}
	defer iterator.Close()

	// Proactively tear down the iterator on ctx cancellation so a
	// long-running tool (slow Bash, Python script) can't keep
	// consuming upstream tokens after the client has disconnected.
	// iterator.Next(ctx) does check ctx between SDK frames, but a
	// tool already executing inside the SDK subprocess may not
	// surface the signal for the duration of the tool — minutes
	// during which we keep paying the upstream gateway for output
	// the user can no longer see. Force-closing from outside makes
	// the next Next() return immediately. SDK Close() is sync.Once
	// so racing with the deferred Close is safe.
	loopDone := make(chan struct{})
	defer close(loopDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = iterator.Close()
		case <-loopDone:
		}
	}()

	conversation := &workagentModel.AgentConversation{
		Messages: make([]json.RawMessage, 0),
	}

	for {
		// Per-iteration tracing dropped to DEBUG. At INFO this emitted two
		// lines for every block in the conversation — a 100-block PPT turn
		// added 200 log entries before any tool work was logged.
		globals.Debug(fmt.Sprintf("[Agent] 🔵 Calling iterator.Next() at %s", time.Now().Format("15:04:05.000")))
		msg, err := iterator.Next(ctx)
		globals.Debug(fmt.Sprintf("[Agent] 🟢 iterator.Next() returned at %s", time.Now().Format("15:04:05.000")))

		if err != nil {
			if err == claudecode.ErrNoMoreMessages {
				globals.Debug("[Agent] ⏹️ Loop ended: ErrNoMoreMessages")
				break
			}
			stderrTail := recentCLIErrors(stderrLines, &stderrMu)
			if stderrTail != "" {
				globals.Error(fmt.Sprintf("[Agent] iterator.Next stderr tail:\n%s", stderrTail))
			}
			logTransportHint(err, stderrTail)
			globals.Error(fmt.Sprintf("[Agent] ❌ Loop ended with error: %v", err))
			// Send email notification for message iteration failure
			NotifyAgentError(ctx, err, sessionID, UserIDStringFromContext(ctx), agentSessionID)
			return nil, fmt.Errorf("failed to get next message: %w", err)
		}
		if msg == nil {
			globals.Info("[Agent] ⏹️ Loop ended: msg is nil")
			break
		}

		// Per-message tracing demoted to DEBUG, matching the same call
		// shape commit e25f7d6a applied to ResultMessage logging and
		// the iterator.Next traces above. At chat-scale a 100-block
		// PPT turn used to emit one of these per message dwarfing
		// real signal in production logs.
		globals.Debug(fmt.Sprintf("[Agent] 📨 Received message type: %T", msg))

		switch m := msg.(type) {
		case *claudecode.AssistantMessage:
			messageJSON, blockJSONs, err := marshalAssistantMessage(m)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal assistant message: %w", err)
			}

			messageIndex := len(conversation.Messages)
			conversation.Messages = append(conversation.Messages, messageJSON)

			globals.Debug(fmt.Sprintf("[Agent] AssistantMessage #%d: %d blocks",
				messageIndex, len(blockJSONs)))

			if callbacks.OnMessage != nil {
				invokeSafe("assistant-message", func() {
					callbacks.OnMessage("assistant", messageJSON, messageIndex)
				})
			}

			if callbacks.OnBlock != nil {
				for blockIndex, block := range blockJSONs {
					blockIndex, block := blockIndex, block
					invokeSafe("assistant-block", func() {
						callbacks.OnBlock("assistant", block, messageIndex, blockIndex)
					})
					logBlockInfo(blockIndex, block)
				}
			}

		case *claudecode.UserMessage:
			// Directly marshal the entire UserMessage to preserve both string and block content
			messageJSON, err := json.Marshal(m)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal user message: %w", err)
			}

			messageIndex := len(conversation.Messages)
			conversation.Messages = append(conversation.Messages, messageJSON)

			globals.Debug(fmt.Sprintf("[Agent] UserMessage #%d (content type: %T)",
				messageIndex, m.Content))

			if callbacks.OnMessage != nil {
				invokeSafe("user-message", func() {
					callbacks.OnMessage("user", messageJSON, messageIndex)
				})
			}

			// Extract blocks for OnBlock callback if needed
			if callbacks.OnBlock != nil {
				contentBlocks := ExtractContentBlocksFromUserMessage(m.Content)
				if len(contentBlocks) > 0 {
					blockJSONs, err := marshalContentBlocks(contentBlocks)
					if err == nil {
						for blockIndex, block := range blockJSONs {
							blockIndex, block := blockIndex, block
							invokeSafe("user-block", func() {
								callbacks.OnBlock("user", block, messageIndex, blockIndex)
							})
							globals.Debug(fmt.Sprintf("[Agent]   Block #%d: user tool_result", blockIndex))
						}
					}
				}
			}

		case *claudecode.ResultMessage:
			resultJSON, err := json.Marshal(m)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal result message: %w", err)
			}

			conversation.Result = resultJSON

			// Cross-check SessionID extraction against the direct
			// field so a future SDK refactor that changes the
			// payload shape doesn't silently break our session-
			// recovery path. Only the mismatch case warrants a
			// log line per turn — the happy path used to spam 5+
			// Info logs per result message which dominated
			// production log volume at chat-scale.
			extracted := extractSessionIDFromResult(resultJSON)
			if m.SessionID != extracted {
				globals.Warn(fmt.Sprintf("[Agent] SessionID mismatch: direct=%q extracted=%q", m.SessionID, extracted))
			} else if m.SessionID == "" {
				globals.Warn("[Agent] SessionID is empty from SDK")
			}

			globals.Info(fmt.Sprintf("[Agent] Result: session_id=%s, turns=%d, duration=%dms",
				m.SessionID, m.NumTurns, m.DurationMs))

			// Surface SDK-side failure metadata when present. The CLI
			// emits api_error_status (since v2.1.110), stop_reason,
			// and total_cost_usd on the result frame — without these
			// in the log stream, an upstream 429 burst or a runaway
			// max_tokens loop is invisible until users complain.
			//
			// Errors[] carries SDK/CLI-side error strings (transport
			// hiccups, parser issues) that aren't necessarily fatal
			// but are worth correlating with the upstream telemetry.
			// PermissionDenials length tells us when the security
			// hook blocked tool calls — useful for tuning the
			// PathValidator without combing through Debug logs.
			if m.IsError || m.APIErrorStatus != nil || (m.StopReason != nil && *m.StopReason != "end_turn") || len(m.Errors) > 0 || len(m.PermissionDenials) > 0 {
				apiStatus := "<nil>"
				if m.APIErrorStatus != nil {
					apiStatus = fmt.Sprintf("%d", *m.APIErrorStatus)
				}
				stopReason := "<nil>"
				if m.StopReason != nil {
					stopReason = *m.StopReason
				}
				globals.Warn(fmt.Sprintf("[Agent] Result error/abnormal: is_error=%v api_status=%s stop_reason=%s subtype=%s errors=%d denials=%d",
					m.IsError, apiStatus, stopReason, m.Subtype, len(m.Errors), len(m.PermissionDenials)))
				// Errors[] strings can carry stack-trace fragments and
				// in rare cases echo upstream auth headers — scrub
				// before logging, same shape as the CLI stderr handler.
				for i, errStr := range m.Errors {
					globals.Warn(fmt.Sprintf("[Agent] Result errors[%d]: %s", i, scrubCredentialsFromLog(errStr)))
				}
			}
			if m.TotalCostUSD != nil {
				globals.Debug(fmt.Sprintf("[Agent] Result cost: total_cost_usd=%.6f session_id=%s", *m.TotalCostUSD, m.SessionID))
			}

			if callbacks.OnDone != nil {
				invokeSafe("result-done", func() {
					callbacks.OnDone(resultJSON)
				})
			}

		case *claudecode.SystemMessage:
			globals.Debug(fmt.Sprintf("[Agent] SystemMessage: subtype=%s", m.Subtype))

		case *claudecode.TaskStartedMessage:
			// task_started fires when the model dispatches a subagent
			// (Task tool). Currently the project doesn't allowlist Task,
			// but the SDK can emit these in MCP-tool flows or future
			// SDK-managed subagent paths. Log as Info so a real subagent
			// dispatch is visible without polluting per-turn output.
			globals.Info(fmt.Sprintf("[Agent] TaskStarted: id=%s task_type=%s desc=%q", m.TaskID, derefStr(m.TaskType), m.Description))

		case *claudecode.TaskProgressMessage:
			// task_progress fires during a long-running subagent task.
			// Log at Debug because a single multi-step task can emit
			// dozens of progress frames.
			lastTool := derefStr(m.LastToolName)
			globals.Debug(fmt.Sprintf("[Agent] TaskProgress: id=%s last_tool=%s desc=%q", m.TaskID, lastTool, m.Description))

		case *claudecode.TaskNotificationMessage:
			// task_notification carries the subagent's terminal status.
			// Log at Info on success, Warn on the error/failed status —
			// downstream ops can correlate failed subagents with parent
			// turn failures without parsing message frames.
			level := globals.Info
			if string(m.Status) == "error" || string(m.Status) == "failed" {
				level = globals.Warn
			}
			level(fmt.Sprintf("[Agent] TaskNotification: id=%s status=%s output_file=%s summary=%q", m.TaskID, m.Status, m.OutputFile, m.Summary))

		default:
			// Unhandled message types used to be Warn — but the SDK
			// adds new types (HookEventMessage when WithIncludeHookEvents
			// is set, MirrorErrorMessage when SessionStore is wired,
			// etc.) and a Warn-per-frame would dominate logs the moment
			// any new SDK feature is enabled. Debug is correct: a
			// developer turning on a new SDK option who wants to see
			// the new frame type can flip log level temporarily.
			globals.Debug(fmt.Sprintf("[Agent] Unhandled message type: %T", msg))
		}
	}

	globals.Info(fmt.Sprintf("[Agent] Loop completed. Total messages: %d", len(conversation.Messages)))
	globals.Info(fmt.Sprintf("[Agent] conversation.Result length: %d bytes", len(conversation.Result)))

	if len(conversation.Result) == 0 {
		globals.Warn("[Agent] ⚠️ No ResultMessage received - conversation.Result is empty")
	}

	// Use standard UUID v4 for consistency with other models
	conversation.ID = uuid.New().String()
	conversation.ThreadID = sessionID
	conversation.CreatedAt = time.Now()

	return conversation, nil
}

func recentCLIErrors(lines []string, mu *sync.Mutex) string {
	if mu == nil {
		return ""
	}
	mu.Lock()
	defer mu.Unlock()
	if len(lines) == 0 {
		return ""
	}
	start := 0
	if len(lines) > 10 {
		start = len(lines) - 10
	}
	return strings.Join(lines[start:], "\n")
}

func logTransportHint(err error, stderrTail string) {
	lowerCombined := strings.ToLower(err.Error() + "\n" + stderrTail)
	switch {
	case strings.Contains(lowerCombined, "unknown option") || strings.Contains(lowerCombined, "input-format"):
		globals.Warn("[Agent] Hint: Claude CLI may be outdated or incompatible with stream-json flags. Please upgrade CLI.")
	case strings.Contains(lowerCombined, "unauthorized") || strings.Contains(lowerCombined, "invalid api key") || strings.Contains(lowerCombined, "401"):
		globals.Warn("[Agent] Hint: Selected agent account credentials/base_url are invalid. Please check API key and endpoint.")
	case strings.Contains(lowerCombined, "permission denied") || strings.Contains(lowerCombined, "operation not permitted"):
		globals.Warn("[Agent] Hint: Process lacks filesystem/execute permissions for workspace or CLI binary.")
	case strings.Contains(lowerCombined, "rate limit") || strings.Contains(lowerCombined, "quota"):
		globals.Warn("[Agent] Hint: Upstream model/account quota or rate limit reached.")
	case strings.Contains(lowerCombined, "xcode license"):
		globals.Warn("[Agent] Hint: macOS Xcode license is not accepted. Run `sudo xcodebuild -license` or isolate runtime HOME for Agent service.")
	}
}

// marshalAssistantMessage converts an AssistantMessage into JSON payloads for the message and its blocks.
func marshalAssistantMessage(msg *claudecode.AssistantMessage) (json.RawMessage, []json.RawMessage, error) {
	blockJSONs, err := marshalContentBlocks(msg.Content)
	if err != nil {
		return nil, nil, err
	}

	envelope := struct {
		Type    string            `json:"type"`
		Model   string            `json:"model,omitempty"`
		Content []json.RawMessage `json:"content"`
	}{
		Type:    "assistant",
		Model:   msg.Model,
		Content: blockJSONs,
	}

	data, err := json.Marshal(envelope)
	if err != nil {
		return nil, nil, err
	}

	return json.RawMessage(data), blockJSONs, nil
}

// marshalContentBlocks marshals SDK content blocks into raw JSON.
func marshalContentBlocks(blocks []claudecode.ContentBlock) ([]json.RawMessage, error) {
	result := make([]json.RawMessage, 0, len(blocks))
	for _, block := range blocks {
		data, err := json.Marshal(block)
		if err != nil {
			return nil, err
		}
		result = append(result, json.RawMessage(data))
	}
	return result, nil
}

// logBlockInfo provides structured logging for assistant blocks.
// One block per message scaled with token throughput; at INFO this
// pegged log volume to the model's output rate during long PPT turns.
// DEBUG keeps the same shape available when the per-block trace is
// actually wanted, without the routine flood.
func logBlockInfo(blockIndex int, block json.RawMessage) {
	var meta struct {
		Type string `json:"type"`
		Name string `json:"name,omitempty"`
	}
	if err := json.Unmarshal(block, &meta); err != nil {
		globals.Warn(fmt.Sprintf("[Agent]   Block #%d: unknown (failed to parse type)", blockIndex))
		return
	}

	if meta.Type == "tool_use" && meta.Name != "" {
		globals.Debug(fmt.Sprintf("[Agent]   Block #%d: tool_use (%s)", blockIndex, meta.Name))
	} else {
		globals.Debug(fmt.Sprintf("[Agent]   Block #%d: %s", blockIndex, meta.Type))
	}
}

// ExtractContentBlocksFromUserMessage extracts ContentBlock array from UserMessage.Content.
func ExtractContentBlocksFromUserMessage(content interface{}) []claudecode.ContentBlock {
	if content == nil {
		return []claudecode.ContentBlock{}
	}

	globals.Debug(fmt.Sprintf("[Agent] UserMessage content type: %T", content))

	if blocks, ok := content.([]claudecode.ContentBlock); ok {
		return blocks
	}

	// Handle slice of interfaces that implement ContentBlock
	if rawSlice, ok := content.([]interface{}); ok {
		result := make([]claudecode.ContentBlock, 0, len(rawSlice))
		for _, item := range rawSlice {
			if block, ok := item.(claudecode.ContentBlock); ok {
				result = append(result, block)
			} else {
				globals.Warn(fmt.Sprintf("[Agent] UserMessage content item has unexpected type: %T", item))
			}
		}
		if len(result) > 0 {
			return result
		}
	}

	if _, ok := content.(string); ok {
		return []claudecode.ContentBlock{}
	}

	globals.Warn(fmt.Sprintf("[Agent] Unknown UserMessage.Content type: %T", content))
	return []claudecode.ContentBlock{}
}

// GetContentSummary extracts a text summary from the last assistant message JSON payload.
func GetContentSummary(message json.RawMessage) string {
	if len(message) == 0 {
		return "[Agent conversation]"
	}

	var parsed struct {
		Type    string            `json:"type"`
		Content []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(message, &parsed); err != nil {
		return "[Agent conversation]"
	}
	if parsed.Type != "assistant" {
		return "[Agent conversation]"
	}

	var summary strings.Builder
	for _, block := range parsed.Content {
		var meta struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(block, &meta); err != nil {
			continue
		}

		switch meta.Type {
		case "text":
			var textBlock struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(block, &textBlock); err == nil {
				summary.WriteString(textBlock.Text)
			}
		case "tool_use":
			var toolBlock struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(block, &toolBlock); err == nil {
				summary.WriteString("[Using tool: " + toolBlock.Name + "] ")
			}
		case "image":
			var imageBlock struct {
				MimeType string `json:"mimeType"`
			}
			if err := json.Unmarshal(block, &imageBlock); err == nil {
				summary.WriteString(fmt.Sprintf("[Image: %s] ", imageBlock.MimeType))
			}
		}

		// Rune-aware truncation: `summary.Len()` is bytes, not visible
		// characters, and a naive `summary.String()[:100]` slices the
		// byte buffer at index 100. With Chinese / emoji / accented
		// content — i.e. most of this product's surface area — that
		// position commonly lands inside a 3- or 4-byte UTF-8 sequence
		// and produces invalid UTF-8 in the output. Convert to runes,
		// gate on rune count, and re-stringify so the cut always lands
		// on a code-point boundary.
		runes := []rune(summary.String())
		if len(runes) > 100 {
			return string(runes[:100]) + "..."
		}
	}

	if summary.Len() == 0 {
		return "[Agent conversation]"
	}

	return summary.String()
}

// isSessionNotFoundError checks if the error is a session not found error
func isSessionNotFoundError(err error) bool {
	if err == nil {
		return false
	}

	errMsg := err.Error()
	return strings.Contains(errMsg, ErrSessionNotFound) ||
		strings.Contains(errMsg, ErrInvalidSession) ||
		strings.Contains(errMsg, ErrSessionExpired) ||
		strings.Contains(errMsg, ErrSessionClosed)
}

// extractSessionIDFromResult extracts the session_id from a ResultMessage JSON
func extractSessionIDFromResult(resultJSON json.RawMessage) string {
	if len(resultJSON) == 0 {
		return ""
	}

	var result struct {
		SessionID string `json:"session_id"`
	}

	if err := json.Unmarshal(resultJSON, &result); err != nil {
		globals.Warn(fmt.Sprintf("[Agent] Failed to extract session_id from result: %v", err))
		return ""
	}

	return result.SessionID
}
