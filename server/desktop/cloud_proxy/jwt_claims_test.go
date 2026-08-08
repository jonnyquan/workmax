//go:build desktop

package cloud_proxy

import (
	"encoding/base64"
	"strings"
	"testing"
)

// mintTestJWT assembles a JWT with the given payload-body (raw
// bytes). Signature segment is fake — ExtractUIDFromAccessToken
// doesn't verify, so any non-empty string works.
func mintTestJWT(payload []byte) string {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	body := base64.RawURLEncoding.EncodeToString(payload)
	sig := base64.RawURLEncoding.EncodeToString([]byte("not-verified"))
	return hdr + "." + body + "." + sig
}

func TestExtractUIDFromAccessToken_HappyPath(t *testing.T) {
	tok := mintTestJWT([]byte(`{"Id":42,"Email":"x@y.z","BufferTime":3600,"exp":9999999999}`))
	uid, err := ExtractUIDFromAccessToken(tok)
	if err != nil {
		t.Fatal(err)
	}
	if uid != 42 {
		t.Errorf("uid: got %d, want 42", uid)
	}
}

func TestExtractUIDFromAccessToken_MissingIDFieldReturnsZero(t *testing.T) {
	// Valid JWT, valid JSON, just no Id claim.
	tok := mintTestJWT([]byte(`{"Email":"x@y.z","exp":9999999999}`))
	uid, err := ExtractUIDFromAccessToken(tok)
	if err != nil {
		t.Errorf("absent Id should not error, got: %v", err)
	}
	if uid != 0 {
		t.Errorf("uid: got %d, want 0", uid)
	}
}

func TestExtractUIDFromAccessToken_RejectsMalformed(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"no dots", "abcdef"},
		{"one dot", "abc.def"},
		{"too many dots", "abc.def.ghi.jkl"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ExtractUIDFromAccessToken(tc.input)
			if err == nil {
				t.Errorf("expected error for %q", tc.input)
			}
		})
	}
}

func TestExtractUIDFromAccessToken_BadBase64Payload(t *testing.T) {
	tok := "header." + strings.Repeat("!", 8) + ".signature"
	_, err := ExtractUIDFromAccessToken(tok)
	if err == nil {
		t.Error("expected error for bad base64 payload")
	}
}

func TestExtractUIDFromAccessToken_BadJSONPayload(t *testing.T) {
	// Valid base64url that decodes to non-JSON.
	garbage := base64.RawURLEncoding.EncodeToString([]byte("not json at all"))
	tok := "header." + garbage + ".signature"
	_, err := ExtractUIDFromAccessToken(tok)
	if err == nil {
		t.Error("expected error for non-JSON payload")
	}
}

func TestExtractUIDFromAccessToken_PaddedBase64Fallback(t *testing.T) {
	// Most JWT emitters use base64url WITHOUT padding (RawURLEncoding).
	// Some legacy emitters use the padded form. Verify the fallback.
	payload := []byte(`{"Id":7}`)
	padded := base64.URLEncoding.EncodeToString(payload) // produces "==" padding sometimes
	tok := "header." + padded + ".signature"
	uid, err := ExtractUIDFromAccessToken(tok)
	if err != nil {
		t.Fatalf("padded base64 should fall back through: %v", err)
	}
	if uid != 7 {
		t.Errorf("uid: got %d, want 7", uid)
	}
}
