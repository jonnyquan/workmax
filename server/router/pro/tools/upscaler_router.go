package tools

import (
	"path"
	"server/api"
	"server/globals"
	"strings"

	"github.com/gin-gonic/gin"
)

type UpscalerRouter struct{}

func (r *UpscalerRouter) InitUpscalerRouter(router *gin.RouterGroup) {
	upscalerApi := api.ApiGroupApp.ToolsApiGroup.UpscalerApi

	prefix := strings.TrimSpace(globals.GraConf.System.RouterPrefix)
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		prefix = "api"
	}

	// Group: /api/tools/upscaler
	Router := router.Group(path.Join(prefix, "tools", "upscaler"))
	{
		Router.GET("/credits", upscalerApi.GetCredits)
		Router.POST("/generate", upscalerApi.Generate)
		Router.GET("/tasks/active", upscalerApi.GetActiveTasks)
		Router.GET("/tasks", upscalerApi.GetTasks)
		Router.GET("/history", upscalerApi.GetHistory)
		Router.DELETE("/history/:id", upscalerApi.DeleteHistoryRecord)
		Router.POST("/task/:taskId/cancel", upscalerApi.CancelTask)
		Router.POST("/task/:taskId/retry", upscalerApi.RetryTask)
	}
}
