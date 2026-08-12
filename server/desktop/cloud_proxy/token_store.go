//go:build desktop

package cloud_proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// TokenPair is the serialized form of an OAuth session. Stored as
// JSON bytes in the Keychain under (KeychainService, KeychainAccount).
//
// Wire shape is internal — the renderer never sees this. Adding a
// field is safe (older saved entries deserialize with zero values);
// renaming is not (would orphan the previous session).
type TokenPair struct {
	AccessToken      string    `json:"access_token"`
	AccessExpiresAt  time.Time `json:"access_expires_at"`
	RefreshToken     string    `json:"refresh_token"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at"`
	Scope            string    `json:"scope"`
	SavedAt          time.Time `json:"saved_at"`
	// sessionStore/sessionEpoch are process-local provenance attached to copies
	// returned by TokenStore. They are never serialized and let the compatibility
	// 401 API reject replaced-login and cross-store tokens without guessing from
	// credential strings.
	sessionStore *TokenStore
	sessionEpoch uint64
}

// IsAccessExpired returns true when the access token is past its
// expiry. Conservative — also true when AccessExpiresAt is zero
// (uninitialized) so callers don't accidentally use a token they
// know nothing about.
func (p TokenPair) IsAccessExpired(now time.Time) bool {
	if p.AccessToken == "" {
		return true
	}
	if p.AccessExpiresAt.IsZero() {
		return true
	}
	return now.After(p.AccessExpiresAt)
}

// NeedsRefresh returns true when the access token will expire within
// the next `buffer` duration. Used to proactively rotate before the
// caller's chat request hits the wire.
func (p TokenPair) NeedsRefresh(now time.Time, buffer time.Duration) bool {
	if p.AccessToken == "" {
		return true
	}
	if p.AccessExpiresAt.IsZero() {
		return true
	}
	return now.Add(buffer).After(p.AccessExpiresAt)
}

// IsRefreshExpired returns true when the refresh token itself has
// expired. At that point the only path forward is a fresh OAuth
// flow (re-login).
func (p TokenPair) IsRefreshExpired(now time.Time) bool {
	if p.RefreshExpiresAt.IsZero() {
		return true
	}
	return now.After(p.RefreshExpiresAt)
}

// ErrNoSession is returned when no authoritative session exists: Keychain is
// empty, or the higher-authority durable tombstone is marked after logout.
// Distinct from "load failed for other reasons" so handlers can render the
// LoginPage state vs an error state.
var ErrNoSession = errors.New("token_store: no session in keychain")

// ErrSessionChanged reports that work bound to an authenticated Desktop
// session outlived that session. An unconditional login Save (including a
// same-user re-login), logout Clear, or explicit FenceCurrentSession cancels
// every lease from the previous epoch with this cause.
//
// Callers should distinguish this from context.Canceled: the former means the
// credential authority changed underneath the operation, while the latter is
// an ordinary caller-initiated cancellation.
var ErrSessionChanged = errors.New("token_store: session changed")

// SessionLease binds in-flight work to one TokenStore session epoch. Epochs
// are independent from revisions: access-token refresh advances revision but
// deliberately preserves the lease, while login replacement and logout retire
// the epoch even when the subject or token values happen to be identical.
//
// A lease is intentionally opaque. Use BindContext for cloud I/O and Check at
// local commit boundaries.
type SessionLease struct {
	store *TokenStore
	epoch uint64
	ctx   context.Context
}

// Epoch returns the opaque process-local epoch identifier. It is useful for
// diagnostics and tests only; authorization decisions must use Check.
func (l SessionLease) Epoch() uint64 { return l.epoch }

// SameSession reports whether two leases name the same TokenStore epoch. It
// does not assert that the epoch is still current; call Check or WithCurrent
// for an authorization decision.
func (l SessionLease) SameSession(other SessionLease) bool {
	return l.store != nil &&
		l.store == other.store &&
		l.epoch != 0 &&
		l.epoch == other.epoch &&
		l.ctx != nil &&
		l.ctx == other.ctx
}

// Check verifies that the lease is still the current, unfenced authenticated
// session. It is safe to call repeatedly and from any goroutine.
func (l SessionLease) Check() error {
	if l.store == nil || l.epoch == 0 || l.ctx == nil {
		return ErrSessionChanged
	}
	if cause := context.Cause(l.ctx); cause != nil {
		return ErrSessionChanged
	}
	l.store.mu.Lock()
	defer l.store.mu.Unlock()
	if !l.store.leaseCurrentLocked(l) {
		return ErrSessionChanged
	}
	return nil
}

