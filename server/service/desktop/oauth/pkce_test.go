package oauth

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// Helper: build a valid (verifier, challenge) pair for happy-path tests.
func validPair(t *testing.T) (verifier, challenge string) {
	t.Helper()
	// RFC 7636 example-style verifier (43 chars from the unreserved set).
	verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])
	return verifier, challenge
}

func TestVerifyPKCE_Happy(t *testing.T) {
	verifier, challenge := validPair(t)
	if err := VerifyPKCE(verifier, challenge, "S256"); err != nil {
		t.Fatalf("expected nil error on happy path, got %v", err)
	}
}

func TestVerifyPKCE_RejectsPlainMethod(t *testing.T) {
	verifier, challenge := validPair(t)
	err := VerifyPKCE(verifier, challenge, "plain")
	if !errors.Is(err, ErrPKCEMethodUnsupported) {
		t.Errorf("plain method should yield ErrPKCEMethodUnsupported, got %v", err)
	}
	// And `verifier` as the literal stored challenge (which is how
	// `plain` would work) must NOT accidentally pass via the S256
	// path either — defense in depth.
	err = VerifyPKCE(verifier, verifier, "S256")
	if !errors.Is(err, ErrPKCEVerifierMismatch) {
		t.Errorf("S256 with verifier-as-challenge should fail, got %v", err)
	}
}

func TestVerifyPKCE_RejectsWrongVerifier(t *testing.T) {
	_, challenge := validPair(t)
	wrong := "wrong-verifier-still-43-chars-long-padded-x"
	if len(wrong) < PKCEVerifierMinLen {
		t.Fatalf("test bug: wrong verifier is too short (%d)", len(wrong))
	}
	err := VerifyPKCE(wrong, challenge, "S256")
	if !errors.Is(err, ErrPKCEVerifierMismatch) {
		t.Errorf("wrong verifier should yield ErrPKCEVerifierMismatch, got %v", err)
	}
}

func TestVerifyPKCE_RejectsBadLength(t *testing.T) {
	_, challenge := validPair(t)
	cases := []struct {
		name     string
		verifier string
	}{
		{"empty", ""},
		{"42 chars (one below min)", strings.Repeat("a", PKCEVerifierMinLen-1)},
		{"129 chars (one above max)", strings.Repeat("a", PKCEVerifierMaxLen+1)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := VerifyPKCE(c.verifier, challenge, "S256")
			if !errors.Is(err, ErrPKCEVerifierLength) {
				t.Errorf("len=%d should yield ErrPKCEVerifierLength, got %v", len(c.verifier), err)
			}
		})
	}
}

func TestVerifyPKCE_BoundaryLengths(t *testing.T) {
	// Verifiers at exactly min and max length should be accepted
	// (length-wise) — they'll fail mismatch since they aren't the
	// right pre-image, but they should NOT trip ErrPKCEVerifierLength.
	_, challenge := validPair(t)
	for _, l := range []int{PKCEVerifierMinLen, PKCEVerifierMaxLen} {
		v := strings.Repeat("z", l)
		err := VerifyPKCE(v, challenge, "S256")
		if errors.Is(err, ErrPKCEVerifierLength) {
			t.Errorf("len=%d at boundary should not be length-rejected, got %v", l, err)
		}
	}
}
