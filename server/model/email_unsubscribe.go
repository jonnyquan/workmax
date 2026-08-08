package model

import (
	"server/globals"
)

type EmailUnsubscribe struct {
	globals.GraMODEL
	UID        int    `json:"uid" gorm:"column:uid;not null;comment:用户ID"`
	Email      string `json:"email" gorm:"column:email;type:varchar(255);not null;comment:邮箱地址"`
	Reason     string `json:"reason" gorm:"column:reason;type:varchar(500);comment:退订原因"`
	CampaignID *int   `json:"campaignId" gorm:"column:campaign_id;comment:触发退订的活动ID"`
	IP         string `json:"ip" gorm:"column:ip;type:varchar(100);comment:退订IP"`
}

func (EmailUnsubscribe) TableName() string {
	return "w_email_unsubscribe"
}