// WithCurrent atomically validates this lease and runs a short local
// transaction callback while TokenStore session replacement is excluded. It
// closes both the session TOCTOU and lock-order inversion: callers that share
// SQLite with the durable logout marker must acquire TokenStore.mu first here,
// then perform their complete Begin/write/cursor/Commit sequence. Either that
// transaction linearizes before login/logout, or it is not invoked and
// ErrSessionChanged is returned.
//
// The callback MUST NOT call TokenStore methods, SessionLease.Check,
// SessionLease.WithCurrent, or any function that may do so; TokenStore.mu is
// held for the callback and such re-entry would deadlock. It should contain
// only a bounded local transaction and must not perform cloud I/O.
func (l SessionLease) WithCurrent(transaction func() error) error {
	if l.store == nil || l.epoch == 0 || l.ctx == nil {
		return ErrSessionChanged
	}
	l.store.mu.Lock()
	defer l.store.mu.Unlock()
	if !l.store.leaseCurrentLocked(l) {
		return ErrSessionChanged
	}
	if transaction == nil {
		return nil
	}
	return transaction()
}

// BindContext derives a context that is canceled with ErrSessionChanged as
// its cause when this epoch is retired. context.AfterFunc avoids a permanent
// bridge goroutine per request; the returned cancel function detaches the hook.
// A nil parent is treated as context.Background.
func (l SessionLease) BindContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	bound, cancelCause := context.WithCancelCause(parent)
	if l.ctx == nil {
		cancelCause(ErrSessionChanged)
		return bound, func() { cancelCause(context.Canceled) }
	}
	stop := context.AfterFunc(l.ctx, func() {
		cancelCause(ErrSessionChanged)
	})
	// Close the registration-vs-retirement race synchronously. If retirement
	// won immediately after this check, AfterFunc still delivers the cause.
	if err := l.Check(); err != nil {
		cancelCause(err)
	}
	return bound, func() {
		stop()
		cancelCause(context.Canceled)
	}
}

// TokenStoreSnapshot is an immutable copy of the current session plus the
// revision at which it was observed. Revision is intentionally opaque: callers
// may only carry it back to SaveIfRevision to prove that no login, logout, or
// explicit Keychain reload committed while they were doing background work.
type TokenStoreSnapshot struct {
	Pair                TokenPair
	Revision            uint64
	Lease               SessionLease
	PersistenceDegraded bool
}

// TokenStore is the in-process cache + Keychain backing for the current
// TokenPair, optionally guarded by a non-secret durable logout marker.
// Thread-safe; one instance per sidecar.
//
// Caches the last-loaded pair in memory so the hot path
// (every chat request needs an access token) doesn't shell out to
// the Keychain. Writes go through both layers.
type TokenStore struct {
	keychain Keychain
	service  string
	account  string
	marker   SessionTombstoneMarker

	// One mutex linearizes the persistent and in-memory layers. Keychain calls
	// deliberately happen while it is held: otherwise a slow Load can cache an
	// older value after a newer Save/Clear has already committed.
	mu       sync.Mutex
	cached   *TokenPair // nil = no session OR not yet loaded
	loaded   bool       // true once Load* has run at least once
	revision uint64     // advances after every authoritative state commit
	// epoch and epochCtx model the lifetime of the authenticated identity.
	// Refresh commits preserve them; login/logout/fence retire them. fenced
	// blocks new leases during the gap between an explicit fence and the final
	// Save/Clear transition (notably while logout waits for refreshMu).
	epoch       uint64
	epochCtx    context.Context
	epochCancel context.CancelCauseFunc
	fenced      bool
	// persistenceDegraded means volatile state is authoritative but its
	// Keychain write/delete failed. While set, Load must not downgrade a rotated
	// pair or resurrect a logout tombstone from stale persistent bytes.
	persistenceDegraded bool

	// refreshMu is a process-local single-flight gate shared by proactive
	// refresh and every 401 recovery path. Refresh tokens rotate server-side;
	// allowing two exchanges with the same old token can make the loser look
	// like a replay and invalidate the winner's chain.
	refreshMu sync.Mutex
}

