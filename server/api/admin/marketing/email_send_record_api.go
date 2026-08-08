package marketing

import (
	"server/service"
	"strconv"

	"github.com/gin-gonic/gin"
)

type EmailSendRecordApi struct{}

var emailSendService = service.GroupServiceApp.MarketingServiceGroup.EmailSendService

// GetSendRecordList 获取发送记录列表
func (a *EmailSendRecordApi) GetSendRecordList(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "10"))
	campaignIDStr := c.Query("campaignId")
	status := c.Query("status")
	keyword := c.Query("keyword")

	var campaignID *int
	if campaignIDStr != "" {
		id, _ := strconv.Atoi(campaignIDStr)
		campaignID = &id
	}

	records, total, err := emailSendService.GetSendRecordList(page, pageSize, campaignID, status, keyword)
	if err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"code":    200,
		"message": "success",
		"data": gin.H{
			"list":     records,
			"total":    total,
			"page":     page,
			"pageSize": pageSize,
		},
	})
}
