//go:build desktop

package cloud_proxy

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func validLeaseTestPair(access, refresh string) TokenPair {
	return TokenPair{
		AccessToken:      access,
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     refresh,
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}
}

// fakeKeychain is an in-memory Keychain for hermetic tests. Tracks
// (service, account) → bytes; surfaces ErrKeychainNoEntry like the
// real one does. Optional `readErr` to simulate hardware failures.
type fakeKeychain struct {
	mu        sync.Mutex
	entries   map[string][]byte
	readErr   error // if set, Read returns this (overrides ErrKeychainNoEntry)
	writeErr  error // if set, Write fails without replacing the entry
	deleteErr error // if set, Delete fails without removing the entry
}

func newFakeKeychain() *fakeKeychain {
	return &fakeKeychain{entries: make(map[string][]byte)}
}

func key(service, account string) string { return service + "::" + account }

func (k *fakeKeychain) Write(service, account string, value []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.writeErr != nil {
		return k.writeErr
	}
	cp := make([]byte, len(value))
	copy(cp, value)
	k.entries[key(service, account)] = cp
	return nil
}

func (k *fakeKeychain) Read(service, account string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.readErr != nil {
		return nil, k.readErr
	}
	v, ok := k.entries[key(service, account)]
	if !ok {
		return nil, ErrKeychainNoEntry
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, nil
}

func (k *fakeKeychain) Delete(service, account string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.deleteErr != nil {
		return k.deleteErr
	}
	delete(k.entries, key(service, account))
	return nil
}

// === TokenPair predicates ===

func TestTokenPair_IsAccessExpired(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name       string
		token      string
		expiresAt  time.Time
		wantExpire bool
	}{
		{"future", "access", now.Add(10 * time.Minute), false},
		{"past", "access", now.Add(-time.Minute), true},
		{"exactly now", "access", now, false}, // After is strict
		{"zero value", "access", time.Time{}, true},
		{"empty access token", "", now.Add(10 * time.Minute), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := TokenPair{AccessToken: c.token, AccessExpiresAt: c.expiresAt}
			if got := p.IsAccessExpired(now); got != c.wantExpire {
				t.Errorf("got %v, want %v", got, c.wantExpire)
			}
		})
	}
}

func TestTokenPair_NeedsRefresh(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	buffer := 5 * time.Minute

	p := TokenPair{AccessToken: "access", AccessExpiresAt: now.Add(10 * time.Minute)}
	if p.NeedsRefresh(now, buffer) {
		t.Error("token valid for 10min should not need refresh with 5min buffer")
	}

	p = TokenPair{AccessToken: "access", AccessExpiresAt: now.Add(3 * time.Minute)}
	if !p.NeedsRefresh(now, buffer) {
		t.Error("token valid for 3min should need refresh with 5min buffer")
	}

	p = TokenPair{AccessToken: "access", AccessExpiresAt: now.Add(-time.Minute)}
	if !p.NeedsRefresh(now, buffer) {
		t.Error("already-expired token must need refresh")
	}

	p = TokenPair{AccessToken: "access"} // zero expiry
	if !p.NeedsRefresh(now, buffer) {
		t.Error("uninitialized token must need refresh")
	}

	p = TokenPair{AccessToken: "", AccessExpiresAt: now.Add(10 * time.Minute)}
	if !p.NeedsRefresh(now, buffer) {
		t.Error("empty access token must need refresh even with a future expiry")
	}
}

// === Store roundtrip ===

func TestTokenStore_GetReturnsErrNoSession_WhenEmpty(t *testing.T) {
	s := NewTokenStore(newFakeKeychain())
	_, err := s.Get()
	if !errors.Is(err, ErrNoSession) {
		t.Errorf("expected ErrNoSession, got %v", err)
	}
}

func TestTokenStore_SaveAndGet(t *testing.T) {
	s := NewTokenStore(newFakeKeychain())
	want := TokenPair{
		AccessToken:      "access-12345",
		AccessExpiresAt:  time.Date(2026, 5, 18, 13, 0, 0, 0, time.UTC),
		RefreshToken:     "refresh-67890",
		RefreshExpiresAt: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC),
		Scope:            "workagent",
	}
	if err := s.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if want.SavedAt.IsZero() == false {
		t.Error("test setup bug: input SavedAt was non-zero")
	}

	got, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.AccessToken != want.AccessToken {
		t.Errorf("AccessToken: got %q, want %q", got.AccessToken, want.AccessToken)
	}
	if got.RefreshToken != want.RefreshToken {
		t.Errorf("RefreshToken: got %q, want %q", got.RefreshToken, want.RefreshToken)
	}
	if !got.AccessExpiresAt.Equal(want.AccessExpiresAt) {
		t.Errorf("AccessExpiresAt drift: got %v, want %v", got.AccessExpiresAt, want.AccessExpiresAt)
	}
	if got.SavedAt.IsZero() {
		t.Error("SavedAt should be auto-stamped on Save")
	}
}

func TestTokenStore_GetUsesCacheAfterFirstLoad(t *testing.T) {
	kc := newFakeKeychain()
	s := NewTokenStore(kc)

	// Save first.
	pair := TokenPair{AccessToken: "abc", Scope: "workagent"}
	if err := s.Save(pair); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Pull the bytes out by hand: cache hit should return the saved
	// value even if the keychain bytes are mutated underneath us.
	if _, err := s.Get(); err != nil {
		t.Fatalf("first Get: %v", err)
	}
	// Corrupt the keychain bytes (simulate someone editing Keychain
	// Access manually). The cached Get must still return the saved
	// pair from the cache.
	kc.entries[key(KeychainService, KeychainAccount)] = []byte(`{"broken-json`)

	got, err := s.Get()
	if err != nil {
		t.Fatalf("cached Get: %v", err)
	}
	if got.AccessToken != "abc" {
		t.Errorf("cache miss: got %q, want abc", got.AccessToken)
	}
}

