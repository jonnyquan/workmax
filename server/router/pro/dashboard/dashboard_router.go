package dashboard

import (
	api "server/api"

	"github.com/gin-gonic/gin"
)

type DashboardRouter struct {
}

func (n *DashboardRouter) InitDashboardRouter(router *gin.RouterGroup) {
	dashboardRouter := router.Group("api/dashboard")
	dashboardApi := api.ApiGroupApp.DashboardApiGroup.DashboardApi
	{
		dashboardRouter.GET("/space-storage/summary", dashboardApi.GetSpaceStorageSummary)
		dashboardRouter.GET("/space-storage/assets", dashboardApi.ListSpaceStorageAssets)
		dashboardRouter.GET("/space-storage/containers", dashboardApi.ListSpaceStorageContainers)
		dashboardRouter.PATCH("/space-storage/assets/generated/:id/visibility", dashboardApi.UpdateGeneratedAssetVisibility)
		dashboardRouter.PATCH("/space-storage/containers/generated/visibility", dashboardApi.UpdateGeneratedContainerVisibility)
		dashboardRouter.POST("/space-storage/assets/delete-batch", dashboardApi.DeleteSpaceStorageAssets)
		dashboardRouter.DELETE("/space-storage/assets/:source/:id", dashboardApi.DeleteSpaceStorageAsset)
	}
}
