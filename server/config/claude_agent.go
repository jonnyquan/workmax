package config

// ClaudeAgent represents Claude Agent configuration (using native Go SDK)
// Note: Account credentials (BaseURL, APIKey) are managed in w_workagent_account table
type ClaudeAgent struct {
	Enabled           bool   `mapstructure:"enabled" json:"enabled" yaml:"enabled"`                                     // Enable Agent mode
	Timeout           int    `mapstructure:"timeout" json:"timeout" yaml:"timeout"`                                     // Timeout in seconds (default: 1800)
	MaxTurns          int    `mapstructure:"max_turns" json:"max_turns" yaml:"max_turns"`                               // Maximum conversation turns (default: 20)
	MaxOutputTokens   int    `mapstructure:"max_output_tokens" json:"max_output_tokens" yaml:"max_output_tokens"`       // Maximum output tokens (default: 64000)
	MaxThinkingTokens int    `mapstructure:"max_thinking_tokens" json:"max_thinking_tokens" yaml:"max_thinking_tokens"` // Maximum thinking tokens (default: 31999)
	// MaxBudgetUSD is a per-turn upstream-cost cap enforced by the
	// SDK. The CLI tracks cumulative cost across the turn and returns
	// an error_max_budget_usd result when the threshold is exceeded.
	// Intended as a RUNAWAY GUARD, not a normal-traffic budget — set
	// it generously above expected per-turn spend so a buggy
	// infinite-tool-loop terminates cleanly instead of chewing through
	// the account balance. <= 0 disables the cap (SDK default).
	//
	// Distinct from the project's credit-reservation system, which
	// runs ABOVE this layer and meters end-user usage against
	// purchased credits. This cap is purely a kill-switch on the
	// upstream side.
	MaxBudgetUSD      float64 `mapstructure:"max_budget_usd" json:"max_budget_usd" yaml:"max_budget_usd"`
	WorkspaceRoot     string  `mapstructure:"workspace_root" json:"workspace_root" yaml:"workspace_root"` // Agent workspace root directory
	AnthropicLog      string  `mapstructure:"anthropic_log" json:"anthropic_log" yaml:"anthropic_log"`    // Anthropic SDK logging level (debug, info, warn, error)
	// SettingSources optionally restricts which CLI config sources
	// the SDK loads. Valid values: "user", "project", "local". When
	// nil/empty, the SDK uses its built-in default (loads everything
	// it can find). When set, ONLY the listed sources load — useful
	// as defense-in-depth on top of the existing HOME isolation in
	// agent_client_manager.buildEnvVarsFromAccount.
	//
	// Threat model: a model with Write access can create a
	// .claude/settings.json file inside its workspace CWD. If the
	// CLI's project-level settings loader picks it up, the file's
	// directives (e.g. permission overrides) take effect mid-turn.
	// HOME isolation already handles the user-level path; this knob
	// closes the project-level path.
	//
	// Set to ["user"] to allow only HOME-isolated user settings (the
	// directory we control). Set to [] to leave the default behaviour.
	// Empty string entries are tolerated and stripped at translation
	// time so a stray YAML "- " doesn't disable the lockdown.
	SettingSources    []string `mapstructure:"setting_sources" json:"setting_sources" yaml:"setting_sources"`

	// Sandbox configuration for bash command isolation (macOS/Linux only)
	Sandbox *ClaudeAgentSandbox `mapstructure:"sandbox" json:"sandbox" yaml:"sandbox"`
}

// ClaudeAgentSandbox represents sandbox configuration for bash command isolation
type ClaudeAgentSandbox struct {
	Enabled                   bool     `mapstructure:"enabled" json:"enabled" yaml:"enabled"`                                                                // Enable bash sandboxing
	AutoAllowBashIfSandboxed  bool     `mapstructure:"auto_allow_bash" json:"auto_allow_bash" yaml:"auto_allow_bash"`                                        // Auto-approve bash commands when sandboxed (default: true)
	ExcludedCommands          []string `mapstructure:"excluded_commands" json:"excluded_commands" yaml:"excluded_commands"`                                  // Commands that run outside sandbox (e.g., ["git", "docker"])
	AllowUnsandboxedCommands  bool     `mapstructure:"allow_unsandboxed" json:"allow_unsandboxed" yaml:"allow_unsandboxed"`                                  // Allow commands to bypass sandbox (default: true)
	AllowLocalBinding         bool     `mapstructure:"allow_local_binding" json:"allow_local_binding" yaml:"allow_local_binding"`                            // Allow binding to localhost ports (macOS only)
	AllowAllUnixSockets       bool     `mapstructure:"allow_all_unix_sockets" json:"allow_all_unix_sockets" yaml:"allow_all_unix_sockets"`                   // Allow all Unix sockets (less secure)
	AllowUnixSockets          []string `mapstructure:"allow_unix_sockets" json:"allow_unix_sockets" yaml:"allow_unix_sockets"`                               // Specific Unix socket paths to allow
	EnableWeakerNestedSandbox bool     `mapstructure:"enable_weaker_nested_sandbox" json:"enable_weaker_nested_sandbox" yaml:"enable_weaker_nested_sandbox"` // Enable weaker sandbox for Docker (Linux only)
}
