package model

import (
	"server/globals"
	"time"
)

type UserRewards struct {
	globals.GraMODEL
	UID         int       `json:"uid" gorm:"column:uid;comment:用户ID"`
	SourceType  string    `json:"sourceType" gorm:"column:source_type;comment:来源类型"`
	SourceID    int       `json:"sourceID" gorm:"column:source_id;comment:来源ID"`
	Description string    `json:"description" gorm:"column:description;comment:描述"`
	RewardItem  string    `json:"rewardItem" gorm:"column:reward_item;comment:奖励物品"`
	RewardNum   int       `json:"rewardNum" gorm:"column:reward_num;comment:奖励数量"`
	ExpireDate  time.Time `json:"expireDate" gorm:"column:expire_date;comment:过期时间"`
}

func (UserRewards) TableName() string {
	return "w_user_rewards"
}

// 奖励来源类型
const (
	UserRewardsSourceTypeInvite  = "invite"  // 邀请奖励
	UserRewardsSourceTypeCheckin = "checkin" // 签到奖励
	UserRewardsSourceTypeBonus   = "bonus"   // 其他奖励
)

// 奖励物品类型
const (
	UserRewardsItemTypeCoins    = "coins"    // Coins (即 Credits)
	UserRewardsItemTypeCredits  = "credits"  // Credits (新增，与 coins 等价)
	UserRewardsItemTypePoints   = "points"   // 社区积分
	UserRewardsItemTypeDiamonds = "diamonds" // 钻石
)

// Credits 奖励常量
const (
	INVITE_CREDITS_REWARD = 50 // 邀请奖励双方各得 50 credits
)
