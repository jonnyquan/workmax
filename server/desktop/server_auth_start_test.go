//go:build desktop

package desktop

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cloudproxy "server/desktop/cloud_proxy"
)

func TestAuthStartScopeFromRequest(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		want    string
		wantErr bool
	}{
		{name: "missing defaults", want: "workagent"},
		{name: "workagent explicit", body: "scope=workagent", want: "workagent"},
		{name: "empty", body: "scope=", wantErr: true},
		{name: "duplicate", body: "scope=workagent&scope=admin", wantErr: true},
		{name: "leading space", body: "scope=%20workagent", wantErr: true},
		{name: "control char", body: "scope=workagent%0Aadmin", wantErr: true},
		{name: "unsupported", body: "scope=admin", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/auth/start", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			got, err := authStartScopeFromRequest(req)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got scope %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("scope: got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestValidateAuthStartResult(t *testing.T) {
	valid := cloudproxy.StartResult{
		AuthorizeURL: "https://workmax.app/api/desktop/oauth/authorize?redirect_uri=http%3A%2F%2F127.0.0.1%3A49152%2Foauth%2Fcallback&response_type=code",
		AuthPort:     49152,
	}
	if err := validateAuthStartResult(valid); err != nil {
		t.Fatalf("valid start result rejected: %v", err)
	}

	cases := []struct {
		name  string
		start cloudproxy.StartResult
	}{
		{
			name: "bad auth port",
			start: cloudproxy.StartResult{
				AuthorizeURL: valid.AuthorizeURL,
				AuthPort:     0,
			},
		},
		{
			name: "non http authorize url",
			start: cloudproxy.StartResult{
				AuthorizeURL: "javascript:alert(1)",
				AuthPort:     valid.AuthPort,
			},
		},
		{
			name: "authorize url credentials",
			start: cloudproxy.StartResult{
				AuthorizeURL: "https://user:pass@workmax.app/api/desktop/oauth/authorize?redirect_uri=http%3A%2F%2F127.0.0.1%3A49152%2Foauth%2Fcallback",
				AuthPort:     valid.AuthPort,
			},
		},
		{
			name: "authorize url fragment",
			start: cloudproxy.StartResult{
				AuthorizeURL: valid.AuthorizeURL + "#token",
				AuthPort:     valid.AuthPort,
			},
		},
		{
			name: "wrong authorize path",
			start: cloudproxy.StartResult{
				AuthorizeURL: "https://workmax.app/oauth/authorize?redirect_uri=http%3A%2F%2F127.0.0.1%3A49152%2Foauth%2Fcallback",
				AuthPort:     valid.AuthPort,
			},
		},
		{
			name: "missing redirect uri",
			start: cloudproxy.StartResult{
				AuthorizeURL: "https://workmax.app/api/desktop/oauth/authorize?response_type=code",
				AuthPort:     valid.AuthPort,
			},
		},
		{
			name: "duplicate redirect uri",
			start: cloudproxy.StartResult{
				AuthorizeURL: valid.AuthorizeURL + "&redirect_uri=http%3A%2F%2F127.0.0.1%3A49152%2Foauth%2Fcallback",
				AuthPort:     valid.AuthPort,
			},
		},
		{
			name: "redirect host is not loopback",
			start: cloudproxy.StartResult{
				AuthorizeURL: "https://workmax.app/api/desktop/oauth/authorize?redirect_uri=http%3A%2F%2Flocalhost%3A49152%2Foauth%2Fcallback",
				AuthPort:     valid.AuthPort,
			},
		},
		{
			name: "redirect port mismatch",
			start: cloudproxy.StartResult{
				AuthorizeURL: "https://workmax.app/api/desktop/oauth/authorize?redirect_uri=http%3A%2F%2F127.0.0.1%3A49153%2Foauth%2Fcallback",
				AuthPort:     valid.AuthPort,
			},
		},
		{
			name: "redirect wrong path",
			start: cloudproxy.StartResult{
				AuthorizeURL: "https://workmax.app/api/desktop/oauth/authorize?redirect_uri=http%3A%2F%2F127.0.0.1%3A49152%2Fcallback",
				AuthPort:     valid.AuthPort,
			},
		},
		{
			name: "redirect query",
			start: cloudproxy.StartResult{
				AuthorizeURL: "https://workmax.app/api/desktop/oauth/authorize?redirect_uri=http%3A%2F%2F127.0.0.1%3A49152%2Foauth%2Fcallback%3Fcode%3Dleak",
				AuthPort:     valid.AuthPort,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateAuthStartResult(tc.start); err == nil {
				t.Fatalf("expected invalid start result to be rejected")
			}
		})
	}
}
