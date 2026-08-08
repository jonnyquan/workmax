package workagent

import (
	"errors"
	"net/url"
	"strings"
)

// allowedBaseURLHosts is the allowlist of hostnames (or hostname suffixes
// for sub-domains) we accept as Claude Agent SDK upstreams. The pool
// pushes account.BaseURL into ANTHROPIC_BASE_URL / CLAUDE_API_BASE_URL
// for every Agent invocation, so an attacker who reaches the admin
// CreateAgentAccount endpoint with an arbitrary URL would otherwise be
// able to hijack every prompt and tool call to a domain they control.
//
// Entries are matched exact-or-suffix: "anthropic.com" matches
// api.anthropic.com but not anthropic.com.evil.example.com because we
// also require the prefix character before the match to be `.` or to
// be exactly the suffix. Add new providers here when an account for
// that gateway is genuinely needed; never widen the wildcard.
var allowedBaseURLHosts = []string{
	"anthropic.com",
	"bigmodel.cn",   // Zhipu Anthropic-compatible gateway
	"volces.com",    // Volcengine Ark
	"deepseek.com",  // DeepSeek Anthropic-compatible
	"moonshot.cn",   // Kimi
	"siliconflow.cn",
}

// ValidateBaseURL rejects any account.BaseURL that doesn't point at a
// known provider. Returns an error suitable to surface to the admin UI.
//
// Empty values are rejected — the SDK falls back to the default
// Anthropic endpoint when ANTHROPIC_BASE_URL is unset, but the schema
// requires the column to be non-empty so an empty string here is
// a configuration error worth flagging at write time.
//
// Scheme rules:
//   - public-provider hosts (entries on allowedBaseURLHosts) MUST be
//     https. The previous validator accepted http://api.anthropic.com,
//     which would silently downgrade every API call if an admin row
//     was ever populated that way (compromise, copy-paste typo,
//     schema drift). https-only closes that hole.
//   - http stays allowed for loopback hosts (localhost / 127.0.0.1 /
//     ::1) so a developer running an Anthropic-compatible relay on
//     their machine can plumb it without a TLS cert.
func ValidateBaseURL(rawURL string) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return errors.New("BaseURL must not be empty")
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return errors.New("BaseURL is not a valid URL")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return errors.New("BaseURL must use http or https scheme")
	}
	if u.Host == "" {
		return errors.New("BaseURL must include a host")
	}

	host := strings.ToLower(u.Hostname())

	if u.Scheme == "http" {
		if !isLoopbackHost(host) {
			return errors.New("BaseURL http scheme is only allowed for localhost — use https for public providers")
		}
		return nil
	}

	for _, allowed := range allowedBaseURLHosts {
		if host == allowed || strings.HasSuffix(host, "."+allowed) {
			return nil
		}
	}
	return errors.New("BaseURL host is not on the provider allowlist; add it to allowedBaseURLHosts in baseurl_validator.go if needed")
}

// isLoopbackHost reports whether host is one of the loopback aliases.
// Used by the scheme-relaxation path so dev relays can use http.
func isLoopbackHost(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1", "[::1]":
		return true
	}
	return false
}
