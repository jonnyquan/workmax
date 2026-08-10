package desktop

import (
	api "server/api"
	"server/middleware"
	oauthmodel "server/model/desktop/oauth"

	"github.com/gin-gonic/gin"
)

// DesktopModelsRouter mounts the conversation-model catalog at
// GET /api/desktop/models.
//
// Credential is identical to /api/desktop/sync/* — Desktop OAuth Bearer, no
// cookies, no legacy Portal JWT. The catalog is a per-caller entitlement
// answer, so it must be bound to the same device-session grant the rest of the
// Desktop surface uses; a shared/anonymous cache of it would be wrong for the
// next user on that machine.
type DesktopModelsRouter struct{}

// InitDesktopModelsRouter registers the /api/desktop/models group.
// Pass an unauthenticated group; this router applies its own OAuth Bearer
// middleware, matching DesktopSyncRouter.
func (DesktopModelsRouter) InitDesktopModelsRouter(router *gin.RouterGroup) {
	g := router.Group("api/desktop")
	g.Use(middleware.OAuthBearerAuth(oauthmodel.DesktopClientID))
	g.Use(middleware.DesktopClientInfo())

	apis := api.ApiGroupApp.DesktopApiGroup.ModelCatalogApi
	g.GET("/models", apis.ListModels)
}
