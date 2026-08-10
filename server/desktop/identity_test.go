//go:build desktop

package desktop

import (
	"errors"
	"testing"
	"time"

	cloudproxy "server/desktop/cloud_proxy"
)

// The whole point of the resolver: identity does not depend on the model
// route. The same signed-out machine must resolve to the same owner whether or
// not a local model is configured — otherwise "can I open my own history" is
// answered by a settings page.
func TestResolveIdentity_SignedOutIsLocalRegardlessOfRoute(t *testing.T) {
	for _, useLocalRoute := range []bool{false, true} {
		db := openServerTestDB(t)
		cfg := ServerConfig{DB: db, TokenStore: cloudproxy.NewTokenStore(newMemKeychain())}
		if useLocalRoute {
			cfg.ModelSettings = ensureLocalModelSettingsDB(t, db)
			cfg.LocalInference = &fakeLocalRunner{}
		}
		server := &Server{cfg: cfg}

		identity := server.resolveIdentity()
		if !identity.IsLocal() {
			t.Fatalf("local route %v: kind = %d, want identityLocal", useLocalRoute, identity.Kind)
		}
		if identity.UID != localSingleUserUID {
			t.Fatalf("local route %v: uid = %d, want localSingleUserUID = %d", useLocalRoute, identity.UID, localSingleUserUID)
		}
		if identity.Lease.Epoch() != 0 {
			t.Fatalf("local route %v: lease epoch = %d, want an empty lease", useLocalRoute, identity.Lease.Epoch())
		}
		if !errors.Is(identity.RequireCloud(), cloudproxy.ErrNoSession) {
			t.Fatalf("local route %v: RequireCloud = %v, want ErrNoSession", useLocalRoute, identity.RequireCloud())
		}
		if identity.SessionConflict() != nil {
			t.Fatalf("local route %v: signed out is not a session conflict", useLocalRoute)
		}
	}
}

// The active local account, not a constant: switching identities switches who
// the signed-out machine is.
func TestResolveIdentity_SignedOutFollowsTheActiveLocalAccount(t *testing.T) {
	db := openLocalAccountsTestDB(t)
	second, err := createLocalAccount(db, "Ming")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := selectLocalAccount(db, second.ID); err != nil {
		t.Fatalf("select: %v", err)
	}
	server := &Server{cfg: ServerConfig{DB: db, TokenStore: cloudproxy.NewTokenStore(newMemKeychain())}}

	identity := server.resolveIdentity()
	if !identity.IsLocal() || identity.UID != localAccountUID(second.ID) {
		t.Fatalf("identity = %+v, want the active local account uid %d", identity, localAccountUID(second.ID))
	}
}

// A connected account still owns its own rows: the uid is the token's subject,
// exactly as before, so nothing an account already created moves anywhere.
func TestResolveIdentity_ConnectedAccountKeepsItsCloudUID(t *testing.T) {
	db := openServerTestDB(t)
	server := &Server{cfg: ServerConfig{DB: db, TokenStore: newHistoryTokenStore(t, 42)}}

	identity := server.resolveIdentity()
	if !identity.IsCloud() || identity.UID != 42 {
		t.Fatalf("identity = %+v, want cloud uid 42", identity)
	}
	if err := identity.RequireCloud(); err != nil {
		t.Fatalf("RequireCloud on a live session: %v", err)
	}
}

// An expired refresh chain is not a cloud identity any more, but the machine's
// user has not gone anywhere. The old code answered "nobody" (the no-match
// sentinel) and left the app staring at a sign-in wall with its own local
// history hidden behind it.
func TestResolveIdentity_ExpiredSessionFallsBackToTheLocalIdentity(t *testing.T) {
	db := openServerTestDB(t)
	store := newHistoryTokenStoreWithRefreshExpiry(t, 42, time.Now().UTC().Add(-time.Minute))
	server := &Server{cfg: ServerConfig{DB: db, TokenStore: store}}

	identity := server.resolveIdentity()
	if !identity.IsLocal() || identity.UID != localSingleUserUID {
		t.Fatalf("identity = %+v, want the local identity", identity)
	}
	if !errors.Is(identity.RequireCloud(), cloudproxy.ErrNoSession) {
		t.Fatalf("RequireCloud = %v, want ErrNoSession", identity.RequireCloud())
	}
}

