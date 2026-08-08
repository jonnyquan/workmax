package model

import (
	"server/globals"
	"time"
)

// GlobalProjectMember stores explicit project membership. The owner remains
// denormalized on w_global_project.uid for compatibility; this table is the
// shared permission layer for collaboration and future teams.
type GlobalProjectMember struct {
	globals.GraMODEL
	DeletedAt    *time.Time `json:"-" gorm:"column:deleted_at;index"`
	ProjectID    uint       `json:"projectId" gorm:"column:project_id;type:bigint unsigned;not null;uniqueIndex:uk_project_member_user,priority:1;index:idx_project_member_project_role,priority:1;comment:项目ID"`
	UID          int        `json:"uid" gorm:"column:uid;not null;uniqueIndex:uk_project_member_user,priority:2;index:idx_project_member_uid_access,priority:1;comment:成员用户ID"`
	Role         string     `json:"role" gorm:"column:role;type:varchar(20);not null;default:'viewer';index:idx_project_member_project_role,priority:2;comment:owner/editor/viewer/commenter"`
	Source       string     `json:"source" gorm:"column:source;type:varchar(32);not null;default:'invite';comment:owner/invite/team/shared_copy"`
	CreatedBy    int        `json:"createdBy" gorm:"column:created_by;not null;default:0;comment:邀请/创建人"`
	LastAccessAt *time.Time `json:"lastAccessAt,omitempty" gorm:"column:last_access_at;type:datetime;default:null;index:idx_project_member_uid_access,priority:2;comment:最近访问时间"`
}

func (GlobalProjectMember) TableName() string {
	return "w_global_project_member"
}

const (
	GlobalProjectRoleOwner     = "owner"
	GlobalProjectRoleEditor    = "editor"
	GlobalProjectRoleViewer    = "viewer"
	GlobalProjectRoleCommenter = "commenter"

	GlobalProjectMemberSourceOwner      = "owner"
	GlobalProjectMemberSourceInvite     = "invite"
	GlobalProjectMemberSourceTeam       = "team"
	GlobalProjectMemberSourceSharedCopy = "shared_copy"
)
