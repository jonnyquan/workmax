//go:build desktop

package desktop

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cloudproxy "server/desktop/cloud_proxy"
)

type sessionLeaseHTTPResult struct {
	status int
	body   string
	err    error
}

func bootSessionLeaseHTTPFixture(
	t *testing.T,
	cloudHandlers map[string]http.HandlerFunc,
) (baseURL string, store *cloudproxy.TokenStore) {
	t.Helper()
	mux := http.NewServeMux()
	for route, handler := range cloudHandlers {
		mux.HandleFunc(route, handler)
	}
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)

	db := openServerTestDB(t)
	store = cloudproxy.NewTokenStore(newMemKeychain())
	if err := store.Save(sessionLeaseTokenPair(mintLocalHistoryJWT(42), "session-a-refresh")); err != nil {
		t.Fatalf("seed session A: %v", err)
	}
	cloud := cloudproxy.NewClient(upstream.URL)
	cloud.HTTPClient = upstream.Client()
	proxy := cloudproxy.NewProxy(cloud, store, db)
	proxy.HTTPClient = upstream.Client()
	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     "tok",
		DB:             db,
		TokenStore:     store,
		Proxy:          proxy,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return "http://" + srv.listener.Addr().String(), store
}

func sessionLeaseTokenPair(accessToken, refreshToken string) cloudproxy.TokenPair {
	return cloudproxy.TokenPair{
		AccessToken:      accessToken,
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     refreshToken,
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}
}

func requestSessionLeaseRoute(baseURL, route string) <-chan sessionLeaseHTTPResult {
	done := make(chan sessionLeaseHTTPResult, 1)
	go func() {
		req, err := http.NewRequest(http.MethodGet, baseURL+route, nil)
		if err != nil {
			done <- sessionLeaseHTTPResult{err: err}
			return
		}
		req.Header.Set("X-Local-Token", "tok")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- sessionLeaseHTTPResult{err: err}
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		done <- sessionLeaseHTTPResult{
			status: resp.StatusCode,
			body:   strings.TrimSpace(string(body)),
			err:    err,
		}
	}()
	return done
}

func waitSessionLeaseSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func waitSessionLeaseResponse(t *testing.T, result <-chan sessionLeaseHTTPResult) sessionLeaseHTTPResult {
	t.Helper()
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("local Desktop request: %v", got.err)
		}
		return got
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for local Desktop response")
		return sessionLeaseHTTPResult{}
	}
}

func TestSessionLease_UserInfoAndSkillsCancelInflightBearerRequest(t *testing.T) {
	services := []struct {
		name       string
		cloudRoute string
		localRoute string
	}{
		{name: "userinfo", cloudRoute: cloudproxy.CloudRouteOAuthUserInfo, localRoute: "/auth/userinfo"},
		{name: "skills", cloudRoute: cloudproxy.CloudRouteSkillsList, localRoute: "/agent/skills/catalog"},
	}
	transitions := []struct {
		name string
		run  func(*cloudproxy.TokenStore) error
	}{
		{
			name: "same UID login replacement",
			run: func(store *cloudproxy.TokenStore) error {
				return store.Save(sessionLeaseTokenPair(mintLocalHistoryJWT(42), "session-b-refresh"))
			},
		},
		{name: "logout", run: func(store *cloudproxy.TokenStore) error { return store.Clear() }},
	}

	for _, service := range services {
		service := service
		for _, transition := range transitions {
			transition := transition
			t.Run(service.name+"/"+transition.name, func(t *testing.T) {
				started := make(chan struct{})
				upstreamCanceled := make(chan struct{})
				releaseUpstream := make(chan struct{})
				var startedOnce sync.Once
				var canceledOnce sync.Once
				var releaseOnce sync.Once
				baseURL, store := bootSessionLeaseHTTPFixture(t, map[string]http.HandlerFunc{
					service.cloudRoute: func(w http.ResponseWriter, r *http.Request) {
						startedOnce.Do(func() { close(started) })
						select {
						case <-r.Context().Done():
							canceledOnce.Do(func() { close(upstreamCanceled) })
						case <-releaseUpstream:
						}
					},
				})
				t.Cleanup(func() { releaseOnce.Do(func() { close(releaseUpstream) }) })

				result := requestSessionLeaseRoute(baseURL, service.localRoute)
				waitSessionLeaseSignal(t, started, service.name+" cloud request")
				if err := transition.run(store); err != nil {
					t.Fatalf("%s: %v", transition.name, err)
				}

				got := waitSessionLeaseResponse(t, result)
				if got.status != http.StatusConflict || got.body != `{"error":"session_changed"}` {
					t.Fatalf("response = status %d body %s, want closed session_changed conflict", got.status, got.body)
				}
				waitSessionLeaseSignal(t, upstreamCanceled, service.name+" upstream cancellation")
			})
		}
	}
}

