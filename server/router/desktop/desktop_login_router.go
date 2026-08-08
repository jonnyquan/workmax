package desktop

import (
	"time"

	api "server/api"
	"server/middleware"

	"github.com/gin-gonic/gin"
)

// DesktopLoginRouter mounts the Server-owned Desktop Login Transaction
// protocol. These routes intentionally do not accept the portal JWT/cookie or
// a Desktop OAuth access token: the create route bootstraps a transaction and
// the remaining routes authenticate with narrow, one-time capabilities.
//
// Renderer code must not call these cloud routes directly. The trusted
// Sidecar/Main login coordinator owns the transaction secret, exchange token,
// redirects and eventual OAuth authorization code.
type DesktopLoginRouter struct{}

func (DesktopLoginRouter) InitDesktopLoginRouter(router *gin.RouterGroup) {
	g := router.Group("api/v1/desktop/identity")
	g.Use(desktopLoginResponseHeaders(), middleware.DesktopClientInfo())
	apis := api.ApiGroupApp.DesktopApiGroup.LoginApi

	// IP buckets bound transaction churn and unauthenticated probing. Do not put
	// a raw transaction-id limiter before capability verification: an attacker
	// who learns only the public id could otherwise exhaust the legitimate
	// caller's bucket. Password attempts have an additional durable budget in
	// the transaction CAS after its secret has been verified.
	createByIP := middleware.RateLimit(10, time.Minute)
	statusByIP := middleware.RateLimit(120, time.Minute)
	passwordByIP := middleware.RateLimit(10, time.Minute)
	exchangeByIP := middleware.RateLimit(30, time.Minute)

	{
		g.POST("/login-transactions", createByIP, apis.Create)
		g.GET("/login-transactions/:id", statusByIP, apis.Status)
		g.POST("/login-transactions/:id/password", passwordByIP, apis.CompletePassword)
		g.POST("/login-transactions/:id/exchange", exchangeByIP, apis.Exchange)
	}
}

// desktopLoginResponseHeaders runs ahead of route-level rate limiters so even
// a 429 response cannot be cached or embedded by a browser intermediary.
func desktopLoginResponseHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Header("Pragma", "no-cache")
		c.Header("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Next()
	}
}