// NewTokenStore constructs a store backed by the given Keychain.
// Does NOT pre-load; first Load() / Get() triggers the read.
func NewTokenStore(kc Keychain) *TokenStore {
	return NewTokenStoreWithTombstone(kc, nil)
}

// NewTokenStoreWithTombstone constructs a TokenStore with a durable,
// non-secret logout marker. Production Desktop supplies its local SQLite
// marker; tests and embedders that only need legacy Keychain behavior can keep
// using NewTokenStore.
func NewTokenStoreWithTombstone(kc Keychain, marker SessionTombstoneMarker) *TokenStore {
	return &TokenStore{
		keychain: kc,
		service:  KeychainServiceName(),
		account:  KeychainAccount,
		marker:   marker,
	}
}

func (s *TokenStore) currentLeaseLocked() SessionLease {
	if s.cached == nil || s.fenced || s.epochCtx == nil || context.Cause(s.epochCtx) != nil {
		return SessionLease{}
	}
	return SessionLease{store: s, epoch: s.epoch, ctx: s.epochCtx}
}

func (s *TokenStore) leaseCurrentLocked(lease SessionLease) bool {
	return lease.store == s &&
		lease.epoch != 0 &&
		lease.epoch == s.epoch &&
		lease.ctx != nil &&
		lease.ctx == s.epochCtx &&
		context.Cause(lease.ctx) == nil &&
		s.cached != nil &&
		!s.fenced
}

func (s *TokenStore) retireEpochLocked() {
	if s.epochCancel != nil {
		s.epochCancel(ErrSessionChanged)
	}
	s.epochCtx = nil
	s.epochCancel = nil
}

func (s *TokenStore) beginEpochLocked() {
	s.retireEpochLocked()
	s.epoch++
	if s.epoch == 0 {
		// Preserve zero as the invalid/empty SessionLease sentinel after wrap.
		s.epoch++
	}
	s.epochCtx, s.epochCancel = context.WithCancelCause(context.Background())
}

