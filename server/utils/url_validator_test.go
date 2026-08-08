package utils

import (
	"testing"
)

func TestValidateRedirectURL(t *testing.T) {
	allowedHost := "workmax.app"

	tests := []struct {
		name        string
		redirectURL string
		want        string
		wantOK      bool
	}{
		// 安全的相对路径
		{
			name:        "valid relative path",
			redirectURL: "/dashboard",
			want:        "/dashboard",
			wantOK:      true,
		},
		{
			name:        "valid relative path with query",
			redirectURL: "/tools/summarizer?id=123",
			want:        "/tools/summarizer?id=123",
			wantOK:      true,
		},
		{
			name:        "root path",
			redirectURL: "/",
			want:        "/",
			wantOK:      true,
		},

		// 危险的协议相对URL（Open Redirect漏洞）
		{
			name:        "protocol-relative URL (attack)",
			redirectURL: "//evil.com",
			want:        "",
			wantOK:      false,
		},
		{
			name:        "protocol-relative URL with path",
			redirectURL: "//evil.com/phishing",
			want:        "",
			wantOK:      false,
		},

		// 路径遍历攻击
		{
			name:        "path traversal attack",
			redirectURL: "/../../../etc/passwd",
			want:        "",
			wantOK:      false,
		},
		{
			name:        "path traversal in middle",
			redirectURL: "/dashboard/../../admin",
			want:        "",
			wantOK:      false,
		},

		// 危险协议
		{
			name:        "javascript protocol",
			redirectURL: "javascript:alert(1)",
			want:        "",
			wantOK:      false,
		},
		{
			name:        "data protocol",
			redirectURL: "data:text/html,<script>alert(1)</script>",
			want:        "",
			wantOK:      false,
		},

		// 完整URL - 同域名
		{
			name:        "same domain HTTPS",
			redirectURL: "https://workmax.app/dashboard",
			want:        "https://workmax.app/dashboard",
			wantOK:      true,
		},
		{
			name:        "same domain with www",
			redirectURL: "https://www.workmax.app/dashboard",
			want:        "https://www.workmax.app/dashboard",
			wantOK:      true,
		},

		// 完整URL - 不同域名（攻击）
		{
			name:        "different domain",
			redirectURL: "https://evil.com",
			want:        "",
			wantOK:      false,
		},
		{
			name:        "subdomain attack",
			redirectURL: "https://workmax.app.evil.com",
			want:        "",
			wantOK:      false,
		},

		// 边界情况
		{
			name:        "empty string",
			redirectURL: "",
			want:        "",
			wantOK:      false,
		},
		{
			name:        "whitespace only",
			redirectURL: "   ",
			want:        "",
			wantOK:      false,
		},

		// 复杂路径
		{
			name:        "complex valid path",
			redirectURL: "/tools/ai-chat/sessions/abc123",
			want:        "/tools/ai-chat/sessions/abc123",
			wantOK:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotOK := ValidateRedirectURL(tt.redirectURL, allowedHost)
			if got != tt.want || gotOK != tt.wantOK {
				t.Errorf("ValidateRedirectURL() = (%v, %v), want (%v, %v)", got, gotOK, tt.want, tt.wantOK)
			}
		})
	}
}

func TestIsSafeRelativePath(t *testing.T) {
	tests := []struct {
		name    string
		urlPath string
		want    bool
	}{
		{"valid path", "/dashboard", true},
		{"root", "/", true},
		{"deep path", "/tools/ai/chat", true},
		{"protocol-relative", "//evil.com", false},
		{"path traversal", "/../admin", false},
		{"relative without slash", "dashboard", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSafeRelativePath(tt.urlPath); got != tt.want {
				t.Errorf("IsSafeRelativePath() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractDomainFromURL(t *testing.T) {
	tests := []struct {
		name    string
		fullURL string
		want    string
	}{
		{"HTTPS with www", "https://www.workmax.app/path", "workmax.app"},
		{"HTTPS without www", "https://workmax.app/path", "workmax.app"},
		{"HTTP", "http://workmax.app", "workmax.app"},
		{"with port", "https://workmax.app:8080", "workmax.app:8080"},
		{"invalid URL", "not-a-url", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractDomainFromURL(tt.fullURL); got != tt.want {
				t.Errorf("ExtractDomainFromURL() = %v, want %v", got, tt.want)
			}
		})
	}
}
