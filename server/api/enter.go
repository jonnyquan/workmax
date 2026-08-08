package api

import (
	admin "server/api/admin"
	auth "server/api/auth"
	callback "server/api/callback"
	desktop "server/api/desktop"
	monitor "server/api/monitor"
	account "server/api/pro/account"
	blog "server/api/pro/blog"
	common "server/api/pro/common"
	dashboard "server/api/pro/dashboard"
	prompt "server/api/pro/prompt"
	seo "server/api/pro/seo"
	tools "server/api/pro/tools"
	"server/api/pro/use_case"
)

type ApiGroup struct {
	AccountApiGroup   account.AccountApiGroup
	AuthApiGroup      auth.AuthApiGroup
	CommonApiGroup    common.CommonApiGroup
	DashboardApiGroup dashboard.DashboardApiGroup
	CallbackApiGroup  callback.CallbackApiGroup
	BlogApiGroup      blog.BlogApiGroup
	AdminApiGroup     admin.AdminApiGroup
	DesktopApiGroup   desktop.DesktopApiGroup
	MonitorApiGroup   monitor.MonitorApiGroup
	ToolsApiGroup     tools.ToolsApiGroup
	UseCaseApiGroup   use_case.UseCaseApiGroup
	PromptApiGroup    prompt.PromptApiGroup
	SeoApiGroup       seo.SeoApiGroup
}

var ApiGroupApp = new(ApiGroup)