func TestTokenStore_Clear_RemovesBothLayers(t *testing.T) {
	kc := newFakeKeychain()
	s := NewTokenStore(kc)

	_ = s.Save(TokenPair{AccessToken: "abc"})
	if _, err := s.Get(); err != nil {
		t.Fatalf("pre-clear Get: %v", err)
	}

	if err := s.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}

	// Cache should now report no session.
	_, err := s.Get()
	if !errors.Is(err, ErrNoSession) {
		t.Errorf("post-clear Get: expected ErrNoSession, got %v", err)
	}
	// Keychain should also be empty.
	if _, ok := kc.entries[key(KeychainService, KeychainAccount)]; ok {
		t.Error("keychain entry still present after Clear")
	}
}

func TestTokenStore_Clear_Idempotent(t *testing.T) {
	s := NewTokenStore(newFakeKeychain())
	// Clear with nothing stored — must not error.
	if err := s.Clear(); err != nil {
		t.Errorf("Clear on empty: %v", err)
	}
}

func TestTokenStore_Load_HandlesCorruptEntry(t *testing.T) {
	kc := newFakeKeychain()
	kc.entries[key(KeychainService, KeychainAccount)] = []byte(`{garbage`)

	s := NewTokenStore(kc)
	_, err := s.Load()
	if err == nil {
		t.Error("corrupt entry should produce an error")
	}
	if errors.Is(err, ErrNoSession) {
		t.Error("corrupt entry should NOT map to ErrNoSession (the user has a session, it's just unreadable)")
	}
}

func TestTokenStore_LoadSameStateDoesNotAdvanceRevision(t *testing.T) {
	t.Run("same pair", func(t *testing.T) {
		store := NewTokenStore(newFakeKeychain())
		if err := store.Save(TokenPair{
			AccessToken:      "same-access",
			AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
			RefreshToken:     "same-refresh",
			RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}
		before, err := store.GetSnapshot()
		if err != nil {
			t.Fatalf("GetSnapshot before Load: %v", err)
		}
		if _, err := store.Load(); err != nil {
			t.Fatalf("Load: %v", err)
		}
		after, err := store.GetSnapshot()
		if err != nil {
			t.Fatalf("GetSnapshot after Load: %v", err)
		}
		if after.Revision != before.Revision {
			t.Fatalf("same-pair Load advanced revision: before=%d after=%d", before.Revision, after.Revision)
		}
	})

	t.Run("same no session", func(t *testing.T) {
		store := NewTokenStore(newFakeKeychain())
		before, err := store.GetSnapshot()
		if !errors.Is(err, ErrNoSession) {
			t.Fatalf("initial GetSnapshot: %v", err)
		}
		if _, err := store.Load(); !errors.Is(err, ErrNoSession) {
			t.Fatalf("Load: %v", err)
		}
		after, err := store.GetSnapshot()
		if !errors.Is(err, ErrNoSession) {
			t.Fatalf("final GetSnapshot: %v", err)
		}
		if after.Revision != before.Revision {
			t.Fatalf("same-empty Load advanced revision: before=%d after=%d", before.Revision, after.Revision)
		}
	})
}

type retainingBufferKeychain struct {
	readRaw    []byte
	writtenRaw []byte
}

func (k *retainingBufferKeychain) Write(_, _ string, value []byte) error {
	k.writtenRaw = value
	return nil
}

func (k *retainingBufferKeychain) Read(_, _ string) ([]byte, error) {
	if k.readRaw == nil {
		return nil, ErrKeychainNoEntry
	}
	return k.readRaw, nil
}

func (k *retainingBufferKeychain) Delete(_, _ string) error { return nil }

func bufferIsZeroed(raw []byte) bool {
	for _, value := range raw {
		if value != 0 {
			return false
		}
	}
	return true
}

func TestTokenStore_ScrubsTemporaryCredentialBuffers(t *testing.T) {
	t.Run("load", func(t *testing.T) {
		kc := &retainingBufferKeychain{readRaw: []byte(`{
			"access_token":"load-access-secret",
			"refresh_token":"load-refresh-secret",
			"scope":"workagent"
		}`)}
		store := NewTokenStore(kc)
		pair, err := store.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if pair.AccessToken != "load-access-secret" || pair.RefreshToken != "load-refresh-secret" {
			t.Fatalf("Load decoded wrong pair: %+v", pair)
		}
		if !bufferIsZeroed(kc.readRaw) {
			t.Fatal("Load retained non-zero Keychain credential bytes")
		}
	})

	t.Run("save", func(t *testing.T) {
		kc := &retainingBufferKeychain{}
		store := NewTokenStore(kc)
		if err := store.Save(TokenPair{AccessToken: "save-access-secret", RefreshToken: "save-refresh-secret"}); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if len(kc.writtenRaw) == 0 || !bufferIsZeroed(kc.writtenRaw) {
			t.Fatal("Save retained non-zero marshaled credential bytes")
		}
	})

	t.Run("conditional save", func(t *testing.T) {
		kc := newFakeKeychain()
		store := NewTokenStore(kc)
		if err := store.Save(TokenPair{AccessToken: "old", RefreshToken: "old-refresh"}); err != nil {
			t.Fatalf("seed Save: %v", err)
		}
		snapshot, err := store.GetSnapshot()
		if err != nil {
			t.Fatalf("GetSnapshot: %v", err)
		}
		observer := &retainingBufferKeychain{readRaw: kc.entries[key(KeychainService, KeychainAccount)]}
		store.keychain = observer
		committed, err := store.SaveIfRevision(
			TokenPair{AccessToken: "conditional-secret", RefreshToken: "conditional-refresh-secret"},
			snapshot.Revision,
		)
		if err != nil || !committed {
			t.Fatalf("SaveIfRevision: committed=%v err=%v", committed, err)
		}
		if len(observer.writtenRaw) == 0 || !bufferIsZeroed(observer.writtenRaw) {
			t.Fatal("SaveIfRevision retained non-zero marshaled credential bytes")
		}
	})
}

func TestTokenStore_SaveIfRevision_WriteFailureCommitsVolatileWinner(t *testing.T) {
	kc := newFakeKeychain()
	store := NewTokenStore(kc)
	oldPair := TokenPair{
		AccessToken:      "old-access",
		AccessExpiresAt:  time.Now().UTC().Add(-time.Minute),
		RefreshToken:     "old-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	}
	if err := store.Save(oldPair); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	snapshot, err := store.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	rotated := TokenPair{
		AccessToken:      "rotated-access",
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "rotated-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	}
	kc.writeErr = errors.New("keychain unavailable")
	committed, err := store.SaveIfRevision(rotated, snapshot.Revision)
	if !committed {
		t.Fatal("rotated pair must commit to volatile cache after Keychain failure")
	}
	if err == nil {
		t.Fatal("SaveIfRevision should still report degraded persistence")
	}

	current, err := store.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot after degraded commit: %v", err)
	}
	if !current.PersistenceDegraded {
		t.Fatal("snapshot did not report degraded persistence")
	}
	if current.Revision == snapshot.Revision {
		t.Fatal("volatile authoritative commit did not advance revision")
	}
	if current.Pair.AccessToken != rotated.AccessToken || current.Pair.RefreshToken != rotated.RefreshToken {
		t.Fatalf("old replayable pair remained authoritative: %+v", current.Pair)
	}

	if _, ok := kc.entries[key(KeychainService, KeychainAccount)]; ok {
		t.Fatal("failed conditional write left replayable persistent residue")
	}
	restarted := NewTokenStore(kc)
	if _, err := restarted.Get(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("restart reloaded credentials after failed conditional write: %v", err)
	}

	// A probe Load in the current process must use the dirty volatile winner
	// rather than consulting the now-empty Keychain.
	kc.readErr = errors.New("Load should not read Keychain while persistence is degraded")
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("degraded Load: %v", err)
	}
	if loaded.AccessToken != rotated.AccessToken || loaded.RefreshToken != rotated.RefreshToken {
		t.Fatalf("degraded Load restored old pair: %+v", loaded)
	}
	afterLoad, err := store.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot after degraded Load: %v", err)
	}
	if afterLoad.Revision != current.Revision {
		t.Fatalf("degraded Load advanced revision: before=%d after=%d", current.Revision, afterLoad.Revision)
	}

	// A later explicit authoritative Save can restore persistence and clear the
	// degraded marker without ever making the old refresh token current again.
	kc.readErr = nil
	kc.writeErr = nil
	if err := store.Save(rotated); err != nil {
		t.Fatalf("re-persist Save: %v", err)
	}
	restored, err := store.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot after re-persist: %v", err)
	}
	if restored.PersistenceDegraded {
		t.Fatal("successful Save did not clear degraded persistence")
	}
}

