package workagent

import (
	"fmt"
	"strings"
	"time"

	claudecode "github.com/jonnyquan/claude-agent-sdk-go"
	claudesdk "github.com/jonnyquan/claude-agent-sdk-go/pkg/claudesdk"
)

// defaultTurnTimeout is the fallback per-turn wall-clock budget when
// config.ClaudeAgent.Timeout is unset or non-positive. 30 minutes is
// the documented config default — keep them aligned so there's no
// silent behaviour change between an unconfigured deploy and an
// explicit 1800s setting.
const defaultTurnTimeout = 30 * time.Minute

// effectiveTurnTimeout returns the per-turn ctx timeout. <=0 input
// (no config or explicit zero) falls back to defaultTurnTimeout —
// safer than zero (zero would cancel the ctx immediately and every
// turn would fail with DeadlineExceeded).
//
// The caller's parent ctx may carry its own (possibly shorter)
// deadline; context.WithTimeout already takes the min. This function
// only chooses the agent-side bound.
func effectiveTurnTimeout(configured time.Duration) time.Duration {
	if configured <= 0 {
		return defaultTurnTimeout
	}
	return configured
}

// ClaudeAgentGoConfig captures the reusable configuration for the Claude Code Go SDK.
type ClaudeAgentGoConfig struct {
	Model             string
	AllowedTools      []string
	PermissionMode    claudecode.PermissionMode
	MaxTurns          int
	MaxOutputTokens   int
	MaxThinkingTokens int
	// Timeout is the per-turn ctx wall-clock budget. <=0 means no
	// caller-side enforcement; the agent loop falls back to
	// defaultTurnTimeout to avoid an unbounded stuck turn (e.g. a
	// runaway Bash command, an upstream that never sends a result
	// frame). Sourced from config.ClaudeAgent.Timeout (seconds);
	// the per-turn ctx wraps the caller's parent so whichever
	// deadline is shorter wins.
	Timeout time.Duration
	// MaxBudgetUSD is a per-turn upstream-cost cap. <= 0 disables.
	// Intended as a runaway guard; see config.ClaudeAgent.MaxBudgetUSD
	// for the policy behind the value.
	MaxBudgetUSD float64
	// Sandbox carries already-translated SDK settings. nil when the
	// project's config has the sandbox disabled or when the platform
	// doesn't support it (Windows). The translator is
	// translateSandboxConfig — keep this field as the post-translation
	// shape so baseOptions doesn't have to re-translate per call.
	Sandbox *claudecode.SandboxSettings
	// SettingSources is the post-validation list of CLI config sources
	// the SDK should load. Empty/nil = SDK default (don't pass the
	// option). Valid entries: "user", "project", "local". Validation
	// (whitespace stripping, empty-entry filtering) happens at
	// BuildClientForAccount time via translateSettingSources so the
	// hot path doesn't re-walk the slice per turn.
	SettingSources []string
	Env            map[string]string
	// Note: Cwd is NOT part of client config — set dynamically per
	// turn via WithCwd in processAgentConversationInternal.
	// Note: SystemPrompt is NOT part of client config — built per turn
	// in processAgentConversationInternal where workspacePath is known
	// and filesContext / workspaceContext can be appended.
}

// ClaudeAgentGoClient uses the native Go SDK for Claude Code, avoiding HTTP overhead.
type ClaudeAgentGoClient struct {
	config ClaudeAgentGoConfig
}

