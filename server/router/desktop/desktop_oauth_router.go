package desktop

import (
	api "server/api"
	"server/middleware"
	oauthmodel "server/model/desktop/oauth"
	"time"

	"github.com/gin-gonic/gin"
)

// DesktopOauthRouter mounts the OAuth Authorization Server endpoints
// at /api/desktop/oauth/*. These are PUBLIC (no JWTAuth middleware) —
// the GET /authorize handler does its own JWT lookup because it
// needs to differentiate "not logged in" (render HTML) from
// "logged in" (render consent), which middleware-based 401 can't do.
//
// POST /authorize/consent is also intentionally public: the user
// session is identified via the pending_authorization ID (server-side
// state) rather than a re-check of the JWT. P1 can tighten if needed.
type DesktopOauthRouter struct{}

func (DesktopOauthRouter) InitDesktopOauthRouter(router *gin.RouterGroup) {
	g := router.Group("api/desktop/oauth")
	// Capture the desktop client info (name + version) so OAuth flows
	// also show up in ops logs with their sidecar version. Public
	// group — no JWTAuth here, but the X-WorkMax-Client headers are sent
	// regardless (set by cloud_proxy/client_headers.go::SetClientHeaders).
	g.Use(middleware.DesktopClientInfo())
	apis := api.ApiGroupApp.DesktopApiGroup.OauthApi
	tokenRateLimit := middleware.RateLimit(10, time.Minute)
	revokeRateLimit := middleware.RateLimit(10, time.Minute)
	authorizeRateLimit := middleware.RateLimit(30, time.Minute)
	consentRateLimit := middleware.RateLimit(30, time.Minute)
	{
		g.GET("/authorize", authorizeRateLimit, apis.Authorize)
		g.POST("/authorize/consent", consentRateLimit, apis.Consent)
		g.POST("/token", tokenRateLimit, apis.Token)
		g.GET("/userinfo", middleware.OAuthBearerAuth(oauthmodel.DesktopClientID), apis.UserInfo)
		g.POST("/revoke", revokeRateLimit, apis.Revoke) // P1: RFC 7009 token revocation
	}
}
