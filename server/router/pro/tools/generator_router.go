package tools

import (
	"path"
	api "server/api"
	"server/globals"
	"strings"

	"github.com/gin-gonic/gin"
)

type GeneratorRouter struct{}

func (r *GeneratorRouter) InitGeneratorRouter(router *gin.RouterGroup) {
	generatorApi := api.ApiGroupApp.ToolsApiGroup.GeneratorApi

	prefix := strings.TrimSpace(globals.GraConf.System.RouterPrefix)
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		prefix = "api"
	}

	Router := router.Group(path.Join(prefix, "tools"))
	{
		// 模型与生成相关
		Router.GET("/models", generatorApi.GetModels)
		Router.GET("/pricing/catalog", generatorApi.GetPricingCatalog)
		Router.GET("/credits", generatorApi.GetCredits)
		Router.POST("/generate", generatorApi.Generate)
		Router.GET("/generate/:id", generatorApi.GetGenerationByID)

		// 任务状态相关
		Router.GET("/task/:taskId", generatorApi.GetTaskStatus)
		Router.POST("/task/:taskId/cancel", generatorApi.CancelTask)
		Router.POST("/task/:taskId/retry", generatorApi.RetryTask)
		Router.GET("/tasks/active", generatorApi.GetActiveTasks)
		Router.GET("/tasks", generatorApi.GetTasks)

		// 历史记录
		Router.GET("/history", generatorApi.GetHistory)
		Router.DELETE("/history/:id", generatorApi.DeleteHistoryRecord)

		// 参考图片上传
		Router.POST("/reference-images/upload", generatorApi.UploadReferenceImage)
		Router.POST("/reference-image-upload", generatorApi.UploadReferenceImage)

		// 参考视频 / 音频上传（Seedance-2 多模态）
		Router.POST("/reference-videos/upload", generatorApi.UploadReferenceVideo)
		Router.POST("/reference-audios/upload", generatorApi.UploadReferenceAudio)
	}
}