func TestTokenStore_SaveIfRevision_WriteAndDeleteFailureKeepsVolatileWinner(t *testing.T) {
	kc := newFakeKeychain()
	store := NewTokenStore(kc)
	if err := store.Save(TokenPair{AccessToken: "old", RefreshToken: "old-refresh"}); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	snapshot, err := store.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	kc.writeErr = errors.New("write denied")
	kc.deleteErr = errors.New("delete denied")
	rotated := TokenPair{AccessToken: "rotated", RefreshToken: "rotated-refresh"}
	committed, err := store.SaveIfRevision(rotated, snapshot.Revision)
	if !committed || err == nil {
		t.Fatalf("SaveIfRevision: committed=%v err=%v", committed, err)
	}
	if _, ok := kc.entries[key(KeychainService, KeychainAccount)]; !ok {
		t.Fatal("test setup: double failure unexpectedly removed persistent residue")
	}
	kc.readErr = errors.New("dirty Load must not consult stale persistent residue")
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.AccessToken != rotated.AccessToken || loaded.RefreshToken != rotated.RefreshToken {
		t.Fatalf("double failure downgraded volatile winner: %+v", loaded)
	}
}

func TestTokenStore_SaveFailureRetiresPreviousSession(t *testing.T) {
	kc := newFakeKeychain()
	store := NewTokenStore(kc)
	if err := store.Save(TokenPair{AccessToken: "old", RefreshToken: "old-refresh"}); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	before, err := store.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	kc.writeErr = errors.New("new login write failed")
	if err := store.Save(TokenPair{AccessToken: "new", RefreshToken: "new-refresh"}); err == nil {
		t.Fatal("Save should report Keychain failure")
	}
	after, err := store.GetSnapshot()
	if !errors.Is(err, ErrNoSession) {
		t.Fatalf("failed new login left old session visible: %v", err)
	}
	if after.Revision == before.Revision {
		t.Fatal("failed new login did not invalidate the old session revision")
	}
	if after.PersistenceDegraded {
		t.Fatal("successful residue deletion should leave a persistent no-session state")
	}
	if _, ok := kc.entries[key(KeychainService, KeychainAccount)]; ok {
		t.Fatal("failed new login left previous persistent session behind")
	}
	restarted := NewTokenStore(kc)
	if _, err := restarted.Get(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("restart reloaded old session after failed new login: %v", err)
	}
}

