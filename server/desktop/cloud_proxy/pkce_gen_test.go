//go:build desktop

package cloud_proxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

func TestGeneratePKCE_Happy(t *testing.T) {
	src := bytes.NewReader([]byte("0123456789abcdefghijklmnopqrstuv")) // 32 bytes
	p, err := GeneratePKCE(src)
	if err != nil {
		t.Fatalf("GeneratePKCE: %v", err)
	}
	if p.Method != "S256" {
		t.Errorf("Method: got %q, want S256", p.Method)
	}
	// Verifier is base64url(32 bytes) = 43 chars.
	if len(p.Verifier) != 43 {
		t.Errorf("Verifier length: got %d, want 43", len(p.Verifier))
	}
	// Challenge MUST equal base64url(SHA256(verifier)). This is the
	// roundtrip the backend's VerifyPKCE will check at /token time.
	h := sha256.Sum256([]byte(p.Verifier))
	want := base64.RawURLEncoding.EncodeToString(h[:])
	if p.Challenge != want {
		t.Errorf("Challenge: got %q, want %q", p.Challenge, want)
	}
}

func TestGeneratePKCE_ShortSource(t *testing.T) {
	src := bytes.NewReader([]byte("only-12-bytes"))
	_, err := GeneratePKCE(src)
	if err == nil {
		t.Error("expected error on short random source")
	}
}

func TestGenerateState_Length(t *testing.T) {
	src := bytes.NewReader([]byte("0123456789abcdef")) // 16 bytes
	s, err := GenerateState(src)
	if err != nil {
		t.Fatalf("GenerateState: %v", err)
	}
	// 16 bytes → base64url no pad → 22 chars.
	if len(s) != 22 {
		t.Errorf("state length: got %d, want 22", len(s))
	}
}

func TestGeneratePKCE_TwoCalls_DifferentValues(t *testing.T) {
	src := bytes.NewReader([]byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaabbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"))
	p1, _ := GeneratePKCE(src)
	p2, _ := GeneratePKCE(src)
	if p1.Verifier == p2.Verifier {
		t.Error("consecutive PKCE pairs from the same source must differ")
	}
	if p1.Challenge == p2.Challenge {
		t.Error("consecutive PKCE challenges must differ")
	}
}
