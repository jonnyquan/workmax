//go:build desktop

package desktop

import (
	"errors"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	cloudproxy "server/desktop/cloud_proxy"
)

// identity.go answers one question, once: who is this request?
//
// It used to be answered twice, differently. currentAgentTurnSession (turns)
// and activeLocalHistoryUID (reads) each walked a four-branch fallback chain
// over the same TokenStore facts, and each ended its chain by asking
// shouldUseLocalRoute() — so whether you had an identity at all depended on
// whether you had configured a local model. On top of that, six handlers
// refused to run unless Proxy AND DB AND TokenStore were all wired, which made
// the local fallback inside those chains unreachable from HTTP: the sidecar
// could serve a signed-out turn in principle and answered 503 in practice.
//
// The rule is now stated in one place:
//
//   - Identity always resolves. This machine's user is somebody: the active
//     row in w_desktop_local_account. A cloud account is a BINDING on top of
//     that, not a precondition for having one.
//   - Routing decides where a turn runs. Only a request that actually leaves
//     this machine needs a cloud session, and it asks for one explicitly with
//     RequireCloud.
//
// What identity does NOT do is move a uid. localAccountUID's derivation is
// untouched, cloud requests still run as the uid in their access token, and
// nothing migrates when an account is connected or disconnected.

type identityKind uint8

const (
	// identityUnscoped: the sidecar was booted with no session subsystem at
	// all (TokenStore nil — diagnostics, legacy test boots). There is nothing
	// to scope by, so uid 0 keeps local history unfiltered exactly as before.
	identityUnscoped identityKind = iota
	// identityLocal: this machine's active local account owns the request.
	identityLocal
	// identityCloud: a connected WorkMax account with a usable session; the
	// uid is the subject of its access token, which is how every row that
	// account already owns is keyed.
	identityCloud
	// identityUnresolved: a session exists on disk but cannot be bound to one
	// positive subject, or its lease has been replaced mid-request. The single
	// fail-closed state: the uid is the no-match sentinel so no read can
	// expose another identity's rows, and writes are refused rather than
	// guessed.
	identityUnresolved
)

// errIdentityNoSubject keeps the pre-existing wire behavior for a credential
// that parses but carries no usable subject: 500 session_unavailable.
var errIdentityNoSubject = errors.New("agent turn session has no subject")

// requestIdentity is the resolved owner of one request plus, when it is a
// cloud identity, the lease that authorizes cloud work on its behalf.
type requestIdentity struct {
	Kind  identityKind
	UID   uint64
	Lease cloudproxy.SessionLease
	// cloudErr records why this is not a cloud identity. It is what
	// RequireCloud hands back, so the "no session" / "session changed" /
	// "session unavailable" distinction survives the fallback.
	cloudErr error
}

// IsCloud reports whether a connected cloud account backs this request.
func (i requestIdentity) IsCloud() bool { return i.Kind == identityCloud }

// IsLocal reports whether this machine's local account owns this request.
func (i requestIdentity) IsLocal() bool { return i.Kind == identityLocal }

// Resolved reports whether this identity may own local rows. False only for
// identityUnresolved, where guessing an owner would be worse than refusing.
func (i requestIdentity) Resolved() bool { return i.Kind != identityUnresolved }

// RequireCloud is what a handler calls at the moment it decides to send this
// request to the cloud. It never fails for a cloud identity and always fails
// with a classifiable error otherwise.
func (i requestIdentity) RequireCloud() error {
	if i.Kind == identityCloud {
		return nil
	}
	if i.cloudErr != nil {
		return i.cloudErr
	}
	return cloudproxy.ErrNoSession
}

// SessionConflict reports a session that was replaced or fenced while this
// request was resolving. Write paths check it even when they are about to run
// locally: a login committing right now is about to change which identity owns
// new rows, and writing one under the outgoing answer is how rows end up
// orphaned.
func (i requestIdentity) SessionConflict() error {
	if errors.Is(i.cloudErr, cloudproxy.ErrSessionChanged) {
		return cloudproxy.ErrSessionChanged
	}
	return nil
}

// resolveIdentity is the single fallback chain. It performs no network I/O —
// the cached TokenStore snapshot and one SQLite read — so it is safe on the
// offline, signed-out path that runs on every history poll.
func (s *Server) resolveIdentity() requestIdentity {
	return resolveIdentityFrom(s.cfg.TokenStore, s.cfg.DB)
}

