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

const (
	MEMBER_SUBSCRIPTION_FREE       = 1
	MEMBER_SUBSCRIPTION_PRO        = 2
	MEMBER_SUBSCRIPTION_ENTERPRISE = 3
)

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
