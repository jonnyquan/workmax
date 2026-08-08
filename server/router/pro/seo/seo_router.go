package seo

import (
	api "server/api"

	"github.com/gin-gonic/gin"
)

type SeoRouter struct{}

func (r *SeoRouter) InitSeoRouter(router *gin.RouterGroup) {
	seoApi := api.ApiGroupApp.SeoApiGroup.SeoApi

	seoRouter := router.Group("api/seo")
	{
		seoRouter.GET("/prompt-slugs", seoApi.GetPromptSlugs)
		seoRouter.GET("/prompt-languages/:slug", seoApi.GetPromptLanguages)
		seoRouter.GET("/categories", seoApi.GetAllCategories)
		seoRouter.GET("/styles", seoApi.GetAllStyles)
		seoRouter.GET("/models", seoApi.GetAllModelsForSEO)
		seoRouter.GET("/stats", seoApi.GetPromptStats)
	}
}
