package desktop

import (
	api "server/api"
	"server/middleware"
	oauthmodel "server/model/desktop/oauth"

	"github.com/gin-gonic/gin"
)

// DesktopAgentRouter mounts Desktop-only Agent resource mutations. The
// browser-era Work Agent routes remain separate. Because this route creates a
// durable resource, it requires the complete signed Desktop resource envelope
// (client, audience, credential type, scope and device-session claims) instead
// of the compatibility middleware's client-ID-only admission.
type DesktopAgentRouter struct{}

func (DesktopAgentRouter) InitDesktopAgentRouter(router *gin.RouterGroup) {
	group := router.Group("api/desktop/agent")
	group.Use(middleware.OAuthBearerAuthWithPolicy(
		middleware.DesktopResourceBearerPolicy(oauthmodel.DesktopClientID),
	))
	group.Use(middleware.DesktopClientInfo())

	apis := api.ApiGroupApp.DesktopApiGroup.AgentApi
	group.PUT("/threads/:uuid", apis.PutThread)
}