// resolveIdentityFrom is resolveIdentity without a Server. It exists because
// the local inference engines are constructed before the Server is — they need
// to know whose API key to read at the moment a turn runs, and the answer must
// be the same one every HTTP handler gets. Two implementations of "who is
// this" is exactly the duplication this file was written to end.
func resolveIdentityFrom(tokenStore *cloudproxy.TokenStore, db *gorm.DB) requestIdentity {
	if tokenStore == nil {
		return requestIdentity{
			Kind:     identityUnscoped,
			UID:      0,
			cloudErr: cloudproxy.ErrNoSession,
		}
	}
	snapshot, err := tokenStore.GetSnapshot()
	if err != nil {
		// ErrNoSession (signed out), ErrSessionChanged (a login or logout is
		// committing), a corrupt entry, a locked Keychain — none of them mean
		// "nobody is using this computer". They mean there is no cloud account
		// to run as right now.
		return localIdentityFor(db, err)
	}
	if snapshot.Pair.AccessToken == "" || snapshot.Pair.IsRefreshExpired(time.Now().UTC()) {
		return localIdentityFor(db, cloudproxy.ErrNoSession)
	}
	uid, err := cloudproxy.ExtractUIDFromAccessToken(snapshot.Pair.AccessToken)
	if err != nil || uid == 0 {
		return requestIdentity{
			Kind:     identityUnresolved,
			UID:      noLocalHistoryUID,
			cloudErr: errIdentityNoSubject,
		}
	}
	if err := snapshot.Lease.Check(); err != nil {
		return requestIdentity{
			Kind:     identityUnresolved,
			UID:      noLocalHistoryUID,
			cloudErr: err,
		}
	}
	return requestIdentity{
		Kind:  identityCloud,
		UID:   uint64(uid),
		Lease: snapshot.Lease,
	}
}

// requestOwner is what a handler calls when it needs to know whose rows it may
// touch. It answers for every ordinary state — connected account, signed-out
// machine, expired credential — and writes a closed error response only for
// the two it must not guess: an unbindable session, and one replaced while
// this request was resolving.
func (s *Server) requestOwner(c *gin.Context) (requestIdentity, bool) {
	identity := s.resolveIdentity()
	if !identity.Resolved() {
		s.writeAgentTurnSessionError(c, identity.RequireCloud())
		return requestIdentity{}, false
	}
	if identity.SessionConflict() != nil {
		writeSessionChanged(c)
		return requestIdentity{}, false
	}
	return identity, true
}

// localIdentity is the machine's own user: the active local account, or the
// reserved single-user uid if account bookkeeping is unavailable. Never fails
// — an accounts problem must not lock anyone out of their own data.
func (s *Server) localIdentity(cause error) requestIdentity {
	return localIdentityFor(s.cfg.DB, cause)
}

func localIdentityFor(db *gorm.DB, cause error) requestIdentity {
	return requestIdentity{
		Kind:     identityLocal,
		UID:      activeLocalAccountUID(db),
		cloudErr: cause,
	}
}

// Cloud binding states as the renderer sees them. Derived entirely from the
// TokenStore snapshot — connecting an account stores no extra row, so
// disconnecting removes nothing and there is no migration either way.
const (
	CloudBindingUnbound = "unbound"
	CloudBindingBound   = "bound"
	CloudBindingExpired = "expired"
)

// CloudBinding is "is this local identity connected to a WorkMax account, and
// which one" in the only form that can be answered offline.
type CloudBinding struct {
	State string `json:"state"`
	// UserID is the connected account's subject, masked to its last four
	// digits. Enough to tell two accounts apart on one machine; not an
	// identifier worth copying out of a UI.
	UserID string `json:"user_id,omitempty"`
}

func (s *Server) cloudBinding() CloudBinding {
	if s.cfg.TokenStore == nil {
		return CloudBinding{State: CloudBindingUnbound}
	}
	pair, err := s.cfg.TokenStore.Get()
	if err != nil || pair == nil || pair.AccessToken == "" {
		return CloudBinding{State: CloudBindingUnbound}
	}
	binding := CloudBinding{State: CloudBindingBound}
	if uid, uidErr := cloudproxy.ExtractUIDFromAccessToken(pair.AccessToken); uidErr == nil && uid != 0 {
		binding.UserID = maskCloudUserID(uint64(uid))
	}
	if pair.IsRefreshExpired(time.Now().UTC()) {
		// Still bound — the account is remembered and the row it owns is
		// still its own — but it cannot authorize anything until it signs in
		// again. Calling this "unbound" would tell the user their account is
		// gone; calling it "bound" would offer cloud work that cannot run.
		binding.State = CloudBindingExpired
	}
	return binding
}

func maskCloudUserID(uid uint64) string {
	text := strconv.FormatUint(uid, 10)
	if len(text) > 4 {
		text = text[len(text)-4:]
	}
	return "…" + text
}
