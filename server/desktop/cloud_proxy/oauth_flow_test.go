//go:build desktop

package cloud_proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCloudServer stands in for workmax.app during OAuth flow tests.
// It responds to:
//
//	GET  /api/desktop/oauth/authorize  → renders a stub page; tests
//	                                     usually drive the callback
//	                                     directly instead of "navigating"
//	POST /api/desktop/oauth/token      → returns canned access+refresh
//	                                     OR a tokenExchangeError
//	                                     depending on per-test config
type fakeCloudServer struct {
	srv *httptest.Server

	tokenStatus      int
	tokenResponse    any // arbitrary struct → JSON-encoded
	lastTokenFormURL url.Values
}

func newFakeCloudServer(t *testing.T) *fakeCloudServer {
	t.Helper()
	f := &fakeCloudServer{tokenStatus: http.StatusOK}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/desktop/oauth/authorize", func(w http.ResponseWriter, r *http.Request) {
		// Just acknowledge — tests don't navigate this; they drive
		// the loopback callback directly.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html>fake consent</html>"))
	})

	mux.HandleFunc("/api/desktop/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.lastTokenFormURL = r.Form
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.tokenStatus)
		if f.tokenResponse == nil {
			// Default success payload.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":       "fake-access-token-abc",
				"token_type":         "Bearer",
				"expires_in":         900,
				"refresh_token":      "fake-refresh-token-xyz",
				"refresh_expires_in": 7776000,
				"scope":              "workagent",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(f.tokenResponse)
	})

	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// fireCallback simulates the browser following the OAuth redirect
// back to the loopback server. Synchronous — returns when the
// loopback's success page comes back.
func fireCallback(t *testing.T, redirectURI, code, state string) {
	t.Helper()
	cb := fmt.Sprintf("%s?code=%s&state=%s", redirectURI, code, state)
	resp, err := http.Get(cb)
	if err != nil {
		t.Fatalf("fireCallback: %v", err)
	}
	resp.Body.Close()
}

func TestOAuthFlow_HappyPath(t *testing.T) {
	cloud := newFakeCloudServer(t)
	kc := newFakeKeychain()
	store := NewTokenStore(kc)
	client := NewClient(cloud.srv.URL)

	flow := NewOAuthFlow(client, store, "2825400e4ecb442f7b842f022cd40d4e")

	ctx := context.Background()
	start, err := flow.Start(ctx, "workagent")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Authorize URL should target the fake cloud with all OAuth params.
	if !strings.HasPrefix(start.AuthorizeURL, cloud.srv.URL+"/api/desktop/oauth/authorize?") {
		t.Errorf("AuthorizeURL prefix: got %q", start.AuthorizeURL)
	}
	for _, want := range []string{
		"response_type=code",
		"client_id=workmax-desktop",
		"code_challenge=",
		"code_challenge_method=S256",
		"state=" + start.State,
		"scope=workagent",
	} {
		if !strings.Contains(start.AuthorizeURL, want) {
			t.Errorf("AuthorizeURL missing %q: %s", want, start.AuthorizeURL)
		}
	}
	if strings.Contains(start.AuthorizeURL, "device_id=") {
		t.Errorf("AuthorizeURL must not expose device_id; got %s", start.AuthorizeURL)
	}

	// Drive the callback in a goroutine — Wait() blocks on it.
	go func() {
		// Tiny delay so Wait() is actually parked before we fire.
		time.Sleep(20 * time.Millisecond)
		flow.mu.Lock()
		redirectURI := flow.pending.loopback.RedirectURI()
		flow.mu.Unlock()
		fireCallback(t, redirectURI, "auth-code-abc", start.State)
	}()

	pair, err := flow.Wait(ctx, start)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if pair.AccessToken != "fake-access-token-abc" {
		t.Errorf("AccessToken: got %q", pair.AccessToken)
	}
	if pair.RefreshToken != "fake-refresh-token-xyz" {
		t.Errorf("RefreshToken: got %q", pair.RefreshToken)
	}

	// Backend received correctly-formed token request.
	form := cloud.lastTokenFormURL
	if form.Get("grant_type") != "authorization_code" {
		t.Errorf("grant_type: got %q", form.Get("grant_type"))
	}
	if form.Get("code") != "auth-code-abc" {
		t.Errorf("code: got %q", form.Get("code"))
	}
	if form.Get("device_id") != "2825400e4ecb442f7b842f022cd40d4e" {
		t.Errorf("device_id: got %q", form.Get("device_id"))
	}
	var deviceInfo map[string]string
	if err := json.Unmarshal([]byte(form.Get("device_info")), &deviceInfo); err != nil {
		t.Fatalf("device_info should be JSON object: %q: %v", form.Get("device_info"), err)
	}
	if deviceInfo["os"] == "" {
		t.Errorf("device_info.os missing: %#v", deviceInfo)
	}
	if deviceInfo["app_version"] == "" {
		t.Errorf("device_info.app_version missing: %#v", deviceInfo)
	}
	if form.Get("code_verifier") == "" {
		t.Error("code_verifier missing")
	}

	// Token was persisted.
	stored, err := store.Get()
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if stored.AccessToken != pair.AccessToken {
		t.Error("stored token differs from returned pair")
	}
}

