package config

// Credits 用户积分相关配置（注册赠送、邀请奖励、签到奖励等）。
//
// 任意字段为 0 / 未配置时，对应服务会退回到代码内的安全默认值（见
// server/service/account/*.go 与 server/model/user_checkin.go 中的常量），
// 以保持 YAML 缺省时的向后兼容；新增字段时请同步更新两个 config.yaml。
type Credits struct {
	SignupBonus      int            `mapstructure:"signup_bonus" json:"signup_bonus" yaml:"signup_bonus"`                   // 新用户注册赠送的 credits 数量
	Invite           int            `mapstructure:"invite" json:"invite" yaml:"invite"`                                     // 邀请奖励：邀请人与被邀请人各得多少 credits
	RewardExpireDays int            `mapstructure:"reward_expire_days" json:"reward_expire_days" yaml:"reward_expire_days"` // 注册/邀请/签到等奖励 credits 的有效期天数
	Checkin          CheckinCredits `mapstructure:"checkin" json:"checkin" yaml:"checkin"`                                  // 每日签到与连签奖励
}

// CheckinCredits 每日签到的奖励规则。
//
// 每日签到额度在 [MinPerDay, MaxPerDay] 之间随机取值；连续签到达到 StreakDays
// 整数倍时额外发 StreakBonus。任意字段为 0 时退回代码默认（model 常量）。
type CheckinCredits struct {
	MinPerDay   int `mapstructure:"min_per_day" json:"min_per_day" yaml:"min_per_day"`       // 每日签到最低 credits
	MaxPerDay   int `mapstructure:"max_per_day" json:"max_per_day" yaml:"max_per_day"`       // 每日签到最高 credits
	StreakDays  int `mapstructure:"streak_days" json:"streak_days" yaml:"streak_days"`       // 连续签到的步长（默认 7）
	StreakBonus int `mapstructure:"streak_bonus" json:"streak_bonus" yaml:"streak_bonus"`   // 连签达成时额外奖励
}
