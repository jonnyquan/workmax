package model

import (
	"server/globals"
	"time"
)

type EmailCampaign struct {
	globals.GraMODEL
	Name             string    `json:"name" gorm:"column:name;type:varchar(255);not null;comment:活动名称"`
	TemplateID       int       `json:"templateId" gorm:"column:template_id;not null;comment:关联模板ID"`
	SegmentID        int       `json:"segmentId" gorm:"column:segment_id;comment:目标用户分组ID"`
	ScheduleType     string    `json:"scheduleType" gorm:"column:schedule_type;type:varchar(50);default:immediate;comment:发送类型"`
	ScheduleTime     time.Time `json:"scheduleTime" gorm:"column:schedule_time;comment:定时发送时间"`
	RecurringRule    string    `json:"recurringRule" gorm:"column:recurring_rule;type:varchar(255);comment:循环规则"`
	Status           string    `json:"status" gorm:"column:status;type:varchar(50);default:draft;comment:状态"`
	TotalRecipients  int       `json:"totalRecipients" gorm:"column:total_recipients;default:0;comment:总接收人数"`
	SentCount        int       `json:"sentCount" gorm:"column:sent_count;default:0;comment:已发送数"`
	DeliveredCount   int       `json:"deliveredCount" gorm:"column:delivered_count;default:0;comment:成功送达数"`
	FailedCount      int       `json:"failedCount" gorm:"column:failed_count;default:0;comment:发送失败数"`
	OpenCount        int       `json:"openCount" gorm:"column:open_count;default:0;comment:打开数"`
	ClickCount       int       `json:"clickCount" gorm:"column:click_count;default:0;comment:点击数"`
	UnsubscribeCount int       `json:"unsubscribeCount" gorm:"column:unsubscribe_count;default:0;comment:退订数"`
	CreatedBy        int       `json:"createdBy" gorm:"column:created_by;comment:创建者ID"`
	StartedAt        time.Time `json:"startedAt" gorm:"column:started_at;comment:开始时间"`
	CompletedAt      time.Time `json:"completedAt" gorm:"column:completed_at;comment:完成时间"`
}

func (EmailCampaign) TableName() string {
	return "w_email_campaign"
}

const (
	EMAIL_CAMPAIGN_SCHEDULE_IMMEDIATE = "immediate"
	EMAIL_CAMPAIGN_SCHEDULE_SCHEDULED = "scheduled"
	EMAIL_CAMPAIGN_SCHEDULE_RECURRING = "recurring"
)

const (
	EMAIL_CAMPAIGN_STATUS_DRAFT     = "draft"
	EMAIL_CAMPAIGN_STATUS_SCHEDULED = "scheduled"
	EMAIL_CAMPAIGN_STATUS_RUNNING   = "running"
	EMAIL_CAMPAIGN_STATUS_COMPLETED = "completed"
	EMAIL_CAMPAIGN_STATUS_PAUSED    = "paused"
	EMAIL_CAMPAIGN_STATUS_CANCELLED = "cancelled"
)
