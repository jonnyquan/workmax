package admin

import (
	api "server/api"

	"github.com/gin-gonic/gin"
)

type AdminDashboardRouter struct{}

func (s *AdminDashboardRouter) InitAdminDashboardRouter(router *gin.RouterGroup) {
	adminDashboardRouter := router.Group("api")
	adminDashboardApi := api.ApiGroupApp.AdminApiGroup.AdminDashboardApi
	{
		// Optimized split endpoints for better performance
		adminDashboardRouter.GET("/admin/dashboard/getBasicStatistics", adminDashboardApi.GetBasicStatistics)
		adminDashboardRouter.GET("/admin/dashboard/getCoreBusinessStats", adminDashboardApi.GetCoreBusinessStats)
		adminDashboardRouter.GET("/admin/dashboard/getToolsStatistics", adminDashboardApi.GetToolsStatistics)
		adminDashboardRouter.GET("/admin/dashboard/getUserRegionData", adminDashboardApi.GetUserRegionData)
		adminDashboardRouter.GET("/admin/dashboard/getRecentOrdersData", adminDashboardApi.GetRecentOrdersData)
		adminDashboardRouter.GET("/admin/dashboard/getTrendData", adminDashboardApi.GetTrendData)
	}
}