func TestOAuthFlow_StartUsesExactLoopbackRedirectURI(t *testing.T) {
	cloud := newFakeCloudServer(t)
	flow := NewOAuthFlow(NewClient(cloud.srv.URL), NewTokenStore(newFakeKeychain()), "dev-1")

	start, err := flow.Start(context.Background(), "workagent")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer flow.Cancel()

	authorizeURL, err := url.Parse(start.AuthorizeURL)
	if err != nil {
		t.Fatalf("parse authorize URL: %v", err)
	}
	redirectURI := authorizeURL.Query().Get("redirect_uri")
	if redirectURI == "" {
		t.Fatal("authorize URL missing redirect_uri")
	}
	redirect, err := url.Parse(redirectURI)
	if err != nil {
		t.Fatalf("parse redirect_uri: %v", err)
	}
	if redirect.Scheme != "http" {
		t.Errorf("redirect scheme: got %q, want http", redirect.Scheme)
	}
	if redirect.Hostname() != "127.0.0.1" {
		t.Errorf("redirect host: got %q, want 127.0.0.1", redirect.Hostname())
	}
	if redirect.Port() != fmt.Sprint(start.AuthPort) {
		t.Errorf("redirect port: got %q, want %d", redirect.Port(), start.AuthPort)
	}
	if redirect.Path != "/oauth/callback" {
		t.Errorf("redirect path: got %q, want /oauth/callback", redirect.Path)
	}
	if redirect.RawQuery != "" || redirect.Fragment != "" {
		t.Errorf("redirect should not include query/fragment: %q #%q", redirect.RawQuery, redirect.Fragment)
	}
}

func TestOAuthFlow_StateMismatch_Rejected(t *testing.T) {
	cloud := newFakeCloudServer(t)
	kc := newFakeKeychain()
	store := NewTokenStore(kc)
	flow := NewOAuthFlow(NewClient(cloud.srv.URL), store, "dev-1")

	start, err := flow.Start(context.Background(), "workagent")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	go func() {
		time.Sleep(20 * time.Millisecond)
		flow.mu.Lock()
		redirectURI := flow.pending.loopback.RedirectURI()
		flow.mu.Unlock()
		fireCallback(t, redirectURI, "code-abc", "wrong-state") // ← mismatched
	}()

	_, err = flow.Wait(context.Background(), start)
	if err == nil {
		t.Fatal("expected state-mismatch error")
	}
	if !strings.Contains(err.Error(), "state mismatch") {
		t.Errorf("error should mention state mismatch: %v", err)
	}

	// Nothing should have been persisted.
	if _, err := store.Get(); !errors.Is(err, ErrNoSession) {
		t.Errorf("store should still be empty, got %v", err)
	}
	_ = start
}

