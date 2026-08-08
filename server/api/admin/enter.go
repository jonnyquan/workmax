package admin

import (
	"server/api/admin/marketing"
	"server/api/admin/use_case"
)

type AdminApiGroup struct {
	AdminDashboardApi
	AdminUserApi
	AdminOrderApi
	AdminBlogApi
	AdminSystemApi
	AdminFeedbackApi
	AdminGeneratorApi
	AdminAgentAccountApi
	AdminThreadApi
	AdminMessageApi
	AdminPromptApi
	AdminWorkAgentKnowledgeApi
	AdminMarketingApiGroup marketing.ApiGroup
	AdminUseCaseApiGroup   use_case.UseCaseApiGroup
}
