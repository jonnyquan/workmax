package admin

type RouterGroup struct {
	AdminDashboardRouter
	AdminUserRouter
	AdminOrderRouter
	AdminBlogRouter
	AdminSystemRouter
	AdminFeedbackRouter
	AdminMarketingRouter
	AdminUseCaseRouter
	AdminGeneratorRouter
	AdminAgentAccountRouter
	AdminThreadRouter
	AdminMessageRouter
	AdminPromptRouter
	AdminWorkAgentKnowledgeRouter
}