func TestTokenStore_ClearFailureLeavesAuthoritativeLogoutTombstone(t *testing.T) {
	kc := newFakeKeychain()
	store := NewTokenStore(kc)
	if err := store.Save(TokenPair{
		AccessToken:      "old-access",
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "old-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	}); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	before, err := store.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	kc.deleteErr = errors.New("keychain delete denied")
	if err := store.Clear(); err == nil {
		t.Fatal("Clear should report Keychain delete failure")
	}

	after, err := store.GetSnapshot()
	if !errors.Is(err, ErrNoSession) {
		t.Fatalf("GetSnapshot after failed Clear: %v", err)
	}
	if after.Revision == before.Revision {
		t.Fatal("failed persistent Clear did not invalidate pre-logout refresh snapshots")
	}
	if !after.PersistenceDegraded {
		t.Fatal("failed persistent Clear did not retain logout tombstone")
	}
	if _, err := store.Get(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("Get exposed old session after failed Clear: %v", err)
	}
	if _, ok := kc.entries[key(KeychainService, KeychainAccount)]; !ok {
		t.Fatal("test setup: failing fake Delete unexpectedly removed persistent entry")
	}

	// Even a direct Load must honor the volatile tombstone and avoid touching
	// the stale persistent entry.
	kc.readErr = errors.New("Load should not read behind logout tombstone")
	if _, err := store.Load(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("Load resurrected/consulted old session after failed Clear: %v", err)
	}

	// A later retry can remove the persistent residue and clear degradation.
	kc.readErr = nil
	kc.deleteErr = nil
	if err := store.Clear(); err != nil {
		t.Fatalf("retry Clear: %v", err)
	}
	if _, ok := kc.entries[key(KeychainService, KeychainAccount)]; ok {
		t.Fatal("successful retry Clear left persistent entry behind")
	}
	final, err := store.GetSnapshot()
	if !errors.Is(err, ErrNoSession) {
		t.Fatalf("final GetSnapshot: %v", err)
	}
	if final.PersistenceDegraded {
		t.Fatal("successful retry Clear did not clear tombstone degradation")
	}
}

func TestTokenStore_ClearIfRevision_PreservesConcurrentSaveAndPersistsLogout(t *testing.T) {
	kc := newFakeKeychain()
	store := NewTokenStore(kc)
	if err := store.Save(TokenPair{AccessToken: "old", RefreshToken: "old-refresh"}); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	stale, err := store.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	newLogin := TokenPair{AccessToken: "new-login", RefreshToken: "new-login-refresh"}
	if err := store.Save(newLogin); err != nil {
		t.Fatalf("new login Save: %v", err)
	}
	cleared, err := store.ClearIfRevision(stale.Revision)
	if err != nil {
		t.Fatalf("stale ClearIfRevision: %v", err)
	}
	if cleared {
		t.Fatal("stale conditional logout deleted concurrent login")
	}
	current, err := store.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot after stale Clear: %v", err)
	}
	if current.Pair.AccessToken != newLogin.AccessToken || current.Pair.RefreshToken != newLogin.RefreshToken {
		t.Fatalf("concurrent login was not preserved: %+v", current.Pair)
	}

	cleared, err = store.ClearIfRevision(current.Revision)
	if err != nil || !cleared {
		t.Fatalf("current ClearIfRevision: cleared=%v err=%v", cleared, err)
	}
	if _, err := store.Get(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("conditional logout left cache session: %v", err)
	}
	if _, ok := kc.entries[key(KeychainService, KeychainAccount)]; ok {
		t.Fatal("conditional logout left persistent session")
	}
	restarted := NewTokenStore(kc)
	if _, err := restarted.Get(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("restart reloaded conditionally retired session: %v", err)
	}

	failingKC := newFakeKeychain()
	failingStore := NewTokenStore(failingKC)
	if err := failingStore.Save(TokenPair{AccessToken: "residue", RefreshToken: "residue-refresh"}); err != nil {
		t.Fatalf("failing-store seed Save: %v", err)
	}
	failingSnapshot, err := failingStore.GetSnapshot()
	if err != nil {
		t.Fatalf("failing-store GetSnapshot: %v", err)
	}
	failingKC.deleteErr = errors.New("delete denied")
	cleared, err = failingStore.ClearIfRevision(failingSnapshot.Revision)
	if !cleared || err == nil {
		t.Fatalf("failing ClearIfRevision: cleared=%v err=%v", cleared, err)
	}
	failedClearState, err := failingStore.GetSnapshot()
	if !errors.Is(err, ErrNoSession) || !failedClearState.PersistenceDegraded {
		t.Fatalf("failed conditional delete did not install tombstone: snapshot=%+v err=%v", failedClearState, err)
	}
	if _, err := failingStore.Load(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("failed conditional delete allowed Load resurrection: %v", err)
	}
}

type blockingReadKeychain struct {
	*fakeKeychain

	gateMu      sync.Mutex
	blockNext   bool
	readStarted chan struct{}
	releaseRead chan struct{}
}

func newBlockingReadKeychain() *blockingReadKeychain {
	return &blockingReadKeychain{
		fakeKeychain: newFakeKeychain(),
		readStarted:  make(chan struct{}),
		releaseRead:  make(chan struct{}),
	}
}

func (k *blockingReadKeychain) blockNextRead() {
	k.gateMu.Lock()
	k.blockNext = true
	k.gateMu.Unlock()
}

func (k *blockingReadKeychain) Read(service, account string) ([]byte, error) {
	raw, err := k.fakeKeychain.Read(service, account)
	k.gateMu.Lock()
	block := k.blockNext
	k.blockNext = false
	k.gateMu.Unlock()
	if block {
		close(k.readStarted)
		<-k.releaseRead
	}
	return raw, err
}

func TestTokenStore_SaveIfRevision_LoadRaceIsLinearized(t *testing.T) {
	kc := newBlockingReadKeychain()
	store := NewTokenStore(kc)
	oldPair := TokenPair{
		AccessToken:      "old-access",
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "old-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	}
	if err := store.Save(oldPair); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	snapshot, err := store.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}

	externalPair := TokenPair{
		AccessToken:      "external-access",
		AccessExpiresAt:  time.Now().UTC().Add(2 * time.Hour),
		RefreshToken:     "external-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(48 * time.Hour),
	}
	if err := NewTokenStore(kc).Save(externalPair); err != nil {
		t.Fatalf("external Save: %v", err)
	}

	// Load captures changed Keychain bytes, then pauses. Because Load owns the
	// store mutex across the read/cache commit, the conditional Save must wait;
	// after Load advances the revision, that CAS must be rejected.
	kc.blockNextRead()
	loadDone := make(chan error, 1)
	go func() {
		_, loadErr := store.Load()
		loadDone <- loadErr
	}()
	<-kc.readStarted

	type conditionalSaveResult struct {
		committed bool
		err       error
	}
	casStarted := make(chan struct{})
	casDone := make(chan conditionalSaveResult, 1)
	go func() {
		close(casStarted)
		committed, saveErr := store.SaveIfRevision(TokenPair{
			AccessToken:      "stale-refresh-access",
			AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
			RefreshToken:     "stale-refresh-rotated",
			RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		}, snapshot.Revision)
		casDone <- conditionalSaveResult{committed: committed, err: saveErr}
	}()
	<-casStarted
	close(kc.releaseRead)
	if err := <-loadDone; err != nil {
		t.Fatalf("Load: %v", err)
	}
	result := <-casDone
	if result.err != nil {
		t.Fatalf("SaveIfRevision: %v", result.err)
	}
	if result.committed {
		t.Fatal("stale conditional Save committed after Load advanced the revision")
	}

	got, err := store.Get()
	if err != nil {
		t.Fatalf("final Get: %v", err)
	}
	if got.AccessToken != externalPair.AccessToken || got.RefreshToken != externalPair.RefreshToken {
		t.Fatalf("Load/CAS race changed authoritative session: %+v", got)
	}
}

type fakeSessionTombstoneMarker struct {
	mu sync.Mutex

	marked    bool
	readErr   error
	markErr   error
	unmarkErr error

	reads   int
	marks   int
	unmarks int
}

func (m *fakeSessionTombstoneMarker) IsMarked() (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads++
	if m.readErr != nil {
		return false, m.readErr
	}
	return m.marked, nil
}

func (m *fakeSessionTombstoneMarker) Mark() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.marks++
	if m.markErr != nil {
		return m.markErr
	}
	m.marked = true
	return nil
}

