package tools

import (
	"path"
	"server/api"
	"server/globals"
	"strings"

	"github.com/gin-gonic/gin"
)

type VectorizerRouter struct{}

func (r *VectorizerRouter) InitVectorizerRouter(router *gin.RouterGroup) {
	vectorizerApi := api.ApiGroupApp.ToolsApiGroup.VectorizerApi

	prefix := strings.TrimSpace(globals.GraConf.System.RouterPrefix)
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		prefix = "api"
	}

	// Group: /api/tools/vectorizer
	Router := router.Group(path.Join(prefix, "tools", "vectorizer"))
	{
		Router.GET("/credits", vectorizerApi.GetCredits)
		Router.GET("/source", vectorizerApi.GetSource)
		Router.POST("/generate", vectorizerApi.Generate)
		Router.GET("/tasks/active", vectorizerApi.GetActiveTasks)
		Router.GET("/tasks", vectorizerApi.GetTasks)
		Router.GET("/history", vectorizerApi.GetHistory)
		Router.DELETE("/history/:id", vectorizerApi.DeleteHistoryRecord)
		Router.POST("/task/:taskId/cancel", vectorizerApi.CancelTask)
		Router.POST("/task/:taskId/retry", vectorizerApi.RetryTask)
	}
}
