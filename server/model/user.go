package model

import (
	"server/globals"
	"time"
)

type User struct {
	globals.GraMODEL
	Email               string    `json:"email" gorm:"column:email;type:varchar(100);default:'';uniqueIndex;comment:邮箱"`
	Phone               string    `json:"phone" gorm:"column:phone;type:varchar(100);default:'';comment:手机号"`
	Nickname            string    `json:"nickname" gorm:"column:nickname;type:varchar(100);default:'';comment:昵称"`
	Avatar              string    `json:"avatar" gorm:"column:avatar;type:varchar(100);default:'';comment:头像"`
	Password            string    `json:"-" gorm:"column:password;type:text;comment:密码"`
	LoginTime           time.Time `json:"loginTime" gorm:"column:login_time;comment:最后登录时间"`
	LoginIP             string    `json:"loginIp" gorm:"column:login_ip;type:varchar(100);default:'';comment:最后登录IP"`
	LoginAddress        string    `json:"loginAddress" gorm:"column:login_address;type:varchar(100);comment:登录地址"`
	Role                string    `json:"role" gorm:"column:role;type:varchar(20);default:'user';comment:用户角色"`
	IdentityCode        string    `json:"identityCode" gorm:"column:identity_code;type:varchar(50);default:'';comment:关联身份Code"`
	Fields              string    `json:"fields" gorm:"column:fields;type:varchar(248);comment:领域"`
	Ban                 bool      `json:"ban" gorm:"column:ban;type:tinyint(1);default:0;comment:是否禁用"`
	AuthEmail           int       `json:"authEmail" gorm:"column:auth_email;default:0;comment:邮箱认证"`
	InviteUID           int       `json:"inviteUid" gorm:"column:invite_uid;default:0;comment:邀请人"`
	PromotionAmount     float64   `json:"promotionAmount" gorm:"column:promotion_amount;type:double(13,2);default:0.00;comment:推广金额"`
	BanExpireTime       time.Time `json:"banExpireTime" gorm:"column:ban_expire_time;default:null;comment:禁用到期时间"`
	BanNote             string    `json:"banNote" gorm:"column:ban_note;type:varchar(255);comment:禁用原因"`
	Member              int       `json:"member" gorm:"column:member;type:tinyint(1);default:0;comment:会员等级"`
	MemberStartTime     time.Time `json:"memberStartTime" gorm:"column:member_start_time;default:null;comment:开通会员时间"`
	MemberEndTime       time.Time `json:"memberEndTime" gorm:"column:member_end_time;default:null;comment:会员截止时间"`
	MemberSubscription  string    `json:"memberSubscription" gorm:"column:member_subscription;type:varchar(100);default:'';comment:订阅计划"`
	Timezone            string    `json:"timezone" gorm:"column:time_zone;type:varchar(100);default:'';comment:时区"`
	Lang                string    `json:"lang" gorm:"column:lang;type:varchar(100);default:'';comment:语言"`
	NotificationSetting string    `json:"notificationSetting" gorm:"column:notification_setting;type:varchar(100);default:'';comment:通知设置"`
	InviteCode          string    `json:"inviteCode" gorm:"column:invite_code;type:varchar(100);default:'';index;comment:邀请码"`
	ApiKey              string    `json:"-" gorm:"column:api_key;type:varchar(200);default:'';comment:API密钥"`
}

func (User) TableName() string {
	return "w_user"
}

// 会员等级（w_user.member）。**这是唯一的一套定义**——它就是数据库里真正被写入
// 的取值，所有读取点都必须用这里的常量与下面的三个 helper，不允许再各自手写
// `member > 1` / `member == 0` 之类的判断。
//
// 写入方（判定依据，2026-08 复核）：
//   - 注册（api/auth/auth_api.go 的邮箱注册与 Google 注册）都不给 member 赋值，
//     落库拿列默认值 0 → MEMBER_SUBSCRIPTION_NONE。
//   - 领取免费计划（api/pro/account/stripe_api.go RegisterFreeSubscription）
//     写 1 → MEMBER_SUBSCRIPTION_FREE。
//   - 付费入账（service/account/account_service.go updateUserMemberTx）写 2 →
//     MEMBER_SUBSCRIPTION_PRO；退款降级（api/admin/admin_order_api.go）写回 1。
//   - 3（ENTERPRISE）目前没有任何写入方，保留为将来的席位/企业版留位。
//
// 因此 0 与 1 **都表示"没有付费权益"**，只是 1 额外表示"已领取免费计划、有一个
// 免费计划窗口"。曾经在 service/account/permission_service.go 里把 1 解释成
// "创作者版" 的第二套常量已经删除：没有任何写入方以那个含义写过 1，所以线上数据
// 不存在两种含义混用，不需要数据迁移。
const (
	MEMBER_SUBSCRIPTION_NONE       = 0 // 注册默认值：从未领取任何计划
	MEMBER_SUBSCRIPTION_FREE       = 1 // 已领取免费计划（仍然不是付费会员）
	MEMBER_SUBSCRIPTION_PRO        = 2 // 付费会员
	MEMBER_SUBSCRIPTION_ENTERPRISE = 3 // 预留：企业版
)

