package v1

import (
	"fmt"
	"strings"

	jwt "github.com/golang-jwt/jwt/v4"
)

// Parser verifies JWT integrity and then applies a Validator. It is immutable
// and safe for concurrent use if its key function is safe for concurrent use.
type Parser struct {
	keyFunc      jwt.Keyfunc
	validMethods []string
	validator    *Validator
}

// NewParser constructs a parser with an explicit algorithm allowlist. It
// rejects the unsecured "none" algorithm and unknown signing methods.
func NewParser(keyFunc jwt.Keyfunc, validMethods []string, validator *Validator) (*Parser, error) {
	if keyFunc == nil {
		return nil, fmt.Errorf("credential: key function must not be nil")
	}
	if validator == nil {
		return nil, fmt.Errorf("credential: validator must not be nil")
	}
	if len(validMethods) == 0 {
		return nil, fmt.Errorf("credential: at least one signing method is required")
	}
	methods := make([]string, 0, len(validMethods))
	seen := make(map[string]struct{}, len(validMethods))
	for _, method := range validMethods {
		if method == "" || method == jwt.SigningMethodNone.Alg() || jwt.GetSigningMethod(method) == nil {
			return nil, fmt.Errorf("credential: signing method %q is not allowed", method)
		}
		if _, duplicate := seen[method]; duplicate {
			return nil, fmt.Errorf("credential: signing method %q is duplicated", method)
		}
		seen[method] = struct{}{}
		methods = append(methods, method)
	}
	return &Parser{keyFunc: keyFunc, validMethods: methods, validator: validator}, nil
}

// NewHMACSHA256Parser is the narrow convenience constructor for the current
// symmetric signing deployment. Target key rotation or asymmetric signing can
// use NewParser with a kid-aware key function instead. Portal/Admin browser
// sessions remain server-side cookie sessions; this constructor does not
// authorize exposing those session credentials as bearer tokens.
func NewHMACSHA256Parser(signingKey []byte, validator *Validator) (*Parser, error) {
	if len(signingKey) < 32 {
		return nil, fmt.Errorf("credential: HS256 signing key must contain at least 32 bytes")
	}
	key := append([]byte(nil), signingKey...)
	return NewParser(func(token *jwt.Token) (any, error) {
		if token.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, fmt.Errorf("credential: unexpected signing method")
		}
		return key, nil
	}, []string{jwt.SigningMethodHS256.Alg()}, validator)
}

// ParseAndValidate verifies signature and algorithm before typed semantic
// validation. Raw tokens and received claim values are never included in its
// errors.
func (parser *Parser) ParseAndValidate(rawToken string, expectation Expectation) (Principal, error) {
	if parser == nil || parser.keyFunc == nil || parser.validator == nil || len(parser.validMethods) == 0 {
		return Principal{}, fmt.Errorf("credential: parser is not configured")
	}
	if rawToken == "" || len(rawToken) > MaxSignedTokenBytes || strings.TrimSpace(rawToken) != rawToken || strings.ContainsAny(rawToken, "\r\n\t ") {
		return Principal{}, validationError(CodeTokenMalformed, "")
	}

	claims := &Claims{}
	jwtParser := jwt.NewParser(
		jwt.WithValidMethods(append([]string(nil), parser.validMethods...)),
		jwt.WithoutClaimsValidation(),
	)
	token, err := jwtParser.ParseWithClaims(rawToken, claims, parser.keyFunc)
	if err != nil {
		if validationErr, ok := err.(*jwt.ValidationError); ok {
			if validationErr.Errors&jwt.ValidationErrorSignatureInvalid != 0 {
				return Principal{}, validationError(CodeTokenSignature, "")
			}
			if validationErr.Errors&jwt.ValidationErrorMalformed != 0 {
				return Principal{}, validationError(CodeTokenMalformed, "")
			}
		}
		return Principal{}, validationError(CodeTokenInvalid, "")
	}
	if token == nil || !token.Valid || token.Claims != claims {
		return Principal{}, validationError(CodeTokenInvalid, "")
	}
	return parser.validator.Validate(*claims, expectation)
}
