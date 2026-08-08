package admin

import (
	api "server/api"

	"github.com/gin-gonic/gin"
)

type AdminBlogRouter struct {
}

func (s *AdminBlogRouter) InitAdminBlogRouter(router *gin.RouterGroup) {
	adminBlogRouter := router.Group("api")
	adminBlogApi := api.ApiGroupApp.AdminApiGroup.AdminBlogApi
	{
		adminBlogRouter.GET("/admin/blog/getBlogCategoryList", adminBlogApi.GetBlogCategoryList)
		adminBlogRouter.POST("/admin/blog/updateBlogCategory", adminBlogApi.UpdateBlogCategory)
		adminBlogRouter.GET("/admin/blog/getBlogCategoryAllList", adminBlogApi.GetBlogCategoryAllList)
		adminBlogRouter.POST("/admin/blog/syncMultiLangBlogCategory", adminBlogApi.SyncMultiLangBlogCategory)

		adminBlogRouter.GET("/admin/blog/getBlogList", adminBlogApi.GetBlogList)
		adminBlogRouter.GET("/admin/blog/getBlog", adminBlogApi.GetBlog)
		adminBlogRouter.GET("/admin/blog/getBlogByAIRecordId", adminBlogApi.GetBlogByAIRecordId)
		adminBlogRouter.POST("/admin/blog/addBlog", adminBlogApi.AddBlog)
		adminBlogRouter.POST("/admin/blog/updateBlog", adminBlogApi.UpdateBlog)
		adminBlogRouter.POST("/admin/blog/copyNewBlog", adminBlogApi.CopyNewBlog)
		adminBlogRouter.POST("/admin/blog/uploadBlogCover", adminBlogApi.UploadBlogCover)
		adminBlogRouter.POST("/admin/blog/syncMultiLangBlog", adminBlogApi.SyncMultiLangBlog)
	}
}
