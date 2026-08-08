package admin

import (
	"github.com/gin-gonic/gin"
	"server/api"
)

func (s *AdminThreadRouter) InitAdminThreadRouter(router *gin.RouterGroup) {
	adminApi := api.ApiGroupApp.AdminApiGroup
	threadRouter := router.Group("/api/admin/chat-threads")
	{
		threadRouter.GET("", adminApi.AdminThreadApi.GetChatThreadList)
		threadRouter.GET("/:id", adminApi.AdminThreadApi.GetChatThreadDetail)
		threadRouter.DELETE("/:id", adminApi.AdminThreadApi.DeleteChatThread)
		threadRouter.POST("/batch-delete", adminApi.AdminThreadApi.BatchDeleteChatThread)
	}
}
