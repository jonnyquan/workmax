package router

import (
	"server/router/admin"
	"server/router/auth"
	"server/router/callback"
	"server/router/desktop"
	"server/router/monitor"
	"server/router/pro/account"
	"server/router/pro/blog"
	"server/router/pro/common"
	"server/router/pro/dashboard"
	"server/router/pro/prompt"
	"server/router/pro/seo"
	"server/router/pro/tools"
	"server/router/pro/use_case"
)

type RouterGroup struct {
	Account   account.RouterGroup
	Auth      auth.RouterGroup
	Common    common.RouterGroup
	Callback  callback.RouterGroup
	Blog      blog.RouterGroup
	Admin     admin.RouterGroup
	Desktop   desktop.RouterGroup
	Monitor   monitor.RouterGroup
	Dashboard dashboard.RouterGroup
	Tools     tools.RouterGroup
	UseCase   use_case.RouterGroup
	Prompt    prompt.RouterGroup
	Seo       seo.RouterGroup
}

var RouterGroupApp = new(RouterGroup)
