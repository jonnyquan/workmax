package tools

import (
	"path"
	api "server/api"
	"server/globals"
	"strings"

	"github.com/gin-gonic/gin"
)

type LoraRouter struct{}

func (r *LoraRouter) InitLoraRouter(router *gin.RouterGroup) {
	loraApi := api.ApiGroupApp.ToolsApiGroup.LoraApi

	prefix := strings.TrimSpace(globals.GraConf.System.RouterPrefix)
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		prefix = "api"
	}

	// Group: /api/tools/lora
	Router := router.Group(path.Join(prefix, "tools", "lora"))
	{
		Router.GET("/credits", loraApi.GetCredits)
		Router.POST("/generate", loraApi.Generate)
		Router.GET("/tasks/active", loraApi.GetActiveTasks)
		Router.GET("/tasks", loraApi.GetTasks)
		Router.GET("/history", loraApi.GetHistory)
		Router.DELETE("/history/:id", loraApi.DeleteHistoryRecord)
		Router.POST("/task/:taskId/cancel", loraApi.CancelTask)
		Router.POST("/task/:taskId/retry", loraApi.RetryTask)
	}
}
