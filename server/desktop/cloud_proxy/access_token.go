//go:build desktop

package cloud_proxy

import (
	"context"
	"errors"
	"time"
)

// AcquireAccessTokenRefreshBuffer is the proactive-refresh window.
// If the cached access token expires within this duration, we rotate
// BEFORE the caller uses it (otherwise a mid-call expiry surfaces
// as a 401 the caller has to handle anyway, with worse UX).
//
// 30s = enough margin for typical chat-turn duration so we don't
// rotate mid-turn; tight enough that we don't churn refreshes on
// short-lived sessions.
const AcquireAccessTokenRefreshBuffer = 30 * time.Second

// AcquireAccessToken returns a non-expired access token from the
// store, refreshing via the cloud /token endpoint if needed.
// Kept package-level so callers that don't own a Proxy (e.g. the
// sync worker, the userinfo handler) can reuse the same logic.
//
// Errors:
//   - ErrNoSession when the store is empty OR the refresh token
//     has expired OR the refresh call itself fails. Caller routes
//     to LoginPage in all three cases — distinguishing them in the
//     error type would let callers handle them differently, but
//     in practice they all mean "user must re-authenticate."
//
// If a refresh fires, the rotated pair is saved back to the store
// before returning.
func AcquireAccessToken(ctx context.Context, store *TokenStore, cloud *Client) (*TokenPair, error) {
	pair, _, err := acquireAccessToken(ctx, store, cloud, true)
	return pair, err
}

// AcquireAccessTokenWithLease returns credentials and the exact session lease
// under which they were authorized. Unlike the compatibility wrapper above,
// it never migrates a call that began under an old epoch onto a concurrent new
// login. Callers must BindContext before cloud I/O and Check before local
// commits.
func AcquireAccessTokenWithLease(
	ctx context.Context,
	store *TokenStore,
	cloud *Client,
) (*TokenPair, SessionLease, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// Freeze the calling epoch before queueing on refreshMu. Otherwise a strict
	// caller that began under session A could wait behind A's refresh, observe a
	// same-subject login B after entering the gate, and silently authorize work
	// as B. The bound context also stops the gate wait as soon as A is retired.
	expected, err := store.GetSnapshot()
	if err != nil {
		return nil, SessionLease{}, err
	}
	if err := expected.Lease.Check(); err != nil {
		return nil, SessionLease{}, err
	}
	boundCtx, cancelBound := expected.Lease.BindContext(ctx)
	defer cancelBound()
	if !lockRefreshGateUntil(boundCtx, store) {
		if errors.Is(context.Cause(boundCtx), ErrSessionChanged) {
			return nil, SessionLease{}, ErrSessionChanged
		}
		return nil, SessionLease{}, boundCtx.Err()
	}
	defer store.refreshMu.Unlock()
	return acquireAccessTokenForLeaseLocked(boundCtx, store, cloud, expected.Lease)
}

func acquireAccessToken(
	ctx context.Context,
	store *TokenStore,
	cloud *Client,
	allowConcurrentLoginWinner bool,
) (*TokenPair, SessionLease, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// Refresh tokens rotate. Serialize the network exchange with proactive and
	// 401-driven refreshes. Every caller re-reads after entering the gate so a
	// waiter consumes the winner instead of replaying the same old refresh token.
	// The cancellable gate prevents a request whose own deadline has elapsed from
	// remaining stuck behind an unrelated slow refresh.
	if !lockRefreshGateUntil(ctx, store) {
		return nil, SessionLease{}, ctx.Err()
	}
	defer store.refreshMu.Unlock()
	pair, lease, err := acquireAccessTokenWithLeaseLocked(ctx, store, cloud)
	if allowConcurrentLoginWinner && errors.Is(err, ErrSessionChanged) {
		// Legacy callers historically accepted a login that won while proactive
		// refresh was in flight. Retry once inside the gate. Lease-aware callers
		// deliberately receive ErrSessionChanged instead.
		return acquireAccessTokenWithLeaseLocked(ctx, store, cloud)
	}
	return pair, lease, err
}

func acquireAccessTokenWithLeaseLocked(
	ctx context.Context,
	store *TokenStore,
	cloud *Client,
) (*TokenPair, SessionLease, error) {
	return acquireAccessTokenForLeaseLocked(ctx, store, cloud, SessionLease{})
}

