//go:build desktop

package cloud_proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAcquireAccessToken_RefreshesEmptyAccessToken(t *testing.T) {
	var refreshCalled bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != CloudRouteOAuthToken {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		refreshCalled = true
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if got := r.PostFormValue("grant_type"); got != "refresh_token" {
			t.Fatalf("grant_type: got %q, want refresh_token", got)
		}
		if got := r.PostFormValue("refresh_token"); got != "refresh-still-valid" {
			t.Fatalf("refresh_token: got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"access_token":"fresh-access",
			"token_type":"Bearer",
			"expires_in":3600,
			"refresh_token":"fresh-refresh",
			"refresh_expires_in":86400,
			"scope":"workagent"
		}`))
	}))
	defer upstream.Close()

	store := NewTokenStore(newFakeKeychain())
	if err := store.Save(TokenPair{
		AccessToken:      "",
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "refresh-still-valid",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatal(err)
	}
	cloud := NewClient(upstream.URL)
	cloud.HTTPClient = upstream.Client()

	pair, err := AcquireAccessToken(context.Background(), store, cloud)
	if err != nil {
		t.Fatalf("AcquireAccessToken: %v", err)
	}
	if !refreshCalled {
		t.Fatal("expected refresh call for empty access token")
	}
	if pair.AccessToken != "fresh-access" {
		t.Fatalf("access token: got %q", pair.AccessToken)
	}

	saved, err := store.Get()
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if saved.AccessToken != "fresh-access" || saved.RefreshToken != "fresh-refresh" {
		t.Fatalf("rotated tokens were not persisted: %#v", saved)
	}
}

func newBlockingRefreshClient(t *testing.T) (*Client, <-chan struct{}, func(), *atomic.Int32) {
	t.Helper()
	refreshStarted := make(chan struct{}, 1)
	releaseRefresh := make(chan struct{})
	var refreshCalls atomic.Int32
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseRefresh) }) }

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != CloudRouteOAuthToken {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		refreshCalls.Add(1)
		select {
		case refreshStarted <- struct{}{}:
		default:
		}
		select {
		case <-releaseRefresh:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"access_token":"stale-rotated-access",
			"token_type":"Bearer",
			"expires_in":3600,
			"refresh_token":"stale-rotated-refresh",
			"refresh_expires_in":86400,
			"scope":"workagent"
		}`))
	}))
	t.Cleanup(upstream.Close)
	t.Cleanup(release)
	cloud := NewClient(upstream.URL)
	cloud.HTTPClient = upstream.Client()
	return cloud, refreshStarted, release, &refreshCalls
}

type acquireAccessTokenResult struct {
	pair *TokenPair
	err  error
}

func startAcquireAccessToken(store *TokenStore, cloud *Client) <-chan acquireAccessTokenResult {
	result := make(chan acquireAccessTokenResult, 1)
	go func() {
		pair, err := AcquireAccessToken(context.Background(), store, cloud)
		result <- acquireAccessTokenResult{pair: pair, err: err}
	}()
	return result
}

func waitForRefreshStart(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for refresh request")
	}
}

func waitForAcquireAccessToken(t *testing.T, result <-chan acquireAccessTokenResult) acquireAccessTokenResult {
	t.Helper()
	select {
	case got := <-result:
		return got
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for AcquireAccessToken")
		return acquireAccessTokenResult{}
	}
}

