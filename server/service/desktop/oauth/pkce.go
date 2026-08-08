// Package oauth implements the OAuth Authorization Server service
// layer that sits behind the /api/desktop/oauth/* handlers added in
// P-1.4 onwards. The data shapes it manipulates live in
// server/model/desktop/oauth.
//
// Platform design:
//
//	ProjectDocs/writer-work-agent-plugin-platform-design-2026-08.md
package oauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
)

// PKCE verifier length bounds (RFC 7636 §4.1).
const (
	PKCEVerifierMinLen = 43
	PKCEVerifierMaxLen = 128
)

// PKCE-related errors. Callers should compare with errors.Is, not by
// string — the wire-level OAuth error code (`invalid_grant`) is
// constructed by the /token handler from these sentinels.
var (
	ErrPKCEMethodUnsupported = errors.New("pkce: unsupported code_challenge_method (only S256 accepted)")
	ErrPKCEVerifierLength    = errors.New("pkce: code_verifier length must be 43-128 chars")
	ErrPKCEVerifierMismatch  = errors.New("pkce: verifier does not match stored challenge")
)

// VerifyPKCE checks `verifier` against `storedChallenge` under the
// given `method`. Returns nil on a successful match.
//
// We deliberately accept only S256. RFC 7636 also defines `plain`,
// but `plain` defeats the entire point of PKCE for a public client
// (an attacker who steals the auth code can replay it). The OAuth
// /authorize handler refuses requests with `code_challenge_method`
// other than S256 before we ever reach this function — this is
// belt-and-suspenders for the /token consume path.
//
// Comparison is constant-time (subtle.ConstantTimeCompare) so a
// timing oracle can't be used to brute-force the challenge byte by
// byte.
func VerifyPKCE(verifier, storedChallenge, method string) error {
	if method != "S256" {
		return ErrPKCEMethodUnsupported
	}
	if l := len(verifier); l < PKCEVerifierMinLen || l > PKCEVerifierMaxLen {
		return ErrPKCEVerifierLength
	}
	h := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(h[:])
	if subtle.ConstantTimeCompare([]byte(computed), []byte(storedChallenge)) != 1 {
		return ErrPKCEVerifierMismatch
	}
	return nil
}
