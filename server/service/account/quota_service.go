package account

import (
	"server/globals"
	"server/model"
	"time"

	"go.uber.org/zap"
)

// QuotaService 次数配额服务在 PR4 之后已退役——所有 AI 调用统一按 credits
// 扣费，w_user_quota 表与 w_user 上的免费次数字段均已移除。保留 QuotaService
// 作为会员资格的查询入口，避免调用方大面积连锁修改。
type QuotaService struct{}

func NewQuotaService() *QuotaService {
	return &QuotaService{}
}

// IsPremiumMember 检查用户是否为**有效**的付费会员。
// 会员等级 > FREE 且 member_end_time 未过期时返回 true。被 workagent 的
// work-plus 模型准入校验使用。
func (s *QuotaService) IsPremiumMember(uid int) (bool, error) {
	var user model.User
	err := globals.GraDBs["system"].Select("member, member_end_time").First(&user, "id = ?", uid).Error
	if err != nil {
		globals.GraLog.Error("Failed to get user member info",
			zap.Int("uid", uid),
			zap.Error(err))
		return false, err
	}

	if user.Member <= model.MEMBER_SUBSCRIPTION_FREE {
		return false, nil
	}

	if !user.MemberEndTime.IsZero() && user.MemberEndTime.Before(time.Now()) {
		return false, nil
	}

	return true, nil
}
