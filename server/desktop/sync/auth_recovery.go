//go:build desktop

package sync

import (
	"context"
	"fmt"

	cloudproxy "server/desktop/cloud_proxy"
)

// Keep the package-local name for existing sync callers/tests while sharing
// the TokenStore's epoch sentinel. A UID comparison is still useful defense in
// depth, but an epoch change also catches logout + same-UID re-login.
var errSyncSessionChanged = cloudproxy.ErrSessionChanged

// recoverSyncAccessToken forces the shared, revision-fenced 401 recovery path.
// A concurrent login may legitimately win while the rejected request is in
// flight; never continue the old user's cursor/write transaction with that new
// user's token.
func recoverSyncAccessToken(
	ctx context.Context,
	store *cloudproxy.TokenStore,
	cloud *cloudproxy.Client,
	rejected *cloudproxy.TokenPair,
	lease cloudproxy.SessionLease,
	expectedUID uint,
) (*cloudproxy.TokenPair, error) {
	fresh, err := cloudproxy.RefreshAccessTokenAfterUnauthorizedWithLease(
		ctx, store, cloud, rejected, lease,
	)
	if err != nil {
		return nil, err
	}
	if err := lease.Check(); err != nil {
		return nil, err
	}
	uid, err := cloudproxy.ExtractUIDFromAccessToken(fresh.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("sync: parse refreshed uid: %w", err)
	}
	if uid == 0 || uid != expectedUID {
		return nil, errSyncSessionChanged
	}
	return fresh, nil
}