func TestOAuthFlow_CallbackError_Rejected(t *testing.T) {
	cloud := newFakeCloudServer(t)
	kc := newFakeKeychain()
	store := NewTokenStore(kc)
	flow := NewOAuthFlow(NewClient(cloud.srv.URL), store, "dev-1")

	start, err := flow.Start(context.Background(), "workagent")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	go func() {
		time.Sleep(20 * time.Millisecond)
		flow.mu.Lock()
		redirectURI := flow.pending.loopback.RedirectURI()
		flow.mu.Unlock()
		// Vendor error response.
		resp, _ := http.Get(redirectURI + "?error=access_denied&error_description=user+canceled&state=" + url.QueryEscape(start.State))
		if resp != nil {
			resp.Body.Close()
		}
	}()

	_, err = flow.Wait(context.Background(), start)
	if err == nil {
		t.Fatal("expected callback-error wrap")
	}
	if !strings.Contains(err.Error(), "access_denied") {
		t.Errorf("error should mention access_denied: %v", err)
	}
}

func TestOAuthFlow_CallbackErrorRequiresMatchingState(t *testing.T) {
	cloud := newFakeCloudServer(t)
	kc := newFakeKeychain()
	store := NewTokenStore(kc)
	flow := NewOAuthFlow(NewClient(cloud.srv.URL), store, "dev-1")

	start, err := flow.Start(context.Background(), "workagent")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	go func() {
		time.Sleep(20 * time.Millisecond)
		flow.mu.Lock()
		redirectURI := flow.pending.loopback.RedirectURI()
		flow.mu.Unlock()
		resp, _ := http.Get(redirectURI + "?error=access_denied&error_description=user+canceled&state=wrong-state")
		if resp != nil {
			resp.Body.Close()
		}
	}()

	_, err = flow.Wait(context.Background(), start)
	if err == nil {
		t.Fatal("expected state-mismatch error")
	}
	if !strings.Contains(err.Error(), "state mismatch") {
		t.Fatalf("error should mention state mismatch, got %v", err)
	}
	if strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("state mismatch should not surface callback error before CSRF check: %v", err)
	}
	if cloud.lastTokenFormURL != nil {
		t.Fatalf("state mismatch must not call token endpoint, got form %#v", cloud.lastTokenFormURL)
	}
	if _, err := store.Get(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("store should still be empty, got %v", err)
	}
}

func TestOAuthFlow_AmbiguousCallbackDoesNotExchangeToken(t *testing.T) {
	cloud := newFakeCloudServer(t)
	kc := newFakeKeychain()
	store := NewTokenStore(kc)
	flow := NewOAuthFlow(NewClient(cloud.srv.URL), store, "dev-1")

	start, err := flow.Start(context.Background(), "workagent")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	go func() {
		time.Sleep(20 * time.Millisecond)
		flow.mu.Lock()
		redirectURI := flow.pending.loopback.RedirectURI()
		flow.mu.Unlock()
		resp, _ := http.Get(redirectURI + "?code=first&code=second&state=" + url.QueryEscape(start.State))
		if resp != nil {
			resp.Body.Close()
		}
	}()

	_, err = flow.Wait(context.Background(), start)
	if err == nil {
		t.Fatal("expected ambiguous callback error")
	}
	if !strings.Contains(err.Error(), "invalid_request") {
		t.Fatalf("error should mention invalid_request: %v", err)
	}
	if cloud.lastTokenFormURL != nil {
		t.Fatalf("ambiguous callback must not call token endpoint, got form %#v", cloud.lastTokenFormURL)
	}
	if _, err := store.Get(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("store should still be empty, got %v", err)
	}
}

func TestOAuthFlow_BackendTokenError_Surfaced(t *testing.T) {
	cloud := newFakeCloudServer(t)
	cloud.tokenStatus = http.StatusBadRequest
	cloud.tokenResponse = map[string]string{
		"error":             "invalid_grant",
		"error_description": "authorization code is invalid, already used, or expired",
	}

	kc := newFakeKeychain()
	store := NewTokenStore(kc)
	flow := NewOAuthFlow(NewClient(cloud.srv.URL), store, "dev-1")

	start, _ := flow.Start(context.Background(), "workagent")

	go func() {
		time.Sleep(20 * time.Millisecond)
		flow.mu.Lock()
		redirectURI := flow.pending.loopback.RedirectURI()
		flow.mu.Unlock()
		fireCallback(t, redirectURI, "code", start.State)
	}()

	_, err := flow.Wait(context.Background(), start)
	if err == nil {
		t.Fatal("expected error from backend rejection")
	}
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("error should mention invalid_grant: %v", err)
	}
}

