//go:build desktop

package cloud_proxy

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
)

// PKCEVerifierBytes is the byte length of the random verifier before
// base64url encoding. 32 bytes = 256 bits → 43 chars base64url, the
// minimum length RFC 7636 §4.1 accepts.
const PKCEVerifierBytes = 32

// StateBytes is the byte length of the OAuth `state` nonce used for
// CSRF protection. 16 bytes = 128 bits is plenty; longer just bloats
// the redirect URL.
const StateBytes = 16

// PKCEPair holds the verifier + challenge a single OAuth flow needs.
// Verifier stays in sidecar memory until the /token exchange; only
// the challenge ever leaves the process (in the authorize URL).
type PKCEPair struct {
	Verifier  string
	Challenge string
	Method    string // always "S256" — `plain` is deliberately not supported
}

// GeneratePKCE returns a fresh (verifier, challenge) pair backed by
// the given crypto source. Production callers pass crypto/rand.Reader;
// tests inject a deterministic byte stream so the pair is predictable.
func GeneratePKCE(src io.Reader) (PKCEPair, error) {
	b := make([]byte, PKCEVerifierBytes)
	if _, err := io.ReadFull(src, b); err != nil {
		return PKCEPair{}, fmt.Errorf("pkce gen: %w", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(b)

	h := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(h[:])

	return PKCEPair{
		Verifier:  verifier,
		Challenge: challenge,
		Method:    "S256",
	}, nil
}

// GenerateState returns a fresh state nonce (CSRF protection for the
// OAuth flow). 16 random bytes base64url-encoded → 22 chars, well
// within URL-length budgets.
func GenerateState(src io.Reader) (string, error) {
	b := make([]byte, StateBytes)
	if _, err := io.ReadFull(src, b); err != nil {
		return "", fmt.Errorf("state gen: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// DefaultRandom returns crypto/rand.Reader — the production source.
// Lifted to a package-level seam so tests can substitute via
// callsite injection.
func DefaultRandom() io.Reader { return rand.Reader }
