package service

import (
	"server/service/account"
	"server/service/auth"
	"server/service/common"
	"server/service/marketing"
	"server/service/system"
	"server/service/tools"
)

type GroupService struct {
	SystemServiceGroup    system.ServiceGroup
	AccountServiceGroup   account.ServiceGroup
	AuthServiceGroup      auth.ServiceGroup
	CommonServiceGroup    common.ServiceGroup
	ToolServiceGroup      tools.ToolsServiceGroup
	MarketingServiceGroup marketing.ServiceGroup
}

var GroupServiceApp = new(GroupService)
