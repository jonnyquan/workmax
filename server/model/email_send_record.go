package model

import (
	"server/globals"
	"time"
)

type EmailSendRecord struct {
	globals.GraMODEL
	CampaignID *int      `json:"campaignId" gorm:"column:campaign_id;comment:关联活动ID"`
	TemplateID int       `json:"templateId" gorm:"column:template_id;not null;comment:使用的模板ID"`
	UID        int       `json:"uid" gorm:"column:uid;not null;comment:接收用户ID"`
	Email      string    `json:"email" gorm:"column:email;type:varchar(255);not null;comment:邮箱地址"`
	Subject    string    `json:"subject" gorm:"column:subject;type:varchar(500);not null;comment:邮件主题"`
	Status     string    `json:"status" gorm:"column:status;type:varchar(50);default:pending;comment:状态"`
	ErrorMsg   string    `json:"errorMsg" gorm:"column:error_msg;type:text;comment:错误信息"`
	SentAt     time.Time `json:"sentAt" gorm:"column:sent_at;comment:发送时间"`
	OpenedAt   time.Time `json:"openedAt" gorm:"column:opened_at;comment:打开时间"`
	ClickedAt  time.Time `json:"clickedAt" gorm:"column:clicked_at;comment:点击时间"`
	OpenCount  int       `json:"openCount" gorm:"column:open_count;default:0;comment:打开次数"`
	ClickCount int       `json:"clickCount" gorm:"column:click_count;default:0;comment:点击次数"`
	IP         string    `json:"ip" gorm:"column:ip;type:varchar(100);comment:用户IP"`
	UserAgent  string    `json:"userAgent" gorm:"column:user_agent;type:varchar(500);comment:用户代理"`
}

func (EmailSendRecord) TableName() string {
	return "w_email_send_record"
}

const (
	EMAIL_SEND_STATUS_PENDING = "pending"
	EMAIL_SEND_STATUS_SENT    = "sent"
	EMAIL_SEND_STATUS_FAILED  = "failed"
	EMAIL_SEND_STATUS_OPENED  = "opened"
	EMAIL_SEND_STATUS_CLICKED = "clicked"
)
