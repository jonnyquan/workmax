//go:build desktop

package cloud_proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUserInfoErrorsNeverIncludeUpstreamBody(t *testing.T) {
	const secret = "userinfo-upstream-private-marker"
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantError  string
	}{
		{
			name:       "non-success body",
			statusCode: http.StatusBadGateway,
			body:       `{"error":"` + secret + `"}`,
			wantError:  "userinfo: HTTP 502",
		},
		{
			name:       "malformed success body",
			statusCode: http.StatusOK,
			body:       `{"email":"` + secret,
			wantError:  "userinfo: invalid response",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.statusCode)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(upstream.Close)
			client := NewClient(upstream.URL)
			client.HTTPClient = upstream.Client()

			_, err := client.UserInfo(context.Background(), "access-token")
			if err == nil {
				t.Fatal("expected userinfo error")
			}
			if got := err.Error(); got != tc.wantError {
				t.Fatalf("error = %q, want %q", got, tc.wantError)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("userinfo error leaked upstream body: %v", err)
			}
		})
	}
}

func TestTokenExchangeErrorUsesOnlyAllowlistedOAuthCode(t *testing.T) {
	const secret = "oauth-upstream-private-marker"
	tests := []struct {
		name      string
		body      string
		wantError string
	}{
		{
			name:      "known code keeps code only",
			body:      `{"error":"invalid_grant","error_description":"` + secret + `"}`,
			wantError: "token exchange: HTTP 400: OAuth error invalid_grant",
		},
		{
			name:      "unknown code is omitted",
			body:      `{"error":"malicious_` + secret + `","error_description":"` + secret + `"}`,
			wantError: "token exchange: HTTP 400",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(upstream.Close)
			client := NewClient(upstream.URL)
			client.HTTPClient = upstream.Client()

			_, err := client.ExchangeRefreshForToken(context.Background(), "refresh-token")
			if err == nil {
				t.Fatal("expected token exchange error")
			}
			if got := err.Error(); got != tc.wantError {
				t.Fatalf("error = %q, want %q", got, tc.wantError)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("token exchange error leaked upstream text: %v", err)
			}
		})
	}
}
