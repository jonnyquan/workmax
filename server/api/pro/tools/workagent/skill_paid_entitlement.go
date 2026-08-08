package workagent

import (
	"strings"

	workagentService "server/service/tools/workagent"
)

func hasSkillRequiredTierEntitlement(uid int, requiredTier string) bool {
	tier := strings.ToLower(strings.TrimSpace(requiredTier))
	if uid <= 0 || tier == "" {
		return false
	}
	switch tier {
	case "pro", "premium", "paid":
		return workagentService.IsUserPremium(uint(uid))
	default:
		return false
	}
}
