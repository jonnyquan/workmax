package auth

import (
	cryptorand "crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"server/globals"
	"server/model"
	"server/model/common/response"
	"server/model/system/request"
	systemReq "server/model/system/request"
	systemRes "server/model/system/response"
	"server/service"
	"server/service/account"
	"server/utils"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"golang.org/x/exp/rand"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

type AuthApi struct {
}

type googleOAuthState struct {
	Nonce       string `json:"nonce"`
	InviteCode  string `json:"inviteCode,omitempty"`
	RedirectURL string `json:"redirectUrl,omitempty"`
}

func generateSecureStateToken(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := cryptorand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func buildGoogleOAuthState(inviteCode, redirectURL string) (string, string, error) {
	nonce, err := generateSecureStateToken(32)
	if err != nil {
		return "", "", err
	}

	payload := googleOAuthState{
		Nonce:       nonce,
		InviteCode:  inviteCode,
		RedirectURL: redirectURL,
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", "", err
	}

	return base64.RawURLEncoding.EncodeToString(encoded), nonce, nil
}

func parseGoogleOAuthState(encodedState string) (*googleOAuthState, error) {
	if encodedState == "" {
		return nil, fmt.Errorf("empty state")
	}

	raw, err := base64.RawURLEncoding.DecodeString(encodedState)
	if err != nil {
		return nil, err
	}

	var payload googleOAuthState
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}

	if payload.Nonce == "" {
		return nil, fmt.Errorf("missing state nonce")
	}

	return &payload, nil
}

func isProductionEnv() bool {
	env := globals.GraConf.System.Env
	return env == "production" || env == "prod"
}

func getSharedCookieDomain() string {
	if !isProductionEnv() {
		return ""
	}

	frontendURL := strings.TrimSpace(globals.GraConf.System.FrontendURL)
	if frontendURL == "" {
		return ""
	}

	parsed, err := url.Parse(frontendURL)
	if err != nil {
		return ""
	}

	host := strings.ToLower(parsed.Hostname())
	host = strings.TrimPrefix(host, "www.")
	if host == "" || !strings.Contains(host, ".") {
		return ""
	}

	return "." + host
}

func setHttpOnlyCookie(c *gin.Context, name, value string, maxAge int) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(name, value, maxAge, "/", getSharedCookieDomain(), isProductionEnv(), true)
}

func clearHttpOnlyCookie(c *gin.Context, name string) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(name, "", -1, "/", getSharedCookieDomain(), isProductionEnv(), true)
}

func extractLocalePrefix(redirectURL string) string {
	if redirectURL == "" {
		return ""
	}

	pathname := redirectURL
	if strings.HasPrefix(redirectURL, "http://") || strings.HasPrefix(redirectURL, "https://") {
		parsed, err := url.Parse(redirectURL)
		if err != nil {
			return ""
		}
		pathname = parsed.Path
	}

	pathname = strings.TrimPrefix(pathname, "/")
	if pathname == "" {
		return ""
	}

	parts := strings.Split(pathname, "/")
	if len(parts) == 0 {
		return ""
	}

	segment := strings.ToLower(parts[0])
	if len(segment) == 2 {
		return segment
	}
	if len(segment) == 5 && segment[2] == '-' {
		return segment
	}
	return ""
}

func buildFrontendLoginURL(errorCode, redirectURL string) string {
	base := strings.TrimRight(globals.GraConf.System.FrontendURL, "/")
	loginPath := "/login"

	if locale := extractLocalePrefix(redirectURL); locale != "" {
		loginPath = "/" + locale + "/login"
	}

	values := url.Values{}
	if errorCode != "" {
		values.Set("error", errorCode)
	}

	if redirectURL != "" {
		allowedDomain := utils.ExtractDomainFromURL(globals.GraConf.System.FrontendURL)
		if validatedURL, ok := utils.ValidateRedirectURL(redirectURL, allowedDomain); ok {
			values.Set("redirect", validatedURL)
		}
	}

	if encoded := values.Encode(); encoded != "" {
		return base + loginPath + "?" + encoded
	}

	return base + loginPath
}

