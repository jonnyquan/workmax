package admin

import (
	api "server/api"

	"github.com/gin-gonic/gin"
)

type AdminGeneratorRouter struct{}

func (r *AdminGeneratorRouter) InitAdminGeneratorRouter(router *gin.RouterGroup) {
	generatorApi := api.ApiGroupApp.AdminApiGroup.AdminGeneratorApi

	Router := router.Group("api/admin/generator")
	{
		// Provider CRUD
		Router.GET("/providers", generatorApi.GetProviderList)
		Router.GET("/providers/:id", generatorApi.GetProvider)
		Router.POST("/providers", generatorApi.CreateProvider)
		Router.PUT("/providers/:id", generatorApi.UpdateProvider)
		Router.DELETE("/providers/:id", generatorApi.DeleteProvider)

		// Provider actions
		Router.POST("/providers/:id/toggle", generatorApi.ToggleProvider)
		Router.POST("/providers/reload", generatorApi.ReloadProviders)
		Router.GET("/providers/active", generatorApi.GetActiveProviders)

		// Storage operations
		Router.GET("/storage/summary", generatorApi.GetStorageSummary)
		Router.GET("/storage/objects", generatorApi.ListStorageObjects)
		Router.GET("/storage/objects/:id/download-url", generatorApi.GetStorageObjectDownloadURL)
		Router.POST("/storage/audit", generatorApi.AuditGenerationObjects)
		Router.POST("/storage/backfill", generatorApi.BackfillGenerationObjects)
		Router.POST("/storage/orphan-objects/cleanup", generatorApi.CleanupOrphanObjects)
	}
}
