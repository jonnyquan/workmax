package admin

import (
	api "server/api"

	"github.com/gin-gonic/gin"
)

type AdminPromptRouter struct{}

func (s *AdminPromptRouter) InitAdminPromptRouter(router *gin.RouterGroup) {
	adminPromptRouter := router.Group("api")
	adminPromptApi := api.ApiGroupApp.AdminApiGroup.AdminPromptApi
	{
		// Prompt
		adminPromptRouter.GET("/admin/prompt/getPromptList", adminPromptApi.GetPromptList)
		adminPromptRouter.GET("/admin/prompt/getPrompt", adminPromptApi.GetPrompt)
		adminPromptRouter.POST("/admin/prompt/updatePrompt", adminPromptApi.UpdatePrompt)
		adminPromptRouter.POST("/admin/prompt/deletePrompt", adminPromptApi.DeletePrompt)

		// Category
		adminPromptRouter.GET("/admin/prompt/getPromptCategoryList", adminPromptApi.GetPromptCategoryList)
		adminPromptRouter.POST("/admin/prompt/updatePromptCategory", adminPromptApi.UpdatePromptCategory)

		// Tag
		adminPromptRouter.GET("/admin/prompt/getPromptTagList", adminPromptApi.GetPromptTagList)
		adminPromptRouter.POST("/admin/prompt/updatePromptTag", adminPromptApi.UpdatePromptTag)
		adminPromptRouter.POST("/admin/prompt/deletePromptTag", adminPromptApi.DeletePromptTag)

		// Stats — admin-triggered TagStatsScheduler phases (async).
		adminPromptRouter.POST("/admin/prompt/runTagStats", adminPromptApi.RunPromptTagStats)
	}
}
