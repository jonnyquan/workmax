package logintransaction

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

func validateCreateInput(in CreateInput) error {
	if !validBoundedText(in.ClientID, 1, 64) {
		return fmt.Errorf("%w: client_id is malformed", ErrInvalidInput)
	}
	if err := validateLoopbackRedirect(in.RedirectURI); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	if !validRandomToken(in.OAuthState, 16) {
		return fmt.Errorf("%w: OAuth state must be a canonical 128-bit base64url nonce", ErrInvalidInput)
	}
	if in.CodeChallengeMethod != GooglePKCEMethod || !validRandomToken(in.CodeChallenge, 32) {
		return fmt.Errorf("%w: PKCE challenge must be S256 base64url", ErrInvalidInput)
	}
	if !validCanonicalScope(in.Scope) {
		return fmt.Errorf("%w: scope is malformed", ErrInvalidInput)
	}
	if len(in.DeviceID) != 32 {
		return fmt.Errorf("%w: device_id must be 32 hex characters", ErrInvalidInput)
	}
	if _, err := hex.DecodeString(in.DeviceID); err != nil {
		return fmt.Errorf("%w: device_id must be 32 hex characters", ErrInvalidInput)
	}
	if in.DeviceID != strings.ToLower(in.DeviceID) {
		return fmt.Errorf("%w: device_id must use canonical lowercase hex", ErrInvalidInput)
	}
	return nil
}

func validateLoopbackRedirect(raw string) error {
	if len(raw) == 0 || len(raw) > 500 || !utf8.ValidString(raw) || strings.TrimSpace(raw) != raw || hasControl(raw) {
		return fmt.Errorf("redirect_uri is malformed")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("redirect_uri is malformed")
	}
	if parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.User != nil ||
		parsed.Path != "/oauth/callback" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("redirect_uri must be an exact 127.0.0.1 loopback callback")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("redirect_uri must include a valid loopback port")
	}
	return nil
}

func validatePasswordCompletionInput(in PasswordCompletionInput) error {
	if !validRandomToken(in.TransactionID, transactionIDBytes) || !validRandomToken(in.TransactionSecret, transactionSecretBytes) {
		return ErrInvalidTransaction
	}
	if !validBoundedText(in.Email, 3, 320) || !strings.Contains(in.Email, "@") {
		return fmt.Errorf("%w: email is malformed", ErrInvalidInput)
	}
	if len(in.Password) == 0 || len(in.Password) > 1024 || !utf8.ValidString(in.Password) || hasControlExceptWhitespace(in.Password) {
		return fmt.Errorf("%w: password is malformed", ErrInvalidInput)
	}
	return nil
}

func validateGoogleStartInput(in GoogleStartInput) error {
	if !validRandomToken(in.TransactionID, transactionIDBytes) || !validRandomToken(in.TransactionSecret, transactionSecretBytes) {
		return ErrInvalidTransaction
	}
	return nil
}

func validateGoogleCompletionInput(in GoogleCompletionInput) (string, error) {
	if len(in.ProviderState) == 0 || len(in.ProviderState) > 128 || !utf8.ValidString(in.ProviderState) || hasControl(in.ProviderState) {
		return "", ErrInvalidTransaction
	}
	transactionID, ok := providerStateTransactionID(in.ProviderState)
	if !ok {
		return "", ErrInvalidTransaction
	}
	if !validBoundedText(in.AuthorizationCode, 1, 8192) {
		return "", fmt.Errorf("%w: provider authorization code is malformed", ErrInvalidInput)
	}
	return transactionID, nil
}

func validateExchangeInput(in ExchangeInput) error {
	if !validRandomToken(in.TransactionID, transactionIDBytes) || !validRandomToken(in.ExchangeToken, exchangeTokenBytes) {
		return ErrInvalidTransaction
	}
	return nil
}

func validCanonicalScope(scope string) bool {
	if len(scope) == 0 || len(scope) > 255 || strings.TrimSpace(scope) != scope || strings.Join(strings.Fields(scope), " ") != scope {
		return false
	}
	for _, token := range strings.Fields(scope) {
		for i := 0; i < len(token); i++ {
			b := token[i]
			if b < 0x21 || b > 0x7e || b == 0x22 || b == 0x5c {
				return false
			}
		}
	}
	return true
}

func validBoundedText(value string, minLength, maxLength int) bool {
	return len(value) >= minLength && len(value) <= maxLength && utf8.ValidString(value) &&
		strings.TrimSpace(value) == value && !hasControl(value)
}

func validRandomToken(value string, byteCount int) bool {
	if value == "" || len(value) != base64.RawURLEncoding.EncodedLen(byteCount) {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) != byteCount {
		return false
	}
	return base64.RawURLEncoding.EncodeToString(decoded) == value
}

func hasControl(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] == 0x7f {
			return true
		}
	}
	return false
}

func hasControlExceptWhitespace(value string) bool {
	for i := 0; i < len(value); i++ {
		b := value[i]
		if (b < 0x20 && b != '\t' && b != '\n' && b != '\r') || b == 0x7f {
			return true
		}
	}
	return false
}
