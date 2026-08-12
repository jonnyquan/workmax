//go:build desktop

// Package cloud_proxy is the sidecar's bridge to the configured Go Server. It
// owns OAuth state, persisted credentials, and the SSE proxying for
// /agent/chat.
//
// Platform design:
//
//	ProjectDocs/writer-work-agent-plugin-platform-design-2026-08.md
package cloud_proxy

import (
	"errors"
	"fmt"
	"os"
	"regexp"
)

// ErrKeychainNoEntry is returned by Keychain.Read when the requested
// service+account pair doesn't exist (vs other errors like
// permission denied). Callers treat this as "user not logged in
// yet" — the most common branch.
var ErrKeychainNoEntry = errors.New("keychain: entry not found")

// Keychain abstracts the platform-specific OS credential store the
// sidecar uses to persist OAuth tokens. macOS uses the Security
// framework via the `security` CLI. Non-macOS implementations are
// intentionally deferred until product commits to those desktop targets.
//
// The contract is tiny on purpose: write/read/delete a single
// service+account pair. Token serialization (TokenPair JSON) lives
// in token_store.go, one level up.
type Keychain interface {
	// Write stores `value` under (service, account), replacing an existing
	// entry. Callers must handle an ambiguous failure: platform adapters are not
	// required to provide a cross-process atomic replace. The bytes are typically
	// a JSON-encoded TokenPair.
	Write(service, account string, value []byte) error

	// Read returns the bytes previously written under (service,
	// account). Returns ErrKeychainNoEntry when the pair doesn't
	// exist; any other error indicates a real failure (permission,
	// corruption, etc).
	Read(service, account string) ([]byte, error)

	// Delete removes the entry. Idempotent — deleting a missing
	// entry returns nil, not ErrKeychainNoEntry.
	Delete(service, account string) error
}

// Constants for the per-workmax-app slot we write into. Kept here so
// both production code and tests reference the same strings.
const (
	// KeychainService is the service identifier visible in
	// macOS Keychain Access. Single per app.
	KeychainService = "ai.workmax.desktop"

	// KeychainAccount is the account name. We have exactly one
	// session per device (one logged-in workmax user at a time), so
	// a fixed account is fine.
	KeychainAccount = "session"
)

// KeychainServiceEnv names the service the process writes under, replacing
// KeychainService for its lifetime. Unset — the shipping default, and what
// every user runs — keeps KeychainService exactly.
//
// It exists because the service name is the ONLY thing separating one
// installation's secrets from another's. Everything else about a run can be
// isolated with WORKMAX_DESKTOP_DATA_DIR: SQLite, workspaces, pi's config. The
// Keychain cannot, because the accounts are derived from the uid and uids are
// per-data-dir counters that every fresh data dir restarts at the same number
// — so a throwaway run and the real app agree on the account and collide on it.
//
// That collision was survivable only while DarwinKeychain.Write stored the
// empty string (fixed in 1855d91: security(1)'s prompt reads the value twice
// and we answered once). With writes working, an isolated run now REPLACES the
// user's real local-model key and OAuth session. The smoke scripts set this so
// they cannot.
const KeychainServiceEnv = "WORKMAX_KEYCHAIN_SERVICE"

// maxKeychainServiceBytes bounds the override. macOS itself is far more
// permissive; the point is that an entry we create must stay findable and
// deletable by a human in Keychain Access, and a kilobyte of service name is
// neither.
const maxKeychainServiceBytes = 96

// keychainServicePattern is what a service name may be made of. Deliberately
// narrower than macOS allows: the value is passed as an argv element to
// security(1), so a leading "-" (an option), whitespace, quotes, NUL and
// newlines are all refused rather than reasoned about. Accounts are NOT checked
// against this — they are ours, and they carry ":" by construction.
var keychainServicePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// validateKeychainService reports why a service name is unusable, or nil.
func validateKeychainService(service string) error {
	switch {
	case service == "":
		return errors.New("must not be empty")
	case len(service) > maxKeychainServiceBytes:
		return fmt.Errorf("must be at most %d bytes", maxKeychainServiceBytes)
	case !keychainServicePattern.MatchString(service):
		return errors.New("must be letters, digits, '.', '_' or '-', and start with a letter or digit")
	}
	return nil
}

// ResolveKeychainService decides the namespace from an environment lookup
// (os.LookupEnv in production; a map in tests). An unset variable yields the
// default. A variable that is set but malformed — including set to the empty
// string — is an error, never a silent fall back to the default: falling back
// would aim an isolated run at the real app's entries, which is the one outcome
// the override exists to prevent.
func ResolveKeychainService(lookup func(string) (string, bool)) (string, error) {
	raw, set := lookup(KeychainServiceEnv)
	if !set {
		return KeychainService, nil
	}
	if err := validateKeychainService(raw); err != nil {
		// The rejected value is not echoed: it is attacker-shaped by
		// definition here, and a log line is a poor place to paste one.
		return "", fmt.Errorf("%s: %w", KeychainServiceEnv, err)
	}
	return raw, nil
}

// KeychainServiceName is the service every production caller passes to a
// Keychain. Returns "" when the override is malformed, which the platform
// adapters refuse — failing closed, so a misconfigured run touches nothing
// rather than touching the user's real slot. Bootstrap refuses to start on the
// same condition, with the sentence that explains it.
func KeychainServiceName() string {
	service, err := ResolveKeychainService(os.LookupEnv)
	if err != nil {
		return ""
	}
	return service
}