func redirectGoogleAuthFailure(c *gin.Context, errorCode, redirectURL string) {
	c.Redirect(http.StatusTemporaryRedirect, buildFrontendLoginURL(errorCode, redirectURL))
}

// GoogleLogin 谷歌登录
// @Tags 系统用户
// @Summary 谷歌登录
// @Produce  application/json
// @Success 200 {string} string "{"code":200,"data":{},"msg":"成功"}"
// @Router /auth/google [get]
func (a *AuthApi) GoogleLogin(c *gin.Context) {
	inviteCode := c.Query("inviteCode")
	redirectUrl := c.Query("redirect")

	var googleOauthConfig = &oauth2.Config{
		RedirectURL:  globals.GraConf.Google.RedirectURL,
		ClientID:     globals.GraConf.Google.ClientID,
		ClientSecret: globals.GraConf.Google.ClientSecret,
		Scopes:       []string{"https://www.googleapis.com/auth/userinfo.email", "https://www.googleapis.com/auth/userinfo.profile"},
		Endpoint:     google.Endpoint,
	}

	if redirectUrl != "" {
		allowedDomain := utils.ExtractDomainFromURL(globals.GraConf.System.FrontendURL)
		validatedRedirect, isValid := utils.ValidateRedirectURL(redirectUrl, allowedDomain)
		if isValid {
			redirectUrl = validatedRedirect
		} else {
			globals.GraLog.Warn("Invalid redirect URL blocked before Google OAuth",
				zap.String("redirect_url", redirectUrl))
			redirectUrl = ""
		}
	}

	state, nonce, err := buildGoogleOAuthState(inviteCode, redirectUrl)
	if err != nil {
		globals.GraLog.Error("Failed to generate Google OAuth state", zap.Error(err))
		response.FailWithMessage("Failed to initialize Google login", c)
		return
	}

	setHttpOnlyCookie(c, "oauth_state", nonce, 600)

	url := googleOauthConfig.AuthCodeURL(state, oauth2.AccessTypeOffline)
	globals.GraLog.Info("Generated Google login URL:",
		zap.String("URL", url),
		zap.String("redirect", redirectUrl))
	response.OkWithData(url, c)
}

