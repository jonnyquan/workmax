package blog

import (
	api "server/api"

	"github.com/gin-gonic/gin"
)

type BlogRouter struct {
}

var blogApi = api.ApiGroupApp.BlogApiGroup.BlogApi

func (s *BlogRouter) InitBlogRouter(router *gin.RouterGroup) {
	Router := router.Group("api/blog")
	{
		Router.GET("/categories", blogApi.GetCategories)
		Router.GET("/categories/count", blogApi.GetCategoriesCount)
		Router.GET("/posts", blogApi.GetBlogList)
		Router.GET("/posts/:slug", blogApi.GetBlogDetail)
		Router.GET("/search", blogApi.SearchPosts)
		Router.GET("/posts/popular", blogApi.GetPopularPosts)
	}
}
