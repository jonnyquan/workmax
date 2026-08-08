//go:build desktop

package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	cloudproxy "server/desktop/cloud_proxy"
)

type userInfoReadFailureKeychain struct {
	err error
}

func (k userInfoReadFailureKeychain) Write(_, _ string, _ []byte) error { return nil }
func (k userInfoReadFailureKeychain) Read(_, _ string) ([]byte, error)  { return nil, k.err }
func (k userInfoReadFailureKeychain) Delete(_, _ string) error          { return nil }

func bootUserInfoFixture(t *testing.T, userinfoHandler http.HandlerFunc) (string, *cloudproxy.TokenStore) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/desktop/oauth/userinfo", userinfoHandler)
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)

	db := openServerTestDB(t)
	store := cloudproxy.NewTokenStore(newMemKeychain())
	if err := store.Save(cloudproxy.TokenPair{
		AccessToken:      "stub-access",
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "stub-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return "http://" + srv.listener.Addr().String(), store
}

func getUserInfo(t *testing.T, baseURL string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, baseURL+"/auth/userinfo", nil)
	req.Header.Set("X-Local-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestAuthUserInfo_ProxiesCloudSnapshot(t *testing.T) {
	base, _ := bootUserInfoFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer stub-access" {
			t.Fatalf("Authorization: got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"user_id":"u_42",
			"email":"creator@workmax.app",
			"display_name":"Creator",
			"tier":"pro",
			"quota":{"month_used":12,"month_limit":100}
		}`)
	})

	resp := getUserInfo(t, base)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var info cloudproxy.UserInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	if info.Email != "creator@workmax.app" || info.Tier != "pro" {
		t.Fatalf("wrong userinfo: %+v", info)
	}
	if info.Quota.MonthUsed != 12 || info.Quota.MonthLimit != 100 {
		t.Fatalf("wrong quota: %+v", info.Quota)
	}
}

func TestAuthUserInfo_NoSessionIsUnauthorized(t *testing.T) {
	base, store := bootUserInfoFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("cloud userinfo should not be called without a local session")
	})
	if err := store.Clear(); err != nil {
		t.Fatal(err)
	}

	resp := getUserInfo(t, base)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d, want 401", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if got := strings.TrimSpace(string(body)); got != `{"error":"authentication_required"}` {
		t.Fatalf("body = %s, want closed authentication_required error", body)
	}
}

func TestAuthUserInfo_UpstreamBodyNeverCrossesRendererBoundary(t *testing.T) {
	const secret = "userinfo-renderer-private-marker"
	base, _ := bootUserInfoFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":"`+secret+`","detail":"arbitrary upstream detail"}`)
	})

	resp := getUserInfo(t, base)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", resp.StatusCode, body)
	}
	if got := strings.TrimSpace(string(body)); got != `{"error":"userinfo_unavailable"}` {
		t.Fatalf("body = %s, want closed userinfo_unavailable error", body)
	}
	if strings.Contains(string(body), secret) || strings.Contains(string(body), "arbitrary upstream detail") {
		t.Fatalf("Renderer response leaked upstream body: %s", body)
	}
}

func TestAuthUserInfo_KeychainReadErrorNeverCrossesRendererBoundary(t *testing.T) {
	const secret = "keychain-renderer-private-marker"
	upstream := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(upstream.Close)
	db := openServerTestDB(t)
	store := cloudproxy.NewTokenStore(userInfoReadFailureKeychain{err: errors.New(secret)})
	cloud := cloudproxy.NewClient(upstream.URL)
	cloud.HTTPClient = upstream.Client()
	proxy := cloudproxy.NewProxy(cloud, store, db)
	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     "tok",
		DB:             db,
		TokenStore:     store,
		Proxy:          proxy,
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	resp := getUserInfo(t, "http://"+srv.listener.Addr().String())
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", resp.StatusCode, body)
	}
	if got := strings.TrimSpace(string(body)); got != `{"error":"session_unavailable"}` {
		t.Fatalf("body = %s, want closed session_unavailable error", body)
	}
	if strings.Contains(string(body), secret) {
		t.Fatalf("Renderer response leaked Keychain error: %s", body)
	}
}

