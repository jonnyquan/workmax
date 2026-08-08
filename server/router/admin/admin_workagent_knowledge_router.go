package admin

import (
	api "server/api"

	"github.com/gin-gonic/gin"
)

type AdminWorkAgentKnowledgeRouter struct{}

func (s *AdminWorkAgentKnowledgeRouter) InitAdminWorkAgentKnowledgeRouter(router *gin.RouterGroup) {
	knowledgeApi := api.ApiGroupApp.AdminApiGroup.AdminWorkAgentKnowledgeApi
	knowledgeRouter := router.Group("api/admin/workagent/knowledge-documents")
	{
		knowledgeRouter.GET("", knowledgeApi.ListKnowledgeDocuments)
		knowledgeRouter.POST("", knowledgeApi.CreateKnowledgeDocument)
		knowledgeRouter.POST("/upload", knowledgeApi.UploadKnowledgeDocument)
		knowledgeRouter.POST("/review-batch", knowledgeApi.BatchReviewKnowledgeDocuments)
		knowledgeRouter.POST("/reindex-batch", knowledgeApi.BatchReindexKnowledgeDocuments)
		knowledgeRouter.GET("/index-jobs", knowledgeApi.ListKnowledgeIndexJobs)
		knowledgeRouter.POST("/index-jobs/:jobId/retry", knowledgeApi.RetryKnowledgeIndexJob)
		knowledgeRouter.PUT("/:id", knowledgeApi.UpdateKnowledgeDocument)
		knowledgeRouter.POST("/:id/review", knowledgeApi.ReviewKnowledgeDocument)
		knowledgeRouter.POST("/:id/reindex", knowledgeApi.ReindexKnowledgeDocument)
	}
}