func (m *fakeSessionTombstoneMarker) Unmark() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.unmarks++
	if m.unmarkErr != nil {
		return m.unmarkErr
	}
	m.marked = false
	return nil
}

func (m *fakeSessionTombstoneMarker) isMarked() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.marked
}

func TestTokenStore_DurableTombstoneBlocksStaleKeychainAfterClearRestart(t *testing.T) {
	kc := newFakeKeychain()
	marker := &fakeSessionTombstoneMarker{}
	store := NewTokenStoreWithTombstone(kc, marker)
	if err := store.Save(TokenPair{AccessToken: "old-access", RefreshToken: "old-refresh"}); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	if marker.isMarked() {
		t.Fatal("successful Save left the durable tombstone marked")
	}

	snapshot, err := store.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	kc.deleteErr = errors.New("delete denied with user-specific detail")
	cleared, err := store.ClearIfRevision(snapshot.Revision)
	if !cleared || !errors.Is(err, ErrSessionPersistence) {
		t.Fatalf("ClearIfRevision: cleared=%v err=%v", cleared, err)
	}
	if !marker.isMarked() {
		t.Fatal("failed Keychain delete did not persist the logout tombstone first")
	}
	if _, ok := kc.entries[key(KeychainService, KeychainAccount)]; !ok {
		t.Fatal("test setup: failed Keychain delete unexpectedly removed stale credentials")
	}

	// A fresh TokenStore models a new sidecar process. It must consult SQLite's
	// marker before Keychain and remain logged out despite the stale entry.
	kc.readErr = errors.New("keychain must not be read behind a durable tombstone")
	restarted := NewTokenStoreWithTombstone(kc, marker)
	if _, err := restarted.Get(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("restart accepted stale Keychain credentials: %v", err)
	}
}

func TestTokenStore_DurableTombstoneProtectsVolatileRefreshWinner(t *testing.T) {
	kc := newFakeKeychain()
	marker := &fakeSessionTombstoneMarker{}
	store := NewTokenStoreWithTombstone(kc, marker)
	if err := store.Save(TokenPair{AccessToken: "old", RefreshToken: "old-refresh"}); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	snapshot, err := store.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}

	rotated := TokenPair{AccessToken: "rotated", RefreshToken: "rotated-refresh"}
	kc.writeErr = errors.New("write failed with private path")
	kc.deleteErr = errors.New("delete failed with private account")
	committed, err := store.SaveIfRevision(rotated, snapshot.Revision)
	if !committed || !errors.Is(err, ErrSessionPersistence) {
		t.Fatalf("SaveIfRevision: committed=%v err=%v", committed, err)
	}
	if strings.Contains(err.Error(), "private") {
		t.Fatalf("closed persistence error leaked underlying diagnostics: %q", err)
	}
	if !marker.isMarked() {
		t.Fatal("conditional write failure did not leave a durable tombstone")
	}
	current, err := store.GetSnapshot()
	if err != nil {
		t.Fatalf("current GetSnapshot: %v", err)
	}
	if !current.PersistenceDegraded || current.Pair.RefreshToken != rotated.RefreshToken {
		t.Fatalf("current process lost the volatile rotation winner: %+v", current)
	}

	// The old persistent pair still exists because both Keychain operations
	// failed, but a fresh process must refuse it using only the marker.
	restarted := NewTokenStoreWithTombstone(kc, marker)
	if _, err := restarted.Get(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("restart replayed old refresh credentials: %v", err)
	}

	// A later complete persistence attempt writes the winner and only then
	// clears the marker, making it authoritative for another fresh process.
	kc.writeErr = nil
	kc.deleteErr = nil
	if err := store.Save(rotated); err != nil {
		t.Fatalf("retry Save: %v", err)
	}
	if marker.isMarked() {
		t.Fatal("complete retry did not clear the durable tombstone")
	}
	restarted = NewTokenStoreWithTombstone(kc, marker)
	loaded, err := restarted.Get()
	if err != nil {
		t.Fatalf("restart after complete retry: %v", err)
	}
	if loaded.AccessToken != rotated.AccessToken || loaded.RefreshToken != rotated.RefreshToken {
		t.Fatalf("restart loaded wrong persisted winner: %+v", loaded)
	}
}

