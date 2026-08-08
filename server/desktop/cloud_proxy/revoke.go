//go:build desktop

package cloud_proxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// RevokeRefreshToken POSTs to workmax.app's /api/desktop/oauth/revoke
// (RFC 7009) to invalidate a refresh token chain server-side.
//
// Per RFC 7009 §2.2 the server returns 200 for both "revoked
// successfully" and "token not recognized" — we treat any 2xx as
// success. 4xx returns a non-nil error so the caller can log it,
// but they should NOT block the logout flow on it: a network blip
// shouldn't prevent the user from clearing their local session.
//
// LogoutSession calls this best-effort and always carries on with local
// clearing. The refresh chain will eventually time out (90 days) even if the
// backend cannot be reached during logout.
func (c *Client) RevokeRefreshToken(ctx context.Context, refreshToken string) error {
	if refreshToken == "" {
		return fmt.Errorf("revoke: refresh token is empty")
	}
	if c == nil {
		return fmt.Errorf("revoke: client is missing")
	}
	baseURL, err := NormalizeBaseURL(c.BaseURL)
	if err != nil {
		return fmt.Errorf("revoke: cloud base URL is invalid")
	}
	form := url.Values{
		"token":           {refreshToken},
		"client_id":       {c.ClientID},
		"token_type_hint": {"refresh_token"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		baseURL+CloudRouteOAuthRevoke,
		strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("revoke: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	SetClientHeaders(req.Header)

	resp, err := c.credentialHTTPClient().Do(req)
	if err != nil {
		return fmt.Errorf("revoke: HTTP: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	// Drain only a bounded prefix for connection reuse, but never include an
	// arbitrary upstream body in the returned error.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 2<<10))
	return fmt.Errorf("revoke: HTTP %d", resp.StatusCode)
}