// GoogleCallback 谷歌登录回调
// @Tags 系统用户
// @Summary 谷歌登录回调
// @Produce  application/json
// @Param state query string true "State token"
// @Param code query string true "Authorization code"
// @Success 200 {string} string "{"code":200,"data":{},"msg":"登录成功"}"
// @Router /auth/google/callback [get]
func (a *AuthApi) GoogleCallback(c *gin.Context) {
	state := c.Query("state")
	statePayload, err := parseGoogleOAuthState(state)
	if err != nil {
		globals.GraLog.Warn("Invalid Google OAuth state payload", zap.Error(err))
		clearHttpOnlyCookie(c, "oauth_state")
		redirectGoogleAuthFailure(c, "invalid_google_state", "")
		return
	}

	redirectUrl := statePayload.RedirectURL
	inviteCode := statePayload.InviteCode

	expectedState, cookieErr := c.Cookie("oauth_state")
	clearHttpOnlyCookie(c, "oauth_state")
	if cookieErr != nil || expectedState == "" || subtle.ConstantTimeCompare([]byte(expectedState), []byte(statePayload.Nonce)) != 1 {
		globals.GraLog.Warn("Google OAuth state validation failed",
			zap.Error(cookieErr),
			zap.String("redirect", redirectUrl))
		redirectGoogleAuthFailure(c, "invalid_google_state", redirectUrl)
		return
	}

	if oauthError := c.Query("error"); oauthError != "" {
		globals.GraLog.Warn("Google OAuth returned error",
			zap.String("error", oauthError),
			zap.String("redirect", redirectUrl))
		redirectGoogleAuthFailure(c, "google_oauth_failed", redirectUrl)
		return
	}

	inviteUser := model.User{}
	if inviteCode != "" {
		globals.GraDBs["system"].Where("invite_code = ?", inviteCode).First(&inviteUser)
	}

	code := c.Query("code")
	if code == "" {
		globals.GraLog.Warn("Google OAuth callback missing code", zap.String("redirect", redirectUrl))
		redirectGoogleAuthFailure(c, "google_oauth_failed", redirectUrl)
		return
	}

	var googleOauthConfig = &oauth2.Config{
		RedirectURL:  globals.GraConf.Google.RedirectURL,
		ClientID:     globals.GraConf.Google.ClientID,
		ClientSecret: globals.GraConf.Google.ClientSecret,
		Scopes:       []string{"https://www.googleapis.com/auth/userinfo.email", "https://www.googleapis.com/auth/userinfo.profile"},
		Endpoint:     google.Endpoint,
	}

	token, err := googleOauthConfig.Exchange(c, code)
	if err != nil {
		globals.GraLog.Error("Code exchange failed:", zap.Error(err))
		redirectGoogleAuthFailure(c, "google_oauth_failed", redirectUrl)
		return
	}

	googleUser, err := getGoogleUserInfo(token)
	if err != nil {
		globals.GraLog.Error("Failed to get Google user info:", zap.Error(err))
		redirectGoogleAuthFailure(c, "google_oauth_failed", redirectUrl)
		return
	}

	// Check if user exists, if not create a new user
	var user model.User
	if err := globals.GraDBs["system"].Where("email = ?", googleUser.Email).First(&user).Error; err != nil {
		// User doesn't exist, create a new one
		if len(googleUser.Picture) < 255 {
			user.Avatar = googleUser.Picture
		}

		user = model.User{
			Email:    googleUser.Email,
			Nickname: googleUser.Name,
			Avatar:   googleUser.Picture,
		}
		// Set default values for the user
		randomPassword := utils.GenerateRandomPassword(10)
		user.Password = utils.CalculateMD5Hash(randomPassword)
		user.Role = "user"
		user.AuthEmail = 1
		user.LoginTime = time.Now()
		user.LoginIP = utils.GetClientIP(c.Request)
		user.LoginAddress = utils.GetClientAddress(user.LoginIP)
		user.InviteCode = utils.GenerateInviteCode() // 生成用户专属邀请码
		// Set creation time
		user.CreatedAt = time.Now()
		user.UpdatedAt = time.Now()
		if inviteUser.Id > 0 {
			user.InviteUID = int(inviteUser.Id)
		}
		if err := globals.GraDBs["system"].Omit("ban_expire_time", "member_start_time", "member_end_time").Create(&user).Error; err != nil {
			globals.GraLog.Error("Failed to create user:", zap.Error(err))
			response.FailWithMessage("Failed to create user", c)
			return
		}
		if err := (&account.AccountService{}).GrantSignupBonus(int(user.Id)); err != nil {
			globals.GraLog.Error("Failed to grant signup bonus", zap.Error(err))
		}
		if inviteUser.Id > 0 {
			go func(inviterID, inviteeID int) {
				// 使用新的 Credits 奖励系统
				accountService := account.AccountService{}
				accountService.GrantInviteReward(inviterID, inviteeID)
			}(int(inviteUser.Id), int(user.Id))
		}
	} else {
		user.LoginTime = time.Now()
		user.LoginIP = utils.GetClientIP(c.Request)
		user.LoginAddress = utils.GetClientAddress(user.LoginIP)
		if err := globals.GraDBs["system"].Model(&user).Updates(map[string]interface{}{
			"updated_at":    user.LoginTime,
			"login_time":    user.LoginTime,
			"login_ip":      user.LoginIP,
			"login_address": user.LoginAddress,
		}).Error; err != nil {
			globals.GraLog.Error("Failed to update user:", zap.Error(err))
			response.FailWithMessage("Failed to update user", c)
			return
		}
	}

	deviceType := c.Request.Header.Get("Sec-ch-ua-platform")
	deviceName := c.Request.Header.Get("Sec-ch-ua")
	LoginHis := model.LoginHis{
		UID:        int(user.Id),
		IP:         user.LoginIP,
		DeviceType: deviceType,
		DeviceName: deviceName,
		LoginTime:  time.Now(),
		Location:   user.LoginAddress,
	}
	LoginHis.CreatedAt = time.Now()
	LoginHis.UpdatedAt = time.Now()
	if err := globals.GraDBs["system"].Create(&LoginHis).Error; err != nil {
		globals.GraLog.Error("Failed to create login history", zap.Error(err))
		response.FailWithMessage("Failed to create login history", c)
		return
	}

	// ✨ 优化：使用统一的 generateLoginResponse 生成 JWT
	loginResponse, jwtToken, err := a.generateLoginResponse(user)
	if err != nil {
		globals.GraLog.Error("Failed to create JWT:", zap.Error(err))
		c.Redirect(http.StatusTemporaryRedirect,
			globals.GraConf.System.FrontendURL+"?error=token_generation_failed")
		return
	}

	// 计算 Cookie 有效期（与 JWT 过期时间一致）
	maxAge := int(loginResponse.ExpiresAt - time.Now().Unix())

	// 设置 HttpOnly Cookie（防止 XSS 攻击）
	setHttpOnlyCookie(c, "access_token", jwtToken, maxAge)

	// 记录日志
	globals.GraLog.Info("Google login successful with HttpOnly Cookie",
		zap.String("email", user.Email),
		zap.Uint("user_id", user.Id),
		zap.Bool("is_production", isProductionEnv()),
		zap.String("redirect_url", redirectUrl))

	// ✨ 安全增强：验证重定向URL，防止Open Redirect漏洞
	targetUrl := globals.GraConf.System.FrontendURL
	if redirectUrl != "" {
		// 提取允许的域名
		allowedDomain := utils.ExtractDomainFromURL(globals.GraConf.System.FrontendURL)

		// 验证重定向URL的安全性
		validatedURL, isValid := utils.ValidateRedirectURL(redirectUrl, allowedDomain)

		if isValid {
			// 如果是相对路径，拼接完整URL
			if strings.HasPrefix(validatedURL, "/") {
				targetUrl = globals.GraConf.System.FrontendURL + validatedURL
			} else {
				// 如果是完整URL（已验证同域名）
				targetUrl = validatedURL
			}
			globals.GraLog.Info("Validated redirect URL",
				zap.String("original", redirectUrl),
				zap.String("validated", validatedURL),
				zap.String("target", targetUrl))
		} else {
			// 不合法的URL，记录警告并使用默认URL
			globals.GraLog.Warn("Invalid redirect URL blocked",
				zap.String("redirect_url", redirectUrl),
				zap.String("user_email", user.Email),
				zap.String("user_ip", user.LoginIP))
		}
	}

	c.Redirect(http.StatusTemporaryRedirect, targetUrl)
}

