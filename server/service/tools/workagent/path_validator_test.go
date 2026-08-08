package workagent

import (
	"strings"
	"testing"
)

// Path validation tests cover traversal/system-path cases plus the
// Bash preflight's known high-signal bypass shapes. Bash command
// matching is still not a complete security boundary; the runtime
// sandbox remains the real containment layer.

func TestValidatePathSafe(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		wantBlock bool
	}{
		// Absolute system paths
		{"etc passwd", "/etc/passwd", true},
		{"etc shadow", "/etc/shadow", true},
		{"root home", "/root/.ssh/id_rsa", true},
		{"proc", "/proc/self/environ", true},
		{"sys", "/sys/class", true},
		{"var log", "/var/log/auth.log", true},
		{"tmp", "/tmp/anything", true},
		{"windows uppercase", "C:\\Windows\\System32\\config", true},
		{"windows lowercase", "c:/windows/system32", true},

		// Forbidden keywords anywhere in path
		{"hidden ssh", "stuff/.ssh/keys", true},
		{"env file", "uploads/.env", true},
		{"aws creds", ".aws/credentials", true},

		// Traversal
		{"parent traversal", "../etc/passwd", true},
		{"deep traversal", "uploads/../../../etc/passwd", true},
		{"single dotdot", "..", true},

		// Unrelated absolute path
		{"foreign abs", "/Users/other/secrets", true},

		// Safe paths
		{"relative file", "uploads/report.pdf", false},
		{"nested relative", "thread_x/uploads/data.csv", false},
		{"empty", "", false},
	}

	pv := NewPathValidator("/workspace/root")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := pv.ValidatePathSafe(tt.path)
			if tt.wantBlock && err == nil {
				t.Errorf("ValidatePathSafe(%q) returned nil, want error", tt.path)
			}
			if !tt.wantBlock && err != nil {
				t.Errorf("ValidatePathSafe(%q) returned %v, want nil", tt.path, err)
			}
		})
	}
}

// Regression: when the workspace lives under a forbidden prefix
// (production deploys often use /var/lib/<app>/... or /opt/<app>/...,
// dev uses /tmp/...), the original validator blocked every legit tool
// call at the system-prefix denylist before the workspace-allow check
// fired. Verify the fast-allow path now wins.
func TestValidatePathSafe_WorkspaceUnderSystemPrefix(t *testing.T) {
	cases := []struct {
		name      string
		root      string
		path      string
		wantAllow bool
	}{
		{"var-deployed workspace", "/var/lib/workmax/agent_workspace", "/var/lib/workmax/agent_workspace/uploads/x.txt", true},
		{"opt-deployed workspace", "/opt/workmax/work", "/opt/workmax/work/outputs/deck.pptx", true},
		{"tmp-deployed dev", "/tmp/workmax-dev", "/tmp/workmax-dev/thread/file.csv", true},
		// Same-prefix-but-outside still gets blocked: /var/lib/OTHER
		// is NOT inside /var/lib/workmax/agent_workspace.
		{"sibling under same prefix blocked", "/var/lib/workmax/agent_workspace", "/var/lib/other/secret.txt", false},
		// Real system file is still blocked.
		{"system file outside workspace blocked", "/var/lib/workmax/agent_workspace", "/etc/passwd", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pv := NewPathValidator(tc.root)
			err := pv.ValidatePathSafe(tc.path)
			if tc.wantAllow && err != nil {
				t.Errorf("workspace path falsely blocked: %v", err)
			}
			if !tc.wantAllow && err == nil {
				t.Errorf("non-workspace path was not blocked: path=%q root=%q", tc.path, tc.root)
			}
		})
	}
}

// `..` detection must operate on path SEGMENTS, not substrings.
// A filename like "report..backup.pdf" was being false-positive
// blocked by the old `strings.Contains(path, "..")` check.
func TestValidatePathSafe_DotDotIsSegmentNotSubstring(t *testing.T) {
	pv := NewPathValidator("/workspace/root")

	// These contain ".." as part of a filename — must NOT trip
	// the traversal check.
	allowed := []string{
		"uploads/report..backup.pdf",
		"thread/v1..v2.diff",
		"some..file.txt",
	}
	for _, p := range allowed {
		if err := pv.ValidatePathSafe(p); err != nil {
			t.Errorf("legit filename %q falsely blocked: %v", p, err)
		}
	}

	// Real traversal must still get caught.
	for _, p := range []string{"../etc/passwd", "uploads/../../etc/passwd", ".."} {
		if err := pv.ValidatePathSafe(p); err == nil {
			t.Errorf("traversal %q not blocked", p)
		}
	}
}

