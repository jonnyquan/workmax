package model

import (
	"server/globals"
	"time"
)

type UserFeedback struct {
	globals.GraMODEL
	Deleted       time.Time `json:"deleted" gorm:"column:deleted;type:datetime;default:null;comment:删除时间"`
	UID           int       `json:"uid" gorm:"column:uid;not null;comment:用户ID"`
	FeedbackType  string    `json:"feedbackType" gorm:"column:feedback_type;type:varchar(50);default:null;comment:反馈类型"`
	Content       string    `json:"content" gorm:"column:content;type:text;not null;comment:反馈内容"`
	Rating        int       `json:"rating" gorm:"column:rating;default:null;comment:评分1-5"`
	Status        string    `json:"status" gorm:"column:status;type:varchar(20);default:'pending';comment:状态"`
	AdminResponse string    `json:"adminResponse" gorm:"column:admin_response;type:text;comment:管理员回复"`
	ResponseTime  time.Time `json:"responseTime" gorm:"column:response_time;default:null;comment:回复时间"`
}

func (UserFeedback) TableName() string {
	return "w_user_feedback"
}

const (
	FEEDBACK_TYPE_FEATURE_REQUEST = "feature_request"
	FEEDBACK_TYPE_BUG_REPORT      = "bug_report"
	FEEDBACK_TYPE_GENERAL         = "general"
	FEEDBACK_TYPE_SUPPORT         = "support"
)

const (
	FEEDBACK_STATUS_PENDING  = "pending"
	FEEDBACK_STATUS_REVIEWED = "reviewed"
	FEEDBACK_STATUS_RESOLVED = "resolved"
	FEEDBACK_STATUS_REJECTED = "rejected"
)
