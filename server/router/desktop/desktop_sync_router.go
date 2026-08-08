package desktop

import (
	api "server/api"
	"server/middleware"
	oauthmodel "server/model/desktop/oauth"

	"github.com/gin-gonic/gin"
)

// DesktopSyncRouter mounts the desktop client's pull-sync HTTP
// endpoints at /api/desktop/sync/*. Per cloud-sync.md §5.1 all
// sync routes require OAuth Bearer auth from the desktop refresh
// chain. Web cookies and legacy non-desktop JWTs are intentionally
// rejected so this API stays desktop-client-only.
//
// P1.A has landed the core routes here (threads, single thread,
// messages). render_jobs and thread_files stay deferred until the
// desktop renderer has a consumer surface for them.
//
// The group is mounted on the explicit Desktop Resource surface in
// server/initialize/router_surfaces.go. It applies its own OAuth bearer
// policy; the parent composition group is intentionally unauthenticated.
type DesktopSyncRouter struct{}

// InitDesktopSyncRouter registers the /api/desktop/sync/* group.
// Pass an unauthenticated group; this router applies the desktop
// OAuth Bearer middleware itself.
//
// Routes registered here:
//   - GET /threads               (P1.A.2 — incremental threads)
//   - GET /threads/:id           (P1.A.3 — single thread full fetch)
//   - GET /messages              (P1.A.4 partial — incremental messages)
//
// Deferred until there is a renderer consumer:
//   - GET /render_jobs
//   - GET /thread_files
func (DesktopSyncRouter) InitDesktopSyncRouter(router *gin.RouterGroup) {
	g := router.Group("api/desktop/sync")
	g.Use(middleware.OAuthBearerAuth(oauthmodel.DesktopClientID))
	// Capture X-WorkMax-Client-Version into Context + log it so ops can
	// see sidecar-version distribution. No-op if the header is
	// missing (curl smoke tests stay quiet).
	g.Use(middleware.DesktopClientInfo())

	apis := api.ApiGroupApp.DesktopApiGroup.SyncApi
	{
		g.GET("/threads", apis.ListThreads)   // P1.A.2 — incremental threads
		g.GET("/threads/:id", apis.GetThread) // P1.A.3 — single thread full-fetch
		g.GET("/messages", apis.ListMessages) // P1.A.4 (partial) — incremental messages
		// Future routes:
		//   g.GET("/render_jobs",    apis.ListRenderJobs)  (P1.A.4 cont.)
		//   g.GET("/thread_files",   apis.ListThreadFiles) (P1.A.4 cont.)
	}
}
