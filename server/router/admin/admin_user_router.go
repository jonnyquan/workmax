package admin

import (
	api "server/api"

	"github.com/gin-gonic/gin"
)

type AdminUserRouter struct {
}

func (s *AdminUserRouter) InitAdminUserRouter(router *gin.RouterGroup) {
	adminUserRouter := router.Group("api")
	adminUserApi := api.ApiGroupApp.AdminApiGroup.AdminUserApi
	{
		adminUserRouter.GET("/admin/user/getUserList", adminUserApi.GetUserList)
		adminUserRouter.GET("/admin/user/getUserStatistic", adminUserApi.GetUserStatistic)
		adminUserRouter.POST("/admin/user/putUser", adminUserApi.PutUser)
		adminUserRouter.POST("/admin/user/deleteUsers", adminUserApi.DeleteUsers)
		adminUserRouter.POST("/admin/user/grantCreditsPack", adminUserApi.GrantCreditsPack)
	}
}
