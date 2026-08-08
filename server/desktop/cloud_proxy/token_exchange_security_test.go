//go:build desktop

package cloud_proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestExchangeRefreshForTokenForScopeValidatesResponse(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*tokenExchangeResponse)
		wantErr     bool
		wantScope   string
		expectScope string
	}{
		{
			name:        "valid exact scope",
			expectScope: "workagent",
			wantScope:   "workagent",
		},
		{
			name:        "valid exact multi-token scope",
			expectScope: "workagent plugin.read",
			wantScope:   "workagent plugin.read",
			mutate: func(response *tokenExchangeResponse) {
				response.Scope = "workagent plugin.read"
			},
		},
		{
			name:        "token type must be exact Bearer",
			expectScope: "workagent",
			wantErr:     true,
			mutate: func(response *tokenExchangeResponse) {
				response.TokenType = "bearer"
			},
		},
		{
			name:        "access token required",
			expectScope: "workagent",
			wantErr:     true,
			mutate: func(response *tokenExchangeResponse) {
				response.AccessToken = ""
			},
		},
		{
			name:        "access token control rejected",
			expectScope: "workagent",
			wantErr:     true,
			mutate: func(response *tokenExchangeResponse) {
				response.AccessToken = "access\nsecret"
			},
		},
		{
			name:        "access token internal whitespace rejected",
			expectScope: "workagent",
			wantErr:     true,
			mutate: func(response *tokenExchangeResponse) {
				response.AccessToken = "access secret"
			},
		},
		{
			name:        "access token unicode rejected",
			expectScope: "workagent",
			wantErr:     true,
			mutate: func(response *tokenExchangeResponse) {
				response.AccessToken = "access-令牌"
			},
		},
		{
			name:        "refresh token non-url-safe alphabet rejected",
			expectScope: "workagent",
			wantErr:     true,
			mutate: func(response *tokenExchangeResponse) {
				response.RefreshToken = "refresh+token="
			},
		},
		{
			name:        "refresh token required",
			expectScope: "workagent",
			wantErr:     true,
			mutate: func(response *tokenExchangeResponse) {
				response.RefreshToken = ""
			},
		},
		{
			name:        "access lifetime must be positive",
			expectScope: "workagent",
			wantErr:     true,
			mutate: func(response *tokenExchangeResponse) {
				response.ExpiresIn = 0
			},
		},
		{
			name:        "access lifetime is bounded",
			expectScope: "workagent",
			wantErr:     true,
			mutate: func(response *tokenExchangeResponse) {
				response.ExpiresIn = loginTransactionMaxTokenLifetimeSec + 1
			},
		},
		{
			name:        "refresh lifetime must be positive",
			expectScope: "workagent",
			wantErr:     true,
			mutate: func(response *tokenExchangeResponse) {
				response.RefreshExpiresIn = -1
			},
		},
		{
			name:        "refresh lifetime is bounded",
			expectScope: "workagent",
			wantErr:     true,
			mutate: func(response *tokenExchangeResponse) {
				response.RefreshExpiresIn = loginTransactionMaxTokenLifetimeSec + 1
			},
		},
		{
			name:        "scope drift rejected",
			expectScope: "workagent",
			wantErr:     true,
			mutate: func(response *tokenExchangeResponse) {
				response.Scope = "workagent plugin.admin"
			},
		},
		{
			name:        "non-canonical scope rejected",
			expectScope: "workagent",
			wantErr:     true,
			mutate: func(response *tokenExchangeResponse) {
				response.Scope = "workagent  plugin.read"
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			response := tokenExchangeResponse{
				AccessToken:      "next-access-token",
				TokenType:        "Bearer",
				ExpiresIn:        300,
				RefreshToken:     "next-refresh-token",
				RefreshExpiresIn: 3600,
				Scope:            "workagent",
			}
			if tc.mutate != nil {
				tc.mutate(&response)
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(response)
			}))
			t.Cleanup(upstream.Close)

			client := NewClient(upstream.URL)
			client.HTTPClient = upstream.Client()
			pair, err := client.ExchangeRefreshForTokenForScope(
				context.Background(), "old-refresh-token", tc.expectScope,
			)
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "invalid token response") {
					t.Fatalf("error = %v, want invalid token response", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("exchange refresh token: %v", err)
			}
			if pair.Scope != tc.wantScope {
				t.Fatalf("scope = %q, want %q", pair.Scope, tc.wantScope)
			}
		})
	}
}