// Load reads the session from Keychain into the in-memory cache. Repeat reads
// only advance revision when the authoritative state actually changes. While a
// rotated pair is volatile because persistence failed, Load returns that dirty
// winner instead of downgrading it from stale Keychain bytes.
// Returns ErrNoSession if nothing is stored.
func (s *TokenStore) Load() (*TokenPair, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

// loadLocked refreshes both layers as one linearized operation. The caller
// must hold s.mu.
func (s *TokenStore) loadLocked() (*TokenPair, error) {
	if s.fenced {
		return nil, ErrSessionChanged
	}
	if s.persistenceDegraded && s.loaded {
		if s.cached == nil {
			return nil, ErrNoSession
		}
		copy := *s.cached
		return &copy, nil
	}
	if s.marker != nil {
		marked, err := s.marker.IsMarked()
		if err != nil {
			// Do not accept Keychain bytes when the higher-authority marker
			// cannot be read. Invalidate a previously cached session as well;
			// loaded remains false so a later call can retry the marker.
			changed := s.loaded || s.cached != nil
			s.cached = nil
			s.loaded = false
			s.persistenceDegraded = false
			s.retireEpochLocked()
			if changed {
				s.revision++
			}
			return nil, ErrSessionStateUnavailable
		}
		if marked {
			changed := !s.loaded || s.cached != nil || s.persistenceDegraded
			s.cached = nil
			s.loaded = true
			s.persistenceDegraded = false
			s.retireEpochLocked()
			if changed {
				s.revision++
			}
			return nil, ErrNoSession
		}
	}
	raw, err := s.keychain.Read(s.service, s.account)
	if err != nil {
		if errors.Is(err, ErrKeychainNoEntry) {
			changed := !s.loaded || s.cached != nil
			s.cached = nil
			s.loaded = true
			s.persistenceDegraded = false
			s.retireEpochLocked()
			if changed {
				s.revision++
			}
			return nil, ErrNoSession
		}
		return nil, fmt.Errorf("token_store load: %w", err)
	}
	// Keychain bytes contain both bearer credentials. Keep their lifetime as
	// short as possible even on the corrupt-JSON branch.
	defer clear(raw)
	var pair TokenPair
	if err := json.Unmarshal(raw, &pair); err != nil {
		// Corrupted entry. Treat as missing so user can re-login;
		// surface as a non-fatal log signal upstream.
		return nil, fmt.Errorf("token_store load: corrupt entry: %w", err)
	}
	changed := !s.loaded || s.cached == nil || !tokenPairsEqual(*s.cached, pair)
	s.cached = &pair
	s.loaded = true
	s.persistenceDegraded = false
	if changed {
		s.revision++
		s.beginEpochLocked()
	} else if s.epochCtx == nil {
		// Compatibility for a store loaded before epochs were initialized.
		s.beginEpochLocked()
	}
	copy := pair
	return &copy, nil
}

// Get returns the cached TokenPair, loading from Keychain on first
// call. Safe to call from any goroutine.
func (s *TokenStore) Get() (*TokenPair, error) {
	snapshot, err := s.GetSnapshot()
	if err != nil {
		return nil, err
	}
	pair := snapshot.Pair
	return &pair, nil
}

// GetSnapshot returns the cached session and the revision that guards it,
// loading from Keychain on first use. The returned TokenPair is a copy.
//
// When there is no session, the returned snapshot still contains the current
// revision, but err is ErrNoSession and Pair is zero-valued.
func (s *TokenStore) GetSnapshot() (TokenStoreSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.loaded {
		if _, err := s.loadLocked(); err != nil {
			return TokenStoreSnapshot{
				Revision:            s.revision,
				PersistenceDegraded: s.persistenceDegraded,
			}, err
		}
	}
	snapshot := TokenStoreSnapshot{
		Revision:            s.revision,
		Lease:               s.currentLeaseLocked(),
		PersistenceDegraded: s.persistenceDegraded,
	}
	if s.fenced {
		return snapshot, ErrSessionChanged
	}
	if s.cached == nil {
		return snapshot, ErrNoSession
	}
	snapshot.Pair = *s.cached
	snapshot.Pair.sessionStore = s
	snapshot.Pair.sessionEpoch = snapshot.Lease.epoch
	return snapshot, nil
}

// AcquireSessionLease loads the current authoritative session, if necessary,
// and returns a lease for its epoch. Callers that also need credentials should
// normally use GetSnapshot or AcquireAccessTokenWithLease so the pair and lease
// are observed atomically.
func (s *TokenStore) AcquireSessionLease() (SessionLease, error) {
	snapshot, err := s.GetSnapshot()
	if err != nil {
		return SessionLease{}, err
	}
	if err := snapshot.Lease.Check(); err != nil {
		return SessionLease{}, err
	}
	return snapshot.Lease, nil
}

// FenceCurrentSession immediately retires the current epoch and blocks new
// leases without deleting credentials. It is used before logout waits for the
// refresh gate, so already-authorized cloud I/O stops immediately and stale
// refresh CAS operations cannot commit. The returned snapshot contains the
// pre-fence pair for best-effort remote revocation; its lease is already
// canceled when this method returns.
//
// A following Save starts a new login epoch. A following Clear makes logout
// durable. Until one of those transitions completes, GetSnapshot and
// AcquireSessionLease return ErrSessionChanged.
func (s *TokenStore) FenceCurrentSession() TokenStoreSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded {
		// Best effort: logout needs the persisted refresh token for remote
		// revocation even when no hot-path request loaded the store yet. A load
		// failure still proceeds to the local fence.
		_, _ = s.loadLocked()
	}

	snapshot := TokenStoreSnapshot{
		Revision:            s.revision,
		Lease:               s.currentLeaseLocked(),
		PersistenceDegraded: s.persistenceDegraded,
	}
	if s.cached != nil {
		snapshot.Pair = *s.cached
		snapshot.Pair.sessionStore = s
		snapshot.Pair.sessionEpoch = snapshot.Lease.epoch
	}
	if !s.fenced {
		s.retireEpochLocked()
		s.fenced = true
		s.revision++
	}
	return snapshot
}

// FenceSessionLease retires the authenticated session only when lease still
// names the current epoch. A stale request must never fence a replacement
// login that became authoritative while the request was validating a cloud
// credential. The return value only tells a trusted in-process caller whether
// this exact lease was retired; it carries no replacement-session data.
func (s *TokenStore) FenceSessionLease(lease SessionLease) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.leaseCurrentLocked(lease) {
		return false
	}
	s.retireEpochLocked()
	s.fenced = true
	s.revision++
	return true
}

