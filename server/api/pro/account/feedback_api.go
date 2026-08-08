package account

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"path/filepath"
	"server/globals"
	"server/model"
	"server/model/common/response"
	"server/service"
	"server/utils"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// FeedbackApi 用户反馈接口
// 提供提交反馈和获取反馈列表两个接口
// 遵循现有代码规范, 使用系统数据库实例 globals.GraDBs["system"]

type FeedbackApi struct{}

// SubmitFeedback 提交用户反馈
// @Tags 系统用户
// @Summary 提交用户反馈
// @Produce application/json
// @Security ApiKeyAuth
// @Param data body SubmitFeedbackRequest true "反馈数据"
// @Success 200 {string} string "{\"code\":200,\"data\":{},\"msg\":\"提交成功\"}"
// @Router /account/feedback/submit [post]
func (f *FeedbackApi) SubmitFeedback(c *gin.Context) {
	uid := utils.GetUserID(c)

	type SubmitFeedbackRequest struct {
		FeedbackType string `json:"feedbackType" binding:"required"` // 反馈类型
		Content      string `json:"content" binding:"required"`      // 反馈内容
		Rating       int    `json:"rating" binding:"required"`       // 评分 1-5
	}

	var req SubmitFeedbackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithDetailed(err.Error(), "Failed to retrieve input", c)
		return
	}

	if req.Rating < 1 || req.Rating > 5 {
		response.FailWithMessage("rating must be between 1 and 5", c)
		return
	}

	feedback := model.UserFeedback{
		UID:          int(uid),
		FeedbackType: req.FeedbackType,
		Content:      req.Content,
		Rating:       req.Rating,
		Status:       model.FEEDBACK_STATUS_PENDING,
	}
	feedback.CreatedAt = time.Now()
	feedback.UpdatedAt = time.Now()

	if err := globals.GraDBs["system"].Create(&feedback).Error; err != nil {
		response.FailWithMessage("submit feedback failed", c)
		return
	}

	// Send the configured Server operator a notification asynchronously.
	go notifyOperatorNewFeedback(uid, req.FeedbackType, req.Content, req.Rating)

	c.JSON(http.StatusOK, gin.H{
		"code": 200,
		"msg":  "提交成功",
	})
}

// GetFeedbackList 获取用户反馈列表
// @Tags 系统用户
// @Summary 获取用户反馈列表
// @Produce application/json
// @Security ApiKeyAuth
// @Param page query int false "页码" default(1)
// @Param limit query int false "每页数量" default(10)
// @Success 200 {string} string "{\"code\":200,\"data\":{},\"msg\":\"获取成功\"}"
// @Router /account/feedback/list [get]
func (f *FeedbackApi) GetFeedbackList(c *gin.Context) {
	uid := utils.GetUserID(c)

	pageStr := c.DefaultQuery("page", "1")
	limitStr := c.DefaultQuery("limit", "10")

	page, _ := strconv.Atoi(pageStr)
	limit, _ := strconv.Atoi(limitStr)
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 10
	}

	var feedbacks []model.UserFeedback
	query := globals.GraDBs["system"].Model(&model.UserFeedback{}).Where("uid = ?", uid)
	var total int64
	query.Count(&total)

	err := query.Order("created_at desc").Offset((page - 1) * limit).Limit(limit).Find(&feedbacks).Error
	if err != nil {
		response.FailWithMessage("get feedback list failed", c)
		return
	}

	data := map[string]interface{}{
		"items": feedbacks,
		"total": total,
		"page":  page,
		"limit": limit,
	}

	response.OkWithData(data, c)
}