func TestTokenStore_UnmarkFailureFailsClosedAcrossRestart(t *testing.T) {
	kc := newFakeKeychain()
	marker := &fakeSessionTombstoneMarker{
		unmarkErr: errors.New("sqlite failure containing local-user-detail"),
	}
	// Simulate the strongest double failure after a successful Keychain write:
	// the new item cannot be deleted, but the pre-write marker remains durable.
	kc.deleteErr = errors.New("keychain delete containing local-user-detail")
	store := NewTokenStoreWithTombstone(kc, marker)
	err := store.Save(TokenPair{AccessToken: "new-secret", RefreshToken: "new-refresh-secret"})
	if !errors.Is(err, ErrSessionPersistence) {
		t.Fatalf("Save error=%v, want ErrSessionPersistence", err)
	}
	if strings.Contains(err.Error(), "local-user-detail") || strings.Contains(err.Error(), "new-secret") {
		t.Fatalf("closed persistence error leaked sensitive diagnostics: %q", err)
	}
	if !marker.isMarked() {
		t.Fatal("failed Unmark did not leave/reassert the durable tombstone")
	}
	if _, ok := kc.entries[key(KeychainService, KeychainAccount)]; !ok {
		t.Fatal("test setup: failed delete unexpectedly removed the new Keychain entry")
	}
	if _, err := store.Get(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("current process exposed session after failed Unmark: %v", err)
	}
	restarted := NewTokenStoreWithTombstone(kc, marker)
	if _, err := restarted.Get(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("restart ignored tombstone after failed Unmark: %v", err)
	}
}

func TestTokenStore_MarkerReadFailureNeverFallsThroughToKeychain(t *testing.T) {
	kc := newFakeKeychain()
	if err := NewTokenStore(kc).Save(TokenPair{
		AccessToken:  "must-not-load",
		RefreshToken: "must-not-load-refresh",
	}); err != nil {
		t.Fatalf("legacy seed Save: %v", err)
	}
	marker := &fakeSessionTombstoneMarker{
		readErr: errors.New("sqlite read with private filesystem path"),
	}
	store := NewTokenStoreWithTombstone(kc, marker)
	if _, err := store.Get(); !errors.Is(err, ErrSessionStateUnavailable) {
		t.Fatalf("Get error=%v, want ErrSessionStateUnavailable", err)
	} else if strings.Contains(err.Error(), "private") {
		t.Fatalf("marker error leaked underlying diagnostics: %q", err)
	}
	if marker.reads != 1 {
		t.Fatalf("marker reads=%d, want 1", marker.reads)
	}
	// loaded remains false after an unavailable marker, so recovery is retried
	// rather than silently consulting Keychain on the next request.
	if _, err := store.Get(); !errors.Is(err, ErrSessionStateUnavailable) {
		t.Fatalf("second Get error=%v, want ErrSessionStateUnavailable", err)
	}
	if marker.reads != 2 {
		t.Fatalf("marker retry reads=%d, want 2", marker.reads)
	}
}

func TestTokenStore_MarkerMarkFailureMutationMatrix(t *testing.T) {
	seed := func(t *testing.T) (*fakeKeychain, *fakeSessionTombstoneMarker, *TokenStore, TokenStoreSnapshot) {
		t.Helper()
		kc := newFakeKeychain()
		if err := NewTokenStore(kc).Save(TokenPair{AccessToken: "old", RefreshToken: "old-refresh"}); err != nil {
			t.Fatalf("legacy seed Save: %v", err)
		}
		marker := &fakeSessionTombstoneMarker{
			markErr: errors.New("marker failure with private database path"),
		}
		store := NewTokenStoreWithTombstone(kc, marker)
		snapshot, err := store.GetSnapshot()
		if err != nil {
			t.Fatalf("GetSnapshot: %v", err)
		}
		return kc, marker, store, snapshot
	}
	assertClosed := func(t *testing.T, err error) {
		t.Helper()
		if !errors.Is(err, ErrSessionPersistence) {
			t.Fatalf("error=%v, want ErrSessionPersistence", err)
		}
		if strings.Contains(err.Error(), "private") {
			t.Fatalf("closed error leaked marker diagnostics: %q", err)
		}
	}

	t.Run("new login Save retires old session", func(t *testing.T) {
		kc, _, store, _ := seed(t)
		assertClosed(t, store.Save(TokenPair{AccessToken: "new", RefreshToken: "new-refresh"}))
		if _, err := store.Get(); !errors.Is(err, ErrNoSession) {
			t.Fatalf("current Get: %v", err)
		}
		if len(kc.entries) != 0 {
			t.Fatal("marker failure left the previous Keychain session after successful cleanup")
		}
		if _, err := NewTokenStore(kc).Get(); !errors.Is(err, ErrNoSession) {
			t.Fatalf("restart Get: %v", err)
		}
	})

	t.Run("Clear retires old session", func(t *testing.T) {
		kc, _, store, _ := seed(t)
		assertClosed(t, store.Clear())
		if _, err := store.Get(); !errors.Is(err, ErrNoSession) {
			t.Fatalf("current Get: %v", err)
		}
		if len(kc.entries) != 0 {
			t.Fatal("marker failure left the Keychain session after successful Clear cleanup")
		}
	})

	t.Run("conditional refresh keeps volatile winner", func(t *testing.T) {
		kc, _, store, snapshot := seed(t)
		rotated := TokenPair{AccessToken: "rotated", RefreshToken: "rotated-refresh"}
		committed, err := store.SaveIfRevision(rotated, snapshot.Revision)
		if !committed {
			t.Fatal("SaveIfRevision did not retain the already-rotated volatile winner")
		}
		assertClosed(t, err)
		current, err := store.GetSnapshot()
		if err != nil {
			t.Fatalf("current GetSnapshot: %v", err)
		}
		if !current.PersistenceDegraded || current.Pair.RefreshToken != rotated.RefreshToken {
			t.Fatalf("wrong volatile state: %+v", current)
		}
		if len(kc.entries) != 0 {
			t.Fatal("marker failure left replayable old Keychain bytes after successful cleanup")
		}
		if _, err := NewTokenStore(kc).Get(); !errors.Is(err, ErrNoSession) {
			t.Fatalf("restart Get: %v", err)
		}
	})
}