func TestValidateToolCall_NonPathTool(t *testing.T) {
	pv := NewPathValidator("/workspace/root")
	if err := pv.ValidateToolCall("UnknownTool", map[string]interface{}{"x": "/etc/passwd"}); err != nil {
		t.Errorf("non-path tool should be allowed: %v", err)
	}
}

func TestValidateToolCall_FileTools(t *testing.T) {
	pv := NewPathValidator("/workspace/root")

	cases := []struct {
		name      string
		tool      string
		args      map[string]interface{}
		wantBlock bool
	}{
		{"Read safe", "Read", map[string]interface{}{"path": "uploads/x.txt"}, false},
		{"Read traversal", "Read", map[string]interface{}{"path": "../../../etc/passwd"}, true},
		{"Read system", "Read", map[string]interface{}{"file_path": "/etc/passwd"}, true},
		{"Write traversal", "Write", map[string]interface{}{"file_path": "../etc/passwd"}, true},
		{"Edit safe", "Edit", map[string]interface{}{"filepath": "report.md"}, false},
		{"non-string arg ignored", "Read", map[string]interface{}{"path": 42}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := pv.ValidateToolCall(tc.tool, tc.args)
			if tc.wantBlock && err == nil {
				t.Errorf("expected block, got nil")
			}
			if !tc.wantBlock && err != nil {
				t.Errorf("expected allow, got %v", err)
			}
		})
	}
}

func TestValidateToolCall_BashBlocksObvious(t *testing.T) {
	pv := NewPathValidator("/workspace/root")

	blocked := []string{
		"cat /etc/passwd",
		"rm -rf /",
		"sudo apt install evil",
		"cat /etc/shadow > /tmp/x",
		"ls .ssh/",
		"chmod 777 file",
		"dd if=/dev/zero of=/dev/sda",
	}
	for _, cmd := range blocked {
		t.Run("block_"+cmd, func(t *testing.T) {
			err := pv.ValidateToolCall("Bash", map[string]interface{}{"command": cmd})
			if err == nil {
				t.Errorf("Bash %q should be blocked, was allowed", cmd)
			}
		})
	}

	allowed := []string{
		"ls uploads",
		"echo hello",
		"python3 script.py",
	}
	for _, cmd := range allowed {
		t.Run("allow_"+cmd, func(t *testing.T) {
			err := pv.ValidateToolCall("Bash", map[string]interface{}{"command": cmd})
			if err != nil {
				t.Errorf("Bash %q should be allowed, got %v", cmd, err)
			}
		})
	}
}

// TestValidateToolCall_BashBlocksKnownBypasses pins the extra
// normalization layer that catches common shell quoting, escaping,
// whitespace, and trivial command-substitution bypasses. This does not
// make Bash string matching a sandbox; it just prevents known bad shapes
// from slipping through the preflight hook.
func TestValidateToolCall_BashBlocksKnownBypasses(t *testing.T) {
	pv := NewPathValidator("/workspace/root")

	bypasses := []struct {
		name string
		cmd  string
	}{
		// Quoting splits the keyword
		{"double_quote_split", `cat /et""c/passwd`},
		{"single_quote_split", `cat /et''c/passwd`},
		{"backslash_in_word", `cat /e\tc/passwd`},
		// Command substitution + glob assembly
		{"command_substitution", `cat $(echo /etc/pas)swd`},
		{"backtick_substitution", "cat `echo /etc/passwd`"},
		// Whitespace tricks
		{"double_space_rm", "rm  -rf /"},
		{"tab_separator", "rm\t-rf\t/"},
		// Encoded paths
		{"hex_escape", `cat $'\x2fetc/passwd'`},
		// Indirection through a variable
		{"var_indirection", `P=/etc/pa; cat ${P}sswd`},
	}

	for _, b := range bypasses {
		t.Run(b.name, func(t *testing.T) {
			err := pv.ValidateToolCall("Bash", map[string]interface{}{"command": b.cmd})
			if err == nil {
				t.Fatalf("Bash bypass %q should be blocked, was allowed", b.cmd)
			}
			if !strings.Contains(err.Error(), "SECURITY") {
				t.Errorf("blocked but with non-security error: %v", err)
			}
		})
	}
}