func TestAcquireAccessToken_RefreshCannotOverwriteNewLogin(t *testing.T) {
	store := NewTokenStore(newFakeKeychain())
	if err := store.Save(TokenPair{
		AccessToken:      "expired-old-access",
		AccessExpiresAt:  time.Now().UTC().Add(-time.Minute),
		RefreshToken:     "old-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatalf("seed old session: %v", err)
	}
	cloud, refreshStarted, releaseRefresh, _ := newBlockingRefreshClient(t)
	result := startAcquireAccessToken(store, cloud)
	waitForRefreshStart(t, refreshStarted)

	// This models the authorization-code/OAuth commit that lands while the old
	// refresh HTTP request is still in flight. Unconditional Save is the winner.
	newLogin := TokenPair{
		AccessToken:      "new-login-access",
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "new-login-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}
	if err := store.Save(newLogin); err != nil {
		t.Fatalf("new login Save: %v", err)
	}
	releaseRefresh()

	got := waitForAcquireAccessToken(t, result)
	if got.err != nil {
		t.Fatalf("AcquireAccessToken: %v", got.err)
	}
	if got.pair == nil || got.pair.AccessToken != newLogin.AccessToken || got.pair.RefreshToken != newLogin.RefreshToken {
		t.Fatalf("AcquireAccessToken returned stale refresh instead of new login: %+v", got.pair)
	}
	saved, err := store.Get()
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if saved.AccessToken != newLogin.AccessToken || saved.RefreshToken != newLogin.RefreshToken {
		t.Fatalf("stale refresh overwrote new login: %+v", saved)
	}
}

func TestAcquireAccessToken_RefreshCannotResurrectClearedSession(t *testing.T) {
	store := NewTokenStore(newFakeKeychain())
	if err := store.Save(TokenPair{
		AccessToken:      "expired-old-access",
		AccessExpiresAt:  time.Now().UTC().Add(-time.Minute),
		RefreshToken:     "old-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatalf("seed old session: %v", err)
	}
	cloud, refreshStarted, releaseRefresh, _ := newBlockingRefreshClient(t)
	result := startAcquireAccessToken(store, cloud)
	waitForRefreshStart(t, refreshStarted)

	if err := store.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	releaseRefresh()

	got := waitForAcquireAccessToken(t, result)
	if !errors.Is(got.err, ErrNoSession) {
		t.Fatalf("AcquireAccessToken error: got %v, want ErrNoSession", got.err)
	}
	if got.pair != nil {
		t.Fatalf("AcquireAccessToken returned a pair after Clear: %+v", got.pair)
	}
	if _, err := store.Get(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("stale refresh resurrected cleared session: %v", err)
	}
}

func TestAcquireAccessToken_SameValueLoadDoesNotDiscardRotation(t *testing.T) {
	store := NewTokenStore(newFakeKeychain())
	if err := store.Save(TokenPair{
		AccessToken:      "expired-old-access",
		AccessExpiresAt:  time.Now().UTC().Add(-time.Minute),
		RefreshToken:     "old-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatalf("seed old session: %v", err)
	}
	before, err := store.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	cloud, refreshStarted, releaseRefresh, refreshCalls := newBlockingRefreshClient(t)
	result := startAcquireAccessToken(store, cloud)
	waitForRefreshStart(t, refreshStarted)

	// Auth-state probes explicitly call Load. Re-reading identical Keychain
	// bytes must not invalidate the refresh snapshot and discard the only
	// successful response from a rotating-token exchange.
	if _, err := store.Load(); err != nil {
		t.Fatalf("same-value Load: %v", err)
	}
	afterLoad, err := store.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot after Load: %v", err)
	}
	if afterLoad.Revision != before.Revision {
		t.Fatalf("same-value Load changed revision: before=%d after=%d", before.Revision, afterLoad.Revision)
	}
	releaseRefresh()

	got := waitForAcquireAccessToken(t, result)
	if got.err != nil {
		t.Fatalf("AcquireAccessToken: %v", got.err)
	}
	if got.pair == nil || got.pair.AccessToken != "stale-rotated-access" {
		t.Fatalf("rotation was discarded after same-value Load: %+v", got.pair)
	}
	if refreshCalls.Load() != 1 {
		t.Fatalf("refresh calls: got %d, want 1", refreshCalls.Load())
	}
	saved, err := store.Get()
	if err != nil || saved.AccessToken != "stale-rotated-access" {
		t.Fatalf("rotated pair not persisted: pair=%+v err=%v", saved, err)
	}
}

func TestAccessTokenRefresh_IsSingleFlightAcrossAcquireAnd401Recovery(t *testing.T) {
	store := NewTokenStore(newFakeKeychain())
	if err := store.Save(TokenPair{
		AccessToken:      "expired-old-access",
		AccessExpiresAt:  time.Now().UTC().Add(-time.Minute),
		RefreshToken:     "old-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatalf("seed old session: %v", err)
	}
	rejected, err := store.Get()
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	cloud, refreshStarted, releaseRefresh, refreshCalls := newBlockingRefreshClient(t)

	firstAcquire := startAcquireAccessToken(store, cloud)
	waitForRefreshStart(t, refreshStarted)
	if store.refreshMu.TryLock() {
		store.refreshMu.Unlock()
		t.Fatal("refresh single-flight gate was not held during token exchange")
	}

	secondEntered := make(chan struct{})
	secondAcquire := make(chan acquireAccessTokenResult, 1)
	go func() {
		close(secondEntered)
		pair, acquireErr := AcquireAccessToken(context.Background(), store, cloud)
		secondAcquire <- acquireAccessTokenResult{pair: pair, err: acquireErr}
	}()
	<-secondEntered
	recoveryEntered := make(chan struct{})
	recoveryDone := make(chan acquireAccessTokenResult, 1)
	go func() {
		close(recoveryEntered)
		pair, recoveryErr := RefreshAccessTokenAfterUnauthorized(context.Background(), store, cloud, rejected)
		recoveryDone <- acquireAccessTokenResult{pair: pair, err: recoveryErr}
	}()
	<-recoveryEntered
	releaseRefresh()

	results := []acquireAccessTokenResult{
		waitForAcquireAccessToken(t, firstAcquire),
		waitForAcquireAccessToken(t, secondAcquire),
		waitForAcquireAccessToken(t, recoveryDone),
	}
	for index, got := range results {
		if got.err != nil {
			t.Fatalf("caller %d: %v", index+1, got.err)
		}
		if got.pair == nil || got.pair.AccessToken != "stale-rotated-access" || got.pair.RefreshToken != "stale-rotated-refresh" {
			t.Fatalf("caller %d got wrong winner: %+v", index+1, got.pair)
		}
	}
	if refreshCalls.Load() != 1 {
		t.Fatalf("same old refresh token exchanged %d times, want exactly 1", refreshCalls.Load())
	}
}

func TestAcquireAccessToken_WaitingForRefreshGateHonorsContext(t *testing.T) {
	store := NewTokenStore(newFakeKeychain())
	if err := store.Save(TokenPair{
		AccessToken:      "expired-old-access",
		AccessExpiresAt:  time.Now().UTC().Add(-time.Minute),
		RefreshToken:     "old-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatalf("seed old session: %v", err)
	}
	cloud, refreshStarted, releaseRefresh, refreshCalls := newBlockingRefreshClient(t)
	first := startAcquireAccessToken(store, cloud)
	waitForRefreshStart(t, refreshStarted)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	startedAt := time.Now()
	pair, err := AcquireAccessToken(ctx, store, cloud)
	if !errors.Is(err, context.DeadlineExceeded) || pair != nil {
		t.Fatalf("queued AcquireAccessToken = pair %+v, err %v; want deadline", pair, err)
	}
	if elapsed := time.Since(startedAt); elapsed > 500*time.Millisecond {
		t.Fatalf("queued acquire ignored its context for %s", elapsed)
	}
	if got := refreshCalls.Load(); got != 1 {
		t.Fatalf("queued canceled caller started refresh: calls=%d", got)
	}

	releaseRefresh()
	if got := waitForAcquireAccessToken(t, first); got.err != nil || got.pair == nil {
		t.Fatalf("first refresh did not complete: %+v", got)
	}
}

func TestAcquireAccessToken_KeychainWriteFailureKeepsVolatileRotatedWinner(t *testing.T) {
	kc := newFakeKeychain()
	store := NewTokenStore(kc)
	if err := store.Save(TokenPair{
		AccessToken:      "expired-old-access",
		AccessExpiresAt:  time.Now().UTC().Add(-time.Minute),
		RefreshToken:     "old-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatalf("seed old session: %v", err)
	}
	kc.writeErr = errors.New("keychain locked")
	cloud, refreshStarted, releaseRefresh, refreshCalls := newBlockingRefreshClient(t)
	result := startAcquireAccessToken(store, cloud)
	waitForRefreshStart(t, refreshStarted)
	releaseRefresh()

	got := waitForAcquireAccessToken(t, result)
	if got.err != nil {
		t.Fatalf("AcquireAccessToken: %v", got.err)
	}
	if got.pair == nil || got.pair.RefreshToken != "stale-rotated-refresh" {
		t.Fatalf("did not return volatile rotated winner: %+v", got.pair)
	}
	snapshot, err := store.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if !snapshot.PersistenceDegraded || snapshot.Pair.RefreshToken != "stale-rotated-refresh" {
		t.Fatalf("rotated winner not authoritative in degraded cache: %+v", snapshot)
	}

	// The next caller must reuse the volatile access token and must never send
	// the server-revoked old refresh token again.
	again, err := AcquireAccessToken(context.Background(), store, cloud)
	if err != nil {
		t.Fatalf("second AcquireAccessToken: %v", err)
	}
	if again.RefreshToken != "stale-rotated-refresh" {
		t.Fatalf("second Acquire returned wrong pair: %+v", again)
	}
	if refreshCalls.Load() != 1 {
		t.Fatalf("old refresh token replayed after persistence failure: calls=%d", refreshCalls.Load())
	}
}

func TestAcquireAccessToken_RefreshErrorReusesConcurrentLoginWinner(t *testing.T) {
	refreshStarted := make(chan struct{})
	releaseRefresh := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseRefresh) }) }
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(refreshStarted)
		select {
		case <-releaseRefresh:
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"temporarily_unavailable"}`))
	}))
	t.Cleanup(upstream.Close)
	t.Cleanup(release)
	cloud := NewClient(upstream.URL)
	cloud.HTTPClient = upstream.Client()

	store := NewTokenStore(newFakeKeychain())
	if err := store.Save(TokenPair{
		AccessToken:      "expired-old-access",
		AccessExpiresAt:  time.Now().UTC().Add(-time.Minute),
		RefreshToken:     "old-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatalf("seed old session: %v", err)
	}
	result := startAcquireAccessToken(store, cloud)
	waitForRefreshStart(t, refreshStarted)
	newLogin := TokenPair{
		AccessToken:      "new-login-winner",
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "new-login-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}
	if err := store.Save(newLogin); err != nil {
		t.Fatalf("new login Save: %v", err)
	}
	release()

	got := waitForAcquireAccessToken(t, result)
	if got.err != nil {
		t.Fatalf("AcquireAccessToken reported stale refresh error: %v", got.err)
	}
	if got.pair == nil || got.pair.AccessToken != newLogin.AccessToken || got.pair.RefreshToken != newLogin.RefreshToken {
		t.Fatalf("AcquireAccessToken did not reuse concurrent login winner: %+v", got.pair)
	}
}

func TestAccessTokenRefresh_FailedExchangeRetiresPersistentToken(t *testing.T) {
	refreshStarted := make(chan struct{})
	releaseRefresh := make(chan struct{})
	var refreshStartedOnce sync.Once
	var releaseOnce sync.Once
	var refreshCalls atomic.Int32
	release := func() { releaseOnce.Do(func() { close(releaseRefresh) }) }
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshCalls.Add(1)
		refreshStartedOnce.Do(func() { close(refreshStarted) })
		select {
		case <-releaseRefresh:
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"ambiguous_refresh_failure"}`))
	}))
	t.Cleanup(upstream.Close)
	t.Cleanup(release)
	cloud := NewClient(upstream.URL)
	cloud.HTTPClient = upstream.Client()

	kc := newFakeKeychain()
	store := NewTokenStore(kc)
	if err := store.Save(TokenPair{
		AccessToken:      "expired-old-access",
		AccessExpiresAt:  time.Now().UTC().Add(-time.Minute),
		RefreshToken:     "old-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatalf("seed old session: %v", err)
	}
	rejected, err := store.Get()
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	first := startAcquireAccessToken(store, cloud)
	waitForRefreshStart(t, refreshStarted)

	queuedEntered := make(chan struct{})
	queued := make(chan acquireAccessTokenResult, 1)
	go func() {
		close(queuedEntered)
		pair, recoveryErr := RefreshAccessTokenAfterUnauthorized(context.Background(), store, cloud, rejected)
		queued <- acquireAccessTokenResult{pair: pair, err: recoveryErr}
	}()
	<-queuedEntered
	release()

	for index, result := range []acquireAccessTokenResult{
		waitForAcquireAccessToken(t, first),
		waitForAcquireAccessToken(t, queued),
	} {
		if !errors.Is(result.err, ErrNoSession) || result.pair != nil {
			t.Fatalf("caller %d did not fail closed: pair=%+v err=%v", index+1, result.pair, result.err)
		}
	}
	if _, err := AcquireAccessToken(context.Background(), store, cloud); !errors.Is(err, ErrNoSession) {
		t.Fatalf("later Acquire ignored refresh logout tombstone: %v", err)
	}
	if _, ok := kc.entries[key(KeychainService, KeychainAccount)]; ok {
		t.Fatal("ambiguous refresh failure left replayable Keychain token")
	}
	restarted := NewTokenStore(kc)
	if _, err := restarted.Get(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("restart reloaded ambiguously consumed refresh token: %v", err)
	}
	if _, err := AcquireAccessToken(context.Background(), restarted, cloud); !errors.Is(err, ErrNoSession) {
		t.Fatalf("restarted Acquire did not stay logged out: %v", err)
	}
	if refreshCalls.Load() != 1 {
		t.Fatalf("ambiguous old refresh token replayed: calls=%d, want 1", refreshCalls.Load())
	}
}

