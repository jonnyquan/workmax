package middleware

import (
	"net/http"
	"server/globals"
	"server/model/common/response"
	desktopoauth "server/model/desktop/oauth"
	request "server/model/system/request"
	"server/service"
	"server/utils"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

var jwtService = service.GroupServiceApp.AccountServiceGroup.JwtService

// isDesktopAudienceToken reports whether a parsed token was minted by the
// Desktop OAuth token endpoint (api/desktop/oauth/oauth_token_api.go), which
// stamps `aud: workmax.desktop` on every access token it signs.
//
// Portal and Desktop tokens are signed with the same key, so the audience is
// the only thing separating them. Until this check existed, a 15-minute
// Desktop access token — issued to a native client with a device-scoped grant
// — was accepted by every JWTAuth-protected Portal route as a full session.
func isDesktopAudienceToken(claims *request.CustomClaims) bool {
	if claims == nil {
		return false
	}
	return claims.VerifyAudience(desktopoauth.DesktopResourceAudience, true)
}

// JWTAuth is the Portal/Admin credential. It rejects Desktop-audience tokens:
// a Desktop grant must never reach a Portal route.
//
// The two Agent routes the Desktop legitimately calls
// (/api/work-agent/skills and /api/work-agent/chat/agent, see
// server/desktop/cloud_proxy/cloud_routes.go) are mounted on their own group
// with JWTAuthAcceptingDesktopAudience instead.
func JWTAuth() gin.HandlerFunc {
	return jwtAuth(false)
}

// JWTAuthAcceptingDesktopAudience is the explicit opt-in for routes that are
// shared between the Portal (cookie/Bearer session JWT) and the Desktop client
// (OAuth access token). A Desktop-audience token additionally has to carry the
// Desktop client_id, so a token minted for some other future OAuth client
// cannot ride in on the audience alone.
//
// Deliberately narrow: mount it only on routes that Desktop actually calls and
// that have no Desktop-prefixed equivalent.
func JWTAuthAcceptingDesktopAudience() gin.HandlerFunc {
	return jwtAuth(true)
}

func jwtAuth(acceptDesktopAudience bool) gin.HandlerFunc {
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

		// Audience gate. A Desktop resource token is admissible only on a route
		// that explicitly opted in; everywhere else it is rejected outright
		// rather than downgraded, so an accidental mount cannot silently widen
		// the Desktop grant into a Portal session.
		if isDesktopAudienceToken(claims) {
			if !acceptDesktopAudience {
				response.FailWithDetailed(gin.H{"reload": true}, "token audience is not accepted on this route", c)
				c.Abort()
				return
			}
			if claims.OAuthClientID != desktopoauth.DesktopClientID {
				response.FailWithDetailed(gin.H{"reload": true}, "token was not issued for this OAuth client", c)
				c.Abort()
				return
			}
			// Desktop tokens are never rebaked here: they carry BufferTime=0
			// and rotate through /api/desktop/oauth/token. Emitting a
			// `new-token` header for one would hand the caller a Portal-shaped
			// refresh path the OAuth flow does not own.
			c.Set("claims", claims)
			c.Next()
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
