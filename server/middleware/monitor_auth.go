package middleware

import (
	"crypto/subtle"
	"os"
	"strings"

	"server/model/common/response"

	"github.com/gin-gonic/gin"
)

func MonitorAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		expectedToken := os.Getenv("MONITOR_TOKEN")
		if expectedToken == "" {
			response.FailWithMessage("monitor token not configured", c)
			c.Abort()
			return
		}

		providedToken := c.GetHeader("X-Monitor-Token")
		if providedToken == "" {
			authorization := c.GetHeader("Authorization")
			if strings.HasPrefix(authorization, "Bearer ") {
				providedToken = strings.TrimPrefix(authorization, "Bearer ")
			}
		}

		if subtle.ConstantTimeCompare([]byte(providedToken), []byte(expectedToken)) != 1 {
			response.FailWithMessage("unauthorized monitor access", c)
			c.Abort()
			return
		}

		c.Next()
	}
}
