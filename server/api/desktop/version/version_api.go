// Package version exposes GET /api/desktop/version — the cloud's
// declaration of which desktop-sidecar versions it currently supports.
//
// Reads the same DesktopMinSupportedVersion constant the warn-only
// middleware uses, so the cloud's version-floor surface is
// single-source. Renderer (and ops) can hit this to decide whether
// to nag the user to update.
//
// Public — no JWTAuth. A user who can't sign in (because their
// sidecar is too stale to OAuth) still needs to discover that fact;
// gating this behind auth would create a chicken-and-egg block.
package version

import (
	"net/http"
	"os"

	"server/middleware"

	"github.com/gin-gonic/gin"
)

// ReleaseNotesURLEnv lets ops point the desktop's update nag at a
// specific changelog page without a code change. Default is empty
// — the renderer's banner suppresses the "What's new" link when
// no URL is published. Production should set this to e.g.
//
//	WORKMAX_DESKTOP_RELEASE_NOTES_URL=https://workmax.app/desktop/changelog
//
// so an outdated user gets a one-click jump to the changelog.
const ReleaseNotesURLEnv = "WORKMAX_DESKTOP_RELEASE_NOTES_URL"

// VersionApi handles GET /api/desktop/version. No DB or per-request
// state — the response is computed from package-level constants
// (today: the middleware's DesktopMinSupportedVersion). Kept as a
// struct so the routing wire-up matches OauthApi / SyncApi shape.
type VersionApi struct{}

// versionResponse is the wire shape.
//
// Stable contract: renderer consumes both fields; CI release notes
// should mention any wire-shape change as a sidecar+renderer
// compatibility hazard. Add new fields rather than reshape.
type versionResponse struct {
	// MinSupported is the floor below which the cloud-side middleware
	// emits a [desktop-stale] WARN. Sidecars older than this should
	// prompt the user to upgrade; today this is warn-only on the
	// cloud, but renderer can act on the same signal.
	MinSupported string `json:"min_supported"`

	// LatestRecommended is the version users should see as "newest"
	// in upgrade prompts. Today identical to MinSupported because we
	// don't track a separate "latest" cadence — kept as a distinct
	// field so a future split (e.g. min=0.5.0 / latest=0.7.2) is a
	// no-op for renderers that already read it.
	LatestRecommended string `json:"latest_recommended"`

	// ReleaseNotesURL is an optional changelog link the renderer's
	// upgrade banner uses for "What's new". Empty when not
	// configured — renderer suppresses the link in that case so
	// users don't see a dead button.
	ReleaseNotesURL string `json:"release_notes_url,omitempty"`
}

// Get returns the current version envelope. Cache-Control: public,
// short-max-age — renderers can re-fetch every few minutes without
// hammering the endpoint, but a deploy that bumps the floor still
// surfaces to live clients within the cache window.
func (VersionApi) Get(c *gin.Context) {
	c.Header("Cache-Control", "public, max-age=60")
	c.JSON(http.StatusOK, versionResponse{
		MinSupported:      middleware.DesktopMinSupportedVersion,
		LatestRecommended: middleware.DesktopMinSupportedVersion,
		ReleaseNotesURL:   os.Getenv(ReleaseNotesURLEnv),
	})
}
