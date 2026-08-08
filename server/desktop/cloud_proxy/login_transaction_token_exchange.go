//go:build desktop

package cloud_proxy

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	loginTransactionMaxTokenBytes       = 8 << 10
	loginTransactionMaxDeviceInfoBytes  = 2 << 10
	loginTransactionMaxTokenLifetimeSec = 366 * 24 * 60 * 60
)

var errLoginTransactionSecureTokenExchange = errors.New("desktop login transaction: token exchange unavailable")

// exchangeLoginTransactionCodeForToken is the coordinator-only authorization
// code exchange. Like the legacy helper, it uses the independent no-cookie,
// no-redirect credential client. It remains separate because the coordinator
// supplies its frozen clock and requires every failure to collapse to one
// static error; response bodies, headers, URLs and transport details must not
// cross that boundary.
func (c *Client) exchangeLoginTransactionCodeForToken(
	ctx context.Context,
	code string,
	redirectURI string,
	pkceVerifier string,
	deviceID string,
	deviceInfoJSON string,
	now time.Time,
) (TokenPair, error) {
	if ctx == nil || c == nil || !validCanonicalLoginToken(code, loginTransactionAuthCodeBytes) ||
		!validCanonicalLoginToken(pkceVerifier, PKCEVerifierBytes) ||
		!validLoginTransactionDeviceID(deviceID) || !validLoginTransactionDeviceInfo(deviceInfoJSON) {
		return TokenPair{}, errLoginTransactionSecureTokenExchange
	}
	baseURL, err := c.validateLoginTransactionClient("token")
	if err != nil {
		return TokenPair{}, errLoginTransactionSecureTokenExchange
	}
	if _, ok := parseCanonicalLoginLoopback(redirectURI); !ok {
		return TokenPair{}, errLoginTransactionSecureTokenExchange
	}

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {c.ClientID},
		"code_verifier": {pkceVerifier},
		"device_id":     {deviceID},
	}
	if deviceInfoJSON != "" {
		form.Set("device_info", deviceInfoJSON)
	}
	body := form.Encode()
	if len(body) == 0 || len(body) > loginTransactionMaxRequestBodyBytes {
		return TokenPair{}, errLoginTransactionSecureTokenExchange
	}

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		baseURL+CloudRouteOAuthToken,
		strings.NewReader(body),
	)
	if err != nil {
		return TokenPair{}, errLoginTransactionSecureTokenExchange
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Cache-Control", "no-store")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	SetClientHeaders(request.Header)

	response, err := c.loginTransactionHTTPClient().Do(request)
	if err != nil {
		return TokenPair{}, errLoginTransactionSecureTokenExchange
	}
	defer response.Body.Close()

	raw, err := readBoundedLoginTransactionBody(response)
	if err != nil {
		return TokenPair{}, errLoginTransactionSecureTokenExchange
	}
	defer clear(raw)
	if !isLoginTransactionJSONContentType(response.Header) || response.StatusCode != http.StatusOK {
		return TokenPair{}, errLoginTransactionSecureTokenExchange
	}
	var payload tokenExchangeResponse
	if !decodeLoginTransactionJSONObject(raw, &payload) ||
		payload.TokenType != "Bearer" ||
		!validLoginTransactionTokenText(payload.AccessToken) ||
		!validLoginTransactionTokenText(payload.RefreshToken) ||
		payload.ExpiresIn <= 0 || payload.ExpiresIn > loginTransactionMaxTokenLifetimeSec ||
		payload.RefreshExpiresIn <= 0 || payload.RefreshExpiresIn > loginTransactionMaxTokenLifetimeSec ||
		!validCanonicalLoginScope(payload.Scope) {
		return TokenPair{}, errLoginTransactionSecureTokenExchange
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	return TokenPair{
		AccessToken:      payload.AccessToken,
		AccessExpiresAt:  now.Add(time.Duration(payload.ExpiresIn) * time.Second),
		RefreshToken:     payload.RefreshToken,
		RefreshExpiresAt: now.Add(time.Duration(payload.RefreshExpiresIn) * time.Second),
		Scope:            payload.Scope,
		SavedAt:          now,
	}, nil
}

func validLoginTransactionTokenText(value string) bool {
	if len(value) == 0 || len(value) > loginTransactionMaxTokenBytes {
		return false
	}
	// WorkMax issues URL-safe opaque refresh tokens and compact JWT access
	// tokens. Keeping the accepted alphabet to their shared ASCII surface
	// prevents whitespace/scheme ambiguity when the value is later placed in an
	// Authorization header, without accepting padded or standard-base64 aliases.
	for i := 0; i < len(value); i++ {
		char := value[i]
		if (char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') {
			continue
		}
		switch char {
		case '-', '_', '.', '~':
			continue
		default:
			return false
		}
	}
	return true
}

func validLoginTransactionDeviceInfo(value string) bool {
	return value == "" || (len(value) <= loginTransactionMaxDeviceInfoBytes &&
		utf8.ValidString(value) && strings.TrimSpace(value) == value && !hasLoginControl(value))
}
