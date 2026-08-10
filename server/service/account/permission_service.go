package account

import (
	"server/globals"
	"server/model"
	"time"
)

type PermissionService struct{}

// 会员等级常量——现在只是 model 那套**唯一真源**的别名。
//
// 这里曾经有第二套定义（MEMBER_FREE=0 / MEMBER_CREATOR=1 / MEMBER_PRO=2 /
// MEMBER_LIFETIME=3），把 member=1 解释成"创作者版"并因此发放
// CanUseProModel + 无限收藏夹 + 免广告。但 member=1 是"已领取免费计划"的写入值
// （api/pro/account/stripe_api.go 的免费计划领取、以及 admin 退款降级都写 1），
// 于是每一个领过免费计划或退过款的用户都白拿了付费能力。别名化之后这条路被堵死。
const (
	MEMBER_FREE       = model.MEMBER_SUBSCRIPTION_NONE       // 0：注册后从未领计划
	MEMBER_FREE_PLAN  = model.MEMBER_SUBSCRIPTION_FREE       // 1：已领免费计划，仍非付费
	MEMBER_PRO        = model.MEMBER_SUBSCRIPTION_PRO        // 2：付费会员
	MEMBER_ENTERPRISE = model.MEMBER_SUBSCRIPTION_ENTERPRISE // 3：预留
)

// 权限限制常量
const (
	FREE_FAVORITES_LIMIT = 10 // 免费用户收藏夹限制
)

// UserPermissions 用户权限信息
type UserPermissions struct {
	CanUseProModel     bool   `json:"canUseProModel"`
	CanUseBatchGen     bool   `json:"canUseBatchGen"`
	CanAccessAPI       bool   `json:"canAccessApi"`
	FavoritesLimit     int    `json:"favoritesLimit"` // -1 表示无限
	HasAds             bool   `json:"hasAds"`
	HasPrioritySupport bool   `json:"hasPrioritySupport"`
	MemberLevel        int    `json:"memberLevel"`
	MemberName         string `json:"memberName"`
}

// GetUserPermissions 获取用户权限
func (s *PermissionService) GetUserPermissions(uid int) (*UserPermissions, error) {
	var user model.User
	err := globals.GraDBs["system"].First(&user, "id = ?", uid).Error
	if err != nil {
		return nil, err
	}

	return s.calculatePermissions(&user), nil
}

// calculatePermissions 根据用户计算权限
func (s *PermissionService) calculatePermissions(user *model.User) *UserPermissions {
	memberLevel := s.getMemberLevel(user)

	perm := &UserPermissions{
		MemberLevel: memberLevel,
	}

	switch memberLevel {
	case MEMBER_PRO:
		perm.MemberName = "Pro"
		perm.CanUseProModel = true
		perm.CanUseBatchGen = true
		perm.CanAccessAPI = true
		perm.FavoritesLimit = -1
		perm.HasAds = false
		perm.HasPrioritySupport = true
	case MEMBER_ENTERPRISE:
		perm.MemberName = "Enterprise"
		perm.CanUseProModel = true
		perm.CanUseBatchGen = true
		perm.CanAccessAPI = true
		perm.FavoritesLimit = -1
		perm.HasAds = false
		perm.HasPrioritySupport = true
	default:
		// MEMBER_FREE / MEMBER_FREE_PLAN 以及任何未知等级都落到免费档。
		// default 分支是有意的：统一之前未知等级会返回一个零值 perm
		// （MemberName="" 且 FavoritesLimit=0），比免费用户还严格，且没有
		// 任何一层告诉调用方发生了什么。
		perm.MemberName = "Free"
		perm.CanUseProModel = false
		perm.CanUseBatchGen = false
		perm.CanAccessAPI = false
		perm.FavoritesLimit = FREE_FAVORITES_LIMIT
		perm.HasAds = true
		perm.HasPrioritySupport = false
	}

	return perm
}

// getMemberLevel 获取用户**折算过期之后**的会员等级。
//
// 过期判定统一委托给 model.EffectiveMemberLevel，与计费链路同一套语义：
// member_end_time 没写 = 无限期授予（不再被当成"零值早于 now 所以已过期"）。
func (s *PermissionService) getMemberLevel(user *model.User) int {
	return model.EffectiveMemberLevel(user.Member, user.MemberEndTime, time.Now())
}

// CanUseProModel 检查用户是否可以使用 Pro 模型
func (s *PermissionService) CanUseProModel(uid int) bool {
	perm, err := s.GetUserPermissions(uid)
	if err != nil {
		return false
	}
	return perm.CanUseProModel
}

// CanUseBatchGeneration 检查用户是否可以使用批量生成
func (s *PermissionService) CanUseBatchGeneration(uid int) bool {
	perm, err := s.GetUserPermissions(uid)
	if err != nil {
		return false
	}
	return perm.CanUseBatchGen
}

// GetFavoritesLimit 获取用户收藏夹限制
func (s *PermissionService) GetFavoritesLimit(uid int) int {
	perm, err := s.GetUserPermissions(uid)
	if err != nil {
		return FREE_FAVORITES_LIMIT
	}
	return perm.FavoritesLimit
}

// CheckFavoritesQuota 检查收藏夹配额
func (s *PermissionService) CheckFavoritesQuota(uid int, currentCount int) bool {
	limit := s.GetFavoritesLimit(uid)
	if limit == -1 {
		return true // 无限制
	}
	return currentCount < limit
}