// Save writes the pair to Keychain and updates the cache. Stamps
// SavedAt to time.Now in UTC; callers don't have to.
func (s *TokenStore) Save(pair TokenPair) error {
	pair, raw, err := encodeTokenPair(pair)
	if err != nil {
		return fmt.Errorf("token_store save: marshal: %w", err)
	}
	defer clear(raw)

	s.mu.Lock()
	defer s.mu.Unlock()
	// An unconditional Save is a new login, not a refresh, even when the
	// subject and serialized token values are identical. Cancel old work before
	// persistence I/O and keep new acquisitions fenced until commit completes.
	s.retireEpochLocked()
	s.fenced = true
	if err := s.markLocked(); err != nil {
		deleteErr := s.keychain.Delete(s.service, s.account)
		s.commitNoSessionLocked(deleteErr != nil)
		return ErrSessionPersistence
	}
	if err := s.keychain.Write(s.service, s.account, raw); err != nil {
		deleteErr := s.keychain.Delete(s.service, s.account)
		// A failed new-login commit must not leave the previous session looking
		// authenticated. Retire any persistent residue best-effort and install a
		// no-session tombstone even though Save still reports failure.
		s.cached = nil
		s.loaded = true
		s.persistenceDegraded = deleteErr != nil
		s.revision++
		s.fenced = false
		return ErrSessionPersistence
	}
	if err := s.unmarkLocked(); err != nil {
		// Unmark can fail ambiguously after committing its delete. Reassert the
		// tombstone before deleting the just-written credentials so either
		// persistence layer that remains reachable says "no session".
		markErr := s.markLocked()
		deleteErr := s.keychain.Delete(s.service, s.account)
		s.commitNoSessionLocked(markErr != nil || deleteErr != nil)
		return ErrSessionPersistence
	}
	s.cached = &pair
	s.loaded = true
	s.persistenceDegraded = false
	s.fenced = false
	s.revision++
	s.beginEpochLocked()
	return nil
}

// SaveIfRevision writes pair only when expectedRevision is still the current
// store revision. The revision check, Keychain write, and cache update are one
// linearized operation, so an unconditional Save (new login), Clear (logout),
// or Load that commits first always wins over a stale background refresh.
//
// The bool reports whether pair committed. A revision mismatch is not an I/O
// error and returns (false, nil); the caller should discard its stale result and
// re-read the authoritative session. If Keychain persistence fails after a
// successful rotation, the pair still commits authoritatively to the volatile
// cache and the method returns (true, error); this prevents reuse of the now-old
// refresh token. GetSnapshot reports that degraded persistence state.
func (s *TokenStore) SaveIfRevision(pair TokenPair, expectedRevision uint64) (bool, error) {
	return s.saveIfGuard(pair, expectedRevision, SessionLease{}, false)
}

// SaveIfSnapshot is the lease-aware refresh commit. Both revision and session
// epoch must still match, preventing an old refresh response from committing
// across an explicit fence even if a future implementation changes revision
// bookkeeping. Successful refresh preserves the epoch and all bound work.
func (s *TokenStore) SaveIfSnapshot(pair TokenPair, expected TokenStoreSnapshot) (bool, error) {
	return s.saveIfGuard(pair, expected.Revision, expected.Lease, true)
}

func (s *TokenStore) saveIfGuard(
	pair TokenPair,
	expectedRevision uint64,
	expectedLease SessionLease,
	guardLease bool,
) (bool, error) {
	pair, raw, err := encodeTokenPair(pair)
	if err != nil {
		return false, fmt.Errorf("token_store conditional save: marshal: %w", err)
	}
	defer clear(raw)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revision != expectedRevision || s.fenced || s.cached == nil || s.epochCtx == nil {
		return false, nil
	}
	if guardLease && !s.leaseCurrentLocked(expectedLease) {
		return false, nil
	}
	if err := s.markLocked(); err != nil {
		// The refresh response may already contain a server-side rotated token.
		// Preserve that winner in this process even though durable fail-closed
		// state could not be established. Best-effort removal still prevents a
		// restart from replaying the old Keychain entry.
		_ = s.keychain.Delete(s.service, s.account)
		s.cached = &pair
		s.loaded = true
		s.persistenceDegraded = true
		s.revision++
		return true, ErrSessionPersistence
	}
	if err := s.keychain.Write(s.service, s.account, raw); err != nil {
		_ = s.keychain.Delete(s.service, s.account)
		// The server may already have rotated the refresh token. Keeping the old
		// cache would invite a replay on the next request, and Darwin's
		// delete-then-add Keychain implementation may also have removed the old
		// persistent item before failing. Best-effort deletion prevents a restart
		// from reloading any residue; commit the winner in memory under the same
		// revision guard and shield it from subsequent Load calls.
		s.cached = &pair
		s.loaded = true
		s.persistenceDegraded = true
		s.revision++
		return true, ErrSessionPersistence
	}
	if err := s.unmarkLocked(); err != nil {
		markErr := s.markLocked()
		deleteErr := s.keychain.Delete(s.service, s.account)
		s.commitNoSessionLocked(markErr != nil || deleteErr != nil)
		return false, ErrSessionPersistence
	}
	s.cached = &pair
	s.loaded = true
	s.persistenceDegraded = false
	s.revision++
	return true, nil
}

