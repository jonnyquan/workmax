package desktop

import (
	api "server/api"
	"server/middleware"

	"github.com/gin-gonic/gin"
)

// DesktopVersionRouter mounts the version-discovery endpoint at
// /api/desktop/version. PUBLIC (no JWTAuth) — a sidecar too stale
// to complete OAuth still needs to learn it's stale, so gating
// behind auth would chicken-and-egg the upgrade prompt.
//
// Goes through the DesktopClientInfo middleware so the
// [desktop-client] log line still fires (gives ops visibility on
// who's polling the version endpoint with what version themselves).
type DesktopVersionRouter struct{}

func (DesktopVersionRouter) InitDesktopVersionRouter(router *gin.RouterGroup) {
	g := router.Group("api/desktop")
	g.Use(middleware.DesktopClientInfo())
	apis := api.ApiGroupApp.DesktopApiGroup.VersionApi
	g.GET("/version", apis.Get)
}
