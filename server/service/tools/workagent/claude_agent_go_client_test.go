package workagent

import (
	"testing"
	"time"

	claudecode "github.com/jonnyquan/claude-agent-sdk-go"
)

// Defaults must be conservative — production passes everything
// explicitly, but a future caller forgetting a field shouldn't
// silently get max-permissive bypass mode.
func TestNewClaudeAgentGoClient_NilConfig_HasSafeDefaults(t *testing.T) {
	c := NewClaudeAgentGoClient(nil)
	if c.config.PermissionMode != claudecode.PermissionModeDefault {
		t.Errorf("default PermissionMode = %q; want %q", c.config.PermissionMode, claudecode.PermissionModeDefault)
	}
	if c.config.PermissionMode == claudecode.PermissionModeBypassPermissions {
		t.Errorf("default PermissionMode is BypassPermissions — unconfigured client would auto-approve every tool call")
	}
}

// Caller-provided config must override the conservative defaults
// without surprises.
func TestNewClaudeAgentGoClient_ExplicitConfigWins(t *testing.T) {
	cfg := &ClaudeAgentGoConfig{
		Model:          "claude-opus-4-7",
		PermissionMode: claudecode.PermissionModeBypassPermissions,
	}
	c := NewClaudeAgentGoClient(cfg)
	if c.config.Model != "claude-opus-4-7" {
		t.Errorf("Model = %q; want claude-opus-4-7", c.config.Model)
	}
	if c.config.PermissionMode != claudecode.PermissionModeBypassPermissions {
		t.Errorf("PermissionMode caller-override lost: %q", c.config.PermissionMode)
	}
}

// Empty cfg fields must NOT clobber callers that meant to leave
// them blank (existing behaviour: blank ⇒ fall back to default).
// Pin so a future "always-overwrite" refactor doesn't change semantics.
func TestNewClaudeAgentGoClient_EmptyFieldsFallToDefault(t *testing.T) {
	cfg := &ClaudeAgentGoConfig{
		Model:          "",
		PermissionMode: "",
	}
	c := NewClaudeAgentGoClient(cfg)
	if c.config.PermissionMode != claudecode.PermissionModeDefault {
		t.Errorf("empty PermissionMode should fall to default; got %q", c.config.PermissionMode)
	}
}

// effectiveTurnTimeout is the only place the agent loop decides
// "how long is too long for one turn." Drift here directly affects
// whether a stuck Bash command runs forever — pin every branch.
func TestEffectiveTurnTimeout(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"zero falls to safety default", 0, defaultTurnTimeout},
		{"negative falls to safety default", -1 * time.Second, defaultTurnTimeout},
		{"positive value passes through", 5 * time.Minute, 5 * time.Minute},
		{"large value passes through (no upper clamp by design)", 4 * time.Hour, 4 * time.Hour},
		{"1 nanosecond passes through (callers responsibility to pick sane values)", 1 * time.Nanosecond, 1 * time.Nanosecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := effectiveTurnTimeout(tc.in)
			if got != tc.want {
				t.Errorf("effectiveTurnTimeout(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// defaultTurnTimeout must align with config.ClaudeAgent.Timeout's
// documented default (1800s = 30min). Drift here means an explicit
// 1800s setting differs from an unconfigured deploy — would surprise
// operators who left the value blank.
func TestDefaultTurnTimeout_MatchesConfigDocComment(t *testing.T) {
	const documentedConfigDefaultSeconds = 1800
	wantDuration := time.Duration(documentedConfigDefaultSeconds) * time.Second
	if defaultTurnTimeout != wantDuration {
		t.Errorf("defaultTurnTimeout = %v, but config.ClaudeAgent.Timeout doc-default is %ds (%v) — drift will silently change behaviour for deploys that left the field blank", defaultTurnTimeout, documentedConfigDefaultSeconds, wantDuration)
	}
}

// Timeout field on ClaudeAgentGoConfig must round-trip through
// the constructor's "explicit value wins" path. <=0 falls back
// (effectiveTurnTimeout handles that downstream); >0 is preserved.
func TestNewClaudeAgentGoClient_TimeoutPropagates(t *testing.T) {
	cfg := &ClaudeAgentGoConfig{Timeout: 10 * time.Minute}
	c := NewClaudeAgentGoClient(cfg)
	if c.config.Timeout != 10*time.Minute {
		t.Errorf("Timeout caller-override lost: got %v, want 10m", c.config.Timeout)
	}
}

func TestNewClaudeAgentGoClient_ZeroTimeoutFallsToDefault(t *testing.T) {
	cfg := &ClaudeAgentGoConfig{Timeout: 0}
	c := NewClaudeAgentGoClient(cfg)
	// At the constructor level, Timeout=0 stays 0 — the safety
	// default is applied by effectiveTurnTimeout at runtime, not
	// here. This test pins that behaviour: don't fold the default
	// into the config struct, or callers reading c.config.Timeout
	// for telemetry would see "30m configured" when nothing was
	// actually configured.
	if c.config.Timeout != 0 {
		t.Errorf("constructor should leave Timeout=0 alone; got %v", c.config.Timeout)
	}
}
