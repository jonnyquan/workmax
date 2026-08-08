//go:build desktop

// Package cloud_proxy is the sidecar's bridge to the configured Go Server. It
// owns OAuth state, persisted credentials, and the SSE proxying for
// /agent/chat.
//
// Platform design:
//
//	ProjectDocs/writer-work-agent-plugin-platform-design-2026-08.md
package cloud_proxy

import "errors"

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
