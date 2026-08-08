package secrets

import (
	"strings"
	"testing"
)

func setupTestKey(t *testing.T) {
	t.Helper()
	key := make([]byte, expectedKeyLen)
	for i := range key {
		key[i] = 0x42
	}
	SetKeyForTesting(key)
	t.Cleanup(ClearKeyForTesting)
}

func TestEncrypt_DecryptRoundTrip(t *testing.T) {
	setupTestKey(t)
	plaintext := []byte("hello world")
	envelope, err := Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	got, err := Decrypt(envelope)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("round-trip lost: got %q", got)
	}
}

func TestEncrypt_TwoCallsProduceDistinctOutputs(t *testing.T) {
	// Nonce randomness — equal plaintext must NOT produce equal
	// ciphertext, otherwise an attacker with DB read can
	// fingerprint which rows share secrets.
	setupTestKey(t)
	a, _ := Encrypt([]byte("same"))
	b, _ := Encrypt([]byte("same"))
	if a == b {
		t.Error("two encrypts of same plaintext produced equal ciphertext (nonce reuse?)")
	}
}

func TestEncrypt_EmptyPlaintext(t *testing.T) {
	// Empty plaintext is legal (e.g. an empty JSON map "{}"
	// would marshal to 2 bytes; but a truly empty input is a
	// non-error path that decrypts to []byte{}).
	setupTestKey(t)
	envelope, err := Encrypt([]byte{})
	if err != nil {
		t.Fatalf("Encrypt empty: %v", err)
	}
	got, err := Decrypt(envelope)
	if err != nil {
		t.Fatalf("Decrypt empty: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty round-trip yielded %d bytes, want 0", len(got))
	}
}

func TestDecrypt_RejectsTamperedCiphertext(t *testing.T) {
	setupTestKey(t)
	envelope, _ := Encrypt([]byte("secret"))
	// Flip a byte in the ciphertext payload.
	tampered := envelope[:len(envelope)-2] + "XY"
	_, err := Decrypt(tampered)
	if err == nil {
		t.Error("tampered ciphertext should fail decrypt")
	}
	// Error message must NOT leak which part is wrong — see
	// the comment in Decrypt about scheme leak.
	if !strings.Contains(err.Error(), "decrypt failed") {
		t.Errorf("expected generic 'decrypt failed' message, got %q", err.Error())
	}
}

func TestDecrypt_RejectsWrongKey(t *testing.T) {
	setupTestKey(t)
	envelope, _ := Encrypt([]byte("secret"))

	// Switch to a different key.
	other := make([]byte, expectedKeyLen)
	for i := range other {
		other[i] = 0x55
	}
	SetKeyForTesting(other)
	defer ClearKeyForTesting()

	_, err := Decrypt(envelope)
	if err == nil {
		t.Error("wrong key should fail decrypt")
	}
}

func TestDecrypt_RejectsMalformedEnvelopes(t *testing.T) {
	setupTestKey(t)
	cases := []string{
		"",                             // empty
		"v1",                           // missing parts
		"v1:onlytwo",                   // only 2 parts
		"v9:abc:def",                   // unknown scheme
		"v1:not-base64!:def",           // bad nonce
		"v1:" + "validbase64==" + ":!", // bad ciphertext (mismatched format)
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			_, err := Decrypt(c)
			if err == nil {
				t.Errorf("malformed envelope %q should error", c)
			}
		})
	}
}

func TestIsEnvelope(t *testing.T) {
	if !IsEnvelope("v1:abc:def") {
		t.Error("v1:abc:def should be envelope")
	}
	if IsEnvelope("v9:abc:def") {
		t.Error("v9 is not the supported scheme")
	}
	if IsEnvelope("{\"raw\":\"json\"}") {
		t.Error("raw JSON is not an envelope")
	}
	if IsEnvelope("v1:onlytwo") {
		t.Error("v1:onlytwo missing third part")
	}
}

func TestLoadAEAD_NoKeyConfigured(t *testing.T) {
	// Clear test override + reset memoization to simulate
	// "production deploy forgot to set WORKMAX_SECRETS_KEY."
	ClearKeyForTesting()
	t.Setenv("WORKMAX_SECRETS_KEY", "")

	_, err := Encrypt([]byte("x"))
	if err == nil {
		t.Error("expected error when no key configured")
	}
	if !strings.Contains(err.Error(), "WORKMAX_SECRETS_KEY") {
		t.Errorf("error should name the env var, got %q", err.Error())
	}
}

func TestValidateConfiguration(t *testing.T) {
	setupTestKey(t)
	if err := ValidateConfiguration(); err != nil {
		t.Fatalf("ValidateConfiguration: %v", err)
	}
}

func TestLoadAEAD_WrongLengthKey(t *testing.T) {
	ClearKeyForTesting()
	t.Setenv("WORKMAX_SECRETS_KEY", "c2hvcnQ") // base64("short") = 5 bytes

	_, err := Encrypt([]byte("x"))
	if err == nil || !strings.Contains(err.Error(), "expected") {
		t.Errorf("expected length error, got %v", err)
	}
}