func getGoogleUserInfo(token *oauth2.Token) (*GoogleUserInfo, error) {
	var googleOauthConfig = &oauth2.Config{
		RedirectURL:  globals.GraConf.Google.RedirectURL,
		ClientID:     globals.GraConf.Google.ClientID,
		ClientSecret: globals.GraConf.Google.ClientSecret,
		Scopes:       []string{"https://www.googleapis.com/auth/userinfo.email", "https://www.googleapis.com/auth/userinfo.profile"},
		Endpoint:     google.Endpoint,
	}
	client := googleOauthConfig.Client(oauth2.NoContext, token)
	resp, err := client.Get("https://www.googleapis.com/oauth2/v3/userinfo")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var userInfo GoogleUserInfo
	if err := json.Unmarshal(data, &userInfo); err != nil {
		return nil, err
	}

	return &userInfo, nil
}

type GoogleUserInfo struct {
	Email   string `json:"email"`
	Name    string `json:"name"`
	Picture string `json:"picture"`
}

// generateLoginResponse 生成登录响应（统一方法）
func (a *AuthApi) generateLoginResponse(user model.User) (*systemRes.LoginResponse, string, error) {
	// 生成 JWT token
	j := &utils.JWT{SigningKey: []byte(globals.GraConf.JWT.SigningKey)}
	claims := j.CreateClaims(systemReq.BaseClaims{
		Id:       user.Id,
		Email:    user.Email,
		Nickname: user.Nickname,
	})
	token, err := j.CreateToken(claims)
	if err != nil {
		return nil, "", err
	}

	// 计算会员状态。取值与 /api/account/quota 的 memberStatus 同词表
	// （active / expired / free）。统一 member 枚举之前这里默认 "active"，
	// 于是刚注册、member=0 的用户也会被登录响应报成 active 会员。
	memberStatus := "free"
	if user.Member > model.MEMBER_SUBSCRIPTION_FREE {
		if model.IsActivePaidMember(user.Member, user.MemberEndTime, time.Now()) {
			memberStatus = "active"
		} else {
			memberStatus = "expired"
		}
	}

	// 计算权限
	authority := []string{"user"}
	if user.Role == "manager" {
		authority = append(authority, "admin")
	}

	// 构建响应
	loginResponse := &systemRes.LoginResponse{
		User: systemRes.LoginUserInfo{
			Email:     user.Email,
			Nickname:  user.Nickname,
			Id:        user.Id,
			Avatar:    user.Avatar,
			AuthEmail: user.AuthEmail,
			Authority: authority,
			Membership: systemRes.MembershipResponse{
				Member:       strconv.FormatInt(int64(user.Member), 10),
				MemberStatus: memberStatus,
				MemberStart:  user.MemberStartTime.Format(time.DateTime),
				MemberEnd:    user.MemberEndTime.Format(time.DateTime),
			},
		},
		Token:     token,
		ExpiresAt: claims.ExpiresAt,
	}

	return loginResponse, token, nil
}

