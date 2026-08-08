package utils

import (
	"errors"
	systemReq "server/model/system/request"
	"strings"

	"github.com/gin-gonic/gin"
)

func GetClaims(c *gin.Context) (*systemReq.CustomClaims, error) {
	var token string

	// 1. 优先从 Authorization Header 获取 token（兼容现有方式）
	Authorization := c.Request.Header.Get("Authorization")
	if Authorization != "" && strings.HasPrefix(Authorization, "Bearer ") {
		token = strings.Replace(Authorization, "Bearer ", "", 1)
		// 排除无效的 token 值（如 "null", "undefined"）
		if token == "null" || token == "undefined" {
			token = ""
		}
	}

	// 2. 如果 Header 中没有有效 token，尝试从 Cookie 获取
	// ✨ 新增：支持 HttpOnly Cookie 认证（更安全）
	if token == "" {
		cookieToken, err := c.Cookie("access_token")
		if err == nil && cookieToken != "" {
			token = cookieToken
		}
	}

	// 3. 如果两处都没有 token，返回错误
	if token == "" {
		return nil, errors.New("no token found in request")
	}

	// 4. 解析 JWT token
	j := NewJWT()
	claims, err := j.ParseToken(token)
	if err != nil {
		//globals.GraLog.Error("基于Context获取用户并解析JWT失败，请检查请求Header是否包含Authorization")
		return nil, err
	}
	return claims, err
}

// GetUserID 从Gin的Context中获取从jwt解析出来的用户ID
func GetUserID(c *gin.Context) uint {
	if claims, exists := c.Get("claims"); !exists {
		if cl, err := GetClaims(c); err != nil {
			return 0
		} else {
			return cl.BaseClaims.Id
		}
	} else {
		waitUse := claims.(*systemReq.CustomClaims)
		return waitUse.BaseClaims.Id
	}
}
