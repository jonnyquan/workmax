package marketing

import (
	"server/globals"
	"server/model/common/response"
	"server/utils"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type EmailTemplateTestRequest struct {
	TemplateCode string            `json:"templateCode" binding:"required"`
	ToEmail      string            `json:"toEmail" binding:"required,email"`
	TestData     map[string]string `json:"testData"`
}

// TestEmailTemplate 测试邮件模板发送
func (api *EmailTemplateApi) TestEmailTemplate(c *gin.Context) {
	var req EmailTemplateTestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMessage(err.Error(), c)
		return
	}

	// 根据模板代码调用不同的发送函数
	var err error

	switch req.TemplateCode {
	case "mail_code":
		// 验证码邮件
		err = utils.EmailService.SendVerificationEmail(req.ToEmail)

	case "welcome_email":
		// 欢迎邮件
		nickname := req.TestData["nickname"]
		if nickname == "" {
			nickname = "Test User"
		}
		err = utils.EmailService.SendWelcomeEmail(req.ToEmail, nickname)

	case "password_reset":
		// 密码重置 - 使用测试数据
		nickname := req.TestData["nickname"]
		if nickname == "" {
			nickname = "Test User"
		}

		// 从数据库获取模板
		var template struct {
			Subject string
			Content string
		}
		err = globals.GraDBs["system"].Table("w_email_template").
			Select("subject, content").
			Where("code = ? AND status = 1", req.TemplateCode).
			First(&template).Error

		if err == nil {
			// 准备变量
			resetLink := globals.GraConf.System.FrontendURL + "/reset-password/test-token-123456"
			variables := map[string]string{
				"nickname":    nickname,
				"email":       req.ToEmail,
				"resetLink":   resetLink,
				"expiryHours": "24",
			}

			// 替换内容和主题中的变量
			content := template.Content
			subject := template.Subject
			for key, value := range variables {
				placeholder := "{{" + key + "}}"
				content = strings.ReplaceAll(content, placeholder, value)
				subject = strings.ReplaceAll(subject, placeholder, value)
			}

			err = utils.SendEmail(req.ToEmail, subject, content)
		}

	case "email_verification":
		// 邮箱验证链接
		nickname := req.TestData["nickname"]
		if nickname == "" {
			nickname = "Test User"
		}
		verificationLink := globals.GraConf.System.FrontendURL + "/verify?token=test123456"
		err = utils.EmailService.SendEmailVerificationLink(req.ToEmail, nickname, verificationLink)

	case "payment_receipt":
		// 支付收据
		nickname := req.TestData["nickname"]
		if nickname == "" {
			nickname = "Test User"
		}
		err = utils.EmailService.SendOrderEmail(
			req.ToEmail,
			nickname,
			"Pro Plan - Monthly",
			"INV-2024-001",
			time.Now(),
			"$19.99",
		)

	case "subscription_renewal":
		// 订阅续费
		nickname := req.TestData["nickname"]
		if nickname == "" {
			nickname = "Test User"
		}
		err = utils.EmailService.SendSubscriptionRenewalEmail(
			req.ToEmail,
			nickname,
			"Pro Plan",
			"19.99",
			time.Now().AddDate(0, 0, 7).Format("2006-01-02"),
			"Visa ****1234",
		)

	case "monthly_newsletter":
		// 月度简报 - 使用通用发送
		// 从数据库获取模板
		var template struct {
			Subject string
			Content string
		}
		err = globals.GraDBs["system"].Table("w_email_template").
			Select("subject, content").
			Where("code = ? AND status = 1", req.TemplateCode).
			First(&template).Error

		if err == nil {
			// 替换变量
			content := template.Content
			subject := template.Subject
			variables := map[string]string{
				"month":               "November",
				"year":                "2024",
				"heroTitle":           "New Features This Month",
				"heroSubtitle":        "Discover what's new in WorkMax",
				"feature1Title":       "AI Writing Assistant 2.0",
				"feature1Description": "Enhanced AI capabilities for better content generation",
				"feature2Title":       "Template Marketplace",
				"feature2Description": "Access hundreds of professional templates",
				"writingTip":          "Start with an outline before diving into detailed writing",
				"ctaUrl":              globals.GraConf.System.FrontendURL,
				"ctaText":             "Explore New Features",
				"unsubscribeUrl":      globals.GraConf.System.FrontendURL + "/unsubscribe",
				"preferencesUrl":      globals.GraConf.System.FrontendURL + "/preferences",
			}

			// 替换内容和主题中的变量
			for key, value := range variables {
				placeholder := "{{" + key + "}}"
				content = strings.ReplaceAll(content, placeholder, value)
				subject = strings.ReplaceAll(subject, placeholder, value)
			}

			err = utils.SendEmail(req.ToEmail, subject, content)
		}

	default:
		response.FailWithMessage("Unsupported template code: "+req.TemplateCode, c)
		return
	}

	if err != nil {
		response.FailWithMessage("Email sending failed: "+err.Error(), c)
		return
	}

	response.OkWithMessage("Test email sent successfully to "+req.ToEmail, c)
}