func acquireAccessTokenForLeaseLocked(
	ctx context.Context,
	store *TokenStore,
	cloud *Client,
	expectedLease SessionLease,
) (*TokenPair, SessionLease, error) {
	snapshot, err := store.GetSnapshot()
	if err != nil {
		return nil, SessionLease{}, err
	}
	if expectedLease.epoch != 0 && !expectedLease.SameSession(snapshot.Lease) {
		return nil, SessionLease{}, ErrSessionChanged
	}
	if err := snapshot.Lease.Check(); err != nil {
		return nil, SessionLease{}, err
	}
	pair := snapshot.Pair
	now := time.Now().UTC()
	if !pair.NeedsRefresh(now, AcquireAccessTokenRefreshBuffer) {
		return &pair, snapshot.Lease, nil
	}
	if pair.IsRefreshExpired(now) {
		return nil, SessionLease{}, ErrNoSession
	}

	refreshCtx, cancelRefresh := snapshot.Lease.BindContext(ctx)
	defer cancelRefresh()
	newPair, refreshErr := cloud.ExchangeRefreshForTokenForScope(refreshCtx, pair.RefreshToken, pair.Scope)
	if refreshErr != nil {
		if errors.Is(context.Cause(refreshCtx), ErrSessionChanged) || snapshot.Lease.Check() != nil {
			return nil, SessionLease{}, ErrSessionChanged
		}
		// Login may have committed while the HTTP call was failing. Re-read
		// only within the same epoch; a new login is never adopted here.
		winner, winnerErr := failClosedRefreshAttemptForLease(store, snapshot)
		return winner, snapshot.Lease, winnerErr
	}
	if err := snapshot.Lease.Check(); err != nil {
		return nil, SessionLease{}, err
	}
	committed, saveErr := store.SaveIfSnapshot(newPair, snapshot)
	if committed {
		// This includes a revision-guarded volatile commit when Keychain
		// persistence failed. Returning the rotated winner prevents reuse of
		// the old refresh token; GetSnapshot exposes the degraded state.
		newPair.sessionStore = store
		newPair.sessionEpoch = snapshot.Lease.epoch
		return &newPair, snapshot.Lease, nil
	}
	if saveErr != nil {
		// Keychain failures commit volatile state and return committed=true
		// above. Any remaining error did not commit the rotated pair, so fail
		// closed instead of leaving callers able to replay the old token.
		winner, winnerErr := failClosedRefreshAttemptForLease(store, snapshot)
		return winner, snapshot.Lease, winnerErr
	}
	// Login, logout, or an actually changed Keychain Load won during the
	// exchange. Never replay snapshot.Pair.RefreshToken after this point.
	winner, winnerErr := failClosedRefreshAttemptForLease(store, snapshot)
	return winner, snapshot.Lease, winnerErr
}

// RefreshAccessTokenAfterUnauthorized forces one refresh after a cloud API has
// rejected an access token that was locally considered valid. rejected ties the
// recovery to the credentials used by that failed request:
//
//   - if login or another refresh already replaced them, return the current
//     authoritative pair instead of rotating the rejected session;
//   - if logout cleared them, return ErrNoSession;
//   - otherwise rotate and commit only if the observed revision still matches.
//
// A Keychain failure that still makes a guarded volatile commit returns the
// rotated winner; non-committing persistence errors are returned.
func RefreshAccessTokenAfterUnauthorized(
	ctx context.Context,
	store *TokenStore,
	cloud *Client,
	rejected *TokenPair,
) (*TokenPair, error) {
	if rejected == nil {
		return nil, ErrNoSession
	}
	snapshot, snapshotErr := store.GetSnapshot()
	if snapshotErr != nil {
		return nil, snapshotErr
	}
	// TokenStore-returned pairs carry process-local epoch provenance. A zero
	// epoch is accepted only when the exact credentials are still current,
	// preserving compatibility for external test/embedding callers without
	// permitting a credential mismatch to jump sessions.
	if rejected.sessionEpoch != 0 &&
		(rejected.sessionStore != store || rejected.sessionEpoch != snapshot.Lease.epoch) {
		return nil, ErrSessionChanged
	}
	if rejected.sessionEpoch == 0 && !sameStoredCredentials(snapshot.Pair, *rejected) {
		return nil, ErrSessionChanged
	}
	pair, refreshErr := RefreshAccessTokenAfterUnauthorizedWithLease(ctx, store, cloud, rejected, snapshot.Lease)
	if errors.Is(refreshErr, ErrSessionChanged) {
		// Preserve the legacy fail-closed classification when another refresh
		// retired this same chain. A fenced/replaced login remains distinguishable
		// as ErrSessionChanged.
		if _, currentErr := store.GetSnapshot(); errors.Is(currentErr, ErrNoSession) {
			return nil, ErrNoSession
		}
	}
	return pair, refreshErr
}

