package model

import (
	"server/globals"
	"time"
)

// Team is the minimal collaboration unit. Phase 2 surfaces owner + plain
// members; more granular roles land in a later iteration when real usage
// patterns make the extra complexity worth paying for.
type Team struct {
	globals.GraMODEL
	DeletedAt   *time.Time `json:"-" gorm:"column:deleted_at;index"`
	OwnerUID    int        `json:"ownerUid" gorm:"column:owner_uid;not null;index:idx_owner;comment:创建者UID"`
	Name        string     `json:"name" gorm:"column:name;type:varchar(120);not null;default:''"`
	Slug        string     `json:"slug" gorm:"column:slug;type:varchar(64);not null;default:'';uniqueIndex:uk_slug,priority:1"`
	Description string     `json:"description" gorm:"column:description;type:varchar(500);not null;default:''"`
	Status      int8       `json:"status" gorm:"column:status;type:tinyint;not null;default:1;comment:1=active 2=archived"`
}

func (Team) TableName() string {
	return "w_team"
}

const (
	TeamStatusActive   int8 = 1
	TeamStatusArchived int8 = 2
)

// TeamMember is a many-to-many link between users and teams. The owner
// always has a row with role="owner" — simplifies permission queries
// (every privileged operation just needs a team_member row), and lets
// ownership transfer later be a single row swap.
type TeamMember struct {
	globals.GraMODEL
	DeletedAt *time.Time `json:"-" gorm:"column:deleted_at;index"`
	TeamID    uint64     `json:"teamId" gorm:"column:team_id;type:bigint unsigned;not null;uniqueIndex:uk_team_uid,priority:1;index:idx_team"`
	UID       int        `json:"uid" gorm:"column:uid;not null;uniqueIndex:uk_team_uid,priority:2;index:idx_uid"`
	Role      string     `json:"role" gorm:"column:role;type:varchar(32);not null;default:'member';comment:owner|member"`
	Status    int8       `json:"status" gorm:"column:status;type:tinyint;not null;default:1;comment:1=active 2=invited 3=removed"`
}

func (TeamMember) TableName() string {
	return "w_team_member"
}

const (
	TeamMemberRoleOwner  = "owner"
	TeamMemberRoleMember = "member"

	TeamMemberStatusActive  int8 = 1
	TeamMemberStatusInvited int8 = 2
	TeamMemberStatusRemoved int8 = 3
)