func TestOAuthFlow_ConcurrentStart_Rejected(t *testing.T) {
	cloud := newFakeCloudServer(t)
	flow := NewOAuthFlow(NewClient(cloud.srv.URL), NewTokenStore(newFakeKeychain()), "dev")
	if _, err := flow.Start(context.Background(), "workagent"); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	defer flow.Cancel()

	_, err := flow.Start(context.Background(), "workagent")
	if err == nil {
		t.Fatal("second Start should be rejected while first is pending")
	}
	if !strings.Contains(err.Error(), "in progress") {
		t.Errorf("error should mention in-progress flow: %v", err)
	}
}

type blockingEntropyReader struct {
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (r *blockingEntropyReader) Read(p []byte) (int, error) {
	r.once.Do(func() {
		close(r.entered)
		<-r.release
	})
	for index := range p {
		p[index] = byte(index + 1)
	}
	return len(p), nil
}

func TestOAuthFlow_ConcurrentStartCannotBothPassBeforePendingIsInstalled(t *testing.T) {
	flow := NewOAuthFlow(NewClient("http://example"), NewTokenStore(newFakeKeychain()), "dev")
	random := &blockingEntropyReader{entered: make(chan struct{}), release: make(chan struct{})}
	flow.random = random

	firstResult := make(chan error, 1)
	go func() {
		_, err := flow.Start(context.Background(), "workagent")
		firstResult <- err
	}()
	<-random.entered

	if _, err := flow.Start(context.Background(), "workagent"); err == nil || !strings.Contains(err.Error(), "in progress") {
		close(random.release)
		t.Fatalf("concurrent Start error = %v, want in-progress rejection", err)
	}
	close(random.release)
	if err := <-firstResult; err != nil {
		t.Fatalf("first Start: %v", err)
	}
	flow.Cancel()
}

func TestOAuthFlow_CancelWhileStartIsPreparingPreventsLateInstall(t *testing.T) {
	flow := NewOAuthFlow(NewClient("http://example"), NewTokenStore(newFakeKeychain()), "dev")
	random := &blockingEntropyReader{entered: make(chan struct{}), release: make(chan struct{})}
	flow.random = random

	firstResult := make(chan error, 1)
	go func() {
		_, err := flow.Start(context.Background(), "workagent")
		firstResult <- err
	}()
	<-random.entered
	flow.Cancel()
	close(random.release)
	if err := <-firstResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Start error = %v, want context.Canceled", err)
	}

	if _, err := flow.Start(context.Background(), "workagent"); err != nil {
		t.Fatalf("Start after canceled preparation: %v", err)
	}
	flow.Cancel()
}

func TestOAuthFlow_Cancel_FreesPort(t *testing.T) {
	flow := NewOAuthFlow(NewClient("http://example"), NewTokenStore(newFakeKeychain()), "dev")
	_, err := flow.Start(context.Background(), "workagent")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	flow.Cancel()

	// Can start again immediately after cancel.
	if _, err := flow.Start(context.Background(), "workagent"); err != nil {
		t.Errorf("Start after Cancel: %v", err)
	}
	flow.Cancel()
}

func TestOAuthFlow_CancelInterruptsWait(t *testing.T) {
	flow := NewOAuthFlow(NewClient("http://example"), NewTokenStore(newFakeKeychain()), "dev")
	start, err := flow.Start(context.Background(), "workagent")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	go func() {
		time.Sleep(20 * time.Millisecond)
		flow.Cancel()
	}()
	started := time.Now()
	_, err = flow.Wait(context.Background(), start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Cancel took %s to interrupt Wait", elapsed)
	}
}