// Signin 登录
// @Tags 系统用户
// @Summary 用户登录
// @Produce  application/json
// @Param data body systemReq.Signin true "用户名和密码"
// @Success 200 {string} string "{"code":200,"data":{},"msg":"登录成功"}"
// @Router /base/signin [post]
func (a *AuthApi) Signin(c *gin.Context) {
	var L systemReq.Login
	_ = c.ShouldBindJSON(&L)

	var user model.User
	if err := globals.GraDBs["system"].Where("email = ?", L.Email).First(&user).Error; err != nil {
		response.FailWithMessage("User does not exist", c)
		return
	}

	if ok := utils.Md5Check(L.Password, user.Password); !ok {
		response.FailWithMessage("Incorrect password", c)
		return
	}
	user.LoginTime = time.Now()
	user.LoginIP = utils.GetClientIP(c.Request)
	user.LoginAddress = utils.GetClientAddress(user.LoginIP)
	if err := globals.GraDBs["system"].Model(&user).Updates(map[string]interface{}{
		"login_time":    time.Now(),
		"login_ip":      utils.GetClientIP(c.Request),
		"login_address": utils.GetClientAddress(user.LoginIP),
	}).Error; err != nil {
		response.FailWithMessage("Failed to update user", c)
		return
	}
	deviceType := c.Request.Header.Get("Sec-ch-ua-platform")
	deviceName := c.Request.Header.Get("Sec-ch-ua")
	LoginHis := model.LoginHis{
		UID:        int(user.Id),
		IP:         user.LoginIP,
		DeviceType: deviceType,
		DeviceName: deviceName,
		LoginTime:  time.Now(),
		Location:   user.LoginAddress,
	}
	LoginHis.CreatedAt = time.Now()
	LoginHis.UpdatedAt = time.Now()
	if err := globals.GraDBs["system"].Create(&LoginHis).Error; err != nil {
		globals.GraLog.Error("Failed to create login history", zap.Error(err))
		response.FailWithMessage("Failed to create login history", c)
		return
	}

	a.TokenSet(c, user)
}

