package marketing

import (
	"server/service"
	"strconv"

	"github.com/gin-gonic/gin"
)

type EmailTrackingApi struct{}

// TrackEmailOpen 追踪邮件打开
func (a *EmailTrackingApi) TrackEmailOpen(c *gin.Context) {
	uid, _ := strconv.Atoi(c.Query("uid"))
	campaignID, _ := strconv.Atoi(c.Query("cid"))
	ip := c.ClientIP()
	userAgent := c.Request.UserAgent()

	sendService := service.GroupServiceApp.MarketingServiceGroup.EmailSendService
	sendService.RecordEmailOpen(uid, campaignID, ip, userAgent)

	// 返回1x1透明图片
	c.Data(200, "image/gif", []byte{
		0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x01, 0x00,
		0x01, 0x00, 0x80, 0x00, 0x00, 0xFF, 0xFF, 0xFF,
		0x00, 0x00, 0x00, 0x21, 0xF9, 0x04, 0x01, 0x00,
		0x00, 0x00, 0x00, 0x2C, 0x00, 0x00, 0x00, 0x00,
		0x01, 0x00, 0x01, 0x00, 0x00, 0x02, 0x02, 0x44,
		0x01, 0x00, 0x3B,
	})
}

// TrackEmailClick 追踪邮件点击
func (a *EmailTrackingApi) TrackEmailClick(c *gin.Context) {
	uid, _ := strconv.Atoi(c.Query("uid"))
	campaignID, _ := strconv.Atoi(c.Query("cid"))
	redirectURL := c.Query("url")
	ip := c.ClientIP()
	userAgent := c.Request.UserAgent()

	sendService := service.GroupServiceApp.MarketingServiceGroup.EmailSendService
	sendService.RecordEmailClick(uid, campaignID, ip, userAgent)

	// 重定向到目标URL
	if redirectURL != "" {
		c.Redirect(302, redirectURL)
	} else {
		c.JSON(200, gin.H{"code": 200, "message": "Tracked"})
	}
}

// Unsubscribe 退订
func (a *EmailTrackingApi) Unsubscribe(c *gin.Context) {
	uid, _ := strconv.Atoi(c.Query("uid"))
	reason := c.Query("reason")
	campaignID, _ := strconv.Atoi(c.Query("cid"))
	ip := c.ClientIP()

	sendService := service.GroupServiceApp.MarketingServiceGroup.EmailSendService
	err := sendService.Unsubscribe(uid, reason, campaignID, ip)
	if err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"code":    200,
		"message": "Unsubscribed successfully",
	})
}
