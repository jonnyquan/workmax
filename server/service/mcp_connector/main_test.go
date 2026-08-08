package mcp_connector

import (
	"os"
	"testing"

	"server/service/secrets"
)

// TestMain installs a deterministic master key for the
// encryption-aware tests. Production loads the key from
// $WORKMAX_SECRETS_KEY; tests bypass that via the package's
// SetKeyForTesting hook (panics on wrong-size input, so we
// pass a fixed 32-byte slice).
//
// All bytes set to 0x42 — a recognizable pattern that screams
// "test, not production" if it ever shows up in a real
// ciphertext audit.
func TestMain(m *testing.M) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = 0x42
	}
	secrets.SetKeyForTesting(key)
	code := m.Run()
	secrets.ClearKeyForTesting()
	os.Exit(code)
}
