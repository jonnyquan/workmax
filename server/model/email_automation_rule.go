package model

import (
	"server/globals"
	"time"
)

type EmailAutomationRule struct {
	globals.GraMODEL
	Name            string    `json:"name" gorm:"column:name;type:varchar(255);not null;comment:规则名称"`
	Description     string    `json:"description" gorm:"column:description;type:varchar(500);comment:规则描述"`
	TriggerType     string    `json:"triggerType" gorm:"column:trigger_type;type:varchar(100);not null;comment:触发类型"`
	TemplateID      int       `json:"templateId" gorm:"column:template_id;not null;comment:使用的模板ID"`
	DelayMinutes    int       `json:"delayMinutes" gorm:"column:delay_minutes;default:0;comment:延迟时间（分钟）"`
	Conditions      string    `json:"conditions" gorm:"column:conditions;type:text;comment:JSON格式的触发条件"`
	Priority        int       `json:"priority" gorm:"column:priority;default:1;comment:优先级"`
	Status          int       `json:"status" gorm:"column:status;type:int;default:1;comment:0:禁用,1:启用"`
	TriggerCount    int       `json:"triggerCount" gorm:"column:trigger_count;default:0;comment:触发次数"`
	LastTriggerTime time.Time `json:"lastTriggerTime" gorm:"column:last_trigger_time;comment:最后触发时间"`
}

func (EmailAutomationRule) TableName() string {
	return "w_email_automation_rule"
}

const (
	EMAIL_AUTOMATION_STATUS_DISABLED = 0
	EMAIL_AUTOMATION_STATUS_ENABLED  = 1
)

const (
	EMAIL_AUTOMATION_TRIGGER_USER_REGISTER       = "user_register"
	EMAIL_AUTOMATION_TRIGGER_ONBOARDING_DAY_3    = "onboarding_day_3"
	EMAIL_AUTOMATION_TRIGGER_ONBOARDING_DAY_7    = "onboarding_day_7"
	EMAIL_AUTOMATION_TRIGGER_SUBSCRIPTION_EXPIRE = "subscription_expire"
	EMAIL_AUTOMATION_TRIGGER_USAGE_LIMIT         = "usage_limit"
	EMAIL_AUTOMATION_TRIGGER_INACTIVE_USER       = "inactive_user" // 调度器自动触发（旧）
	EMAIL_AUTOMATION_TRIGGER_INACTIVITY          = "inactivity"    // 手动触发（新）
	EMAIL_AUTOMATION_TRIGGER_FIRST_CREATION      = "first_creation"
	EMAIL_AUTOMATION_TRIGGER_PAYMENT_SUCCESS     = "payment_success"
	EMAIL_AUTOMATION_TRIGGER_PAYMENT_FAILED      = "payment_failed"
)