// TokenSet 签发token（调用 generateLoginResponse）
// ✨ 优化：使用统一的 generateLoginResponse 方法
// ✨ 新增：同时设置 HttpOnly Cookie（增强安全性）
func (a *AuthApi) TokenSet(c *gin.Context, user model.User) {
	loginResponse, jwtToken, err := a.generateLoginResponse(user)
	if err != nil {
		globals.GraLog.Error("获取token失败!", zap.Error(err))
		response.FailWithMessage("获取token失败", c)
		return
	}

	// 设置 HttpOnly Cookie（与 GoogleCallback 保持一致）
	maxAge := int(loginResponse.ExpiresAt - time.Now().Unix())
	setHttpOnlyCookie(c, "access_token", jwtToken, maxAge)

	globals.GraLog.Info("User login successful with HttpOnly Cookie",
		zap.String("email", user.Email),
		zap.Uint("user_id", user.Id),
		zap.Bool("is_production", isProductionEnv()))

	response.OkWithDetailed(*loginResponse, fmt.Sprintf("%s,欢迎回来", user.Nickname), c)
}

// SignUp 注册
// @Tags 系统用户
// @Summary 注册
// @Produce  application/json
// @Param data body systemReq.SignUp true "用户名和密码"
// @Success 200 {string} string "{"code":200,"data":{},"msg":"注册成功"}"
// @Router /base/sign-up [post]
func (a *AuthApi) SignUp(c *gin.Context) {
	var L systemReq.SignUp
	_ = c.ShouldBindJSON(&L)

	if L.Email == "" {
		response.FailWithMessage("Email is required", c)
		return
	}
	if err := globals.GraDBs["system"].Where("email = ?", L.Email).First(&model.User{}).Error; err == nil {
		response.FailWithMessage("This email is already registered. Please use another email or sign in.", c)
		return
	}
	// Set default values for the user
	user := model.User{}
	user.Email = L.Email
	user.Password = utils.CalculateMD5Hash(L.Password)
	user.Avatar = fmt.Sprintf("/img/avatars/thumb-%d.jpg", rand.Intn(16)+1)
	user.Nickname = strings.Split(user.Email, "@")[0] // Use email prefix as default nickname if not provided
	user.Role = "user"
	user.AuthEmail = 0
	user.LoginTime = time.Now()
	user.CreatedAt = time.Now()
	user.UpdatedAt = time.Now()
	user.LoginIP = utils.GetClientIP(c.Request)
	user.LoginAddress = utils.GetClientAddress(user.LoginIP)
	user.InviteCode = utils.GenerateInviteCode() // 生成用户专属邀请码

	var inviteUser model.User
	if L.InviteCode != "" {
		if err := globals.GraDBs["system"].Where("invite_code = ?", L.InviteCode).First(&inviteUser).Error; err != nil {
			response.FailWithMessage("Invite code is invalid", c)
			return
		}
		user.InviteUID = int(inviteUser.Id)
	}

	if err := globals.GraDBs["system"].Create(&user).Error; err != nil {
		response.FailWithMessage("Registration failed", c)
		return
	}
	if err := (&account.AccountService{}).GrantSignupBonus(int(user.Id)); err != nil {
		globals.GraLog.Error("Failed to grant signup bonus", zap.Error(err))
	}

	a.TokenSet(c, user)

	// 发送验证邮件（异步）
	go func(email string) {
		err := utils.SendVerificationEmail(email)
		if err != nil {
			globals.GraLog.Error("Failed to send verification email",
				zap.String("email", email),
				zap.Error(err))
		}
	}(user.Email)

	// 触发欢迎邮件自动化规则（异步）
	go func(uid uint, nickname, email string) {
		automationService := service.GroupServiceApp.MarketingServiceGroup.EmailAutomationService
		if err := automationService.TriggerUserRegister(int(uid), nickname, email); err != nil {
			globals.GraLog.Error("Failed to trigger user register automation",
				zap.Uint("uid", uid),
				zap.String("email", email),
				zap.Error(err))
		}
	}(user.Id, user.Nickname, user.Email)

	//需要迁移到激活的时候奖励
	if inviteUser.Id > 0 {
		go func(inviterID, inviteeID int) {
			// 使用新的 Credits 奖励系统
			accountService := account.AccountService{}
			accountService.GrantInviteReward(inviterID, inviteeID)
		}(int(inviteUser.Id), int(user.Id))
	}
}