func TestSessionLease_401RecoveryDoesNotMigrateToReplacementLogin(t *testing.T) {
	services := []struct {
		name       string
		cloudRoute string
		localRoute string
	}{
		{name: "userinfo", cloudRoute: cloudproxy.CloudRouteOAuthUserInfo, localRoute: "/auth/userinfo"},
		{name: "skills", cloudRoute: cloudproxy.CloudRouteSkillsList, localRoute: "/agent/skills/catalog"},
	}

	for _, service := range services {
		service := service
		t.Run(service.name, func(t *testing.T) {
			refreshStarted := make(chan struct{})
			refreshCanceled := make(chan struct{})
			releaseRefresh := make(chan struct{})
			var refreshStartedOnce sync.Once
			var refreshCanceledOnce sync.Once
			var releaseRefreshOnce sync.Once
			var bearerCalls atomic.Int64
			var refreshCalls atomic.Int64
			baseURL, store := bootSessionLeaseHTTPFixture(t, map[string]http.HandlerFunc{
				service.cloudRoute: func(w http.ResponseWriter, r *http.Request) {
					bearerCalls.Add(1)
					if got := r.Header.Get("Authorization"); got != "Bearer "+mintLocalHistoryJWT(42) {
						t.Errorf("Authorization = %q, want frozen session A", got)
					}
					w.WriteHeader(http.StatusUnauthorized)
				},
				cloudproxy.CloudRouteOAuthToken: func(w http.ResponseWriter, r *http.Request) {
					refreshCalls.Add(1)
					if err := r.ParseForm(); err != nil {
						t.Errorf("parse refresh form: %v", err)
					}
					if got := r.Form.Get("refresh_token"); got != "session-a-refresh" {
						t.Errorf("refresh_token = %q, want frozen session A", got)
					}
					refreshStartedOnce.Do(func() { close(refreshStarted) })
					select {
					case <-r.Context().Done():
						refreshCanceledOnce.Do(func() { close(refreshCanceled) })
					case <-releaseRefresh:
					}
				},
			})
			t.Cleanup(func() { releaseRefreshOnce.Do(func() { close(releaseRefresh) }) })

			result := requestSessionLeaseRoute(baseURL, service.localRoute)
			waitSessionLeaseSignal(t, refreshStarted, service.name+" 401 refresh")
			if err := store.Save(sessionLeaseTokenPair(mintLocalHistoryJWT(42), "session-b-refresh")); err != nil {
				t.Fatalf("replace login: %v", err)
			}

			got := waitSessionLeaseResponse(t, result)
			if got.status != http.StatusConflict || got.body != `{"error":"session_changed"}` {
				t.Fatalf("response = status %d body %s, want closed session_changed conflict", got.status, got.body)
			}
			waitSessionLeaseSignal(t, refreshCanceled, service.name+" refresh cancellation")
			if bearerCalls.Load() != 1 {
				t.Fatalf("Bearer endpoint calls = %d, want no session-B retry", bearerCalls.Load())
			}
			if refreshCalls.Load() != 1 {
				t.Fatalf("refresh calls = %d, want exactly one session-A attempt", refreshCalls.Load())
			}
		})
	}
}

func TestUserInfoAndSkills_SecondUnauthorizedIsAuthenticationRequired(t *testing.T) {
	services := []struct {
		name       string
		cloudRoute string
		localRoute string
	}{
		{name: "userinfo", cloudRoute: cloudproxy.CloudRouteOAuthUserInfo, localRoute: "/auth/userinfo"},
		{name: "skills", cloudRoute: cloudproxy.CloudRouteSkillsList, localRoute: "/agent/skills/catalog"},
	}

	for _, service := range services {
		service := service
		t.Run(service.name, func(t *testing.T) {
			var bearerCalls atomic.Int64
			var refreshCalls atomic.Int64
			baseURL, _ := bootSessionLeaseHTTPFixture(t, map[string]http.HandlerFunc{
				cloudproxy.CloudRouteOAuthToken: func(w http.ResponseWriter, r *http.Request) {
					refreshCalls.Add(1)
					if err := r.ParseForm(); err != nil {
						t.Errorf("parse refresh form: %v", err)
					}
					if got := r.Form.Get("refresh_token"); got != "session-a-refresh" {
						t.Errorf("refresh_token = %q, want session-a-refresh", got)
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{
						"access_token":"still-rejected-access","token_type":"Bearer","expires_in":900,
						"refresh_token":"rotated-refresh","refresh_expires_in":86400,"scope":"workagent"
					}`)
				},
				service.cloudRoute: func(w http.ResponseWriter, r *http.Request) {
					bearerCalls.Add(1)
					w.WriteHeader(http.StatusUnauthorized)
				},
			})

			got := waitSessionLeaseResponse(t, requestSessionLeaseRoute(baseURL, service.localRoute))
			if got.status != http.StatusUnauthorized || got.body != `{"error":"authentication_required"}` {
				t.Fatalf("response = status %d body %s, want closed authentication_required", got.status, got.body)
			}
			if bearerCalls.Load() != 2 || refreshCalls.Load() != 1 {
				t.Fatalf("cloud calls: bearer=%d refresh=%d, want 2/1", bearerCalls.Load(), refreshCalls.Load())
			}
		})
	}
}
