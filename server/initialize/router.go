package initialize

import (
	"server/api"
	desktopAgentApi "server/api/desktop/agent"
	desktopLoginApi "server/api/desktop/login"
	desktopModelsApi "server/api/desktop/models"
	desktopOauthApi "server/api/desktop/oauth"
	desktopSyncApi "server/api/desktop/sync"
	"server/globals"
	"server/model/common/response"

	"github.com/gin-gonic/gin"
)

func Routers() *gin.Engine {
	// 使用 gin.New() 而不是 gin.Default() 来完全控制中间件
	Router := gin.New()
	// Safe default for an independently deployed open-source Server: never
	// trust caller-supplied forwarding headers. Deployments behind a reverse
	// proxy must add an explicit CIDR configuration before enabling forwarded
	// client IPs; trusting Gin's default all-proxies list lets an attacker evade
	// every ClientIP-based login rate limit with a forged X-Forwarded-For.
	if err := Router.SetTrustedProxies(nil); err != nil {
		panic("disable implicit trusted proxies: " + err.Error())
	}
	Router.Use(gin.Logger())
	Router.Use(gin.Recovery())
	Router.MaxMultipartMemory = 10 << 20

	// Desktop Renderer traffic terminates at the loopback Sidecar, not directly
	// at this cloud Router. Do not install a repository-wide browser CORS
	// allowlist for retired Web/Admin clients. Any future browser-facing protocol
	// must own a route-scoped origin policy and its tests.

	// 自定义404
	Router.NoRoute(func(c *gin.Context) {
		response.NotFound(c)
	})

	// Wire desktop OAuth services (P-1.4+). The handler struct
	// carries DB + service instances rather than reading globals
	// per request — so we construct it once here, after globals.GraDBs
	// is populated.
	systemDB := globals.GraDBs["system"]
	api.ApiGroupApp.DesktopApiGroup.AgentApi = desktopAgentApi.NewThreadApi(systemDB)
	api.ApiGroupApp.DesktopApiGroup.ModelCatalogApi = desktopModelsApi.NewModelCatalogApi(systemDB)
	api.ApiGroupApp.DesktopApiGroup.OauthApi = desktopOauthApi.NewOauthApi(systemDB)
	api.ApiGroupApp.DesktopApiGroup.SyncApi = desktopSyncApi.NewSyncApi(systemDB)
	if systemDB == nil {
		// Route-catalog and offline composition tests intentionally do not open
		// a database. Keep the route registered and fail closed with 503 if it
		// is invoked. A real Server initializes the system DB before Routers.
		api.ApiGroupApp.DesktopApiGroup.LoginApi = &desktopLoginApi.LoginApi{}
	} else {
		loginAPI, err := desktopLoginApi.NewLoginApi(systemDB)
		if err != nil {
			panic("initialize Desktop Login Transaction API: " + err.Error())
		}
		api.ApiGroupApp.DesktopApiGroup.LoginApi = loginAPI
	}

	// Explicit product surfaces. Current paths and middleware remain stable;
	// separate groups make future credential-policy migration observable and
	// independently testable.
	mountHealthSurface(Router.Group(""))
	mountPortalPublicSurface(Router.Group(""))
	mountProviderCallbackSurface(Router.Group(""))
	mountDesktopResourceSurface(Router.Group(""))
	mountLegacyAgentPublicSurface(Router.Group(""))
	mountMonitorSurface(newMonitorGroup(Router))
	mountPortalAuthenticatedSurface(newPortalAuthenticatedGroup(Router))
	mountLegacyAgentAuthenticatedSurface(newLegacyAgentAuthenticatedGroup(Router))
	mountLegacyAgentDesktopSharedSurface(Router.Group(""))
	mountAdminSurface(newAdminGroup(Router))

	return Router
}
