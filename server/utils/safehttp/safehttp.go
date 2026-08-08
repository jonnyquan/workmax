// Package safehttp provides SSRF-safe HTTP helpers for endpoints that
// fetch user-supplied URLs (e.g. attachment downloads, image imports).
//
// The defenses are layered:
//
//  1. ValidateURL: scheme allowlist (http/https), strip credentials,
//     resolve the host, refuse anything pointing at a private /
//     loopback / link-local / multicast / unspecified address.
//  2. DialContext: re-resolves the host at connect time to close the
//     TOCTOU between validate and dial — a hostile resolver can't
//     return a public IP at validate time and a private IP at dial.
//  3. IsUnsafeIP: the canonical "do not connect to this" predicate;
//     used by both layers above.
//
// This package was extracted from server/service/tools/canvasagent
// (where it lived as private helpers) so multiple endpoints that need
// the same protection — currently canvasagent attachment downloads
// and comic-page imports — share one implementation. Fixes to the
// IP-classification rules land in one place.
package safehttp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ValidateURL parses rawURL and returns a *url.URL only when:
//   - scheme is http or https
//   - host is non-empty
//   - URL contains no userinfo (no embedded credentials)
//   - host resolves to at least one IP that passes IsUnsafeIP=false
//
// Any failure returns a descriptive error suitable for surfacing to
// the operator log. Callers should not echo the error back to clients
// verbatim — error text leaks the host.
func ValidateURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("url must use http or https")
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("url must not include credentials")
	}
	host := parsed.Hostname()
	if host == "" {
		return nil, fmt.Errorf("url missing host")
	}
	if _, err := firstSafeIP(context.Background(), host); err != nil {
		return nil, err
	}
	return parsed, nil
}

// DialContext is a net.Dialer.DialContext-shaped function that
// re-resolves the host at connect time and refuses unsafe IPs. Wire
// it into http.Transport.DialContext on any client that fetches
// user-supplied URLs.
//
// The dial timeout (10 s) is conservative — most legitimate fetches
// resolve and connect well under that. Callers needing a different
// budget should construct their own dialer.
func DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ip, err := firstSafeIP(ctx, host)
	if err != nil {
		return nil, err
	}
	dialer := net.Dialer{Timeout: 10 * time.Second}
	return dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), port))
}

// IsUnsafeIP reports whether ip points somewhere we refuse to connect
// from a server-fetch path: loopback, RFC1918 private, link-local,
// multicast, or unspecified (0.0.0.0 / ::). nil counts as unsafe so
// callers can treat lookup failure conservatively.
func IsUnsafeIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast()
}

// CheckRedirect is a redirect-policy function for http.Client. It
// caps redirect depth at 5 and re-validates each hop's URL against
// ValidateURL — without this, a 302 can route around the initial
// validation by pointing at a private IP.
func CheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return fmt.Errorf("too many redirects")
	}
	if _, err := ValidateURL(req.URL.String()); err != nil {
		return err
	}
	return nil
}

// firstSafeIP resolves host (or accepts a literal IP) and returns the
// first address that's NOT classified unsafe. Used by both ValidateURL
// (best-effort fast-fail) and DialContext (TOCTOU-closed re-resolve).
//
// IP literals: parsed directly and checked.
// Hostnames: net.DefaultResolver.LookupIPAddr with a 5s timeout.
func firstSafeIP(ctx context.Context, host string) (net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		if IsUnsafeIP(ip) {
			return nil, fmt.Errorf("url resolves to a private or reserved address")
		}
		return ip, nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(lookupCtx, host)
	if err != nil {
		return nil, fmt.Errorf("url host lookup failed: %w", err)
	}
	for _, addr := range addrs {
		if !IsUnsafeIP(addr.IP) {
			return addr.IP, nil
		}
	}
	return nil, fmt.Errorf("url resolves to a private or reserved address")
}
