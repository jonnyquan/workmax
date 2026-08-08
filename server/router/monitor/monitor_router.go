package monitor

import (
	api "server/api"

	"github.com/gin-gonic/gin"
)

type MonitorRouter struct{}

func (r *MonitorRouter) InitMonitorRouter(router *gin.RouterGroup) {
	monitorRouter := router.Group("api/internal/monitor")
	monitorApi := api.ApiGroupApp.MonitorApiGroup.MonitorApi
	{
		monitorRouter.GET("/summary", monitorApi.GetSummary)
		monitorRouter.GET("/trends", monitorApi.GetTrends)
		monitorRouter.GET("/drilldown", monitorApi.GetDrilldown)
	}
}