func TestOAuthFlow_FinishCanceledWaitCannotCancelReplacementFlow(t *testing.T) {
	flow := NewOAuthFlow(NewClient("http://example"), NewTokenStore(newFakeKeychain()), "dev")
	oldContext, oldCancel := context.WithCancel(context.Background())
	old := &pendingFlow{generation: 1, context: oldContext, cancel: oldCancel}
	replacementContext, replacementCancel := context.WithCancel(context.Background())
	replacement := &pendingFlow{generation: 3, context: replacementContext, cancel: replacementCancel}
	defer replacementCancel()

	// Model the exact ordering: Wait copied generation 1, Cancel detached it,
	// and Start installed generation 3 before the old Wait's defer ran.
	oldCancel()
	flow.generation = replacement.generation
	flow.pending = replacement
	flow.finishPending(old)

	if flow.pending != replacement || flow.generation != replacement.generation {
		t.Fatal("old Wait cleanup removed or changed the replacement flow")
	}
	if replacement.context.Err() != nil {
		t.Fatalf("old Wait cleanup canceled replacement context: %v", replacement.context.Err())
	}
}

func TestOAuthFlow_StaleWaitHandleCannotClaimReplacementFlow(t *testing.T) {
	flow := NewOAuthFlow(NewClient("http://example"), NewTokenStore(newFakeKeychain()), "dev")
	first, err := flow.Start(context.Background(), "workagent")
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	flow.Cancel()
	second, err := flow.Start(context.Background(), "workagent")
	if err != nil {
		t.Fatalf("replacement Start: %v", err)
	}
	defer flow.Cancel()

	if _, err := flow.Wait(context.Background(), first); err == nil ||
		!strings.Contains(err.Error(), "no longer current") {
		t.Fatalf("stale Wait error = %v", err)
	}
	flow.mu.Lock()
	replacement := flow.pending
	flow.mu.Unlock()
	if replacement == nil || replacement.generation != second.generation || replacement.waitClaimed {
		t.Fatal("stale Wait claimed or changed the replacement flow")
	}
}

func TestOAuthFlow_StartGenerationAllowsOnlyOneWaiter(t *testing.T) {
	flow := NewOAuthFlow(NewClient("http://example"), NewTokenStore(newFakeKeychain()), "dev")
	start, err := flow.Start(context.Background(), "workagent")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	firstResult := make(chan error, 1)
	go func() {
		_, waitErr := flow.Wait(context.Background(), start)
		firstResult <- waitErr
	}()
	deadline := time.Now().Add(time.Second)
	for {
		flow.mu.Lock()
		claimed := flow.pending != nil && flow.pending.waitClaimed
		flow.mu.Unlock()
		if claimed {
			break
		}
		if time.Now().After(deadline) {
			flow.Cancel()
			t.Fatal("first waiter did not claim the flow")
		}
		time.Sleep(time.Millisecond)
	}

	if _, err := flow.Wait(context.Background(), start); err == nil ||
		!strings.Contains(err.Error(), "already has a waiter") {
		flow.Cancel()
		t.Fatalf("second Wait error = %v", err)
	}
	flow.Cancel()
	select {
	case err := <-firstResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("first Wait error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first Wait did not stop after Cancel")
	}
}

func TestOAuthFlow_SaveFenceLinearizesWithCancel(t *testing.T) {
	keychain := newCoordinatorBlockingWriteKeychain()
	defer closeCoordinatorTestChannel(keychain.release)
	store := NewTokenStore(keychain)
	flowContext, flowCancel := context.WithCancel(context.Background())
	pending := &pendingFlow{
		generation: 1,
		context:    flowContext,
		cancel:     flowCancel,
	}
	flow := NewOAuthFlow(NewClient("https://example.invalid"), store, "dev")
	flow.generation = 1
	flow.pending = pending

	commitResult := make(chan error, 1)
	go func() {
		commitResult <- flow.commitSession(pending, context.Background(), TokenPair{
			AccessToken:  "legacy-commit-access",
			RefreshToken: "legacy-commit-refresh",
			Scope:        "workagent",
		})
	}()
	select {
	case <-keychain.entered:
	case <-time.After(time.Second):
		t.Fatal("legacy OAuth Keychain save did not start")
	}

	cancelDone := make(chan struct{})
	go func() {
		flow.Cancel()
		close(cancelDone)
	}()
	select {
	case <-cancelDone:
		t.Fatal("Cancel crossed an in-progress legacy OAuth save fence")
	case <-time.After(20 * time.Millisecond):
	}
	closeCoordinatorTestChannel(keychain.release)
	if err := <-commitResult; err != nil {
		t.Fatalf("legacy OAuth commit: %v", err)
	}
	select {
	case <-cancelDone:
	case <-time.After(time.Second):
		t.Fatal("Cancel did not finish after legacy OAuth save")
	}
	if keychain.writes.Load() != 1 {
		t.Fatalf("legacy OAuth Keychain writes = %d, want 1", keychain.writes.Load())
	}
	// Logout calls Clear only after Cancel returns, so a Save that won this
	// fence cannot later resurrect the cleared session.
	if err := store.Clear(); err != nil {
		t.Fatalf("Clear after fenced Cancel: %v", err)
	}
	if _, err := store.Get(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("legacy OAuth session survived clear: %v", err)
	}
}