func encodeTokenPair(pair TokenPair) (TokenPair, []byte, error) {
	if pair.SavedAt.IsZero() {
		pair.SavedAt = time.Now().UTC()
	}
	raw, err := json.Marshal(pair)
	return pair, raw, err
}

func tokenPairsEqual(left, right TokenPair) bool {
	return left.AccessToken == right.AccessToken &&
		left.AccessExpiresAt.Equal(right.AccessExpiresAt) &&
		left.RefreshToken == right.RefreshToken &&
		left.RefreshExpiresAt.Equal(right.RefreshExpiresAt) &&
		left.Scope == right.Scope &&
		left.SavedAt.Equal(right.SavedAt)
}

// Clear marks durable logout before deleting the Keychain entry. If either
// persistence step fails it returns a closed error but still installs an
// in-process no-session tombstone, so Get/Load cannot resurrect old
// credentials. Repeating Clear retries both idempotent operations. Called on
// explicit logout; ambiguous refresh failures use ClearIfRevision.
func (s *TokenStore) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clearLocked()
}

// ClearIfRevision conditionally makes logout authoritative only when
// expectedRevision is still current. It is the fail-closed endpoint for an
// ambiguous refresh exchange: removing the old Keychain token prevents a
// process restart from replaying a token the server may already have consumed,
// while the revision guard preserves a concurrent successful login.
//
// The bool reports whether the conditional logout committed. As with Clear, a
// Keychain delete failure still commits an in-process no-session tombstone and
// returns (true, error); when the durable marker succeeded, a new process also
// stays logged out while Keychain cleanup is retried.
func (s *TokenStore) ClearIfRevision(expectedRevision uint64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revision != expectedRevision {
		return false, nil
	}
	return true, s.clearLocked()
}

// clearLocked makes no-session authoritative regardless of Keychain deletion
// success. The caller must hold s.mu.
func (s *TokenStore) clearLocked() error {
	// Cancel in-flight requests before persistence I/O. Holding s.mu keeps new
	// acquisitions blocked until the authoritative no-session state commits.
	s.retireEpochLocked()
	s.fenced = true
	markErr := s.markLocked()
	deleteErr := s.keychain.Delete(s.service, s.account)
	// Logout is authoritative in this process even if persistence failed. Bump
	// revision before returning the error so every pre-Clear refresh CAS fails,
	// and retain a dirty nil-cache tombstone so Load cannot resurrect the old
	// Keychain entry while the UI retries deletion.
	s.cached = nil
	s.loaded = true
	s.persistenceDegraded = deleteErr != nil
	s.fenced = false
	s.revision++
	if markErr != nil || deleteErr != nil {
		return ErrSessionPersistence
	}
	return nil
}

// markLocked/unmarkLocked are no-ops for the compatibility constructor. The
// caller must hold s.mu so the marker, Keychain and cache form one linearized
// state machine inside a sidecar process.
func (s *TokenStore) markLocked() error {
	if s.marker == nil {
		return nil
	}
	if err := s.marker.Mark(); err != nil {
		return ErrSessionPersistence
	}
	return nil
}

func (s *TokenStore) unmarkLocked() error {
	if s.marker == nil {
		return nil
	}
	if err := s.marker.Unmark(); err != nil {
		return ErrSessionPersistence
	}
	return nil
}

func (s *TokenStore) commitNoSessionLocked(persistenceDegraded bool) {
	s.retireEpochLocked()
	s.cached = nil
	s.loaded = true
	s.persistenceDegraded = persistenceDegraded
	s.fenced = false
	s.revision++
}
