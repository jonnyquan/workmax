package workagent

import (
	"reflect"
	"runtime"
	"testing"

	"server/config"
)

// translateSandboxConfig is the only place the project decides
// whether the SDK gets a sandbox configured. Drift here directly
// affects whether bash commands run isolated — pin every branch.
func TestTranslateSandboxConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sandbox tests assume macOS/Linux; Windows path is exercised by TestTranslateSandboxConfig_WindowsSkips")
	}

	cases := []struct {
		name string
		in   *config.ClaudeAgentSandbox
		// asserts are run against the result; nil result = nil asserts
		want func(t *testing.T, got interface{})
	}{
		{
			name: "nil config → nil result (sandbox not configured)",
			in:   nil,
			want: func(t *testing.T, got interface{}) {
				if got != nil {
					t.Errorf("expected nil, got %#v", got)
				}
			},
		},
		{
			name: "Enabled=false → nil result (don't pass through a disabled sandbox)",
			in: &config.ClaudeAgentSandbox{
				Enabled:                  false,
				AutoAllowBashIfSandboxed: true, // these should be ignored when disabled
			},
			want: func(t *testing.T, got interface{}) {
				if got != nil {
					t.Errorf("expected nil, got %#v", got)
				}
			},
		},
		{
			name: "Enabled=true minimal → flat fields mapped, network nil",
			in: &config.ClaudeAgentSandbox{
				Enabled:                  true,
				AutoAllowBashIfSandboxed: true,
				AllowUnsandboxedCommands: true,
			},
			want: func(t *testing.T, got interface{}) {
				sandbox := got.(*sandboxFields)
				if !sandbox.Enabled {
					t.Error("Enabled = false, want true")
				}
				if !sandbox.AutoAllowBash {
					t.Error("AutoAllowBashIfSandboxed = false, want true")
				}
				if !sandbox.AllowUnsandboxed {
					t.Error("AllowUnsandboxedCommands = false, want true")
				}
				if sandbox.NetworkSet {
					t.Error("Network non-nil but no network fields set in input")
				}
			},
		},
		{
			name: "Enabled=true with excluded commands → list copied not aliased",
			in: &config.ClaudeAgentSandbox{
				Enabled:          true,
				ExcludedCommands: []string{"git", "docker"},
			},
			want: func(t *testing.T, got interface{}) {
				sandbox := got.(*sandboxFields)
				if !reflect.DeepEqual(sandbox.ExcludedCommands, []string{"git", "docker"}) {
					t.Errorf("ExcludedCommands = %v, want [git docker]", sandbox.ExcludedCommands)
				}
			},
		},
		{
			name: "Network fields lift to nested config when AllowLocalBinding set",
			in: &config.ClaudeAgentSandbox{
				Enabled:           true,
				AllowLocalBinding: true,
			},
			want: func(t *testing.T, got interface{}) {
				sandbox := got.(*sandboxFields)
				if !sandbox.NetworkSet {
					t.Fatal("Network = nil, want non-nil")
				}
				if !sandbox.NetworkAllowLocalBinding {
					t.Error("Network.AllowLocalBinding = false, want true")
				}
			},
		},
		{
			name: "Network fields lift when AllowUnixSockets list present",
			in: &config.ClaudeAgentSandbox{
				Enabled:          true,
				AllowUnixSockets: []string{"/tmp/sock1", "/tmp/sock2"},
			},
			want: func(t *testing.T, got interface{}) {
				sandbox := got.(*sandboxFields)
				if !sandbox.NetworkSet {
					t.Fatal("Network = nil, want non-nil")
				}
				if !reflect.DeepEqual(sandbox.NetworkAllowUnixSockets, []string{"/tmp/sock1", "/tmp/sock2"}) {
					t.Errorf("AllowUnixSockets = %v", sandbox.NetworkAllowUnixSockets)
				}
			},
		},
		{
			name: "Network nil when no network field is set (don't allocate empty config)",
			in: &config.ClaudeAgentSandbox{
				Enabled:                  true,
				AutoAllowBashIfSandboxed: true,
				ExcludedCommands:         []string{"git"},
				// no AllowLocalBinding, AllowAllUnixSockets, AllowUnixSockets
			},
			want: func(t *testing.T, got interface{}) {
				sandbox := got.(*sandboxFields)
				if sandbox.NetworkSet {
					t.Error("Network non-nil but no network fields requested it")
				}
			},
		},
		{
			name: "EnableWeakerNestedSandbox passes through (Linux-only flag, but flat-mapped)",
			in: &config.ClaudeAgentSandbox{
				Enabled:                   true,
				EnableWeakerNestedSandbox: true,
			},
			want: func(t *testing.T, got interface{}) {
				sandbox := got.(*sandboxFields)
				if !sandbox.WeakerNested {
					t.Error("EnableWeakerNestedSandbox = false, want true")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := translateSandboxConfig(tc.in)
			if raw == nil {
				tc.want(t, nil)
				return
			}
			fields := &sandboxFields{
				Enabled:                  raw.Enabled,
				AutoAllowBash:            raw.AutoAllowBashIfSandboxed,
				AllowUnsandboxed:         raw.AllowUnsandboxedCommands,
				ExcludedCommands:         raw.ExcludedCommands,
				WeakerNested:             raw.EnableWeakerNestedSandbox,
				NetworkSet:               raw.Network != nil,
			}
			if raw.Network != nil {
				fields.NetworkAllowLocalBinding = raw.Network.AllowLocalBinding
				fields.NetworkAllowAllUnixSockets = raw.Network.AllowAllUnixSockets
				fields.NetworkAllowUnixSockets = raw.Network.AllowUnixSockets
			}
			tc.want(t, fields)
		})
	}
}

// TestTranslateSandboxConfig_CopiesSlices confirms the translator
// doesn't alias the caller's slice memory. A future caller mutating
// its config struct after BuildClientForAccount would otherwise
// corrupt the live SDK options for the in-flight turn.
func TestTranslateSandboxConfig_CopiesSlices(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows path returns nil; slice-aliasing question doesn't apply")
	}

	src := &config.ClaudeAgentSandbox{
		Enabled:          true,
		ExcludedCommands: []string{"git"},
		AllowUnixSockets: []string{"/tmp/sock1"},
	}
	got := translateSandboxConfig(src)
	if got == nil {
		t.Fatal("translateSandboxConfig returned nil for enabled config")
	}

	// Mutate the source slices and confirm the translation result is unaffected.
	src.ExcludedCommands[0] = "MUTATED"
	src.AllowUnixSockets[0] = "MUTATED"

	if got.ExcludedCommands[0] != "git" {
		t.Errorf("ExcludedCommands aliased: got %q after source mutation", got.ExcludedCommands[0])
	}
	if got.Network != nil && got.Network.AllowUnixSockets[0] != "/tmp/sock1" {
		t.Errorf("AllowUnixSockets aliased: got %q after source mutation", got.Network.AllowUnixSockets[0])
	}
}

// sandboxFields is a flat assertion struct so test cases can compare
// the cross-cutting result without poking at the SDK type's internals
// (which may shift across SDK versions). Keeping the assertion shape
// flat means the tests survive minor SDK reshapes.
type sandboxFields struct {
	Enabled                    bool
	AutoAllowBash              bool
	AllowUnsandboxed           bool
	ExcludedCommands           []string
	WeakerNested               bool
	NetworkSet                 bool
	NetworkAllowLocalBinding   bool
	NetworkAllowAllUnixSockets bool
	NetworkAllowUnixSockets    []string
}