// NewClaudeAgentGoClient creates a new Go SDK-based client with optional configuration.
//
// Defaults are deliberately conservative — the production caller in
// agent_client_manager.BuildClientForAccount sets every field
// explicitly, so these fallbacks only matter when a future caller
// (test, new code path, accidental partial config) leaves a field
// zero-valued. Notable safety choice: PermissionMode defaults to the
// SDK's "default" mode rather than BypassPermissions, so an
// unconfigured client doesn't silently auto-approve every Bash/Write
// call.
func NewClaudeAgentGoClient(cfg *ClaudeAgentGoConfig) *ClaudeAgentGoClient {
	defaultConfig := ClaudeAgentGoConfig{
		Model:             "", // Model should be provided by config, no hardcoded default
		AllowedTools:      []string{"Read", "Write", "Bash"},
		PermissionMode:    claudecode.PermissionModeDefault,
		MaxTurns:          10,
		MaxOutputTokens:   64000, // Default max output tokens
		MaxThinkingTokens: 31999, // Default max thinking tokens
		Env:               map[string]string{},
	}

	if cfg != nil {
		// Model is required and should come from config
		if trimmed := strings.TrimSpace(cfg.Model); trimmed != "" {
			defaultConfig.Model = trimmed
		}
		if len(cfg.AllowedTools) > 0 {
			defaultConfig.AllowedTools = append([]string{}, cfg.AllowedTools...)
		}
		if cfg.PermissionMode != "" {
			defaultConfig.PermissionMode = cfg.PermissionMode
		}
		if cfg.MaxTurns > 0 {
			defaultConfig.MaxTurns = cfg.MaxTurns
		}
		if cfg.MaxOutputTokens > 0 {
			defaultConfig.MaxOutputTokens = cfg.MaxOutputTokens
		}
		if cfg.MaxThinkingTokens > 0 {
			defaultConfig.MaxThinkingTokens = cfg.MaxThinkingTokens
		}
		if cfg.MaxBudgetUSD > 0 {
			defaultConfig.MaxBudgetUSD = cfg.MaxBudgetUSD
		}
		if cfg.Timeout > 0 {
			defaultConfig.Timeout = cfg.Timeout
		}
		if cfg.Sandbox != nil {
			defaultConfig.Sandbox = cfg.Sandbox
		}
		if len(cfg.SettingSources) > 0 {
			// Defensive copy so the client doesn't alias the caller's
			// slice — same pattern as AllowedTools above.
			defaultConfig.SettingSources = append([]string(nil), cfg.SettingSources...)
		}
		if cfg.Env != nil {
			defaultConfig.Env = copyStringMap(cfg.Env)
		}
	}

	return &ClaudeAgentGoClient{
		config: defaultConfig,
	}
}

// baseOptions returns the base options for all SDK calls.
func (c *ClaudeAgentGoClient) baseOptions() []claudecode.Option {
	opts := []claudecode.Option{
		claudecode.WithAllowedTools(c.config.AllowedTools...),
		claudecode.WithMaxTurns(c.config.MaxTurns),
		claudecode.WithPermissionMode(c.config.PermissionMode),
	}

	// Configure thinking via the typed option. WithMaxThinkingTokens
	// is the deprecated shortcut; WithThinking takes a full
	// ThinkingConfig that maps 1:1 to the CLI's thinking flags. Same
	// budget, future-proof against the deprecated shim being removed.
	if c.config.MaxThinkingTokens > 0 {
		opts = append(opts, claudesdk.WithThinking(claudesdk.ThinkingConfig{
			Type:         claudesdk.ThinkingTypeEnabled,
			BudgetTokens: c.config.MaxThinkingTokens,
		}))
	}

	// Per-turn upstream-cost cap. Off when MaxBudgetUSD <= 0 (default
	// for any deploy that hasn't opted in via config). When set, the
	// SDK terminates the turn with an error_max_budget_usd result
	// once cumulative cost crosses the threshold — useful as a
	// kill-switch against runaway tool loops, not as a normal-traffic
	// budget.
	if c.config.MaxBudgetUSD > 0 {
		opts = append(opts, claudecode.WithMaxBudgetUSD(c.config.MaxBudgetUSD))
	}

	// Bash sandbox isolation. Translated at BuildClientForAccount
	// time; nil here means disabled, unsupported platform, or
	// nil-config — append unconditionally is wrong because the SDK
	// treats *SandboxSettings(nil) as the "no override" sentinel
	// rather than a no-op.
	if c.config.Sandbox != nil {
		opts = append(opts, claudecode.WithSandbox(c.config.Sandbox))
	}

	// Settings-source lockdown. Empty slice → don't pass the option,
	// SDK loads its built-in default. Non-empty → SDK loads ONLY the
	// listed sources. Translated and validated at BuildClientForAccount
	// time so this hot path is a length check.
	if len(c.config.SettingSources) > 0 {
		opts = append(opts, claudesdk.WithSettingSources(c.config.SettingSources...))
	}

	if strings.TrimSpace(c.config.Model) != "" {
		opts = append(opts, claudecode.WithModel(c.config.Model))
	}

	// Merge environment variables with CLAUDE_CODE_MAX_OUTPUT_TOKENS
	envVars := copyStringMap(c.config.Env)
	if envVars == nil {
		envVars = make(map[string]string)
	}
	if c.config.MaxOutputTokens > 0 {
		envVars["CLAUDE_CODE_MAX_OUTPUT_TOKENS"] = fmt.Sprintf("%d", c.config.MaxOutputTokens)
	}
	if len(envVars) > 0 {
		opts = append(opts, claudecode.WithEnv(envVars))
	}

	// Note: Cwd is NOT set in baseOptions
	// Each thread sets its own workspace directory dynamically via WithCwd() in agent_processor.go

	return opts
}

// Helper functions

func copyStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
