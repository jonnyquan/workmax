package workagent

import "testing"

func TestValidateBaseURL(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		// Allowlisted providers
		{"anthropic root", "https://api.anthropic.com", false},
		{"anthropic with path", "https://api.anthropic.com/v1", false},
		{"zhipu", "https://open.bigmodel.cn/api/anthropic", false},
		{"volces", "https://ark.cn-beijing.volces.com/api/coding", false},
		{"moonshot", "https://api.moonshot.cn/v1", false},

		// Empty / malformed
		{"empty", "", true},
		{"whitespace", "   ", true},
		{"no scheme", "api.anthropic.com", true},
		{"bad url", "https:// invalid", true},
		{"file scheme", "file:///etc/passwd", true},
		{"ftp scheme", "ftp://api.anthropic.com", true},

		// Off-allowlist hosts — the attack surface this guards
		{"evil tld", "https://evil.example.com", true},
		{"unrelated", "https://api.openai.com", true},
		{"raw ip", "https://1.2.3.4/v1", true},
		{"localhost", "https://localhost:8080", true},

		// Suffix-confusion tries: must NOT match
		{"suffix prefix attack", "https://api.anthropic.com.evil.example.com", true},
		{"contains-not-suffix", "https://anthropic.com-foo.example.com", true},
		{"plain typo", "https://aanthropic.com", true},

		// Scheme downgrade: a public-provider host MUST be https.
		// http://api.anthropic.com would otherwise quietly downgrade
		// every API call if an admin row got populated this way.
		{"http public anthropic", "http://api.anthropic.com", true},
		{"http public moonshot", "http://api.moonshot.cn/v1", true},
		{"http subdomain bigmodel", "http://open.bigmodel.cn/api/anthropic", true},

		// Loopback escape hatch: developers running a local
		// Anthropic-compatible relay on http should still work.
		{"http localhost", "http://localhost:8080", false},
		{"http 127.0.0.1", "http://127.0.0.1:9000/v1", false},
		{"http ipv6 loopback", "http://[::1]:8080", false},
		// But https://localhost remains rejected — not on the
		// public allowlist; pin behaviour so a future "allow
		// https everywhere" tweak doesn't punch a hole.
		{"https localhost off allowlist", "https://localhost:8080", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateBaseURL(tc.input)
			if tc.wantErr && err == nil {
				t.Errorf("ValidateBaseURL(%q) returned nil, want error", tc.input)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("ValidateBaseURL(%q) returned %v, want nil", tc.input, err)
			}
		})
	}
}
