package workagent

import (
	"runtime"
	"sync"

	"server/config"
	"server/globals"

	claudecode "github.com/jonnyquan/claude-agent-sdk-go"
)

// sandboxWindowsWarnOnce gates the "sandbox enabled on Windows"
// warning so a per-turn log line doesn't repeat on every build.
// Once-per-process is enough for an operator to spot the
// misconfiguration without pollution.
var sandboxWindowsWarnOnce sync.Once

// translateSandboxConfig converts the project's flat sandbox config
// into the SDK's nested SandboxSettings shape. Returns nil when the
// sandbox is disabled, nil, or running on a platform that doesn't
// support bash sandboxing — the caller appends WithSandbox only when
// the result is non-nil.
//
// The SDK's contract (see SandboxSettings comments in the SDK):
// sandboxing is macOS/Linux only; on Windows the setting flows
// through but the underlying CLI rejects/ignores it. Translating to
// nil on Windows surfaces the platform mismatch in our own log
// stream rather than leaving it to the SDK to fail silently.
//
// Filesystem and network ALLOW/DENY rules are NOT controlled by
// these flags; they live on permission rules (Edit/Read/WebFetch
// allow/deny). This function only carries process-isolation knobs
// (Bash sandbox, network endpoints, Unix sockets).
func translateSandboxConfig(cfg *config.ClaudeAgentSandbox) *claudecode.SandboxSettings {
	if cfg == nil || !cfg.Enabled {
		return nil
	}

	if runtime.GOOS == "windows" {
		// One-shot warn at first translation attempt: an operator
		// who set sandbox.enabled=true on Windows should know it's
		// being silently dropped, otherwise they'll assume the
		// sandbox is active when it isn't.
		sandboxWindowsWarnOnce.Do(func() {
			globals.Warn("[Agent] sandbox.enabled=true on Windows — bash sandboxing is macOS/Linux only; sandbox config skipped")
		})
		return nil
	}

	sandbox := &claudecode.SandboxSettings{
		Enabled:                   cfg.Enabled,
		AutoAllowBashIfSandboxed:  cfg.AutoAllowBashIfSandboxed,
		ExcludedCommands:          append([]string(nil), cfg.ExcludedCommands...),
		AllowUnsandboxedCommands:  cfg.AllowUnsandboxedCommands,
		EnableWeakerNestedSandbox: cfg.EnableWeakerNestedSandbox,
	}

	// Network-related fields are flat on our config but nested in
	// the SDK's struct. Lift them only when at least one is set —
	// allocating an empty SandboxNetworkConfig is wire-noise.
	if cfg.AllowLocalBinding || cfg.AllowAllUnixSockets || len(cfg.AllowUnixSockets) > 0 {
		sandbox.Network = &claudecode.SandboxNetworkConfig{
			AllowLocalBinding:   cfg.AllowLocalBinding,
			AllowAllUnixSockets: cfg.AllowAllUnixSockets,
			AllowUnixSockets:    append([]string(nil), cfg.AllowUnixSockets...),
		}
	}

	return sandbox
}
