package tools

import (
	"path"
	api "server/api"
	"server/globals"
	"server/middleware"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type VideoGeneratorRouter struct{}

func (r *VideoGeneratorRouter) InitVideoGeneratorRouter(router *gin.RouterGroup) {
	videoApi := api.ApiGroupApp.ToolsApiGroup.VideoGeneratorApi

	prefix := strings.TrimSpace(globals.GraConf.System.RouterPrefix)
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		prefix = "api"
	}

	Router := router.Group(path.Join(prefix, "tools", "video"))
	// L1 IP-level guard. Video generation is the platform's most
	// expensive op (per-shot GPU minutes + provider credit burn);
	// without this, a misbehaving client can drain the user's
	// credits or rack provider costs faster than the FE's
	// in-flight cap can react. Pin at 120/min — well above the
	// FE's natural submission rate (~1 click every few seconds)
	// but bounded enough to block abuse.
	Router.Use(middleware.RateLimit(120, time.Minute))
	{
		Router.GET("/models", videoApi.GetModels)
		Router.GET("/pricing", videoApi.GetPricingCatalog)
		Router.GET("/credits", videoApi.GetCreditQuote)
		Router.POST("/generate", videoApi.Generate)
		Router.GET("/history", videoApi.GetHistory)
		Router.DELETE("/history/:id", videoApi.DeleteHistoryRecord)
		Router.POST("/history/:id/cover", videoApi.UpdateHistoryCover)
		Router.GET("/history/:id/download", videoApi.DownloadHistoryRecord)
		Router.GET("/tasks", videoApi.GetTasks)
		Router.GET("/tasks/active", videoApi.GetActiveTasks)
		Router.GET("/task/:taskId", videoApi.GetTaskStatus)
		Router.POST("/task/:taskId/cancel", videoApi.CancelTask)
	}
}
