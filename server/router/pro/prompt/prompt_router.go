package prompt

import (
	api "server/api"

	"github.com/gin-gonic/gin"
)

type PromptRouter struct{}

func (s *PromptRouter) InitPromptRouter(router *gin.RouterGroup) {
	promptApi := api.ApiGroupApp.PromptApiGroup.PromptApi

	promptRouter := router.Group("api/prompts")
	{
		// 列表与搜索
		promptRouter.GET("", promptApi.GetPromptList)
		promptRouter.GET("/search", promptApi.SearchPrompts)
		promptRouter.GET("/featured", promptApi.GetFeaturedPrompts)
		promptRouter.GET("/trending", promptApi.GetTrendingPrompts)

		// 分类、标签、模型
		promptRouter.GET("/categories", promptApi.GetCategories)
		promptRouter.GET("/tags/popular", promptApi.GetPopularTags)
		promptRouter.GET("/models", promptApi.GetModels)

		// 详情和相关推荐
		promptRouter.GET("/:slug", promptApi.GetPromptDetail)
		promptRouter.GET("/:slug/related", promptApi.GetRelatedPrompts)

		// 互动 (使用 /interact/:id 前缀避免与 :slug 冲突)
		promptRouter.POST("/interact/:id/copy", promptApi.RecordCopy)
		promptRouter.POST("/interact/:id/like", promptApi.RecordLike)
		promptRouter.DELETE("/interact/:id/like", promptApi.UnlikePrompt)
		promptRouter.GET("/interact/:id/like/status", promptApi.CheckLikeStatus)
		promptRouter.POST("/interact/:id/rating", promptApi.SubmitRating)
	}
}