// RefreshAccessTokenAfterUnauthorizedWithLease forces one rotation for a 401
// only while the rejected request's session epoch remains authoritative. A
// login/logout replacement cancels the exchange and returns ErrSessionChanged;
// it can never turn the old request into a request by the new session.
func RefreshAccessTokenAfterUnauthorizedWithLease(
	ctx context.Context,
	store *TokenStore,
	cloud *Client,
	rejected *TokenPair,
	lease SessionLease,
) (*TokenPair, error) {
	if rejected == nil {
		return nil, ErrNoSession
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := lease.Check(); err != nil {
		return nil, err
	}
	boundCtx, cancelBound := lease.BindContext(ctx)
	defer cancelBound()
	if !lockRefreshGateUntil(boundCtx, store) {
		if errors.Is(context.Cause(boundCtx), ErrSessionChanged) {
			return nil, ErrSessionChanged
		}
		return nil, boundCtx.Err()
	}
	defer store.refreshMu.Unlock()
	return refreshAccessTokenAfterUnauthorizedForLeaseLocked(boundCtx, store, cloud, rejected, lease)
}

func refreshAccessTokenAfterUnauthorizedForLeaseLocked(
	ctx context.Context,
	store *TokenStore,
	cloud *Client,
	rejected *TokenPair,
	lease SessionLease,
) (*TokenPair, error) {
	if err := lease.Check(); err != nil {
		return nil, err
	}
	snapshot, err := store.GetSnapshot()
	if err != nil {
		return nil, err
	}
	if !snapshot.Lease.SameSession(lease) {
		return nil, ErrSessionChanged
	}
	if !sameStoredCredentials(snapshot.Pair, *rejected) {
		pair, currentLease, acquireErr := acquireAccessTokenForLeaseLocked(ctx, store, cloud, lease)
		if acquireErr != nil {
			return nil, acquireErr
		}
		if !currentLease.SameSession(lease) {
			return nil, ErrSessionChanged
		}
		return pair, nil
	}
	if snapshot.Pair.IsRefreshExpired(time.Now().UTC()) {
		return nil, ErrNoSession
	}

	newPair, err := cloud.ExchangeRefreshForTokenForScope(
		ctx,
		snapshot.Pair.RefreshToken,
		snapshot.Pair.Scope,
	)
	if err != nil {
		if errors.Is(context.Cause(ctx), ErrSessionChanged) || lease.Check() != nil {
			return nil, ErrSessionChanged
		}
		return failClosedRefreshAttemptForLease(store, snapshot)
	}
	if err := lease.Check(); err != nil {
		return nil, err
	}
	committed, err := store.SaveIfSnapshot(newPair, snapshot)
	if committed {
		newPair.sessionStore = store
		newPair.sessionEpoch = lease.epoch
		return &newPair, nil
	}
	if err != nil {
		return failClosedRefreshAttemptForLease(store, snapshot)
	}
	return failClosedRefreshAttemptForLease(store, snapshot)
}

// failClosedRefreshAttemptForLease is the strict equivalent used by Desktop
// request paths. A revision winner is reusable only inside the attempted
// epoch. Login/logout replacement returns ErrSessionChanged instead of
// borrowing credentials from the new authority.
func failClosedRefreshAttemptForLease(
	store *TokenStore,
	attempted TokenStoreSnapshot,
) (*TokenPair, error) {
	if winner, winnerErr, changed := resolveRefreshWinnerForLease(store, attempted); changed {
		return winner, winnerErr
	}
	cleared, clearErr := store.ClearIfRevision(attempted.Revision)
	if !cleared {
		if winner, winnerErr, changed := resolveRefreshWinnerForLease(store, attempted); changed {
			return winner, winnerErr
		}
		return nil, ErrSessionChanged
	}
	if clearErr != nil {
		return nil, ErrSessionPersistence
	}
	return nil, ErrNoSession
}

func resolveRefreshWinnerForLease(
	store *TokenStore,
	attempted TokenStoreSnapshot,
) (pair *TokenPair, err error, changed bool) {
	current, currentErr := store.GetSnapshot()
	if current.Revision == attempted.Revision {
		if leaseErr := attempted.Lease.Check(); leaseErr != nil {
			return nil, ErrSessionChanged, true
		}
		if currentErr != nil {
			return nil, currentErr, true
		}
		return nil, nil, false
	}
	if currentErr != nil {
		if errors.Is(currentErr, ErrNoSession) || errors.Is(currentErr, ErrSessionChanged) {
			return nil, ErrSessionChanged, true
		}
		return nil, currentErr, true
	}
	if !current.Lease.SameSession(attempted.Lease) {
		return nil, ErrSessionChanged, true
	}
	if current.Pair.NeedsRefresh(time.Now().UTC(), AcquireAccessTokenRefreshBuffer) {
		return nil, ErrNoSession, true
	}
	pairCopy := current.Pair
	return &pairCopy, nil, true
}

func sameStoredCredentials(left, right TokenPair) bool {
	return left.AccessToken == right.AccessToken && left.RefreshToken == right.RefreshToken
}
