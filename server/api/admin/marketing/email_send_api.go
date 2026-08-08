package marketing

import (
	"server/globals"
	"server/model"
	"server/model/common/response"
	"server/utils"
	"time"

	"github.com/gin-gonic/gin"
)

type EmailSendApi struct{}

type SendEmailRequest struct {
	Email   string `json:"email" binding:"required,email"`
	Subject string `json:"subject" binding:"required"`
	Content string `json:"content" binding:"required"`
}

// SendEmail 发送单封邮件
func (e *EmailSendApi) SendEmail(c *gin.Context) {
	var req SendEmailRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMessage("参数错误: "+err.Error(), c)
		return
	}

	record := model.EmailSendRecord{
		Email:   req.Email,
		Subject: req.Subject,
		Status:  model.EMAIL_SEND_STATUS_PENDING,
	}

	if err := utils.SendEmail(req.Email, req.Subject, req.Content); err != nil {
		record.Status = model.EMAIL_SEND_STATUS_FAILED
		record.ErrorMsg = err.Error()
		globals.GraDBs["system"].Create(&record)
		response.FailWithMessage("发送失败: "+err.Error(), c)
		return
	}

	record.Status = model.EMAIL_SEND_STATUS_SENT
	record.SentAt = time.Now()
	globals.GraDBs["system"].Create(&record)

	response.OkWithMessage("发送成功", c)
}
