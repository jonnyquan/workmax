//go:build desktop

package cloud_proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// ErrUserInfoAuthExpired is returned when the cloud userinfo endpoint
// rejects the access token with HTTP 401. Callers that still have a
// valid refresh token can rotate once and retry.
var ErrUserInfoAuthExpired = errors.New("userinfo: auth expired (HTTP 401)")

// UserInfo is the denormalized account snapshot returned by the
// cloud OAuth userinfo endpoint and re-emitted by the sidecar. Keep
// this shape aligned with server/api/desktop/oauth/oauth_userinfo_api.go.
type UserInfo struct {
	UserID        string        `json:"user_id"`
	Email         string        `json:"email"`
	DisplayName   string        `json:"display_name"`
	AvatarURL     string        `json:"avatar_url,omitempty"`
	Tier          string        `json:"tier"`
	TierExpiresAt string        `json:"tier_expires_at,omitempty"`
	Quota         UserInfoQuota `json:"quota"`
}

type UserInfoQuota struct {
	MonthUsed  int `json:"month_used"`
	MonthLimit int `json:"month_limit"`
}

// UserInfo fetches the current user's account snapshot from the cloud using a
// Bearer access token. A lease-bound context canceled by a login/logout
// transition returns ErrSessionChanged instead of a generic transport error.
func (c *Client) UserInfo(ctx context.Context, accessToken string) (UserInfo, error) {
	if err := sessionChangedContextError(ctx); err != nil {
		return UserInfo{}, err
	}
	if c == nil {
		return UserInfo{}, fmt.Errorf("userinfo: HTTP: client is missing")
	}
	baseURL, err := NormalizeBaseURL(c.BaseURL)
	if err != nil {
		return UserInfo{}, fmt.Errorf("userinfo: HTTP: cloud base URL is invalid")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+CloudRouteOAuthUserInfo, nil)
	if err != nil {
		return UserInfo{}, fmt.Errorf("userinfo: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	SetClientHeaders(req.Header)

	resp, err := c.credentialHTTPClient().Do(req)
	if err != nil {
		if sessionErr := sessionChangedContextError(ctx); sessionErr != nil {
			return UserInfo{}, sessionErr
		}
		return UserInfo{}, fmt.Errorf("userinfo: HTTP: %w", err)
	}
	defer resp.Body.Close()

	body, err := readBoundedCloudResponseBody(resp, 64<<10)
	if err != nil {
		if sessionErr := sessionChangedContextError(ctx); sessionErr != nil {
			return UserInfo{}, sessionErr
		}
		return UserInfo{}, fmt.Errorf("userinfo: invalid response body")
	}
	defer clear(body)
	if sessionErr := sessionChangedContextError(ctx); sessionErr != nil {
		return UserInfo{}, sessionErr
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return UserInfo{}, ErrUserInfoAuthExpired
	}
	if resp.StatusCode != http.StatusOK {
		return UserInfo{}, fmt.Errorf("userinfo: HTTP %d", resp.StatusCode)
	}

	var out UserInfo
	if err := json.Unmarshal(body, &out); err != nil {
		return UserInfo{}, fmt.Errorf("userinfo: invalid response")
	}
	if sessionErr := sessionChangedContextError(ctx); sessionErr != nil {
		return UserInfo{}, sessionErr
	}
	return out, nil
}

// sessionChangedContextError gives session replacement precedence over the
// transport's generic context-canceled wrapper. A SessionLease binds
// ErrSessionChanged as the cancellation cause, while renderer disconnects and
// ordinary deadlines retain their standard context errors.
func sessionChangedContextError(ctx context.Context) error {
	if ctx != nil && errors.Is(context.Cause(ctx), ErrSessionChanged) {
		return ErrSessionChanged
	}
	return nil
}
