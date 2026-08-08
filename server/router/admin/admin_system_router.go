package admin

import (
	api "server/api"

	"github.com/gin-gonic/gin"
)

type AdminSystemRouter struct {
}

func (s *AdminSystemRouter) InitAdminSystemRouter(router *gin.RouterGroup) {
	adminSystemRouter := router.Group("api")
	adminSystemApi := api.ApiGroupApp.AdminApiGroup.AdminSystemApi
	{
		adminSystemRouter.GET("/admin/system/getIdentityList", adminSystemApi.GetIdentityList)
		adminSystemRouter.PUT("/admin/system/putIdentity", adminSystemApi.PutIdentity)
		adminSystemRouter.POST("/admin/system/syncMultiLangIdentity", adminSystemApi.SyncMultiLangIdentity)
		adminSystemRouter.GET("/admin/system/getLanguageList", adminSystemApi.GetLanguageList)
		adminSystemRouter.POST("/admin/system/runSchedulerNow", adminSystemApi.RunSchedulerNow)
	}
}
