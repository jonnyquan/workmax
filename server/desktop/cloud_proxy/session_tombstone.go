//go:build desktop

package cloud_proxy

import "errors"

// ErrSessionStateUnavailable is returned when the durable, non-secret session
// state cannot be read safely. Callers must treat it as unauthenticated: the
// TokenStore never falls through to Keychain credentials after this error.
var ErrSessionStateUnavailable = errors.New("token_store: session state unavailable")

// ErrSessionPersistence is the closed error returned when a Keychain or
// durable tombstone mutation does not complete. Underlying persistence errors
// are deliberately not exposed because platform credential-store diagnostics
// can contain user-specific data.
var ErrSessionPersistence = errors.New("token_store: session persistence unavailable")

// SessionTombstoneMarker persists one non-sensitive bit of authority outside
// Keychain. A marked state means "do not accept any Keychain session".
//
// TokenStore uses the marker as the first and last step of every credential
// mutation:
//
//  1. Mark before touching Keychain (a crash is therefore fail-closed).
//  2. Write the new credential pair to Keychain.
//  3. Unmark only after that write succeeds.
//
// Implementations must make Mark and Unmark idempotent. They must never store
// token material or other secrets; the production implementation stores only
// a fixed key/value in Desktop's local SQLite metadata table.
type SessionTombstoneMarker interface {
	IsMarked() (bool, error)
	Mark() error
	Unmark() error
}
