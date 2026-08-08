package admin

import (
	api "server/api"

	"github.com/gin-gonic/gin"
)

type AdminFeedbackRouter struct {
}

func (s *AdminFeedbackRouter) InitAdminFeedbackRouter(router *gin.RouterGroup) {
	adminFeedbackRouter := router.Group("api")
	adminFeedbackApi := api.ApiGroupApp.AdminApiGroup.AdminFeedbackApi
	{
		adminFeedbackRouter.GET("/admin/feedback/getFeedbackList", adminFeedbackApi.GetFeedbackList)
		adminFeedbackRouter.GET("/admin/feedback/getFeedbackStatistic", adminFeedbackApi.GetFeedbackStatistic)
		adminFeedbackRouter.POST("/admin/feedback/updateFeedback", adminFeedbackApi.UpdateFeedback)
		adminFeedbackRouter.POST("/admin/feedback/deleteFeedback", adminFeedbackApi.DeleteFeedback)
		adminFeedbackRouter.POST("/admin/feedback/batchUpdateStatus", adminFeedbackApi.BatchUpdateStatus)
	}
}
