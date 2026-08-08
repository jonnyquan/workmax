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
	"sync/atomic"
	"testing"
	"time"

	cloudproxy "server/desktop/cloud_proxy"
)

type logoutDeleteFailureKeychain struct {
	raw       []byte
	deleteErr error
}

func (k *logoutDeleteFailureKeychain) Write(_, _ string, value []byte) error {
	k.raw = append(k.raw[:0], value...)
	return nil
}

func (k *logoutDeleteFailureKeychain) Read(_, _ string) ([]byte, error) {
	if len(k.raw) == 0 {
		return nil, cloudproxy.ErrKeychainNoEntry
	}
	return append([]byte(nil), k.raw...), nil
}

func (k *logoutDeleteFailureKeychain) Delete(_, _ string) error {
	if k.deleteErr != nil {
		return k.deleteErr
	}
	k.raw = nil
	return nil
}

// bootLogoutFixture stands up a Server with TokenStore + Proxy
// pointed at a stub upstream the test controls. Returns the base
// URL + the token store handle (so tests can verify the local
// session was cleared).
func bootLogoutFixture(t *testing.T, revokeHandler http.HandlerFunc) (string, *cloudproxy.TokenStore) {
	t.Helper()
	mux := http.NewServeMux()
	if revokeHandler != nil {
		mux.HandleFunc("/api/desktop/oauth/revoke", revokeHandler)
	}
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)

	db := openServerTestDB(t)
	store := cloudproxy.NewTokenStore(newMemKeychain())
	// Seed a real token so /logout has something to revoke.
	if err := store.Save(cloudproxy.TokenPair{
		AccessToken:      "stub-access",
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "the-refresh-to-revoke",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
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

func postLogout(t *testing.T, baseURL string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/auth/logout", nil)
	req.Header.Set("X-Local-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestLogout_HappyPath_RevokesAndClears(t *testing.T) {
	var revokeCalled atomic.Bool
	var receivedToken atomic.Pointer[string]
	base, store := bootLogoutFixture(t, func(w http.ResponseWriter, r *http.Request) {
		revokeCalled.Store(true)
		_ = r.ParseForm()
		tok := r.PostFormValue("token")
		receivedToken.Store(&tok)
		w.WriteHeader(http.StatusOK)
	})

	resp := postLogout(t, base)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)
	if parsed["ok"] != true {
		t.Errorf("ok=true expected, body: %s", body)
	}
	if parsed["revoke_status"] != "ok" {
		t.Errorf("revoke_status=ok expected, body: %s", body)
	}

	if !revokeCalled.Load() {
		t.Error("/revoke should have been called on the cloud")
	}
	if got := receivedToken.Load(); got == nil || *got != "the-refresh-to-revoke" {
		t.Errorf("cloud received wrong token: %v", got)
	}

	// Local Keychain should be cleared.
	if _, err := store.Get(); err == nil {
		t.Error("TokenStore.Get should error after logout (session cleared)")
	}
}

func TestLogout_RevokeFailureStillClearsLocal(t *testing.T) {
	// Cloud /revoke errors → sidecar must still clear local Keychain
	// so the renderer routes to LoginPage. The user pressed Logout;
	// our job is to make that visibly happen.
	base, store := bootLogoutFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		io.WriteString(w, `{"error":"backend down"}`)
	})

	resp := postLogout(t, base)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d, want 200 (local clear should still succeed)", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"revoke_status":"failed"`) {
		t.Errorf("body should report the closed revoke_status=failed value, got: %s", body)
	}
	if _, err := store.Get(); err == nil {
		t.Error("Keychain should still be cleared even when cloud /revoke fails")
	}
}

func TestLogout_NoSessionToRevoke(t *testing.T) {
	// User clicked Logout without ever being signed in. Should still
	// return 200 (idempotent) and not call /revoke at all.
	var revokeCalled atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/api/desktop/oauth/revoke", func(w http.ResponseWriter, r *http.Request) {
		revokeCalled.Store(true)
		w.WriteHeader(http.StatusOK)
	})
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)

	db := openServerTestDB(t)
	store := cloudproxy.NewTokenStore(newMemKeychain())
	// NO Save call — store is empty.
	cloud := cloudproxy.NewClient(upstream.URL)
	cloud.HTTPClient = upstream.Client()
	proxy := cloudproxy.NewProxy(cloud, store, db)
	srv, _ := NewServer(ServerConfig{
		SidecarVersion: "test", LocalToken: "tok", DB: db,
		TokenStore: store, Proxy: proxy,
	})
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	resp := postLogout(t, "http://"+srv.listener.Addr().String())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: %d, want 200 (idempotent logout)", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"revoke_status":"skipped"`) {
		t.Errorf("expected revoke_status=skipped, got: %s", body)
	}
	if revokeCalled.Load() {
		t.Error("/revoke should NOT be called when there's no session")
	}
}

func TestLogout_NoTokenStoreConfigured(t *testing.T) {
	db := openServerTestDB(t)
	srv, _ := NewServer(ServerConfig{
		SidecarVersion: "test", LocalToken: "tok", DB: db,
		// No TokenStore
	})
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	resp := postLogout(t, "http://"+srv.listener.Addr().String())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status: %d, want 503", resp.StatusCode)
	}
}

func TestLogout_KeychainDeleteFailureStillInvalidatesMemoryAndRedactsError(t *testing.T) {
	db := openServerTestDB(t)
	keychain := &logoutDeleteFailureKeychain{}
	store := cloudproxy.NewTokenStore(keychain)
	if err := store.Save(cloudproxy.TokenPair{
		AccessToken:      "access-before-logout",
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "refresh-before-logout",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	keychain.deleteErr = errors.New("keychain failure containing private-secret-marker")

	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     "tok",
		DB:             db,
		TokenStore:     store,
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		keychain.deleteErr = nil
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	resp := postLogout(t, "http://"+srv.listener.Addr().String())
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"error":"local_session_cleanup_failed"`) ||
		strings.Contains(string(body), "private-secret-marker") {
		t.Fatalf("logout error was not closed/redacted: %s", body)
	}
	if _, err := store.Get(); !errors.Is(err, cloudproxy.ErrNoSession) {
		t.Fatalf("failed persistent logout left in-memory session usable: %v", err)
	}
}