func TestExchangeRefreshForTokenForScopeRejectsInvalidExpectedScopeBeforeNetwork(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(upstream.Close)

	client := NewClient(upstream.URL)
	client.HTTPClient = upstream.Client()
	_, err := client.ExchangeRefreshForTokenForScope(
		context.Background(), "refresh-token", "workagent  plugin.read",
	)
	if err == nil || !strings.Contains(err.Error(), "expected scope is invalid") {
		t.Fatalf("error = %v, want invalid expected scope", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("network calls = %d, want 0", got)
	}
}

func TestExchangeCodeForTokenForScopeRejectsScopeDrift(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tokenExchangeResponse{
			AccessToken:      "access-token",
			TokenType:        "Bearer",
			ExpiresIn:        300,
			RefreshToken:     "refresh-token",
			RefreshExpiresIn: 3600,
			Scope:            "workagent plugin.admin",
		})
	}))
	t.Cleanup(upstream.Close)

	client := NewClient(upstream.URL)
	client.HTTPClient = upstream.Client()
	_, err := client.ExchangeCodeForTokenForScope(
		context.Background(),
		"authorization-code",
		"http://127.0.0.1:43210/callback",
		"pkce-verifier",
		"device-id",
		"",
		"workagent",
	)
	if err == nil || !strings.Contains(err.Error(), "invalid token response") {
		t.Fatalf("error = %v, want invalid token response", err)
	}
}

func TestTokenExchangeRejectsNonCanonicalJSONResponses(t *testing.T) {
	valid := `{"access_token":"access-token","token_type":"Bearer","expires_in":300,` +
		`"refresh_token":"refresh-token","refresh_expires_in":3600,"scope":"workagent"}`
	tests := []struct {
		name        string
		contentType []string
		body        string
	}{
		{
			name: "missing Content-Type",
			body: valid,
		},
		{
			name:        "wrong Content-Type",
			contentType: []string{"text/plain"},
			body:        valid,
		},
		{
			name:        "duplicate Content-Type",
			contentType: []string{"application/json", "application/json"},
			body:        valid,
		},
		{
			name:        "duplicate known key",
			contentType: []string{"application/json"},
			body: strings.Replace(
				valid,
				`"access_token":"access-token"`,
				`"access_token":"first","access_token":"access-token"`,
				1,
			),
		},
		{
			name:        "case-insensitive key alias",
			contentType: []string{"application/json"},
			body: strings.Replace(
				valid,
				`"access_token":"access-token"`,
				`"Access_Token":"access-token"`,
				1,
			),
		},
		{
			name:        "trailing JSON value",
			contentType: []string{"application/json"},
			body:        valid + `{}`,
		},
		{
			name:        "body exceeds response limit",
			contentType: []string{"application/json"},
			body:        valid + strings.Repeat(" ", loginTransactionMaxResponseBodyBytes-len(valid)+1),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for _, contentType := range tc.contentType {
					w.Header().Add("Content-Type", contentType)
				}
				_, _ = fmt.Fprint(w, tc.body)
			}))
			t.Cleanup(upstream.Close)

			client := NewClient(upstream.URL)
			client.HTTPClient = upstream.Client()
			if _, err := client.ExchangeRefreshForTokenForScope(
				context.Background(), "refresh-token", "workagent",
			); err == nil {
				t.Fatal("token exchange accepted non-canonical response")
			}
		})
	}
}
