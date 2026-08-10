package initialize

import (
	"net/http"

	"server/middleware"
	"server/router"

	"github.com/gin-gonic/gin"
)

// These mount functions are the composition-root representation of product
// surfaces. Current URLs, handlers and credential behavior remain unchanged;
// the separation lets each surface migrate to its target credential policy
// without another all-in-one router rewrite.

func mountHealthSurface(group *gin.RouterGroup) {
	group.GET("/api/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, "ok")
	})
}

func mountPortalPublicSurface(group *gin.RouterGroup) {
	routers := router.RouterGroupApp
	routers.Auth.InitAuthRouter(group)
	routers.Blog.InitBlogRouter(group)
	routers.Prompt.InitPromptRouter(group)
	routers.Seo.InitSeoRouter(group)
	routers.UseCase.InitUseCaseRouter(group)
}

func mountProviderCallbackSurface(group *gin.RouterGroup) {
	router.RouterGroupApp.Callback.InitCallbackRouter(group)
}

func mountDesktopResourceSurface(group *gin.RouterGroup) {
	desktop := router.RouterGroupApp.Desktop
	desktop.DesktopAgentRouter.InitDesktopAgentRouter(group)
	desktop.DesktopLoginRouter.InitDesktopLoginRouter(group)
	desktop.DesktopModelsRouter.InitDesktopModelsRouter(group)
	desktop.DesktopOauthRouter.InitDesktopOauthRouter(group)
	desktop.DesktopSyncRouter.InitDesktopSyncRouter(group)
	desktop.DesktopVersionRouter.InitDesktopVersionRouter(group)
}

func mountLegacyAgentPublicSurface(group *gin.RouterGroup) {
	tools := router.RouterGroupApp.Tools
	tools.WorkAgentRouter.AIChatRouter.InitPublicAIChatRouter(group)
	tools.CanvasRouter.InitCanvasPublicRouter(group)
}

func mountMonitorSurface(group *gin.RouterGroup) {
	router.RouterGroupApp.Monitor.InitMonitorRouter(group)
}

func mountPortalAuthenticatedSurface(group *gin.RouterGroup) {
	routers := router.RouterGroupApp
	routers.Dashboard.InitDashboardRouter(group)
	routers.Account.InitAccountRouter(group)
	routers.Common.InitNotificationRouter(group)
}

func mountLegacyAgentAuthenticatedSurface(group *gin.RouterGroup) {
	tools := router.RouterGroupApp.Tools
	tools.GeneratorRouter.InitGeneratorRouter(group)
	tools.VideoGeneratorRouter.InitVideoGeneratorRouter(group)
	tools.UpscalerRouter.InitUpscalerRouter(group)
	tools.VectorizerRouter.InitVectorizerRouter(group)
	tools.AvatarsRouter.InitAvatarsRouter(group)
	tools.LoraRouter.InitLoraRouter(group)
	tools.RemoverRouter.InitRemoverRouter(group)
	tools.CanvasRouter.InitCanvasRouter(group)
	tools.PromptBuilderRouter.InitPromptBuilderRouter(group)
	tools.VideoPromptRewriterRouter.InitVideoPromptRewriterRouter(group)
	tools.CharacterRouter.InitCharacterRouter(group)
	tools.AssetLibraryRouter.InitAssetLibraryRouter(group)
	tools.TeamRouter.InitTeamRouter(group)
	tools.GlobalCatalogRouter.InitGlobalCatalogRouter(group)
	tools.WorkAgentRouter.AIChatRouter.InitAIChatRouter(group)
}

// mountLegacyAgentDesktopSharedSurface carries the two Agent routes the
// Desktop client calls with a Desktop OAuth access token. They keep their
// current URLs and handlers; only the credential differs from the rest of the
// Agent surface, which is exactly why they are mounted separately.
func mountLegacyAgentDesktopSharedSurface(group *gin.RouterGroup) {
	router.RouterGroupApp.Tools.WorkAgentRouter.AIChatRouter.InitDesktopSharedAIChatRouter(group)
}

func mountAdminSurface(group *gin.RouterGroup) {
	admin := router.RouterGroupApp.Admin
	admin.InitAdminDashboardRouter(group)
	admin.InitAdminUserRouter(group)
	admin.InitAdminOrderRouter(group)
	admin.InitAdminSystemRouter(group)
	admin.InitAdminBlogRouter(group)
	admin.InitAdminFeedbackRouter(group)
	admin.InitAdminMarketingRouter(group)
	admin.InitAdminUseCaseRouter(group)
	admin.InitAdminGeneratorRouter(group)
	admin.InitAdminAgentAccountRouter(group)
	admin.InitAdminThreadRouter(group)
	admin.InitAdminMessageRouter(group)
	admin.InitAdminPromptRouter(group)
	admin.InitAdminWorkAgentKnowledgeRouter(group)
}

func newPortalAuthenticatedGroup(engine *gin.Engine) *gin.RouterGroup {
	group := engine.Group("")
	group.Use(middleware.JWTAuth())
	return group
}

func newLegacyAgentAuthenticatedGroup(engine *gin.Engine) *gin.RouterGroup {
	group := engine.Group("")
	// Compatibility only: legacy Agent routes still accept the generic JWT
	// until Desktop parity and AgentResourceAuth(agent.run) are complete.
	group.Use(middleware.JWTAuth())
	return group
}

func newMonitorGroup(engine *gin.Engine) *gin.RouterGroup {
	group := engine.Group("")
	group.Use(middleware.MonitorAuth())
	return group
}

func newAdminGroup(engine *gin.Engine) *gin.RouterGroup {
	group := engine.Group("")
	group.Use(middleware.JWTAuth(), middleware.AdminAuth())
	return group
}
