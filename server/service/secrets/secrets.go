// Package secrets provides at-rest encryption helpers for
// sensitive columns (MCP connector env / headers in Phase B3;
// future surfaces: integration API keys, third-party OAuth
// tokens, etc.).
//
// Scheme: AES-GCM with a single platform-managed master key.
// Each ciphertext carries its own random nonce so two encrypts
// of the same plaintext produce distinct ciphertexts (no
// equality leak).
//
// On-disk format (all base64-url, no padding, separator ":"):
//
//	v1:<nonce-12B>:<ciphertext+tag>
//
// The "v1:" version prefix is a forward seam — a future Phase
// B3.2 that rotates to ChaCha20-Poly1305 or AES-SIV writes "v2:"
// and reads continue to honour "v1:" rows until backfill. Today
// only v1 ships.
//
// Master key sourcing: WORKMAX_SECRETS_KEY env var, base64-url
// encoded, exactly 32 bytes after decode (AES-256). The package
// loads it once at first use (sync.Once + memoization). A
// missing / malformed key returns an error from every Encrypt /
// Decrypt call — the caller decides whether that's fatal (it
// usually is for production data) or whether to degrade.
//
// Test posture: SetKeyForTesting(rawBytes) overrides the
// memoized key for unit tests; production code never calls it.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
)

const (
	// scheme version tag prepended to every ciphertext. Lets
	// future migrations swap algorithms without breaking
	// existing rows.
	schemeV1 = "v1"

	// envKey names the OS environment variable carrying the
	// master key. base64-url encoded, 32 bytes after decode.
	envKey = "WORKMAX_SECRETS_KEY"

	// expectedKeyLen — AES-256 requires exactly 32 bytes. We
	// reject longer / shorter inputs at load time instead of
	// silently truncating.
	expectedKeyLen = 32
)

var (
	keyOnce      sync.Once
	cachedAEAD   cipher.AEAD
	cachedErr    error
	testOverride []byte
)

// SetKeyForTesting installs a fixed 32-byte key for unit tests.
// Resets the memoization so a test that ran first doesn't lock
// out a later test with a different key. Production code MUST
// NOT call this — it bypasses the env-var contract.
func SetKeyForTesting(key []byte) {
	if len(key) != expectedKeyLen {
		panic(fmt.Sprintf("SetKeyForTesting: expected %d bytes, got %d", expectedKeyLen, len(key)))
	}
	testOverride = append([]byte(nil), key...)
	// Reset memoization so the next loadAEAD picks up the test
	// key. sync.Once doesn't support reset; replace it.
	keyOnce = sync.Once{}
	cachedAEAD = nil
	cachedErr = nil
}

// ClearKeyForTesting removes the test override + resets
// memoization. Call from t.Cleanup to keep tests isolated.
func ClearKeyForTesting() {
	testOverride = nil
	keyOnce = sync.Once{}
	cachedAEAD = nil
	cachedErr = nil
}

// loadAEAD returns the AES-GCM AEAD instance bound to the
// configured master key. Memoized — first call decodes the env
// var, subsequent calls reuse the cached AEAD. Errors are also
// memoized so a misconfigured deploy doesn't repeatedly retry
// the same broken key on every write.
func loadAEAD() (cipher.AEAD, error) {
	keyOnce.Do(func() {
		var keyBytes []byte
		if testOverride != nil {
			keyBytes = testOverride
		} else {
			raw := strings.TrimSpace(os.Getenv(envKey))
			if raw == "" {
				cachedErr = fmt.Errorf("secrets: %s is not set", envKey)
				return
			}
			decoded, err := base64.RawURLEncoding.DecodeString(raw)
			if err != nil {
				// Try standard base64 as a fallback — some ops
				// teams configure with padded base64; both flavors
				// produce the same bytes.
				decoded, err = base64.StdEncoding.DecodeString(raw)
				if err != nil {
					cachedErr = fmt.Errorf("secrets: %s is not valid base64: %w", envKey, err)
					return
				}
			}
			if len(decoded) != expectedKeyLen {
				cachedErr = fmt.Errorf("secrets: %s decoded to %d bytes, expected %d", envKey, len(decoded), expectedKeyLen)
				return
			}
			keyBytes = decoded
		}

		block, err := aes.NewCipher(keyBytes)
		if err != nil {
			cachedErr = fmt.Errorf("secrets: AES init: %w", err)
			return
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			cachedErr = fmt.Errorf("secrets: GCM init: %w", err)
			return
		}
		cachedAEAD = aead
	})
	return cachedAEAD, cachedErr
}

// ValidateConfiguration resolves and validates the configured master key
// without encrypting application data. Security-sensitive services call this
// at construction time so a missing key fails startup/readiness rather than
// surfacing as the first user's 500 response.
func ValidateConfiguration() error {
	_, err := loadAEAD()
	return err
}

// Encrypt wraps plaintext with v1 AES-GCM. Returns the on-disk
// format ("v1:<nonce>:<ciphertext>"). Two calls with identical
// plaintext produce different outputs because the nonce is
// random per call — equality probing against the ciphertext
// reveals nothing about the plaintext.
//
// Empty plaintext (len == 0) encrypts to a valid v1 envelope
// (zero-length plaintext is a legitimate value, e.g. an
// empty map serialized to "{}"). Callers that mean "no value"
// should pass nil and short-circuit at their layer.
func Encrypt(plaintext []byte) (string, error) {
	aead, err := loadAEAD()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("secrets: nonce: %w", err)
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, nil)
	return schemeV1 + ":" +
		base64.RawURLEncoding.EncodeToString(nonce) + ":" +
		base64.RawURLEncoding.EncodeToString(ciphertext), nil
}

// Decrypt is the inverse. Rejects ciphertexts with unknown
// version prefixes (future-proofs the v2 / v3 paths) and
// returns a single sentinel error for tampering / wrong key /
// truncated input — callers don't need to distinguish the three
// (they all mean "this row is unreadable").
func Decrypt(envelope string) ([]byte, error) {
	if envelope == "" {
		return nil, errors.New("secrets: empty envelope")
	}
	parts := strings.SplitN(envelope, ":", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("secrets: envelope malformed (expected 3 parts, got %d)", len(parts))
	}
	if parts[0] != schemeV1 {
		return nil, fmt.Errorf("secrets: unknown scheme version %q", parts[0])
	}
	nonce, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("secrets: nonce decode: %w", err)
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("secrets: ciphertext decode: %w", err)
	}
	aead, err := loadAEAD()
	if err != nil {
		return nil, err
	}
	if len(nonce) != aead.NonceSize() {
		return nil, fmt.Errorf("secrets: nonce length %d, expected %d", len(nonce), aead.NonceSize())
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		// Don't echo the underlying error — GCM's "message
		// authentication failed" leaks scheme detail to a
		// caller that's already over the wire.
		return nil, errors.New("secrets: decrypt failed (wrong key or tampered)")
	}
	return plaintext, nil
}

// IsEnvelope returns true when the string looks like a v1
// ciphertext envelope. Used by the model's BeforeSave hook to
// avoid double-encrypting a value that's already encrypted
// (idempotent writes: Save() of an unmodified row should not
// re-wrap the existing ciphertext under a fresh nonce).
func IsEnvelope(s string) bool {
	return strings.HasPrefix(s, schemeV1+":") && strings.Count(s, ":") == 2
}
