package tools

import (
	"path"
	api "server/api"
	"server/globals"
	"server/middleware"
	"strings"

	"github.com/gin-gonic/gin"
)

type GlobalCatalogRouter struct{}

func (r *GlobalCatalogRouter) InitGlobalCatalogRouter(router *gin.RouterGroup) {
	globalApi := api.ApiGroupApp.ToolsApiGroup.GlobalCatalogApi

	prefix := strings.TrimSpace(globals.GraConf.System.RouterPrefix)
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		prefix = "api"
	}

	Router := router.Group(path.Join(prefix, "global"))
	{
		Router.GET("/models", globalApi.ListModels)
		Router.GET("/models/:modelId", globalApi.GetModel)
		Router.GET("/assets", globalApi.ListAssets)
		Router.GET("/assets/audit", middleware.AdminAuth(), globalApi.AuditAssets)
		// Static-path routes (audit, by-source) precede the
		// `/:id` parameter route in source order so Gin's radix
		// tree resolves them deterministically — `/by-source`
		// and `/audit` would otherwise be caught by `:id` if the
		// :id route were registered first.
		Router.GET("/assets/by-source", globalApi.ResolveAssetBySource)
		Router.GET("/assets/:id", globalApi.GetAsset)
	}
}