func TestOAuthFlow_CancelFencePreventsLateSave(t *testing.T) {
	keychain := newCoordinatorBlockingWriteKeychain()
	defer closeCoordinatorTestChannel(keychain.release)
	store := NewTokenStore(keychain)
	flowContext, flowCancel := context.WithCancel(context.Background())
	pending := &pendingFlow{
		generation: 1,
		context:    flowContext,
		cancel:     flowCancel,
	}
	flow := NewOAuthFlow(NewClient("https://example.invalid"), store, "dev")
	flow.generation = 1
	flow.pending = pending
	flow.Cancel()

	err := flow.commitSession(pending, context.Background(), TokenPair{
		AccessToken: "must-not-save",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("late legacy OAuth commit error = %v", err)
	}
	if keychain.writes.Load() != 0 {
		t.Fatalf("late legacy OAuth Keychain writes = %d, want 0", keychain.writes.Load())
	}
}

func TestExchangeRefreshForToken_Happy(t *testing.T) {
	cloud := newFakeCloudServer(t)
	client := NewClient(cloud.srv.URL)

	pair, err := client.ExchangeRefreshForToken(context.Background(), "rt-1")
	if err != nil {
		t.Fatalf("ExchangeRefreshForToken: %v", err)
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" {
		t.Error("missing tokens")
	}
	form := cloud.lastTokenFormURL
	if form.Get("grant_type") != "refresh_token" {
		t.Errorf("grant_type: got %q", form.Get("grant_type"))
	}
	if form.Get("refresh_token") != "rt-1" {
		t.Errorf("refresh_token: got %q", form.Get("refresh_token"))
	}
}

func TestExchangeRefreshForToken_RedactsHTTPErrorBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"access_token":"access-secret","refresh_token":"refresh-secret","error":"Authorization: Bearer bearer-secret https://user:pass@example.com/path?client_secret=client-secret"}`))
	}))
	t.Cleanup(upstream.Close)

	client := NewClient(upstream.URL)
	client.HTTPClient = upstream.Client()
	_, err := client.ExchangeRefreshForToken(context.Background(), "rt-1")
	if err == nil {
		t.Fatal("expected token exchange error")
	}
	for _, secret := range []string{"access-secret", "refresh-secret", "bearer-secret", "user:pass", "client-secret"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("token exchange error leaked %q: %v", secret, err)
		}
	}
	if got, want := err.Error(), "token exchange: HTTP 502"; got != want {
		t.Fatalf("error = %q, want body-independent %q", got, want)
	}
}

func TestExchangeRefreshForToken_RedactsOAuthErrorDescription(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Authorization: Bearer bearer-secret refresh_token=refresh-secret"}`))
	}))
	t.Cleanup(upstream.Close)

	client := NewClient(upstream.URL)
	client.HTTPClient = upstream.Client()
	_, err := client.ExchangeRefreshForToken(context.Background(), "rt-1")
	if err == nil {
		t.Fatal("expected token exchange error")
	}
	for _, secret := range []string{"bearer-secret", "refresh-secret"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("OAuth error description leaked %q: %v", secret, err)
		}
	}
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("expected OAuth error code to remain visible: %v", err)
	}
}