// SignOut 登出
// @Tags 系统用户
// @Summary 登出
// @Produce  application/json
// @Security ApiKeyAuth
// @Success 200 {string} string "{"code":200,"data":{},"msg":"登出成功"}"
// @Router /base/sign-out [post]
func (a *AuthApi) SignOut(c *gin.Context) {
	clearHttpOnlyCookie(c, "access_token")

	globals.GraLog.Info("User signed out, Cookie cleared")
	response.OkWithMessage("登出成功", c)
}

// ForgotPassword 忘记密码
// @Tags 系统用户
// @Summary 忘记密码
// @Produce  application/json
// @Param data body systemReq.ForgotPassword true "忘记密码"
// @Success 200 {string} string "{"code":200,"data":{},"msg":"密码重置邮件已发送"}"
// @Router /base/forgot-password [post]
func (a *AuthApi) ForgotPassword(c *gin.Context) {
	var request systemReq.ForgotPassword
	if err := c.ShouldBindJSON(&request); err != nil {
		response.FailWithMessage("Invalid request", c)
		return
	}

	var user model.User
	err := globals.GraDBs["system"].Where("email = ?", request.Email).First(&user).Error
	if err != nil {
		response.FailWithMessage("User not found", c)
		return
	}

	// Send reset password email asynchronously
	go func() {
		err := utils.SendResetPasswordEmail(user.Id, user.Email)
		if err != nil {
			globals.GraLog.Error("Failed to send reset password email", zap.Error(err))
		}
	}()

	response.OkWithMessage("Password reset email sent", c)
}

// ChangePassword 修改密码, 通过token修改密码, 用于忘记密码
// @Tags 系统用户
// @Summary 修改密码
// @Produce  application/json
// @Security ApiKeyAuth
// @Param oldPassword query string true "旧密码"
// @Param newPassword query string true "新密码"
// @Success 200 {string} string "{"code":200,"data":{},"msg":"修改成功"}"
// @Router /user/changePassword [post]
func (a *AuthApi) ChangePassword(c *gin.Context) {
	var chnagePwd request.ChangePassword
	if err := c.ShouldBindJSON(&chnagePwd); err != nil {
		response.FailWithMessage("Invalid request", c)
		return
	}
	userID, ok := globals.GraCache.Get(fmt.Sprintf("resetToken-%s", chnagePwd.Token))
	if !ok {
		response.FailWithMessage("Token is invalid or expired", c)
		return
	}
	if err := service.GroupServiceApp.AccountServiceGroup.AccountService.ChangePassword(userID.(uint), chnagePwd); err != nil {
		response.FailWithMessage(err.Error(), c)
		return
	}
	response.OkWithMessage("修改成功", c)
}

// GetSession 获取session
func (a *AuthApi) GetSession(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("User not found", c)
		return
	}
	user, err := service.GroupServiceApp.AccountServiceGroup.AccountService.GetUserInfo(uid)
	if err != nil {
		response.FailWithMessage("User not found", c)
		return
	}
	response.OkWithData(user, c)
}

// GetAccessToken 从 Cookie 中提取 access token（用于 localStorage 同步）
// 主要用于 Google OAuth 登录后，前端同步 token 到 localStorage
// @Tags 系统用户
// @Summary 获取访问令牌
// @Produce  application/json
// @Security ApiKeyAuth
// @Success 200 {object} response.Response{data=gin.H} "{"code":200,"data":{"token":"..."},"msg":"获取成功"}"
// @Router /auth/token [get]
func (a *AuthApi) GetAccessToken(c *gin.Context) {
	// 验证用户是否已登录
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}

	// 从 Cookie 中提取 token
	token, err := c.Cookie("access_token")
	if err != nil || token == "" {
		globals.GraLog.Warn("No access_token cookie found for user", zap.Uint("uid", uid))
		response.FailWithMessage("No token found", c)
		return
	}

	globals.GraLog.Info("Access token retrieved for localStorage sync", zap.Uint("uid", uid))
	response.OkWithData(gin.H{"token": token}, c)
}
