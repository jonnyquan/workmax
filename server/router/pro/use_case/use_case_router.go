package use_case

import (
	"server/api"

	"github.com/gin-gonic/gin"
)

type UseCaseRouter struct{}

func (r *UseCaseRouter) InitUseCaseRouter(Router *gin.RouterGroup) {
	useCaseRouter := Router.Group("api/use-cases")
	useCaseApi := api.ApiGroupApp.UseCaseApiGroup.UseCaseApi
	{
		useCaseRouter.GET("list", useCaseApi.GetUseCaseList)
		useCaseRouter.GET("by-app/:appSlug", useCaseApi.GetUseCasesByApp)
		useCaseRouter.GET(":slug", useCaseApi.GetUseCase)
	}
}
