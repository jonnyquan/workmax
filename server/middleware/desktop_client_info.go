package middleware

import (
	"fmt"
	"strconv"
	"strings"

	"server/globals"

	"github.com/gin-gonic/gin"
)

// Header names — must match the sidecar's cloud_proxy/client_headers.go.
// Duplicated here rather than imported because that package is
// //go:build desktop and the cloud router compiles without that tag.
const (
	headerWorkMaxClientName    = "X-WorkMax-Client"
	headerWorkMaxClientVersion = "X-WorkMax-Client-Version"
	maxDesktopClientNameBytes  = 32
	maxDesktopVersionBytes     = 128
)

// Context keys for downstream handlers that want to read the
// captured client info without re-parsing headers.
const (
	ContextKeyWorkMaxClientName    = "workmax_client_name"
	ContextKeyWorkMaxClientVersion = "workmax_client_version"
)

// DesktopMinSupportedVersion is the floor at which the cloud
// considers a sidecar "recent enough". Below this, requests still
// succeed (warn-only enforcement) but a structured warning is
// emitted so ops can see which clients are stuck and decide whether
// to flip a hard-block flag.
//
// Format: semver-ish ("MAJOR.MINOR.PATCH[-pre]"). Pre-release suffix
// is ignored for comparison purposes; the comparison is over the
// numeric triple. Empty / unparseable client versions are skipped
// (treat as "old client we can't classify" rather than warn-spam).
//
// Bumping policy: only raise this when a real wire-shape change
// requires older clients to upgrade. Don't bump for every release.
// CI release notes should call out "DesktopMinSupportedVersion
// bumped from X to Y" as a sidecar-upgrade requirement.
var DesktopMinSupportedVersion = "0.0.3"

// DesktopClientInfo captures the X-WorkMax-Client and
// X-WorkMax-Client-Version headers stamped by the desktop sidecar
// (server/desktop/cloud_proxy/client_headers.go::SetClientHeaders).
//
// Why this exists: the sidecar already sends the version on every
// cloud-bound request, but without cloud-side capture the header
// only shows up in raw access logs (often not retained). Surfacing
// it into gin Context + emitting a structured log line means:
//   - ops can grep for "[desktop-client]" to see which sidecar
//     versions are calling production
//   - downstream handlers can refuse / migrate stale clients if a
//     wire-shape change makes that necessary
//
// Behavior:
//   - Header missing → silent no-op so non-desktop callers aren't
//     affected (someone might curl /api/desktop/* during a smoke
//     test; no need to noise the log).
//   - Header present → stash in Context, emit one info log line
//     keyed on path + name + version for ops visibility.
//   - Version < DesktopMinSupportedVersion → additionally emit a
//     "[desktop-stale]" WARN log line so ops can see distribution
//     of stale clients without grepping every request log. Does
//     NOT reject; gating policy (warn vs. soft-block vs. floor-
//     reject) is intentionally deferred until we have a concrete
//     wire-shape change forcing the decision.
func DesktopClientInfo() gin.HandlerFunc {
	return func(c *gin.Context) {
		nameValues := c.Request.Header.Values(headerWorkMaxClientName)
		versionValues := c.Request.Header.Values(headerWorkMaxClientVersion)
		if len(nameValues) > 1 || len(versionValues) > 1 {
			c.Next()
			return
		}
		clientName := firstHeaderValue(nameValues)
		clientVersion := firstHeaderValue(versionValues)
		if clientName == "" && clientVersion == "" {
			c.Next()
			return
		}
		if !validDesktopClientInfoValue(clientName, maxDesktopClientNameBytes) ||
			!validDesktopClientInfoValue(clientVersion, maxDesktopVersionBytes) {
			// These headers are telemetry, never credentials. Ignore malformed
			// caller input instead of reflecting it into logs or request context.
			c.Next()
			return
		}
		c.Set(ContextKeyWorkMaxClientName, clientName)
		c.Set(ContextKeyWorkMaxClientVersion, clientVersion)
		globals.Info(fmt.Sprintf("[desktop-client] path=%q client=%q version=%q",
			c.Request.URL.Path, clientName, clientVersion))

		if isVersionBelowFloor(clientVersion, DesktopMinSupportedVersion) {
			globals.Warn(fmt.Sprintf(
				"[desktop-stale] path=%q client=%q version=%q floor=%q — client is below the minimum supported version (warn-only; no rejection)",
				c.Request.URL.Path, clientName, clientVersion, DesktopMinSupportedVersion,
			))
		}
		c.Next()
	}
}

func firstHeaderValue(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func validDesktopClientInfoValue(value string, maxBytes int) bool {
	if len(value) > maxBytes {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < 0x20 || value[index] > 0x7e {
			return false
		}
	}
	return true
}

// isVersionBelowFloor reports whether `actual` is strictly less
// than `floor`. Both are parsed as MAJOR.MINOR.PATCH triples; any
// pre-release suffix after a "-" or "+" is stripped. Unparseable
// inputs return false ("don't warn about clients we can't classify")
// — the goal of the warn is to flag known-stale clients, not to
// noise the log every time someone curls without a version header
// or with a custom dev build like "main-dirty".
func isVersionBelowFloor(actual, floor string) bool {
	a, ok := parseSemverTriple(actual)
	if !ok {
		return false
	}
	f, ok := parseSemverTriple(floor)
	if !ok {
		return false
	}
	for i := 0; i < 3; i++ {
		if a[i] < f[i] {
			return true
		}
		if a[i] > f[i] {
			return false
		}
	}
	return false
}

// parseSemverTriple extracts the (major, minor, patch) numeric
// components from a semver-ish string. Strips any pre-release /
// build metadata suffix after the first "-" or "+". Returns
// ok=false if the result isn't exactly three numeric components.
func parseSemverTriple(v string) ([3]int, bool) {
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}
