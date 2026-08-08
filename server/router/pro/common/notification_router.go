package common

import (
	api "server/api"

	"github.com/gin-gonic/gin"
)

type NotificationRouter struct {
}

func (n *NotificationRouter) InitNotificationRouter(router *gin.RouterGroup) {
	notificationRouter := router.Group("api/common/notification")
	notificationApi := api.ApiGroupApp.CommonApiGroup.NotificationApi
	{
		notificationRouter.GET("/count", notificationApi.GetNotificationCount)
		notificationRouter.GET("/list", notificationApi.GetNotificationList)
		notificationRouter.GET("/detail", notificationApi.GetNotificationDetail)
		notificationRouter.POST("/read", notificationApi.MarkNotificationRead)
		notificationRouter.POST("/read-all", notificationApi.MarkAllNotificationsRead)
	}
}
