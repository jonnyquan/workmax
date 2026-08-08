package auth

import (
	"time"

	api "server/api"
	"server/middleware"

	"github.com/gin-gonic/gin"
)

type AuthRouter struct {
}

func (s *AuthRouter) InitAuthRouter(router *gin.RouterGroup) {
	authRouter := router.Group("api/auth")
	authApi := api.ApiGroupApp.AuthApiGroup.AuthApi

	// Rate limit sensitive auth endpoints: 10 requests per minute per IP
	authRateLimit := middleware.RateLimit(10, time.Minute)

	{
		authRouter.POST("/sign-in", authRateLimit, authApi.Signin)
		authRouter.POST("/sign-up", authRateLimit, authApi.SignUp)
		authRouter.POST("/sign-out", authApi.SignOut)
		authRouter.GET("/google", authApi.GoogleLogin)
		authRouter.GET("/google/callback", authApi.GoogleCallback)
		authRouter.POST("/forgot-password", authRateLimit, authApi.ForgotPassword)
		authRouter.POST("/change-password", authRateLimit, authApi.ChangePassword)
		authRouter.GET("/session", authApi.GetSession)
		authRouter.GET("/token", authApi.GetAccessToken)
	}
}
