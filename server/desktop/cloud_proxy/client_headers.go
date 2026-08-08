//go:build desktop

package cloud_proxy

import (
	"net/http"

	"server/desktop/buildinfo"
)

// Client identification headers stamped on every cloud-bound request.
// Centralized here so the 5+ call sites can't drift:
//
//   - X-WorkMax-Client          identifies the client family for
//     cloud-side routing + access logs.
//   - X-WorkMax-Client-Version  identifies the running sidecar build.
//     Lets ops see the version distribution
//     in access logs and gives us a hook to
//     refuse / migrate stale clients on wire-
//     shape changes (none yet, but the header
//     is the prerequisite).
const (
	HeaderClientName    = "X-WorkMax-Client"
	HeaderClientVersion = "X-WorkMax-Client-Version"

	clientNameDesktop = "desktop"
)

// SetClientHeaders stamps the standard client identification headers
// on outbound cloud requests. Call from every path that builds an
// *http.Request — never construct these headers ad-hoc.
//
// The function takes an http.Header (not *http.Request) so callers
// can also stamp http.Request.Trailer or a manually-built header map
// (the SSE relay does this for upstream proxy requests).
func SetClientHeaders(h http.Header) {
	h.Set(HeaderClientName, clientNameDesktop)
	h.Set(HeaderClientVersion, buildinfo.Version)
}
