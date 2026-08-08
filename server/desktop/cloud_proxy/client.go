//go:build desktop

package cloud_proxy

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is the sidecar's HTTP client for talking directly to the configured
// WorkMax Go Server API origin.
// Wraps a configured BaseURL + ClientID + http.Client so individual
// calls don't have to re-thread them.
//
// It hosts OAuth token exchange plus JSON cloud calls that can share
// the default bounded timeout. Long-lived SSE calls use separate
// clients where appropriate.
type Client struct {
	BaseURL  string // bare Go Server/API gateway origin (no trailing slash)
	ClientID string // OAuth client_id, e.g. "workmax-desktop"

	// HTTPClient is the underlying transport. Injectable so tests
	// can substitute httptest.Server.URL + an http.Client with
	// short timeouts. Nil → use NewDefaultHTTPClient() lazily.
	HTTPClient *http.Client
}

// NewClient returns a Client for the given Go Server/API gateway origin.
// Caller fills HTTPClient if customization is needed; otherwise
// the default (10s timeout, default Transport) is used on first call.
func NewClient(baseURL string) *Client {
	return &Client{
		BaseURL:  baseURL,
		ClientID: "workmax-desktop",
	}
}

// NormalizeBaseURL validates a configured cloud base and returns the canonical
// origin string used for route concatenation. Desktop cloud routes are absolute
// paths such as /api/desktop/oauth/authorize, so accepting a base path, query,
// fragment, or credentials would either produce malformed URLs or silently send
// OAuth traffic to an unexpected place.
//
// Credential-bearing Desktop routes require HTTPS. Plain HTTP is accepted only
// for an exact IP loopback origin used by local development; hostnames such as
// localhost are intentionally excluded so DNS or hosts-file changes cannot turn
// an insecure development exception into a remote credential target.
func NormalizeBaseURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("cloud base URL is required")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("parse cloud base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("cloud base URL must use http or https")
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("cloud base URL must include a host")
	}
	if parsed.User != nil {
		return "", fmt.Errorf("cloud base URL must not include credentials")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("cloud base URL must not include query or fragment")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", fmt.Errorf("cloud base URL must not include a path")
	}
	if parsed.Scheme == "http" && !isExactCloudLoopbackHost(parsed.Hostname()) {
		return "", fmt.Errorf("cloud base URL must use https unless the host is exact IP loopback")
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

func isExactCloudLoopbackHost(hostname string) bool {
	return hostname == "127.0.0.1" || hostname == "::1"
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return NewDefaultHTTPClient()
}

// credentialHTTPClient is the only client credential-bearing OAuth calls may
// use. It intentionally shares just the configured transport and timeout: no
// Cookie Jar and no caller-provided redirect callback cross this boundary.
// Refusing every redirect is stronger than relying on net/http's selective
// header stripping because a 307/308 can replay a POST form body containing a
// code, PKCE verifier, or refresh token to the Location target.
func (c *Client) credentialHTTPClient() *http.Client {
	return cloneCredentialHTTPClient(c.httpClient())
}

// cloneCredentialHTTPClient preserves the caller's transport and timeout while
// removing all ambient authority that could accompany or replay a credential.
// It is also used by Proxy's separate timeout-free SSE client.
func cloneCredentialHTTPClient(shared *http.Client) *http.Client {
	if shared == nil {
		shared = &http.Client{}
	}
	return &http.Client{
		Transport: shared.Transport,
		Timeout:   shared.Timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// NewDefaultHTTPClient is the production HTTP client. 10s timeout is
// snug for short JSON calls (typical < 1s) but generous enough to
// survive a hiccup. SSE-bearing calls use a separate client with no
// overall timeout.
func NewDefaultHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
	}
}
