//go:build desktop

package cloud_proxy

import (
	"context"
	"errors"
	"time"
)

const refreshGatePollInterval = 5 * time.Millisecond

// LogoutRevokeStatus is the deliberately closed result exposed to the
// Renderer. Upstream and transport diagnostics stay inside the cloud layer.
type LogoutRevokeStatus string

const (
	LogoutRevokeSkipped LogoutRevokeStatus = "skipped"
	LogoutRevokeOK      LogoutRevokeStatus = "ok"
	LogoutRevokeFailed  LogoutRevokeStatus = "failed"
)

// ErrLogoutLocalCleanup reports that the process-local session is already
// unusable but durable credential removal failed. It intentionally does not
// wrap the Keychain implementation's error.
var ErrLogoutLocalCleanup = errors.New("logout: local session cleanup failed")

type LogoutResult struct {
	RevokeStatus LogoutRevokeStatus
}

// LogoutSession immediately fences the authenticated session, then linearizes
// remote revocation and durable clearing with proactive and 401-driven
// refreshes. Fencing happens before waiting for refreshMu so already-authorized
// cloud I/O is canceled without inheriting the logout request's wait budget.
//
// Revocation is best effort and never prevents local clearing. The caller must
// fence/cancel login producers before calling this function; TokenStore's
// refresh gate owns refresh ordering, while the login generation gate owns
// new-session ordering.
func LogoutSession(ctx context.Context, store *TokenStore, cloud *Client) (LogoutResult, error) {
	result := LogoutResult{RevokeStatus: LogoutRevokeSkipped}
	if store == nil {
		return result, ErrLogoutLocalCleanup
	}
	if ctx == nil {
		ctx = context.Background()
	}
	retired := store.FenceCurrentSession()

	if !lockRefreshGateUntil(ctx, store) {
		// The caller's logout budget expired behind an in-flight rotation.
		// The pre-gate epoch fence already canceled its context and rejected its
		// later guarded save. Clear now makes that in-process result durable even
		// if a non-cooperative transport has not released refreshMu yet.
		result.RevokeStatus = LogoutRevokeFailed
		if err := store.Clear(); err != nil {
			return result, ErrLogoutLocalCleanup
		}
		return result, nil
	}
	defer store.refreshMu.Unlock()

	if retired.Pair.RefreshToken != "" && cloud != nil {
		if err := cloud.RevokeRefreshToken(ctx, retired.Pair.RefreshToken); err != nil {
			result.RevokeStatus = LogoutRevokeFailed
		} else {
			result.RevokeStatus = LogoutRevokeOK
		}
	}

	// Clear installs TokenStore's authoritative in-process tombstone even if
	// the durable delete fails, so no local caller can continue using the pair.
	if err := store.Clear(); err != nil {
		return result, ErrLogoutLocalCleanup
	}
	return result, nil
}

// lockRefreshGateUntil adds cancellation to sync.Mutex without spawning a
// waiter goroutine that could acquire and strand the lock after its caller has
// returned. Credential paths have few contenders, so a short TryLock interval
// keeps the implementation bounded and allocation-light when contended.
func lockRefreshGateUntil(ctx context.Context, store *TokenStore) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	if store.refreshMu.TryLock() {
		return true
	}
	ticker := time.NewTicker(refreshGatePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			if store.refreshMu.TryLock() {
				return true
			}
		}
	}
}
