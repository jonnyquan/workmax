//go:build desktop

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Opening a link without losing the app.
//
// macOS gives Wails no cancellable navigation hook (decidePolicyForNavigation-
// Action is implemented for iOS only in v3.0.0-beta.5), so a plain anchor in
// the renderer navigates the window itself. Because the UI origin serves only
// what is under the capability path, that navigation replaces the application
// with a remote page and there is no way back. The renderer therefore has to
// stop the navigation before it starts, and hand the URL to Go.
//
// Electron did the same thing from the other side — setWindowOpenHandler
// denied everything and routed through shell.openExternal after the same
// validation. The validation is ported rather than reinvented: it is what
// keeps "open this link" from becoming a request to something on the user's
// own network.

// uiOpenExternalPath is the capability-protected endpoint the renderer posts
// a URL to. It is mounted alongside the API proxy, so reaching it requires
// already knowing the page's own URL.
const uiOpenExternalPath = "/open-external"

// externalURLOpener is what actually reaches the system browser. A field so
// tests can assert what would have been opened without opening it.
type externalURLOpener func(url string) error

// newOpenExternalHandler validates and opens. It answers with a small JSON
// body so the renderer can tell "refused" from "failed to launch" — those want
// different messages in the UI.
func newOpenExternalHandler(open externalURLOpener) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "open-external expects POST", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
			writeExternalResult(w, http.StatusBadRequest, "invalid_request")
			return
		}
		target, err := normalizeExternalHTTPURL(body.URL)
		if err != nil {
			// Deliberately terse to the renderer, specific in the log: the
			// page does not need to learn which private range it just probed.
			log.Printf("open-external: refused %q: %v", truncate(body.URL, 120), err)
			writeExternalResult(w, http.StatusForbidden, "refused")
			return
		}
		if err := open(target); err != nil {
			log.Printf("open-external: launch failed for %q: %v", target, err)
			writeExternalResult(w, http.StatusBadGateway, "launch_failed")
			return
		}
		writeExternalResult(w, http.StatusOK, "opened")
	})
}

func writeExternalResult(w http.ResponseWriter, status int, result string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"result": result})
}

// normalizeExternalHTTPURL accepts only links that are unambiguously somewhere
// else on the internet, and returns the canonical form to open.
//
// Ported from the Electron build's security-helpers.ts, same rules:
//
//   - http and https only. Anything else is a way to reach a local handler —
//     file:, and the custom schemes other installed apps register.
//   - No credentials in the URL. A link the model produced should not be able
//     to make the browser send a username and password anywhere.
//   - Nothing local or private. This is the SSRF rule: the app can reach the
//     user's own network, so "open this link" must not become a way to make it
//     do so on someone else's behalf.
func normalizeExternalHTTPURL(raw string) (string, error) {
	if strings.TrimSpace(raw) != raw || raw == "" {
		return "", fmt.Errorf("empty or padded URL")
	}
	target, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("unparseable: %w", err)
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return "", fmt.Errorf("scheme %q is not http or https", target.Scheme)
	}
	if target.User != nil {
		return "", fmt.Errorf("URL carries credentials")
	}
	host := strings.ToLower(target.Hostname())
	if host == "" {
		return "", fmt.Errorf("no host")
	}
	if isLocalOrPrivateHost(host) {
		return "", fmt.Errorf("host %q is local or private", host)
	}
	return target.String(), nil
}

// isLocalOrPrivateHost mirrors the Electron helper, but decides on parsed IPs
// rather than string patterns where it can: the patterns were the part most
// likely to miss a spelling (0x7f.1, ::ffff:127.0.0.1, and so on).
func isLocalOrPrivateHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		// A name, and not one of the local ones. Whether it resolves somewhere
		// private is not knowable here without resolving it, which would itself
		// be a request this endpoint should not make.
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() ||
		ip.IsInterfaceLocalMulticast()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
