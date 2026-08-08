package workagent

import (
	"time"

	"server/globals"
)

const (
	SkillAccessRequestStatusPending  = "pending"
	SkillAccessRequestStatusApproved = "approved"
	SkillAccessRequestStatusRejected = "rejected"
)

type SkillAccessRequest struct {
	globals.GraMODEL
	UID        int        `json:"uid" gorm:"column:uid;not null;index;comment:用户ID"`
	AgentMode  string     `json:"agentMode" gorm:"column:agent_mode;type:varchar(80);not null;default:'';index:idx_skill_access_uid_agent,unique;comment:requested skill agent mode"`
	Source     string     `json:"source" gorm:"column:source;type:varchar(64);not null;default:'';comment:official/marketplace/beta"`
	Status     string     `json:"status" gorm:"column:status;type:varchar(32);not null;default:'pending';index;comment:pending/approved/rejected"`
	Reason     string     `json:"reason" gorm:"column:reason;type:varchar(512);not null;default:'';comment:user supplied access reason"`
	ReviewedBy int        `json:"reviewedBy" gorm:"column:reviewed_by;not null;default:0;index;comment:manager reviewer uid"`
	ReviewedAt *time.Time `json:"reviewedAt,omitempty" gorm:"column:reviewed_at;comment:latest review timestamp"`
	ReviewNote string     `json:"reviewNote" gorm:"column:review_note;type:varchar(512);not null;default:'';comment:manager review note"`
}

func (SkillAccessRequest) TableName() string {
	return "w_workagent_skill_access_request"
}
