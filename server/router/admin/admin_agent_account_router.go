package admin

import (
	"github.com/gin-gonic/gin"
	"server/api"
)

func (s *AdminAgentAccountRouter) InitAdminAgentAccountRouter(router *gin.RouterGroup) {
	adminApi := api.ApiGroupApp.AdminApiGroup
	accountRouter := router.Group("/api/admin/agent-accounts")
	{
		accountRouter.GET("", adminApi.AdminAgentAccountApi.GetAgentAccountList)
		// /health must come BEFORE /:id so gin doesn't match "health"
		// as an account id and route to GetAgentAccountDetail.
		accountRouter.GET("/health", adminApi.AdminAgentAccountApi.GetAgentPoolHealth)
		accountRouter.GET("/:id", adminApi.AdminAgentAccountApi.GetAgentAccountDetail)
		accountRouter.POST("", adminApi.AdminAgentAccountApi.CreateAgentAccount)
		accountRouter.PUT("/:id", adminApi.AdminAgentAccountApi.UpdateAgentAccount)
		accountRouter.DELETE("/:id", adminApi.AdminAgentAccountApi.DeleteAgentAccount)
		accountRouter.POST("/:id/switch", adminApi.AdminAgentAccountApi.SwitchAgentAccount)
		accountRouter.POST("/:id/test", adminApi.AdminAgentAccountApi.TestAgentAccount)
		accountRouter.GET("/:id/stats", adminApi.AdminAgentAccountApi.GetAgentAccountStats)
	}
}