func TestAuthUserInfo_RefreshesExpiringAccessToken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/desktop/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if got := r.PostFormValue("grant_type"); got != "refresh_token" {
			t.Fatalf("grant_type: got %q", got)
		}
		if got := r.PostFormValue("refresh_token"); got != "old-refresh" {
			t.Fatalf("refresh_token: got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"access_token":"fresh-access",
			"token_type":"Bearer",
			"expires_in":3600,
			"refresh_token":"fresh-refresh",
			"refresh_expires_in":7200,
			"scope":"workagent"
		}`)
	})
	mux.HandleFunc("/api/desktop/oauth/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer fresh-access" {
			t.Fatalf("Authorization: got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"user_id":"u_42",
			"email":"creator@workmax.app",
			"display_name":"Creator",
			"tier":"pro",
			"quota":{"month_used":12,"month_limit":100}
		}`)
	})
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)

	db := openServerTestDB(t)
	store := cloudproxy.NewTokenStore(newMemKeychain())
	if err := store.Save(cloudproxy.TokenPair{
		AccessToken:      "old-access",
		AccessExpiresAt:  time.Now().UTC().Add(5 * time.Second),
		RefreshToken:     "old-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	resp := getUserInfo(t, "http://"+srv.listener.Addr().String())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: %d, body: %s", resp.StatusCode, body)
	}
	saved, err := store.Get()
	if err != nil {
		t.Fatal(err)
	}
	if saved.AccessToken != "fresh-access" || saved.RefreshToken != "fresh-refresh" {
		t.Fatalf("rotated token not saved: %+v", saved)
	}
}

