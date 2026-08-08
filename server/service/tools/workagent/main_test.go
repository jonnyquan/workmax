package workagent

import (
	"os"
	"testing"

	"server/service/secrets"
)

// TestMain installs the deterministic master key the Phase B3
// encrypted-column tests need. The MCP connector bridge tests
// (mcp_connector_bridge_test.go) round-trip Env / Headers
// through Encrypt + Decrypt; without a configured key, every
// such test fails at Scan() with "WORKMAX_SECRETS_KEY is not set."
//
// Production loads the key from the env var; tests bypass via
// the package's SetKeyForTesting hook. ClearKeyForTesting runs
// after the suite to keep parallel-test isolation across
// packages.
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
