package model

import (
	"server/globals"
	"time"
)

// Notification represents an in-app message delivered to a user.
// A UID of 0 indicates a system-wide broadcast, which still requires a
// per-user read record to track `readed` state accurately. For now we
// persist per-recipient rows (broadcast fan-out happens on write) to keep
// list/count queries trivial.
type Notification struct {
	globals.GraMODEL
	UID           uint       `json:"uid" gorm:"column:uid;not null;default:0;comment:接收用户ID，0表示系统广播"`
	Title         string     `json:"title" gorm:"column:title;type:varchar(200);not null;default:'';comment:通知标题"`
	Content       string     `json:"content" gorm:"column:content;type:text;comment:通知内容"`
	Image         string     `json:"image" gorm:"column:image;type:varchar(1024);not null;default:'';comment:图片URL"`
	Type          int        `json:"type" gorm:"column:type;not null;default:1;comment:通知类型 1系统 2活动 3订单 4账户"`
	Location      string     `json:"location" gorm:"column:location;type:varchar(255);not null;default:'';comment:跳转位置（path或标识）"`
	LocationLabel string     `json:"locationLabel" gorm:"column:location_label;type:varchar(100);not null;default:'';comment:跳转位置展示文案"`
	Readed        bool       `json:"readed" gorm:"column:readed;not null;default:false;comment:是否已读"`
	ReadAt        *time.Time `json:"readAt" gorm:"column:read_at;default:null;comment:已读时间"`
	DeletedAt     *time.Time `json:"-" gorm:"column:deleted_at;default:null;index;comment:软删除时间"`
}

func (Notification) TableName() string {
	return "w_notification"
}

const (
	NOTIFICATION_TYPE_SYSTEM   = 1
	NOTIFICATION_TYPE_ACTIVITY = 2
	NOTIFICATION_TYPE_ORDER    = 3
	NOTIFICATION_TYPE_ACCOUNT  = 4
)