// notifyOperatorNewFeedback emails the configured Server operator when new
// feedback is submitted. AdminEmail is the legacy configuration key name.
func notifyOperatorNewFeedback(uid uint, feedbackType, content string, rating int) {
	operatorEmail := globals.GraConf.System.AdminEmail
	if operatorEmail == "" {
		globals.Warn("[Feedback] Operator email not configured, skipping notification")
		return
	}

	// Get user info
	user, err := service.GroupServiceApp.AccountServiceGroup.AccountService.GetUserInfo(uid)
	userEmail := "Unknown"
	userName := "Unknown"
	if err == nil {
		userEmail = user.Email
		userName = user.Nickname
		if userName == "" {
			userName = userEmail
		}
	}

	// Build rating stars
	stars := ""
	for i := 0; i < rating; i++ {
		stars += "⭐"
	}

	// Format feedback type display
	feedbackTypeDisplay := feedbackType
	switch feedbackType {
	case model.FEEDBACK_TYPE_FEATURE_REQUEST:
		feedbackTypeDisplay = "Feature Request"
	case model.FEEDBACK_TYPE_BUG_REPORT:
		feedbackTypeDisplay = "Bug Report"
	case model.FEEDBACK_TYPE_GENERAL:
		feedbackTypeDisplay = "General Feedback"
	case model.FEEDBACK_TYPE_SUPPORT:
		feedbackTypeDisplay = "Support Request"
	}

	subject := fmt.Sprintf("📬 New Feedback: %s (%s)", feedbackTypeDisplay, stars)

	// Load email template
	basePath := globals.GraConf.System.ServerRunPath
	if basePath == "" {
		basePath = "."
	}
	templatePath := filepath.Join(basePath, "resource", "template", "mail_new_feedback.html")
	templateContent, readErr := ioutil.ReadFile(templatePath)

	var body string
	if readErr != nil {
		globals.Warn(fmt.Sprintf("[Feedback] Template not found, using fallback: %v", readErr))
		body = buildFallbackFeedbackEmail(feedbackTypeDisplay, stars, rating, userName, userEmail, content)
	} else {
		// Replace template variables
		body = string(templateContent)
		body = strings.ReplaceAll(body, "{{feedbackType}}", feedbackType)
		body = strings.ReplaceAll(body, "{{feedbackTypeDisplay}}", feedbackTypeDisplay)
		body = strings.ReplaceAll(body, "{{ratingStars}}", stars)
		body = strings.ReplaceAll(body, "{{rating}}", strconv.Itoa(rating))
		body = strings.ReplaceAll(body, "{{userName}}", userName)
		body = strings.ReplaceAll(body, "{{userEmail}}", userEmail)
		body = strings.ReplaceAll(body, "{{timestamp}}", time.Now().Format("2006-01-02 15:04:05"))
		body = strings.ReplaceAll(body, "{{content}}", content)
	}

	if err := utils.SendEmail(operatorEmail, subject, body); err != nil {
		globals.Error(fmt.Sprintf("[Feedback] Failed to send operator notification: %v", err))
	} else {
		globals.Info(fmt.Sprintf("[Feedback] Operator notification sent to %s", operatorEmail))
	}
}

// buildFallbackFeedbackEmail creates a simple email when template loading fails
func buildFallbackFeedbackEmail(feedbackTypeDisplay, stars string, rating int, userName, userEmail, content string) string {
	return fmt.Sprintf(`
<!DOCTYPE html>
<html>
<head>
    <style>
        body { font-family: Arial, sans-serif; line-height: 1.6; color: #333; }
        .container { max-width: 600px; margin: 0 auto; padding: 20px; }
        .header { background: linear-gradient(135deg, #6366F1, #8B5CF6); color: white; padding: 20px; border-radius: 8px 8px 0 0; }
        .content { background: #f9fafb; padding: 20px; border: 1px solid #e5e7eb; border-top: none; border-radius: 0 0 8px 8px; }
        .info-row { margin: 10px 0; padding: 10px; background: white; border-radius: 4px; }
        .label { font-weight: bold; color: #6366F1; }
        .feedback-content { background: white; padding: 15px; border-left: 4px solid #6366F1; margin: 15px 0; }
        .footer { text-align: center; margin-top: 20px; color: #666; font-size: 12px; }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <h2 style="margin:0;">📬 New User Feedback</h2>
            <p style="margin:5px 0 0 0;">WorkMax Server Operator Notification</p>
        </div>
        <div class="content">
            <div class="info-row">
                <span class="label">Type:</span> %s
            </div>
            <div class="info-row">
                <span class="label">Rating:</span> %s (%d/5)
            </div>
            <div class="info-row">
                <span class="label">User:</span> %s
            </div>
            <div class="info-row">
                <span class="label">Email:</span> %s
            </div>
            <div class="info-row">
                <span class="label">Time:</span> %s
            </div>
            
            <h3>Feedback Content:</h3>
            <div class="feedback-content">
                %s
            </div>
        </div>
        <div class="footer">
            <p>This is an automated notification from WorkMax.</p>
        </div>
    </div>
</body>
</html>
`, feedbackTypeDisplay, stars, rating, userName, userEmail, time.Now().Format("2006-01-02 15:04:05"), content)
}