// The one fail-closed state. A credential that cannot be bound to a subject
// must not silently become somebody — it gets the no-match sentinel, and
// writes are refused rather than filed under a guess.
func TestResolveIdentity_UnbindableCredentialFailsClosed(t *testing.T) {
	db := openServerTestDB(t)
	store := cloudproxy.NewTokenStore(newMemKeychain())
	if err := store.Save(cloudproxy.TokenPair{
		AccessToken:      "not-a-jwt",
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	server := &Server{cfg: ServerConfig{DB: db, TokenStore: store}}

	identity := server.resolveIdentity()
	if identity.Resolved() {
		t.Fatalf("identity = %+v, want the unresolved fail-closed state", identity)
	}
	if identity.UID != noLocalHistoryUID {
		t.Fatalf("uid = %d, want the no-match sentinel %d", identity.UID, noLocalHistoryUID)
	}
}

// A boot with no session subsystem at all keeps the legacy unfiltered read.
func TestResolveIdentity_WithoutTokenStoreIsUnscoped(t *testing.T) {
	server := &Server{cfg: ServerConfig{DB: openServerTestDB(t)}}
	identity := server.resolveIdentity()
	if identity.Kind != identityUnscoped || identity.UID != 0 {
		t.Fatalf("identity = %+v, want the unscoped legacy boot", identity)
	}
}

// Connecting an account is a binding, and the binding is derived — no row, no
// migration, and disconnecting is just the absence of a token again.
func TestCloudBinding_DerivesFromTheSessionSnapshot(t *testing.T) {
	db := openServerTestDB(t)

	signedOut := &Server{cfg: ServerConfig{DB: db, TokenStore: cloudproxy.NewTokenStore(newMemKeychain())}}
	if got := signedOut.cloudBinding(); got.State != CloudBindingUnbound || got.UserID != "" {
		t.Fatalf("signed out binding = %+v, want unbound with no subject", got)
	}

	bound := &Server{cfg: ServerConfig{DB: db, TokenStore: newHistoryTokenStore(t, 987654)}}
	got := bound.cloudBinding()
	if got.State != CloudBindingBound {
		t.Fatalf("binding state = %q, want bound", got.State)
	}
	if got.UserID != "…7654" {
		t.Fatalf("binding user = %q, want the masked tail of the subject", got.UserID)
	}

	expiredStore := newHistoryTokenStoreWithRefreshExpiry(t, 987654, time.Now().UTC().Add(-time.Minute))
	expired := &Server{cfg: ServerConfig{DB: db, TokenStore: expiredStore}}
	if got := expired.cloudBinding(); got.State != CloudBindingExpired || got.UserID != "…7654" {
		t.Fatalf("expired binding = %+v, want expired with the account still named", got)
	}

	// Disconnect is the existing logout: the binding disappears, and the local
	// identity that was there all along is what remains.
	if err := expiredStore.Clear(); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if got := expired.cloudBinding(); got.State != CloudBindingUnbound {
		t.Fatalf("binding after logout = %+v, want unbound", got)
	}
	if identity := expired.resolveIdentity(); !identity.IsLocal() || identity.UID != localSingleUserUID {
		t.Fatalf("identity after logout = %+v, want the local identity still usable", identity)
	}
}

func TestMaskCloudUserID(t *testing.T) {
	for _, tc := range []struct {
		uid  uint64
		want string
	}{
		{uid: 7, want: "…7"},
		{uid: 1234, want: "…1234"},
		{uid: 918273645, want: "…3645"},
	} {
		if got := maskCloudUserID(tc.uid); got != tc.want {
			t.Fatalf("maskCloudUserID(%d) = %q, want %q", tc.uid, got, tc.want)
		}
	}
}
