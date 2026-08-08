package admin

import (
	api "server/api"

	"github.com/gin-gonic/gin"
)

type AdminMarketingRouter struct{}

func (s *AdminMarketingRouter) InitAdminMarketingRouter(router *gin.RouterGroup) {
	marketingRouter := router.Group("api/admin/email")
	marketingApi := api.ApiGroupApp.AdminApiGroup.AdminMarketingApiGroup

	// 邮件模板
	{
		marketingRouter.GET("/templates", marketingApi.EmailTemplateApi.GetTemplateList)
		marketingRouter.GET("/templates/:id", marketingApi.EmailTemplateApi.GetTemplateByID)
		marketingRouter.POST("/templates", marketingApi.EmailTemplateApi.CreateTemplate)
		marketingRouter.PUT("/templates/:id", marketingApi.EmailTemplateApi.UpdateTemplate)
		marketingRouter.DELETE("/templates/:id", marketingApi.EmailTemplateApi.DeleteTemplate)
		marketingRouter.PUT("/templates/:id/status", marketingApi.EmailTemplateApi.UpdateTemplateStatus)
		marketingRouter.POST("/templates/test", marketingApi.EmailTemplateApi.TestEmailTemplate)
	}

	// 邮件活动
	{
		marketingRouter.GET("/campaigns", marketingApi.EmailCampaignApi.GetCampaignList)
		marketingRouter.GET("/campaigns/:id", marketingApi.EmailCampaignApi.GetCampaignByID)
		marketingRouter.POST("/campaigns", marketingApi.EmailCampaignApi.CreateCampaign)
		marketingRouter.PUT("/campaigns/:id", marketingApi.EmailCampaignApi.UpdateCampaign)
		marketingRouter.DELETE("/campaigns/:id", marketingApi.EmailCampaignApi.DeleteCampaign)
		marketingRouter.POST("/campaigns/:id/start", marketingApi.EmailCampaignApi.StartCampaign)
		marketingRouter.POST("/campaigns/:id/pause", marketingApi.EmailCampaignApi.PauseCampaign)
		marketingRouter.GET("/campaigns/:id/stats", marketingApi.EmailCampaignApi.GetCampaignStats)
	}

	// 用户分组
	{
		marketingRouter.GET("/segments", marketingApi.EmailSegmentApi.GetSegmentList)
		marketingRouter.GET("/segments/:id", marketingApi.EmailSegmentApi.GetSegmentByID)
		marketingRouter.POST("/segments", marketingApi.EmailSegmentApi.CreateSegment)
		marketingRouter.PUT("/segments/:id", marketingApi.EmailSegmentApi.UpdateSegment)
		marketingRouter.DELETE("/segments/:id", marketingApi.EmailSegmentApi.DeleteSegment)
		marketingRouter.GET("/segments/:id/users", marketingApi.EmailSegmentApi.GetSegmentUsers)
		marketingRouter.POST("/segments/:id/sync", marketingApi.EmailSegmentApi.SyncSegmentUsers)
	}

	// 发送记录
	{
		marketingRouter.GET("/records", marketingApi.EmailSendRecordApi.GetSendRecordList)
	}

	// 自动化规则
	{
		marketingRouter.GET("/automation", marketingApi.EmailAutomationApi.GetAutomationRuleList)
		marketingRouter.GET("/automation/:id", marketingApi.EmailAutomationApi.GetAutomationRuleByID)
		marketingRouter.POST("/automation", marketingApi.EmailAutomationApi.CreateAutomationRule)
		marketingRouter.PUT("/automation/:id", marketingApi.EmailAutomationApi.UpdateAutomationRule)
		marketingRouter.DELETE("/automation/:id", marketingApi.EmailAutomationApi.DeleteAutomationRule)
		marketingRouter.PUT("/automation/:id/status", marketingApi.EmailAutomationApi.UpdateAutomationRuleStatus)

		// 不活跃用户召回
		marketingRouter.POST("/automation/check-inactive", marketingApi.EmailAutomationApi.CheckInactiveUsers)
		marketingRouter.POST("/automation/batch-check-inactive", marketingApi.EmailAutomationApi.BatchCheckInactiveUsers)
	}

	// 发送单封邮件
	{
		marketingRouter.POST("/send", marketingApi.EmailSendApi.SendEmail)
	}

	// 追踪和退订（公开路由）
	trackingRouter := router.Group("api/email")
	{
		trackingRouter.GET("/track/open", marketingApi.EmailTrackingApi.TrackEmailOpen)
		trackingRouter.GET("/track/click", marketingApi.EmailTrackingApi.TrackEmailClick)
		trackingRouter.GET("/unsubscribe", marketingApi.EmailTrackingApi.Unsubscribe)
	}
}
