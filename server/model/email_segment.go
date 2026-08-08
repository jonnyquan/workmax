package model

import (
	"server/globals"
	"time"
)

type EmailSegment struct {
	globals.GraMODEL
	Name         string    `json:"name" gorm:"column:name;type:varchar(255);not null;comment:分组名称"`
	Description  string    `json:"description" gorm:"column:description;type:varchar(500);comment:分组描述"`
	Rules        string    `json:"rules" gorm:"column:rules;type:text;not null;comment:JSON格式的筛选规则"`
	UserCount    int       `json:"userCount" gorm:"column:user_count;default:0;comment:用户数量"`
	LastSyncTime time.Time `json:"lastSyncTime" gorm:"column:last_sync_time;comment:最后同步时间"`
	Status       int       `json:"status" gorm:"column:status;type:int;default:1;comment:0:禁用,1:启用"`
}

func (EmailSegment) TableName() string {
	return "w_email_segment"
}

const (
	EMAIL_SEGMENT_STATUS_DISABLED = 0
	EMAIL_SEGMENT_STATUS_ENABLED  = 1
)