func TestTokenStoreSessionLease_SaveAlwaysStartsNewEpoch(t *testing.T) {
	store := NewTokenStore(newFakeKeychain())
	pair := validLeaseTestPair("same-access", "same-refresh")
	if err := store.Save(pair); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	first, err := store.GetSnapshot()
	if err != nil {
		t.Fatalf("first snapshot: %v", err)
	}
	bound, cancelBound := first.Lease.BindContext(context.Background())
	defer cancelBound()

	// An unconditional Save models a fresh login, even for byte-identical
	// credentials and therefore must not inherit already-authorized work.
	if err := store.Save(pair); err != nil {
		t.Fatalf("same-value login Save: %v", err)
	}
	second, err := store.GetSnapshot()
	if err != nil {
		t.Fatalf("second snapshot: %v", err)
	}
	if first.Lease.Epoch() == 0 || second.Lease.Epoch() == 0 || first.Lease.Epoch() == second.Lease.Epoch() {
		t.Fatalf("login did not replace epoch: first=%d second=%d", first.Lease.Epoch(), second.Lease.Epoch())
	}
	if err := first.Lease.Check(); !errors.Is(err, ErrSessionChanged) {
		t.Fatalf("old lease Check = %v, want ErrSessionChanged", err)
	}
	select {
	case <-bound.Done():
		if cause := context.Cause(bound); !errors.Is(cause, ErrSessionChanged) {
			t.Fatalf("bound cause = %v, want ErrSessionChanged", cause)
		}
	case <-time.After(time.Second):
		t.Fatal("old lease context was not canceled by login replacement")
	}
	if err := second.Lease.Check(); err != nil {
		t.Fatalf("new lease is not current: %v", err)
	}
}

func TestTokenStoreSessionLease_SaveIfSnapshotPreservesEpoch(t *testing.T) {
	store := NewTokenStore(newFakeKeychain())
	if err := store.Save(validLeaseTestPair("old-access", "old-refresh")); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	before, err := store.GetSnapshot()
	if err != nil {
		t.Fatalf("before snapshot: %v", err)
	}
	bound, cancelBound := before.Lease.BindContext(context.Background())
	defer cancelBound()

	committed, err := store.SaveIfSnapshot(validLeaseTestPair("rotated-access", "rotated-refresh"), before)
	if err != nil || !committed {
		t.Fatalf("SaveIfSnapshot committed=%v err=%v", committed, err)
	}
	after, err := store.GetSnapshot()
	if err != nil {
		t.Fatalf("after snapshot: %v", err)
	}
	if after.Revision == before.Revision {
		t.Fatal("refresh commit did not advance revision")
	}
	if after.Lease.Epoch() != before.Lease.Epoch() {
		t.Fatalf("refresh replaced epoch: before=%d after=%d", before.Lease.Epoch(), after.Lease.Epoch())
	}
	if err := before.Lease.Check(); err != nil {
		t.Fatalf("refresh retired original lease: %v", err)
	}
	select {
	case <-bound.Done():
		t.Fatalf("refresh canceled bound context: %v", context.Cause(bound))
	default:
	}
}

func TestTokenStoreSessionLease_ClearCancelsWithStableCause(t *testing.T) {
	store := NewTokenStore(newFakeKeychain())
	if err := store.Save(validLeaseTestPair("access", "refresh")); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	lease, err := store.AcquireSessionLease()
	if err != nil {
		t.Fatalf("AcquireSessionLease: %v", err)
	}
	bound, cancelBound := lease.BindContext(context.Background())
	defer cancelBound()
	if err := store.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	select {
	case <-bound.Done():
		if cause := context.Cause(bound); !errors.Is(cause, ErrSessionChanged) {
			t.Fatalf("bound cause = %v, want ErrSessionChanged", cause)
		}
	case <-time.After(time.Second):
		t.Fatal("Clear did not cancel the session lease")
	}
	if _, err := store.AcquireSessionLease(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("post-Clear AcquireSessionLease = %v, want ErrNoSession", err)
	}
}

func TestTokenStoreSessionLease_FenceBlocksNewWorkAndStaleCAS(t *testing.T) {
	store := NewTokenStore(newFakeKeychain())
	oldPair := validLeaseTestPair("old-access", "old-refresh")
	if err := store.Save(oldPair); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	before, err := store.GetSnapshot()
	if err != nil {
		t.Fatalf("before snapshot: %v", err)
	}
	fenced := store.FenceCurrentSession()
	if fenced.Pair.RefreshToken != oldPair.RefreshToken {
		t.Fatalf("fence lost revocation pair: %+v", fenced.Pair)
	}
	if err := before.Lease.Check(); !errors.Is(err, ErrSessionChanged) {
		t.Fatalf("old lease Check = %v, want ErrSessionChanged", err)
	}
	if _, err := store.AcquireSessionLease(); !errors.Is(err, ErrSessionChanged) {
		t.Fatalf("fenced AcquireSessionLease = %v, want ErrSessionChanged", err)
	}
	if committed, err := store.SaveIfSnapshot(validLeaseTestPair("stale", "stale-refresh"), before); err != nil || committed {
		t.Fatalf("stale refresh committed=%v err=%v after fence", committed, err)
	}

	if err := store.Save(validLeaseTestPair("new-access", "new-refresh")); err != nil {
		t.Fatalf("new login Save: %v", err)
	}
	after, err := store.GetSnapshot()
	if err != nil {
		t.Fatalf("post-login snapshot: %v", err)
	}
	if after.Lease.Epoch() == before.Lease.Epoch() {
		t.Fatalf("new login reused fenced epoch %d", after.Lease.Epoch())
	}
}

