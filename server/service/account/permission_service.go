package account

import (
	"server/globals"
	"server/model"
	"time"
)

type PermissionService struct{}

// 会员等级常量
const (
	MEMBER_FREE     = 0 // 免费用户
	MEMBER_CREATOR  = 1 // 创作者版
	MEMBER_PRO      = 2 // 专业版
	MEMBER_LIFETIME = 3 // 终身版
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
	case MEMBER_FREE:
		perm.MemberName = "Free"
		perm.CanUseProModel = false
		perm.CanUseBatchGen = false
		perm.CanAccessAPI = false
		perm.FavoritesLimit = FREE_FAVORITES_LIMIT
		perm.HasAds = true
		perm.HasPrioritySupport = false
	case MEMBER_CREATOR:
		perm.MemberName = "Creator"
		perm.CanUseProModel = true
		perm.CanUseBatchGen = false
		perm.CanAccessAPI = false
		perm.FavoritesLimit = -1 // 无限
		perm.HasAds = false
		perm.HasPrioritySupport = false
	case MEMBER_PRO:
		perm.MemberName = "Pro"
		perm.CanUseProModel = true
		perm.CanUseBatchGen = true
		perm.CanAccessAPI = true
		perm.FavoritesLimit = -1
		perm.HasAds = false
		perm.HasPrioritySupport = true
	case MEMBER_LIFETIME:
		perm.MemberName = "Lifetime"
		perm.CanUseProModel = true
		perm.CanUseBatchGen = true
		perm.CanAccessAPI = true
		perm.FavoritesLimit = -1
		perm.HasAds = false
		perm.HasPrioritySupport = true
	}

	return perm
}

// getMemberLevel 获取用户会员等级
func (s *PermissionService) getMemberLevel(user *model.User) int {
	// 检查会员是否过期
	if user.Member > 0 && user.MemberEndTime.Before(time.Now()) {
		return MEMBER_FREE
	}
	return user.Member
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