// 会员等级对外的 tier 字符串。桌面 userinfo、模型目录、技能目录共用同一套词表，
// 避免同一个 member 整数在不同端点被翻译成不同的名字。
const (
	MemberTierFree       = "free"
	MemberTierPro        = "pro"
	MemberTierEnterprise = "enterprise"
)

// IsActivePaidMember 是"当前是不是有效付费会员"的唯一判定。
//
// 语义与计费/credits 主链路（service/account/credits_pack_service.go 的
// isSubscriptionUserActive / isSubscriptionCreditsActiveTx、
// api/pro/account/stripe_api.go 的 hasActivePaidMembership）逐字一致：
// 等级高于免费，且 member_end_time 要么没写（无限期授予），要么还没到期。
//
// 统一之前 permission_service 把"未写 end_time"当成已过期、user_lookup 用
// `now.Before(end)` 也把零值当成已过期，与花钱那条链路互相矛盾；现在一律以
// 计费链路为准。
func IsActivePaidMember(memberLevel int, memberEndTime time.Time, now time.Time) bool {
	if memberLevel <= MEMBER_SUBSCRIPTION_FREE {
		return false
	}
	return memberEndTime.IsZero() || memberEndTime.After(now)
}

// EffectiveMemberLevel 返回把"过期"折算进去之后的等级：过期的付费会员塌回
// MEMBER_SUBSCRIPTION_NONE，免费档（0/1）原样返回。
func EffectiveMemberLevel(memberLevel int, memberEndTime time.Time, now time.Time) int {
	if memberLevel <= MEMBER_SUBSCRIPTION_NONE {
		return MEMBER_SUBSCRIPTION_NONE
	}
	if memberLevel <= MEMBER_SUBSCRIPTION_FREE {
		return MEMBER_SUBSCRIPTION_FREE
	}
	if IsActivePaidMember(memberLevel, memberEndTime, now) {
		return memberLevel
	}
	return MEMBER_SUBSCRIPTION_NONE
}

// MemberTierName 把一个**已经折算过期**的等级翻译成 tier 字符串。想从原始
// user.Member 出发请用 EffectiveMemberTier。
func MemberTierName(memberLevel int) string {
	switch {
	case memberLevel >= MEMBER_SUBSCRIPTION_ENTERPRISE:
		return MemberTierEnterprise
	case memberLevel == MEMBER_SUBSCRIPTION_PRO:
		return MemberTierPro
	default:
		return MemberTierFree
	}
}

// EffectiveMemberTier 是给对外端点用的一步到位版本：过期即 free。
func EffectiveMemberTier(memberLevel int, memberEndTime time.Time, now time.Time) string {
	return MemberTierName(EffectiveMemberLevel(memberLevel, memberEndTime, now))
}

// 配额维度分类。PR4 之后次数配额已退役，常量仅作为 usage_record 的 tool_id 标签保留。
// TOOL_AGENT 是 TOOL_AI_AGENT 的别名，历史原因被 workagent usage 记录使用。
const (
	TOOL_AI_AGENT  = "ai_agent"
	TOOL_AGENT     = "ai_agent"
	TOOL_PRO_TOOLS = "pro_tools"
)

// 次数重置周期常量。PR4 之后次数配额已取消，RESET_TYPE_MONTHLY 仅作为
// account_api DTO 的默认值返回给前端（形状保持兼容）。
const (
	RESET_TYPE_MONTHLY = "monthly"
)
