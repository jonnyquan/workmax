//go:build desktop

package cloud_proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLogoutSessionFencesInflightRefreshAndRevokesRetiredSnapshot(t *testing.T) {
	refreshStarted := make(chan struct{})
	releaseRefresh := make(chan struct{})
	var startOnce sync.Once
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseRefresh) }) }
	defer release()

	var exchangeCalls atomic.Int64
	var revokeCalls atomic.Int64
	revokedToken := make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(CloudRouteOAuthToken, func(w http.ResponseWriter, r *http.Request) {
		exchangeCalls.Add(1)
		startOnce.Do(func() { close(refreshStarted) })
		select {
		case <-releaseRefresh:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"access_token":"rotated-access",
			"token_type":"Bearer",
			"expires_in":3600,
			"refresh_token":"rotated-refresh",
			"refresh_expires_in":7200,
			"scope":"workagent"
		}`)
	})
	mux.HandleFunc(CloudRouteOAuthRevoke, func(w http.ResponseWriter, r *http.Request) {
		revokeCalls.Add(1)
		_ = r.ParseForm()
		revokedToken <- r.PostFormValue("token")
		w.WriteHeader(http.StatusOK)
	})
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)

	keychain := newFakeKeychain()
	store := NewTokenStore(keychain)
	if err := store.Save(TokenPair{
		AccessToken:      "old-access",
		AccessExpiresAt:  time.Now().UTC().Add(time.Second),
		RefreshToken:     "old-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	active, err := store.GetSnapshot()
	if err != nil {
		t.Fatalf("active snapshot: %v", err)
	}
	leaseContext, releaseLease := active.Lease.BindContext(context.Background())
	defer releaseLease()
	cloud := NewClient(upstream.URL)
	cloud.HTTPClient = upstream.Client()

	refreshDone := make(chan struct {
		pair *TokenPair
		err  error
	}, 1)
	go func() {
		pair, err := AcquireAccessToken(context.Background(), store, cloud)
		refreshDone <- struct {
			pair *TokenPair
			err  error
		}{pair: pair, err: err}
	}()
	select {
	case <-refreshStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for refresh")
	}

	logoutEntered := make(chan struct{})
	logoutDone := make(chan struct {
		result LogoutResult
		err    error
	}, 1)
	go func() {
		close(logoutEntered)
		result, err := LogoutSession(context.Background(), store, cloud)
		logoutDone <- struct {
			result LogoutResult
			err    error
		}{result: result, err: err}
	}()
	<-logoutEntered
	select {
	case <-leaseContext.Done():
		if cause := context.Cause(leaseContext); !errors.Is(cause, ErrSessionChanged) {
			t.Fatalf("fenced lease cause = %v, want ErrSessionChanged", cause)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("logout did not cancel the active session lease")
	}

	// Logout fences the epoch before it waits for refreshMu. Assert the Desktop
	// request returns before releaseRefresh; whether a remote HTTP server
	// observes the TCP disconnect immediately is transport-specific.
	select {
	case got := <-refreshDone:
		if !errors.Is(got.err, ErrSessionChanged) || got.pair != nil {
			t.Fatalf("fenced refresh = pair %+v, err %v; want ErrSessionChanged", got.pair, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for fenced refresh")
	}

	select {
	case got := <-logoutDone:
		if got.err != nil {
			t.Fatalf("LogoutSession: %v", got.err)
		}
		if got.result.RevokeStatus != LogoutRevokeOK {
			t.Fatalf("revoke status = %q, want %q", got.result.RevokeStatus, LogoutRevokeOK)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for logout completion")
	}

	select {
	case got := <-revokedToken:
		if got != "old-refresh" {
			t.Fatalf("revoked token = %q, want pre-fence snapshot", got)
		}
	default:
		t.Fatal("revoke endpoint was not called")
	}
	if got := exchangeCalls.Load(); got != 1 {
		t.Fatalf("token exchanges = %d, want 1", got)
	}
	if got := revokeCalls.Load(); got != 1 {
		t.Fatalf("revokes = %d, want 1", got)
	}
	if _, err := store.Get(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("session survived logout: %v", err)
	}
	if _, err := keychain.Read(KeychainService, KeychainAccount); !errors.Is(err, ErrKeychainNoEntry) {
		t.Fatalf("durable rotated winner survived logout: %v", err)
	}
}

func TestLogoutSessionDeleteFailureIsClosedAndStillInvalidatesMemory(t *testing.T) {
	const secretMarker = "private-keychain-secret-marker"
	keychain := newFakeKeychain()
	store := NewTokenStore(keychain)
	if err := store.Save(TokenPair{
		AccessToken:      "access",
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	keychain.deleteErr = errors.New(secretMarker)

	result, err := LogoutSession(context.Background(), store, nil)
	if !errors.Is(err, ErrLogoutLocalCleanup) {
		t.Fatalf("error = %v, want ErrLogoutLocalCleanup", err)
	}
	if strings.Contains(err.Error(), secretMarker) {
		t.Fatalf("logout leaked Keychain error: %v", err)
	}
	if result.RevokeStatus != LogoutRevokeSkipped {
		t.Fatalf("revoke status = %q, want skipped", result.RevokeStatus)
	}
	if _, getErr := store.Get(); !errors.Is(getErr, ErrNoSession) {
		t.Fatalf("failed delete left process-local session usable: %v", getErr)
	}
}

func TestLogoutSessionDeadlineStillClearsAndFencesInflightRefresh(t *testing.T) {
	keychain := newFakeKeychain()
	store := NewTokenStore(keychain)
	if err := store.Save(TokenPair{
		AccessToken:      "old-access",
		AccessExpiresAt:  time.Now().UTC().Add(time.Second),
		RefreshToken:     "old-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	snapshot, err := store.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	// Hold the gate directly to model a non-cooperative refresh owner. Real
	// net/http exchanges honor the epoch context and normally release at once;
	// this case proves logout remains bounded even if an adapter does not.
	store.refreshMu.Lock()
	defer store.refreshMu.Unlock()

	logoutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	startedAt := time.Now()
	result, err := LogoutSession(logoutCtx, store, nil)
	if err != nil {
		t.Fatalf("LogoutSession: %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 500*time.Millisecond {
		t.Fatalf("deadline-bounded logout took %s", elapsed)
	}
	if result.RevokeStatus != LogoutRevokeFailed {
		t.Fatalf("revoke status = %q, want %q", result.RevokeStatus, LogoutRevokeFailed)
	}
	if _, getErr := store.Get(); !errors.Is(getErr, ErrNoSession) {
		t.Fatalf("deadline logout left session usable: %v", getErr)
	}
	if !errors.Is(snapshot.Lease.Check(), ErrSessionChanged) {
		t.Fatal("deadline logout did not immediately retire pre-existing lease")
	}
}
