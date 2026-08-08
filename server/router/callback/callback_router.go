package callback

import (
	api "server/api"

	"github.com/gin-gonic/gin"
)

type CallbackRouter struct {
}

var (
	stripeApi = api.ApiGroupApp.CallbackApiGroup.StripeCallbackApi
)

func (s *CallbackRouter) InitCallbackRouter(router *gin.RouterGroup) {
	Router := router.Group("api/callback")
	{
		Router.POST("/subscription/stripe", stripeApi.StripeCallback)
	}
}
