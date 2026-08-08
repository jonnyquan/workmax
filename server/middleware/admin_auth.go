package middleware

import (
	"server/model/common/response"
	"server/service"
	"server/utils"

	"github.com/gin-gonic/gin"
)

// AdminAuth 管理员鉴权中间件
func AdminAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		uid := utils.GetUserID(c)
		user, err := service.GroupServiceApp.AccountServiceGroup.AccountService.GetUserInfo(uid)
		if err != nil {
			response.FailWithMessage(err.Error(), c)
			c.Abort()
			return
		}
		if user.Role != "manager" {
			response.FailWithMessage("No permission", c)
			c.Abort()
			return
		}
		c.Next()
	}
}
