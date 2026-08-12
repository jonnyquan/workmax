//go:build desktop

package cloud_proxy

import (
	"strings"
	"testing"
)

// lookupOf builds an os.LookupEnv stand-in over a fixed map, so the resolver's
// "set but empty" branch is testable — os.Getenv cannot tell that apart from
// unset, and the two mean opposite things here.
func lookupOf(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestResolveKeychainService_UnsetKeepsTheShippingDefault(t *testing.T) {
	got, err := ResolveKeychainService(lookupOf(nil))
	if err != nil {
		t.Fatalf("unset override must not error: %v", err)
	}
	if got != KeychainService {
		t.Fatalf("service = %q, want the default %q", got, KeychainService)
	}
	if KeychainService != "ai.workmax.desktop" {
		t.Fatalf("the default name is user-visible in Keychain Access and must not drift: %q", KeychainService)
	}
}

func TestResolveKeychainService_OverrideTakesEffect(t *testing.T) {
	for _, want := range []string{
		"ai.workmax.desktop.smoke-l2-pi.9f2a",
		"workmax_test",
		"WorkMax-Dev.2",
		"a",
		strings.Repeat("x", maxKeychainServiceBytes),
	} {
		got, err := ResolveKeychainService(lookupOf(map[string]string{KeychainServiceEnv: want}))
		if err != nil {
			t.Fatalf("%q must be accepted: %v", want, err)
		}
		if got != want {
			t.Fatalf("service = %q, want %q", got, want)
		}
	}
}

func TestResolveKeychainService_RejectsUnusableValues(t *testing.T) {
	// Set-but-empty is listed first on purpose: it is the one an env-var typo
	// produces, and falling back to the default for it would silently aim an
	// isolated run at the user's real entries.
	cases := map[string]string{
		"empty":            "",
		"whitespace only":  "   ",
		"leading hyphen":   "-s",
		"embedded newline": "ai.workmax\nfoo",
		"embedded CR":      "ai.workmax\rfoo",
		"embedded NUL":     "ai.workmax\x00foo",
		"embedded space":   "ai workmax",
		"quote":            `ai."workmax`,
		"shell metachar":   "ai.workmax;rm -rf /",
		"colon":            "ai.workmax:smoke",
		"slash":            "ai/workmax",
		"too long":         strings.Repeat("x", maxKeychainServiceBytes+1),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ResolveKeychainService(lookupOf(map[string]string{KeychainServiceEnv: raw}))
			if err == nil {
				t.Fatalf("%q must be rejected, resolved to %q", raw, got)
			}
			if got != "" {
				t.Fatalf("a rejected override must resolve to nothing, got %q", got)
			}
			if !strings.Contains(err.Error(), KeychainServiceEnv) {
				t.Fatalf("the error must name the variable to fix: %v", err)
			}
			// The value itself never appears in the message: it may carry a
			// newline or a control byte, and a log line is a poor place to
			// paste one.
			if strings.Contains(err.Error(), raw) && raw != "" {
				t.Fatalf("the rejected value must not be echoed back: %v", err)
			}
		})
	}
}

func TestKeychainServiceName_ReadsTheProcessEnvironment(t *testing.T) {
	if got := KeychainServiceName(); got != KeychainService {
		t.Fatalf("with no override the name must be the default, got %q", got)
	}
	t.Setenv(KeychainServiceEnv, "ai.workmax.desktop.test")
	if got := KeychainServiceName(); got != "ai.workmax.desktop.test" {
		t.Fatalf("the override must reach every caller, got %q", got)
	}
	// Fail closed: a malformed override yields a name the adapters refuse,
	// never the real one.
	t.Setenv(KeychainServiceEnv, "not a service name")
	if got := KeychainServiceName(); got != "" {
		t.Fatalf("a malformed override must not fall back to a usable name, got %q", got)
	}
}

func TestNewTokenStore_UsesTheResolvedNamespace(t *testing.T) {
	t.Setenv(KeychainServiceEnv, "ai.workmax.desktop.tokenstore-test")
	if got := NewTokenStore(newMemKeychain()).service; got != "ai.workmax.desktop.tokenstore-test" {
		t.Fatalf("the session slot must follow the override, got %q", got)
	}
}
