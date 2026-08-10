package desktop

import (
	api "server/api"
	"server/middleware"
	oauthmodel "server/model/desktop/oauth"

	"github.com/gin-gonic/gin"
)

// DesktopModelGatewayRouter mounts the bare-model proxy the Desktop's local
// tool loop calls:
//
//	POST /api/desktop/model-gateway/anthropic/v1/messages
//	POST /api/desktop/model-gateway/openai/v1/chat/completions
//
// Credential is identical to /api/desktop/models and /api/desktop/sync/*:
// Desktop OAuth Bearer, no cookies, no legacy Portal JWT. That is not a
// convenience — this endpoint spends platform money, so it must be bound to
// the same device-session grant the rest of the Desktop surface uses, and a
// weaker admission here would let one machine's session buy inference against
// another account's entitlement.
type DesktopModelGatewayRouter struct{}

// InitDesktopModelGatewayRouter registers the gateway group. Pass an
// unauthenticated group; this router applies its own OAuth Bearer middleware,
// matching DesktopModelsRouter and DesktopSyncRouter.
func (DesktopModelGatewayRouter) InitDesktopModelGatewayRouter(router *gin.RouterGroup) {
	g := router.Group("api/desktop/model-gateway")
	// Ordering is load-bearing: gin runs Use handlers in declaration order,
	// so the alias must be installed BEFORE the bearer middleware reads
	// Authorization. The packaged Anthropic engine sends its credential as
	// `x-api-key`; that credential is the SAME Desktop OAuth access token and
	// is validated identically — this is a header rename, not a second
	// admission path, so no policy is relaxed. Without it the engine would
	// need a bespoke transport just to move one header.
	g.Use(promoteAPIKeyHeader())
	g.Use(middleware.OAuthBearerAuth(oauthmodel.DesktopClientID))
	g.Use(middleware.DesktopClientInfo())

	apis := api.ApiGroupApp.DesktopApiGroup.ModelGatewayApi
	g.POST("/anthropic/v1/messages", apis.AnthropicMessages)
	g.POST("/openai/v1/chat/completions", apis.OpenAIChatCompletions)
}

// promoteAPIKeyHeader copies a bare `x-api-key` into `Authorization: Bearer`
// when Authorization is absent. The existing OAuth validation is still what
// admits the request — this only decides which header it reads the token out
// of.
//
// It never overwrites a present Authorization header: a request carrying both
// must be judged on the one it explicitly chose, not on whichever we happen
// to prefer.
func promoteAPIKeyHeader() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetHeader("Authorization") == "" {
			if key := c.GetHeader("x-api-key"); key != "" {
				c.Request.Header.Set("Authorization", "Bearer "+key)
			}
		}
		c.Next()
	}
}
