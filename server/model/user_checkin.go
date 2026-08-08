package model

import (
	"server/globals"
	"time"
)

// UserCheckin 用户签到记录
type UserCheckin struct {
	globals.GraMODEL
	UID           int       `json:"uid" gorm:"column:uid;not null;index;uniqueIndex:idx_user_checkin_uid_date,priority:1;comment:用户ID"`
	CheckinDate   time.Time `json:"checkinDate" gorm:"column:checkin_date;type:date;not null;index;uniqueIndex:idx_user_checkin_uid_date,priority:2;comment:签到日期"`
	CreditsEarned int       `json:"creditsEarned" gorm:"column:credits_earned;default:0;comment:获得的Credits"`
	StreakDays    int       `json:"streakDays" gorm:"column:streak_days;default:1;comment:连续签到天数"`
	BonusEarned   int       `json:"bonusEarned" gorm:"column:bonus_earned;default:0;comment:额外奖励Credits"`
}

func (UserCheckin) TableName() string {
	return "w_user_checkin"
}

// 签到奖励常量
const (
	CHECKIN_MIN_CREDITS  = 1  // 每日签到最低Credits
	CHECKIN_MAX_CREDITS  = 3  // 每日签到最高Credits
	CHECKIN_STREAK_BONUS = 10 // 连续7天额外奖励
	CHECKIN_STREAK_DAYS  = 7  // 连续签到天数要求
)

// CheckinResult 签到结果
type CheckinResult struct {
	Success       bool   `json:"success"`
	Message       string `json:"message"`
	CreditsEarned int    `json:"creditsEarned"`
	BonusEarned   int    `json:"bonusEarned"`
	TotalEarned   int    `json:"totalEarned"`
	StreakDays    int    `json:"streakDays"`
	AlreadyDone   bool   `json:"alreadyDone"`
}

// CheckinStatus 签到状态
type CheckinStatus struct {
	CheckedInToday bool      `json:"checkedInToday"`
	StreakDays     int       `json:"streakDays"`
	LastCheckin    time.Time `json:"lastCheckin"`
	TotalCheckins  int       `json:"totalCheckins"`
	TotalCredits   int       `json:"totalCredits"`
}
