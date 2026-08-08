//go:build desktop && !darwin

package cloud_proxy

import "errors"

// On non-darwin desktop builds the keychain is not yet implemented.
// Constructor returns a value whose methods all error out — callers
// who try to log in will see a clear failure rather than a nil
// pointer panic.
//
// Windows (Credential Manager) and Linux (libsecret / keyring
// fallback) are intentionally deferred until product commits to
// non-mac desktop builds.

type stubKeychain struct{}

// NewDarwinKeychain shares the name with the macOS variant so
// platform-conditional callers don't need build-tag matrices. On
// non-darwin this returns the stub.
func NewDarwinKeychain() Keychain { return stubKeychain{} }

var errNotImplemented = errors.New("keychain: not implemented on this OS (macOS-only desktop build; Win/Linux deferred)")

func (stubKeychain) Write(service, account string, value []byte) error {
	return errNotImplemented
}

func (stubKeychain) Read(service, account string) ([]byte, error) {
	return nil, errNotImplemented
}

func (stubKeychain) Delete(service, account string) error {
	return errNotImplemented
}
