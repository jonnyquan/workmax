package tools

import (
	"path"
	"server/api"
	"server/globals"
	"strings"

	"github.com/gin-gonic/gin"
)

type RemoverRouter struct{}

func (r *RemoverRouter) InitRemoverRouter(router *gin.RouterGroup) {
	removerApi := api.ApiGroupApp.ToolsApiGroup.RemoverApi

	prefix := strings.TrimSpace(globals.GraConf.System.RouterPrefix)
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		prefix = "api"
	}

	Router := router.Group(path.Join(prefix, "tools", "remover"))
	{
		Router.GET("/credits", removerApi.GetCredits)
		Router.POST("/generate", removerApi.Generate)
		Router.GET("/tasks/active", removerApi.GetActiveTasks)
		Router.GET("/tasks", removerApi.GetTasks)
		Router.GET("/history", removerApi.GetHistory)
		Router.DELETE("/history/:id", removerApi.DeleteHistoryRecord)
		Router.POST("/task/:taskId/cancel", removerApi.CancelTask)
		Router.POST("/task/:taskId/retry", removerApi.RetryTask)
	}
}
