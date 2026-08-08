package v1

import (
	"strings"
	"testing"

	jwt "github.com/golang-jwt/jwt/v4"
)

var contractSigningKey = []byte("0123456789abcdef0123456789abcdef")

func TestParserVerifiesSignatureAlgorithmAndTypedClaims(t *testing.T) {
	validator := contractValidator(t)
	parser, err := NewHMACSHA256Parser(contractSigningKey, validator)
	if err != nil {
		t.Fatalf("NewHMACSHA256Parser: %v", err)
	}
	claims := validClaimsFor(t, PolicyAgentResource)
	claims.Scope = "agent.run workspace.primary"
	raw := signContractToken(t, jwt.SigningMethodHS256, claims, contractSigningKey)
	principal, err := parser.ParseAndValidate(raw, Expectation{
		Policy:         PolicyAgentResource,
		RequiredScopes: []Scope{"workspace.primary"},
	})
	if err != nil {
		t.Fatalf("ParseAndValidate: %v", err)
	}
	if principal.Subject != claims.Subject || principal.DeviceSessionID != claims.DeviceSessionID {
		t.Fatalf("unexpected principal: %+v", principal)
	}
}

func TestParserRejectsBadSignatureAlgorithmAndWire(t *testing.T) {
	validator := contractValidator(t)
	parser, err := NewHMACSHA256Parser(contractSigningKey, validator)
	if err != nil {
		t.Fatalf("NewHMACSHA256Parser: %v", err)
	}
	claims := validClaimsFor(t, PolicyDeviceSession)

	tests := []struct {
		name string
		raw  func() string
		code ErrorCode
	}{
		{
			name: "wrong signature",
			raw: func() string {
				return signContractToken(t, jwt.SigningMethodHS256, claims, []byte("abcdef0123456789abcdef0123456789"))
			},
			code: CodeTokenSignature,
		},
		{
			name: "algorithm outside allowlist",
			raw: func() string {
				return signContractToken(t, jwt.SigningMethodHS384, claims, contractSigningKey)
			},
			code: CodeTokenSignature,
		},
		{name: "empty", raw: func() string { return "" }, code: CodeTokenMalformed},
		{name: "surrounding whitespace", raw: func() string { return " token " }, code: CodeTokenMalformed},
		{name: "not a JWT", raw: func() string { return "not-a-jwt" }, code: CodeTokenMalformed},
		{name: "oversized", raw: func() string { return strings.Repeat("x", MaxSignedTokenBytes+1) }, code: CodeTokenMalformed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := test.raw()
			_, err := parser.ParseAndValidate(raw, Expectation{Policy: PolicyDeviceSession})
			if !HasErrorCode(err, test.code) {
				t.Fatalf("ParseAndValidate: got %v, want %s", err, test.code)
			}
			if raw != "" && strings.Contains(err.Error(), raw) {
				t.Fatalf("error leaked raw token: %v", err)
			}
		})
	}
}

func TestParserCopiesHMACKey(t *testing.T) {
	validator := contractValidator(t)
	sourceKey := append([]byte(nil), contractSigningKey...)
	parser, err := NewHMACSHA256Parser(sourceKey, validator)
	if err != nil {
		t.Fatalf("NewHMACSHA256Parser: %v", err)
	}
	for index := range sourceKey {
		sourceKey[index] = 'x'
	}
	raw := signContractToken(t, jwt.SigningMethodHS256, validClaimsFor(t, PolicyPortalSession), contractSigningKey)
	if _, err := parser.ParseAndValidate(raw, Expectation{Policy: PolicyPortalSession}); err != nil {
		t.Fatalf("parser retained mutable caller key: %v", err)
	}
}

func TestNewParserRejectsUnsafeConfiguration(t *testing.T) {
	validator := contractValidator(t)
	keyFunc := func(_ *jwt.Token) (any, error) { return contractSigningKey, nil }
	for name, build := range map[string]func() error{
		"nil key":       func() error { _, err := NewParser(nil, []string{"HS256"}, validator); return err },
		"nil validator": func() error { _, err := NewParser(keyFunc, []string{"HS256"}, nil); return err },
		"empty methods": func() error { _, err := NewParser(keyFunc, nil, validator); return err },
		"none":          func() error { _, err := NewParser(keyFunc, []string{"none"}, validator); return err },
		"unknown":       func() error { _, err := NewParser(keyFunc, []string{"unknown"}, validator); return err },
		"duplicate":     func() error { _, err := NewParser(keyFunc, []string{"HS256", "HS256"}, validator); return err },
		"short HMAC":    func() error { _, err := NewHMACSHA256Parser([]byte("short"), validator); return err },
	} {
		t.Run(name, func(t *testing.T) {
			if err := build(); err == nil {
				t.Fatal("unsafe parser configuration was accepted")
			}
		})
	}
}

func signContractToken(t *testing.T, method jwt.SigningMethod, claims Claims, key []byte) string {
	t.Helper()
	token := jwt.NewWithClaims(method, claims)
	raw, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return raw
}
