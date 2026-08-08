package model

import (
	"server/globals"
	"time"
)

type CreditsPack struct {
	globals.GraMODEL
	UID          int        `json:"uid" gorm:"column:uid;comment:用户ID"`
	SourceType   string     `json:"sourceType" gorm:"column:source_type;type:varchar(50);comment:来源类型"`
	SourceID     string     `json:"sourceId" gorm:"column:source_id;type:varchar(64);comment:来源ID"`
	CreditsTotal int        `json:"creditsTotal" gorm:"column:credits_total;comment:本笔积分总量"`
	CreditsUsed  int        `json:"creditsUsed" gorm:"column:credits_used;comment:本笔已使用积分"`
	ExpiresAt    *time.Time `json:"expiresAt" gorm:"column:expires_at;comment:过期时间(会员月度额度)"`
	Remark       string     `json:"remark" gorm:"column:remark;type:varchar(255);comment:备注"`
}

func (CreditsPack) TableName() string {
	return "w_credits_pack"
}

const (
	CreditsSourceSubscription = "subscription"
	CreditsSourcePurchase     = "purchase"
	CreditsSourceCheckin      = "checkin"
	CreditsSourceInvite       = "invite"
	CreditsSourceBonus        = "bonus"
	CreditsSourceAdmin        = "admin"
	CreditsSourceLegacy       = "legacy"
)
