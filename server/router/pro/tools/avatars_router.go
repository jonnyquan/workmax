package tools

import (
	"path"
	"server/api"
	"server/globals"
	"strings"

	"github.com/gin-gonic/gin"
)

type AvatarsRouter struct{}

func (r *AvatarsRouter) InitAvatarsRouter(router *gin.RouterGroup) {
	avatarsApi := api.ApiGroupApp.ToolsApiGroup.AvatarsApi

	prefix := strings.TrimSpace(globals.GraConf.System.RouterPrefix)
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		prefix = "api"
	}

	// Group: /api/tools/avatars
	Router := router.Group(path.Join(prefix, "tools", "avatars"))
	{
		Router.GET("/credits", avatarsApi.GetCredits)
		Router.GET("/presets", avatarsApi.GetScenePresets)
		Router.POST("/generate", avatarsApi.Generate)
		Router.GET("/tasks/active", avatarsApi.GetActiveTasks)
		Router.GET("/tasks", avatarsApi.GetTasks)
		Router.GET("/history", avatarsApi.GetHistory)
		Router.DELETE("/history/:id", avatarsApi.DeleteHistoryRecord)
		Router.POST("/task/:taskId/cancel", avatarsApi.CancelTask)
		Router.POST("/task/:taskId/retry", avatarsApi.RetryTask)
	}
}
