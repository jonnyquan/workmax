//go:build desktop

package cloud_proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// VersionInfo is the decoded form of the cloud's
// GET /api/desktop/version response. Field shape matches the
// cloud's version_api.go::versionResponse 1:1.
//
// Both fields are JSON-named because they're consumed by the
// renderer through the sidecar's /system/server-version proxy and
// the renderer expects the cloud-side names; renaming a field here
// requires the renderer + cloud to agree first.
type VersionInfo struct {
	MinSupported      string `json:"min_supported"`
	LatestRecommended string `json:"latest_recommended"`
	// ReleaseNotesURL is optional — empty when ops hasn't set
	// WORKMAX_DESKTOP_RELEASE_NOTES_URL on the cloud side. Renderer
	// suppresses the "What's new" link when empty.
	ReleaseNotesURL string `json:"release_notes_url,omitempty"`
}

// GetVersion fetches the cloud's published version envelope.
// Unauthenticated — the cloud route is public so a stale sidecar
// (which may not be able to complete OAuth) can still discover
// it's stale.
//
// Uses the snug-timeout JSON client (inherited from c.httpClient())
// rather than the chat-relay's no-timeout client; the version
// payload is tiny and we don't want a hung cloud to block the
// renderer's diagnostics panel.
func (c *Client) GetVersion(ctx context.Context) (VersionInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+CloudRouteVersion, nil)
	if err != nil {
		return VersionInfo{}, fmt.Errorf("version: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	SetClientHeaders(req.Header)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return VersionInfo{}, fmt.Errorf("version: HTTP: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<10)) // 4 KiB — tiny version envelope
	if err != nil {
		return VersionInfo{}, fmt.Errorf("version: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return VersionInfo{}, fmt.Errorf("version: HTTP %d: %s", resp.StatusCode, bodyPrefix(body))
	}

	var info VersionInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return VersionInfo{}, fmt.Errorf("version: decode: %w (body: %s)", err, bodyPrefix(body))
	}
	if info.MinSupported == "" {
		return VersionInfo{}, fmt.Errorf("version: missing min_supported")
	}
	if info.LatestRecommended == "" {
		return VersionInfo{}, fmt.Errorf("version: missing latest_recommended")
	}
	info.ReleaseNotesURL = safeReleaseNotesURL(info.ReleaseNotesURL)
	return info, nil
}

func safeReleaseNotesURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	for key := range u.Query() {
		if isSensitiveReleaseNotesQueryKey(key) {
			return ""
		}
	}
	return u.String()
}

func isSensitiveReleaseNotesQueryKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "authorization",
		"x-local-token",
		"workmax_local_token",
		"access_token",
		"refresh_token",
		"id_token",
		"token",
		"api_key",
		"api-key",
		"apikey",
		"client_secret",
		"password",
		"secret":
		return true
	default:
		return false
	}
}
