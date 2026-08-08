package admin

import (
	"server/api"

	"github.com/gin-gonic/gin"
)

type AdminUseCaseRouter struct {
}

func (s *AdminUseCaseRouter) InitAdminUseCaseRouter(Router *gin.RouterGroup) {
	adminUseCaseRouter := Router.Group("api/admin/use-cases")
	adminUseCaseApi := api.ApiGroupApp.AdminApiGroup.AdminUseCaseApiGroup.UseCaseApi
	{
		adminUseCaseRouter.POST("create", adminUseCaseApi.CreateUseCase)
		adminUseCaseRouter.POST("delete", adminUseCaseApi.DeleteUseCase)
		adminUseCaseRouter.POST("deleteByIds", adminUseCaseApi.DeleteUseCaseByIds)
		adminUseCaseRouter.POST("update", adminUseCaseApi.UpdateUseCase)
		adminUseCaseRouter.GET("get", adminUseCaseApi.GetUseCase)
		adminUseCaseRouter.POST("list", adminUseCaseApi.GetUseCaseList)
	}
}