func TestTokenStoreSessionLease_StaleFenceCannotRetireConcurrentReplacementLogin(t *testing.T) {
	store := NewTokenStore(newFakeKeychain())
	if err := store.Save(validLeaseTestPair("old-access", "old-refresh")); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	oldLease, err := store.AcquireSessionLease()
	if err != nil {
		t.Fatalf("old AcquireSessionLease: %v", err)
	}

	// Model an old request that has already decided its credential is invalid,
	// but reaches the fence only after a concurrent login becomes authoritative.
	releaseOldRequest := make(chan struct{})
	oldRequestDone := make(chan bool, 1)
	go func() {
		<-releaseOldRequest
		oldRequestDone <- store.FenceSessionLease(oldLease)
	}()

	newPair := validLeaseTestPair("new-access", "new-refresh")
	if err := store.Save(newPair); err != nil {
		t.Fatalf("replacement login Save: %v", err)
	}
	close(releaseOldRequest)
	select {
	case fenced := <-oldRequestDone:
		if fenced {
			t.Fatal("stale request reported fencing the replacement session")
		}
	case <-time.After(time.Second):
		t.Fatal("stale request fence did not finish")
	}

	current, err := store.GetSnapshot()
	if err != nil {
		t.Fatalf("replacement session was fenced: %v", err)
	}
	if current.Pair.AccessToken != newPair.AccessToken || current.Lease.Epoch() == oldLease.Epoch() {
		t.Fatalf("stale request changed replacement authority: %+v", current)
	}
	if err := current.Lease.Check(); err != nil {
		t.Fatalf("replacement lease is not usable: %v", err)
	}
}

func TestTokenStoreSessionLease_CurrentFenceReportsAndRetiresLease(t *testing.T) {
	store := NewTokenStore(newFakeKeychain())
	if err := store.Save(validLeaseTestPair("access", "refresh")); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	lease, err := store.AcquireSessionLease()
	if err != nil {
		t.Fatalf("AcquireSessionLease: %v", err)
	}
	if !store.FenceSessionLease(lease) {
		t.Fatal("current lease was not fenced")
	}
	if err := lease.Check(); !errors.Is(err, ErrSessionChanged) {
		t.Fatalf("fenced lease Check = %v, want ErrSessionChanged", err)
	}
	if _, err := store.GetSnapshot(); !errors.Is(err, ErrSessionChanged) {
		t.Fatalf("fenced store GetSnapshot = %v, want ErrSessionChanged", err)
	}
}

func TestSessionLeaseBindContext_PreservesCallerCancellationCause(t *testing.T) {
	store := NewTokenStore(newFakeKeychain())
	if err := store.Save(validLeaseTestPair("access", "refresh")); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	lease, err := store.AcquireSessionLease()
	if err != nil {
		t.Fatalf("AcquireSessionLease: %v", err)
	}
	parent, cancelParent := context.WithCancel(context.Background())
	bound, cancelBound := lease.BindContext(parent)
	defer cancelBound()
	cancelParent()
	<-bound.Done()
	if cause := context.Cause(bound); !errors.Is(cause, context.Canceled) || errors.Is(cause, ErrSessionChanged) {
		t.Fatalf("caller cancellation cause = %v", cause)
	}
}

func TestSessionLeaseWithCurrent_LinearizesCommitBeforeLoginReplacement(t *testing.T) {
	store := NewTokenStore(newFakeKeychain())
	if err := store.Save(validLeaseTestPair("old-access", "old-refresh")); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	lease, err := store.AcquireSessionLease()
	if err != nil {
		t.Fatalf("AcquireSessionLease: %v", err)
	}

	commitEntered := make(chan struct{})
	releaseCommit := make(chan struct{})
	commitDone := make(chan error, 1)
	go func() {
		commitDone <- lease.WithCurrent(func() error {
			close(commitEntered)
			<-releaseCommit
			return nil
		})
	}()
	<-commitEntered

	loginEntered := make(chan struct{})
	loginDone := make(chan error, 1)
	go func() {
		close(loginEntered)
		loginDone <- store.Save(validLeaseTestPair("new-access", "new-refresh"))
	}()
	<-loginEntered
	select {
	case err := <-loginDone:
		t.Fatalf("login crossed guarded commit: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseCommit)
	if err := <-commitDone; err != nil {
		t.Fatalf("WithCurrent commit: %v", err)
	}
	if err := <-loginDone; err != nil {
		t.Fatalf("replacement Save: %v", err)
	}
	if err := lease.Check(); !errors.Is(err, ErrSessionChanged) {
		t.Fatalf("old lease after login = %v, want ErrSessionChanged", err)
	}
}

func TestSessionLeaseWithCurrent_RejectsRetiredLeaseWithoutCallback(t *testing.T) {
	store := NewTokenStore(newFakeKeychain())
	if err := store.Save(validLeaseTestPair("old-access", "old-refresh")); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	oldLease, err := store.AcquireSessionLease()
	if err != nil {
		t.Fatalf("AcquireSessionLease: %v", err)
	}
	if err := store.Save(validLeaseTestPair("new-access", "new-refresh")); err != nil {
		t.Fatalf("replacement Save: %v", err)
	}
	newLease, err := store.AcquireSessionLease()
	if err != nil {
		t.Fatalf("new AcquireSessionLease: %v", err)
	}
	if oldLease.SameSession(newLease) {
		t.Fatal("replacement login reused old session identity")
	}
	called := false
	if err := oldLease.WithCurrent(func() error {
		called = true
		return nil
	}); !errors.Is(err, ErrSessionChanged) {
		t.Fatalf("retired WithCurrent = %v, want ErrSessionChanged", err)
	}
	if called {
		t.Fatal("retired lease callback was invoked")
	}
	if err := newLease.WithCurrent(nil); err != nil {
		t.Fatalf("current nil commit guard: %v", err)
	}
}
