//go:build desktop

package cloud_proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCredentialCallsDoNotFollowRedirectsOrUseSharedCookies(t *testing.T) {
	operations := []struct {
		name string
		do   func(*testing.T, context.Context, *Client) error
	}{
		{
			name: "legacy authorization code exchange",
			do: func(_ *testing.T, ctx context.Context, client *Client) error {
				_, err := client.ExchangeCodeForTokenForScope(
					ctx,
					"authorization-code-secret",
					"http://127.0.0.1:43210/callback",
					"pkce-verifier-secret",
					"device-id",
					"",
					"workagent",
				)
				return err
			},
		},
		{
			name: "refresh token exchange",
			do: func(_ *testing.T, ctx context.Context, client *Client) error {
				_, err := client.ExchangeRefreshForTokenForScope(ctx, "refresh-token-secret", "workagent")
				return err
			},
		},
		{
			name: "refresh token revocation",
			do: func(_ *testing.T, ctx context.Context, client *Client) error {
				return client.RevokeRefreshToken(ctx, "refresh-token-secret")
			},
		},
		{
			name: "userinfo bearer request",
			do: func(_ *testing.T, ctx context.Context, client *Client) error {
				_, err := client.UserInfo(ctx, "access-token-secret")
				return err
			},
		},
		{
			name: "skills bearer request",
			do: func(_ *testing.T, ctx context.Context, client *Client) error {
				_, err := client.ListSkills(ctx, "access-token-secret")
				return err
			},
		},
		{
			name: "threads sync bearer request",
			do: func(_ *testing.T, ctx context.Context, client *Client) error {
				_, err := client.ListThreadsDelta(ctx, "access-token-secret", "", 1)
				return err
			},
		},
		{
			name: "messages sync bearer request",
			do: func(_ *testing.T, ctx context.Context, client *Client) error {
				_, err := client.ListMessagesDelta(ctx, "access-token-secret", 1, "", 1)
				return err
			},
		},
		{
			name: "chat SSE bearer request",
			do: func(t *testing.T, ctx context.Context, client *Client) error {
				const chatUID = uint64(42)
				accessToken := proxyTestAccessToken(chatUID, "redirect-isolation")
				store := NewTokenStore(newFakeKeychain())
				if err := store.Save(TokenPair{
					AccessToken:      accessToken,
					AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
					RefreshToken:     "refresh-token-secret",
					RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
					Scope:            "workagent",
				}); err != nil {
					t.Fatalf("seed chat token: %v", err)
				}
				proxy := NewProxy(client, store, openCacheTestDB(t))
				proxy.HTTPClient = client.HTTPClient
				return proxy.Chat(ctx, ChatRequest{
					ThreadID: 1,
					TurnUUID: proxyTestTurnUUID,
					UID:      chatUID,
					UserText: "redirect isolation",
					Body:     []byte(`{}`),
				}, &memSSEWriter{})
			},
		},
	}

	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		for _, operation := range operations {
			t.Run(http.StatusText(status)+"/"+operation.name, func(t *testing.T) {
				var targetCalls atomic.Int64
				target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					targetCalls.Add(1)
					_, _ = io.Copy(io.Discard, r.Body)
					w.WriteHeader(http.StatusNoContent)
				}))
				t.Cleanup(target.Close)

				var sourceCalls atomic.Int64
				var sourceCookies atomic.Int64
				source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					sourceCalls.Add(1)
					if r.Header.Get("Cookie") != "" {
						sourceCookies.Add(1)
					}
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("Location", target.URL+"/capture")
					w.WriteHeader(status)
				}))
				t.Cleanup(source.Close)

				jar, err := cookiejar.New(nil)
				if err != nil {
					t.Fatalf("cookie jar: %v", err)
				}
				sourceURL, err := url.Parse(source.URL)
				if err != nil {
					t.Fatalf("parse source URL: %v", err)
				}
				jar.SetCookies(sourceURL, []*http.Cookie{{Name: "shared-session", Value: "cookie-secret"}})

				var sharedRedirectCalls atomic.Int64
				shared := source.Client()
				shared.Jar = jar
				shared.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
					sharedRedirectCalls.Add(1)
					return nil
				}
				client := NewClient(source.URL)
				client.HTTPClient = shared

				if err := operation.do(t, context.Background(), client); err == nil {
					t.Fatal("credential call accepted redirect response")
				}
				if got := sourceCalls.Load(); got != 1 {
					t.Fatalf("source calls = %d, want 1", got)
				}
				if got := targetCalls.Load(); got != 0 {
					t.Fatalf("redirect target calls = %d, want 0", got)
				}
				if got := sourceCookies.Load(); got != 0 {
					t.Fatalf("credential request carried shared Cookie Jar values %d time(s)", got)
				}
				if got := sharedRedirectCalls.Load(); got != 0 {
					t.Fatalf("credential request used caller redirect callback %d time(s)", got)
				}
			})
		}
	}
}

func TestCredentialCallsRejectRemoteHTTPBeforeTransport(t *testing.T) {
	var transportCalls atomic.Int64
	client := NewClient("http://cloud.example.test")
	client.HTTPClient = &http.Client{
		Transport: credentialRoundTripFunc(func(*http.Request) (*http.Response, error) {
			transportCalls.Add(1)
			return nil, errors.New("transport must not be called")
		}),
	}

	operations := []struct {
		name string
		do   func() error
	}{
		{
			name: "legacy authorization code exchange",
			do: func() error {
				_, err := client.ExchangeCodeForTokenForScope(
					context.Background(),
					"code",
					"http://127.0.0.1:43210/callback",
					"verifier",
					"device",
					"",
					"workagent",
				)
				return err
			},
		},
		{
			name: "refresh token exchange",
			do: func() error {
				_, err := client.ExchangeRefreshForTokenForScope(
					context.Background(), "refresh", "workagent",
				)
				return err
			},
		},
		{
			name: "refresh token revocation",
			do: func() error {
				return client.RevokeRefreshToken(context.Background(), "refresh")
			},
		},
		{
			name: "userinfo bearer request",
			do: func() error {
				_, err := client.UserInfo(context.Background(), "access")
				return err
			},
		},
		{
			name: "skills bearer request",
			do: func() error {
				_, err := client.ListSkills(context.Background(), "access")
				return err
			},
		},
		{
			name: "threads sync bearer request",
			do: func() error {
				_, err := client.ListThreadsDelta(context.Background(), "access", "", 1)
				return err
			},
		},
		{
			name: "messages sync bearer request",
			do: func() error {
				_, err := client.ListMessagesDelta(context.Background(), "access", 1, "", 1)
				return err
			},
		},
		{
			name: "chat SSE bearer request",
			do: func() error {
				proxy := &Proxy{cloud: client, HTTPClient: client.HTTPClient}
				_, err := proxy.buildUpstreamRequest(
					context.Background(), ChatRequest{TurnUUID: proxyTestTurnUUID, Body: []byte(`{}`)}, "access",
				)
				return err
			},
		},
	}

	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			err := operation.do()
			if err == nil || !strings.Contains(err.Error(), "cloud base URL is invalid") {
				t.Fatalf("error = %v, want invalid cloud base URL", err)
			}
		})
	}
	if got := transportCalls.Load(); got != 0 {
		t.Fatalf("transport calls = %d, want 0", got)
	}
}

type credentialRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn credentialRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}
