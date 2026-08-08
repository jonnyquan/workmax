//go:build desktop

package cloud_proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// tokenExchangeResponse mirrors the JSON returned by workmax.app's
// POST /api/desktop/oauth/token (RFC 6749 §5.1). All fields present
// in the spec; we read them all so future code that wants e.g.
// scope info doesn't have to re-parse.
type tokenExchangeResponse struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int    `json:"expires_in"`
	RefreshToken     string `json:"refresh_token"`
	RefreshExpiresIn int    `json:"refresh_expires_in"`
	Scope            string `json:"scope"`
}

// tokenExchangeError mirrors RFC 6749 §5.2's error envelope. Description is
// accepted for wire compatibility but is never returned or logged because it
// is arbitrary upstream text.
type tokenExchangeError struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// ExchangeCodeForToken POSTs the OAuth code (and PKCE verifier +
// device_id) to workmax.app's /api/desktop/oauth/token endpoint and
// returns a populated TokenPair.
//
// The caller is responsible for handing the result to
// TokenStore.Save — this function is wire-level only.
func (c *Client) ExchangeCodeForToken(
	ctx context.Context,
	code, redirectURI, pkceVerifier, deviceID, deviceInfoJSON string,
) (TokenPair, error) {
	return c.ExchangeCodeForTokenForScope(
		ctx,
		code,
		redirectURI,
		pkceVerifier,
		deviceID,
		deviceInfoJSON,
		"workagent",
	)
}

// ExchangeCodeForTokenForScope is the scope-bound legacy authorization-code
// exchange. expectedScope is frozen by OAuthFlow.Start and the response must
// match it byte-for-byte; a server must never broaden the persisted session.
func (c *Client) ExchangeCodeForTokenForScope(
	ctx context.Context,
	code, redirectURI, pkceVerifier, deviceID, deviceInfoJSON, expectedScope string,
) (TokenPair, error) {
	if c == nil {
		return TokenPair{}, fmt.Errorf("token exchange: client is missing")
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
	return c.postToken(ctx, form, expectedScope)
}

// ExchangeRefreshForToken POSTs grant_type=refresh_token. The backend
// rotates and we get a fresh access+refresh pair (same chain id
// under the hood, opaque to us).
func (c *Client) ExchangeRefreshForToken(ctx context.Context, refreshToken string) (TokenPair, error) {
	return c.ExchangeRefreshForTokenForScope(ctx, refreshToken, "workagent")
}

// ExchangeRefreshForTokenForScope rotates a refresh token while preserving the
// exact scope of the stored session. Current callers should pass TokenPair.Scope;
// the compatibility wrapper above remains fixed to the only currently supported
// Desktop scope.
func (c *Client) ExchangeRefreshForTokenForScope(
	ctx context.Context,
	refreshToken string,
	expectedScope string,
) (TokenPair, error) {
	if c == nil {
		return TokenPair{}, fmt.Errorf("token exchange: client is missing")
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {c.ClientID},
	}
	return c.postToken(ctx, form, expectedScope)
}

func (c *Client) postToken(ctx context.Context, form url.Values, expectedScope string) (TokenPair, error) {
	if !validCanonicalLoginScope(expectedScope) {
		return TokenPair{}, fmt.Errorf("token exchange: expected scope is invalid")
	}
	baseURL, err := NormalizeBaseURL(c.BaseURL)
	if err != nil {
		return TokenPair{}, fmt.Errorf("token exchange: cloud base URL is invalid")
	}
	tokenURL := baseURL + CloudRouteOAuthToken
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return TokenPair{}, fmt.Errorf("token exchange: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cache-Control", "no-store")
	SetClientHeaders(req.Header)

	resp, err := c.credentialHTTPClient().Do(req)
	if err != nil {
		return TokenPair{}, fmt.Errorf("token exchange: HTTP: %w", err)
	}
	defer resp.Body.Close()

	body, err := readBoundedLoginTransactionBody(resp)
	if err != nil {
		return TokenPair{}, fmt.Errorf("token exchange: backend returned an invalid response body")
	}
	defer clear(body)

	if resp.StatusCode != http.StatusOK {
		var errPayload tokenExchangeError
		if decodeLoginTransactionJSONObject(body, &errPayload) && allowedOAuthErrorCode(errPayload.Error) {
			return TokenPair{}, fmt.Errorf("token exchange: HTTP %d: OAuth error %s",
				resp.StatusCode, errPayload.Error)
		}
		return TokenPair{}, fmt.Errorf("token exchange: HTTP %d", resp.StatusCode)
	}

	if !isLoginTransactionJSONContentType(resp.Header) {
		return TokenPair{}, fmt.Errorf("token exchange: backend returned an invalid token response")
	}
	var ok tokenExchangeResponse
	if !decodeLoginTransactionJSONObject(body, &ok) ||
		ok.TokenType != "Bearer" ||
		!validLoginTransactionTokenText(ok.AccessToken) ||
		!validLoginTransactionTokenText(ok.RefreshToken) ||
		ok.ExpiresIn <= 0 || ok.ExpiresIn > loginTransactionMaxTokenLifetimeSec ||
		ok.RefreshExpiresIn <= 0 || ok.RefreshExpiresIn > loginTransactionMaxTokenLifetimeSec ||
		!validCanonicalLoginScope(ok.Scope) || ok.Scope != expectedScope {
		return TokenPair{}, fmt.Errorf("token exchange: backend returned an invalid token response")
	}

	now := time.Now().UTC()
	return TokenPair{
		AccessToken:      ok.AccessToken,
		AccessExpiresAt:  now.Add(time.Duration(ok.ExpiresIn) * time.Second),
		RefreshToken:     ok.RefreshToken,
		RefreshExpiresAt: now.Add(time.Duration(ok.RefreshExpiresIn) * time.Second),
		Scope:            ok.Scope,
		SavedAt:          now,
	}, nil
}

func allowedOAuthErrorCode(code string) bool {
	switch code {
	case "invalid_request",
		"invalid_client",
		"invalid_grant",
		"unauthorized_client",
		"unsupported_grant_type",
		"invalid_scope",
		"temporarily_unavailable",
		"server_error":
		return true
	default:
		return false
	}
}
