package admin

import (
	"github.com/gin-gonic/gin"
	"server/api"
)

func (s *AdminMessageRouter) InitAdminMessageRouter(router *gin.RouterGroup) {
	adminApi := api.ApiGroupApp.AdminApiGroup
	messageRouter := router.Group("/api/admin/chat-messages")
	{
		messageRouter.GET("", adminApi.AdminMessageApi.GetChatMessageList)
		messageRouter.GET("/:id", adminApi.AdminMessageApi.GetChatMessageDetail)
		messageRouter.DELETE("/:id", adminApi.AdminMessageApi.DeleteChatMessage)
		messageRouter.POST("/batch-delete", adminApi.AdminMessageApi.BatchDeleteChatMessage)
	}
}