type acquireAccessTokenWithLeaseResult struct {
	pair  *TokenPair
	lease SessionLease
	err   error
}

type accessTokenRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn accessTokenRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestAcquireAccessTokenWithLease_QueuedCallerCannotAdoptSameUIDLogin(t *testing.T) {
	keychain := newFakeKeychain()
	seed := NewTokenStore(keychain)
	if err := seed.Save(TokenPair{
		AccessToken:      "old-expiring-access",
		AccessExpiresAt:  time.Now().UTC().Add(time.Second),
		RefreshToken:     "old-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatalf("seed old session: %v", err)
	}

	// Use a fresh store so the queued goroutine's initial Keychain load gives us
	// an observable point proving it froze the old epoch before replacement.
	store := NewTokenStore(keychain)
	store.refreshMu.Lock()
	gateHeld := true
	defer func() {
		if gateHeld {
			store.refreshMu.Unlock()
		}
	}()
	var tokenCalls atomic.Int32
	cloud := NewClient("http://127.0.0.1")
	cloud.HTTPClient = &http.Client{Transport: accessTokenRoundTripFunc(func(*http.Request) (*http.Response, error) {
		tokenCalls.Add(1)
		return nil, errors.New("unexpected token exchange")
	})}
	result := make(chan acquireAccessTokenWithLeaseResult, 1)
	go func() {
		pair, lease, err := AcquireAccessTokenWithLease(context.Background(), store, cloud)
		result <- acquireAccessTokenWithLeaseResult{pair: pair, lease: lease, err: err}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		store.mu.Lock()
		loadedOldEpoch := store.loaded && store.cached != nil && store.cached.AccessToken == "old-expiring-access"
		store.mu.Unlock()
		if loadedOldEpoch {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("queued strict acquire did not freeze old session")
		}
		time.Sleep(time.Millisecond)
	}

	// The replacement is intentionally also expiring. A broken gate-after-load
	// implementation would refresh it; strict acquisition must instead stop at
	// the epoch boundary without any token endpoint call.
	if err := store.Save(TokenPair{
		AccessToken:      "same-uid-new-expiring-access",
		AccessExpiresAt:  time.Now().UTC().Add(time.Second),
		RefreshToken:     "same-uid-new-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatalf("same-UID replacement Save: %v", err)
	}
	store.refreshMu.Unlock()
	gateHeld = false

	select {
	case got := <-result:
		if got.pair != nil || got.lease.Epoch() != 0 || !errors.Is(got.err, ErrSessionChanged) {
			t.Fatalf("queued strict acquire adopted replacement: pair=%+v epoch=%d err=%v", got.pair, got.lease.Epoch(), got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued strict acquire did not stop after session replacement")
	}
	if got := tokenCalls.Load(); got != 0 {
		t.Fatalf("queued old caller touched new-session refresh token: calls=%d", got)
	}
}

func TestRefreshAccessToken_CrossStoreSameEpochAndCredentialsRejected(t *testing.T) {
	pair := TokenPair{
		AccessToken:      "same-access",
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "same-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
		SavedAt:          time.Now().UTC().Add(-time.Minute),
	}
	storeA := NewTokenStore(newFakeKeychain())
	storeB := NewTokenStore(newFakeKeychain())
	if err := storeA.Save(pair); err != nil {
		t.Fatalf("store A Save: %v", err)
	}
	if err := storeB.Save(pair); err != nil {
		t.Fatalf("store B Save: %v", err)
	}
	var tokenCalls atomic.Int32
	cloud := NewClient("http://127.0.0.1")
	cloud.HTTPClient = &http.Client{Transport: accessTokenRoundTripFunc(func(*http.Request) (*http.Response, error) {
		tokenCalls.Add(1)
		return nil, errors.New("unexpected cross-store token exchange")
	})}
	rejected, leaseA, err := AcquireAccessTokenWithLease(context.Background(), storeA, cloud)
	if err != nil {
		t.Fatalf("store A acquire: %v", err)
	}
	snapshotB, err := storeB.GetSnapshot()
	if err != nil {
		t.Fatalf("store B snapshot: %v", err)
	}
	if leaseA.Epoch() != snapshotB.Lease.Epoch() {
		t.Fatalf("test requires colliding numeric epochs: A=%d B=%d", leaseA.Epoch(), snapshotB.Lease.Epoch())
	}
	if leaseA.SameSession(snapshotB.Lease) {
		t.Fatal("leases from different stores compare as the same session")
	}

	if got, err := RefreshAccessTokenAfterUnauthorizedWithLease(
		context.Background(), storeB, cloud, rejected, leaseA,
	); got != nil || !errors.Is(err, ErrSessionChanged) {
		t.Fatalf("strict cross-store refresh = pair %+v, err %v", got, err)
	}
	if got, err := RefreshAccessTokenAfterUnauthorized(
		context.Background(), storeB, cloud, rejected,
	); got != nil || !errors.Is(err, ErrSessionChanged) {
		t.Fatalf("legacy cross-store refresh = pair %+v, err %v", got, err)
	}
	if got := tokenCalls.Load(); got != 0 {
		t.Fatalf("cross-store request reached token endpoint: calls=%d", got)
	}
}

func TestAcquireAccessTokenWithLease_NewLoginCancelsRefreshWithoutMigration(t *testing.T) {
	store := NewTokenStore(newFakeKeychain())
	if err := store.Save(TokenPair{
		AccessToken:      "expired-old-access",
		AccessExpiresAt:  time.Now().UTC().Add(-time.Minute),
		RefreshToken:     "old-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatalf("seed old session: %v", err)
	}
	oldSnapshot, err := store.GetSnapshot()
	if err != nil {
		t.Fatalf("old snapshot: %v", err)
	}
	cloud, refreshStarted, _, refreshCalls := newBlockingRefreshClient(t)
	result := make(chan acquireAccessTokenWithLeaseResult, 1)
	go func() {
		pair, lease, acquireErr := AcquireAccessTokenWithLease(context.Background(), store, cloud)
		result <- acquireAccessTokenWithLeaseResult{pair: pair, lease: lease, err: acquireErr}
	}()
	waitForRefreshStart(t, refreshStarted)

	newLogin := TokenPair{
		AccessToken:      "new-login-access",
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "new-login-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}
	if err := store.Save(newLogin); err != nil {
		t.Fatalf("new login Save: %v", err)
	}
	select {
	case got := <-result:
		if got.pair != nil || got.lease.Epoch() != 0 || !errors.Is(got.err, ErrSessionChanged) {
			t.Fatalf("strict acquire migrated session: pair=%+v epoch=%d err=%v", got.pair, got.lease.Epoch(), got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("new login did not cancel in-flight refresh")
	}
	current, err := store.GetSnapshot()
	if err != nil {
		t.Fatalf("current snapshot: %v", err)
	}
	if current.Pair.AccessToken != newLogin.AccessToken || current.Lease.Epoch() == oldSnapshot.Lease.Epoch() {
		t.Fatalf("new login was not authoritative: %+v", current)
	}
	if got := refreshCalls.Load(); got != 1 {
		t.Fatalf("refresh calls=%d, want 1", got)
	}
}

func TestRefreshAccessTokenAfterUnauthorizedWithLease_NewLoginCannotBecomeRetryIdentity(t *testing.T) {
	store := NewTokenStore(newFakeKeychain())
	if err := store.Save(TokenPair{
		AccessToken:      "rejected-old-access",
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "old-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
		SavedAt:          time.Now().UTC().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("seed old session: %v", err)
	}
	rejected, lease, err := AcquireAccessTokenWithLease(context.Background(), store, NewClient("http://127.0.0.1"))
	if err != nil {
		t.Fatalf("AcquireAccessTokenWithLease: %v", err)
	}
	cloud, refreshStarted, _, refreshCalls := newBlockingRefreshClient(t)
	result := make(chan acquireAccessTokenResult, 1)
	go func() {
		pair, refreshErr := RefreshAccessTokenAfterUnauthorizedWithLease(
			context.Background(), store, cloud, rejected, lease,
		)
		result <- acquireAccessTokenResult{pair: pair, err: refreshErr}
	}()
	waitForRefreshStart(t, refreshStarted)

	newLogin := TokenPair{
		AccessToken:      "new-login-access",
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "new-login-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}
	if err := store.Save(newLogin); err != nil {
		t.Fatalf("new login Save: %v", err)
	}
	got := waitForAcquireAccessToken(t, result)
	if got.pair != nil || !errors.Is(got.err, ErrSessionChanged) {
		t.Fatalf("401 recovery migrated to new login: pair=%+v err=%v", got.pair, got.err)
	}
	current, err := store.GetSnapshot()
	if err != nil {
		t.Fatalf("current snapshot: %v", err)
	}
	if current.Pair.AccessToken != newLogin.AccessToken {
		t.Fatalf("stale 401 recovery overwrote new login: %+v", current.Pair)
	}
	if got := refreshCalls.Load(); got != 1 {
		t.Fatalf("refresh calls=%d, want 1", got)
	}
}

func TestAcquireAccessToken_FenceCancelsInflightRefresh(t *testing.T) {
	store := NewTokenStore(newFakeKeychain())
	if err := store.Save(TokenPair{
		AccessToken:      "expiring-access",
		AccessExpiresAt:  time.Now().UTC().Add(time.Second),
		RefreshToken:     "old-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	cloud, refreshStarted, _, _ := newBlockingRefreshClient(t)
	result := startAcquireAccessToken(store, cloud)
	waitForRefreshStart(t, refreshStarted)
	store.FenceCurrentSession()
	got := waitForAcquireAccessToken(t, result)
	if got.pair != nil || !errors.Is(got.err, ErrSessionChanged) {
		t.Fatalf("fenced acquire = pair %+v, err %v", got.pair, got.err)
	}
}
