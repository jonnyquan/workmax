//go:build desktop

package cloud_proxy

import (
	"strings"
	"testing"
)

func TestNormalizeBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr string
	}{
		{
			name: "https origin",
			raw:  "https://workmax.app",
			want: "https://workmax.app",
		},
		{
			name: "http IPv4 loopback with trailing slash",
			raw:  " http://127.0.0.1:3000/ ",
			want: "http://127.0.0.1:3000",
		},
		{
			name: "http IPv6 loopback",
			raw:  "http://[::1]:3000",
			want: "http://[::1]:3000",
		},
		{
			name:    "http localhost is rejected",
			raw:     "http://localhost:3000",
			wantErr: "must use https unless the host is exact IP loopback",
		},
		{
			name:    "remote http is rejected",
			raw:     "http://workmax.app",
			wantErr: "must use https unless the host is exact IP loopback",
		},
		{
			name:    "non-loopback IPv4 is rejected",
			raw:     "http://127.0.0.2:3000",
			wantErr: "must use https unless the host is exact IP loopback",
		},
		{
			name:    "missing scheme",
			raw:     "workmax.app",
			wantErr: "must use http or https",
		},
		{
			name:    "unsupported scheme",
			raw:     "file:///tmp/workmax",
			wantErr: "must use http or https",
		},
		{
			name:    "path is rejected",
			raw:     "https://workmax.app/staging",
			wantErr: "must not include a path",
		},
		{
			name:    "encoded path is rejected",
			raw:     "https://workmax.app/%2e%2e/staging",
			wantErr: "must not include a path",
		},
		{
			name:    "query is rejected",
			raw:     "https://workmax.app?env=staging",
			wantErr: "must not include query or fragment",
		},
		{
			name:    "fragment is rejected",
			raw:     "https://workmax.app#desktop",
			wantErr: "must not include query or fragment",
		},
		{
			name:    "credentials are rejected",
			raw:     "https://user:pass@workmax.app",
			wantErr: "must not include credentials",
		},
		{
			name:    "userinfo host confusion is rejected",
			raw:     "https://workmax.app@evil.example",
			wantErr: "must not include credentials",
		},
		{
			name:    "hostless is rejected",
			raw:     "https:///staging",
			wantErr: "must include a host",
		},
		{
			name:    "empty is rejected",
			raw:     " ",
			wantErr: "is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeBaseURL(tc.raw)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("NormalizeBaseURL() error = nil, want %q", tc.wantErr)
				}
				if got != "" {
					t.Fatalf("NormalizeBaseURL() value = %q, want empty on error", got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("NormalizeBaseURL() error = %q, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeBaseURL() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("NormalizeBaseURL() = %q, want %q", got, tc.want)
			}
		})
	}
}
