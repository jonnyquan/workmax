package middleware

import (
	"net/http"
	"server/globals"
	"server/model/common/response"
	"server/service"
	"server/utils"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

var jwtService = service.GroupServiceApp.AccountServiceGroup.JwtService

// JWTAuth JWT鉴权中间件
func JWTAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		var token string

		// 1. 优先从 Authorization Header 获取 token
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

		// 3. 如果两处都没有 token，返回未授权
		if token == "" {
			response.FailWithDetailed(gin.H{"reload": true}, "unauthorized access", c)
			c.Abort()
			return
		}

		// JWT黑名单功能已移除 - 使用无状态JWT

		// 解析token包含的信息
		j := utils.NewJWT()
		claims, err := j.ParseToken(token)
		if err != nil {
			if err == utils.TokenExpired {
				response.FailWithDetailed(gin.H{"reload": true}, "token expired", c)
				c.Abort()
				return
			}
			response.FailWithDetailed(gin.H{"reload": true}, err.Error(), c)
			c.Abort()
			return
		}

		// 如果token即将过期 则刷新token
		if claims.ExpiresAt-time.Now().Unix() < claims.BufferTime {
			dr, _ := utils.ParseDuration(globals.GraConf.JWT.ExpiresTime)
			claims.ExpiresAt = time.Now().Add(dr).Unix()
			newToken, _ := j.CreateTokenByOldToken(token, *claims)
			newClaims, _ := j.ParseToken(newToken)

			// 设置 Header（兼容旧前端）
			c.Header("new-token", newToken)
			c.Header("new-expires-at", strconv.FormatInt(newClaims.ExpiresAt, 10))

			// ✨ 新增：如果使用 Cookie 认证，也更新 Cookie
			// 检测当前请求是否使用 Cookie 认证
			if _, cookieErr := c.Cookie("access_token"); cookieErr == nil {
				maxAge := int(newClaims.ExpiresAt - time.Now().Unix())
				env := globals.GraConf.System.Env
				isProduction := env == "production" || env == "prod"

				// 动态提取 Cookie Domain
				cookieDomain := extractCookieDomain()

				c.SetSameSite(http.SameSiteLaxMode)
				c.SetCookie(
					"access_token",
					newToken,
					maxAge,
					"/",
					cookieDomain,
					isProduction,
					true,
				)
				globals.GraLog.Info("JWT refreshed and Cookie updated",
					zap.Uint("user_id", claims.BaseClaims.Id))
			}
		}

		c.Set("claims", claims)
		c.Next()
	}
}

// extractCookieDomain 动态提取 Cookie Domain（支持任意域名）
// 从 FrontendURL 配置中自动提取域名，避免硬编码
func extractCookieDomain() string {
	env := globals.GraConf.System.Env
	isProduction := env == "production" || env == "prod"

	if !isProduction {
		return ""
	}

	frontendURL := globals.GraConf.System.FrontendURL
	if strings.HasPrefix(frontendURL, "https://") {
		domain := strings.TrimPrefix(frontendURL, "https://")
		domain = strings.TrimPrefix(domain, "www.")
		// 移除路径部分
		if idx := strings.Index(domain, "/"); idx != -1 {
			domain = domain[:idx]
		}
		// 如果是顶级域名，添加前缀点以允许子域名共享
		if strings.Count(domain, ".") >= 1 {
			return "." + domain
		}
	}
	return ""
}