func TestAuthUserInfo_RefreshesAndRetriesAfterCloud401(t *testing.T) {
	var userInfoCalls int
	mux := http.NewServeMux()
	mux.HandleFunc("/api/desktop/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if got := r.PostFormValue("refresh_token"); got != "still-valid-refresh" {
			t.Fatalf("refresh_token: got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"access_token":"fresh-after-401",
			"token_type":"Bearer",
			"expires_in":3600,
			"refresh_token":"rotated-after-401",
			"refresh_expires_in":7200,
			"scope":"workagent"
		}`)
	})
	mux.HandleFunc("/api/desktop/oauth/userinfo", func(w http.ResponseWriter, r *http.Request) {
		userInfoCalls++
		switch userInfoCalls {
		case 1:
			if got := r.Header.Get("Authorization"); got != "Bearer locally-fresh-but-revoked" {
				t.Fatalf("first Authorization: got %q", got)
			}
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"invalid_token"}`)
		case 2:
			if got := r.Header.Get("Authorization"); got != "Bearer fresh-after-401" {
				t.Fatalf("retry Authorization: got %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{
				"user_id":"u_42",
				"email":"creator@workmax.app",
				"display_name":"Creator",
				"tier":"pro",
				"quota":{"month_used":12,"month_limit":100}
			}`)
		default:
			t.Fatalf("unexpected userinfo call #%d", userInfoCalls)
		}
	})
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)

	db := openServerTestDB(t)
	store := cloudproxy.NewTokenStore(newMemKeychain())
	if err := store.Save(cloudproxy.TokenPair{
		AccessToken:      "locally-fresh-but-revoked",
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "still-valid-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	resp := getUserInfo(t, "http://"+srv.listener.Addr().String())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: %d, body: %s", resp.StatusCode, body)
	}
	if userInfoCalls != 2 {
		t.Fatalf("userinfo calls: got %d, want 2", userInfoCalls)
	}
	saved, err := store.Get()
	if err != nil {
		t.Fatal(err)
	}
	if saved.AccessToken != "fresh-after-401" || saved.RefreshToken != "rotated-after-401" {
		t.Fatalf("rotated token not saved: %+v", saved)
	}
}

func TestAuthUserInfo_401RefreshCannotResurrectLogout(t *testing.T) {
	refreshStarted := make(chan struct{})
	releaseRefresh := make(chan struct{})
	var refreshStartedOnce sync.Once
	var releaseRefreshOnce sync.Once
	release := func() { releaseRefreshOnce.Do(func() { close(releaseRefresh) }) }
	var userInfoCalls int

	mux := http.NewServeMux()
	mux.HandleFunc(cloudproxy.CloudRouteOAuthToken, func(w http.ResponseWriter, r *http.Request) {
		refreshStartedOnce.Do(func() { close(refreshStarted) })
		select {
		case <-releaseRefresh:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"access_token":"stale-rotated-access",
			"token_type":"Bearer",
			"expires_in":3600,
			"refresh_token":"stale-rotated-refresh",
			"refresh_expires_in":7200,
			"scope":"workagent"
		}`)
	})
	mux.HandleFunc(cloudproxy.CloudRouteOAuthUserInfo, func(w http.ResponseWriter, r *http.Request) {
		userInfoCalls++
		w.WriteHeader(http.StatusInternalServerError)
	})
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)
	t.Cleanup(release)

	store := cloudproxy.NewTokenStore(newMemKeychain())
	if err := store.Save(cloudproxy.TokenPair{
		AccessToken:      "rejected-access",
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "old-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatalf("seed old session: %v", err)
	}
	stale, err := store.Get()
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	cloud := cloudproxy.NewClient(upstream.URL)
	cloud.HTTPClient = upstream.Client()
	srv := &Server{cfg: ServerConfig{TokenStore: store}}
	ginContext := &gin.Context{Request: httptest.NewRequest(http.MethodGet, "/auth/userinfo", nil)}
	snapshot, err := store.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	leaseContext, releaseLease := snapshot.Lease.BindContext(ginContext.Request.Context())
	defer releaseLease()

	type retryResult struct {
		info cloudproxy.UserInfo
		err  error
	}
	retryDone := make(chan retryResult, 1)
	go func() {
		info, retryErr := srv.refreshAndRetryUserInfo(leaseContext, cloud, stale, snapshot.Lease)
		retryDone <- retryResult{info: info, err: retryErr}
	}()
	select {
	case <-refreshStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for userinfo refresh")
	}
	if err := store.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	release()

	select {
	case got := <-retryDone:
		if !errors.Is(got.err, cloudproxy.ErrSessionChanged) {
			t.Fatalf("refreshAndRetryUserInfo error: got %v, want ErrSessionChanged", got.err)
		}
		if got.info != (cloudproxy.UserInfo{}) {
			t.Fatalf("unexpected userinfo after logout: %+v", got.info)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for userinfo retry")
	}
	if userInfoCalls != 0 {
		t.Fatalf("userinfo retried after logout %d time(s)", userInfoCalls)
	}
	if _, err := store.Get(); !errors.Is(err, cloudproxy.ErrNoSession) {
		t.Fatalf("stale userinfo refresh resurrected logout: %v", err)
	}
}

func TestAuthUserInfo_Cloud401RefreshFailureIsUnauthorized(t *testing.T) {
	var refreshCalls int
	var userInfoCalls int
	mux := http.NewServeMux()
	mux.HandleFunc("/api/desktop/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		refreshCalls++
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":"invalid_grant","error_description":"refresh-renderer-private-marker"}`)
	})
	mux.HandleFunc("/api/desktop/oauth/userinfo", func(w http.ResponseWriter, r *http.Request) {
		userInfoCalls++
		if got := r.Header.Get("Authorization"); got != "Bearer revoked-access" {
			t.Fatalf("Authorization: got %q", got)
		}
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":"invalid_token"}`)
	})
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)

	db := openServerTestDB(t)
	store := cloudproxy.NewTokenStore(newMemKeychain())
	if err := store.Save(cloudproxy.TokenPair{
		AccessToken:      "revoked-access",
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "revoked-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	resp := getUserInfo(t, "http://"+srv.listener.Addr().String())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: %d, want 401, body: %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	if got := strings.TrimSpace(string(body)); got != `{"error":"authentication_required"}` {
		t.Fatalf("body = %s, want closed authentication_required error", body)
	}
	if strings.Contains(string(body), "refresh-renderer-private-marker") || strings.Contains(string(body), "invalid_grant") {
		t.Fatalf("Renderer response leaked refresh diagnostics: %s", body)
	}
	if userInfoCalls != 1 {
		t.Fatalf("userinfo calls: got %d, want 1", userInfoCalls)
	}
	if refreshCalls != 1 {
		t.Fatalf("refresh calls: got %d, want 1", refreshCalls)
	}
}
