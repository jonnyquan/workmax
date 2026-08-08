package marketing

import (
	"server/model"
	"server/service"
	"strconv"

	"github.com/gin-gonic/gin"
)

type EmailCampaignApi struct{}

var emailCampaignService = service.GroupServiceApp.MarketingServiceGroup.EmailCampaignService

// GetCampaignList 获取活动列表
func (a *EmailCampaignApi) GetCampaignList(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "10"))
	status := c.Query("status")
	keyword := c.Query("keyword")

	campaigns, total, err := emailCampaignService.GetCampaignList(page, pageSize, status, keyword)
	if err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"code":    200,
		"message": "success",
		"data": gin.H{
			"list":     campaigns,
			"total":    total,
			"page":     page,
			"pageSize": pageSize,
		},
	})
}

// GetCampaignByID 获取活动详情
func (a *EmailCampaignApi) GetCampaignByID(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))

	campaign, err := emailCampaignService.GetCampaignByID(id)
	if err != nil {
		c.JSON(404, gin.H{"code": 404, "message": "Campaign not found"})
		return
	}

	c.JSON(200, gin.H{
		"code":    200,
		"message": "success",
		"data":    campaign,
	})
}

// CreateCampaign 创建活动
func (a *EmailCampaignApi) CreateCampaign(c *gin.Context) {
	var campaign model.EmailCampaign
	if err := c.ShouldBindJSON(&campaign); err != nil {
		c.JSON(400, gin.H{"code": 400, "message": err.Error()})
		return
	}

	if err := emailCampaignService.CreateCampaign(&campaign); err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"code":    200,
		"message": "Campaign created successfully",
		"data":    campaign,
	})
}

// UpdateCampaign 更新活动
func (a *EmailCampaignApi) UpdateCampaign(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))

	var campaign model.EmailCampaign
	if err := c.ShouldBindJSON(&campaign); err != nil {
		c.JSON(400, gin.H{"code": 400, "message": err.Error()})
		return
	}

	campaign.Id = uint(id)
	if err := emailCampaignService.UpdateCampaign(&campaign); err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"code":    200,
		"message": "Campaign updated successfully",
	})
}

// DeleteCampaign 删除活动
func (a *EmailCampaignApi) DeleteCampaign(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))

	if err := emailCampaignService.DeleteCampaign(id); err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"code":    200,
		"message": "Campaign deleted successfully",
	})
}

// StartCampaign 启动活动
func (a *EmailCampaignApi) StartCampaign(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))

	// 启动发送任务（异步）
	go func() {
		sendService := service.GroupServiceApp.MarketingServiceGroup.EmailSendService
		sendService.SendCampaignEmails(id)
	}()

	c.JSON(200, gin.H{
		"code":    200,
		"message": "Campaign started successfully",
	})
}

// PauseCampaign 暂停活动
func (a *EmailCampaignApi) PauseCampaign(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))

	if err := emailCampaignService.UpdateCampaignStatus(id, model.EMAIL_CAMPAIGN_STATUS_PAUSED); err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"code":    200,
		"message": "Campaign paused successfully",
	})
}

// GetCampaignStats 获取活动统计
func (a *EmailCampaignApi) GetCampaignStats(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))

	stats, err := emailCampaignService.GetCampaignStats(id)
	if err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"code":    200,
		"message": "success",
		"data":    stats,
	})
}
